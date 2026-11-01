package monitor

import (
	"context"
	"sync"
	"testing"
	"time"
)

type mockChecker struct {
	mu            sync.Mutex
	healthy       map[string]bool
	restartCounts map[string]int
}

func newMockChecker() *mockChecker {
	return &mockChecker{
		healthy:       make(map[string]bool),
		restartCounts: make(map[string]int),
	}
}

func (m *mockChecker) IsHealthy(_ context.Context, containerID string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	h, ok := m.healthy[containerID]
	if !ok {
		return true, nil
	}
	return h, nil
}

func (m *mockChecker) RestartCount(_ context.Context, containerID string) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	return m.restartCounts[containerID], nil
}

func (m *mockChecker) setHealthy(id string, h bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.healthy[id] = h
}

func (m *mockChecker) setRestartCount(id string, count int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.restartCounts[id] = count
}

func TestMonitorDetectsUnhealthy(t *testing.T) {
	checker := newMockChecker()
	checker.setHealthy("container_abc123", true)

	var triggered string
	var triggerReason string
	var mu sync.Mutex

	mon := NewHealthMonitor(checker, []string{"container_abc123"}, func(id, reason string) {
		mu.Lock()
		triggered = id
		triggerReason = reason
		mu.Unlock()
	})
	mon.interval = 10 * time.Millisecond

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	go func() {
		time.Sleep(30 * time.Millisecond)
		checker.setHealthy("container_abc123", false)
	}()

	_ = mon.Run(ctx)

	mu.Lock()
	defer mu.Unlock()

	if triggered != "container_abc123" {
		t.Errorf("expected trigger for container_abc123, got %q", triggered)
	}

	if triggerReason == "" {
		t.Error("expected a reason")
	}
}

func TestMonitorDetectsRestarts(t *testing.T) {
	checker := newMockChecker()
	checker.setHealthy("container_abc123", true)
	checker.setRestartCount("container_abc123", 0)

	var triggered string
	var mu sync.Mutex

	mon := NewHealthMonitor(checker, []string{"container_abc123"}, func(id, reason string) {
		mu.Lock()
		triggered = id
		mu.Unlock()
	})
	mon.interval = 10 * time.Millisecond

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	go func() {
		time.Sleep(30 * time.Millisecond)
		checker.setRestartCount("container_abc123", 3)
	}()

	_ = mon.Run(ctx)

	mu.Lock()
	defer mu.Unlock()

	if triggered != "container_abc123" {
		t.Errorf("expected trigger for container_abc123, got %q", triggered)
	}
}

func TestMonitorNoFalseAlarm(t *testing.T) {
	checker := newMockChecker()
	checker.setHealthy("container_abc123", true)
	checker.setRestartCount("container_abc123", 0)

	triggered := false

	mon := NewHealthMonitor(checker, []string{"container_abc123"}, func(id, reason string) {
		triggered = true
	})
	mon.interval = 10 * time.Millisecond

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()

	_ = mon.Run(ctx)

	if triggered {
		t.Error("should not trigger for healthy container")
	}
}

func TestMonitorMultipleContainers(t *testing.T) {
	checker := newMockChecker()
	checker.setHealthy("container_aaa111", true)
	checker.setHealthy("container_bbb222", true)

	var triggered string
	var mu sync.Mutex

	ids := []string{"container_aaa111", "container_bbb222"}
	mon := NewHealthMonitor(checker, ids, func(id, reason string) {
		mu.Lock()
		triggered = id
		mu.Unlock()
	})
	mon.interval = 10 * time.Millisecond

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	go func() {
		time.Sleep(30 * time.Millisecond)
		checker.setHealthy("container_bbb222", false)
	}()

	_ = mon.Run(ctx)

	mu.Lock()
	defer mu.Unlock()

	if triggered != "container_bbb222" {
		t.Errorf("expected trigger for container_bbb222, got %q", triggered)
	}
}

func TestMonitorInitialRestartBaseline(t *testing.T) {
	checker := newMockChecker()
	checker.setHealthy("container_abc123", true)
	checker.setRestartCount("container_abc123", 5)

	triggered := false

	mon := NewHealthMonitor(checker, []string{"container_abc123"}, func(id, reason string) {
		triggered = true
	})
	mon.interval = 10 * time.Millisecond

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()

	_ = mon.Run(ctx)

	if triggered {
		t.Error("should not trigger when restart count hasn't increased beyond baseline")
	}
}
