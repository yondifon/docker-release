package controller

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/malico/docker-release/internal/config"
	"github.com/malico/docker-release/internal/docker"
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

	if _, err := config.ParseLabels(serviceContainers[0].Labels); err != nil {
		return fmt.Errorf("parsing labels: %w", err)
	}

	newContainers, cfg, err := c.scaleUp(ctx, key, serviceContainers)
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

// scaleUp starts one replacement for each existing container and returns them
// with the service config read from their labels. It lets Docker Compose create
// them when the project's files are readable here, so they get what `docker
// compose up` would give them; otherwise it copies the running containers.
// Replacements the proxy can't reach are removed and reported as an error.
func (c *Controller) scaleUp(ctx context.Context, key serviceKey, existing []types.Container) ([]types.Container, *config.ServiceConfig, error) {
	var created []string
	var err error
	if project, ok := c.composeProject(key, existing[0]); ok {
		created, err = c.composeScaleUp(ctx, key, project, existing)
	} else {
		created, err = c.cloneScaleUp(ctx, existing, key)
	}
	if err != nil {
		return nil, nil, err
	}
	removeNew := func() {
		for _, id := range created {
			_ = c.docker.Remove(context.Background(), id)
		}
	}

	// Filter using the explicit project so global mode doesn't accidentally
	// include same-named services from other projects.
	allContainers, err := c.docker.ListManagedContainers(ctx, key.project)
	if err != nil {
		removeNew()
		return nil, nil, fmt.Errorf("listing containers: %w", err)
	}

	createdSet := make(map[string]bool, len(created))
	for _, id := range created {
		createdSet[id] = true
	}
	var newContainers []types.Container
	for _, ctr := range allContainers {
		if createdSet[ctr.ID] {
			newContainers = append(newContainers, ctr)
		}
	}
	if len(newContainers) != len(existing) {
		removeNew()
		return nil, nil, fmt.Errorf("expected %d new containers, found %d; check that the service keeps its release.enable label", len(existing), len(newContainers))
	}

	cfg, err := config.ParseLabels(newContainers[0].Labels)
	if err != nil {
		removeNew()
		return nil, nil, fmt.Errorf("parsing labels: %w", err)
	}
	c.resolveNginxProxyUpstream(ctx, cfg, newContainers)

	if _, err := c.resolveContainers(ctx, c.addrResolver(ctx, cfg), newContainers, cfg.Provider != config.ProviderNone); err != nil {
		removeNew()
		return nil, nil, fmt.Errorf("new containers are not reachable by the proxy: %w", err)
	}

	return newContainers, cfg, nil
}

// composeProject returns the Compose project of a container when Compose can
// be run for it from here. When it can't, it logs why, once per service.
func (c *Controller) composeProject(key serviceKey, ctr types.Container) (docker.ComposeProject, bool) {
	project, ok := docker.ComposeProjectOf(ctr.Labels)
	if !ok {
		return project, false
	}
	if err := project.Available(); err != nil {
		if _, logged := c.composeWarned.LoadOrStore(key, true); !logged {
			slog.Warn("compose files are not mounted in the controller; copying the running container, so compose file changes will not apply", "component", "controller", "service", key.deployKey(), "err", err)
		}
		return project, false
	}
	return project, true
}

func (c *Controller) composeScaleUp(ctx context.Context, key serviceKey, project docker.ComposeProject, existing []types.Container) ([]string, error) {
	slog.Info("scaling up: creating containers with docker compose", "component", "controller", "service", key.deployKey(), "count", len(existing))

	before, err := c.serviceContainerIDs(ctx, key)
	if err != nil {
		return nil, err
	}

	runErr := c.docker.ScaleService(ctx, project, key.service, len(existing)*2)

	after, err := c.serviceContainerIDs(ctx, key)
	if err != nil && runErr == nil {
		runErr = err
	}
	var created []string
	for id := range after {
		if !before[id] {
			created = append(created, id)
		}
	}
	if runErr != nil {
		for _, id := range created {
			_ = c.docker.Remove(context.Background(), id)
		}
		return nil, runErr
	}
	return created, nil
}

func (c *Controller) serviceContainerIDs(ctx context.Context, key serviceKey) (map[string]bool, error) {
	containers, err := c.docker.ListServiceContainers(ctx, key.project, key.service)
	if err != nil {
		return nil, fmt.Errorf("listing containers: %w", err)
	}
	ids := make(map[string]bool, len(containers))
	for _, ctr := range containers {
		ids[ctr.ID] = true
	}
	return ids, nil
}

func (c *Controller) cloneScaleUp(ctx context.Context, existing []types.Container, key serviceKey) ([]string, error) {
	slog.Info("scaling up: copying running containers", "component", "controller", "count", len(existing))

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
	return newIDs, nil
}
