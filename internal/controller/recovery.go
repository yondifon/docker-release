package controller

import (
	"context"
	"log/slog"
	"time"

	"github.com/malico/docker-release/internal/state"
)

func (c *Controller) recoverInterruptedDeployments(ctx context.Context) {
	if c.project != "" {
		// Per-project mode: scan only the single project's state.
		c.recoverFromManager(ctx, c.project, c.stateManager)
		return
	}

	// Global mode: scan all project managers created so far. Projects whose
	// containers came up before Watch started may not have a manager yet, but
	// generateInitialConfigs ran first and would have created them via
	// managerFor calls in discoverServices/syncServicesConfigs, so by the time
	// we get here the map should be complete for the current container set.
	c.mu.Lock()
	snapshot := make(map[string]*state.Manager, len(c.stateManagers))
	for k, v := range c.stateManagers {
		snapshot[k] = v
	}
	c.mu.Unlock()

	for project, mgr := range snapshot {
		c.recoverFromManager(ctx, project, mgr)
	}
}

func (c *Controller) recoverFromManager(ctx context.Context, project string, mgr *state.Manager) {
	states, err := mgr.ListAll()
	if err != nil {
		slog.Error("error reading state", "component", "recovery", "project", project, "err", err)
		return
	}
	for _, ds := range states {
		if ds.Status == state.StatusIdle {
			continue
		}
		c.recoverService(ctx, project, ds)
	}
}

func (c *Controller) recoverService(ctx context.Context, project string, ds *state.DeploymentState) {
	key := serviceKey{project: project, service: ds.Service}
	slog.Info("recovering interrupted deployment", "component", "recovery", "service", key.deployKey(),
		"status", string(ds.Status), "strategy", ds.Strategy, "updated", ds.UpdatedAt.UTC().Format(time.RFC3339))

	if !c.anyContainerRunning(ctx, ds.Containers.Canary) {
		slog.Info("no live canary containers, resetting to idle", "component", "recovery", "service", key.deployKey())
		ds.Status = state.StatusIdle
		ds.Containers.Canary = nil
		if err := c.managerFor(project).Save(ds); err != nil {
			slog.Error("error saving state", "component", "recovery", "service", key.deployKey(), "err", err)
		}
		return
	}

	slog.Info("found live canary containers, rolling back to free resources", "component", "recovery", "service", key.deployKey())
	if err := c.Rollback(ctx, project, ds.Service); err != nil {
		slog.Error("rollback error", "component", "recovery", "service", key.deployKey(), "err", err)
		return
	}
	slog.Info("rolled back successfully", "component", "recovery", "service", key.deployKey())
}

func (c *Controller) anyContainerRunning(ctx context.Context, ids []string) bool {
	for _, id := range ids {
		info, err := c.docker.Inspect(ctx, id)
		if err != nil {
			continue
		}
		if info.State.Running {
			return true
		}
	}
	return false
}
