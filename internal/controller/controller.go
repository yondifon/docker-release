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
	docker  *docker.Client
	project string
	factory *provider.Factory

	// stateManager is used in per-project mode (project != "").
	stateManager *state.Manager
	// stateBaseDir and stateManagers are used in global mode (project == "").
	// Managers are created lazily and cached here.
	stateBaseDir  string
	stateManagers map[string]*state.Manager

	mu          sync.Mutex
	deployments map[string]activeDeployment
	wg          sync.WaitGroup
}

// New creates a Controller. stateManager is the per-project state manager; in
// global mode (project == "") pass a manager with an empty project so its Dir()
// is available for cross-project command scanning.
func New(dockerClient *docker.Client, stateManager *state.Manager, project string) *Controller {
	return &Controller{
		docker:        dockerClient,
		stateManager:  stateManager,
		stateBaseDir:  stateManager.Dir(),
		stateManagers: make(map[string]*state.Manager),
		deployments:   make(map[string]activeDeployment),
		project:       project,
		factory:       provider.NewFactory(dockerClient, project),
	}
}

// effectiveProject returns the project to use for an operation. When project
// is "" and the controller is in per-project mode, c.project is used so CLI
// callers don't need to repeat the project name.
func (c *Controller) effectiveProject(project string) string {
	if project == "" {
		return c.project
	}
	return project
}

// managerFor returns the state.Manager for the given project. In per-project
// mode it always returns c.stateManager. In global mode it creates and caches
// one manager per project encountered.
func (c *Controller) managerFor(project string) *state.Manager {
	if c.project != "" {
		return c.stateManager
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if mgr, ok := c.stateManagers[project]; ok {
		return mgr
	}
	mgr := state.NewManager(c.stateBaseDir, project)
	c.stateManagers[project] = mgr
	return mgr
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
	for key, containers := range services {
		slog.Info("managed service", "component", "controller", "service", key.deployKey(), "containers", len(containers))
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

// CancelDeployment cancels an in-progress deployment. service is the
// deployKey() value ("project/service" in global mode, bare name in
// per-project mode).
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

// ActiveDeployments returns a map of deployKey → deployment ID for all
// currently running deployments.
func (c *Controller) ActiveDeployments() map[string]string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make(map[string]string, len(c.deployments))
	for key, d := range c.deployments {
		out[key] = d.id
	}
	return out
}

func (c *Controller) WaitDeployments() {
	c.wg.Wait()
}

func (c *Controller) discoverServices(ctx context.Context) (map[serviceKey][]types.Container, error) {
	containers, err := c.docker.ListManagedContainers(ctx, c.project)
	if err != nil {
		return nil, err
	}
	return groupContainersByService(containers), nil
}

func (c *Controller) Rollback(ctx context.Context, project, service string) error {
	project = c.effectiveProject(project)
	coord := rollback.NewCoordinator(c.managerFor(project), c.docker)

	cfg := c.resolveServiceConfig(ctx, project, service)
	prov, err := c.factory.Provider(cfg)
	if err != nil {
		return fmt.Errorf("building provider: %w", err)
	}
	coord.RegisterStrategy("linear", strategy.NewLinear(c.docker, prov, c.managerFor(project)))
	coord.RegisterStrategy("blue-green", strategy.NewBlueGreen(c.docker, prov, c.managerFor(project)))
	coord.RegisterStrategy("canary", strategy.NewCanary(c.docker, prov, c.managerFor(project)))

	return coord.Execute(ctx, service)
}

func (c *Controller) resolveServiceConfig(ctx context.Context, project, service string) *config.ServiceConfig {
	containers, err := c.docker.ListManagedContainers(ctx, project)
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
