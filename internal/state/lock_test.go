package state

import (
	"errors"
	"testing"
)

func TestAcquireDeployLockExclusive(t *testing.T) {
	mgr := NewManager(t.TempDir(), "proj")

	release, err := mgr.AcquireDeployLock("app")
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}

	if _, err := mgr.AcquireDeployLock("app"); !errors.Is(err, ErrDeployLocked) {
		t.Fatalf("second acquire: want ErrDeployLocked, got %v", err)
	}

	// A different service is independently lockable.
	releaseOther, err := mgr.AcquireDeployLock("other")
	if err != nil {
		t.Fatalf("acquire other service: %v", err)
	}
	releaseOther()

	release()

	release2, err := mgr.AcquireDeployLock("app")
	if err != nil {
		t.Fatalf("re-acquire after release: %v", err)
	}
	release2()
}

func TestAcquireDeployLockInvalidName(t *testing.T) {
	mgr := NewManager(t.TempDir(), "proj")
	if _, err := mgr.AcquireDeployLock("../escape"); err == nil {
		t.Fatal("expected error for invalid service name")
	}
}
