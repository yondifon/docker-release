package server

import (
	"crypto/subtle"
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/malico/docker-release/internal/state"
)

type PendingCommand struct {
	ID        string    `json:"id"`
	Force     bool      `json:"force"`
	CreatedAt time.Time `json:"created_at"`
}

type ServiceInfo struct {
	Service              string           `json:"service"`
	Status               string           `json:"status"`
	Strategy             string           `json:"strategy,omitempty"`
	CurrentWeight        int              `json:"current_weight"`
	ActiveDeploymentID   string           `json:"active_deployment_id,omitempty"`
	PreviousDeploymentID string           `json:"previous_deployment_id,omitempty"`
	UpdatedAt            time.Time        `json:"updated_at"`
	InProgress           bool             `json:"in_progress"`
	Stale                bool             `json:"stale"`
	PendingCommands      []PendingCommand `json:"pending_commands,omitempty"`
}

// DeploymentStatus is the response for /api/deployments/{id}.
// IsActive means this deployment is the current ActiveDeploymentID for the service.
// InProgress means there is a live goroutine executing it right now.
type DeploymentStatus struct {
	DeploymentID  string    `json:"deployment_id"`
	Service       string    `json:"service"`
	Status        string    `json:"status"`
	Strategy      string    `json:"strategy,omitempty"`
	CurrentWeight int       `json:"current_weight"`
	IsActive      bool      `json:"is_active"`
	InProgress    bool      `json:"in_progress"`
	Stale         bool      `json:"stale"`
	UpdatedAt     time.Time `json:"updated_at"`
}

func (s *Server) apiMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", s.handleHealth)
	mux.HandleFunc("GET /api/services", s.handleServices)
	mux.HandleFunc("GET /api/services/{service}", s.handleService)
	mux.HandleFunc("POST /api/services/{service}/cancel", s.requireToken(s.handleCancelByService))
	mux.HandleFunc("GET /api/deployments/{id}", s.handleDeployment)
	mux.HandleFunc("POST /api/deployments/{id}/cancel", s.requireToken(s.handleCancelByDeployment))
	return mux
}

// requireToken guards mutating endpoints with a bearer token when
// Config.APIToken is set. Comparison is constant-time.
func (s *Server) requireToken(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.cfg.APIToken != "" {
			got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
			if !ok || subtle.ConstantTimeCompare([]byte(got), []byte(s.cfg.APIToken)) != 1 {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
		}
		next(w, r)
	}
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "version": s.cfg.Version})
}

func (s *Server) handleServices(w http.ResponseWriter, r *http.Request) {
	states, err := s.listAll()
	if err != nil {
		http.Error(w, "failed to load state", http.StatusInternalServerError)
		return
	}
	active := s.ctrl.ActiveDeployments()
	infos := buildServiceList(states, active, s.pendingCommandsByService())
	writeJSON(w, http.StatusOK, infos)
}

func (s *Server) handleService(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("service")
	ds, err := s.mgr.Load(name)
	if err != nil {
		// Load fails for invalid names (e.g. path traversal) before any I/O;
		// treat as not found rather than leaking internals.
		http.Error(w, "service not found", http.StatusNotFound)
		return
	}
	active := s.ctrl.ActiveDeployments()
	_, inProgress := active[name]
	if ds.UpdatedAt.IsZero() && !inProgress {
		http.Error(w, "service not found", http.StatusNotFound)
		return
	}
	info := toServiceInfo(ds, inProgress, s.pendingCommandsByService()[name])
	writeJSON(w, http.StatusOK, info)
}

func (s *Server) handleDeployment(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	status, err := s.findDeployment(id)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if status == nil {
		http.Error(w, "deployment not found", http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, status)
}

func (s *Server) handleCancelByDeployment(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	active := s.ctrl.ActiveDeployments()
	var service string
	for svc, deployID := range active {
		if deployID == id {
			service = svc
			break
		}
	}
	if service == "" {
		http.Error(w, "deployment not found or not in progress", http.StatusNotFound)
		return
	}
	s.ctrl.CancelDeployment(service)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "deployment_id": id, "service": service})
}

func (s *Server) handleCancelByService(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("service")
	if !s.ctrl.CancelDeployment(name) {
		http.Error(w, "no active deployment", http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "service": name})
}

// findDeployment scans all state files for the given deployment ID, matching
// both active and previous deployment IDs. Returns nil, nil when not found.
func (s *Server) findDeployment(id string) (*DeploymentStatus, error) {
	states, err := s.listAll()
	if err != nil {
		log.Printf("[server] findDeployment: %v", err)
		return nil, err
	}
	active := s.ctrl.ActiveDeployments()
	for _, ds := range states {
		switch id {
		case ds.ActiveDeploymentID:
			_, inProgress := active[ds.Service]
			return &DeploymentStatus{
				DeploymentID:  id,
				Service:       ds.Service,
				Status:        string(ds.Status),
				Strategy:      ds.Strategy,
				CurrentWeight: ds.CurrentWeight,
				IsActive:      true,
				InProgress:    inProgress,
				Stale:         ds.IsStale(state.DefaultStaleThreshold),
				UpdatedAt:     ds.UpdatedAt,
			}, nil
		case ds.PreviousDeploymentID:
			return &DeploymentStatus{
				DeploymentID: id,
				Service:      ds.Service,
				Status:       "completed",
				Strategy:     ds.Strategy,
				UpdatedAt:    ds.UpdatedAt,
			}, nil
		}
	}
	return nil, nil
}

func (s *Server) pendingCommandsByService() map[string][]PendingCommand {
	cmds, err := s.mgr.PendingReleaseCommands()
	if err != nil {
		log.Printf("[server] pending commands: %v", err)
		return map[string][]PendingCommand{}
	}
	out := make(map[string][]PendingCommand, len(cmds))
	for _, cmd := range cmds {
		out[cmd.Service] = append(out[cmd.Service], PendingCommand{
			ID:        cmd.ID,
			Force:     cmd.Force,
			CreatedAt: cmd.CreatedAt,
		})
	}
	return out
}

func buildServiceList(
	states []*state.DeploymentState,
	active map[string]string,
	pendingByService map[string][]PendingCommand,
) []ServiceInfo {
	out := make([]ServiceInfo, 0, len(states))
	for _, ds := range states {
		_, inProgress := active[ds.Service]
		out = append(out, toServiceInfo(ds, inProgress, pendingByService[ds.Service]))
	}
	return out
}

func toServiceInfo(ds *state.DeploymentState, inProgress bool, pending []PendingCommand) ServiceInfo {
	return ServiceInfo{
		Service:              ds.Service,
		Status:               string(ds.Status),
		Strategy:             ds.Strategy,
		CurrentWeight:        ds.CurrentWeight,
		ActiveDeploymentID:   ds.ActiveDeploymentID,
		PreviousDeploymentID: ds.PreviousDeploymentID,
		UpdatedAt:            ds.UpdatedAt,
		InProgress:           inProgress,
		Stale:                ds.IsStale(state.DefaultStaleThreshold),
		PendingCommands:      pending,
	}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("[server] encode response: %v", err)
	}
}
