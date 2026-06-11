package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/malico/docker-release/internal/controller"
	"github.com/malico/docker-release/internal/state"
)

func newTestServer(dir, project string) *Server {
	mgr := state.NewManager(dir, project)
	ctrl := controller.New(nil, mgr, project)
	return &Server{cfg: Config{}, mgr: mgr, ctrl: ctrl, project: project}
}

func TestConfigFromEnvDefaults(t *testing.T) {
	os.Unsetenv("DR_EXPOSE_API")
	os.Unsetenv("DR_EXPOSE_WEB")
	os.Unsetenv("DR_API_PORT")
	os.Unsetenv("DR_WEB_PORT")
	os.Unsetenv("DR_BIND_ADDR")

	cfg := ConfigFromEnv()

	if cfg.APIEnabled {
		t.Error("APIEnabled should default to false")
	}
	if cfg.WebEnabled {
		t.Error("WebEnabled should default to false")
	}
	if cfg.APIPort != 9080 {
		t.Errorf("APIPort = %d, want 9080", cfg.APIPort)
	}
	if cfg.WebPort != 9081 {
		t.Errorf("WebPort = %d, want 9081", cfg.WebPort)
	}
	if cfg.BindAddr != "0.0.0.0" {
		t.Errorf("BindAddr = %s, want 0.0.0.0", cfg.BindAddr)
	}
}

func TestConfigFromEnvOverrides(t *testing.T) {
	t.Setenv("DR_EXPOSE_API", "true")
	t.Setenv("DR_EXPOSE_WEB", "1")
	t.Setenv("DR_API_PORT", "8080")
	t.Setenv("DR_WEB_PORT", "8081")
	t.Setenv("DR_BIND_ADDR", "127.0.0.1")

	cfg := ConfigFromEnv()

	if !cfg.APIEnabled {
		t.Error("APIEnabled should be true")
	}
	if !cfg.WebEnabled {
		t.Error("WebEnabled should be true")
	}
	if cfg.APIPort != 8080 {
		t.Errorf("APIPort = %d, want 8080", cfg.APIPort)
	}
	if cfg.WebPort != 8081 {
		t.Errorf("WebPort = %d, want 8081", cfg.WebPort)
	}
	if cfg.BindAddr != "127.0.0.1" {
		t.Errorf("BindAddr = %s, want 127.0.0.1", cfg.BindAddr)
	}
}

func TestEnvBool(t *testing.T) {
	cases := []struct {
		val  string
		want bool
	}{
		{"true", true},
		{"1", true},
		{"yes", true},
		{"false", false},
		{"0", false},
		{"", false},
		{"True", false},
	}
	for _, c := range cases {
		t.Setenv("TEST_BOOL_KEY", c.val)
		if got := envBool("TEST_BOOL_KEY"); got != c.want {
			t.Errorf("envBool(%q) = %v, want %v", c.val, got, c.want)
		}
	}
}

func TestHandleHealth(t *testing.T) {
	srv := &Server{cfg: Config{Version: "test-1.0"}, mgr: state.NewManager(t.TempDir(), ""), ctrl: controller.New(nil, state.NewManager(t.TempDir(), ""), "")}
	mux := srv.apiMux()

	req := httptest.NewRequest("GET", "/api/health", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}

	var body map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["ok"] != true {
		t.Errorf("ok = %v, want true", body["ok"])
	}
	if body["version"] != "test-1.0" {
		t.Errorf("version = %v, want test-1.0", body["version"])
	}
}

func TestHandleServicesEmpty(t *testing.T) {
	srv := newTestServer(t.TempDir(), "proj")
	mux := srv.apiMux()

	req := httptest.NewRequest("GET", "/api/services", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}

	var body []ServiceInfo
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body) != 0 {
		t.Errorf("len = %d, want 0", len(body))
	}
}

func TestBuildServiceList(t *testing.T) {
	dir := t.TempDir()
	mgr := state.NewManager(dir, "proj")

	_ = mgr.Save(&state.DeploymentState{
		Service:              "web",
		Status:               state.StatusInProgress,
		Strategy:             "canary",
		CurrentWeight:        25,
		ActiveDeploymentID:   "deploy_abc",
		PreviousDeploymentID: "deploy_xyz",
	})
	_ = mgr.Save(&state.DeploymentState{
		Service: "worker",
		Status:  state.StatusIdle,
	})

	states, err := mgr.ListAll()
	if err != nil {
		t.Fatalf("ListAll: %v", err)
	}
	if len(states) != 2 {
		t.Fatalf("len(states) = %d, want 2", len(states))
	}

	active := map[string]string{"web": "deploy_abc"}
	infos := buildServiceList(states, active, nil)

	byService := make(map[string]ServiceInfo)
	for _, i := range infos {
		byService[i.Service] = i
	}

	web := byService["web"]
	if web.Status != "in_progress" {
		t.Errorf("web status = %s, want in_progress", web.Status)
	}
	if !web.InProgress {
		t.Error("web.InProgress should be true")
	}
	if web.CurrentWeight != 25 {
		t.Errorf("web weight = %d, want 25", web.CurrentWeight)
	}
	if web.ActiveDeploymentID != "deploy_abc" {
		t.Errorf("web active = %s, want deploy_abc", web.ActiveDeploymentID)
	}

	worker := byService["worker"]
	if worker.Status != "idle" {
		t.Errorf("worker status = %s, want idle", worker.Status)
	}
	if worker.InProgress {
		t.Error("worker.InProgress should be false")
	}
}

func TestBuildServiceListStale(t *testing.T) {
	ds := &state.DeploymentState{
		Service:   "api",
		Status:    state.StatusInProgress,
		UpdatedAt: time.Now().Add(-2 * time.Hour),
	}
	infos := buildServiceList([]*state.DeploymentState{ds}, nil, nil)
	if len(infos) != 1 {
		t.Fatalf("len = %d, want 1", len(infos))
	}
	if !infos[0].Stale {
		t.Error("stale = false, want true")
	}
}

func TestWebIndexRoute(t *testing.T) {
	srv := newTestServer(t.TempDir(), "testproj")
	srv.project = "testproj"
	mux := srv.webMux()

	req := httptest.NewRequest("GET", "/", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "testproj") {
		t.Error("response missing project name")
	}
	if !strings.Contains(body, "htmx") {
		t.Error("response missing htmx script tag")
	}
}

func TestWebPartialRoute(t *testing.T) {
	dir := t.TempDir()
	srv := newTestServer(dir, "testproj")
	_ = srv.mgr.(*state.Manager).Save(&state.DeploymentState{
		Service: "web",
		Status:  state.StatusIdle,
	})
	mux := srv.webMux()

	req := httptest.NewRequest("GET", "/partials/services", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "web") {
		t.Error("response missing service name")
	}
}

func TestHandleDeploymentByID(t *testing.T) {
	dir := t.TempDir()
	srv := newTestServer(dir, "proj")
	_ = srv.mgr.(*state.Manager).Save(&state.DeploymentState{
		Service:              "web",
		Status:               state.StatusInProgress,
		Strategy:             "canary",
		CurrentWeight:        50,
		ActiveDeploymentID:   "deploy_abc",
		PreviousDeploymentID: "deploy_xyz",
	})
	mux := srv.apiMux()

	// Active deployment
	req := httptest.NewRequest("GET", "/api/deployments/deploy_abc", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var active DeploymentStatus
	if err := json.NewDecoder(rec.Body).Decode(&active); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if active.DeploymentID != "deploy_abc" {
		t.Errorf("deployment_id = %s, want deploy_abc", active.DeploymentID)
	}
	if active.Service != "web" {
		t.Errorf("service = %s, want web", active.Service)
	}
	if !active.IsActive {
		t.Error("is_active should be true for active deployment")
	}
	if active.CurrentWeight != 50 {
		t.Errorf("current_weight = %d, want 50", active.CurrentWeight)
	}

	// Previous deployment
	req2 := httptest.NewRequest("GET", "/api/deployments/deploy_xyz", nil)
	rec2 := httptest.NewRecorder()
	mux.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec2.Code)
	}
	var prev DeploymentStatus
	if err := json.NewDecoder(rec2.Body).Decode(&prev); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if prev.IsActive {
		t.Error("is_active should be false for previous deployment")
	}
	if prev.Status != "completed" {
		t.Errorf("status = %s, want completed", prev.Status)
	}

	// Unknown deployment
	req3 := httptest.NewRequest("GET", "/api/deployments/deploy_unknown", nil)
	rec3 := httptest.NewRecorder()
	mux.ServeHTTP(rec3, req3)
	if rec3.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec3.Code)
	}
}

func TestHandleCancelByDeploymentNotFound(t *testing.T) {
	srv := newTestServer(t.TempDir(), "")
	mux := srv.apiMux()

	req := httptest.NewRequest("POST", "/api/deployments/deploy_unknown/cancel", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestHandleCancelNoDeployment(t *testing.T) {
	srv := newTestServer(t.TempDir(), "")
	mux := srv.apiMux()

	req := httptest.NewRequest("POST", "/api/services/web/cancel", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestPendingCommandsInServiceList(t *testing.T) {
	dir := t.TempDir()
	mgr := state.NewManager(dir, "proj")
	_ = mgr.Save(&state.DeploymentState{Service: "web", Status: state.StatusIdle})
	_, _ = mgr.EnqueueReleaseCommand("web", false)
	_, _ = mgr.EnqueueReleaseCommand("web", true)

	states, _ := mgr.ListAll()
	cmds, _ := mgr.PendingReleaseCommands()
	pendingByService := make(map[string][]PendingCommand)
	for _, cmd := range cmds {
		pendingByService[cmd.Service] = append(pendingByService[cmd.Service], PendingCommand{
			ID: cmd.ID, Force: cmd.Force, CreatedAt: cmd.CreatedAt,
		})
	}
	infos := buildServiceList(states, nil, pendingByService)
	if len(infos) != 1 {
		t.Fatalf("len = %d, want 1", len(infos))
	}
	if len(infos[0].PendingCommands) != 2 {
		t.Errorf("pending = %d, want 2", len(infos[0].PendingCommands))
	}
	if !infos[0].PendingCommands[1].Force {
		t.Error("second command should be force=true")
	}
}

func TestWebNotFound(t *testing.T) {
	srv := newTestServer(t.TempDir(), "")
	mux := srv.webMux()

	req := httptest.NewRequest("GET", "/does-not-exist", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestRequireTokenRejectsMissingOrWrong(t *testing.T) {
	srv := newTestServer(t.TempDir(), "proj")
	srv.cfg.APIToken = "secret"
	mux := srv.apiMux()

	for _, auth := range []string{"", "Bearer wrong", "secret"} {
		req := httptest.NewRequest("POST", "/api/services/web/cancel", nil)
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("auth=%q: status = %d, want 401", auth, rec.Code)
		}
	}
}

func TestRequireTokenAcceptsValid(t *testing.T) {
	srv := newTestServer(t.TempDir(), "proj")
	srv.cfg.APIToken = "secret"
	mux := srv.apiMux()

	req := httptest.NewRequest("POST", "/api/services/web/cancel", nil)
	req.Header.Set("Authorization", "Bearer secret")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	// Valid token passes auth; 404 because no deployment is active.
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestHandleServiceUnknownReturns404(t *testing.T) {
	srv := newTestServer(t.TempDir(), "proj")
	mux := srv.apiMux()

	req := httptest.NewRequest("GET", "/api/services/ghost", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestHandleServiceInvalidNameReturns404(t *testing.T) {
	srv := newTestServer(t.TempDir(), "proj")
	mux := srv.apiMux()

	req := httptest.NewRequest("GET", "/api/services/"+url.PathEscape("../etc"), nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}
