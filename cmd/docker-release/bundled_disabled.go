//go:build !bundled_nginx && !bundled_angie && !bundled_caddy && !bundled_haproxy && !bundled_traefik

package main

import (
	"context"
	"fmt"

	"github.com/malico/docker-release/internal/config"
)

type bundledProxy struct{}

func bundledProxyFromEnv() (*bundledProxy, error) {
	v := config.BundledProxy()
	switch v {
	case "", "none":
		return nil, nil
	default:
		return nil, fmt.Errorf("DR_BUNDLED_PROXY=%s requires a bundled dr build", v)
	}
}

func runWithBundledProxy(ctx context.Context, _ *bundledProxy, run func(context.Context) error) error {
	return run(ctx)
}

func bundledProxyHealthy() bool { return true }
