package controller

import (
	"context"
	"log/slog"
	"time"

	"github.com/malico/docker-release/internal/state"
)

func (c *Controller) recoverInterruptedDeployments(ctx context.Context) {
	states, err := c.stateManager.ListAll()
	if err != nil {
		slog.Error("error reading state", "component", "recovery", "err", err)
		return
	}
	for _, ds := range states {
		if ds.Status == state.StatusIdle {
			continue
		}
		c.recoverService(ctx, ds)
	}
}

func (c *Controller) recoverService(ctx context.Context, ds *state.DeploymentState) {
	slog.Info("recovering interrupted deployment", "component", "recovery", "service", ds.Service,
		"status", string(ds.Status), "strategy", ds.Strategy, "updated", ds.UpdatedAt.UTC().Format(time.RFC3339))

	if !c.anyContainerRunning(ctx, ds.Containers.Canary) {
		slog.Info("no live canary containers, resetting to idle", "component", "recovery", "service", ds.Service)
		ds.Status = state.StatusIdle
		ds.Containers.Canary = nil
		if err := c.stateManager.Save(ds); err != nil {
			slog.Error("error saving state", "component", "recovery", "service", ds.Service, "err", err)
		}
		return
	}

	slog.Info("found live canary containers, rolling back to free resources", "component", "recovery", "service", ds.Service)
	if err := c.Rollback(ctx, ds.Service); err != nil {
		slog.Error("rollback error", "component", "recovery", "service", ds.Service, "err", err)
		return
	}
	slog.Info("rolled back successfully", "component", "recovery", "service", ds.Service)
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
