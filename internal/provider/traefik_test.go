package provider

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRenderTraefikBasic(t *testing.T) {
	state := &UpstreamState{
		Service: "app",
		Servers: []Server{
			{Addr: "172.18.0.5:80"},
			{Addr: "172.18.0.6:80"},
		},
	}

	got := renderTraefikYAML(state)

	expects := []string{
		"app:",
		"loadBalancer:",
		`url: "http://172.18.0.5:80"`,
		`url: "http://172.18.0.6:80"`,
	}

	for _, want := range expects {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}

	if strings.Contains(got, "weighted") {
		t.Error("should not have weighted section without weights")
	}

	if strings.Contains(got, "sticky") {
		t.Error("should not have sticky without affinity")
	}

	if strings.Contains(got, "routers:") || strings.Contains(got, "app.local") {
		t.Error("should not generate routers by default")
	}
}

func TestRenderTraefikWithCookieAffinity(t *testing.T) {
	state := &UpstreamState{
		Service:  "app",
		Affinity: "cookie",
		Servers: []Server{
			{Addr: "172.18.0.5:80"},
		},
	}

	got := renderTraefikYAML(state)

	expects := []string{
		"sticky:",
		"cookie:",
		`name: "_srr_a172cedcae"`,
	}

	for _, want := range expects {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

func TestRenderTraefikWeighted(t *testing.T) {
	state := &UpstreamState{
		Service:  "app",
		Affinity: "cookie",
		Servers: []Server{
			{Addr: "172.18.0.5:80", Weight: 90},
			{Addr: "172.18.0.6:80", Weight: 90},
			{Addr: "172.18.0.8:80", Weight: 10},
		},
	}

	got := renderTraefikYAML(state)

	expects := []string{
		"weighted:",
		"app-stable:",
		"app-canary:",
		"weight: 90",
		"weight: 10",
		`url: "http://172.18.0.5:80"`,
		`url: "http://172.18.0.6:80"`,
		`url: "http://172.18.0.8:80"`,
		"sticky:",
		"cookie:",
		`name: "_srr_a172cedcae"`,
	}

	for _, want := range expects {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

func TestRenderTraefikWithIPAffinity(t *testing.T) {
	state := &UpstreamState{
		Service:  "app",
		Affinity: "ip",
		Servers: []Server{
			{Addr: "172.18.0.5:80"},
		},
	}

	got := renderTraefikYAML(state)

	if !strings.Contains(got, "sticky:") || !strings.Contains(got, "cookie:") {
		t.Error("ip affinity on a non-weighted Traefik service should fall back to sticky cookie")
	}
	if strings.Contains(got, `strategy: "hrw"`) {
		t.Error("hrw is not a valid loadBalancer field in Traefik; only valid as a service type for weighted services")
	}
}

func TestRenderTraefikDisabledAffinity(t *testing.T) {
	state := &UpstreamState{
		Service:  "app",
		Affinity: "",
		Servers: []Server{
			{Addr: "172.18.0.5:80"},
		},
	}

	got := renderTraefikYAML(state)

	if strings.Contains(got, "sticky:") {
		t.Error("disabled affinity should not have sticky section")
	}
	if strings.Contains(got, "cookie:") {
		t.Error("disabled affinity should not have cookie section")
	}
}

func TestRenderTraefikWeightedBlueGreenCutover(t *testing.T) {
	state := &UpstreamState{
		Service:  "blue_green_app",
		Affinity: "ip",
		Servers: []Server{
			{Addr: "172.18.0.5:80", Weight: 50, Group: "stable"},
			{Addr: "172.18.0.6:80", Weight: 50, Group: "stable"},
			{Addr: "172.18.0.8:80", Weight: 50, Group: "canary"},
			{Addr: "172.18.0.9:80", Weight: 50, Group: "canary"},
		},
	}

	got := renderTraefikYAML(state)

	expects := []string{
		"highestRandomWeight:",
		"blue_green_app-stable:",
		"blue_green_app-canary:",
		"weight: 50",
		`url: "http://172.18.0.5:80"`,
		`url: "http://172.18.0.6:80"`,
		`url: "http://172.18.0.8:80"`,
		`url: "http://172.18.0.9:80"`,
	}

	for _, want := range expects {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}

	if strings.Count(got, "weight: 50") != 2 {
		t.Errorf("expected both weighted groups at 50:\n%s", got)
	}
}

func TestRenderTraefikSkipsDownServers(t *testing.T) {
	state := &UpstreamState{
		Service: "app",
		Servers: []Server{
			{Addr: "172.18.0.5:80"},
			{Addr: "172.18.0.6:80", Down: true},
		},
	}

	got := renderTraefikYAML(state)

	if !strings.Contains(got, `url: "http://172.18.0.5:80"`) {
		t.Error("missing active server")
	}
	if strings.Contains(got, "172.18.0.6") {
		t.Error("down server should be excluded")
	}
}

func TestTraefikGenerateConfigWritesFile(t *testing.T) {
	dir := t.TempDir()
	p := NewTraefik(dir)

	state := &UpstreamState{
		Service: "webapp",
		Servers: []Server{
			{Addr: "172.18.0.5:3000"},
		},
	}

	if err := p.GenerateConfig(context.Background(), state); err != nil {
		t.Fatalf("generate error: %v", err)
	}

	path := filepath.Join(dir, "webapp.yml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read error: %v", err)
	}

	content := string(data)
	if !strings.Contains(content, "webapp:") {
		t.Error("file missing service name")
	}
	if !strings.Contains(content, `url: "http://172.18.0.5:3000"`) {
		t.Error("file missing server url")
	}

	tmp := path + ".tmp"
	if _, err := os.Stat(tmp); !os.IsNotExist(err) {
		t.Error("temp file not cleaned up")
	}
}

func TestTraefikReloadIsNoop(t *testing.T) {
	p := NewTraefik("/tmp/test")

	if err := p.Reload(context.Background()); err != nil {
		t.Errorf("reload should be no-op, got: %v", err)
	}
}

func TestTraefikBackupServersDropped(t *testing.T) {
	state := &UpstreamState{
		Service: "app",
		Servers: []Server{
			{Addr: "172.18.0.10:80"},
			{Addr: "172.18.0.2:80", Backup: true},
		},
	}

	got := renderTraefikYAML(state)

	if !strings.Contains(got, "172.18.0.10:80") {
		t.Error("missing primary server")
	}
	if strings.Contains(got, "172.18.0.2:80") {
		t.Error("backup server should be silently dropped for traefik")
	}
}
