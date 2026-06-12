package controller

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/malico/docker-release/internal/config"
	"github.com/malico/docker-release/internal/state"

	"github.com/docker/docker/api/types"
)

func (c *Controller) EnqueueRelease(service string, force bool) error {
	cmd, err := c.stateManager.EnqueueReleaseCommand(service, force)
	if err != nil {
		return err
	}

	slog.Info("queued detached release", "component", "controller", "service", service, "command", cmd.ID)
	return nil
}

func (c *Controller) processReleaseCommands(ctx context.Context) {
	commands, err := c.stateManager.PendingReleaseCommands()
	if err != nil {
		slog.Error("error reading release commands", "component", "controller", "err", err)
		return
	}

	for _, cmd := range commands {
		claimed, ok, err := c.stateManager.ClaimReleaseCommand(cmd)
		if err != nil {
			slog.Error("error claiming release command", "component", "controller", "command", cmd.ID, "err", err)
			continue
		}
		if !ok {
			continue
		}

		slog.Info("processing detached release", "component", "controller", "service", claimed.Service, "command", claimed.ID)
		if err := c.Release(ctx, claimed.Service, claimed.Force); err != nil {
			slog.Error("detached release failed", "component", "controller", "service", claimed.Service, "command", claimed.ID, "err", err)
		}

		if err := c.stateManager.CompleteReleaseCommand(claimed); err != nil {
			slog.Error("error completing release command", "component", "controller", "command", claimed.ID, "err", err)
		}
	}
}

func (c *Controller) Release(ctx context.Context, service string, force bool) error {
	ds, err := c.stateManager.Load(service)
	if err != nil {
		return fmt.Errorf("loading state: %w", err)
	}

	if ds.Status == state.StatusInProgress && !ds.IsStale(state.DefaultStaleThreshold) && !force {
		return fmt.Errorf("deployment already in progress for %q (started %s) — use --force to override", service, formatTimestamp(ds.UpdatedAt))
	}

	release, err := c.stateManager.AcquireDeployLock(service)
	if err != nil {
		if errors.Is(err, state.ErrDeployLocked) {
			return fmt.Errorf("deployment already running for %q in another process", service)
		}
		return fmt.Errorf("acquiring deploy lock: %w", err)
	}
	// Hold the lock until ownership transfers to the deploy goroutine; any early
	// return below releases it.
	releaseOnReturn := release
	defer func() {
		if releaseOnReturn != nil {
			releaseOnReturn()
		}
	}()

	containers, err := c.docker.ListManagedContainers(ctx, c.project)
	if err != nil {
		return fmt.Errorf("listing containers: %w", err)
	}

	serviceContainers := filterServiceContainers(containers, service)

	if len(serviceContainers) == 0 {
		return fmt.Errorf("no managed containers found for service %q", service)
	}

	revisions := groupByRevision(serviceContainers)

	if len(revisions) >= 2 {
		oldContainers, newContainers := splitByRevision(serviceContainers, revisions)
		cfg, err := config.ParseLabels(newContainers[0].Labels)
		if err != nil {
			return fmt.Errorf("parsing labels: %w", err)
		}

		c.resolveNginxProxyUpstream(ctx, cfg, newContainers)

		slog.Info("releasing", "component", "controller", "service", service, "old", len(oldContainers), "new", len(newContainers))
		releaseOnReturn = nil
		c.wg.Add(1)
		go func() {
			defer c.wg.Done()
			c.deploy(ctx, service, cfg, oldContainers, newContainers, release)
		}()
		return nil
	}

	cfg, err := config.ParseLabels(serviceContainers[0].Labels)
	if err != nil {
		return fmt.Errorf("parsing labels: %w", err)
	}

	c.resolveNginxProxyUpstream(ctx, cfg, serviceContainers)

	newContainers, err := c.scaleUp(ctx, serviceContainers)
	if err != nil {
		return fmt.Errorf("scaling up: %w", err)
	}

	slog.Info("releasing", "component", "controller", "service", service, "old", len(serviceContainers), "new", len(newContainers))
	releaseOnReturn = nil
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		c.deploy(ctx, service, cfg, serviceContainers, newContainers, release)
	}()

	return nil
}

func (c *Controller) scaleUp(ctx context.Context, existing []types.Container) ([]types.Container, error) {
	slog.Info("scaling up: creating containers from image", "component", "controller", "count", len(existing))

	var project, service string
	if len(existing) > 0 {
		project = existing[0].Labels["com.docker.compose.project"]
		service = existing[0].Labels["com.docker.compose.service"]
	}
	maxNum := c.docker.MaxServiceContainerNumber(ctx, project, service)

	var newIDs []string
	for i, ctr := range existing {
		newID, err := c.docker.CreateContainerFromImage(ctx, ctr, maxNum+1+i)
		if err != nil {
			for _, id := range newIDs {
				_ = c.docker.Remove(context.Background(), id)
			}
			return nil, err
		}
		newIDs = append(newIDs, newID)
	}

	newIDSet := make(map[string]bool, len(newIDs))
	for _, id := range newIDs {
		newIDSet[id] = true
	}

	allContainers, err := c.docker.ListManagedContainers(ctx, c.project)
	if err != nil {
		return nil, fmt.Errorf("listing containers: %w", err)
	}

	var newContainers []types.Container
	for _, ctr := range allContainers {
		if newIDSet[ctr.ID] {
			newContainers = append(newContainers, ctr)
		}
	}

	return newContainers, nil
}
