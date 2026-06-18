package provider

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRenderUpstreamBasic(t *testing.T) {
	state := &UpstreamState{
		Service: "app",
		Servers: []Server{
			{Addr: "172.18.0.5:80"},
			{Addr: "172.18.0.6:80"},
		},
	}

	got := renderUpstream(state)

	if !strings.Contains(got, "upstream app_upstream {") {
		t.Error("missing upstream block")
	}
	if !strings.Contains(got, "server 172.18.0.5:80;") {
		t.Error("missing server 1")
	}
	if !strings.Contains(got, "server 172.18.0.6:80;") {
		t.Error("missing server 2")
	}
	if strings.Contains(got, "ip_hash") {
		t.Error("ip_hash should not be present without affinity")
	}
	if !strings.Contains(got, "least_conn;") {
		t.Error("least_conn should be present by default")
	}
}

func TestRenderUpstreamAffinityNoLeastConn(t *testing.T) {
	state := &UpstreamState{
		Service:  "app",
		Affinity: "ip",
		Servers: []Server{
			{Addr: "172.18.0.5:80"},
		},
	}

	got := renderUpstream(state)

	if !strings.Contains(got, "ip_hash;") {
		t.Error("missing ip_hash")
	}
	if strings.Contains(got, "least_conn") {
		t.Error("least_conn should not be present when affinity is set")
	}
}

func TestRenderUpstreamWithWeights(t *testing.T) {
	state := &UpstreamState{
		Service:  "app",
		Affinity: "ip",
		Servers: []Server{
			{Addr: "172.18.0.5:80", Weight: 90},
			{Addr: "172.18.0.6:80", Weight: 90},
			{Addr: "172.18.0.8:80", Weight: 10},
		},
	}

	got := renderUpstream(state)

	if !strings.Contains(got, "ip_hash;") {
		t.Error("missing ip_hash")
	}
	if !strings.Contains(got, "server 172.18.0.5:80 weight=90;") {
		t.Error("missing weighted server 1")
	}
	if !strings.Contains(got, "server 172.18.0.8:80 weight=10;") {
		t.Error("missing canary server")
	}
}

func TestRenderUpstreamWithDrain(t *testing.T) {
	state := &UpstreamState{
		Service: "app",
		Servers: []Server{
			{Addr: "172.18.0.5:80"},
			{Addr: "172.18.0.6:80", Down: true},
		},
	}

	got := renderUpstream(state)

	if !strings.Contains(got, "server 172.18.0.6:80 down;") {
		t.Error("missing down server")
	}
	if strings.Contains(got, "server 172.18.0.5:80 down") {
		t.Error("first server should not be down")
	}
}

func TestRenderUpstreamKeepalive(t *testing.T) {
	state := &UpstreamState{
		Service:   "app",
		Keepalive: 5,
		Servers: []Server{
			{Addr: "172.18.0.5:80"},
		},
	}

	got := renderUpstream(state)

	if !strings.Contains(got, "keepalive 5;") {
		t.Error("missing keepalive directive")
	}
}

func TestRenderUpstreamCookieAffinityFallsBackToIpHash(t *testing.T) {
	state := &UpstreamState{
		Service:  "app",
		Affinity: "cookie",
		Servers: []Server{
			{Addr: "172.18.0.5:80", Weight: 90},
			{Addr: "172.18.0.8:80", Weight: 10},
		},
	}

	got := renderUpstream(state)

	if !strings.Contains(got, "ip_hash;") {
		t.Error("cookie affinity should fall back to ip_hash for nginx")
	}
	if strings.Contains(got, "least_conn") {
		t.Error("least_conn should not be present with cookie affinity")
	}
}

func TestRenderUpstreamDisabledAffinity(t *testing.T) {
	state := &UpstreamState{
		Service:  "app",
		Affinity: "",
		Servers: []Server{
			{Addr: "172.18.0.5:80"},
		},
	}

	got := renderUpstream(state)

	if !strings.Contains(got, "least_conn;") {
		t.Error("disabled affinity should use least_conn")
	}
	if strings.Contains(got, "ip_hash") {
		t.Error("ip_hash should not be present with disabled affinity")
	}
}

func TestRenderUpstreamBackupNoAffinity(t *testing.T) {
	state := &UpstreamState{
		Service:  "app",
		Affinity: "",
		Servers: []Server{
			{Addr: "172.18.0.10:80"},
			{Addr: "172.18.0.2:80", Backup: true},
			{Addr: "172.18.0.3:80", Backup: true},
		},
	}

	got := renderUpstream(state)

	if !strings.Contains(got, "server 172.18.0.10:80;") {
		t.Error("missing primary server")
	}
	if !strings.Contains(got, "server 172.18.0.2:80 backup;") {
		t.Error("missing backup server 1")
	}
	if !strings.Contains(got, "server 172.18.0.3:80 backup;") {
		t.Error("missing backup server 2")
	}
}

func TestRenderUpstreamBackupSkippedWithIpHash(t *testing.T) {
	for _, affinity := range []string{"ip", "cookie"} {
		state := &UpstreamState{
			Service:  "app",
			Affinity: affinity,
			Servers: []Server{
				{Addr: "172.18.0.10:80"},
				{Addr: "172.18.0.2:80", Backup: true},
			},
		}

		got := renderUpstream(state)

		if strings.Contains(got, "172.18.0.2:80") {
			t.Errorf("affinity=%s: backup server should be omitted with ip_hash", affinity)
		}
		if !strings.Contains(got, "172.18.0.10:80") {
			t.Errorf("affinity=%s: primary server should still be present", affinity)
		}
	}
}

func TestGenerateConfigWritesFile(t *testing.T) {
	dir := t.TempDir()
	p := NewNginx(dir, "", "", nil, "", "")

	state := &UpstreamState{
		Service: "webapp",
		Servers: []Server{
			{Addr: "172.18.0.5:3000"},
		},
	}

	if err := p.GenerateConfig(context.Background(), state); err != nil {
		t.Fatalf("generate error: %v", err)
	}

	path := filepath.Join(dir, "webapp.conf")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read error: %v", err)
	}

	content := string(data)
	if !strings.Contains(content, "upstream webapp_upstream {") {
		t.Error("file missing upstream block")
	}
	if !strings.Contains(content, "server 172.18.0.5:3000;") {
		t.Error("file missing server")
	}

	// No temp file left behind
	tmp := path + ".tmp"
	if _, err := os.Stat(tmp); !os.IsNotExist(err) {
		t.Error("temp file not cleaned up")
	}
}

func TestGenerateConfigWritesRouteFile(t *testing.T) {
	dir := t.TempDir()
	routeDir := t.TempDir()
	p := NewNginx(dir, routeDir, "/app/", nil, "", "")

	state := &UpstreamState{
		Service: "webapp",
		Servers: []Server{{Addr: "172.18.0.5:3000"}},
	}

	if err := p.GenerateConfig(context.Background(), state); err != nil {
		t.Fatalf("generate error: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(routeDir, "webapp.location"))
	if err != nil {
		t.Fatalf("read route error: %v", err)
	}

	content := string(data)
	if !strings.Contains(content, "location /app/ {") {
		t.Error("route missing location block")
	}
	if !strings.Contains(content, "proxy_pass http://webapp_upstream/;") {
		t.Error("route missing proxy_pass")
	}
}
