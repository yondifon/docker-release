package docker

import (
	"context"
	"fmt"
	"slices"
	"testing"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	"github.com/docker/go-connections/nat"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

type mockDockerAPI struct {
	client.APIClient
	inspectResult types.ContainerJSON
	inspectByID   map[string]types.ContainerJSON
	oldImage      *container.Config
	listResult    []types.Container
	startErr      error
	removedIDs    []string
	created       *container.Config
	createdNets   *network.NetworkingConfig
	connected     map[string]*network.EndpointSettings
}

func (m *mockDockerAPI) ContainerInspect(_ context.Context, id string) (types.ContainerJSON, error) {
	if info, ok := m.inspectByID[id]; ok {
		return info, nil
	}
	return m.inspectResult, nil
}

func (m *mockDockerAPI) ImageInspectWithRaw(context.Context, string) (types.ImageInspect, []byte, error) {
	return types.ImageInspect{Config: m.oldImage}, nil, nil
}

func (m *mockDockerAPI) NetworkConnect(_ context.Context, networkID, _ string, cfg *network.EndpointSettings) error {
	if m.connected == nil {
		m.connected = map[string]*network.EndpointSettings{}
	}
	m.connected[networkID] = cfg
	return nil
}

func (m *mockDockerAPI) ContainerList(context.Context, container.ListOptions) ([]types.Container, error) {
	return m.listResult, nil
}

func (m *mockDockerAPI) ContainerCreate(_ context.Context, cfg *container.Config, _ *container.HostConfig, nets *network.NetworkingConfig, _ *ocispec.Platform, _ string) (container.CreateResponse, error) {
	m.created, m.createdNets = cfg, nets
	return container.CreateResponse{ID: "new-id"}, nil
}

func (m *mockDockerAPI) ContainerStart(context.Context, string, container.StartOptions) error {
	return m.startErr
}

func (m *mockDockerAPI) ContainerRemove(_ context.Context, id string, _ container.RemoveOptions) error {
	m.removedIDs = append(m.removedIDs, id)
	return nil
}

func TestFindContainerByImageSkipsControllerOnlyImage(t *testing.T) {
	api := &mockDockerAPI{listResult: []types.Container{
		{ID: "controller-id", Image: "malico/docker-release-nginx:latest", Labels: map[string]string{imageTitleLabel: "docker-release"}},
		{ID: "nginx-id", Image: "nginx:alpine"},
	}}
	ctr, err := (&Client{api: api}).FindContainerByImage(context.Background(), "", "nginx")
	if err != nil || ctr.ID != "nginx-id" {
		t.Fatalf("proxy detection selected the controller-only image: %+v, %v", ctr, err)
	}
}

func TestFindContainerByImagePrefersBundledProxyOverUnrelatedImage(t *testing.T) {
	api := &mockDockerAPI{listResult: []types.Container{
		{ID: "unrelated", Image: "my-nginx-app:latest"},
		{ID: "bundled-id", Image: "private/proxy:1", Labels: map[string]string{imageTitleLabel: "docker-release", bundledProxyLabel: "nginx"}},
	}}
	ctr, err := (&Client{api: api}).FindContainerByImage(context.Background(), "", "nginx")
	if err != nil || ctr.ID != "bundled-id" {
		t.Fatalf("proxy detection ignored the explicit bundled proxy label: %+v, %v", ctr, err)
	}
}

func TestCreateContainerFromImageCleansUpOnStartFailure(t *testing.T) {
	api := &mockDockerAPI{
		inspectResult: types.ContainerJSON{ContainerJSONBase: &types.ContainerJSONBase{}, Config: &container.Config{}},
		oldImage:      &container.Config{},
		startErr:      fmt.Errorf("start failed"),
	}
	ref := types.Container{ID: "ref-id", NetworkSettings: &types.SummaryNetworkSettings{}}
	if _, err := (&Client{api: api}).CreateContainerFromImage(context.Background(), ref, 2); err == nil {
		t.Fatal("expected error on start failure")
	}
	if len(api.removedIDs) != 1 || api.removedIDs[0] != "new-id" {
		t.Fatalf("failed replacement was left behind: %v", api.removedIDs)
	}
}

func TestFirstExposedPortDeterministic(t *testing.T) {
	info := types.ContainerJSON{Config: &container.Config{ExposedPorts: nat.PortSet{"9090/tcp": {}, "8080/tcp": {}, "8443/tcp": {}}}}
	for i := 0; i < 50; i++ {
		if got := firstExposedPort(info); got != "8080" {
			t.Fatalf("port selection changed with map iteration order: %s", got)
		}
	}
}

func TestCreateContainerFromImageKeepsOnlyTheContainersOwnSettings(t *testing.T) {
	const id = "0123456789abcdef0123"
	api := &mockDockerAPI{
		oldImage: &container.Config{
			Env:        []string{"APP_VERSION=1", "SHARED=image"},
			Cmd:        []string{"serve", "v1"},
			Labels:     map[string]string{"app.version": "1"},
			WorkingDir: "/app",
		},
		inspectResult: types.ContainerJSON{
			ContainerJSONBase: &types.ContainerJSONBase{
				ID: id, Name: "/shop-app-1", Image: "sha256:old",
				HostConfig: &container.HostConfig{NetworkMode: "shop_default"},
			},
			Config: &container.Config{
				Hostname:   id[:12],
				Image:      "shop-app:latest",
				Env:        []string{"APP_VERSION=1", "SHARED=compose", "FROM_COMPOSE=one"},
				Cmd:        []string{"serve", "v1"},
				Labels:     map[string]string{"app.version": "1", "com.docker.compose.service": "app"},
				WorkingDir: "/app",
			},
			NetworkSettings: &types.NetworkSettings{Networks: map[string]*network.EndpointSettings{
				"shop_default": {NetworkID: "net-default", Aliases: []string{"app", "shop-app-1", id[:12]}},
				"shop_backend": {NetworkID: "net-backend", Aliases: []string{"app", "api"}},
			}},
		},
	}
	ref := types.Container{ID: "ref-id", Names: []string{"/shop-app-1"}}
	if _, err := (&Client{api: api}).CreateContainerFromImage(context.Background(), ref, 2); err != nil {
		t.Fatal(err)
	}

	got := api.created
	if !slices.Equal(got.Env, []string{"SHARED=compose", "FROM_COMPOSE=one"}) {
		t.Errorf("env should hold only what the container set, so the new image's env applies: %v", got.Env)
	}
	if got.Cmd != nil || got.WorkingDir != "" {
		t.Errorf("command and working dir came from the old image and must follow the new one: %v %q", got.Cmd, got.WorkingDir)
	}
	if _, stale := got.Labels["app.version"]; stale {
		t.Errorf("old image label was carried over: %v", got.Labels)
	}
	if got.Labels["com.docker.compose.service"] != "app" || got.Labels["com.docker.compose.container-number"] != "2" {
		t.Errorf("compose labels lost: %v", got.Labels)
	}
	if got.Hostname != "" {
		t.Errorf("the old container's hostname was copied: %q", got.Hostname)
	}

	if aliases := api.createdNets.EndpointsConfig["shop_default"].Aliases; !slices.Equal(aliases, []string{"app"}) {
		t.Errorf("primary network aliases should be the service name only: %v", aliases)
	}
	if aliases := api.connected["net-backend"].Aliases; !slices.Equal(aliases, []string{"app", "api"}) {
		t.Errorf("second network lost its aliases: %v", aliases)
	}
}

func TestCreateContainerFromImageKeepsCmdWhenEntrypointIsOverridden(t *testing.T) {
	api := &mockDockerAPI{
		oldImage: &container.Config{Entrypoint: []string{"/image-entry"}, Cmd: []string{"serve"}},
		inspectResult: types.ContainerJSON{
			ContainerJSONBase: &types.ContainerJSONBase{ID: "0123456789abcdef", Image: "sha256:old"},
			Config:            &container.Config{Entrypoint: []string{"/my-entry"}, Cmd: []string{"serve"}},
		},
	}
	ref := types.Container{ID: "ref-id", NetworkSettings: &types.SummaryNetworkSettings{}}
	if _, err := (&Client{api: api}).CreateContainerFromImage(context.Background(), ref, 2); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(api.created.Entrypoint, []string{"/my-entry"}) || !slices.Equal(api.created.Cmd, []string{"serve"}) {
		t.Fatalf("an overridden entrypoint must keep its own command: %v %v", api.created.Entrypoint, api.created.Cmd)
	}
}

func TestContainerAddrViaUsesANetworkTheProxyIsOn(t *testing.T) {
	api := &mockDockerAPI{inspectByID: map[string]types.ContainerJSON{
		"app-0123456789abcdef": {
			Config: &container.Config{},
			NetworkSettings: &types.NetworkSettings{Networks: map[string]*network.EndpointSettings{
				"a_backend": {NetworkID: "net-backend", IPAddress: "10.0.1.5"},
				"z_default": {NetworkID: "net-default", IPAddress: "10.0.0.5"},
			}},
		},
		"proxy": {
			ContainerJSONBase: &types.ContainerJSONBase{HostConfig: &container.HostConfig{}},
			NetworkSettings: &types.NetworkSettings{Networks: map[string]*network.EndpointSettings{
				"z_default": {NetworkID: "net-default"},
			}},
		},
		"lonely": {
			ContainerJSONBase: &types.ContainerJSONBase{HostConfig: &container.HostConfig{}},
			NetworkSettings: &types.NetworkSettings{Networks: map[string]*network.EndpointSettings{
				"other": {NetworkID: "net-other"},
			}},
		},
	}}
	c := &Client{api: api}

	for i := 0; i < 20; i++ {
		addr, err := c.ContainerAddrVia(context.Background(), "app-0123456789abcdef", "proxy")
		if err != nil || addr != "10.0.0.5:80" {
			t.Fatalf("upstream must use the network the proxy shares: %q, %v", addr, err)
		}
	}
	if _, err := c.ContainerAddrVia(context.Background(), "app-0123456789abcdef", "lonely"); err == nil {
		t.Fatal("a container on no network with the proxy must be an error")
	}
}
