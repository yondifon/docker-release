package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/malico/docker-release/internal/config"
	"github.com/malico/docker-release/internal/docker"
)

func nginxReloadFixture(t *testing.T) (*NginxProvider, *atomic.Bool) {
	t.Helper()
	var reject atomic.Bool
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		path := strings.TrimPrefix(r.URL.Path, "/v1.47")
		switch {
		case path == "/containers/json":
			_, _ = w.Write([]byte(`[{"Id":"0123456789abcdef","Image":"nginx:alpine"}]`))
		case strings.HasSuffix(path, "/exec"):
			var body struct{ Cmd []string }
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			id := "reload"
			for _, arg := range body.Cmd {
				if arg == "-t" {
					id = "validate"
				}
			}
			_, _ = w.Write([]byte(`{"Id":"` + id + `"}`))
		case strings.HasSuffix(path, "/start"):
		case strings.HasSuffix(path, "/json"):
			code := 0
			if strings.Contains(path, "/validate/") && reject.Load() {
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
	return NewNginx(t.TempDir(), t.TempDir(), NginxRoute{Host: "app.example.com"}, client, "", "test"), &reject
}

func TestNginxRejectedConfigPreservesWorkingRoutes(t *testing.T) {
	p, reject := nginxReloadFixture(t)
	ctx := context.Background()
	state := &UpstreamState{Service: "app", Servers: []Server{{Addr: "127.0.0.1:8080"}}}
	if err := p.GenerateConfig(ctx, state); err != nil {
		t.Fatal(err)
	}
	if err := p.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	upstreamPath := filepath.Join(p.configDir, "app.conf")
	routePath := filepath.Join(p.routeDir, "app.server")
	upstream, _ := os.ReadFile(upstreamPath)
	route, _ := os.ReadFile(routePath)

	reject.Store(true)
	p.route = NginxRoute{Path: "/app/"}
	state.Servers[0].Addr = "127.0.0.1:9090"
	if err := p.GenerateConfig(ctx, state); err != nil {
		t.Fatal(err)
	}
	if err := p.Reload(ctx); err == nil {
		t.Fatal("deployment accepted a config nginx rejected")
	}
	for path, want := range map[string]string{upstreamPath: string(upstream), routePath: string(route)} {
		got, err := os.ReadFile(path)
		if err != nil || string(got) != want {
			t.Fatalf("working config not restored at %s: %s, %v", path, got, err)
		}
	}
	if _, err := os.Stat(filepath.Join(p.routeDir, "app.location")); !os.IsNotExist(err) {
		t.Fatal("rejected fallback route still exists")
	}

	reject.Store(false)
	other := NewNginx(p.configDir, p.routeDir, NginxRoute{Host: "api.example.com"}, p.docker, "", "test")
	if err := other.GenerateConfig(ctx, &UpstreamState{Service: "api", Servers: state.Servers}); err != nil {
		t.Fatal(err)
	}
	if err := other.Reload(ctx); err != nil {
		t.Fatalf("rejected app config poisoned another service's update: %v", err)
	}
}

func TestNginxRouteChangesRemoveOnlyGeneratedRoutes(t *testing.T) {
	p, _ := nginxReloadFixture(t)
	ctx := context.Background()
	state := &UpstreamState{Service: "app", Servers: []Server{{Addr: "127.0.0.1:8080"}}}
	for _, route := range []NginxRoute{{Path: "/app/"}, {Host: "app.example.com"}, {}} {
		p.route = route
		if err := p.GenerateConfig(ctx, state); err != nil {
			t.Fatal(err)
		}
		if err := p.Reload(ctx); err != nil {
			t.Fatal(err)
		}
		for ext, want := range map[string]bool{".server": route.Host != "", ".location": route.Path != ""} {
			_, err := os.Stat(filepath.Join(p.routeDir, "app"+ext))
			if (err == nil) != want {
				t.Fatalf("route change left wrong %s file: %v", ext, err)
			}
		}
	}
	customPath := filepath.Join(p.routeDir, "app.server")
	custom := "# User config\nserver { listen 8080; }\n"
	if err := os.WriteFile(customPath, []byte(custom), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := p.GenerateConfig(ctx, state); err != nil {
		t.Fatal(err)
	}
	if err := p.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(customPath)
	if err != nil || string(got) != custom {
		t.Fatalf("route cleanup discarded user config: %s, %v", got, err)
	}
}

func TestNginxConcurrentServiceUpdatesKeepCompleteConfigs(t *testing.T) {
	p, _ := nginxReloadFixture(t)
	ctx := context.Background()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			service := fmt.Sprintf("app-%d", i)
			other := NewNginx(p.configDir, p.routeDir, NginxRoute{Host: service + ".example.com"}, p.docker, "", "test")
			if err := other.GenerateConfig(ctx, &UpstreamState{Service: service, Servers: []Server{{Addr: "127.0.0.1:8080"}}}); err != nil {
				t.Error(err)
				return
			}
			if err := other.Reload(ctx); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	for i := 0; i < 8; i++ {
		service := fmt.Sprintf("app-%d", i)
		for dir, ext := range map[string]string{p.configDir: ".conf", p.routeDir: ".server"} {
			data, err := os.ReadFile(filepath.Join(dir, service+ext))
			if err != nil || !strings.Contains(string(data), service+"_upstream") {
				t.Fatalf("concurrent update lost %s%s: %s, %v", service, ext, data, err)
			}
		}
	}
}

func TestNginxStoppedServiceRemovesUpstreamAndRoutesTogether(t *testing.T) {
	p, _ := nginxReloadFixture(t)
	ctx := context.Background()
	for _, service := range []string{"app", "api"} {
		p.route.Host = service + ".example.com"
		if err := p.GenerateConfig(ctx, &UpstreamState{Service: service, Servers: []Server{{Addr: "127.0.0.1:8080"}}}); err != nil {
			t.Fatal(err)
		}
		if err := p.Reload(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if err := p.RemoveStaleConfigs(ctx, map[string]bool{"api": true}, func(string) bool { return true }); err != nil {
		t.Fatal(err)
	}
	for dir, ext := range map[string]string{p.configDir: ".conf", p.routeDir: ".server"} {
		if _, err := os.Stat(filepath.Join(dir, "app"+ext)); !os.IsNotExist(err) {
			t.Fatalf("stopped service still has %s: %v", ext, err)
		}
		if _, err := os.Stat(filepath.Join(dir, "api"+ext)); err != nil {
			t.Fatalf("cleanup removed a live service's %s: %v", ext, err)
		}
	}
}

func TestNginxDuplicateHostDoesNotSilentlyHideAnotherService(t *testing.T) {
	p, _ := nginxReloadFixture(t)
	ctx := context.Background()
	state := &UpstreamState{Service: "app", Servers: []Server{{Addr: "127.0.0.1:8080"}}}
	if err := p.GenerateConfig(ctx, state); err != nil {
		t.Fatal(err)
	}
	if err := p.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	p.route.Host = "APP.EXAMPLE.COM"
	state.Service = "api"
	if err := p.GenerateConfig(ctx, state); err != nil {
		t.Fatal(err)
	}
	if err := p.Reload(ctx); err == nil {
		t.Fatal("duplicate hostname silently hid another service")
	}
	if _, err := os.Stat(filepath.Join(p.configDir, "api.conf")); !os.IsNotExist(err) {
		t.Fatal("rejected duplicate service left a config file")
	}
}

func TestBundledStartupDoesNotAcceptDeploymentBeforeProxyIsReady(t *testing.T) {
	if _, err := os.Stat("/run/docker-release/nginx.started"); !os.IsNotExist(err) {
		t.Skip("requires no running bundled nginx")
	}
	p, _ := nginxReloadFixture(t)
	t.Setenv("DR_BUNDLED_PROXY", "nginx")
	state := &UpstreamState{Service: "app", Servers: []Server{{Addr: "127.0.0.1:8080"}}}
	if err := p.GenerateConfig(context.Background(), state); err != nil {
		t.Fatal(err)
	}
	if err := p.Reload(context.Background()); err == nil {
		t.Fatal("deployment reported a successful reload before nginx started")
	}
	if err := p.Reload(WithNginxBootstrap(context.Background())); err != nil {
		t.Fatalf("initial config could not be generated before nginx started: %v", err)
	}
}

func TestNginxDefaultProviderRoutesEachServiceSeparately(t *testing.T) {
	p, _ := nginxReloadFixture(t)
	t.Setenv("DR_DEFAULT_PROVIDER", "nginx")
	factory := NewFactory(p.docker, "test")
	ctx := context.Background()
	for _, service := range []string{"app", "api"} {
		cfg, err := config.ParseLabels(map[string]string{
			"release.enable": "true", "release.nginx.host": service + ".example.com",
			"release.nginx.config_dir": p.configDir, "release.nginx.route_dir": p.routeDir,
		})
		if err != nil {
			t.Fatal(err)
		}
		prov, err := factory.Provider(cfg)
		if err != nil {
			t.Fatal(err)
		}
		if err := prov.GenerateConfig(ctx, &UpstreamState{Service: service, Servers: []Server{{Addr: "127.0.0.1:8080"}}}); err != nil {
			t.Fatal(err)
		}
		if err := prov.Reload(ctx); err != nil {
			t.Fatal(err)
		}
	}
	for _, service := range []string{"app", "api"} {
		data, err := os.ReadFile(filepath.Join(p.routeDir, service+".server"))
		if err != nil || !strings.Contains(string(data), "server_name "+service+".example.com;") || !strings.Contains(string(data), "http://"+service+"_upstream/") {
			t.Fatalf("labels did not produce a separate route for %s: %s, %v", service, data, err)
		}
	}
}
