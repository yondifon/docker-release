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

// EnqueueRelease writes a detached release command for the given service to
// disk so the watch daemon picks it up on the next poll tick.
func (c *Controller) EnqueueRelease(project, service string, force bool) error {
	project = c.effectiveProject(project)
	cmd, err := c.managerFor(project).EnqueueReleaseCommand(service, force)
	if err != nil {
		return err
	}

	slog.Info("queued detached release", "component", "controller", "service", serviceKey{project: project, service: service}.deployKey(), "command", cmd.ID)
	return nil
}

func (c *Controller) processReleaseCommands(ctx context.Context) {
	var commands []state.QueuedReleaseCommand
	var err error

	if c.project != "" {
		// Per-project mode: only scan this project's command queue.
		commands, err = c.stateManager.PendingReleaseCommands()
	} else {
		// Global mode: scan all projects' command directories.
		commands, err = state.ScanAllPendingCommands(c.stateBaseDir)
	}

	if err != nil {
		slog.Error("error reading release commands", "component", "controller", "err", err)
		return
	}

	for _, cmd := range commands {
		project := cmd.Project
		if project == "" && c.project != "" {
			project = c.project
		}

		mgr := c.managerFor(project)

		claimed, ok, err := mgr.ClaimReleaseCommand(cmd)
		if err != nil {
			slog.Error("error claiming release command", "component", "controller", "command", cmd.ID, "err", err)
			continue
		}
		if !ok {
			continue
		}

		key := serviceKey{project: project, service: claimed.Service}
		slog.Info("processing detached release", "component", "controller", "service", key.deployKey(), "command", claimed.ID)
		if err := c.Release(ctx, project, claimed.Service, claimed.Force); err != nil {
			slog.Error("detached release failed", "component", "controller", "service", key.deployKey(), "command", claimed.ID, "err", err)
		}

		if err := mgr.CompleteReleaseCommand(claimed); err != nil {
			slog.Error("error completing release command", "component", "controller", "command", claimed.ID, "err", err)
		}
	}
}

// Release triggers a deployment for the given project and service. project is
// required in global mode (c.project == "") so the correct state manager and
// container filter are used.
func (c *Controller) Release(ctx context.Context, project, service string, force bool) error {
	project = c.effectiveProject(project)
	mgr := c.managerFor(project)
	key := serviceKey{project: project, service: service}

	ds, err := mgr.Load(service)
	if err != nil {
		return fmt.Errorf("loading state: %w", err)
	}

	if ds.Status == state.StatusInProgress && !ds.IsStale(state.DefaultStaleThreshold) && !force {
		return fmt.Errorf("deployment already in progress for %q (started %s) — use --force to override", key.deployKey(), formatTimestamp(ds.UpdatedAt))
	}

	release, err := mgr.AcquireDeployLock(service)
	if err != nil {
		if errors.Is(err, state.ErrDeployLocked) {
			return fmt.Errorf("deployment already running for %q in another process", key.deployKey())
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

	containers, err := c.docker.ListManagedContainers(ctx, project)
	if err != nil {
		return fmt.Errorf("listing containers: %w", err)
	}

	serviceContainers := filterServiceContainers(containers, key)

	if len(serviceContainers) == 0 {
		return fmt.Errorf("no managed containers found for service %q", key.deployKey())
	}

	revisions := groupByRevision(serviceContainers)

	if len(revisions) >= 2 {
		oldContainers, newContainers := splitByRevision(serviceContainers, revisions)
		cfg, err := config.ParseLabels(newContainers[0].Labels)
		if err != nil {
			return fmt.Errorf("parsing labels: %w", err)
		}

		c.resolveNginxProxyUpstream(ctx, cfg, newContainers)

		slog.Info("releasing", "component", "controller", "service", key.deployKey(), "old", len(oldContainers), "new", len(newContainers))
		releaseOnReturn = nil
		c.wg.Add(1)
		go func() {
			defer c.wg.Done()
			c.deploy(ctx, key, cfg, oldContainers, newContainers, release)
		}()
		return nil
	}

	cfg, err := config.ParseLabels(serviceContainers[0].Labels)
	if err != nil {
		return fmt.Errorf("parsing labels: %w", err)
	}

	c.resolveNginxProxyUpstream(ctx, cfg, serviceContainers)

	newContainers, err := c.scaleUp(ctx, key, serviceContainers)
	if err != nil {
		return fmt.Errorf("scaling up: %w", err)
	}

	slog.Info("releasing", "component", "controller", "service", key.deployKey(), "old", len(serviceContainers), "new", len(newContainers))
	releaseOnReturn = nil
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		c.deploy(ctx, key, cfg, serviceContainers, newContainers, release)
	}()

	return nil
}

func (c *Controller) scaleUp(ctx context.Context, key serviceKey, existing []types.Container) ([]types.Container, error) {
	slog.Info("scaling up: creating containers from image", "component", "controller", "count", len(existing))

	maxNum := c.docker.MaxServiceContainerNumber(ctx, key.project, key.service)

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

	// Filter using the explicit project so global mode doesn't accidentally
	// include same-named services from other projects.
	allContainers, err := c.docker.ListManagedContainers(ctx, key.project)
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
