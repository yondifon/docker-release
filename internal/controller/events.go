package controller

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/malico/docker-release/internal/config"
	"github.com/malico/docker-release/internal/state"

	"github.com/docker/docker/api/types"
)

func (c *Controller) serviceFromEvent(ctx context.Context, containerID string, attrs map[string]string) string {
	var cached *types.ContainerJSON
	getInfo := func() *types.ContainerJSON {
		if cached != nil {
			return cached
		}
		info, err := c.docker.Inspect(ctx, containerID)
		if err != nil {
			return nil
		}
		cached = &info
		return cached
	}

	if c.project != "" {
		eventProject := attrs["com.docker.compose.project"]
		if eventProject == "" {
			if info := getInfo(); info != nil && info.Config != nil && info.Config.Labels != nil {
				eventProject = info.Config.Labels["com.docker.compose.project"]
			}
		}
		if eventProject != c.project {
			return ""
		}
	}

	if serviceName := attrs["com.docker.compose.service"]; serviceName != "" {
		return serviceName
	}

	info := getInfo()
	if info == nil || info.Config == nil || info.Config.Labels == nil {
		return ""
	}
	return info.Config.Labels["com.docker.compose.service"]
}

func (c *Controller) handleDie(ctx context.Context, containerID string, attrs map[string]string) {
	serviceName := c.serviceFromEvent(ctx, containerID, attrs)
	if serviceName == "" {
		c.refreshAllConfigs(ctx)
		return
	}

	exitCode := attrs["exitCode"]

	c.mu.Lock()
	_, deploying := c.deployments[serviceName]
	c.mu.Unlock()

	if deploying {
		slog.Info("container died during deployment", "component", "controller", "container", containerID[:12], "service", serviceName, "exit", exitCode)
		return
	}

	slog.Info("container died", "component", "controller", "container", containerID[:12], "service", serviceName, "exit", exitCode)

	c.refreshServiceConfig(ctx, serviceName)
	c.refreshServiceConfigAfter(ctx, serviceName, 2*time.Second)
}

func (c *Controller) handleStart(ctx context.Context, containerID string, attrs map[string]string) {
	serviceName := c.serviceFromEvent(ctx, containerID, attrs)
	if serviceName == "" {
		return
	}

	slog.Info("container started", "component", "controller", "container", containerID[:12], "service", serviceName)

	containers, err := c.docker.ListManagedContainers(ctx, c.project)
	if err != nil {
		slog.Error("error listing containers", "component", "controller", "err", err)
		return
	}

	serviceContainers := filterServiceContainers(containers, serviceName)

	if len(serviceContainers) < 2 {
		c.refreshServiceConfig(ctx, serviceName)
		c.refreshServiceConfigAfter(ctx, serviceName, 2*time.Second)
		return
	}

	revisions := groupByRevision(serviceContainers)

	if len(revisions) < 2 {
		c.refreshServiceConfig(ctx, serviceName)
		c.refreshServiceConfigAfter(ctx, serviceName, 2*time.Second)
		return
	}

	old, new := separateByRevision(serviceContainers, revisions, containerID)

	if len(old) == 0 || len(new) == 0 {
		c.refreshServiceConfig(ctx, serviceName)
		c.refreshServiceConfigAfter(ctx, serviceName, 2*time.Second)
		return
	}

	ds, err := c.stateManager.Load(serviceName)
	if err != nil {
		slog.Error("error loading state", "component", "controller", "service", serviceName, "err", err)
		return
	}

	if ds.Status == state.StatusInProgress {
		if !ds.IsStale(state.DefaultStaleThreshold) {
			slog.Info("deployment already in progress, skipping", "component", "controller", "service", serviceName)
			return
		}

		slog.Info("clearing stale deployment state", "component", "controller", "service", serviceName, "updated", formatTimestamp(ds.UpdatedAt))
	}

	cfg, err := config.ParseLabels(new[0].Labels)
	if err != nil {
		slog.Error("error parsing labels", "component", "controller", "service", serviceName, "err", err)
		return
	}

	c.resolveNginxProxyUpstream(ctx, cfg, new)

	release, err := c.stateManager.AcquireDeployLock(serviceName)
	if err != nil {
		if errors.Is(err, state.ErrDeployLocked) {
			slog.Info("deployment already running in another process, skipping", "component", "controller", "service", serviceName)
			return
		}
		slog.Error("error acquiring deploy lock", "component", "controller", "service", serviceName, "err", err)
		return
	}

	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		c.deploy(ctx, serviceName, cfg, old, new, release)
	}()
}

func (c *Controller) handleHealthStatus(ctx context.Context, containerID string, attrs map[string]string) {
	serviceName := c.serviceFromEvent(ctx, containerID, attrs)
	if serviceName == "" {
		return
	}

	slog.Info("health status changed", "component", "controller", "container", containerID[:12], "service", serviceName)

	c.refreshServiceConfig(ctx, serviceName)
}
