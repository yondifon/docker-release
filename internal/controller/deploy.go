package controller

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/malico/docker-release/internal/config"
	"github.com/malico/docker-release/internal/monitor"
	"github.com/malico/docker-release/internal/provider"
	"github.com/malico/docker-release/internal/state"
	"github.com/malico/docker-release/internal/strategy"

	"github.com/docker/docker/api/types"
)

func (c *Controller) deploy(parentCtx context.Context, serviceName string, cfg *config.ServiceConfig, oldContainers, newContainers []types.Container, releaseLock func()) {
	c.mu.Lock()
	if d, ok := c.deployments[serviceName]; ok {
		d.cancel()
	}

	ctx, cancel := context.WithCancel(parentCtx)
	deployID := state.GenerateDeploymentID()
	c.deployments[serviceName] = activeDeployment{id: deployID, cancel: cancel}
	c.mu.Unlock()

	// Capture the previous deployment's ID before the early save overwrites it,
	// and use one ID for both the tracking map and the persisted state so the
	// API can cancel by the ID it reports.
	prevDeployID := ""
	if prev, err := c.stateManager.Load(serviceName); err == nil {
		prevDeployID = prev.ActiveDeploymentID
	}

	ds := &state.DeploymentState{
		Service:              serviceName,
		Status:               state.StatusInProgress,
		Strategy:             string(cfg.Strategy),
		ActiveDeploymentID:   deployID,
		PreviousDeploymentID: prevDeployID,
	}
	if err := c.stateManager.Save(ds); err != nil {
		slog.Error("error saving early state", "component", "controller", "service", serviceName, "err", err)
	}

	defer func() {
		c.mu.Lock()
		if d, ok := c.deployments[serviceName]; ok && d.id == deployID {
			delete(c.deployments, serviceName)
		}
		c.mu.Unlock()
		cancel()
		if releaseLock != nil {
			releaseLock()
		}
	}()

	slog.Info("starting deployment", "component", "controller", "service", serviceName, "strategy", cfg.Strategy)

	expected := len(oldContainers)
	if len(newContainers) < expected {
		newContainers = c.waitForContainers(ctx, serviceName, containerRevision(newContainers[0]), expected)
	}

	prov, err := c.factory.Provider(cfg)
	if err != nil {
		slog.Error("error building provider", "component", "controller", "service", serviceName, "err", err)
		return
	}

	resolveAddr := cfg.Provider != config.ProviderNone

	oldInfos := c.resolveContainers(ctx, oldContainers, resolveAddr)
	newInfos := c.resolveContainers(ctx, newContainers, resolveAddr)

	d := &strategy.Deployment{
		Service:      serviceName,
		Config:       cfg,
		Old:          oldInfos,
		New:          newInfos,
		DeployID:     deployID,
		PrevDeployID: prevDeployID,
	}

	deployCtx, deployCancel := context.WithCancel(ctx)
	defer deployCancel()

	newIDs := make([]string, len(newInfos))
	for i, info := range newInfos {
		newIDs[i] = info.ID
	}

	strat := strategy.New(cfg, c.docker, prov, c.stateManager)

	mon := monitor.NewHealthMonitor(c.docker, newIDs, func(containerID, reason string) {
		slog.Warn("auto-rollback triggered", "component", "controller", "service", serviceName, "reason", reason)
		deployCancel()
	})
	mon.SetGracePeriod(cfg.HealthCheckTimeout)

	go func() {
		if err := mon.Run(deployCtx); err != nil && !errors.Is(err, monitor.ErrUnhealthy) && deployCtx.Err() == nil {
			slog.Warn("health monitor stopped early; deployment proceeding unmonitored", "component", "controller", "service", serviceName, "err", err)
		}
	}()

	if err := strat.Execute(deployCtx, d); err != nil {
		slog.Error("deployment failed", "component", "controller", "service", serviceName, "err", err)

		rollbackCtx, rollbackCancel := context.WithTimeout(context.Background(), cfg.HealthCheckTimeout+cfg.DrainTimeout+30*time.Second)
		defer rollbackCancel()

		slog.Info("initiating abort rollback", "component", "controller", "service", serviceName)
		if rbErr := c.abortDeployment(rollbackCtx, serviceName, cfg, prov, d); rbErr != nil {
			slog.Error("rollback failed", "component", "controller", "service", serviceName, "err", rbErr)
		}
		return
	}

	slog.Info("deployment complete", "component", "controller", "service", serviceName)
}

func (c *Controller) abortDeployment(ctx context.Context, serviceName string, cfg *config.ServiceConfig, prov provider.Provider, d *strategy.Deployment) error {
	targets, err := c.abortTargets(ctx, serviceName, d)
	if err != nil {
		return err
	}
	if len(targets) == 0 {
		return fmt.Errorf("no live rollback targets for %s", serviceName)
	}

	upstream := &provider.UpstreamState{
		Service:      serviceName,
		UpstreamName: d.UpstreamName(),
		Affinity:     cfg.Affinity,
	}

	for _, target := range targets {
		upstream.Servers = append(upstream.Servers, provider.Server{Addr: target.Addr})
	}
	strategy.ApplyProviderSettings(cfg, upstream)

	if err := prov.GenerateConfig(ctx, upstream); err != nil {
		return fmt.Errorf("generating abort rollback config: %w", err)
	}

	if err := prov.Reload(ctx); err != nil {
		return fmt.Errorf("reloading abort rollback config: %w", err)
	}

	select {
	case <-time.After(cfg.DrainTimeout):
	case <-ctx.Done():
		return ctx.Err()
	}

	targetIDs := make(map[string]bool, len(targets))
	for _, target := range targets {
		targetIDs[target.ID] = true
	}

	for _, newContainer := range d.New {
		if targetIDs[newContainer.ID] {
			continue
		}

		if err := c.docker.Stop(ctx, newContainer.ID, 10); err != nil {
			slog.Warn("abort rollback: stop failed", "component", "controller", "container", newContainer.ID[:12], "err", err)
		}

		if err := c.docker.Remove(ctx, newContainer.ID); err != nil {
			slog.Warn("abort rollback: remove failed", "component", "controller", "container", newContainer.ID[:12], "err", err)
		}
	}

	return c.stateManager.Save(&state.DeploymentState{
		Service:    serviceName,
		Status:     state.StatusIdle,
		Strategy:   string(cfg.Strategy),
		Containers: state.Containers{Stable: containerInfoIDs(targets)},
	})
}

func (c *Controller) abortTargets(ctx context.Context, serviceName string, d *strategy.Deployment) ([]strategy.ContainerInfo, error) {
	containersByID := make(map[string]strategy.ContainerInfo, len(d.Old)+len(d.New))
	for _, info := range d.Old {
		containersByID[info.ID] = info
	}
	for _, info := range d.New {
		containersByID[info.ID] = info
	}

	ds, err := c.stateManager.Load(serviceName)
	if err != nil {
		return nil, fmt.Errorf("loading abort state: %w", err)
	}

	ids := ds.Containers.Stable
	if len(ids) == 0 {
		ids = containerInfoIDs(d.Old)
	}

	targets := make([]strategy.ContainerInfo, 0, len(ids))
	for _, id := range ids {
		info, ok := containersByID[id]
		if !ok {
			continue
		}
		if _, err := c.docker.Inspect(ctx, info.ID); err != nil {
			continue
		}
		targets = append(targets, info)
	}

	return targets, nil
}

func containerInfoIDs(containers []strategy.ContainerInfo) []string {
	ids := make([]string, len(containers))
	for i, container := range containers {
		ids[i] = container.ID
	}
	return ids
}

func (c *Controller) resolveNginxProxyUpstream(ctx context.Context, cfg *config.ServiceConfig, containers []types.Container) {
	if cfg.Provider != config.ProviderNginxProxy || cfg.UpstreamName != "" || len(containers) == 0 {
		return
	}
	env, err := c.docker.ContainerEnv(ctx, containers[0].ID)
	if err != nil {
		slog.Warn("could not read container env for nginx-proxy upstream", "component", "controller", "err", err)
		return
	}
	name, err := provider.NginxProxyUpstreamName(env)
	if err != nil {
		slog.Warn("could not resolve nginx-proxy upstream name", "component", "controller", "err", err)
		return
	}
	cfg.UpstreamName = name
}

func (c *Controller) waitForContainers(ctx context.Context, serviceName, revision string, expected int) []types.Container {
	timeout := 30 * time.Second
	deadline := time.After(timeout)
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	slog.Info("waiting for new containers (have fewer)", "component", "controller", "service", serviceName, "expected", expected)

	for {
		select {
		case <-deadline:
			slog.Warn("timed out waiting for new containers, proceeding with what's available", "component", "controller", "service", serviceName, "expected", expected)
			return c.listContainersByRevision(ctx, serviceName, revision)
		case <-ctx.Done():
			return c.listContainersByRevision(ctx, serviceName, revision)
		case <-ticker.C:
			found := c.listContainersByRevision(ctx, serviceName, revision)
			if len(found) >= expected {
				slog.Info("found new containers", "component", "controller", "service", serviceName, "found", len(found), "expected", expected)
				return found
			}
		}
	}
}

func (c *Controller) listContainersByRevision(ctx context.Context, serviceName, revision string) []types.Container {
	containers, err := c.docker.ListManagedContainers(ctx, c.project)
	if err != nil {
		return nil
	}

	var matched []types.Container
	for _, ctr := range filterServiceContainers(containers, serviceName) {
		if containerRevision(ctr) == revision {
			matched = append(matched, ctr)
		}
	}

	return matched
}

func (c *Controller) resolveContainers(ctx context.Context, containers []types.Container, resolveAddr bool) []strategy.ContainerInfo {
	var infos []strategy.ContainerInfo

	for _, ctr := range containers {
		info := strategy.ContainerInfo{ID: ctr.ID}

		if resolveAddr {
			addr, err := c.docker.ContainerAddr(ctx, ctr.ID)
			if err != nil {
				slog.Warn("resolving container address failed", "component", "controller", "container", ctr.ID[:12], "err", err)
				continue
			}
			info.Addr = addr
		}

		infos = append(infos, info)
	}

	return infos
}
