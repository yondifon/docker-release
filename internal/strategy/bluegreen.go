package strategy

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/malico/docker-release/internal/provider"
	"github.com/malico/docker-release/internal/state"
)

type BlueGreen struct {
	docker   DockerOps
	provider provider.Provider
	state    *state.Manager
}

func NewBlueGreen(docker DockerOps, prov provider.Provider, stateMgr *state.Manager) *BlueGreen {
	return &BlueGreen{
		docker:   docker,
		provider: prov,
		state:    stateMgr,
	}
}

func (bg *BlueGreen) Execute(ctx context.Context, d *Deployment) error {
	slog.Info("starting deployment", "component", "blue-green", "service", d.Service, "blue", len(d.Old), "green", len(d.New))

	ds := &state.DeploymentState{
		Service:              d.Service,
		Status:               state.StatusInProgress,
		Strategy:             "blue-green",
		ActiveDeploymentID:   d.resolveDeployID(),
		PreviousDeploymentID: d.PrevDeployID,
		Containers: state.Containers{
			Stable: containerIDs(d.Old),
			Canary: containerIDs(d.New),
		},
	}

	if err := bg.state.Save(ds); err != nil {
		return fmt.Errorf("saving initial state: %w", err)
	}

	if err := waitAllHealthy(ctx, "blue-green", bg.docker, d); err != nil {
		return err
	}

	slog.Info("all green containers healthy, cutting over traffic", "component", "blue-green", "service", d.Service)

	cutoverUpstream := buildBlueGreenCutoverUpstream(d)
	if err := bg.provider.GenerateConfig(ctx, cutoverUpstream); err != nil {
		return fmt.Errorf("generating cutover config: %w", err)
	}

	if err := bg.provider.Reload(ctx); err != nil {
		return fmt.Errorf("reloading provider: %w", err)
	}

	ds.CurrentWeight = d.Config.BlueGreen.GreenWeight
	if err := bg.state.Save(ds); err != nil {
		return fmt.Errorf("saving cutover state: %w", err)
	}

	soakTime := d.Config.BlueGreen.SoakTime
	slog.Info("soaking on green before removing blue", "component", "blue-green", "service", d.Service, "soak", soakTime)

	select {
	case <-time.After(soakTime):
	case <-ctx.Done():
		return ctx.Err()
	}

	if err := promoteAndDrain(ctx, "blue-green", bg.docker, bg.provider, d, ""); err != nil {
		return err
	}

	ds.Status = state.StatusIdle
	ds.CurrentWeight = 100
	ds.Containers.Stable = containerIDs(d.New)
	ds.Containers.Canary = nil
	if err := bg.state.Save(ds); err != nil {
		return fmt.Errorf("saving final state: %w", err)
	}

	slog.Info("deployment complete", "component", "blue-green", "service", d.Service)
	return nil
}

func buildBlueGreenCutoverUpstream(d *Deployment) *provider.UpstreamState {
	greenWeight := d.Config.BlueGreen.GreenWeight
	blueWeight := 100 - greenWeight

	upstream := &provider.UpstreamState{
		Service:      d.Service,
		UpstreamName: d.UpstreamName(),
		Affinity:     d.Config.Affinity,
	}

	for _, c := range d.Old {
		upstream.Servers = append(upstream.Servers, provider.Server{Addr: c.Addr, Weight: blueWeight, Group: "stable"})
	}

	for _, c := range d.New {
		upstream.Servers = append(upstream.Servers, provider.Server{Addr: c.Addr, Weight: greenWeight, Group: "canary"})
	}

	ApplyProviderSettings(d.Config, upstream)

	return upstream
}

func (bg *BlueGreen) Rollback(ctx context.Context, d *Deployment) error {
	return baseRollback(ctx, "blue-green", bg.docker, bg.provider, bg.state, d)
}
