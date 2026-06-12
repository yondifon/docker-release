package controller

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/malico/docker-release/internal/config"
	"github.com/malico/docker-release/internal/docker"
	"github.com/malico/docker-release/internal/health"
	"github.com/malico/docker-release/internal/monitor"
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

func (c *Controller) recoverInterruptedDeployments(ctx context.Context) {
	states, err := c.stateManager.ListAll()
	if err != nil {
		slog.Error("error reading state", "component", "recovery", "err", err)
		return
	}
	for _, ds := range states {
		if ds.Status == state.StatusIdle {
			continue
		}
		c.recoverService(ctx, ds)
	}
}

func (c *Controller) recoverService(ctx context.Context, ds *state.DeploymentState) {
	slog.Info("recovering interrupted deployment", "component", "recovery", "service", ds.Service,
		"status", string(ds.Status), "strategy", ds.Strategy, "updated", ds.UpdatedAt.UTC().Format(time.RFC3339))

	if !c.anyContainerRunning(ctx, ds.Containers.Canary) {
		slog.Info("no live canary containers, resetting to idle", "component", "recovery", "service", ds.Service)
		ds.Status = state.StatusIdle
		ds.Containers.Canary = nil
		if err := c.stateManager.Save(ds); err != nil {
			slog.Error("error saving state", "component", "recovery", "service", ds.Service, "err", err)
		}
		return
	}

	slog.Info("found live canary containers, rolling back to free resources", "component", "recovery", "service", ds.Service)
	if err := c.Rollback(ctx, ds.Service); err != nil {
		slog.Error("rollback error", "component", "recovery", "service", ds.Service, "err", err)
		return
	}
	slog.Info("rolled back successfully", "component", "recovery", "service", ds.Service)
}

func (c *Controller) anyContainerRunning(ctx context.Context, ids []string) bool {
	for _, id := range ids {
		info, err := c.docker.Inspect(ctx, id)
		if err != nil {
			continue
		}
		if info.State.Running {
			return true
		}
	}
	return false
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

func (c *Controller) EnqueueRelease(service string, force bool) error {
	cmd, err := c.stateManager.EnqueueReleaseCommand(service, force)
	if err != nil {
		return err
	}

	slog.Info("queued detached release", "component", "controller", "service", service, "command", cmd.ID)
	return nil
}

func (c *Controller) processReleaseCommands(ctx context.Context) {
	commands, err := c.stateManager.PendingReleaseCommands()
	if err != nil {
		slog.Error("error reading release commands", "component", "controller", "err", err)
		return
	}

	for _, cmd := range commands {
		claimed, ok, err := c.stateManager.ClaimReleaseCommand(cmd)
		if err != nil {
			slog.Error("error claiming release command", "component", "controller", "command", cmd.ID, "err", err)
			continue
		}
		if !ok {
			continue
		}

		slog.Info("processing detached release", "component", "controller", "service", claimed.Service, "command", claimed.ID)
		if err := c.Release(ctx, claimed.Service, claimed.Force); err != nil {
			slog.Error("detached release failed", "component", "controller", "service", claimed.Service, "command", claimed.ID, "err", err)
		}

		if err := c.stateManager.CompleteReleaseCommand(claimed); err != nil {
			slog.Error("error completing release command", "component", "controller", "command", claimed.ID, "err", err)
		}
	}
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

func (c *Controller) deploy(parentCtx context.Context, serviceName string, cfg *config.ServiceConfig, oldContainers, newContainers []types.Container, releaseLock func()) {
	c.mu.Lock()
	if d, ok := c.deployments[serviceName]; ok {
		d.cancel()
	}

	ctx, cancel := context.WithCancel(parentCtx)
	deployID := state.GenerateDeploymentID()
	c.deployments[serviceName] = activeDeployment{id: deployID, cancel: cancel}
	c.mu.Unlock()

	// Capture the previous deployment's ID before the early save overwrites it,
	// and use one ID for both the tracking map and the persisted state so the
	// API can cancel by the ID it reports.
	prevDeployID := ""
	if prev, err := c.stateManager.Load(serviceName); err == nil {
		prevDeployID = prev.ActiveDeploymentID
	}

	ds := &state.DeploymentState{
		Service:              serviceName,
		Status:               state.StatusInProgress,
		Strategy:             string(cfg.Strategy),
		ActiveDeploymentID:   deployID,
		PreviousDeploymentID: prevDeployID,
	}
	if err := c.stateManager.Save(ds); err != nil {
		slog.Error("error saving early state", "component", "controller", "service", serviceName, "err", err)
	}

	defer func() {
		c.mu.Lock()
		if d, ok := c.deployments[serviceName]; ok && d.id == deployID {
			delete(c.deployments, serviceName)
		}
		c.mu.Unlock()
		cancel()
		if releaseLock != nil {
			releaseLock()
		}
	}()

	slog.Info("starting deployment", "component", "controller", "service", serviceName, "strategy", cfg.Strategy)

	expected := len(oldContainers)
	if len(newContainers) < expected {
		newContainers = c.waitForContainers(ctx, serviceName, containerRevision(newContainers[0]), expected)
	}

	prov, err := c.factory.Provider(cfg)
	if err != nil {
		slog.Error("error building provider", "component", "controller", "service", serviceName, "err", err)
		return
	}

	resolveAddr := cfg.Provider != config.ProviderNone

	oldInfos := c.resolveContainers(ctx, oldContainers, resolveAddr)
	newInfos := c.resolveContainers(ctx, newContainers, resolveAddr)

	d := &strategy.Deployment{
		Service:      serviceName,
		Config:       cfg,
		Old:          oldInfos,
		New:          newInfos,
		DeployID:     deployID,
		PrevDeployID: prevDeployID,
	}

	deployCtx, deployCancel := context.WithCancel(ctx)
	defer deployCancel()

	newIDs := make([]string, len(newInfos))
	for i, info := range newInfos {
		newIDs[i] = info.ID
	}

	strat := strategy.New(cfg, c.docker, prov, c.stateManager)

	mon := monitor.NewHealthMonitor(c.docker, newIDs, func(containerID, reason string) {
		slog.Warn("auto-rollback triggered", "component", "controller", "service", serviceName, "reason", reason)
		deployCancel()
	})
	mon.SetGracePeriod(cfg.HealthCheckTimeout)

	go func() {
		if err := mon.Run(deployCtx); err != nil && !errors.Is(err, monitor.ErrUnhealthy) && deployCtx.Err() == nil {
			slog.Warn("health monitor stopped early; deployment proceeding unmonitored", "component", "controller", "service", serviceName, "err", err)
		}
	}()

	if err := strat.Execute(deployCtx, d); err != nil {
		slog.Error("deployment failed", "component", "controller", "service", serviceName, "err", err)

		rollbackCtx, rollbackCancel := context.WithTimeout(context.Background(), cfg.HealthCheckTimeout+cfg.DrainTimeout+30*time.Second)
		defer rollbackCancel()

		slog.Info("initiating abort rollback", "component", "controller", "service", serviceName)
		if rbErr := c.abortDeployment(rollbackCtx, serviceName, cfg, prov, d); rbErr != nil {
			slog.Error("rollback failed", "component", "controller", "service", serviceName, "err", rbErr)
		}
		return
	}

	slog.Info("deployment complete", "component", "controller", "service", serviceName)
}

func (c *Controller) abortDeployment(ctx context.Context, serviceName string, cfg *config.ServiceConfig, prov provider.Provider, d *strategy.Deployment) error {
	targets, err := c.abortTargets(ctx, serviceName, d)
	if err != nil {
		return err
	}
	if len(targets) == 0 {
		return fmt.Errorf("no live rollback targets for %s", serviceName)
	}

	upstream := &provider.UpstreamState{
		Service:      serviceName,
		UpstreamName: d.UpstreamName(),
		Affinity:     cfg.Affinity,
	}

	for _, target := range targets {
		upstream.Servers = append(upstream.Servers, provider.Server{Addr: target.Addr})
	}
	strategy.ApplyProviderSettings(cfg, upstream)

	if err := prov.GenerateConfig(ctx, upstream); err != nil {
		return fmt.Errorf("generating abort rollback config: %w", err)
	}

	if err := prov.Reload(ctx); err != nil {
		return fmt.Errorf("reloading abort rollback config: %w", err)
	}

	select {
	case <-time.After(cfg.DrainTimeout):
	case <-ctx.Done():
		return ctx.Err()
	}

	targetIDs := make(map[string]bool, len(targets))
	for _, target := range targets {
		targetIDs[target.ID] = true
	}

	for _, newContainer := range d.New {
		if targetIDs[newContainer.ID] {
			continue
		}

		if err := c.docker.Stop(ctx, newContainer.ID, 10); err != nil {
			slog.Warn("abort rollback: stop failed", "component", "controller", "container", newContainer.ID[:12], "err", err)
		}

		if err := c.docker.Remove(ctx, newContainer.ID); err != nil {
			slog.Warn("abort rollback: remove failed", "component", "controller", "container", newContainer.ID[:12], "err", err)
		}
	}

	return c.stateManager.Save(&state.DeploymentState{
		Service:    serviceName,
		Status:     state.StatusIdle,
		Strategy:   string(cfg.Strategy),
		Containers: state.Containers{Stable: containerInfoIDs(targets)},
	})
}

func (c *Controller) abortTargets(ctx context.Context, serviceName string, d *strategy.Deployment) ([]strategy.ContainerInfo, error) {
	containersByID := make(map[string]strategy.ContainerInfo, len(d.Old)+len(d.New))
	for _, info := range d.Old {
		containersByID[info.ID] = info
	}
	for _, info := range d.New {
		containersByID[info.ID] = info
	}

	ds, err := c.stateManager.Load(serviceName)
	if err != nil {
		return nil, fmt.Errorf("loading abort state: %w", err)
	}

	ids := ds.Containers.Stable
	if len(ids) == 0 {
		ids = containerInfoIDs(d.Old)
	}

	targets := make([]strategy.ContainerInfo, 0, len(ids))
	for _, id := range ids {
		info, ok := containersByID[id]
		if !ok {
			continue
		}
		if _, err := c.docker.Inspect(ctx, info.ID); err != nil {
			continue
		}
		targets = append(targets, info)
	}

	return targets, nil
}

func containerInfoIDs(containers []strategy.ContainerInfo) []string {
	ids := make([]string, len(containers))
	for i, container := range containers {
		ids[i] = container.ID
	}
	return ids
}

func (c *Controller) resolveNginxProxyUpstream(ctx context.Context, cfg *config.ServiceConfig, containers []types.Container) {
	if cfg.Provider != config.ProviderNginxProxy || cfg.UpstreamName != "" || len(containers) == 0 {
		return
	}
	env, err := c.docker.ContainerEnv(ctx, containers[0].ID)
	if err != nil {
		slog.Warn("could not read container env for nginx-proxy upstream", "component", "controller", "err", err)
		return
	}
	name, err := provider.NginxProxyUpstreamName(env)
	if err != nil {
		slog.Warn("could not resolve nginx-proxy upstream name", "component", "controller", "err", err)
		return
	}
	cfg.UpstreamName = name
}

func (c *Controller) waitForContainers(ctx context.Context, serviceName, revision string, expected int) []types.Container {
	timeout := 30 * time.Second
	deadline := time.After(timeout)
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	slog.Info("waiting for new containers (have fewer)", "component", "controller", "service", serviceName, "expected", expected)

	for {
		select {
		case <-deadline:
			slog.Warn("timed out waiting for new containers, proceeding with what's available", "component", "controller", "service", serviceName, "expected", expected)
			return c.listContainersByRevision(ctx, serviceName, revision)
		case <-ctx.Done():
			return c.listContainersByRevision(ctx, serviceName, revision)
		case <-ticker.C:
			found := c.listContainersByRevision(ctx, serviceName, revision)
			if len(found) >= expected {
				slog.Info("found new containers", "component", "controller", "service", serviceName, "found", len(found), "expected", expected)
				return found
			}
		}
	}
}

func (c *Controller) listContainersByRevision(ctx context.Context, serviceName, revision string) []types.Container {
	containers, err := c.docker.ListManagedContainers(ctx, c.project)
	if err != nil {
		return nil
	}

	var matched []types.Container
	for _, ctr := range filterServiceContainers(containers, serviceName) {
		if containerRevision(ctr) == revision {
			matched = append(matched, ctr)
		}
	}

	return matched
}

func (c *Controller) resolveContainers(ctx context.Context, containers []types.Container, resolveAddr bool) []strategy.ContainerInfo {
	var infos []strategy.ContainerInfo

	for _, ctr := range containers {
		info := strategy.ContainerInfo{ID: ctr.ID}

		if resolveAddr {
			addr, err := c.docker.ContainerAddr(ctx, ctr.ID)
			if err != nil {
				slog.Warn("resolving container address failed", "component", "controller", "container", ctr.ID[:12], "err", err)
				continue
			}
			info.Addr = addr
		}

		infos = append(infos, info)
	}

	return infos
}

func (c *Controller) Release(ctx context.Context, service string, force bool) error {
	ds, err := c.stateManager.Load(service)
	if err != nil {
		return fmt.Errorf("loading state: %w", err)
	}

	if ds.Status == state.StatusInProgress && !ds.IsStale(state.DefaultStaleThreshold) && !force {
		return fmt.Errorf("deployment already in progress for %q (started %s) — use --force to override", service, formatTimestamp(ds.UpdatedAt))
	}

	release, err := c.stateManager.AcquireDeployLock(service)
	if err != nil {
		if errors.Is(err, state.ErrDeployLocked) {
			return fmt.Errorf("deployment already running for %q in another process", service)
		}
		return fmt.Errorf("acquiring deploy lock: %w", err)
	}
	// Hold the lock until ownership transfers to the deploy goroutine; any early
	// return below releases it.
	releaseOnReturn := release
	defer func() {
		if releaseOnReturn != nil {
			releaseOnReturn()
		}
	}()

	containers, err := c.docker.ListManagedContainers(ctx, c.project)
	if err != nil {
		return fmt.Errorf("listing containers: %w", err)
	}

	serviceContainers := filterServiceContainers(containers, service)

	if len(serviceContainers) == 0 {
		return fmt.Errorf("no managed containers found for service %q", service)
	}

	revisions := groupByRevision(serviceContainers)

	if len(revisions) >= 2 {
		oldContainers, newContainers := splitByRevision(serviceContainers, revisions)
		cfg, err := config.ParseLabels(newContainers[0].Labels)
		if err != nil {
			return fmt.Errorf("parsing labels: %w", err)
		}

		c.resolveNginxProxyUpstream(ctx, cfg, newContainers)

		slog.Info("releasing", "component", "controller", "service", service, "old", len(oldContainers), "new", len(newContainers))
		releaseOnReturn = nil
		c.wg.Add(1)
		go func() {
			defer c.wg.Done()
			c.deploy(ctx, service, cfg, oldContainers, newContainers, release)
		}()
		return nil
	}

	cfg, err := config.ParseLabels(serviceContainers[0].Labels)
	if err != nil {
		return fmt.Errorf("parsing labels: %w", err)
	}

	c.resolveNginxProxyUpstream(ctx, cfg, serviceContainers)

	newContainers, err := c.scaleUp(ctx, serviceContainers)
	if err != nil {
		return fmt.Errorf("scaling up: %w", err)
	}

	slog.Info("releasing", "component", "controller", "service", service, "old", len(serviceContainers), "new", len(newContainers))
	releaseOnReturn = nil
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		c.deploy(ctx, service, cfg, serviceContainers, newContainers, release)
	}()

	return nil
}

func (c *Controller) WaitDeployments() {
	c.wg.Wait()
}

func (c *Controller) scaleUp(ctx context.Context, existing []types.Container) ([]types.Container, error) {
	slog.Info("scaling up: creating containers from image", "component", "controller", "count", len(existing))

	var project, service string
	if len(existing) > 0 {
		project = existing[0].Labels["com.docker.compose.project"]
		service = existing[0].Labels["com.docker.compose.service"]
	}
	maxNum := c.docker.MaxServiceContainerNumber(ctx, project, service)

	var newIDs []string
	for i, ctr := range existing {
		newID, err := c.docker.CreateContainerFromImage(ctx, ctr, maxNum+1+i)
		if err != nil {
			for _, id := range newIDs {
				_ = c.docker.Remove(context.Background(), id)
			}
			return nil, err
		}
		newIDs = append(newIDs, newID)
	}

	newIDSet := make(map[string]bool, len(newIDs))
	for _, id := range newIDs {
		newIDSet[id] = true
	}

	allContainers, err := c.docker.ListManagedContainers(ctx, c.project)
	if err != nil {
		return nil, fmt.Errorf("listing containers: %w", err)
	}

	var newContainers []types.Container
	for _, ctr := range allContainers {
		if newIDSet[ctr.ID] {
			newContainers = append(newContainers, ctr)
		}
	}

	return newContainers, nil
}

func splitByRevision(containers []types.Container, revisions map[string][]types.Container) (old, new []types.Container) {
	var newestTime int64
	var newestRevision string
	for _, ctr := range containers {
		if ctr.Created > newestTime {
			newestTime = ctr.Created
			newestRevision = containerRevision(ctr)
		}
	}

	for revision, ctrs := range revisions {
		if revision == newestRevision {
			new = ctrs
		} else {
			old = append(old, ctrs...)
		}
	}

	return old, new
}

func filterServiceContainers(containers []types.Container, serviceName string) []types.Container {
	var matched []types.Container
	for _, container := range containers {
		if container.Labels["com.docker.compose.service"] == serviceName {
			matched = append(matched, container)
		}
	}

	return matched
}

func (c *Controller) serviceFromEvent(ctx context.Context, containerID string, attrs map[string]string) string {
	if c.project != "" {
		eventProject := attrs["com.docker.compose.project"]
		if eventProject == "" {
			info, err := c.docker.Inspect(ctx, containerID)
			if err != nil {
				return ""
			}
			if info.Config != nil && info.Config.Labels != nil {
				eventProject = info.Config.Labels["com.docker.compose.project"]
			}
		}
		if eventProject != c.project {
			return ""
		}
	}

	serviceName := attrs["com.docker.compose.service"]
	if serviceName != "" {
		return serviceName
	}

	info, err := c.docker.Inspect(ctx, containerID)
	if err != nil {
		return ""
	}

	if info.Config == nil {
		return ""
	}

	if info.Config.Labels == nil {
		return ""
	}

	return info.Config.Labels["com.docker.compose.service"]
}

func groupByRevision(containers []types.Container) map[string][]types.Container {
	grouped := make(map[string][]types.Container)
	for _, container := range containers {
		revision := containerRevision(container)
		grouped[revision] = append(grouped[revision], container)
	}

	return grouped
}

func containerRevision(container types.Container) string {
	if hash := container.Labels["com.docker.compose.config-hash"]; hash != "" {
		return "config:" + hash
	}

	return "image:" + container.ImageID
}

// separateByRevision splits containers into the revision group the just-started
// container belongs to ("new") and everything else ("old"). Grouping by revision
// (compose config-hash, falling back to image) means a config-only change with an
// unchanged image still triggers a rollout, matching the CLI release path.
func separateByRevision(containers []types.Container, revisions map[string][]types.Container, startedID string) (oldContainers, newContainers []types.Container) {
	startedRevision := ""
	for _, container := range containers {
		if container.ID == startedID {
			startedRevision = containerRevision(container)
			break
		}
	}

	if startedRevision == "" {
		return nil, nil
	}

	for revision, ctrs := range revisions {
		if revision == startedRevision {
			newContainers = ctrs
			continue
		}

		oldContainers = append(oldContainers, ctrs...)
	}

	return oldContainers, newContainers
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

func (c *Controller) discoverServices(ctx context.Context) (map[string][]types.Container, error) {
	containers, err := c.docker.ListManagedContainers(ctx, c.project)
	if err != nil {
		return nil, err
	}

	services := make(map[string][]types.Container)
	for _, ctr := range containers {
		name := ctr.Labels["com.docker.compose.service"]
		if name == "" {
			continue
		}
		services[name] = append(services[name], ctr)
	}

	return services, nil
}

func (c *Controller) handleHealthStatus(ctx context.Context, containerID string, attrs map[string]string) {
	serviceName := c.serviceFromEvent(ctx, containerID, attrs)
	if serviceName == "" {
		return
	}

	slog.Info("health status changed", "component", "controller", "container", containerID[:12], "service", serviceName)

	c.refreshServiceConfig(ctx, serviceName)
}
