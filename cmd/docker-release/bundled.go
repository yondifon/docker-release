//go:build bundled_nginx || bundled_angie || bundled_caddy || bundled_haproxy || bundled_traefik

package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/malico/docker-release/internal/config"
	"github.com/malico/docker-release/internal/health"
	"github.com/malico/docker-release/internal/provider"
)

type bundledProxy struct {
	name       string
	args       []string
	pid        int
	configPath string
	config     []byte
}

func bundledProxyFromEnv() (*bundledProxy, error) {
	v := config.BundledProxy()
	switch v {
	case "", "none":
		return nil, nil
	case bundledProxyName:
		proxy := &bundledProxy{
			name:       v,
			args:       []string{v, "-g", "daemon off;"},
			configPath: bundledConfigPath,
			config:     bundledConfig,
		}
		if v == "caddy" {
			proxy.args = []string{"caddy", "run", "--config", proxy.configPath}
		}
		if v == "haproxy" {
			proxy.args = []string{
				"haproxy", "-W", "-db", "-S", "/run/haproxy/master.sock,mode,600",
				"-f", proxy.configPath, "-f", "/shared/haproxy-config",
			}
			if !truthy(os.Getenv("DR_PROXY_SKIP_CONFIG")) {
				proxy.args = append(proxy.args, "-f", "/shared/haproxy-config/routes")
			}
		}
		if v == "traefik" {
			proxy.args = []string{"traefik", "--configFile=" + proxy.configPath}
		}
		return proxy, nil
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
	skip, set := os.LookupEnv("DR_PROXY_SKIP_CONFIG")
	if !set && proxy.name == "nginx" {
		skip = os.Getenv("DR_NGINX_SKIP_CONFIG")
	}
	if !truthy(skip) {
		if err := os.WriteFile(proxy.configPath, proxy.config, 0o644); err != nil {
			return err
		}
	}
	runProxy := func(ctx context.Context) error {
		return superviseBundledProxy(ctx, proxy, run, health.IsReady, proxyResponding, func(context.Context) error {
			return markBundledProxyStarted(proxy)
		})
	}
	if proxy.name == "traefik" {
		return runWithBundledRedirect(ctx, runProxy)
	}
	return runProxy(ctx)
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
		stopBundledProxy(cmd, proxyDone, proxy.name)
		if proxyErr != nil {
			result = errors.Join(result, fmt.Errorf("bundled %s: %w", proxy.name, proxyErr))
		}
	}()

	if err := waitForBundledProxy(ctx, proxy.name, controllerDone, proxyDone, proxyReady); err != nil {
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

func waitForBundledProxy(ctx context.Context, name string, controllerDone, proxyDone <-chan struct{}, ready func(context.Context) bool) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for !ready(ctx) {
		select {
		case <-proxyDone:
			return fmt.Errorf("bundled %s exited before accepting traffic; check the proxy logs", name)
		case <-controllerDone:
			return fmt.Errorf("controller exited before bundled %s was ready", name)
		case <-ctx.Done():
			return fmt.Errorf("bundled %s did not respond at /health; check its HTTP health route: %w", name, ctx.Err())
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

func stopBundledProxy(cmd *exec.Cmd, done <-chan struct{}, name string) {
	select {
	case <-done:
		return
	default:
	}
	sig := syscall.SIGQUIT
	if name == "caddy" || name == "traefik" {
		sig = syscall.SIGTERM
	}
	if name == "haproxy" {
		sig = syscall.SIGUSR1
	}
	_ = cmd.Process.Signal(sig)
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		_ = cmd.Process.Kill()
		<-done
	}
}

func bundledProxyHealthy() bool {
	proxy, err := bundledProxyFromEnv()
	if err != nil {
		return false
	}
	if proxy == nil {
		return true
	}
	if _, err := os.Stat(bundledProxyStartedPath(proxy)); err != nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	return proxyResponding(ctx)
}

func proxyResponding(ctx context.Context) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://127.0.0.1/health", nil)
	if err != nil {
		return false
	}
	client := &http.Client{
		Transport: &http.Transport{Proxy: nil},
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	defer client.CloseIdleConnections()
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false
	}
	return bundledProxyName != "traefik" || provider.TraefikReady(ctx, "/shared/traefik-config")
}

func truthy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func runWithBundledRedirect(ctx context.Context, run func(context.Context) error) error {
	// Traefik's redirect middleware sends `301`; bundles promise `308`.
	listener, err := net.Listen("tcp", "127.0.0.1:8082")
	if err != nil {
		return err
	}
	server := &http.Server{ReadHeaderTimeout: 5 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		if hostname, _, err := net.SplitHostPort(host); err == nil {
			host = hostname
		}
		w.Header().Set("Location", "https://"+host+r.URL.RequestURI())
		w.WriteHeader(http.StatusPermanentRedirect)
	})}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	err = run(ctx)
	_ = server.Close()
	if serveErr := <-done; serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
		err = errors.Join(err, serveErr)
	}
	return err
}
