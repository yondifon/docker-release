package provider

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHAProxyRenderBackendCookieAffinity(t *testing.T) {
	state := &UpstreamState{
		Service:  "app",
		Affinity: "cookie",
		Servers: []Server{
			{Addr: "172.18.0.5:80"},
			{Addr: "172.18.0.6:80"},
		},
	}

	got := renderHAProxyBackend(state)

	if !strings.Contains(got, "cookie _srr_a172cedcae insert indirect nocache") {
		t.Error("missing cookie affinity directive")
	}
	if !strings.Contains(got, "server s1 172.18.0.5:80 cookie s1") {
		t.Error("missing per-server cookie s1")
	}
	if !strings.Contains(got, "server s2 172.18.0.6:80 cookie s2") {
		t.Error("missing per-server cookie s2")
	}
}

func TestHAProxyRenderBackendIPAffinity(t *testing.T) {
	state := &UpstreamState{
		Service:  "app",
		Affinity: "ip",
		Servers:  []Server{{Addr: "172.18.0.5:80"}},
	}

	got := renderHAProxyBackend(state)

	if !strings.Contains(got, "balance source") {
		t.Error("missing source balance for ip affinity")
	}
	if strings.Contains(got, "roundrobin") {
		t.Error("roundrobin should not appear with ip affinity")
	}
}

func TestHAProxyRenderBackendWeighted(t *testing.T) {
	state := &UpstreamState{
		Service: "app",
		Servers: []Server{
			{Addr: "172.18.0.5:80", Weight: 75},
			{Addr: "172.18.0.8:80", Weight: 25},
		},
	}

	got := renderHAProxyBackend(state)

	if !strings.Contains(got, "server s1 172.18.0.5:80 weight 75") {
		t.Error("missing weighted server 1")
	}
	if !strings.Contains(got, "server s2 172.18.0.8:80 weight 25") {
		t.Error("missing weighted canary server")
	}
}

func TestHAProxyRenderBackendDrain(t *testing.T) {
	state := &UpstreamState{
		Service: "app",
		Servers: []Server{
			{Addr: "172.18.0.5:80"},
			{Addr: "172.18.0.6:80", Down: true},
		},
	}

	got := renderHAProxyBackend(state)

	if !strings.Contains(got, "server s2 172.18.0.6:80 disabled") {
		t.Error("down server should be rendered with disabled keyword")
	}
	if strings.Contains(got, "server s1 172.18.0.5:80 disabled") {
		t.Error("active server should not be disabled")
	}
}

func TestHAProxyRenderBackendCustomName(t *testing.T) {
	state := &UpstreamState{
		Service:      "app",
		UpstreamName: "myapp",
		Servers:      []Server{{Addr: "172.18.0.5:80"}},
	}

	got := renderHAProxyBackend(state)

	if !strings.Contains(got, "backend myapp_be") {
		t.Error("should use UpstreamName override in backend name")
	}
	if strings.Contains(got, "backend app_be") {
		t.Error("should not use service name when UpstreamName is set")
	}
}

func TestHAProxyRenderBackendBackup(t *testing.T) {
	state := &UpstreamState{
		Service: "app",
		Servers: []Server{
			{Addr: "172.18.0.10:80"},
			{Addr: "172.18.0.2:80", Backup: true},
		},
	}

	got := renderHAProxyBackend(state)

	if !strings.Contains(got, "server s1 172.18.0.10:80") {
		t.Error("missing primary server")
	}
	if strings.Contains(got, "server s1 172.18.0.10:80 backup") {
		t.Error("primary server should not have backup flag")
	}
	if !strings.Contains(got, "server s2 172.18.0.2:80 backup") {
		t.Error("backup server should have backup keyword")
	}
}

func TestHAProxyRenderBackendBackupWithIPAffinity(t *testing.T) {
	state := &UpstreamState{
		Service:  "app",
		Affinity: "ip",
		Servers: []Server{
			{Addr: "172.18.0.10:80"},
			{Addr: "172.18.0.2:80", Backup: true},
		},
	}

	got := renderHAProxyBackend(state)

	if !strings.Contains(got, "server s2 172.18.0.2:80 backup") {
		t.Error("haproxy backup should work regardless of affinity")
	}
}

func TestHAProxyRenderBackendBackupNoCookieDirective(t *testing.T) {
	state := &UpstreamState{
		Service:  "app",
		Affinity: "cookie",
		Servers: []Server{
			{Addr: "172.18.0.10:80"},
			{Addr: "172.18.0.2:80", Backup: true},
		},
	}

	got := renderHAProxyBackend(state)

	if !strings.Contains(got, "server s1 172.18.0.10:80 cookie s1") {
		t.Error("primary server should have cookie directive")
	}
	if strings.Contains(got, "server s2 172.18.0.2:80 cookie") {
		t.Error("backup server should not have cookie directive")
	}
	if !strings.Contains(got, "server s2 172.18.0.2:80 backup") {
		t.Error("backup server should have backup keyword")
	}
}

func bundledHAProxy(t *testing.T, dir string, route HAProxyRoute) *HAProxyProvider {
	t.Helper()
	return NewHAProxyRoutes(dir, route, nil, "app", "proj")
}

// settle writes what the provider queued, as a reload would, and returns the
// rendered frontend and the backend file.
func settle(t *testing.T, p *HAProxyProvider) (frontend, backend string) {
	t.Helper()
	for path, data := range p.pending {
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	routes, err := p.bundledRoutes(nil)
	if err != nil {
		t.Fatal(err)
	}
	frontend, err = renderHAProxyFrontend(routes)
	if err != nil {
		t.Fatal(err)
	}
	return frontend, string(p.pending[filepath.Join(p.configDir, "app.cfg")])
}

func writeCert(t *testing.T) (cert, key string) {
	t.Helper()
	dir := t.TempDir()
	cert, key = filepath.Join(dir, "c.pem"), filepath.Join(dir, "k.pem")
	for _, f := range []string{cert, key} {
		if err := os.WriteFile(f, []byte("pem"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return cert, key
}

func TestHAProxyBundledPreviewFollowsRelease(t *testing.T) {
	cert, key := writeCert(t)
	p := bundledHAProxy(t, t.TempDir(), HAProxyRoute{
		Host: "app.example.com", PreviewHost: "next.example.com", Path: "/api",
		TLSCert: cert, TLSKey: key, TLSRedirect: true, Affinity: "ip",
	})
	live := &UpstreamState{
		Service: "app",
		Servers: []Server{{Addr: "10.0.0.1:80"}},
		Preview: []Server{{Addr: "10.0.0.2:80"}},
	}

	if err := p.GenerateConfig(context.Background(), live); err != nil {
		t.Fatal(err)
	}
	frontend, backend := settle(t, p)

	if !strings.Contains(frontend, "acl host_0 req.hdr(host),host_only -i next.example.com") &&
		!strings.Contains(frontend, "acl host_1 req.hdr(host),host_only -i next.example.com") {
		t.Errorf("preview host is not matched:\n%s", frontend)
	}
	if !strings.Contains(frontend, "str(app_preview_be)") || !strings.Contains(frontend, "str(app_be)") {
		t.Errorf("frontend should route to both backends:\n%s", frontend)
	}
	if n := strings.Count(frontend, "load crt"); n != 1 {
		t.Errorf("certificate loaded %d times, want 1", n)
	}
	if n := strings.Count(frontend, "code 308"); n != 2 {
		t.Errorf("want a TLS redirect for both hosts, got %d:\n%s", n, frontend)
	}
	if n := strings.Count(backend, "backend "); n != 2 || !strings.Contains(backend, "backend app_preview_be") {
		t.Errorf("want main and preview backends:\n%s", backend)
	}
	preview := backend[strings.Index(backend, "backend app_preview_be"):]
	if strings.Contains(preview, "10.0.0.1") || !strings.Contains(preview, "10.0.0.2:80") {
		t.Errorf("preview backend should hold only the preview servers:\n%s", preview)
	}
	if strings.Contains(preview, "replace-path") {
		t.Errorf("preview serves `/`, so it must not rewrite the path:\n%s", preview)
	}
	if !strings.Contains(backend, "replace-path") {
		t.Errorf("main backend lost its path rewrite:\n%s", backend)
	}

	ended := &UpstreamState{Service: "app", Servers: []Server{{Addr: "10.0.0.2:80"}}}
	if err := p.GenerateConfig(context.Background(), ended); err != nil {
		t.Fatal(err)
	}
	frontend, backend = settle(t, p)

	if strings.Contains(frontend, "next.example.com") || strings.Contains(frontend, "preview") {
		t.Errorf("preview route should be gone:\n%s", frontend)
	}
	if strings.Contains(backend, "preview") || strings.Count(backend, "backend ") != 1 {
		t.Errorf("preview backend should be gone:\n%s", backend)
	}
}

func TestHAProxyBundledPreviewHostCannotTakeAnotherHost(t *testing.T) {
	dir := t.TempDir()
	other := bundledHAProxy(t, dir, HAProxyRoute{Host: "next.example.com"})
	other.serviceName = "other"
	if err := other.GenerateConfig(context.Background(), &UpstreamState{Service: "other", Servers: []Server{{Addr: "10.0.0.9:80"}}}); err != nil {
		t.Fatal(err)
	}
	settle(t, other)

	p := bundledHAProxy(t, dir, HAProxyRoute{Host: "app.example.com", PreviewHost: "Next.Example.com"})
	if err := p.GenerateConfig(context.Background(), &UpstreamState{
		Service: "app",
		Servers: []Server{{Addr: "10.0.0.1:80"}},
		Preview: []Server{{Addr: "10.0.0.2:80"}},
	}); err != nil {
		t.Fatal(err)
	}
	routes, err := p.bundledRoutes(p.pending)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := renderHAProxyFrontend(routes); err == nil || !strings.Contains(err.Error(), "next.example.com") {
		t.Errorf("want a duplicate host error, got %v", err)
	}
}

func TestHAProxyBundledWithoutPreviewHostIsUnchanged(t *testing.T) {
	p := bundledHAProxy(t, t.TempDir(), HAProxyRoute{Host: "app.example.com", Affinity: "ip"})
	state := &UpstreamState{
		Service: "app",
		Servers: []Server{{Addr: "10.0.0.1:80"}},
		Preview: []Server{{Addr: "10.0.0.2:80"}},
	}
	if err := p.GenerateConfig(context.Background(), state); err != nil {
		t.Fatal(err)
	}
	frontend, backend := settle(t, p)

	if strings.Contains(frontend, "preview") || strings.Contains(backend, "preview") {
		t.Errorf("no preview host is set, so nothing about a preview should be written:\n%s\n%s", frontend, backend)
	}
	route := string(p.pending[filepath.Join(p.configDir, "app.route")])
	if strings.Contains(route, "Preview") {
		t.Errorf("route file grew preview fields: %s", route)
	}
}

func TestHAProxyPlainBackendAlwaysHasPreviewBackend(t *testing.T) {
	dir := t.TempDir()
	p := NewHAProxy(dir, nil, "haproxy", "proj")
	p.route.PreviewHost = "next.example.com"
	read := func() string {
		data, err := os.ReadFile(filepath.Join(dir, "app.cfg"))
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}

	if err := p.GenerateConfig(context.Background(), &UpstreamState{Service: "app", Servers: []Server{{Addr: "10.0.0.1:80"}}}); err != nil {
		t.Fatal(err)
	}
	got := read()
	if !strings.Contains(got, "backend app_preview_be\n") || strings.Contains(got[strings.Index(got, "backend app_preview_be"):], "server ") {
		t.Errorf("want an empty preview backend:\n%s", got)
	}

	if err := p.GenerateConfig(context.Background(), &UpstreamState{
		Service: "app",
		Servers: []Server{{Addr: "10.0.0.1:80"}},
		Preview: []Server{{Addr: "10.0.0.2:80"}},
	}); err != nil {
		t.Fatal(err)
	}
	got = read()
	preview := got[strings.Index(got, "backend app_preview_be"):]
	if !strings.Contains(preview, "server s1 10.0.0.2:80") || strings.Contains(preview, "10.0.0.1") {
		t.Errorf("preview backend should hold only the preview servers:\n%s", got)
	}

	plain := NewHAProxy(dir, nil, "haproxy", "proj")
	if err := plain.GenerateConfig(context.Background(), &UpstreamState{Service: "app", Servers: []Server{{Addr: "10.0.0.1:80"}}}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(read(), "preview") {
		t.Errorf("no preview host, so no preview backend:\n%s", read())
	}
}

func TestHAProxyRuntimeUpdateSeesBothBackends(t *testing.T) {
	p := bundledHAProxy(t, t.TempDir(), HAProxyRoute{Host: "app.example.com", PreviewHost: "next.example.com"})
	if err := p.GenerateConfig(context.Background(), &UpstreamState{
		Service: "app",
		Servers: []Server{{Addr: "10.0.0.1:80"}},
		Preview: []Server{{Addr: "10.0.0.2:80"}, {Addr: "10.0.0.3:80"}},
	}); err != nil {
		t.Fatal(err)
	}
	sections := splitHAProxyBackends(p.pending[filepath.Join(p.configDir, "app.cfg")])

	var seen []string
	for _, section := range sections {
		backend, servers := haproxyBackendServers(section)
		if backend != "" {
			seen = append(seen, fmt.Sprintf("%s:%d", backend, len(servers)))
		}
	}
	if strings.Join(seen, " ") != "app_be:1 app_preview_be:2" {
		t.Errorf("got %v", seen)
	}
}
