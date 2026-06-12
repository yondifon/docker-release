package provider

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func projectRoot() string {
	_, filename, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(filename), "..", "..")
}

func TestExtractUpstreamFromRealTemplate(t *testing.T) {
	tmplPath := filepath.Join(projectRoot(), "builds", "nginx.tmpl")

	data, err := os.ReadFile(tmplPath)
	if err != nil {
		t.Skipf("stock template not available: %v", err)
	}

	stock := string(data)
	block := extractUpstreamBlock(stock)

	if block == "" {
		t.Fatal("failed to extract upstream block from real template")
	}

	if !strings.Contains(block, "$vpath.upstream") {
		t.Error("extracted block missing upstream name reference")
	}

	if !strings.Contains(block, "server 127.0.0.1 down") {
		t.Error("extracted block missing fallback entry")
	}

	if !strings.Contains(block, "keepalive") {
		t.Error("extracted block missing keepalive")
	}
}

func TestNginxProxyWithRealTemplate(t *testing.T) {
	tmplPath := filepath.Join(projectRoot(), "builds", "nginx.tmpl")

	data, err := os.ReadFile(tmplPath)
	if err != nil {
		t.Skipf("stock template not available: %v", err)
	}

	stock := string(data)
	dir := t.TempDir()
	outPath := filepath.Join(dir, "nginx.tmpl")
	os.WriteFile(outPath, data, 0o644)

	p := NewNginxProxyFromString(outPath, stock)

	state := &UpstreamState{
		Service:  "webapp",
		Affinity: "ip",
		Servers: []Server{
			{Addr: "172.18.0.5:80", Weight: 90},
			{Addr: "172.18.0.8:80", Weight: 10},
		},
	}

	if err := p.GenerateConfig(context.Background(), state); err != nil {
		t.Fatalf("generate: %v", err)
	}

	result, _ := os.ReadFile(outPath)
	content := string(result)

	if !strings.Contains(content, `upstream webapp {`) {
		t.Error("missing managed upstream")
	}

	if !strings.Contains(content, "ip_hash;") {
		t.Error("missing ip_hash")
	}

	if !strings.Contains(content, "weight=90") {
		t.Error("missing weight")
	}

	if !strings.Contains(content, "keepalive") {
		t.Error("original template content (keepalive) should be preserved")
	}

	if !strings.Contains(content, "ssl_policy") {
		t.Error("non-upstream template content should be fully preserved")
	}

	if err := p.RemoveService("webapp"); err != nil {
		t.Fatal(err)
	}

	reverted, _ := os.ReadFile(outPath)
	if string(reverted) != stock {
		t.Error("template should revert exactly to stock after removing all services")
	}
}
