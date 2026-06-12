package state

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// ErrDeployLocked is returned by AcquireDeployLock when another process already
// holds the deploy lock for a service.
var ErrDeployLocked = errors.New("deployment already locked by another process")

// AcquireDeployLock takes an advisory exclusive flock on a per-service lock file
// so the CLI and the watch daemon — separate processes sharing the state dir —
// cannot deploy the same service concurrently. The returned release drops the
// lock; the OS also drops it if the process dies, so a crash never leaves a
// service permanently locked (unlike the in-state StatusInProgress guard, which
// is why both exist).
//
// Acquisition is non-blocking: a contended lock returns ErrDeployLocked
// immediately rather than waiting.
func (m *Manager) AcquireDeployLock(service string) (release func(), err error) {
	if err := validateName(service); err != nil {
		return nil, err
	}
	if err := validateName(m.project); err != nil {
		return nil, err
	}

	if err := os.MkdirAll(m.dir, 0o755); err != nil {
		return nil, fmt.Errorf("creating state dir: %w", err)
	}

	name := service + ".lock"
	if m.project != "" {
		name = m.project + "_" + service + ".lock"
	}
	path := filepath.Join(m.dir, name)

	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("opening lock file: %w", err)
	}

	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrDeployLocked
		}
		return nil, fmt.Errorf("locking %s: %w", path, err)
	}

	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}, nil
}
