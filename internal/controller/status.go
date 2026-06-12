package controller

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/malico/docker-release/internal/state"
)

// Status prints the deployment state for one service, or all managed services
// when service is empty.
func (c *Controller) Status(ctx context.Context, service string) error {
	if service == "" {
		return c.statusAll(ctx)
	}

	s, err := c.stateManager.Load(service)
	if err != nil {
		return fmt.Errorf("loading state: %w", err)
	}

	statusStr := string(s.Status)
	if s.IsStale(state.DefaultStaleThreshold) {
		statusStr += " (stale)"
	}

	fmt.Printf("Service:    %s\n", s.Service)
	fmt.Printf("Status:     %s\n", statusStr)
	fmt.Printf("Strategy:   %s\n", s.Strategy)
	fmt.Printf("Updated:    %s\n", formatTimestamp(s.UpdatedAt))
	fmt.Printf("Weight:     %d%%\n", s.CurrentWeight)
	fmt.Printf("Stable:     %v\n", s.Containers.Stable)
	fmt.Printf("Canary:     %v\n", s.Containers.Canary)

	return nil
}

func (c *Controller) statusAll(ctx context.Context) error {
	containers, err := c.docker.ListManagedContainers(ctx, c.project)
	if err != nil {
		return fmt.Errorf("listing containers: %w", err)
	}

	services := make(map[string]bool)
	for _, ctr := range containers {
		name := ctr.Labels["com.docker.compose.service"]
		if name != "" {
			services[name] = true
		}
	}

	if len(services) == 0 {
		fmt.Println("no managed services found")
		return nil
	}

	for name := range services {
		s, err := c.stateManager.Load(name)
		if err != nil {
			slog.Error("loading state failed", "component", "controller", "service", name, "err", err)
			continue
		}

		status := string(s.Status)
		if status == "" {
			status = "idle"
		}

		if s.IsStale(state.DefaultStaleThreshold) {
			status += " (stale)"
		}

		fmt.Printf("%-20s %s\n", name, status)
	}

	return nil
}

func formatTimestamp(t time.Time) string {
	if t.IsZero() {
		return "unknown"
	}

	return t.Format("2006-01-02 15:04:05")
}
