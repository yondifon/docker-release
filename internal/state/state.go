package state

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

var validName = regexp.MustCompile(`^[a-zA-Z0-9._-]+$`)

func validateName(name string) error {
	if name == "" {
		return nil
	}
	if !validName.MatchString(name) {
		return fmt.Errorf("invalid name %q: must match [a-zA-Z0-9._-]+", name)
	}
	return nil
}

type Status string

const (
	StatusIdle        Status = "idle"
	StatusInProgress  Status = "in_progress"
	StatusRollingBack Status = "rolling_back"
)

const DefaultStaleThreshold = 30 * time.Minute

type DeploymentState struct {
	Service              string     `json:"service"`
	Status               Status     `json:"status"`
	Strategy             string     `json:"strategy"`
	CurrentWeight        int        `json:"current_weight"`
	ActiveDeploymentID   string     `json:"active_deployment_id"`
	PreviousDeploymentID string     `json:"previous_deployment_id"`
	Containers           Containers `json:"containers"`
	UpdatedAt            time.Time  `json:"updated_at"`
}

func (s *DeploymentState) IsStale(threshold time.Duration) bool {
	if s.Status != StatusInProgress && s.Status != StatusRollingBack {
		return false
	}

	if s.UpdatedAt.IsZero() {
		return true
	}

	return time.Since(s.UpdatedAt) > threshold
}

type Containers struct {
	Stable []string `json:"stable"`
	Canary []string `json:"canary"`
}

type Manager struct {
	dir     string
	project string

	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

func NewManager(dir, project string) *Manager {
	return &Manager{
		dir:     dir,
		project: project,
		locks:   make(map[string]*sync.Mutex),
	}
}

func (m *Manager) serviceLock(service string) *sync.Mutex {
	m.mu.Lock()
	defer m.mu.Unlock()
	mu, ok := m.locks[service]
	if !ok {
		mu = &sync.Mutex{}
		m.locks[service] = mu
	}
	return mu
}

func (m *Manager) Load(service string) (*DeploymentState, error) {
	if err := validateName(service); err != nil {
		return nil, err
	}
	if err := validateName(m.project); err != nil {
		return nil, err
	}

	path := m.path(service)

	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return &DeploymentState{
			Service: service,
			Status:  StatusIdle,
		}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading state file: %w", err)
	}

	var s DeploymentState
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("parsing state file: %w", err)
	}

	return &s, nil
}

func (m *Manager) Save(s *DeploymentState) error {
	if err := validateName(s.Service); err != nil {
		return err
	}
	if err := validateName(m.project); err != nil {
		return err
	}

	lock := m.serviceLock(s.Service)
	lock.Lock()
	defer lock.Unlock()

	s.UpdatedAt = time.Now()

	if err := os.MkdirAll(m.dir, 0o755); err != nil {
		return fmt.Errorf("creating state dir: %w", err)
	}

	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling state: %w", err)
	}

	tmp := m.path(s.Service) + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("writing temp state file: %w", err)
	}

	if err := os.Rename(tmp, m.path(s.Service)); err != nil {
		return fmt.Errorf("renaming state file: %w", err)
	}

	return nil
}

func GenerateDeploymentID() string {
	b := make([]byte, 4)
	rand.Read(b)
	return fmt.Sprintf("deploy_%s_%s", time.Now().Format("20060102150405"), hex.EncodeToString(b))
}

func (m *Manager) ListAll() ([]*DeploymentState, error) {
	entries, err := os.ReadDir(m.dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading state dir: %w", err)
	}

	prefix := ""
	if m.project != "" {
		prefix = m.project + "_"
	}
	const suffix = "_state.json"

	result := make([]*DeploymentState, 0)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, suffix) {
			continue
		}
		service := strings.TrimPrefix(name, prefix)
		service = strings.TrimSuffix(service, suffix)
		if service == "" {
			continue
		}
		s, err := m.Load(service)
		if err != nil {
			slog.Warn("skipping state file", "component", "state", "file", name, "err", err)
			continue
		}
		result = append(result, s)
	}
	return result, nil
}

func (m *Manager) path(service string) string {
	name := service
	if m.project != "" {
		name = m.project + "_" + service
	}
	return filepath.Join(m.dir, name+"_state.json")
}
