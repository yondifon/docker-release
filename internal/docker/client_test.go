package docker

import (
	"context"
	"fmt"
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
	listResult    []types.Container
	startErr      error
	removedIDs    []string
}

func (m *mockDockerAPI) ContainerInspect(context.Context, string) (types.ContainerJSON, error) {
	return m.inspectResult, nil
}

func (m *mockDockerAPI) ContainerList(context.Context, container.ListOptions) ([]types.Container, error) {
	return m.listResult, nil
}

func (m *mockDockerAPI) ContainerCreate(context.Context, *container.Config, *container.HostConfig, *network.NetworkingConfig, *ocispec.Platform, string) (container.CreateResponse, error) {
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
