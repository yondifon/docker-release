package controller

import (
	"strings"
	"testing"

	"github.com/docker/docker/api/types"
	"github.com/malico/docker-release/internal/config"
)

func serviceContainer(service string, labels map[string]string) types.Container {
	labels["com.docker.compose.service"] = service
	labels["com.docker.compose.project"] = "shop"
	labels["release.enable"] = "true"
	labels["release.provider"] = "nginx"
	return types.Container{ID: service, Labels: labels}
}

func TestPreviewHostCannotBeAnotherServicesHost(t *testing.T) {
	web := serviceContainer("web", map[string]string{"release.strategy": "blue-green", "release.host": "app.example.com", "release.preview.host": "next.example.com"})
	cfg, err := config.ParseLabels(web.Labels)
	if err != nil {
		t.Fatal(err)
	}
	key := serviceKey{project: "shop", service: "web"}

	for name, other := range map[string]map[string]string{
		"host":         {"release.host": "docs.example.com, NEXT.example.com"},
		"preview host": {"release.strategy": "blue-green", "release.preview.host": "next.example.com"},
	} {
		err := checkPreviewHost(key, cfg, []types.Container{web, serviceContainer("docs", other)})
		if err == nil || !strings.Contains(err.Error(), `service "docs"`) {
			t.Errorf("%s: want a refusal naming the other service, got %v", name, err)
		}
	}

	if err := checkPreviewHost(key, cfg, []types.Container{web, serviceContainer("docs", map[string]string{"release.host": "docs.example.com"})}); err != nil {
		t.Errorf("a different host must pass: %v", err)
	}
}

func TestCheckGetsThePreviewURL(t *testing.T) {
	for cert, want := range map[string]string{"": "http://next.example.com", "/certs/app.pem": "https://next.example.com"} {
		if got := previewURL(&config.ServiceConfig{PreviewHost: "next.example.com", TLSCert: cert}); got != want {
			t.Errorf("cert %q: got %s, want %s", cert, got, want)
		}
	}
}
