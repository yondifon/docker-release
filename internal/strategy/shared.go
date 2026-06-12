package strategy

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/malico/docker-release/internal/provider"
	"github.com/malico/docker-release/internal/state"
)

// waitAllHealthy blocks until every new container reports healthy, used by
// strategies that stand up all replacements before shifting any traffic.
func waitAllHealthy(ctx context.Context, tag string, docker DockerOps, d *Deployment) error {
	for _, c := range d.New {
		slog.Info("waiting for container to be healthy", "component", tag, "container", c.ID[:12])
		if err := docker.WaitHealthy(ctx, c.ID, d.Config.HealthCheckTimeout); err != nil {
			return fmt.Errorf("health check failed for %s: %w", c.ID[:12], err)
		}
	}
	return nil
}

// removeContainers stops then removes each container, logging (not failing) on
// error since a teardown best-effort: the deployment has already moved on.
func removeContainers(ctx context.Context, tag string, docker DockerOps, containers []ContainerInfo) {
	for _, c := range containers {
		if err := docker.Stop(ctx, c.ID, 10); err != nil {
			slog.Warn("stop failed", "component", tag, "container", c.ID[:12], "err", err)
		}
		if err := docker.Remove(ctx, c.ID); err != nil {
			slog.Warn("remove failed", "component", tag, "container", c.ID[:12], "err", err)
		}
	}
}

// baseRollback is the shared rollback path: point traffic back at the old
// containers, tear down the new ones, and persist an idle state. tag doubles as
// the strategy name written to state, matching the values strategies save.
func baseRollback(ctx context.Context, tag string, docker DockerOps, prov provider.Provider, stateMgr *state.Manager, d *Deployment) error {
	slog.Info("rolling back", "component", tag, "service", d.Service)

	upstream := &provider.UpstreamState{Service: d.Service, UpstreamName: d.UpstreamName()}
	for _, c := range d.Old {
		upstream.Servers = append(upstream.Servers, provider.Server{Addr: c.Addr})
	}
	ApplyProviderSettings(d.Config, upstream)

	if err := prov.GenerateConfig(ctx, upstream); err != nil {
		return fmt.Errorf("generating rollback config: %w", err)
	}

	if err := prov.Reload(ctx); err != nil {
		return fmt.Errorf("reloading provider: %w", err)
	}

	removeContainers(ctx, tag, docker, d.New)

	ds := &state.DeploymentState{
		Service:    d.Service,
		Status:     state.StatusIdle,
		Strategy:   tag,
		Containers: state.Containers{Stable: containerIDs(d.Old)},
	}
	if err := stateMgr.Save(ds); err != nil {
		return fmt.Errorf("saving rollback state: %w", err)
	}

	slog.Info("rollback complete", "component", tag, "service", d.Service)
	return nil
}

// promoteAndDrain routes traffic to the new containers (with old as backup),
// waits for the drain timeout, then removes old containers. drainAffinity sets
// the affinity on the transition config; pass "" to disable affinity during drain.
func promoteAndDrain(ctx context.Context, tag string, docker DockerOps, prov provider.Provider, d *Deployment, drainAffinity string) error {
	finalUpstream := &provider.UpstreamState{
		Service:      d.Service,
		UpstreamName: d.UpstreamName(),
		Affinity:     drainAffinity,
	}
	for _, c := range d.New {
		finalUpstream.Servers = append(finalUpstream.Servers, provider.Server{Addr: c.Addr})
	}
	for _, c := range d.Old {
		finalUpstream.Servers = append(finalUpstream.Servers, provider.Server{Addr: c.Addr, Backup: true})
	}
	ApplyProviderSettings(d.Config, finalUpstream)

	if err := prov.GenerateConfig(ctx, finalUpstream); err != nil {
		return fmt.Errorf("generating drain config: %w", err)
	}
	if err := prov.Reload(ctx); err != nil {
		return fmt.Errorf("reloading drain config: %w", err)
	}

	slog.Info("draining old containers", "component", tag, "service", d.Service, "timeout", d.Config.DrainTimeout)

	select {
	case <-time.After(d.Config.DrainTimeout):
	case <-ctx.Done():
		return ctx.Err()
	}

	stableUpstream := &provider.UpstreamState{Service: d.Service, UpstreamName: d.UpstreamName()}
	for _, c := range d.New {
		stableUpstream.Servers = append(stableUpstream.Servers, provider.Server{Addr: c.Addr})
	}
	ApplyProviderSettings(d.Config, stableUpstream)

	if err := prov.GenerateConfig(ctx, stableUpstream); err != nil {
		return fmt.Errorf("generating stable config: %w", err)
	}
	if err := prov.Reload(ctx); err != nil {
		return fmt.Errorf("reloading stable config: %w", err)
	}

	removeContainers(ctx, tag, docker, d.Old)
	return nil
}
