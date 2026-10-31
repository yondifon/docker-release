package docker

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/events"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	"github.com/docker/docker/errdefs"
	"github.com/docker/go-connections/nat"
)

type Client struct {
	api client.APIClient
}

const (
	imageTitleLabel   = "org.opencontainers.image.title"
	bundledProxyLabel = "com.malico.docker-release.bundled-proxy"

	composeContainerNumberLabel = "com.docker.compose.container-number"
)

func NewClient() (*Client, error) {
	api, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("connecting to docker: %w", err)
	}

	return &Client{api: api}, nil
}

func (c *Client) Ping(ctx context.Context) error {
	_, err := c.api.Ping(ctx)
	return err
}

func (c *Client) ListManagedContainers(ctx context.Context, project string) ([]types.Container, error) {
	f := filters.NewArgs()
	f.Add("label", "release.enable=true")
	if project != "" {
		f.Add("label", fmt.Sprintf("com.docker.compose.project=%s", project))
	}

	return c.api.ContainerList(ctx, container.ListOptions{Filters: f})
}

func (c *Client) Inspect(ctx context.Context, containerID string) (types.ContainerJSON, error) {
	return c.api.ContainerInspect(ctx, containerID)
}

func (c *Client) Stop(ctx context.Context, containerID string, timeoutSeconds int) error {
	timeout := timeoutSeconds
	err := c.api.ContainerStop(ctx, containerID, container.StopOptions{Timeout: &timeout})
	if errdefs.IsNotFound(err) {
		return nil
	}

	return err
}

func (c *Client) Remove(ctx context.Context, containerID string) error {
	err := c.api.ContainerRemove(ctx, containerID, container.RemoveOptions{Force: true})
	if errdefs.IsNotFound(err) {
		return nil
	}

	return err
}

func (c *Client) Logs(ctx context.Context, containerID string, lines string) (io.ReadCloser, error) {
	return c.api.ContainerLogs(ctx, containerID, container.LogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Tail:       lines,
	})
}

func (c *Client) Events(ctx context.Context, project string) (<-chan events.Message, <-chan error) {
	f := filters.NewArgs()
	f.Add("type", "container")
	f.Add("label", "release.enable=true")
	if project != "" {
		f.Add("label", fmt.Sprintf("com.docker.compose.project=%s", project))
	}

	return c.api.Events(ctx, events.ListOptions{Filters: f})
}

func (c *Client) Exec(ctx context.Context, containerID string, cmd []string) error {
	exec, err := c.api.ContainerExecCreate(ctx, containerID, container.ExecOptions{
		Cmd: cmd,
	})
	if err != nil {
		return fmt.Errorf("exec create: %w", err)
	}

	if err := c.api.ContainerExecStart(ctx, exec.ID, container.ExecStartOptions{}); err != nil {
		return fmt.Errorf("exec start: %w", err)
	}

	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		inspect, err := c.api.ContainerExecInspect(ctx, exec.ID)
		if err != nil {
			return fmt.Errorf("exec inspect: %w", err)
		}

		if !inspect.Running {
			if inspect.ExitCode != 0 {
				return fmt.Errorf("exec %q exited with code %d", strings.Join(cmd, " "), inspect.ExitCode)
			}
			return nil
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (c *Client) FindContainerByService(ctx context.Context, serviceName string) (types.Container, error) {
	return c.FindContainerByServiceInProject(ctx, "", serviceName)
}

func (c *Client) FindContainerByServiceInProject(ctx context.Context, project, serviceName string) (types.Container, error) {
	return c.findContainerByService(ctx, project, serviceName, true)
}

func (c *Client) FindAnyContainerByServiceInProject(ctx context.Context, project, serviceName string) (types.Container, error) {
	return c.findContainerByService(ctx, project, serviceName, false)
}

func (c *Client) findContainerByService(ctx context.Context, project, serviceName string, runningOnly bool) (types.Container, error) {
	f := filters.NewArgs()
	f.Add("label", fmt.Sprintf("com.docker.compose.service=%s", serviceName))
	if runningOnly {
		f.Add("status", "running")
	}
	if project != "" {
		f.Add("label", fmt.Sprintf("com.docker.compose.project=%s", project))
	}

	containers, err := c.api.ContainerList(ctx, container.ListOptions{All: !runningOnly, Filters: f})
	if err != nil {
		return types.Container{}, fmt.Errorf("listing containers: %w", err)
	}

	if len(containers) == 0 {
		state := "running"
		if !runningOnly {
			state = "any"
		}
		if project != "" {
			return types.Container{}, fmt.Errorf("no %s container found for service %q in project %q", state, serviceName, project)
		}
		return types.Container{}, fmt.Errorf("no %s container found for service %q", state, serviceName)
	}

	return containers[0], nil
}

func (c *Client) FindContainerByImage(ctx context.Context, project, keyword string) (types.Container, error) {
	return c.findContainerByImage(ctx, project, keyword, true)
}

func (c *Client) FindAnyContainerByImage(ctx context.Context, project, keyword string) (types.Container, error) {
	return c.findContainerByImage(ctx, project, keyword, false)
}

func (c *Client) findContainerByImage(ctx context.Context, project, keyword string, runningOnly bool) (types.Container, error) {
	f := filters.NewArgs()
	if runningOnly {
		f.Add("status", "running")
	}
	if project != "" {
		f.Add("label", fmt.Sprintf("com.docker.compose.project=%s", project))
	}

	containers, err := c.api.ContainerList(ctx, container.ListOptions{All: !runningOnly, Filters: f})
	if err != nil {
		return types.Container{}, fmt.Errorf("listing containers: %w", err)
	}

	kw := strings.ToLower(keyword)
	for _, ctr := range containers {
		if strings.ToLower(ctr.Labels[bundledProxyLabel]) == kw {
			return ctr, nil
		}
	}
	for _, ctr := range containers {
		if ctr.Labels[imageTitleLabel] == "docker-release" {
			continue
		}

		if strings.Contains(strings.ToLower(ctr.Image), kw) {
			return ctr, nil
		}
	}

	if project != "" {
		return types.Container{}, fmt.Errorf("no container found in project %q with image containing %q", project, keyword)
	}
	return types.Container{}, fmt.Errorf("no container found with image containing %q", keyword)
}

func (c *Client) Start(ctx context.Context, containerID string) error {
	return c.api.ContainerStart(ctx, containerID, container.StartOptions{})
}

func (c *Client) MaxServiceContainerNumber(ctx context.Context, project, service string) int {
	f := filters.NewArgs()
	if project != "" {
		f.Add("label", fmt.Sprintf("com.docker.compose.project=%s", project))
	}
	f.Add("label", fmt.Sprintf("com.docker.compose.service=%s", service))

	containers, err := c.api.ContainerList(ctx, container.ListOptions{All: true, Filters: f})
	if err != nil {
		return 0
	}

	max := 0
	for _, ctr := range containers {
		if n, err := strconv.Atoi(ctr.Labels["com.docker.compose.container-number"]); err == nil && n > max {
			max = n
		}
	}
	return max
}

// CreateContainerFromImage starts a copy of ref from the image its tag points
// at now. The copy gets the new image's defaults (env, command, labels, health
// check, ...) with only the settings the container set itself laid over them.
// ListServiceContainers lists every container of a Compose service, running or not.
func (c *Client) ListServiceContainers(ctx context.Context, project, service string) ([]types.Container, error) {
	f := filters.NewArgs()
	f.Add("label", "com.docker.compose.service="+service)
	if project != "" {
		f.Add("label", composeProjectLabel+"="+project)
	}
	return c.api.ContainerList(ctx, container.ListOptions{All: true, Filters: f})
}

func (c *Client) CreateContainerFromImage(ctx context.Context, ref types.Container, num int) (string, error) {
	refInfo, err := c.api.ContainerInspect(ctx, ref.ID)
	if err != nil {
		return "", fmt.Errorf("inspecting reference container: %w", err)
	}

	cfg := c.ownConfig(ctx, refInfo)
	cfg.Labels[composeContainerNumberLabel] = strconv.Itoa(num)

	var hostCfg *container.HostConfig
	if refInfo.HostConfig != nil {
		copied := *refInfo.HostConfig
		copied.PortBindings = nil
		hostCfg = &copied
	}

	endpoints := cloneEndpoints(refInfo)
	var networkCfg *network.NetworkingConfig
	if len(endpoints) > 0 {
		primary := primaryNetwork(refInfo, endpoints)
		networkCfg = &network.NetworkingConfig{
			EndpointsConfig: map[string]*network.EndpointSettings{primary: endpoints[primary]},
		}
		delete(endpoints, primary)
	}

	name := nextContainerName(ref, num)
	resp, err := c.api.ContainerCreate(ctx, cfg, hostCfg, networkCfg, nil, name)
	if err != nil {
		return "", fmt.Errorf("creating container: %w", err)
	}

	for _, name := range sortedKeys(endpoints) {
		if err := c.api.NetworkConnect(ctx, endpoints[name].NetworkID, resp.ID, endpoints[name]); err != nil {
			_ = c.api.ContainerRemove(ctx, resp.ID, container.RemoveOptions{Force: true})
			return "", fmt.Errorf("connecting to network %s: %w", name, err)
		}
	}

	if err := c.api.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		_ = c.api.ContainerRemove(ctx, resp.ID, container.RemoveOptions{Force: true})
		return "", fmt.Errorf("starting container: %w", err)
	}

	return resp.ID, nil
}

// ownConfig returns the settings the container set itself, so Docker fills the
// rest from the image the tag points at now. A setting that equals the old
// image's value came from that image and is left out. When the old image can't
// be read, the container's whole config is kept.
func (c *Client) ownConfig(ctx context.Context, info types.ContainerJSON) *container.Config {
	cfg := *info.Config
	cfg.Labels = make(map[string]string, len(info.Config.Labels)+1)
	for k, v := range info.Config.Labels {
		cfg.Labels[k] = v
	}
	if info.ID != "" && len(info.ID) >= 12 && cfg.Hostname == info.ID[:12] {
		cfg.Hostname = ""
	}

	oldImage, _, err := c.api.ImageInspectWithRaw(ctx, info.Image)
	if err != nil || oldImage.Config == nil {
		slog.Warn("old image not readable; copying the container's whole config", "component", "docker", "image", info.Image, "err", err)
		return &cfg
	}
	base := oldImage.Config

	cfg.Env = nil
	for _, kv := range info.Config.Env {
		if !slices.Contains(base.Env, kv) {
			cfg.Env = append(cfg.Env, kv)
		}
	}
	for k, v := range base.Labels {
		if cfg.Labels[k] == v {
			delete(cfg.Labels, k)
		}
	}
	cfg.ExposedPorts = nat.PortSet{}
	for port := range info.Config.ExposedPorts {
		if _, ok := base.ExposedPorts[port]; !ok {
			cfg.ExposedPorts[port] = struct{}{}
		}
	}
	cfg.Volumes = map[string]struct{}{}
	for path := range info.Config.Volumes {
		if _, ok := base.Volumes[path]; !ok {
			cfg.Volumes[path] = struct{}{}
		}
	}
	// A command only counts as inherited together with the entrypoint: Docker
	// drops the image command when the container sets its own entrypoint.
	if slices.Equal(info.Config.Entrypoint, base.Entrypoint) {
		cfg.Entrypoint = nil
		if slices.Equal(info.Config.Cmd, base.Cmd) {
			cfg.Cmd = nil
		}
	}
	if reflect.DeepEqual(info.Config.Healthcheck, base.Healthcheck) {
		cfg.Healthcheck = nil
	}
	if cfg.WorkingDir == base.WorkingDir {
		cfg.WorkingDir = ""
	}
	if cfg.User == base.User {
		cfg.User = ""
	}
	if cfg.StopSignal == base.StopSignal {
		cfg.StopSignal = ""
	}
	return &cfg
}

func nextContainerName(ref types.Container, num int) string {
	for _, name := range ref.Names {
		n := strings.TrimPrefix(name, "/")
		if idx := strings.LastIndexAny(n, "-_"); idx != -1 {
			if _, err := strconv.Atoi(n[idx+1:]); err == nil {
				return n[:idx+1] + strconv.Itoa(num)
			}
		}
	}

	project := ref.Labels[composeProjectLabel]
	service := ref.Labels["com.docker.compose.service"]
	return fmt.Sprintf("%s-%s-%d", project, service, num)
}

// cloneEndpoints returns one endpoint per network the container is on, keeping
// its aliases but not its own name or ID, and not its address.
func cloneEndpoints(info types.ContainerJSON) map[string]*network.EndpointSettings {
	if info.NetworkSettings == nil || networkModeHasNoEndpoints(info) {
		return nil
	}

	own := map[string]bool{strings.TrimPrefix(info.Name, "/"): true, info.ID: true}
	if len(info.ID) >= 12 {
		own[info.ID[:12]] = true
	}

	endpoints := make(map[string]*network.EndpointSettings, len(info.NetworkSettings.Networks))
	for name, endpoint := range info.NetworkSettings.Networks {
		var aliases []string
		for _, alias := range endpoint.Aliases {
			if !own[alias] {
				aliases = append(aliases, alias)
			}
		}
		endpoints[name] = &network.EndpointSettings{NetworkID: endpoint.NetworkID, Aliases: aliases}
	}
	return endpoints
}

func networkModeHasNoEndpoints(info types.ContainerJSON) bool {
	if info.HostConfig == nil {
		return false
	}
	mode := info.HostConfig.NetworkMode
	return mode.IsHost() || mode.IsNone() || mode.IsContainer()
}

// primaryNetwork is the network named by the container's network mode, else the
// first one by name.
func primaryNetwork(info types.ContainerJSON, endpoints map[string]*network.EndpointSettings) string {
	if info.HostConfig != nil {
		if _, ok := endpoints[string(info.HostConfig.NetworkMode)]; ok {
			return string(info.HostConfig.NetworkMode)
		}
	}
	return sortedKeys(endpoints)[0]
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// ContainerAddrVia returns the container's upstream address using an IP on a
// network that proxyID is also on. With no proxyID, any network will do.
func (c *Client) ContainerAddrVia(ctx context.Context, containerID, proxyID string) (string, error) {
	info, err := c.api.ContainerInspect(ctx, containerID)
	if err != nil {
		return "", fmt.Errorf("inspecting container: %w", err)
	}

	var proxyNetworks map[string]bool
	if proxyID != "" {
		proxy, err := c.api.ContainerInspect(ctx, proxyID)
		if err != nil {
			return "", fmt.Errorf("inspecting proxy container: %w", err)
		}
		if !networkModeHasNoEndpoints(proxy) {
			proxyNetworks = networkIDs(proxy)
		}
	}

	for _, name := range sortedKeys(info.NetworkSettings.Networks) {
		endpoint := info.NetworkSettings.Networks[name]
		if endpoint.IPAddress == "" || (proxyNetworks != nil && !proxyNetworks[endpoint.NetworkID]) {
			continue
		}
		return fmt.Sprintf("%s:%s", endpoint.IPAddress, firstExposedPort(info)), nil
	}

	if proxyNetworks != nil {
		return "", fmt.Errorf("container %s shares no network with the proxy", containerID[:12])
	}
	return "", fmt.Errorf("no network address for container %s", containerID[:12])
}

func networkIDs(info types.ContainerJSON) map[string]bool {
	ids := make(map[string]bool)
	if info.NetworkSettings != nil {
		for _, endpoint := range info.NetworkSettings.Networks {
			ids[endpoint.NetworkID] = true
		}
	}
	return ids
}

// firstExposedPort returns the lowest-numbered exposed port so the chosen
// upstream address is stable across calls (ExposedPorts is a map; ranging it
// is nondeterministic and could flip the port between config regenerations).
func firstExposedPort(info types.ContainerJSON) string {
	best := -1
	for port := range info.Config.ExposedPorts {
		if n := port.Int(); n > 0 && (best == -1 || n < best) {
			best = n
		}
	}
	if best == -1 {
		return "80"
	}
	return strconv.Itoa(best)
}

func (c *Client) IsHealthy(ctx context.Context, containerID string) (bool, error) {
	info, err := c.api.ContainerInspect(ctx, containerID)
	if err != nil {
		return false, fmt.Errorf("inspecting container: %w", err)
	}

	if !info.State.Running {
		return false, nil
	}

	if info.State.Health == nil {
		return true, nil
	}

	// "starting" means the healthcheck hasn't determined status yet — the
	// container is running, so treat it as healthy until proven otherwise.
	// Only an explicit "unhealthy" status should mark a container as down.
	return info.State.Health.Status != "unhealthy", nil
}

func (c *Client) RestartCount(ctx context.Context, containerID string) (int, error) {
	info, err := c.api.ContainerInspect(ctx, containerID)
	if err != nil {
		return 0, fmt.Errorf("inspecting container: %w", err)
	}

	return info.RestartCount, nil
}

func (c *Client) WaitHealthy(ctx context.Context, containerID string, timeout time.Duration) error {
	deadline := time.After(timeout)
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-deadline:
			return fmt.Errorf("container %s did not become healthy within %s", containerID[:12], timeout)
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			info, err := c.api.ContainerInspect(ctx, containerID)
			if err != nil {
				return fmt.Errorf("inspecting container: %w", err)
			}

			if info.State.Health == nil {
				return nil
			}

			if info.State.Health.Status == "healthy" {
				return nil
			}
		}
	}
}

func (c *Client) Close() error {
	return c.api.Close()
}

func (c *Client) ContainerEnv(ctx context.Context, containerID string) ([]string, error) {
	info, err := c.api.ContainerInspect(ctx, containerID)
	if err != nil {
		return nil, fmt.Errorf("inspecting container: %w", err)
	}
	return info.Config.Env, nil
}
