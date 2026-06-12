package docker

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	"github.com/docker/go-connections/nat"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// mockDockerAPI implements only the methods called by CreateContainerFromImage.
// Unimplemented methods are satisfied by the embedded nil interface and will
// panic if reached — acceptable since tests don't exercise those paths.
type mockDockerAPI struct {
	client.APIClient

	inspectResult types.ContainerJSON
	createResult  container.CreateResponse
	createErr     error
	startErr      error

	capturedConfig     *container.Config
	capturedHostConfig *container.HostConfig
	connectedNetworks  []string
	removedIDs         []string
}

func (m *mockDockerAPI) ContainerInspect(_ context.Context, _ string) (types.ContainerJSON, error) {
	return m.inspectResult, nil
}

func (m *mockDockerAPI) ContainerCreate(_ context.Context, cfg *container.Config, hostCfg *container.HostConfig, _ *network.NetworkingConfig, _ *ocispec.Platform, _ string) (container.CreateResponse, error) {
	m.capturedConfig = cfg
	m.capturedHostConfig = hostCfg
	return m.createResult, m.createErr
}

func (m *mockDockerAPI) NetworkConnect(_ context.Context, networkID, _ string, _ *network.EndpointSettings) error {
	m.connectedNetworks = append(m.connectedNetworks, networkID)
	return nil
}

func (m *mockDockerAPI) ContainerStart(_ context.Context, _ string, _ container.StartOptions) error {
	return m.startErr
}

func (m *mockDockerAPI) ContainerRemove(_ context.Context, id string, _ container.RemoveOptions) error {
	m.removedIDs = append(m.removedIDs, id)
	return nil
}

func refFixture() (types.Container, types.ContainerJSON) {
	ref := types.Container{
		ID: "ref-id",
		Labels: map[string]string{
			"com.docker.compose.project":          "myproject",
			"com.docker.compose.service":          "api",
			"com.docker.compose.container-number": "1",
			"release.enable":                      "true",
		},
		Names: []string{"/myproject-api-1"},
		NetworkSettings: &types.SummaryNetworkSettings{
			Networks: map[string]*network.EndpointSettings{
				"myproject_default": {NetworkID: "net-abc"},
			},
		},
	}

	refInfo := types.ContainerJSON{
		ContainerJSONBase: &types.ContainerJSONBase{
			HostConfig: &container.HostConfig{
				Binds: []string{"/host/db:/app/db", "/host/secrets:/app/secrets:ro"},
				RestartPolicy: container.RestartPolicy{
					Name:              "unless-stopped",
					MaximumRetryCount: 0,
				},
				PortBindings: nat.PortMap{
					"8081/tcp": []nat.PortBinding{{HostIP: "127.0.0.1", HostPort: "8081"}},
				},
				ShmSize: 67108864,
			},
		},
		Config: &container.Config{
			Image:      "nkap/api:latest",
			Cmd:        []string{"./server", "--prod"},
			Entrypoint: []string{"/bin/sh", "-c"},
			WorkingDir: "/app",
			User:       "1000:1000",
			Env: []string{
				"PORT=8081",
				"DB_PATH=/app/db/nkap.db",
				"SECRET_KEY=super-secret",
				"APP_URL=https://nkap.malico.me",
			},
			ExposedPorts: nat.PortSet{
				"8081/tcp": struct{}{},
			},
			Healthcheck: &container.HealthConfig{
				Test:     []string{"CMD", "wget", "--spider", "http://localhost:8081/health"},
				Interval: 30 * time.Second,
				Timeout:  5 * time.Second,
				Retries:  3,
			},
			Labels: map[string]string{
				"com.docker.compose.project":          "myproject",
				"com.docker.compose.service":          "api",
				"com.docker.compose.container-number": "1",
				"release.enable":                      "true",
			},
		},
	}

	return ref, refInfo
}

func newClient(mock *mockDockerAPI) *Client {
	return &Client{api: mock}
}

func TestCreateContainerFromImage_CopiesConfigFields(t *testing.T) {
	t.Parallel()

	ref, refInfo := refFixture()
	mock := &mockDockerAPI{
		inspectResult: refInfo,
		createResult:  container.CreateResponse{ID: "new-id"},
	}

	if _, err := newClient(mock).CreateContainerFromImage(context.Background(), ref, 2); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	cfg := mock.capturedConfig
	if cfg == nil {
		t.Fatal("no config captured")
	}

	if cfg.Image != refInfo.Config.Image {
		t.Errorf("Image: got %q, want %q", cfg.Image, refInfo.Config.Image)
	}
	if cfg.WorkingDir != refInfo.Config.WorkingDir {
		t.Errorf("WorkingDir: got %q, want %q", cfg.WorkingDir, refInfo.Config.WorkingDir)
	}
	if cfg.User != refInfo.Config.User {
		t.Errorf("User: got %q, want %q", cfg.User, refInfo.Config.User)
	}
	if fmt.Sprint(cfg.Cmd) != fmt.Sprint(refInfo.Config.Cmd) {
		t.Errorf("Cmd: got %v, want %v", cfg.Cmd, refInfo.Config.Cmd)
	}
	if fmt.Sprint(cfg.Entrypoint) != fmt.Sprint(refInfo.Config.Entrypoint) {
		t.Errorf("Entrypoint: got %v, want %v", cfg.Entrypoint, refInfo.Config.Entrypoint)
	}
	if len(cfg.Env) != len(refInfo.Config.Env) {
		t.Fatalf("Env len: got %d, want %d", len(cfg.Env), len(refInfo.Config.Env))
	}
	for i, e := range refInfo.Config.Env {
		if cfg.Env[i] != e {
			t.Errorf("Env[%d]: got %q, want %q", i, cfg.Env[i], e)
		}
	}
	if len(cfg.ExposedPorts) != len(refInfo.Config.ExposedPorts) {
		t.Errorf("ExposedPorts len: got %d, want %d", len(cfg.ExposedPorts), len(refInfo.Config.ExposedPorts))
	}

	if cfg.Healthcheck == nil {
		t.Fatal("Healthcheck not copied")
	}
	if cfg.Healthcheck.Interval != refInfo.Config.Healthcheck.Interval {
		t.Errorf("Healthcheck.Interval: got %v, want %v", cfg.Healthcheck.Interval, refInfo.Config.Healthcheck.Interval)
	}
	if cfg.Healthcheck.Timeout != refInfo.Config.Healthcheck.Timeout {
		t.Errorf("Healthcheck.Timeout: got %v, want %v", cfg.Healthcheck.Timeout, refInfo.Config.Healthcheck.Timeout)
	}
	if cfg.Healthcheck.Retries != refInfo.Config.Healthcheck.Retries {
		t.Errorf("Healthcheck.Retries: got %d, want %d", cfg.Healthcheck.Retries, refInfo.Config.Healthcheck.Retries)
	}
}

func TestCreateContainerFromImage_CopiesHostConfigFields(t *testing.T) {
	t.Parallel()

	ref, refInfo := refFixture()
	mock := &mockDockerAPI{
		inspectResult: refInfo,
		createResult:  container.CreateResponse{ID: "new-id"},
	}

	if _, err := newClient(mock).CreateContainerFromImage(context.Background(), ref, 2); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	hc := mock.capturedHostConfig
	if hc == nil {
		t.Fatal("no host config captured")
	}

	if len(hc.Binds) != len(refInfo.HostConfig.Binds) {
		t.Fatalf("Binds len: got %d, want %d", len(hc.Binds), len(refInfo.HostConfig.Binds))
	}
	for i, b := range refInfo.HostConfig.Binds {
		if hc.Binds[i] != b {
			t.Errorf("Binds[%d]: got %q, want %q", i, hc.Binds[i], b)
		}
	}
	if hc.RestartPolicy.Name != refInfo.HostConfig.RestartPolicy.Name {
		t.Errorf("RestartPolicy.Name: got %q, want %q", hc.RestartPolicy.Name, refInfo.HostConfig.RestartPolicy.Name)
	}
	if hc.ShmSize != refInfo.HostConfig.ShmSize {
		t.Errorf("ShmSize: got %d, want %d", hc.ShmSize, refInfo.HostConfig.ShmSize)
	}
}

func TestCreateContainerFromImage_ClearsPortBindings(t *testing.T) {
	t.Parallel()

	ref, refInfo := refFixture()
	mock := &mockDockerAPI{
		inspectResult: refInfo,
		createResult:  container.CreateResponse{ID: "new-id"},
	}

	if _, err := newClient(mock).CreateContainerFromImage(context.Background(), ref, 2); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(mock.capturedHostConfig.PortBindings) != 0 {
		t.Errorf("PortBindings should be empty, got %v", mock.capturedHostConfig.PortBindings)
	}
}

func TestCreateContainerFromImage_UpdatesContainerNumberLabel(t *testing.T) {
	t.Parallel()

	ref, refInfo := refFixture()
	mock := &mockDockerAPI{
		inspectResult: refInfo,
		createResult:  container.CreateResponse{ID: "new-id"},
	}

	if _, err := newClient(mock).CreateContainerFromImage(context.Background(), ref, 3); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	cfg := mock.capturedConfig
	if got := cfg.Labels["com.docker.compose.container-number"]; got != "3" {
		t.Errorf("container-number: got %q, want %q", got, "3")
	}
	if cfg.Labels["com.docker.compose.project"] != "myproject" {
		t.Error("compose project label should be preserved")
	}
	if cfg.Labels["release.enable"] != "true" {
		t.Error("release.enable label should be preserved")
	}
}

func TestCreateContainerFromImage_ConnectsAdditionalNetworks(t *testing.T) {
	t.Parallel()

	ref, refInfo := refFixture()
	ref.NetworkSettings.Networks["myproject_extra"] = &network.EndpointSettings{NetworkID: "net-extra-xyz"}

	mock := &mockDockerAPI{
		inspectResult: refInfo,
		createResult:  container.CreateResponse{ID: "new-id"},
	}

	if _, err := newClient(mock).CreateContainerFromImage(context.Background(), ref, 2); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// One network is primary (via ContainerCreate networkConfig), the other via NetworkConnect.
	if len(mock.connectedNetworks) != 1 {
		t.Errorf("expected 1 additional NetworkConnect call, got %d: %v", len(mock.connectedNetworks), mock.connectedNetworks)
	}
}

func TestCreateContainerFromImage_CleansUpOnStartFailure(t *testing.T) {
	t.Parallel()

	ref, refInfo := refFixture()
	mock := &mockDockerAPI{
		inspectResult: refInfo,
		createResult:  container.CreateResponse{ID: "new-id"},
		startErr:      fmt.Errorf("start failed"),
	}

	_, err := newClient(mock).CreateContainerFromImage(context.Background(), ref, 2)
	if err == nil {
		t.Fatal("expected error on start failure")
	}

	if len(mock.removedIDs) != 1 || mock.removedIDs[0] != "new-id" {
		t.Errorf("expected container new-id to be removed on failure, got %v", mock.removedIDs)
	}
}

func TestFirstExposedPortDeterministic(t *testing.T) {
	info := types.ContainerJSON{
		Config: &container.Config{
			ExposedPorts: nat.PortSet{
				"9090/tcp": struct{}{},
				"8080/tcp": struct{}{},
				"8443/tcp": struct{}{},
			},
		},
	}

	// Lowest-numbered port wins, regardless of map iteration order.
	for i := 0; i < 50; i++ {
		if got := firstExposedPort(info); got != "8080" {
			t.Fatalf("firstExposedPort: want 8080, got %s", got)
		}
	}
}

func TestFirstExposedPortFallback(t *testing.T) {
	info := types.ContainerJSON{Config: &container.Config{}}
	if got := firstExposedPort(info); got != "80" {
		t.Errorf("firstExposedPort with no ports: want 80, got %s", got)
	}
}
