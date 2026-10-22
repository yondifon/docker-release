package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/malico/docker-release/internal/docker"
)

func caddyReloadFixture(t *testing.T) (*CaddyProvider, *atomic.Bool) {
	t.Helper()
	var reject atomic.Bool
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		path := strings.TrimPrefix(r.URL.Path, "/v1.47")
		switch {
		case path == "/containers/json":
			_ = json.NewEncoder(w).Encode([]map[string]any{{
				"Id": "0123456789abcdef", "Image": "caddy:alpine", "State": "running",
			}})
		case strings.HasSuffix(path, "/exec"):
			_, _ = w.Write([]byte(`{"Id":"reload"}`))
		case strings.HasSuffix(path, "/start"):
		case strings.HasSuffix(path, "/json"):
			code := 0
			if reject.Load() {
				code = 1
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"Running": false, "ExitCode": code})
		default:
			t.Errorf("unexpected Docker API request: %s %s", r.Method, path)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(api.Close)
	t.Setenv("DOCKER_HOST", api.URL)
	t.Setenv("DOCKER_API_VERSION", "1.47")
	t.Setenv("DR_BUNDLED_PROXY", "")
	client, err := docker.NewClient()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return NewCaddyRoutes(t.TempDir(), CaddyRoute{Host: "app.localhost"}, client, "", "test"), &reject
}

func TestCaddyRejectedConfigDoesNotPoisonLaterReleases(t *testing.T) {
	p, reject := caddyReloadFixture(t)
	ctx := context.Background()
	state := &UpstreamState{Service: "app", Servers: []Server{{Addr: "127.0.0.1:8080"}}}
	if err := p.GenerateConfig(ctx, state); err != nil {
		t.Fatal(err)
	}
	if err := p.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(p.configDir, "app.caddy")
	working, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	reject.Store(true)
	p.route = CaddyRoute{Path: "/app/"}
	state.Servers[0].Addr = "127.0.0.1:9090"
	if err := p.GenerateConfig(ctx, state); err != nil {
		t.Fatal(err)
	}
	if err := p.Reload(ctx); err == nil {
		t.Fatal("release accepted a config Caddy rejected")
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != string(working) {
		t.Fatalf("working route was not restored: %s, %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(p.configDir, "app.location")); !os.IsNotExist(err) {
		t.Fatal("rejected path route remains on disk")
	}
	reject.Store(false)
	other := NewCaddyRoutes(p.configDir, CaddyRoute{Host: "api.localhost"}, p.docker, "", "test")
	state.Service = "api"
	if err := other.GenerateConfig(ctx, state); err != nil {
		t.Fatal(err)
	}
	if err := other.Reload(ctx); err != nil {
		t.Fatalf("rejected app config broke another app's reload: %v", err)
	}
}

func TestCaddyRouteChangesAndServiceRemovalCleanBothFiles(t *testing.T) {
	p, _ := caddyReloadFixture(t)
	ctx := context.Background()
	state := &UpstreamState{Service: "app", Servers: []Server{{Addr: "127.0.0.1:8080"}}}
	for _, route := range []CaddyRoute{{Path: "/app/"}, {Host: "app.localhost"}, {Path: "/app/"}} {
		p.route = route
		if err := p.GenerateConfig(ctx, state); err != nil {
			t.Fatal(err)
		}
		if err := p.Reload(ctx); err != nil {
			t.Fatal(err)
		}
		_, err := os.Stat(filepath.Join(p.configDir, "app.location"))
		if (err == nil) != (route.Host == "") {
			t.Fatalf("route change left the wrong fallback route: %v", err)
		}
	}
	if err := p.RemoveStaleConfigs(ctx, map[string]bool{}, func(string) bool { return true }); err != nil {
		t.Fatal(err)
	}
	for _, ext := range []string{".caddy", ".location"} {
		if _, err := os.Stat(filepath.Join(p.configDir, "app"+ext)); !os.IsNotExist(err) {
			t.Fatalf("removed app still has a %s route: %v", ext, err)
		}
	}
}

func TestCaddyReleaseCannotSkipReloadBeforeStartup(t *testing.T) {
	p, _ := caddyReloadFixture(t)
	t.Setenv("DR_BUNDLED_PROXY", "caddy")
	ctx := context.Background()
	if err := p.GenerateConfig(ctx, &UpstreamState{Service: "app"}); err != nil {
		t.Fatal(err)
	}
	if err := p.Reload(ctx); err == nil {
		t.Fatal("release succeeded before Caddy started")
	}
	if err := p.Reload(WithProxyBootstrap(ctx)); err != nil {
		t.Fatalf("initial config could not be generated: %v", err)
	}
}
