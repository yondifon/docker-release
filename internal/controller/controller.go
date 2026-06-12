package controller

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/malico/docker-release/internal/config"
	"github.com/malico/docker-release/internal/docker"
	"github.com/malico/docker-release/internal/health"
	"github.com/malico/docker-release/internal/provider"
	"github.com/malico/docker-release/internal/rollback"
	"github.com/malico/docker-release/internal/state"
	"github.com/malico/docker-release/internal/strategy"

	"github.com/docker/docker/api/types"
)

type activeDeployment struct {
	id     string
	cancel context.CancelFunc
}

type Controller struct {
	docker       *docker.Client
	stateManager *state.Manager
	project      string
	factory      *provider.Factory

	mu          sync.Mutex
	deployments map[string]activeDeployment
	wg          sync.WaitGroup
}

func New(dockerClient *docker.Client, stateManager *state.Manager, project string) *Controller {
	return &Controller{
		docker:       dockerClient,
		stateManager: stateManager,
		deployments:  make(map[string]activeDeployment),
		project:      project,
		factory:      provider.NewFactory(dockerClient, project),
	}
}

func (c *Controller) Watch(ctx context.Context) error {
	health.ClearReady()

	if err := c.docker.Ping(ctx); err != nil {
		return fmt.Errorf("docker not reachable: %w", err)
	}

	slog.Info("connected to docker", "component", "controller")

	// Block shutdown until in-flight deployments (and their abort-rollbacks,
	// which run on a detached context) finish, so the process doesn't exit
	// mid-rollout. All deploy goroutines are tracked on c.wg.
	defer c.wg.Wait()

	services, err := c.discoverServices(ctx)
	if err != nil {
		return fmt.Errorf("discovering services: %w", err)
	}

	slog.Info("discovered managed services", "component", "controller", "count", len(services))
	for name, containers := range services {
		slog.Info("managed service", "component", "controller", "service", name, "containers", len(containers))
	}

	msgCh, errCh := c.docker.Events(ctx, c.project)
	commandTicker := time.NewTicker(time.Second)
	defer commandTicker.Stop()

	c.generateInitialConfigs(ctx, services)
	c.recoverInterruptedDeployments(ctx)

	if err := health.MarkReady(); err != nil {
		slog.Warn("could not write ready sentinel", "component", "controller", "err", err)
	}

	c.processReleaseCommands(ctx)

	slog.Info("watching for events (ctrl+c to stop)", "component", "controller")
	for {
		select {
		case <-commandTicker.C:
			c.processReleaseCommands(ctx)
		case msg := <-msgCh:
			switch msg.Action {
			case "create", "start":
				c.handleStart(ctx, msg.Actor.ID, msg.Actor.Attributes)
			case "die", "stop", "destroy":
				c.handleDie(ctx, msg.Actor.ID, msg.Actor.Attributes)
			case "health_status: healthy", "health_status: unhealthy":
				c.handleHealthStatus(ctx, msg.Actor.ID, msg.Actor.Attributes)
			}
		case err := <-errCh:
			if ctx.Err() != nil {
				slog.Info("shutting down", "component", "controller")
				return nil
			}
			return fmt.Errorf("event stream: %w", err)
		case <-ctx.Done():
			slog.Info("shutting down", "component", "controller")
			return nil
		}
	}
}

func (c *Controller) CancelDeployment(service string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	d, ok := c.deployments[service]
	if !ok {
		return false
	}
	d.cancel()
	return true
}

func (c *Controller) StateManager() *state.Manager {
	return c.stateManager
}

func (c *Controller) Project() string {
	return c.project
}

func (c *Controller) ActiveDeployments() map[string]string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make(map[string]string, len(c.deployments))
	for service, d := range c.deployments {
		out[service] = d.id
	}
	return out
}

func (c *Controller) WaitDeployments() {
	c.wg.Wait()
}

func (c *Controller) discoverServices(ctx context.Context) (map[string][]types.Container, error) {
	containers, err := c.docker.ListManagedContainers(ctx, c.project)
	if err != nil {
		return nil, err
	}
	return groupContainersByService(containers), nil
}

func (c *Controller) Rollback(ctx context.Context, service string) error {
	coord := rollback.NewCoordinator(c.stateManager, c.docker)

	cfg := c.resolveServiceConfig(ctx, service)
	prov, err := c.factory.Provider(cfg)
	if err != nil {
		return fmt.Errorf("building provider: %w", err)
	}
	coord.RegisterStrategy("linear", strategy.NewLinear(c.docker, prov, c.stateManager))
	coord.RegisterStrategy("blue-green", strategy.NewBlueGreen(c.docker, prov, c.stateManager))
	coord.RegisterStrategy("canary", strategy.NewCanary(c.docker, prov, c.stateManager))

	return coord.Execute(ctx, service)
}

func (c *Controller) resolveServiceConfig(ctx context.Context, service string) *config.ServiceConfig {
	containers, err := c.docker.ListManagedContainers(ctx, c.project)
	if err != nil {
		return &config.ServiceConfig{Provider: config.ProviderNone}
	}

	for _, ctr := range containers {
		if ctr.Labels["com.docker.compose.service"] == service {
			cfg, err := config.ParseLabels(ctr.Labels)
			if err != nil {
				continue
			}
			return cfg
		}
	}

	return &config.ServiceConfig{Provider: config.ProviderNone}
}
