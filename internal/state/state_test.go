package state

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestLoadMissing(t *testing.T) {
	dir := t.TempDir()
	mgr := NewManager(dir, "")

	s, err := mgr.Load("webapp")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if s.Service != "webapp" {
		t.Errorf("service = %s, want webapp", s.Service)
	}
	if s.Status != StatusIdle {
		t.Errorf("status = %s, want idle", s.Status)
	}
}

func TestSaveAndLoad(t *testing.T) {
	dir := t.TempDir()
	mgr := NewManager(dir, "")

	original := &DeploymentState{
		Service:              "webapp",
		Status:               StatusInProgress,
		Strategy:             "canary",
		CurrentWeight:        25,
		ActiveDeploymentID:   "deploy_abc",
		PreviousDeploymentID: "deploy_xyz",
		Containers: Containers{
			Stable: []string{"c1", "c2"},
			Canary: []string{"c3"},
		},
	}

	if err := mgr.Save(original); err != nil {
		t.Fatalf("save error: %v", err)
	}

	loaded, err := mgr.Load("webapp")
	if err != nil {
		t.Fatalf("load error: %v", err)
	}

	if loaded.Status != StatusInProgress {
		t.Errorf("status = %s, want in_progress", loaded.Status)
	}
	if loaded.CurrentWeight != 25 {
		t.Errorf("weight = %d, want 25", loaded.CurrentWeight)
	}
	if loaded.ActiveDeploymentID != "deploy_abc" {
		t.Errorf("active = %s, want deploy_abc", loaded.ActiveDeploymentID)
	}
	if len(loaded.Containers.Stable) != 2 {
		t.Errorf("stable count = %d, want 2", len(loaded.Containers.Stable))
	}
	if len(loaded.Containers.Canary) != 1 {
		t.Errorf("canary count = %d, want 1", len(loaded.Containers.Canary))
	}
}

func TestSaveAtomic(t *testing.T) {
	dir := t.TempDir()
	mgr := NewManager(dir, "")

	s := &DeploymentState{Service: "webapp", Status: StatusIdle}
	if err := mgr.Save(s); err != nil {
		t.Fatalf("save error: %v", err)
	}

	// No .tmp file should remain
	tmp := filepath.Join(dir, "webapp_state.json.tmp")
	if _, err := os.Stat(tmp); !os.IsNotExist(err) {
		t.Error("temp file was not cleaned up")
	}

	// Actual file should exist
	actual := filepath.Join(dir, "webapp_state.json")
	if _, err := os.Stat(actual); err != nil {
		t.Errorf("state file missing: %v", err)
	}
}

func TestGenerateDeploymentID(t *testing.T) {
	id1 := GenerateDeploymentID()
	id2 := GenerateDeploymentID()

	if id1 == "" {
		t.Error("deployment ID should not be empty")
	}

	if !testing.Short() && id1 == id2 {
		t.Error("deployment IDs should be unique")
	}

	if len(id1) < 20 {
		t.Errorf("deployment ID too short: %s", id1)
	}
}

func TestSaveCreatesDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "state")
	mgr := NewManager(dir, "")

	s := &DeploymentState{Service: "webapp", Status: StatusIdle}
	if err := mgr.Save(s); err != nil {
		t.Fatalf("save error: %v", err)
	}

	if _, err := os.Stat(dir); err != nil {
		t.Errorf("dir not created: %v", err)
	}
}

func TestSaveSetsUpdatedAt(t *testing.T) {
	dir := t.TempDir()
	mgr := NewManager(dir, "")

	s := &DeploymentState{Service: "webapp", Status: StatusInProgress}
	before := time.Now()

	if err := mgr.Save(s); err != nil {
		t.Fatalf("save error: %v", err)
	}

	after := time.Now()

	if s.UpdatedAt.Before(before) || s.UpdatedAt.After(after) {
		t.Errorf("UpdatedAt = %v, want between %v and %v", s.UpdatedAt, before, after)
	}

	loaded, err := mgr.Load("webapp")
	if err != nil {
		t.Fatalf("load error: %v", err)
	}

	if loaded.UpdatedAt.IsZero() {
		t.Error("loaded UpdatedAt should not be zero")
	}
}

func TestIsStaleZeroTimestamp(t *testing.T) {
	s := &DeploymentState{Status: StatusInProgress}

	if !s.IsStale(DefaultStaleThreshold) {
		t.Error("zero UpdatedAt with in_progress should be stale")
	}
}

func TestIsStaleRecentInProgress(t *testing.T) {
	s := &DeploymentState{
		Status:    StatusInProgress,
		UpdatedAt: time.Now(),
	}

	if s.IsStale(DefaultStaleThreshold) {
		t.Error("recent in_progress should not be stale")
	}
}

func TestIsStaleOldInProgress(t *testing.T) {
	s := &DeploymentState{
		Status:    StatusInProgress,
		UpdatedAt: time.Now().Add(-1 * time.Hour),
	}

	if !s.IsStale(DefaultStaleThreshold) {
		t.Error("old in_progress should be stale")
	}
}

func TestIsStaleIdleNeverStale(t *testing.T) {
	s := &DeploymentState{
		Status:    StatusIdle,
		UpdatedAt: time.Now().Add(-24 * time.Hour),
	}

	if s.IsStale(DefaultStaleThreshold) {
		t.Error("idle should never be stale regardless of age")
	}
}

func TestIsStaleRollingBack(t *testing.T) {
	s := &DeploymentState{
		Status:    StatusRollingBack,
		UpdatedAt: time.Now().Add(-1 * time.Hour),
	}

	if !s.IsStale(DefaultStaleThreshold) {
		t.Error("old rolling_back should be stale")
	}
}

func TestLegacyJSONBackwardCompat(t *testing.T) {
	dir := t.TempDir()
	mgr := NewManager(dir, "")

	legacy := `{"service":"webapp","status":"in_progress","strategy":"linear","current_weight":0}`
	path := filepath.Join(dir, "webapp_state.json")

	if err := os.WriteFile(path, []byte(legacy), 0o644); err != nil {
		t.Fatalf("write error: %v", err)
	}

	loaded, err := mgr.Load("webapp")
	if err != nil {
		t.Fatalf("load error: %v", err)
	}

	if loaded.Status != StatusInProgress {
		t.Errorf("status = %s, want in_progress", loaded.Status)
	}

	if !loaded.UpdatedAt.IsZero() {
		t.Error("legacy file should have zero UpdatedAt")
	}

	if !loaded.IsStale(DefaultStaleThreshold) {
		t.Error("legacy in_progress file should be treated as stale")
	}
}

func TestSaveRejectsPathTraversal(t *testing.T) {
	dir := t.TempDir()
	mgr := NewManager(dir, "")

	s := &DeploymentState{Service: "../evil", Status: StatusIdle}
	if err := mgr.Save(s); err == nil {
		t.Fatal("expected error for service name with path traversal")
	}

	s2 := &DeploymentState{Service: "valid-service", Status: StatusIdle}
	mgr2 := NewManager(dir, "../evil-project")
	if err := mgr2.Save(s2); err == nil {
		t.Fatal("expected error for project name with path traversal")
	}
}

func TestLoadRejectsPathTraversal(t *testing.T) {
	dir := t.TempDir()
	mgr := NewManager(dir, "")

	if _, err := mgr.Load("../evil"); err == nil {
		t.Fatal("expected error for service name with path traversal")
	}
}

func TestSaveConcurrentSameService(t *testing.T) {
	dir := t.TempDir()
	mgr := NewManager(dir, "")

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(weight int) {
			defer wg.Done()
			_ = mgr.Save(&DeploymentState{
				Service:       "webapp",
				Status:        StatusInProgress,
				CurrentWeight: weight,
			})
		}(i)
	}
	wg.Wait()

	loaded, err := mgr.Load("webapp")
	if err != nil {
		t.Fatalf("load after concurrent saves: %v", err)
	}
	if loaded.Service != "webapp" {
		t.Errorf("service = %s, want webapp", loaded.Service)
	}
}
