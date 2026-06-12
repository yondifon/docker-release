package strategy

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/malico/docker-release/internal/provider"
	"github.com/malico/docker-release/internal/state"
)

type DockerOps interface {
	WaitHealthy(ctx context.Context, containerID string, timeout time.Duration) error
	Stop(ctx context.Context, containerID string, timeoutSeconds int) error
	Remove(ctx context.Context, containerID string) error
}

type Linear struct {
	docker   DockerOps
	provider provider.Provider
	state    *state.Manager
}

func NewLinear(docker DockerOps, prov provider.Provider, stateMgr *state.Manager) *Linear {
	return &Linear{
		docker:   docker,
		provider: prov,
		state:    stateMgr,
	}
}

func (l *Linear) Execute(ctx context.Context, d *Deployment) error {
	slog.Info("starting deployment", "component", "linear", "service", d.Service, "old", len(d.Old), "new", len(d.New))

	ds := &state.DeploymentState{
		Service:              d.Service,
		Status:               state.StatusInProgress,
		Strategy:             "linear",
		ActiveDeploymentID:   d.resolveDeployID(),
		PreviousDeploymentID: d.PrevDeployID,
		Containers: state.Containers{
			Stable: containerIDs(d.Old),
		},
	}

	if err := l.state.Save(ds); err != nil {
		return fmt.Errorf("saving initial state: %w", err)
	}

	replacements := min(len(d.Old), len(d.New))
	for i := 0; i < replacements; i++ {
		if err := ctx.Err(); err != nil {
			return err
		}

		oldC := d.Old[i]
		newC := d.New[i]

		slog.Info("replacing container", "component", "linear", "service", d.Service, "step", i+1, "steps", replacements, "old", oldC.ID[:12], "new", newC.ID[:12])

		slog.Info("waiting for container to be healthy", "component", "linear", "container", newC.ID[:12])
		if err := l.docker.WaitHealthy(ctx, newC.ID, d.Config.HealthCheckTimeout); err != nil {
			return fmt.Errorf("health check failed for %s: %w", newC.ID[:12], err)
		}

		upstream := l.buildUpstream(d, i)
		ApplyProviderSettings(d.Config, upstream)
		if err := l.provider.GenerateConfig(ctx, upstream); err != nil {
			return fmt.Errorf("generating config: %w", err)
		}

		if err := l.provider.Reload(ctx); err != nil {
			return fmt.Errorf("reloading provider: %w", err)
		}

		slog.Info("draining container", "component", "linear", "container", oldC.ID[:12], "timeout", d.Config.DrainTimeout)
		select {
		case <-time.After(d.Config.DrainTimeout):
		case <-ctx.Done():
			return ctx.Err()
		}

		slog.Info("stopping container", "component", "linear", "container", oldC.ID[:12])
		if err := l.docker.Stop(ctx, oldC.ID, 10); err != nil {
			slog.Warn("stop failed", "component", "linear", "container", oldC.ID[:12], "err", err)
		}

		if err := l.docker.Remove(ctx, oldC.ID); err != nil {
			slog.Warn("remove failed", "component", "linear", "container", oldC.ID[:12], "err", err)
		}

		ds.Containers.Stable = containerIDs(d.New[:i+1])
		if i+1 < replacements {
			ds.Containers.Stable = append(ds.Containers.Stable, containerIDs(d.Old[i+1:])...)
		}
		if err := l.state.Save(ds); err != nil {
			return fmt.Errorf("saving state at step %d: %w", i+1, err)
		}
	}

	if len(d.New) > len(d.Old) {
		for i := len(d.Old); i < len(d.New); i++ {
			slog.Info("waiting for extra container to be healthy", "component", "linear", "container", d.New[i].ID[:12])
			if err := l.docker.WaitHealthy(ctx, d.New[i].ID, d.Config.HealthCheckTimeout); err != nil {
				return fmt.Errorf("health check failed for %s: %w", d.New[i].ID[:12], err)
			}
		}
	}

	upstream := l.buildFinalUpstream(d, d.Config.Affinity)
	ApplyProviderSettings(d.Config, upstream)
	if err := l.provider.GenerateConfig(ctx, upstream); err != nil {
		return fmt.Errorf("generating final deployment config: %w", err)
	}

	if err := l.provider.Reload(ctx); err != nil {
		return fmt.Errorf("reloading final deployment config: %w", err)
	}

	stableUpstream := l.buildFinalUpstream(d, "")
	ApplyProviderSettings(d.Config, stableUpstream)
	if err := l.provider.GenerateConfig(ctx, stableUpstream); err != nil {
		return fmt.Errorf("generating final stable config: %w", err)
	}

	if err := l.provider.Reload(ctx); err != nil {
		return fmt.Errorf("reloading final stable config: %w", err)
	}

	ds.Status = state.StatusIdle
	ds.Containers.Stable = containerIDs(d.New)
	ds.Containers.Canary = nil
	if err := l.state.Save(ds); err != nil {
		return fmt.Errorf("saving final state: %w", err)
	}

	slog.Info("deployment complete", "component", "linear", "service", d.Service)
	return nil
}

func (l *Linear) Rollback(ctx context.Context, d *Deployment) error {
	return baseRollback(ctx, "linear", l.docker, l.provider, l.state, d)
}

func (l *Linear) buildUpstream(d *Deployment, step int) *provider.UpstreamState {
	upstream := &provider.UpstreamState{Service: d.Service, UpstreamName: d.UpstreamName(), Affinity: d.Config.Affinity}

	for j := 0; j <= step; j++ {
		upstream.Servers = append(upstream.Servers, provider.Server{Addr: d.New[j].Addr})
	}

	upstream.Servers = append(upstream.Servers, provider.Server{
		Addr: d.Old[step].Addr,
		Down: true,
	})

	for j := step + 1; j < len(d.Old); j++ {
		upstream.Servers = append(upstream.Servers, provider.Server{Addr: d.Old[j].Addr})
	}

	return upstream
}

func (l *Linear) buildFinalUpstream(d *Deployment, affinity string) *provider.UpstreamState {
	upstream := &provider.UpstreamState{Service: d.Service, UpstreamName: d.UpstreamName(), Affinity: affinity}

	for _, c := range d.New {
		upstream.Servers = append(upstream.Servers, provider.Server{Addr: c.Addr})
	}

	return upstream
}

func containerIDs(containers []ContainerInfo) []string {
	ids := make([]string, len(containers))
	for i, c := range containers {
		ids[i] = c.ID
	}
	return ids
}
