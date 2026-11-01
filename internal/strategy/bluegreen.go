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

	if d.Config.PreviewHost != "" {
		if err := bg.publishPreview(ctx, d); err != nil {
			return err
		}
	}

	check := d.Config.Check
	if check.Command != "" {
		reason, err := runChecks(ctx, d)
		if err != nil {
			return err
		}
		if reason != "" {
			return bg.undoBeforeSwitch(ctx, d, ds, reason)
		}
	}

	var watch *errorWatch
	var lost <-chan struct{}
	if check.Watch {
		if d.Requests == nil {
			return fmt.Errorf("release.check error rules need the bundled proxy's access log, which is not available")
		}
		watch = newErrorWatch(check, d.New)
		stopWatching, lostStream, err := d.Requests.Subscribe(watch.observe)
		if err != nil {
			return err
		}
		defer stopWatching()
		lost = lostStream
	}

	slog.Info("all green containers healthy, cutting over traffic", "component", "blue-green", "service", d.Service)

	cutoverUpstream := buildBlueGreenCutoverUpstream(d)
	if watch != nil {
		// Users move completely; blue stays up without traffic for a quick way back.
		cutoverUpstream = newOnlyUpstream(d, d.Config.Affinity)
	}
	if err := bg.provider.GenerateConfig(ctx, cutoverUpstream); err != nil {
		return fmt.Errorf("generating cutover config: %w", err)
	}

	if err := bg.provider.Reload(ctx); err != nil {
		return fmt.Errorf("reloading provider: %w", err)
	}

	ds.CurrentWeight = d.Config.BlueGreen.GreenWeight
	if watch != nil {
		ds.CurrentWeight = 100
	}
	if err := bg.state.Save(ds); err != nil {
		return fmt.Errorf("saving cutover state: %w", err)
	}

	if watch != nil {
		if err := bg.settleAfterWindow(ctx, d, watch, lost); err != nil {
			return err
		}
	} else if err := bg.settleAfterSoak(ctx, d); err != nil {
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

// settleAfterWindow watches green for the check window, then removes blue. Too
// many errors send users back to blue instead.
func (bg *BlueGreen) settleAfterWindow(ctx context.Context, d *Deployment, watch *errorWatch, lost <-chan struct{}) error {
	slog.Info("watching green for errors before removing blue", "component", "blue-green", "service", d.Service, "window", watch.rules.Window)
	end, err := watch.wait(ctx, lost)
	if err != nil {
		return err
	}
	switch end {
	case windowTooManyErrors:
		return bg.undoAfterSwitch(ctx, d, watch.reason())
	case windowBlind:
		return bg.undoAfterSwitch(ctx, d, "The proxy's access log stopped, so the new version could not be watched.")
	}
	removeContainers(ctx, "blue-green", bg.docker, d.Old)
	return nil
}

// settleAfterSoak lets green take its share of traffic for the soak time, then
// moves everyone to green and removes blue.
func (bg *BlueGreen) settleAfterSoak(ctx context.Context, d *Deployment) error {
	soakTime := d.Config.BlueGreen.SoakTime
	slog.Info("soaking on green before removing blue", "component", "blue-green", "service", d.Service, "soak", soakTime)

	select {
	case <-time.After(soakTime):
	case <-ctx.Done():
		return ctx.Err()
	}
	return promoteAndDrain(ctx, "blue-green", bg.docker, bg.provider, d, "")
}

// publishPreview lets green answer on the preview host while blue keeps
// serving everyone else. The switch config has no preview, so it ends there.
func (bg *BlueGreen) publishPreview(ctx context.Context, d *Deployment) error {
	upstream := blueOnlyUpstream(d)
	for _, c := range d.New {
		upstream.Preview = append(upstream.Preview, provider.Server{Addr: c.Addr})
	}
	if err := bg.provider.GenerateConfig(ctx, upstream); err != nil {
		return fmt.Errorf("generating preview config: %w", err)
	}
	if err := bg.provider.Reload(ctx); err != nil {
		return fmt.Errorf("reloading provider for the preview: %w", err)
	}
	slog.Info("green answers on the preview host", "component", "blue-green", "service", d.Service, "host", d.Config.PreviewHost)
	return nil
}

// withdrawPreview takes the preview host away when the release stops before the
// switch. Failing here is logged only: the release has already failed.
func (bg *BlueGreen) withdrawPreview(ctx context.Context, d *Deployment) {
	if d.Config.PreviewHost == "" {
		return
	}
	if err := bg.provider.GenerateConfig(ctx, blueOnlyUpstream(d)); err != nil {
		slog.Warn("removing the preview host failed", "component", "blue-green", "service", d.Service, "err", err)
		return
	}
	if err := bg.provider.Reload(ctx); err != nil {
		slog.Warn("reloading provider without the preview host failed", "component", "blue-green", "service", d.Service, "err", err)
	}
}

// undoBeforeSwitch removes the new containers after a failed check. The proxy
// never sent them traffic, so the old containers keep serving untouched.
func (bg *BlueGreen) undoBeforeSwitch(ctx context.Context, d *Deployment, ds *state.DeploymentState, reason string) error {
	slog.Warn("check failed, removing green", "component", "blue-green", "service", d.Service)
	bg.withdrawPreview(ctx, d)
	removeContainers(ctx, "blue-green", bg.docker, d.New)

	ds.Status = state.StatusIdle
	ds.Containers = state.Containers{Stable: containerIDs(d.Old)}
	ds.Failure = reason
	if err := bg.state.Save(ds); err != nil {
		return fmt.Errorf("saving state after failed check: %w", err)
	}
	return &UndoneError{Reason: reason}
}

// undoAfterSwitch sends users back to the old containers and removes the new ones.
func (bg *BlueGreen) undoAfterSwitch(ctx context.Context, d *Deployment, reason string) error {
	slog.Warn("too many errors from green, rolling back", "component", "blue-green", "service", d.Service)
	if err := rollbackWithFailure(ctx, "blue-green", bg.docker, bg.provider, bg.state, d, reason); err != nil {
		return err
	}
	return &UndoneError{Reason: reason}
}

// blueOnlyUpstream is the config before the switch: everyone goes to blue.
func blueOnlyUpstream(d *Deployment) *provider.UpstreamState {
	upstream := &provider.UpstreamState{Service: d.Service, UpstreamName: d.UpstreamName(), Affinity: d.Config.Affinity}
	for _, c := range d.Old {
		upstream.Servers = append(upstream.Servers, provider.Server{Addr: c.Addr})
	}
	ApplyProviderSettings(d.Config, upstream)
	return upstream
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
