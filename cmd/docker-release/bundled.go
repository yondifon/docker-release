//go:build bundled_nginx

package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/malico/docker-release/internal/health"
)

type bundledProxy struct {
	name string
	args []string
	pid  int
}

func bundledProxyFromEnv() (*bundledProxy, error) {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("DR_BUNDLED_PROXY")))
	switch v {
	case "", "none":
		return nil, nil
	case "nginx":
		return &bundledProxy{name: "nginx", args: []string{"nginx", "-g", "daemon off;"}}, nil
	default:
		return nil, fmt.Errorf("unknown DR_BUNDLED_PROXY %q", os.Getenv("DR_BUNDLED_PROXY"))
	}
}

func runWithBundledProxy(ctx context.Context, proxy *bundledProxy, run func(context.Context) error) error {
	if proxy == nil {
		return run(ctx)
	}
	if err := health.ClearReady(); err != nil {
		return err
	}
	if err := clearBundledProxyStarted(proxy); err != nil {
		return err
	}
	defer health.ClearReady()
	defer clearBundledProxyStarted(proxy)
	if err := prepareBundledNginxConfig(); err != nil {
		return err
	}
	return superviseBundledProxy(ctx, proxy, run, health.IsReady, nginxResponding, func(context.Context) error {
		return markBundledProxyStarted(proxy)
	})
}

func superviseBundledProxy(ctx context.Context, proxy *bundledProxy, run func(context.Context) error, configsReady func() bool, proxyReady func(context.Context) bool, started func(context.Context) error) (result error) {
	runCtx, cancel := context.WithCancel(ctx)
	controllerDone := make(chan struct{})
	var controllerErr error
	go func() {
		controllerErr = run(runCtx)
		close(controllerDone)
	}()
	defer func() {
		cancel()
		<-controllerDone
		if controllerErr != nil && !errors.Is(controllerErr, context.Canceled) {
			result = errors.Join(result, controllerErr)
		}
	}()

	if err := waitForInitialConfigs(ctx, controllerDone, configsReady); err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return err
	}

	cmd := exec.Command(proxy.args[0], proxy.args[1:]...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting bundled %s: %w", proxy.name, err)
	}
	proxy.pid = cmd.Process.Pid
	proxyDone := make(chan struct{})
	var proxyErr error
	go func() {
		proxyErr = cmd.Wait()
		close(proxyDone)
	}()
	defer func() {
		cancel()
		<-controllerDone
		stopBundledProxy(cmd, proxyDone)
		if proxyErr != nil {
			result = errors.Join(result, fmt.Errorf("bundled %s: %w", proxy.name, proxyErr))
		}
	}()

	if err := waitForBundledNginx(ctx, controllerDone, proxyDone, proxyReady); err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return err
	}
	if err := started(runCtx); err != nil {
		return err
	}
	slog.Info("started bundled proxy", "component", "main", "proxy", proxy.name)
	select {
	case <-controllerDone:
		if controllerErr == nil && ctx.Err() == nil {
			return fmt.Errorf("controller exited while bundled proxy was running")
		}
		return nil
	case <-proxyDone:
		return fmt.Errorf("bundled %s exited", proxy.name)
	case <-ctx.Done():
		return nil
	}
}

func waitForInitialConfigs(ctx context.Context, controllerDone <-chan struct{}, ready func() bool) error {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for !ready() {
		select {
		case <-controllerDone:
			return fmt.Errorf("controller exited before bundled proxy started")
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
	return nil
}

func waitForBundledNginx(ctx context.Context, controllerDone, proxyDone <-chan struct{}, ready func(context.Context) bool) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for !ready(ctx) {
		select {
		case <-proxyDone:
			return fmt.Errorf("bundled nginx exited before accepting traffic; check the nginx logs")
		case <-controllerDone:
			return fmt.Errorf("controller exited before bundled nginx was ready")
		case <-ctx.Done():
			return fmt.Errorf("bundled nginx did not respond at /health; check its HTTP health route: %w", ctx.Err())
		case <-ticker.C:
		}
	}
	return nil
}

func bundledProxyStartedPath(proxy *bundledProxy) string {
	return filepath.Join("/run/docker-release", proxy.name+".started")
}

func clearBundledProxyStarted(proxy *bundledProxy) error {
	err := os.Remove(bundledProxyStartedPath(proxy))
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func markBundledProxyStarted(proxy *bundledProxy) error {
	path := bundledProxyStartedPath(proxy)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(fmt.Sprintf("%d\n", proxy.pid)), 0o644)
}

func stopBundledProxy(cmd *exec.Cmd, done <-chan struct{}) {
	select {
	case <-done:
		return
	default:
	}
	_ = cmd.Process.Signal(syscall.SIGQUIT)
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		_ = cmd.Process.Kill()
		<-done
	}
}
