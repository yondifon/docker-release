//go:build bundled_nginx || bundled_angie || bundled_caddy || bundled_haproxy

package main

import (
	"context"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestBundledProcess(t *testing.T) {
	dir := os.Getenv("DR_TEST_PROXY_DIR")
	if dir == "" {
		return
	}
	if os.Getenv("DR_TEST_PROXY_EXIT") == "1" {
		os.Exit(7)
	}
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGQUIT, syscall.SIGTERM, syscall.SIGUSR1)
	if err := os.WriteFile(filepath.Join(dir, "listening"), nil, 0o600); err != nil {
		os.Exit(2)
	}
	want := syscall.SIGQUIT
	if os.Getenv("DR_TEST_CADDY") == "1" {
		want = syscall.SIGTERM
	}
	if os.Getenv("DR_TEST_HAPROXY") == "1" {
		want = syscall.SIGUSR1
	}
	if <-sig != want {
		os.Exit(3)
	}
	if _, err := os.Stat(filepath.Join(dir, "controller-stopped")); err != nil {
		os.Exit(4)
	}
	if err := os.WriteFile(filepath.Join(dir, "graceful-stop"), nil, 0o600); err != nil {
		os.Exit(5)
	}
	os.Exit(0)
}

func TestBundledShutdownWaitsForControllerBeforeGracefulProxyStop(t *testing.T) {
	for _, name := range []string{"nginx", "angie", "caddy", "haproxy"} {
		t.Run(name, func(t *testing.T) { testBundledShutdown(t, name) })
	}
}

func testBundledShutdown(t *testing.T, name string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("DR_TEST_PROXY_DIR", dir)
	if name == "caddy" {
		t.Setenv("DR_TEST_CADDY", "1")
	}
	if name == "haproxy" {
		t.Setenv("DR_TEST_HAPROXY", "1")
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	proxy := &bundledProxy{name: name, args: []string{executable, "-test.run=^TestBundledProcess$"}}
	err = superviseBundledProxy(ctx, proxy, func(ctx context.Context) error {
		<-ctx.Done()
		return os.WriteFile(filepath.Join(dir, "controller-stopped"), nil, 0o600)
	}, func() bool { return true }, func(context.Context) bool {
		_, err := os.Stat(filepath.Join(dir, "listening"))
		return err == nil
	}, func(context.Context) error {
		cancel()
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "graceful-stop")); err != nil {
		t.Fatalf("proxy was not stopped gracefully after controller shutdown: %v", err)
	}
}

func TestBundledStartFailureJoinsControllerBeforeReturning(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan struct{})
	err := superviseBundledProxy(ctx, &bundledProxy{name: "test", args: []string{filepath.Join(t.TempDir(), "missing-proxy")}}, func(ctx context.Context) error {
		<-ctx.Done()
		close(done)
		return nil
	}, func() bool { return true }, func(context.Context) bool { return false }, func(context.Context) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "starting bundled test") {
		t.Fatalf("missing proxy did not fail startup: %v", err)
	}
	select {
	case <-done:
	default:
		t.Fatal("supervisor returned while controller cleanup was still running")
	}
}

func TestBundledProxyExitStopsController(t *testing.T) {
	t.Setenv("DR_TEST_PROXY_DIR", t.TempDir())
	t.Setenv("DR_TEST_PROXY_EXIT", "1")
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan struct{})
	err = superviseBundledProxy(ctx, &bundledProxy{name: "test", args: []string{executable, "-test.run=^TestBundledProcess$"}}, func(ctx context.Context) error {
		<-ctx.Done()
		close(done)
		return nil
	}, func() bool { return true }, func(context.Context) bool { return false }, func(context.Context) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "exit status 7") {
		t.Fatalf("proxy exit was not reported: %v", err)
	}
	select {
	case <-done:
	default:
		t.Fatal("proxy died but controller was still running")
	}
}
