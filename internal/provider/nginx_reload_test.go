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

func nginxReloadFixture(t *testing.T, images ...string) (*NginxProvider, *atomic.Bool) {
	t.Helper()
	image := "nginx:alpine"
	if len(images) > 0 {
		image = images[0]
	}
	var reject atomic.Bool
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		path := strings.TrimPrefix(r.URL.Path, "/v1.47")
		switch {
		case path == "/containers/json":
			_ = json.NewEncoder(w).Encode([]map[string]string{{"Id": "0123456789abcdef", "Image": image}})
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
	if err := p.Reload(WithProxyBootstrap(context.Background())); err != nil {
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

func TestBundleSwitchKeepsRoutesAndRejectedChangesDoNotPoisonOtherApps(t *testing.T) {
	for _, kind := range []string{"nginx", "angie"} {
		t.Run(kind, func(t *testing.T) {
			p, reject := nginxReloadFixture(t, kind+":alpine")
			t.Setenv("DR_DEFAULT_PROVIDER", kind)
			factory := NewFactory(p.docker, "test")
			ctx := context.Background()
			labels := map[string]string{
				"release.enable": "true", "release.host": "app.example.com",
				"release.nginx.host": "ignored.example.com", "release.path": "/app/",
				"release.nginx.config_dir": p.configDir, "release.nginx.route_dir": p.routeDir,
				"release.angie.config_dir": p.configDir, "release.angie.route_dir": p.routeDir,
			}
			deploy := func(service string) error {
				cfg, err := config.ParseLabels(labels)
				if err != nil {
					return err
				}
				prov, err := factory.Provider(cfg)
				if err != nil {
					return err
				}
				state := &UpstreamState{Service: service, Servers: []Server{{Addr: "127.0.0.1:8080"}}}
				if err := prov.GenerateConfig(ctx, state); err != nil {
					return err
				}
				return prov.Reload(ctx)
			}
			if err := deploy("app"); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(p.routeDir, "app.server")
			before, err := os.ReadFile(path)
			if err != nil || !strings.Contains(string(before), "server_name app.example.com;") || !strings.Contains(string(before), "location /app/") {
				t.Fatalf("shared labels did not route the app: %s, %v", before, err)
			}
			reject.Store(true)
			labels["release.host"] = "changed.example.com"
			if err := deploy("app"); err == nil {
				t.Fatal("rejected config was accepted")
			}
			after, err := os.ReadFile(path)
			if err != nil || string(after) != string(before) {
				t.Fatalf("rejected update lost working route: %s, %v", after, err)
			}
			reject.Store(false)
			if err := deploy("api"); err != nil {
				t.Fatalf("rejected config blocked another app: %v", err)
			}
		})
	}
}

func previewRoute() NginxRoute {
	return NginxRoute{
		Host: "app.example.com", PreviewHost: "next.app.example.com",
		SSLCert: "/certs/app.pem", SSLKey: "/certs/app.key", SSLRedirect: true,
	}
}

func TestNginxPreviewHostAnswersOnlyWhilePreviewIsLive(t *testing.T) {
	for _, kind := range []string{"nginx", "angie"} {
		t.Run(kind, func(t *testing.T) {
			p, _ := nginxReloadFixture(t, kind+":alpine")
			p.proxyName = kind
			p.route = previewRoute()
			ctx := context.Background()
			state := &UpstreamState{
				Service: "app", Servers: []Server{{Addr: "10.0.0.1:80"}},
				Preview: []Server{{Addr: "10.0.0.2:80"}},
			}
			generate := func() (upstream, server string) {
				t.Helper()
				if err := p.GenerateConfig(ctx, state); err != nil {
					t.Fatal(err)
				}
				if err := p.Reload(ctx); err != nil {
					t.Fatal(err)
				}
				u, _ := os.ReadFile(filepath.Join(p.configDir, "app.conf"))
				s, _ := os.ReadFile(filepath.Join(p.routeDir, "app.server"))
				return string(u), string(s)
			}

			upstream, server := generate()
			if !strings.Contains(upstream, "upstream app_preview_upstream {") || !strings.Contains(upstream, "server 10.0.0.2:80;") {
				t.Fatalf("preview upstream missing: %s", upstream)
			}
			if strings.Count(server, "server_name next.app.example.com;") != 2 {
				t.Fatalf("preview host needs an http and an https block: %s", server)
			}
			for _, want := range []string{
				"proxy_pass http://app_preview_upstream/;",
				"ssl_certificate /certs/app.pem;",
				"ssl_certificate_key /certs/app.key;",
				"return 308 https://$host$request_uri;",
				"proxy_pass http://app_upstream/;",
			} {
				if !strings.Contains(server, want) {
					t.Fatalf("server file missing %q: %s", want, server)
				}
			}
			if strings.Index(server, "server_name app.example.com;") > strings.Index(server, "server_name next.app.example.com;") {
				t.Fatalf("preview blocks must follow the main ones: %s", server)
			}

			// The switch: no preview servers. The route is gone, the upstream stays.
			state.Preview = nil
			upstream, server = generate()
			if strings.Contains(server, "next.app.example.com") || strings.Contains(server, "app_preview_upstream") {
				t.Fatalf("preview route outlived the preview: %s", server)
			}
			if !strings.Contains(upstream, "upstream app_preview_upstream {") || !strings.Contains(upstream, "server 127.0.0.1:1 down;") {
				t.Fatalf("hand-written proxy rules need a loadable preview upstream: %s", upstream)
			}
		})
	}
}

func TestNginxPreviewHostWithPathOnlyRouteKeepsLocation(t *testing.T) {
	p, _ := nginxReloadFixture(t)
	p.route = NginxRoute{Path: "/app/", PreviewHost: "next.example.com"}
	state := &UpstreamState{
		Service: "app", Servers: []Server{{Addr: "10.0.0.1:80"}},
		Preview: []Server{{Addr: "10.0.0.2:80"}},
	}
	if err := p.GenerateConfig(context.Background(), state); err != nil {
		t.Fatal(err)
	}
	if err := p.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	location, _ := os.ReadFile(filepath.Join(p.routeDir, "app.location"))
	server, _ := os.ReadFile(filepath.Join(p.routeDir, "app.server"))
	if !strings.Contains(string(location), "location /app/ {") || strings.Contains(string(location), "preview") {
		t.Fatalf("location file changed: %s", location)
	}
	if !strings.Contains(string(server), "server_name next.example.com;") ||
		!strings.Contains(string(server), "location / {") ||
		!strings.Contains(string(server), "proxy_pass http://app_preview_upstream/;") ||
		strings.Contains(string(server), "/app/") {
		t.Fatalf("server file should hold only the preview block: %s", server)
	}
}

func TestNginxWithoutPreviewHostWritesTheSameFiles(t *testing.T) {
	state := &UpstreamState{
		Service: "app", Servers: []Server{{Addr: "10.0.0.1:80"}},
		Preview: []Server{{Addr: "10.0.0.2:80"}},
	}
	for _, kind := range []string{"nginx", "angie"} {
		p, _ := nginxReloadFixture(t)
		p.proxyName = kind
		if err := p.GenerateConfig(context.Background(), state); err != nil {
			t.Fatal(err)
		}
		render := renderUpstream
		if kind == "angie" {
			render = renderAngieUpstream
		}
		want := map[string]string{
			filepath.Join(p.configDir, "app.conf"):    render(state),
			filepath.Join(p.routeDir, "app.server"):   renderNginxServer(state, p.route, kind),
			filepath.Join(p.routeDir, "app.location"): "",
		}
		for path, content := range want {
			if string(p.pending[path]) != content {
				t.Fatalf("%s: %s changed without a preview host: %s", kind, path, p.pending[path])
			}
		}
		if strings.Contains(string(p.pending[filepath.Join(p.configDir, "app.conf")]), "preview") {
			t.Fatalf("%s: upstream file mentions a preview nobody asked for", kind)
		}
	}
}

func TestNginxPreviewHostCannotShadowAnotherServicesHost(t *testing.T) {
	p, _ := nginxReloadFixture(t)
	ctx := context.Background()
	other := NewNginx(p.configDir, p.routeDir, NginxRoute{Host: "next.app.example.com"}, p.docker, "", "test")
	if err := other.GenerateConfig(ctx, &UpstreamState{Service: "docs", Servers: []Server{{Addr: "10.0.0.9:80"}}}); err != nil {
		t.Fatal(err)
	}
	if err := other.Reload(ctx); err != nil {
		t.Fatal(err)
	}

	p.route = previewRoute()
	state := &UpstreamState{
		Service: "app", Servers: []Server{{Addr: "10.0.0.1:80"}},
		Preview: []Server{{Addr: "10.0.0.2:80"}},
	}
	if err := p.GenerateConfig(ctx, state); err != nil {
		t.Fatal(err)
	}
	err := p.Reload(ctx)
	if err == nil || !strings.Contains(err.Error(), "next.app.example.com") {
		t.Fatalf("preview host taken by another service was accepted: %v", err)
	}
	if _, err := os.Stat(filepath.Join(p.routeDir, "app.server")); !os.IsNotExist(err) {
		t.Fatal("rejected preview left a route file")
	}
}
