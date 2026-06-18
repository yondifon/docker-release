//go:build !bundled_nginx

package main

import (
	"context"
	"fmt"
	"os"
	"strings"
)

type bundledProxy struct{}

func bundledProxyFromEnv() (*bundledProxy, error) {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("DR_BUNDLED_PROXY")))
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
