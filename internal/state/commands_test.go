package state

import (
	"testing"
)

func TestReleaseCommandQueue(t *testing.T) {
	m := NewManager(t.TempDir(), "demo")

	cmd, err := m.EnqueueReleaseCommand("app", true)
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if cmd.ID == "" {
		t.Fatal("expected command ID")
	}

	pending, err := m.PendingReleaseCommands()
	if err != nil {
		t.Fatalf("pending: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("pending count = %d, want 1", len(pending))
	}
	if pending[0].Service != "app" || !pending[0].Force {
		t.Fatalf("pending command = %+v", pending[0].ReleaseCommand)
	}

	claimed, ok, err := m.ClaimReleaseCommand(pending[0])
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if !ok {
		t.Fatal("expected command claim")
	}

	pending, err = m.PendingReleaseCommands()
	if err != nil {
		t.Fatalf("pending after claim: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("pending after claim count = %d, want 0", len(pending))
	}

	if err := m.CompleteReleaseCommand(claimed); err != nil {
		t.Fatalf("complete: %v", err)
	}
}

func TestPendingReleaseCommandsMissingDir(t *testing.T) {
	m := NewManager(t.TempDir(), "demo")

	pending, err := m.PendingReleaseCommands()
	if err != nil {
		t.Fatalf("pending: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("pending count = %d, want 0", len(pending))
	}
}

// TestReleaseCommandIncludesProject verifies that the Project field is
// populated when a command is enqueued via a project-scoped manager.
func TestReleaseCommandIncludesProject(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(dir, "myproject")

	cmd, err := m.EnqueueReleaseCommand("app", false)
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if cmd.Project != "myproject" {
		t.Errorf("Project = %q, want myproject", cmd.Project)
	}
}

// TestScanAllPendingCommandsCrossProject verifies that ScanAllPendingCommands
// returns commands from all projects and correctly sets Project.
func TestScanAllPendingCommandsCrossProject(t *testing.T) {
	dir := t.TempDir()

	fooMgr := NewManager(dir, "foo")
	barMgr := NewManager(dir, "bar")

	if _, err := fooMgr.EnqueueReleaseCommand("app", false); err != nil {
		t.Fatalf("enqueue foo: %v", err)
	}
	if _, err := barMgr.EnqueueReleaseCommand("app", true); err != nil {
		t.Fatalf("enqueue bar: %v", err)
	}

	all, err := ScanAllPendingCommands(dir)
	if err != nil {
		t.Fatalf("ScanAllPendingCommands: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("len = %d, want 2", len(all))
	}

	byProject := make(map[string]QueuedReleaseCommand)
	for _, cmd := range all {
		byProject[cmd.Project] = cmd
	}

	if byProject["foo"].Service != "app" || byProject["foo"].Force {
		t.Errorf("foo command = %+v", byProject["foo"].ReleaseCommand)
	}
	if byProject["bar"].Service != "app" || !byProject["bar"].Force {
		t.Errorf("bar command = %+v", byProject["bar"].ReleaseCommand)
	}
}

// TestScanAllPendingCommandsEmptyDir verifies no error and empty slice when
// the base dir doesn't exist.
func TestScanAllPendingCommandsEmptyDir(t *testing.T) {
	all, err := ScanAllPendingCommands("/nonexistent/path")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(all) != 0 {
		t.Errorf("len = %d, want 0", len(all))
	}
}
