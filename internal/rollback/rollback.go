package rollback

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/malico/docker-release/internal/config"
	"github.com/malico/docker-release/internal/provider"
	"github.com/malico/docker-release/internal/state"
	"github.com/malico/docker-release/internal/strategy"
)

type ContainerResolver interface {
	ContainerAddr(ctx context.Context, containerID string) (string, error)
}

type Coordinator struct {
	stateMgr   *state.Manager
	resolver   ContainerResolver
	strategies map[string]strategy.Strategy
}

func NewCoordinator(stateMgr *state.Manager, resolver ContainerResolver) *Coordinator {
	return &Coordinator{
		stateMgr:   stateMgr,
		resolver:   resolver,
		strategies: make(map[string]strategy.Strategy),
	}
}

func (c *Coordinator) RegisterStrategy(name string, s strategy.Strategy) {
	c.strategies[name] = s
}

func (c *Coordinator) Execute(ctx context.Context, service string) error {
	ds, err := c.stateMgr.Load(service)
	if err != nil {
		return fmt.Errorf("loading state: %w", err)
	}

	if ds.Status == state.StatusIdle {
		return fmt.Errorf("service %q has no active deployment to roll back", service)
	}

	s, ok := c.strategies[ds.Strategy]
	if !ok {
		return fmt.Errorf("unknown strategy %q in state", ds.Strategy)
	}

	ds.Status = state.StatusRollingBack
	if err := c.stateMgr.Save(ds); err != nil {
		return fmt.Errorf("saving rollback state: %w", err)
	}

	slog.Info("rolling back", "component", "rollback", "service", service, "strategy", ds.Strategy)

	old, err := c.resolveContainers(ctx, ds.Containers.Stable)
	if err != nil {
		return fmt.Errorf("resolving stable containers: %w", err)
	}

	newC, err := c.resolveContainers(ctx, ds.Containers.Canary)
	if err != nil {
		return fmt.Errorf("resolving canary containers: %w", err)
	}

	d := &strategy.Deployment{
		Service: service,
		Config:  &config.ServiceConfig{},
		Old:     old,
		New:     newC,
	}

	return s.Rollback(ctx, d)
}

func (c *Coordinator) ExecuteWithDeployment(ctx context.Context, d *strategy.Deployment) error {
	ds, err := c.stateMgr.Load(d.Service)
	if err != nil {
		return fmt.Errorf("loading state: %w", err)
	}

	if ds.Status == state.StatusIdle {
		return fmt.Errorf("service %q has no active deployment to roll back", d.Service)
	}

	s, ok := c.strategies[ds.Strategy]
	if !ok {
		return fmt.Errorf("unknown strategy %q in state", ds.Strategy)
	}

	ds.Status = state.StatusRollingBack
	if err := c.stateMgr.Save(ds); err != nil {
		return fmt.Errorf("saving rollback state: %w", err)
	}

	slog.Info("rolling back", "component", "rollback", "service", d.Service, "strategy", ds.Strategy)
	return s.Rollback(ctx, d)
}

func (c *Coordinator) resolveContainers(ctx context.Context, ids []string) ([]strategy.ContainerInfo, error) {
	var containers []strategy.ContainerInfo

	for _, id := range ids {
		addr, err := c.resolver.ContainerAddr(ctx, id)
		if err != nil {
			slog.Warn("cannot resolve container, skipping", "component", "rollback", "container", id[:12], "err", err)
			continue
		}

		containers = append(containers, strategy.ContainerInfo{
			ID:   id,
			Addr: addr,
		})
	}

	return containers, nil
}

type NoopProvider struct{}

func (n *NoopProvider) GenerateConfig(_ context.Context, _ *provider.UpstreamState) error { return nil }
func (n *NoopProvider) Reload(_ context.Context) error                                    { return nil }
