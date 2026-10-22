package health

import (
	"os"
	"path/filepath"
	"testing"
)

func setup(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	origSentinel := sentinelPath
	origManifest := manifestPath
	sentinelPath = filepath.Join(dir, ".ready")
	manifestPath = filepath.Join(dir, ".files")
	t.Cleanup(func() {
		sentinelPath = origSentinel
		manifestPath = origManifest
	})
}

func TestMarkReady(t *testing.T) {
	setup(t)

	if IsReady() {
		t.Fatal("should not be ready before MarkReady")
	}
	if err := MarkReady(); err != nil {
		t.Fatalf("MarkReady: %v", err)
	}
	if !IsReady() {
		t.Fatal("should be ready after MarkReady")
	}
}

func TestClearReady(t *testing.T) {
	setup(t)

	if err := MarkReady(); err != nil {
		t.Fatalf("MarkReady: %v", err)
	}
	if err := ClearReady(); err != nil {
		t.Fatalf("ClearReady: %v", err)
	}
	if IsReady() {
		t.Fatal("should not be ready after ClearReady")
	}
}

func TestClearReadyIdempotent(t *testing.T) {
	setup(t)

	if err := ClearReady(); err != nil {
		t.Fatalf("ClearReady on missing sentinel: %v", err)
	}
}

func TestIsReadyFailsWhenRecordedFileDeleted(t *testing.T) {
	setup(t)

	f := filepath.Join(t.TempDir(), "app.conf")
	if err := os.WriteFile(f, []byte("upstream {}"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	RecordFile(f)
	if err := MarkReady(); err != nil {
		t.Fatalf("MarkReady: %v", err)
	}
	if !IsReady() {
		t.Fatal("should be ready when recorded file exists")
	}

	os.Remove(f)

	if IsReady() {
		t.Fatal("should not be ready after recorded file is deleted")
	}
}

func TestIsReadyNoManifest(t *testing.T) {
	setup(t)

	if err := MarkReady(); err != nil {
		t.Fatalf("MarkReady: %v", err)
	}
	if !IsReady() {
		t.Fatal("should be ready with no manifest (no services)")
	}
}

func TestClearReadyRemovesManifest(t *testing.T) {
	setup(t)

	RecordFile("/some/path.conf")
	if err := MarkReady(); err != nil {
		t.Fatalf("MarkReady: %v", err)
	}
	if err := ClearReady(); err != nil {
		t.Fatalf("ClearReady: %v", err)
	}

	if _, err := os.Stat(manifestPath); !os.IsNotExist(err) {
		t.Fatal("manifest should be removed by ClearReady")
	}
}

func TestRemovingGeneratedRouteDoesNotLeaveHealthPermanentlyUnready(t *testing.T) {
	setup(t)
	old := filepath.Join(t.TempDir(), "app.location")
	if err := os.WriteFile(old, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	RecordFile(old)
	RecordFile(old)
	if err := MarkReady(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(old); err != nil {
		t.Fatal(err)
	}
	ForgetFile(old)
	if !IsReady() {
		t.Fatal("removing a generated route left health permanently unready")
	}
}

func TestHealthDoesNotIgnoreUnreadableManifest(t *testing.T) {
	setup(t)
	if err := MarkReady(); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(manifestPath, 0o755); err != nil {
		t.Fatal(err)
	}
	if IsReady() {
		t.Fatal("health ignored an unreadable config manifest")
	}
}
