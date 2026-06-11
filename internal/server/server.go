package server

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/malico/docker-release/internal/state"
)

// Deployer is the slice of the controller the server needs.
type Deployer interface {
	ActiveDeployments() map[string]string
	CancelDeployment(service string) bool
}

// StateStore is the slice of the state manager the server needs.
type StateStore interface {
	Load(service string) (*state.DeploymentState, error)
	ListAll() ([]*state.DeploymentState, error)
	PendingReleaseCommands() ([]state.QueuedReleaseCommand, error)
}

type Config struct {
	BindAddr   string
	APIEnabled bool
	APIPort    int
	WebEnabled bool
	WebPort    int
	Version    string
	// APIToken, when set, is required as "Authorization: Bearer <token>"
	// on mutating API endpoints. Empty means no auth (trusted networks only).
	APIToken string
}

func ConfigFromEnv() Config {
	cfg := Config{
		BindAddr: envOr("DR_BIND_ADDR", "0.0.0.0"),
		APIPort:  envIntOr("DR_API_PORT", 9080),
		WebPort:  envIntOr("DR_WEB_PORT", 9081),
		APIToken: os.Getenv("DR_API_TOKEN"),
	}
	cfg.APIEnabled = envBool("DR_EXPOSE_API")
	cfg.WebEnabled = envBool("DR_EXPOSE_WEB")
	if cfg.APIEnabled && cfg.APIToken == "" {
		log.Printf("[server] WARNING: DR_API_TOKEN not set; cancel endpoints are unauthenticated")
	}
	return cfg
}

type Server struct {
	cfg     Config
	ctrl    Deployer
	mgr     StateStore
	project string

	// cache for ListAll: the web UI polls /partials/services and every API
	// list/find hits disk; a short TTL bounds I/O under polling load.
	cacheMu   sync.Mutex
	cacheAt   time.Time
	cacheList []*state.DeploymentState
}

// listCacheTTL bounds state-file reads under UI polling. Short enough that
// status changes appear within one poll cycle.
const listCacheTTL = time.Second

func New(cfg Config, ctrl Deployer, mgr StateStore, project string) *Server {
	return &Server{
		cfg:     cfg,
		ctrl:    ctrl,
		mgr:     mgr,
		project: project,
	}
}

// listAll returns all deployment states, served from a 1s cache.
func (s *Server) listAll() ([]*state.DeploymentState, error) {
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()
	if s.cacheList != nil && time.Since(s.cacheAt) < listCacheTTL {
		return s.cacheList, nil
	}
	states, err := s.mgr.ListAll()
	if err != nil {
		return nil, err
	}
	s.cacheList = states
	s.cacheAt = time.Now()
	return states, nil
}

// Start runs the enabled listeners and blocks until ctx is cancelled or a
// listener fails. Listener failures (e.g. port already in use) are returned.
func (s *Server) Start(ctx context.Context) error {
	var wg sync.WaitGroup
	errCh := make(chan error, 2)

	serve := func(port int, mux http.Handler) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := s.listenAndServe(ctx, port, mux); err != nil {
				errCh <- err
			}
		}()
	}

	if s.cfg.APIEnabled {
		serve(s.cfg.APIPort, s.apiMux())
	}
	if s.cfg.WebEnabled {
		serve(s.cfg.WebPort, s.webMux())
	}

	wg.Wait()
	close(errCh)
	var errs []error
	for err := range errCh {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

func (s *Server) listenAndServe(ctx context.Context, port int, mux http.Handler) error {
	addr := fmt.Sprintf("%s:%d", s.cfg.BindAddr, port)
	srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}

	go func() {
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutCtx); err != nil {
			log.Printf("[server] shutdown %s: %v", addr, err)
		}
	}()

	log.Printf("[server] listening on %s", addr)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Printf("[server] %s: %v", addr, err)
		return fmt.Errorf("listen %s: %w", addr, err)
	}
	return nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envIntOr(key string, fallback int) int {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 || n > 65535 {
		log.Printf("[server] invalid %s=%q, using %d", key, v, fallback)
		return fallback
	}
	return n
}

func envBool(key string) bool {
	v := os.Getenv(key)
	return v == "1" || v == "true" || v == "yes"
}
