//go:build bundled_nginx

package main

import (
	"context"
	"net/http"
	"os"
	"strings"
	"time"

	nginx "github.com/malico/docker-release/packaging/nginx"
)

func prepareBundledNginxConfig() error {
	if truthy(os.Getenv("DR_NGINX_SKIP_CONFIG")) {
		return nil
	}
	return os.WriteFile("/etc/nginx/nginx.conf", nginx.Config, 0o644)
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
	return nginxResponding(ctx)
}

func nginxResponding(ctx context.Context) bool {
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
	return resp.StatusCode == http.StatusOK
}

func truthy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}
