package health

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

var (
	sentinelPath = "/var/lib/docker-release/.ready"
	manifestPath = "/var/lib/docker-release/.files"
)

// RecordFile notes that path was successfully written by a provider.
// Best-effort: errors are silently ignored so providers never fail because of this.
func RecordFile(path string) {
	updateManifest(path, true)
}

func ForgetFile(path string) {
	updateManifest(path, false)
}

func updateManifest(path string, present bool) {
	if err := os.MkdirAll(filepath.Dir(manifestPath), 0o755); err != nil {
		return
	}
	f, err := os.OpenFile(manifestPath+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return
	}
	data, err := os.ReadFile(manifestPath)
	if err != nil && !os.IsNotExist(err) {
		return
	}
	files := make(map[string]bool)
	for _, name := range strings.Split(string(data), "\n") {
		if name != "" && name != path {
			files[name] = true
		}
	}
	if present {
		files[path] = true
	}
	var b strings.Builder
	for name := range files {
		fmt.Fprintln(&b, name)
	}
	tmp, err := os.CreateTemp(filepath.Dir(manifestPath), ".files-*")
	if err != nil {
		return
	}
	defer os.Remove(tmp.Name())
	_, writeErr := tmp.WriteString(b.String())
	if err := errors.Join(writeErr, tmp.Close()); err != nil {
		return
	}
	_ = os.Rename(tmp.Name(), manifestPath)
}

func MarkReady() error {
	if err := os.MkdirAll(filepath.Dir(sentinelPath), 0o755); err != nil {
		return err
	}
	return os.WriteFile(sentinelPath, nil, 0o644)
}

func ClearReady() error {
	for _, p := range []string{sentinelPath, manifestPath} {
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

// IsReady returns true when the initial scan has completed and every config
// file recorded by RecordFile still exists on disk.
func IsReady() bool {
	if _, err := os.Stat(sentinelPath); err != nil {
		return false
	}

	data, err := os.ReadFile(manifestPath)
	if errors.Is(err, os.ErrNotExist) {
		return true
	}
	if err != nil {
		return false
	}

	for _, path := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if path == "" {
			continue
		}
		if _, err := os.Stat(path); err != nil {
			return false
		}
	}
	return true
}
