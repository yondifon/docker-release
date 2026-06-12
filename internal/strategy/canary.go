package strategy

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/malico/docker-release/internal/provider"
	"github.com/malico/docker-release/internal/state"
)

type Canary struct {
	docker   DockerOps
	provider provider.Provider
	state    *state.Manager
}

func NewCanary(docker DockerOps, prov provider.Provider, stateMgr *state.Manager) *Canary {
	return &Canary{
		docker:   docker,
		provider: prov,
		state:    stateMgr,
	}
}

func (c *Canary) Execute(ctx context.Context, d *Deployment) error {
	slog.Info("starting deployment", "component", "canary", "service", d.Service, "stable", len(d.Old), "canary", len(d.New))

	ds := &state.DeploymentState{
		Service:              d.Service,
		Status:               state.StatusInProgress,
		Strategy:             "canary",
		ActiveDeploymentID:   d.resolveDeployID(),
		PreviousDeploymentID: d.PrevDeployID,
		Containers: state.Containers{
			Stable: containerIDs(d.Old),
			Canary: containerIDs(d.New),
		},
	}

	if err := c.state.Save(ds); err != nil {
		return fmt.Errorf("saving initial state: %w", err)
	}

	if err := waitAllHealthy(ctx, "canary", c.docker, d); err != nil {
		return err
	}

	canaryCfg := d.Config.Canary
	weight := canaryCfg.StartPercentage

	for weight < 100 {
		if err := ctx.Err(); err != nil {
			return err
		}

		slog.Info("setting canary weight", "component", "canary", "service", d.Service, "weight", weight)

		upstream := buildCanaryUpstream(d, weight)
		if err := c.provider.GenerateConfig(ctx, upstream); err != nil {
			return fmt.Errorf("generating config at %d%%: %w", weight, err)
		}

		if err := c.provider.Reload(ctx); err != nil {
			return fmt.Errorf("reloading at %d%%: %w", weight, err)
		}

		ds.CurrentWeight = weight
		if err := c.state.Save(ds); err != nil {
			return fmt.Errorf("saving state at %d%%: %w", weight, err)
		}

		slog.Info("observing canary", "component", "canary", "service", d.Service, "interval", canaryCfg.Interval, "weight", weight)

		select {
		case <-time.After(canaryCfg.Interval):
		case <-ctx.Done():
			return ctx.Err()
		}

		weight += canaryCfg.Step
		if weight > 100 {
			weight = 100
		}
	}

	slog.Info("promoting canary to 100%", "component", "canary", "service", d.Service)

	finalUpstream := &provider.UpstreamState{Service: d.Service, UpstreamName: d.UpstreamName(), Affinity: d.Config.Affinity}
	for _, cn := range d.New {
		finalUpstream.Servers = append(finalUpstream.Servers, provider.Server{Addr: cn.Addr})
	}
	for _, old := range d.Old {
		finalUpstream.Servers = append(finalUpstream.Servers, provider.Server{Addr: old.Addr, Backup: true})
	}
	ApplyProviderSettings(d.Config, finalUpstream)

	if err := c.provider.GenerateConfig(ctx, finalUpstream); err != nil {
		return fmt.Errorf("generating final deployment config: %w", err)
	}

	if err := c.provider.Reload(ctx); err != nil {
		return fmt.Errorf("reloading final deployment: %w", err)
	}

	slog.Info("draining old containers", "component", "canary", "service", d.Service, "timeout", d.Config.DrainTimeout)

	select {
	case <-time.After(d.Config.DrainTimeout):
	case <-ctx.Done():
		return ctx.Err()
	}

	stableUpstream := &provider.UpstreamState{Service: d.Service, UpstreamName: d.UpstreamName()}
	for _, cn := range d.New {
		stableUpstream.Servers = append(stableUpstream.Servers, provider.Server{Addr: cn.Addr})
	}
	ApplyProviderSettings(d.Config, stableUpstream)

	if err := c.provider.GenerateConfig(ctx, stableUpstream); err != nil {
		return fmt.Errorf("generating final stable config: %w", err)
	}

	if err := c.provider.Reload(ctx); err != nil {
		return fmt.Errorf("reloading final stable: %w", err)
	}

	removeContainers(ctx, "canary", c.docker, d.Old)

	ds.Status = state.StatusIdle
	ds.CurrentWeight = 100
	ds.Containers.Stable = containerIDs(d.New)
	ds.Containers.Canary = nil
	if err := c.state.Save(ds); err != nil {
		return fmt.Errorf("saving final state: %w", err)
	}

	slog.Info("deployment complete", "component", "canary", "service", d.Service)
	return nil
}

func (c *Canary) Rollback(ctx context.Context, d *Deployment) error {
	return baseRollback(ctx, "canary", c.docker, c.provider, c.state, d)
}

func buildCanaryUpstream(d *Deployment, canaryWeight int) *provider.UpstreamState {
	stableWeight := 100 - canaryWeight

	upstream := &provider.UpstreamState{
		Service:      d.Service,
		UpstreamName: d.UpstreamName(),
		Affinity:     d.Config.Affinity,
	}

	for _, old := range d.Old {
		upstream.Servers = append(upstream.Servers, provider.Server{
			Addr:   old.Addr,
			Weight: stableWeight,
			Group:  "stable",
		})
	}

	for _, cn := range d.New {
		upstream.Servers = append(upstream.Servers, provider.Server{
			Addr:   cn.Addr,
			Weight: canaryWeight,
			Group:  "canary",
		})
	}
	ApplyProviderSettings(d.Config, upstream)

	return upstream
}
