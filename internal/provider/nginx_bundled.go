//go:build bundled_nginx || bundled_angie

package provider

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func confirmBundledReload(ctx context.Context, name string, reload func() error) error {
	if !strings.EqualFold(strings.TrimSpace(os.Getenv("DR_BUNDLED_PROXY")), name) {
		return reload()
	}
	data, err := os.ReadFile("/run/docker-release/" + name + ".started")
	if err != nil {
		return fmt.Errorf("reading bundled %s process: %w", name, err)
	}
	pid := strings.TrimSpace(string(data))
	if number, err := strconv.Atoi(pid); err != nil || number < 1 {
		return fmt.Errorf("bundled %s process is unknown; restart the controller", name)
	}
	// `nginx -s reload` and `angie -s reload` return before the config is applied.
	before, err := proxyWorkers(name, pid)
	if err != nil {
		return err
	}
	if len(before) == 0 {
		return fmt.Errorf("bundled %s has no workers; keep master_process enabled in its config", name)
	}
	if err := reload(); err != nil {
		return err
	}
	return waitForProxyWorkers(ctx, name, pid, before)
}

func proxyWorkers(name, pid string) (map[string]bool, error) {
	path := filepath.Join("/proc", pid, "task", pid, "children")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading bundled %s workers: %w", name, err)
	}
	children := make(map[string]bool)
	for _, child := range strings.Fields(string(data)) {
		command, err := os.ReadFile(filepath.Join("/proc", child, "cmdline"))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("reading bundled %s worker %s: %w", name, child, err)
		}
		if strings.HasPrefix(string(command), name+": worker process") {
			children[child] = true
		}
	}
	return children, nil
}

func waitForProxyWorkers(ctx context.Context, name, pid string, before map[string]bool) error {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		after, err := proxyWorkers(name, pid)
		if err != nil {
			return err
		}
		for child := range after {
			if !before[child] {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("%s did not accept the reload; check the proxy logs: %w", name, ctx.Err())
		case <-ticker.C:
		}
	}
}
