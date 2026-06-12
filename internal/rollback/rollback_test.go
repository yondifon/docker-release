package rollback

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/malico/docker-release/internal/config"
	"github.com/malico/docker-release/internal/provider"
	"github.com/malico/docker-release/internal/state"
	"github.com/malico/docker-release/internal/strategy"
)

type mockResolver struct {
	addrs map[string]string
}

func (m *mockResolver) ContainerAddr(_ context.Context, containerID string) (string, error) {
	addr, ok := m.addrs[containerID]
	if !ok {
		return "", fmt.Errorf("container %s not found", containerID)
	}
	return addr, nil
}

type mockStrategy struct {
	rollbackCalls []*strategy.Deployment
	rollbackErr   error
}

func (m *mockStrategy) Execute(_ context.Context, d *strategy.Deployment) error {
	return nil
}

func (m *mockStrategy) Rollback(_ context.Context, d *strategy.Deployment) error {
	m.rollbackCalls = append(m.rollbackCalls, d)
	return m.rollbackErr
}

func TestCoordinatorExecute(t *testing.T) {
	dir := t.TempDir()
	stateMgr := state.NewManager(dir, "")

	ds := &state.DeploymentState{
		Service:  "app",
		Status:   state.StatusInProgress,
		Strategy: "linear",
		Containers: state.Containers{
			Stable: []string{"stable_aaa111_full"},
			Canary: []string{"canary_bbb222_full"},
		},
	}

	if err := stateMgr.Save(ds); err != nil {
		t.Fatal(err)
	}

	resolver := &mockResolver{addrs: map[string]string{
		"stable_aaa111_full": "172.18.0.2:80",
		"canary_bbb222_full": "172.18.0.10:80",
	}}

	mock := &mockStrategy{}
	coord := NewCoordinator(stateMgr, resolver)
	coord.RegisterStrategy("linear", mock)

	if err := coord.Execute(context.Background(), "app"); err != nil {
		t.Fatalf("execute: %v", err)
	}

	if len(mock.rollbackCalls) != 1 {
		t.Fatalf("expected 1 rollback call, got %d", len(mock.rollbackCalls))
	}

	d := mock.rollbackCalls[0]
	if d.Service != "app" {
		t.Errorf("expected service app, got %s", d.Service)
	}

	if len(d.Old) != 1 || d.Old[0].Addr != "172.18.0.2:80" {
		t.Errorf("unexpected old containers: %+v", d.Old)
	}

	if len(d.New) != 1 || d.New[0].Addr != "172.18.0.10:80" {
		t.Errorf("unexpected new containers: %+v", d.New)
	}

	saved, _ := stateMgr.Load("app")
	if saved.Status != state.StatusRollingBack {
		t.Errorf("expected rolling_back (mock strategy doesn't reset state), got %s", saved.Status)
	}
}

func TestCoordinatorIdleService(t *testing.T) {
	dir := t.TempDir()
	stateMgr := state.NewManager(dir, "")

	coord := NewCoordinator(stateMgr, &mockResolver{})

	err := coord.Execute(context.Background(), "app")
	if err == nil {
		t.Fatal("expected error for idle service")
	}

	if !strings.Contains(err.Error(), "no active deployment") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestCoordinatorUnknownStrategy(t *testing.T) {
	dir := t.TempDir()
	stateMgr := state.NewManager(dir, "")

	ds := &state.DeploymentState{
		Service:  "app",
		Status:   state.StatusInProgress,
		Strategy: "unknown",
	}
	stateMgr.Save(ds)

	coord := NewCoordinator(stateMgr, &mockResolver{})

	err := coord.Execute(context.Background(), "app")
	if err == nil {
		t.Fatal("expected error for unknown strategy")
	}

	if !strings.Contains(err.Error(), "unknown strategy") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestCoordinatorExecuteWithDeployment(t *testing.T) {
	dir := t.TempDir()
	stateMgr := state.NewManager(dir, "")

	ds := &state.DeploymentState{
		Service:  "app",
		Status:   state.StatusInProgress,
		Strategy: "canary",
		Containers: state.Containers{
			Stable: []string{"stable_aaa111_full"},
			Canary: []string{"canary_bbb222_full"},
		},
	}
	stateMgr.Save(ds)

	mock := &mockStrategy{}
	coord := NewCoordinator(stateMgr, &mockResolver{})
	coord.RegisterStrategy("canary", mock)

	d := &strategy.Deployment{
		Service: "app",
		Config: &config.ServiceConfig{
			HealthCheckTimeout: time.Second,
			DrainTimeout:       time.Millisecond,
		},
		Old: []strategy.ContainerInfo{
			{ID: "stable_aaa111_full", Addr: "172.18.0.2:80"},
		},
		New: []strategy.ContainerInfo{
			{ID: "canary_bbb222_full", Addr: "172.18.0.10:80"},
		},
	}

	if err := coord.ExecuteWithDeployment(context.Background(), d); err != nil {
		t.Fatalf("execute: %v", err)
	}

	if len(mock.rollbackCalls) != 1 {
		t.Fatalf("expected 1 rollback call, got %d", len(mock.rollbackCalls))
	}
}

func TestCoordinatorSkipsMissingContainers(t *testing.T) {
	dir := t.TempDir()
	stateMgr := state.NewManager(dir, "")

	ds := &state.DeploymentState{
		Service:  "app",
		Status:   state.StatusInProgress,
		Strategy: "linear",
		Containers: state.Containers{
			Stable: []string{"stable_aaa111_full", "stable_gone12_full"},
			Canary: []string{"canary_bbb222_full"},
		},
	}
	stateMgr.Save(ds)

	resolver := &mockResolver{addrs: map[string]string{
		"stable_aaa111_full": "172.18.0.2:80",
		"canary_bbb222_full": "172.18.0.10:80",
	}}

	mock := &mockStrategy{}
	coord := NewCoordinator(stateMgr, resolver)
	coord.RegisterStrategy("linear", mock)

	if err := coord.Execute(context.Background(), "app"); err != nil {
		t.Fatalf("execute: %v", err)
	}

	d := mock.rollbackCalls[0]
	if len(d.Old) != 1 {
		t.Errorf("expected 1 old (missing container skipped), got %d", len(d.Old))
	}
}

// Verify NoopProvider satisfies the interface
func TestNoopProvider(t *testing.T) {
	var p provider.Provider = &NoopProvider{}

	if err := p.GenerateConfig(context.Background(), nil); err != nil {
		t.Error(err)
	}

	if err := p.Reload(context.Background()); err != nil {
		t.Error(err)
	}
}
