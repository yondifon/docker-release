//go:build bundled_nginx

package main

import (
	"context"
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

	_ = health.ClearReady()
	_ = clearBundledProxyStarted(proxy)
	if err := prepareBundledProxyConfig(proxy); err != nil {
		return err
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	controllerErr := make(chan error, 1)
	go func() {
		controllerErr <- run(runCtx)
	}()

	if err := waitForInitialConfigs(ctx, controllerErr); err != nil {
		cancel()
		if ctx.Err() != nil {
			return nil
		}
		return err
	}

	cmd := exec.Command(proxy.args[0], proxy.args[1:]...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		cancel()
		return fmt.Errorf("starting bundled %s: %w", proxy.name, err)
	}
	if err := markBundledProxyStarted(proxy); err != nil {
		cancel()
		stopBundledProxy(cmd, proxy, nil)
		return err
	}
	slog.Info("started bundled proxy", "component", "main", "proxy", proxy.name)

	proxyErr := make(chan error, 1)
	go func() {
		proxyErr <- cmd.Wait()
	}()

	select {
	case err := <-controllerErr:
		cancel()
		stopBundledProxy(cmd, proxy, proxyErr)
		return err
	case err := <-proxyErr:
		cancel()
		<-controllerErr
		if err != nil {
			return fmt.Errorf("bundled %s exited: %w", proxy.name, err)
		}
		return fmt.Errorf("bundled %s exited", proxy.name)
	case <-ctx.Done():
		cancel()
		stopBundledProxy(cmd, proxy, proxyErr)
		if err := <-controllerErr; err != nil {
			return err
		}
		return nil
	}
}

func prepareBundledProxyConfig(proxy *bundledProxy) error {
	switch proxy.name {
	case "nginx":
		return prepareBundledNginxConfig()
	default:
		return nil
	}
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
	return os.WriteFile(path, nil, 0o644)
}

func waitForInitialConfigs(ctx context.Context, controllerErr <-chan error) error {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		if health.IsReady() {
			return nil
		}

		select {
		case err := <-controllerErr:
			if err != nil {
				return err
			}
			return fmt.Errorf("controller exited before bundled proxy started")
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func stopBundledProxy(cmd *exec.Cmd, proxy *bundledProxy, proxyErr <-chan error) {
	if cmd.Process == nil {
		return
	}

	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		slog.Warn("could not stop bundled proxy", "component", "main", "proxy", proxy.name, "err", err)
		return
	}

	if proxyErr == nil {
		done := make(chan struct{})
		go func() {
			_ = cmd.Wait()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			_ = cmd.Process.Kill()
			<-done
		}
		return
	}

	select {
	case <-proxyErr:
		return
	case <-time.After(5 * time.Second):
		_ = cmd.Process.Kill()
		<-proxyErr
	}
}
