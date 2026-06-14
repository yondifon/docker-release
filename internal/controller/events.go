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

// serviceKeyFromEvent extracts the (project, service) identity from a Docker
// event. Returns (key, true) when the event belongs to a managed service in
// scope; (zero, false) otherwise.
func (c *Controller) serviceKeyFromEvent(ctx context.Context, containerID string, attrs map[string]string) (serviceKey, bool) {
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

	// Resolve project from event attrs, falling back to inspect.
	eventProject := attrs["com.docker.compose.project"]
	if eventProject == "" {
		if info := getInfo(); info != nil && info.Config != nil {
			eventProject = info.Config.Labels["com.docker.compose.project"]
		}
	}

	// In per-project mode, drop events from other projects.
	if c.project != "" && eventProject != c.project {
		return serviceKey{}, false
	}

	// Resolve service from event attrs, falling back to inspect.
	serviceName := attrs["com.docker.compose.service"]
	if serviceName == "" {
		info := getInfo()
		if info == nil || info.Config == nil {
			return serviceKey{}, false
		}
		serviceName = info.Config.Labels["com.docker.compose.service"]
	}
	if serviceName == "" {
		return serviceKey{}, false
	}

	return serviceKey{project: eventProject, service: serviceName}, true
}

func (c *Controller) handleDie(ctx context.Context, containerID string, attrs map[string]string) {
	key, ok := c.serviceKeyFromEvent(ctx, containerID, attrs)
	if !ok {
		c.refreshAllConfigs(ctx)
		return
	}

	exitCode := attrs["exitCode"]

	c.mu.Lock()
	_, deploying := c.deployments[key.deployKey()]
	c.mu.Unlock()

	if deploying {
		slog.Info("container died during deployment", "component", "controller", "container", containerID[:12], "service", key.deployKey(), "exit", exitCode)
		return
	}

	slog.Info("container died", "component", "controller", "container", containerID[:12], "service", key.deployKey(), "exit", exitCode)

	c.refreshServiceConfig(ctx, key)
	c.refreshServiceConfigAfter(ctx, key, 2*time.Second)
}

func (c *Controller) handleStart(ctx context.Context, containerID string, attrs map[string]string) {
	key, ok := c.serviceKeyFromEvent(ctx, containerID, attrs)
	if !ok {
		return
	}

	slog.Info("container started", "component", "controller", "container", containerID[:12], "service", key.deployKey())

	containers, err := c.docker.ListManagedContainers(ctx, key.project)
	if err != nil {
		slog.Error("error listing containers", "component", "controller", "err", err)
		return
	}

	serviceContainers := filterServiceContainers(containers, key)

	if len(serviceContainers) < 2 {
		c.refreshServiceConfig(ctx, key)
		c.refreshServiceConfigAfter(ctx, key, 2*time.Second)
		return
	}

	revisions := groupByRevision(serviceContainers)

	if len(revisions) < 2 {
		c.refreshServiceConfig(ctx, key)
		c.refreshServiceConfigAfter(ctx, key, 2*time.Second)
		return
	}

	old, new := separateByRevision(serviceContainers, revisions, containerID)

	if len(old) == 0 || len(new) == 0 {
		c.refreshServiceConfig(ctx, key)
		c.refreshServiceConfigAfter(ctx, key, 2*time.Second)
		return
	}

	ds, err := c.managerFor(key.project).Load(key.service)
	if err != nil {
		slog.Error("error loading state", "component", "controller", "service", key.deployKey(), "err", err)
		return
	}

	if ds.Status == state.StatusInProgress {
		if !ds.IsStale(state.DefaultStaleThreshold) {
			slog.Info("deployment already in progress, skipping", "component", "controller", "service", key.deployKey())
			return
		}

		slog.Info("clearing stale deployment state", "component", "controller", "service", key.deployKey(), "updated", formatTimestamp(ds.UpdatedAt))
	}

	cfg, err := config.ParseLabels(new[0].Labels)
	if err != nil {
		slog.Error("error parsing labels", "component", "controller", "service", key.deployKey(), "err", err)
		return
	}

	c.resolveNginxProxyUpstream(ctx, cfg, new)

	release, err := c.managerFor(key.project).AcquireDeployLock(key.service)
	if err != nil {
		if errors.Is(err, state.ErrDeployLocked) {
			slog.Info("deployment already running in another process, skipping", "component", "controller", "service", key.deployKey())
			return
		}
		slog.Error("error acquiring deploy lock", "component", "controller", "service", key.deployKey(), "err", err)
		return
	}

	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		c.deploy(ctx, key, cfg, old, new, release)
	}()
}

func (c *Controller) handleHealthStatus(ctx context.Context, containerID string, attrs map[string]string) {
	key, ok := c.serviceKeyFromEvent(ctx, containerID, attrs)
	if !ok {
		return
	}

	slog.Info("health status changed", "component", "controller", "container", containerID[:12], "service", key.deployKey())

	c.refreshServiceConfig(ctx, key)
}
