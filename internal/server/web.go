package server

import (
	"embed"
	"html/template"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/malico/docker-release/internal/state"
)

//go:embed assets
var assets embed.FS

var tmpl = template.Must(template.New("").ParseFS(assets, "assets/index.html", "assets/partials.html"))

type serviceView struct {
	Service              string
	StatusClass          string
	StatusLabel          string
	Strategy             string
	CurrentWeight        int
	ActiveDeploymentID   string
	PreviousDeploymentID string
	UpdatedAtFmt         string
}

type pageData struct {
	Project  string
	Services []serviceView
}

func (s *Server) webMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", s.handleIndex)
	mux.HandleFunc("GET /partials/services", s.handlePartialServices)
	return mux
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	states, err := s.listAll()
	if err != nil {
		slog.Error("web list failed", "component", "server", "err", err)
		states = nil
	}
	data := pageData{
		Project:  s.project,
		Services: toServiceViews(states),
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmpl.ExecuteTemplate(w, "index.html", data); err != nil {
		slog.Error("web render failed", "component", "server", "err", err)
	}
}

func (s *Server) handlePartialServices(w http.ResponseWriter, r *http.Request) {
	states, err := s.listAll()
	if err != nil {
		slog.Error("partial list failed", "component", "server", "err", err)
		states = nil
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmpl.ExecuteTemplate(w, "rows", toServiceViews(states)); err != nil {
		slog.Error("partial render failed", "component", "server", "err", err)
	}
}

func toServiceViews(states []*state.DeploymentState) []serviceView {
	views := make([]serviceView, 0, len(states))
	for _, ds := range states {
		views = append(views, toServiceView(ds))
	}
	return views
}

func toServiceView(ds *state.DeploymentState) serviceView {
	stale := ds.IsStale(state.DefaultStaleThreshold)
	updFmt := ""
	if !ds.UpdatedAt.IsZero() {
		updFmt = ds.UpdatedAt.UTC().Format(time.RFC3339)
	}
	return serviceView{
		Service:              ds.Service,
		StatusClass:          statusClass(string(ds.Status), stale),
		StatusLabel:          statusLabel(string(ds.Status), stale),
		Strategy:             ds.Strategy,
		CurrentWeight:        ds.CurrentWeight,
		ActiveDeploymentID:   ds.ActiveDeploymentID,
		PreviousDeploymentID: ds.PreviousDeploymentID,
		UpdatedAtFmt:         updFmt,
	}
}

func statusClass(status string, stale bool) string {
	if stale {
		return "stale"
	}
	return status
}

func statusLabel(status string, stale bool) string {
	label := strings.ReplaceAll(status, "_", " ")
	if stale {
		label += " (stale)"
	}
	return label
}
