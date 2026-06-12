package provider

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const stockTemplate = `# stock nginx.tmpl
{{- define "upstream" }}
    {{- $path := .Path }}
    {{- $vpath := .VPath }}
upstream {{ $vpath.upstream }} {
    {{- range $port, $containers := $vpath.ports }}
        {{- range $container := $containers }}
    server {{ $container.IP }}:{{ $port }};
        {{- end }}
    {{- end }}
}
{{- end }}

# rest of template
server {
    listen 80;
}
`

func TestNginxProxyNoServices(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nginx.tmpl")
	os.WriteFile(path, []byte(stockTemplate), 0o644)

	p := NewNginxProxyFromString(path, stockTemplate)

	state := &UpstreamState{
		Service: "app",
		Servers: []Server{
			{Addr: "172.18.0.5:80"},
		},
	}

	if err := p.GenerateConfig(context.Background(), state); err != nil {
		t.Fatalf("generate: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	content := string(data)

	if !strings.Contains(content, "docker-release:managed-start") {
		t.Error("missing managed marker")
	}

	if !strings.Contains(content, "server 172.18.0.5:80;") {
		t.Error("missing server entry")
	}

	if !strings.Contains(content, `upstream app {`) {
		t.Error("missing upstream name")
	}

	if !strings.Contains(content, "rest of template") {
		t.Error("rest of template should be preserved")
	}
}

func TestNginxProxyWithWeights(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nginx.tmpl")
	os.WriteFile(path, []byte(stockTemplate), 0o644)

	p := NewNginxProxyFromString(path, stockTemplate)

	state := &UpstreamState{
		Service:  "app",
		Affinity: "ip",
		Servers: []Server{
			{Addr: "172.18.0.5:80", Weight: 90},
			{Addr: "172.18.0.6:80", Weight: 90},
			{Addr: "172.18.0.8:80", Weight: 10},
		},
	}

	if err := p.GenerateConfig(context.Background(), state); err != nil {
		t.Fatalf("generate: %v", err)
	}

	data, _ := os.ReadFile(path)
	content := string(data)

	if !strings.Contains(content, "ip_hash;") {
		t.Error("missing ip_hash")
	}

	if !strings.Contains(content, "server 172.18.0.5:80 weight=90;") {
		t.Error("missing weighted server 1")
	}

	if !strings.Contains(content, "server 172.18.0.8:80 weight=10;") {
		t.Error("missing canary server")
	}
}

func TestNginxProxyWithDrain(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nginx.tmpl")
	os.WriteFile(path, []byte(stockTemplate), 0o644)

	p := NewNginxProxyFromString(path, stockTemplate)

	state := &UpstreamState{
		Service: "app",
		Servers: []Server{
			{Addr: "172.18.0.5:80"},
			{Addr: "172.18.0.6:80", Down: true},
		},
	}

	if err := p.GenerateConfig(context.Background(), state); err != nil {
		t.Fatalf("generate: %v", err)
	}

	data, _ := os.ReadFile(path)
	content := string(data)

	if !strings.Contains(content, "server 172.18.0.6:80 down;") {
		t.Error("missing down server")
	}
}

func TestNginxProxyKeepalive(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nginx.tmpl")
	os.WriteFile(path, []byte(stockTemplate), 0o644)

	p := NewNginxProxyFromString(path, stockTemplate)

	state := &UpstreamState{
		Service:   "app",
		Keepalive: 7,
		Servers: []Server{
			{Addr: "172.18.0.5:80"},
		},
	}

	if err := p.GenerateConfig(context.Background(), state); err != nil {
		t.Fatalf("generate: %v", err)
	}

	data, _ := os.ReadFile(path)
	content := string(data)

	if !strings.Contains(content, "keepalive 7;") {
		t.Error("missing keepalive directive")
	}
}

func TestNginxProxyMultipleServices(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nginx.tmpl")
	os.WriteFile(path, []byte(stockTemplate), 0o644)

	p := NewNginxProxyFromString(path, stockTemplate)

	if err := p.GenerateConfig(context.Background(), &UpstreamState{
		Service: "webapp",
		Servers: []Server{{Addr: "172.18.0.5:3000"}},
	}); err != nil {
		t.Fatal(err)
	}

	if err := p.GenerateConfig(context.Background(), &UpstreamState{
		Service: "apiapp",
		Servers: []Server{{Addr: "172.18.0.10:8080"}},
	}); err != nil {
		t.Fatal(err)
	}

	data, _ := os.ReadFile(path)
	content := string(data)

	if !strings.Contains(content, `upstream webapp {`) {
		t.Error("missing webapp upstream")
	}

	if !strings.Contains(content, `upstream apiapp {`) {
		t.Error("missing apiapp upstream")
	}

	if !strings.Contains(content, "server 172.18.0.5:3000;") {
		t.Error("missing webapp server")
	}

	if !strings.Contains(content, "server 172.18.0.10:8080;") {
		t.Error("missing apiapp server")
	}
}

func TestNginxProxyRemoveService(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nginx.tmpl")
	os.WriteFile(path, []byte(stockTemplate), 0o644)

	p := NewNginxProxyFromString(path, stockTemplate)

	p.GenerateConfig(context.Background(), &UpstreamState{
		Service: "app",
		Servers: []Server{{Addr: "172.18.0.5:80"}},
	})

	if err := p.RemoveService("app"); err != nil {
		t.Fatal(err)
	}

	data, _ := os.ReadFile(path)
	content := string(data)

	if content != stockTemplate {
		t.Error("template should revert to stock when no services are managed")
	}
}

func TestNginxProxyPreservesOriginalFallback(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nginx.tmpl")
	os.WriteFile(path, []byte(stockTemplate), 0o644)

	p := NewNginxProxyFromString(path, stockTemplate)

	p.GenerateConfig(context.Background(), &UpstreamState{
		Service: "app",
		Servers: []Server{{Addr: "172.18.0.5:80"}},
	})

	data, _ := os.ReadFile(path)
	content := string(data)

	if !strings.Contains(content, "{{- else }}") {
		t.Error("missing else clause for unmanaged upstreams")
	}

	if !strings.Contains(content, "$vpath.ports") {
		t.Error("original upstream logic should be preserved in else clause")
	}
}

func TestNginxProxyAtomicWrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nginx.tmpl")
	os.WriteFile(path, []byte(stockTemplate), 0o644)

	p := NewNginxProxyFromString(path, stockTemplate)

	p.GenerateConfig(context.Background(), &UpstreamState{
		Service: "app",
		Servers: []Server{{Addr: "172.18.0.5:80"}},
	})

	tmp := path + ".tmp"
	if _, err := os.Stat(tmp); !os.IsNotExist(err) {
		t.Error("temp file not cleaned up")
	}
}

func TestNginxProxyUpstreamNameOverride(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nginx.tmpl")
	os.WriteFile(path, []byte(stockTemplate), 0o644)

	p := NewNginxProxyFromString(path, stockTemplate)

	state := &UpstreamState{
		Service:      "webapp",
		UpstreamName: "webapp.local",
		Servers: []Server{
			{Addr: "172.18.0.5:80"},
		},
	}

	if err := p.GenerateConfig(context.Background(), state); err != nil {
		t.Fatalf("generate: %v", err)
	}

	data, _ := os.ReadFile(path)
	content := string(data)

	if !strings.Contains(content, `upstream webapp.local {`) {
		t.Error("should use UpstreamName for upstream block name")
	}

	if !strings.Contains(content, `eq $vpath.upstream "webapp.local"`) {
		t.Error("should use UpstreamName in conditional match")
	}
}

func TestExtractUpstreamBlock(t *testing.T) {
	block := extractUpstreamBlock(stockTemplate)

	if block == "" {
		t.Fatal("failed to extract upstream block")
	}

	if !strings.HasPrefix(block, `{{- define "upstream" }}`) {
		t.Error("block should start with upstream define")
	}

	if !strings.HasSuffix(block, "{{- end }}") {
		t.Error("block should end with end")
	}
}

func TestNginxProxyBackupNoAffinity(t *testing.T) {
	state := &UpstreamState{
		Service:  "app",
		Affinity: "",
		Servers: []Server{
			{Addr: "172.18.0.10:80"},
			{Addr: "172.18.0.2:80", Backup: true},
		},
	}

	got := renderNginxProxyUpstream(state)

	if !strings.Contains(got, "server 172.18.0.10:80;") {
		t.Error("missing primary server")
	}
	if !strings.Contains(got, "server 172.18.0.2:80 backup;") {
		t.Error("missing backup server")
	}
}

func TestNginxProxyBackupSkippedWithIpHash(t *testing.T) {
	for _, affinity := range []string{"ip", "cookie"} {
		state := &UpstreamState{
			Service:  "app",
			Affinity: affinity,
			Servers: []Server{
				{Addr: "172.18.0.10:80"},
				{Addr: "172.18.0.2:80", Backup: true},
			},
		}

		got := renderNginxProxyUpstream(state)

		if strings.Contains(got, "172.18.0.2:80") {
			t.Errorf("affinity=%s: backup server should be omitted with ip_hash", affinity)
		}
		if !strings.Contains(got, "172.18.0.10:80") {
			t.Errorf("affinity=%s: primary server should still be present", affinity)
		}
	}
}
