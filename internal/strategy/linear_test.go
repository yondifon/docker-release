package strategy

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/malico/docker-release/internal/config"
	"github.com/malico/docker-release/internal/provider"
	"github.com/malico/docker-release/internal/state"
)

type mockDocker struct {
	healthyCalls []string
	stopCalls    []string
	removeCalls  []string
	healthErr    error
}

func (m *mockDocker) WaitHealthy(_ context.Context, containerID string, _ time.Duration) error {
	m.healthyCalls = append(m.healthyCalls, containerID)
	return m.healthErr
}

func (m *mockDocker) Stop(_ context.Context, containerID string, _ int) error {
	m.stopCalls = append(m.stopCalls, containerID)
	return nil
}

func (m *mockDocker) Remove(_ context.Context, containerID string) error {
	m.removeCalls = append(m.removeCalls, containerID)
	return nil
}

type mockProvider struct {
	configs   []*provider.UpstreamState
	reloads   int
	genErr    error
	reloadErr error
}

func (m *mockProvider) GenerateConfig(_ context.Context, s *provider.UpstreamState) error {
	m.configs = append(m.configs, s)
	return m.genErr
}

func (m *mockProvider) Reload(_ context.Context) error {
	m.reloads++
	return m.reloadErr
}

func testDeployment(oldCount, newCount int) *Deployment {
	d := &Deployment{
		Service: "app",
		Config: &config.ServiceConfig{
			HealthCheckTimeout: time.Second,
			DrainTimeout:       time.Millisecond,
			Affinity:           "cookie",
		},
	}

	for i := 0; i < oldCount; i++ {
		d.Old = append(d.Old, ContainerInfo{
			ID:   fmt.Sprintf("old_%d_xxxxxx_full_id", i),
			Addr: fmt.Sprintf("172.18.0.%d:80", i+2),
		})
	}

	for i := 0; i < newCount; i++ {
		d.New = append(d.New, ContainerInfo{
			ID:   fmt.Sprintf("new_%d_xxxxxx_full_id", i),
			Addr: fmt.Sprintf("172.18.0.%d:80", i+10),
		})
	}

	return d
}

func TestLinearExecuteBasic(t *testing.T) {
	docker := &mockDocker{}
	prov := &mockProvider{}
	stateMgr := state.NewManager(t.TempDir(), "")

	l := NewLinear(docker, prov, stateMgr)
	d := testDeployment(2, 2)

	if err := l.Execute(context.Background(), d); err != nil {
		t.Fatalf("execute: %v", err)
	}

	if len(docker.healthyCalls) != 2 {
		t.Errorf("expected 2 health checks, got %d", len(docker.healthyCalls))
	}

	if len(docker.stopCalls) != 2 {
		t.Errorf("expected 2 stops, got %d", len(docker.stopCalls))
	}

	if len(docker.removeCalls) != 2 {
		t.Errorf("expected 2 removes, got %d", len(docker.removeCalls))
	}

	if len(prov.configs) != 4 {
		t.Errorf("expected 4 config generations, got %d", len(prov.configs))
	}

	if prov.reloads != 4 {
		t.Errorf("expected 4 reloads, got %d", prov.reloads)
	}

	for i, id := range docker.stopCalls {
		if !strings.HasPrefix(id, "old_") {
			t.Errorf("stop %d: expected old container, got %s", i, id)
		}
	}
}

func TestLinearExecuteOrder(t *testing.T) {
	docker := &mockDocker{}
	prov := &mockProvider{}
	stateMgr := state.NewManager(t.TempDir(), "")

	l := NewLinear(docker, prov, stateMgr)
	d := testDeployment(3, 3)

	if err := l.Execute(context.Background(), d); err != nil {
		t.Fatalf("execute: %v", err)
	}

	if len(docker.stopCalls) != 3 {
		t.Fatalf("expected 3 stops, got %d", len(docker.stopCalls))
	}

	for i, id := range docker.stopCalls {
		expected := fmt.Sprintf("old_%d_xxxxxx_full_id", i)
		if id != expected {
			t.Errorf("stop %d: expected %s, got %s", i, expected, id)
		}
	}
}

func TestLinearConfigAtEachStep(t *testing.T) {
	docker := &mockDocker{}
	prov := &mockProvider{}
	stateMgr := state.NewManager(t.TempDir(), "")

	l := NewLinear(docker, prov, stateMgr)
	d := testDeployment(2, 2)

	if err := l.Execute(context.Background(), d); err != nil {
		t.Fatalf("execute: %v", err)
	}

	if len(prov.configs) != 4 {
		t.Fatalf("expected 4 configs, got %d", len(prov.configs))
	}

	cfg0 := prov.configs[0]
	hasDown := false
	hasNew := false
	for _, s := range cfg0.Servers {
		if s.Down && s.Addr == "172.18.0.2:80" {
			hasDown = true
		}
		if s.Addr == "172.18.0.10:80" {
			hasNew = true
		}
	}

	if !hasDown {
		t.Error("step 0: old[0] should be marked down")
	}
	if !hasNew {
		t.Error("step 0: new[0] should be in upstream")
	}

	cfg1 := prov.configs[1]
	hasDown = false
	hasNew = false
	for _, s := range cfg1.Servers {
		if s.Down && s.Addr == "172.18.0.3:80" {
			hasDown = true
		}
		if s.Addr == "172.18.0.11:80" {
			hasNew = true
		}
	}

	if !hasDown {
		t.Error("step 1: old[1] should be marked down")
	}
	if !hasNew {
		t.Error("step 1: new[1] should be in upstream")
	}

	cfg2 := prov.configs[2]
	for _, s := range cfg2.Servers {
		if s.Down {
			t.Error("final deployment config should not have down servers")
		}
	}

	if cfg2.Affinity == "" {
		t.Error("final deployment config should keep affinity")
	}

	cfg3 := prov.configs[3]
	if cfg3.Affinity != "" {
		t.Error("final stable config should clear affinity")
	}
}

func TestLinearHealthCheckFailure(t *testing.T) {
	docker := &mockDocker{healthErr: fmt.Errorf("unhealthy")}
	prov := &mockProvider{}
	stateMgr := state.NewManager(t.TempDir(), "")

	l := NewLinear(docker, prov, stateMgr)
	d := testDeployment(2, 2)

	err := l.Execute(context.Background(), d)
	if err == nil {
		t.Fatal("expected error on health check failure")
	}

	if !strings.Contains(err.Error(), "health check failed") {
		t.Errorf("expected health check error, got: %v", err)
	}

	if len(docker.stopCalls) != 0 {
		t.Error("should not stop any containers on health check failure")
	}

	if len(prov.configs) != 0 {
		t.Error("should not generate config on health check failure")
	}
}

func TestLinearSavesState(t *testing.T) {
	docker := &mockDocker{}
	prov := &mockProvider{}
	dir := t.TempDir()
	stateMgr := state.NewManager(dir, "")

	l := NewLinear(docker, prov, stateMgr)
	d := testDeployment(2, 2)

	if err := l.Execute(context.Background(), d); err != nil {
		t.Fatalf("execute: %v", err)
	}

	s, err := stateMgr.Load("app")
	if err != nil {
		t.Fatalf("load state: %v", err)
	}

	if s.Status != state.StatusIdle {
		t.Errorf("expected idle, got %s", s.Status)
	}

	if s.Strategy != "linear" {
		t.Errorf("expected linear, got %s", s.Strategy)
	}

	if len(s.Containers.Stable) != 2 {
		t.Errorf("expected 2 stable containers, got %d", len(s.Containers.Stable))
	}

	for i, id := range s.Containers.Stable {
		expected := fmt.Sprintf("new_%d_xxxxxx_full_id", i)
		if id != expected {
			t.Errorf("stable[%d]: expected %s, got %s", i, expected, id)
		}
	}
}

func TestLinearRollback(t *testing.T) {
	docker := &mockDocker{}
	prov := &mockProvider{}
	stateMgr := state.NewManager(t.TempDir(), "")

	l := NewLinear(docker, prov, stateMgr)
	d := testDeployment(2, 2)

	if err := l.Rollback(context.Background(), d); err != nil {
		t.Fatalf("rollback: %v", err)
	}

	if len(prov.configs) != 1 {
		t.Fatalf("expected 1 config, got %d", len(prov.configs))
	}

	cfg := prov.configs[0]
	if len(cfg.Servers) != 2 {
		t.Errorf("expected 2 servers in rollback config, got %d", len(cfg.Servers))
	}

	for _, s := range cfg.Servers {
		if s.Down {
			t.Error("rollback config should not have down servers")
		}
	}

	if prov.reloads != 1 {
		t.Errorf("expected 1 reload, got %d", prov.reloads)
	}

	if len(docker.stopCalls) != 2 {
		t.Errorf("expected 2 stops for new containers, got %d", len(docker.stopCalls))
	}

	for _, id := range docker.stopCalls {
		if !strings.HasPrefix(id, "new_") {
			t.Errorf("rollback should stop new containers, got %s", id)
		}
	}

	s, err := stateMgr.Load("app")
	if err != nil {
		t.Fatalf("load state: %v", err)
	}

	if s.Status != state.StatusIdle {
		t.Errorf("expected idle after rollback, got %s", s.Status)
	}
}

func TestLinearContextCancellation(t *testing.T) {
	docker := &mockDocker{}
	prov := &mockProvider{}
	stateMgr := state.NewManager(t.TempDir(), "")

	l := NewLinear(docker, prov, stateMgr)
	d := testDeployment(2, 2)
	d.Config.DrainTimeout = time.Second

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := l.Execute(ctx, d)
	if err == nil {
		t.Fatal("expected context cancellation error")
	}
}
