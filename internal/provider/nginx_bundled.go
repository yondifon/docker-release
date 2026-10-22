//go:build bundled_nginx

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

func confirmBundledNginxReload(ctx context.Context, reload func() error) error {
	if !strings.EqualFold(strings.TrimSpace(os.Getenv("DR_BUNDLED_PROXY")), "nginx") {
		return reload()
	}
	data, err := os.ReadFile("/run/docker-release/nginx.started")
	if err != nil {
		return fmt.Errorf("reading bundled nginx process: %w", err)
	}
	pid := strings.TrimSpace(string(data))
	if number, err := strconv.Atoi(pid); err != nil || number < 1 {
		return fmt.Errorf("bundled nginx process is unknown; restart the controller")
	}
	// `nginx -s reload` returns before the master applies the config.
	before, err := nginxWorkers(pid)
	if err != nil {
		return err
	}
	if len(before) == 0 {
		return fmt.Errorf("bundled nginx has no workers; keep master_process enabled in its config")
	}
	if err := reload(); err != nil {
		return err
	}
	return waitForNginxWorkers(ctx, pid, before)
}

func nginxWorkers(pid string) (map[string]bool, error) {
	path := filepath.Join("/proc", pid, "task", pid, "children")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading bundled nginx workers: %w", err)
	}
	children := make(map[string]bool)
	for _, child := range strings.Fields(string(data)) {
		command, err := os.ReadFile(filepath.Join("/proc", child, "cmdline"))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("reading bundled nginx worker %s: %w", child, err)
		}
		if strings.HasPrefix(string(command), "nginx: worker process") {
			children[child] = true
		}
	}
	return children, nil
}

func waitForNginxWorkers(ctx context.Context, pid string, before map[string]bool) error {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		after, err := nginxWorkers(pid)
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
			return fmt.Errorf("nginx did not accept the reload; check the nginx logs: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}
