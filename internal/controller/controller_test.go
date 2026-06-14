package controller

import (
	"testing"

	"github.com/docker/docker/api/types"

	"github.com/malico/docker-release/internal/config"
)

func TestSplitByRevisionUsesNewestComposeConfigHash(t *testing.T) {
	t.Parallel()

	containers := []types.Container{
		containerWithRevision("old-1", 10, "image-a", "hash-old"),
		containerWithRevision("old-2", 11, "image-a", "hash-old"),
		containerWithRevision("new-1", 20, "image-a", "hash-new"),
		containerWithRevision("new-2", 21, "image-a", "hash-new"),
	}

	oldContainers, newContainers := splitByRevision(containers, groupByRevision(containers))

	assertContainerIDs(t, oldContainers, []string{"old-1", "old-2"})
	assertContainerIDs(t, newContainers, []string{"new-1", "new-2"})
}

func TestSplitByRevisionFallsBackToImageID(t *testing.T) {
	t.Parallel()

	containers := []types.Container{
		containerWithRevision("old-1", 10, "image-a", ""),
		containerWithRevision("new-1", 20, "image-b", ""),
	}

	oldContainers, newContainers := splitByRevision(containers, groupByRevision(containers))

	assertContainerIDs(t, oldContainers, []string{"old-1"})
	assertContainerIDs(t, newContainers, []string{"new-1"})
}

func containerWithRevision(id string, created int64, imageID string, configHash string) types.Container {
	labels := map[string]string{}
	if configHash != "" {
		labels["com.docker.compose.config-hash"] = configHash
	}

	return types.Container{
		ID:      id,
		Created: created,
		ImageID: imageID,
		Labels:  labels,
	}
}

// TestGroupContainersByServiceIsolatesProjects verifies that global mode
// (no per-project filter) does not merge same-named services across projects.
func TestGroupContainersByServiceIsolatesProjects(t *testing.T) {
	t.Parallel()

	containers := []types.Container{
		containerWithProject("foo-app-1", "foo", "app"),
		containerWithProject("foo-app-2", "foo", "app"),
		containerWithProject("bar-app-1", "bar", "app"),
		containerWithProject("foo-api-1", "foo", "api"),
	}

	groups := groupContainersByService(containers)

	fooApp := groups[serviceKey{project: "foo", service: "app"}]
	if len(fooApp) != 2 {
		t.Errorf("foo/app: want 2 containers, got %d", len(fooApp))
	}

	barApp := groups[serviceKey{project: "bar", service: "app"}]
	if len(barApp) != 1 {
		t.Errorf("bar/app: want 1 container, got %d", len(barApp))
	}

	fooAPI := groups[serviceKey{project: "foo", service: "api"}]
	if len(fooAPI) != 1 {
		t.Errorf("foo/api: want 1 container, got %d", len(fooAPI))
	}
}

// TestFilterServiceContainersRespectProject verifies that filterServiceContainers
// does not return containers from a different project when project is set.
func TestFilterServiceContainersRespectProject(t *testing.T) {
	t.Parallel()

	containers := []types.Container{
		containerWithProject("foo-app", "foo", "app"),
		containerWithProject("bar-app", "bar", "app"),
	}

	fooKey := serviceKey{project: "foo", service: "app"}
	got := filterServiceContainers(containers, fooKey)
	if len(got) != 1 || got[0].ID != "foo-app" {
		t.Errorf("expected only foo-app, got %v", got)
	}
}

// TestServiceKeyDeployKey verifies composite key format used as map key.
func TestServiceKeyDeployKey(t *testing.T) {
	t.Parallel()

	cases := []struct {
		key  serviceKey
		want string
	}{
		{serviceKey{project: "myproject", service: "app"}, "myproject/app"},
		{serviceKey{project: "", service: "app"}, "app"},
	}
	for _, c := range cases {
		if got := c.key.deployKey(); got != c.want {
			t.Errorf("deployKey() = %q, want %q", got, c.want)
		}
	}
}

// TestSupportedInGlobalMode locks in which providers a single shared controller
// may manage. Per-service-file providers collide across projects, so only
// nginx-proxy and none are allowed; if this list changes, the collision risk
// (filename + upstream name) must be handled first.
func TestSupportedInGlobalMode(t *testing.T) {
	t.Parallel()

	cases := []struct {
		provider config.ProviderType
		want     bool
	}{
		{config.ProviderNginxProxy, true},
		{config.ProviderNone, true},
		{config.ProviderNginx, false},
		{config.ProviderAngie, false},
		{config.ProviderCaddy, false},
		{config.ProviderHAProxy, false},
		{config.ProviderTraefik, false},
	}
	for _, c := range cases {
		if got := supportedInGlobalMode(c.provider); got != c.want {
			t.Errorf("supportedInGlobalMode(%q) = %v, want %v", c.provider, got, c.want)
		}
	}
}

func containerWithProject(id, project, service string) types.Container {
	return types.Container{
		ID: id,
		Labels: map[string]string{
			"com.docker.compose.project": project,
			"com.docker.compose.service": service,
		},
	}
}

func assertContainerIDs(t *testing.T, containers []types.Container, want []string) {
	t.Helper()

	if len(containers) != len(want) {
		t.Fatalf("len(containers) = %d, want %d", len(containers), len(want))
	}

	for i := range want {
		if containers[i].ID != want[i] {
			t.Fatalf("containers[%d].ID = %q, want %q", i, containers[i].ID, want[i])
		}
	}
}
