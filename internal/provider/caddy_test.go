package provider

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func bundledCaddyConf(t *testing.T, dir string, route CaddyRoute, state *UpstreamState) string {
	t.Helper()
	p := NewCaddyRoutes(dir, route, nil, "", "test")
	if err := p.GenerateConfig(context.Background(), state); err != nil {
		t.Fatal(err)
	}
	return string(p.pending[filepath.Join(p.configDir, state.Service+".caddy")])
}

func TestCaddyBundledPreviewSiteLivesOnlyWhilePreviewServersExist(t *testing.T) {
	dir := t.TempDir()
	cert, key := filepath.Join(dir, "c.pem"), filepath.Join(dir, "k.pem")
	for _, f := range []string{cert, key} {
		if err := os.WriteFile(f, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	route := CaddyRoute{
		Host: "app.example.com", PreviewHost: "next.app.example.com",
		TLSCert: cert, TLSKey: key, TLSRedirect: true,
	}
	state := &UpstreamState{
		Service: "app",
		Servers: []Server{{Addr: "10.0.0.1:80"}},
		Preview: []Server{{Addr: "10.0.0.9:80"}, {Addr: "10.0.0.10:80"}},
	}

	live := bundledCaddyConf(t, dir, route, state)
	_, block, found := strings.Cut(live, "https://next.app.example.com {")
	if !found {
		t.Fatalf("no preview site block:\n%s", live)
	}
	block, _, _ = strings.Cut(block, "\n}\n")
	for _, want := range []string{
		"tls " + cert + " " + key,
		"output stdout",
		"log_append upstream {http.reverse_proxy.upstream.hostport}",
		"reverse_proxy 10.0.0.9:80 10.0.0.10:80\n",
	} {
		if !strings.Contains(block, want) {
			t.Errorf("preview block missing %q:\n%s", want, block)
		}
	}
	if strings.Contains(block, "10.0.0.1:80") || strings.Contains(block, "import app_upstream") {
		t.Errorf("preview block reaches the live servers:\n%s", block)
	}
	if !strings.Contains(live, "http://next.app.example.com {\n    redir https://{host}{uri} 308") {
		t.Errorf("preview host has no http to https redirect:\n%s", live)
	}
	if !strings.Contains(live, "https://app.example.com {") || !strings.Contains(live, "import app_upstream") {
		t.Errorf("main site changed:\n%s", live)
	}

	state.Preview = nil
	ended := bundledCaddyConf(t, dir, route, state)
	if strings.Contains(ended, "next.app.example.com") || strings.Contains(ended, "10.0.0.9") {
		t.Errorf("preview site remains after the preview ended:\n%s", ended)
	}
	if want := strings.Replace(live, live[strings.Index(live, "\nhttp://next.app.example.com"):strings.Index(live, "# TLS")], "", 1); ended != want {
		t.Errorf("main config differs once preview is gone:\n%s\n---\n%s", ended, want)
	}
}

func TestCaddyBundledNoPreviewHostLeavesConfigUnchanged(t *testing.T) {
	route := CaddyRoute{Host: "app.example.com", TLSRedirect: true}
	state := &UpstreamState{
		Service: "app",
		Servers: []Server{{Addr: "10.0.0.1:80"}},
	}
	dir := t.TempDir()
	before := bundledCaddyConf(t, dir, route, state)
	state.Preview = []Server{{Addr: "10.0.0.9:80"}}
	if after := bundledCaddyConf(t, dir, route, state); after != before {
		t.Errorf("preview servers changed config without a preview host:\n%s", after)
	}
}

func TestCaddyPlainPreviewSnippetIsAlwaysImportable(t *testing.T) {
	state := &UpstreamState{
		Service: "app",
		Servers: []Server{{Addr: "10.0.0.1:80"}},
	}
	write := func(route CaddyRoute) string {
		p := NewCaddy(t.TempDir(), nil, "", "test")
		p.route = route
		if err := p.GenerateConfig(context.Background(), state); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(filepath.Join(p.configDir, "app.caddy"))
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}

	plain := write(CaddyRoute{})
	if strings.Contains(plain, "preview") {
		t.Errorf("snippet written without a preview host:\n%s", plain)
	}

	route := CaddyRoute{PreviewHost: "next.example.com"}
	idle := write(route)
	if !strings.HasPrefix(idle, plain) {
		t.Errorf("main snippet changed:\n%s", idle)
	}
	if !strings.Contains(idle, "(app_preview_upstream) {\n    respond 404\n}") {
		t.Errorf("idle preview snippet should answer 404:\n%s", idle)
	}

	state.Preview = []Server{{Addr: "10.0.0.9:80"}}
	live := write(route)
	_, snippet, _ := strings.Cut(live, "(app_preview_upstream) {")
	if !strings.Contains(snippet, "reverse_proxy 10.0.0.9:80") || strings.Contains(snippet, "10.0.0.1:80") || strings.Contains(snippet, "respond 404") {
		t.Errorf("live preview snippet is wrong:\n%s", live)
	}
	if !strings.Contains(live, "(app_upstream) {\n    reverse_proxy 10.0.0.1:80") {
		t.Errorf("main snippet changed:\n%s", live)
	}
}

func TestCaddyRenderUpstreamCookieAffinity(t *testing.T) {
	state := &UpstreamState{
		Service:  "app",
		Affinity: "cookie",
		Servers:  []Server{{Addr: "172.18.0.5:80"}},
	}

	got := renderCaddyUpstream(state)

	if !strings.Contains(got, "lb_policy cookie _srr_a172cedcae") {
		t.Error("missing cookie affinity policy")
	}
	if strings.Contains(got, "round_robin") {
		t.Error("round_robin should not appear with cookie affinity")
	}
}

func TestCaddyRenderUpstreamWeightedNoCookieAffinity(t *testing.T) {
	state := &UpstreamState{
		Service: "app",
		Servers: []Server{
			{Addr: "172.18.0.5:80", Weight: 75},
			{Addr: "172.18.0.6:80", Weight: 75},
			{Addr: "172.18.0.8:80", Weight: 25},
		},
	}

	got := renderCaddyUpstream(state)

	if !strings.Contains(got, "lb_policy weighted_round_robin") {
		t.Error("missing weighted_round_robin policy")
	}
	if !strings.Contains(got, "172.18.0.5:80") || !strings.Contains(got, "172.18.0.8:80") {
		t.Error("missing servers in weighted upstream")
	}
}

func TestCaddyRenderUpstreamCookieAffinityWithWeights(t *testing.T) {
	state := &UpstreamState{
		Service:  "app",
		Affinity: "cookie",
		Servers: []Server{
			{Addr: "172.18.0.5:80", Weight: 75},
			{Addr: "172.18.0.8:80", Weight: 25},
		},
	}

	got := renderCaddyUpstream(state)

	if !strings.Contains(got, "lb_policy cookie") {
		t.Error("cookie affinity must win over weights")
	}
	if strings.Contains(got, "weighted_round_robin") {
		t.Error("weighted_round_robin must not appear when affinity is cookie")
	}
}

func TestCaddyRenderUpstreamDrain(t *testing.T) {
	state := &UpstreamState{
		Service: "app",
		Servers: []Server{
			{Addr: "172.18.0.5:80"},
			{Addr: "172.18.0.6:80", Down: true},
		},
	}

	got := renderCaddyUpstream(state)

	if strings.Contains(got, "172.18.0.6:80") {
		t.Error("down server should be omitted from Caddy upstream")
	}
	if !strings.Contains(got, "172.18.0.5:80") {
		t.Error("active server must be present")
	}
}

func TestCaddyRenderUpstreamCustomName(t *testing.T) {
	state := &UpstreamState{
		Service:      "app",
		UpstreamName: "myapp",
		Servers:      []Server{{Addr: "172.18.0.5:80"}},
	}

	got := renderCaddyUpstream(state)

	if !strings.Contains(got, "(myapp_upstream) {") {
		t.Error("should use UpstreamName override in snippet name")
	}
	if strings.Contains(got, "(app_upstream)") {
		t.Error("should not use service name when UpstreamName is set")
	}
}

func TestCaddyBackupServersDropped(t *testing.T) {
	state := &UpstreamState{
		Service: "app",
		Servers: []Server{
			{Addr: "172.18.0.10:80"},
			{Addr: "172.18.0.2:80", Backup: true},
			{Addr: "172.18.0.3:80", Backup: true},
		},
	}

	got := renderCaddyUpstream(state)

	if !strings.Contains(got, "172.18.0.10:80") {
		t.Error("missing primary server")
	}
	if strings.Contains(got, "172.18.0.2:80") {
		t.Error("backup server should be silently dropped for caddy")
	}
	if strings.Contains(got, "172.18.0.3:80") {
		t.Error("backup server should be silently dropped for caddy")
	}
}
