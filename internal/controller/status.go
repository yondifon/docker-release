package controller

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/malico/docker-release/internal/state"
)

// Status prints the deployment state for one service, or all managed services
// when service is empty. project is required in global mode; pass c.project or
// the value from --project.
func (c *Controller) Status(ctx context.Context, project, service string) error {
	project = c.effectiveProject(project)
	if service == "" {
		return c.statusAll(ctx, project)
	}

	mgr := c.managerFor(project)
	s, err := mgr.Load(service)
	if err != nil {
		return fmt.Errorf("loading state: %w", err)
	}

	statusStr := string(s.Status)
	if s.IsStale(state.DefaultStaleThreshold) {
		statusStr += " (stale)"
	}

	if project != "" {
		fmt.Printf("Project:    %s\n", project)
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

func (c *Controller) statusAll(ctx context.Context, project string) error {
	// In per-project mode, always use c.project. In global mode, project may be
	// "" (show all projects) or a specific value (show one project).
	effectiveProject := project
	if c.project != "" {
		effectiveProject = c.project
	}

	containers, err := c.docker.ListManagedContainers(ctx, effectiveProject)
	if err != nil {
		return fmt.Errorf("listing containers: %w", err)
	}

	type serviceEntry struct {
		project string
		service string
	}
	seen := make(map[serviceEntry]bool)
	var entries []serviceEntry

	for _, ctr := range containers {
		proj := ctr.Labels["com.docker.compose.project"]
		svc := ctr.Labels["com.docker.compose.service"]
		if svc == "" {
			continue
		}
		e := serviceEntry{project: proj, service: svc}
		if !seen[e] {
			seen[e] = true
			entries = append(entries, e)
		}
	}

	if len(entries) == 0 {
		fmt.Println("no managed services found")
		return nil
	}

	for _, e := range entries {
		s, err := c.managerFor(e.project).Load(e.service)
		if err != nil {
			slog.Error("loading state failed", "component", "controller", "service", e.service, "project", e.project, "err", err)
			continue
		}

		status := string(s.Status)
		if status == "" {
			status = "idle"
		}

		if s.IsStale(state.DefaultStaleThreshold) {
			status += " (stale)"
		}

		if c.project == "" {
			// Global mode: include project in output.
			fmt.Printf("%-20s %-20s %s\n", e.project, e.service, status)
		} else {
			fmt.Printf("%-20s %s\n", e.service, status)
		}
	}

	return nil
}

func formatTimestamp(t time.Time) string {
	if t.IsZero() {
		return "unknown"
	}

	return t.Format("2006-01-02 15:04:05")
}
