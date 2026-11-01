package config

import (
	"strings"
	"testing"
)

func TestPreviewHostRules(t *testing.T) {
	for _, c := range []struct {
		name    string
		labels  map[string]string
		wantErr string
	}{
		{"accepted", map[string]string{"release.host": "app.example.com", "release.preview.host": "next.app.example.com"}, ""},
		{"same as the service's host", map[string]string{"release.host": "App.Example.com, www.example.com", "release.preview.host": "app.example.com"}, "same as release.host"},
		{"not blue-green", map[string]string{"release.strategy": "canary", "release.preview.host": "next.example.com"}, "blue-green only"},
		{"nginx-proxy", map[string]string{"release.provider": "nginx-proxy", "release.preview.host": "next.example.com"}, "nginx-proxy"},
		{"wildcard", map[string]string{"release.preview.host": "*.example.com"}, "one hostname"},
		{"several hosts", map[string]string{"release.preview.host": "a.example.com,b.example.com"}, "one hostname"},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, err := ParseLabels(blueGreenLabels(c.labels))
			if c.wantErr == "" && err != nil {
				t.Fatal(err)
			}
			if c.wantErr != "" && (err == nil || !strings.Contains(err.Error(), c.wantErr)) {
				t.Fatalf("want error containing %q, got %v", c.wantErr, err)
			}
		})
	}
}

func TestPreviewTokenRules(t *testing.T) {
	for _, c := range []struct {
		name    string
		bundled string
		labels  map[string]string
		wantErr string
	}{
		{"auto", "nginx", map[string]string{"release.preview.token": "auto"}, ""},
		{"a fixed token", "nginx", map[string]string{"release.preview.token": "from-env_0123456789abcdef"}, ""},
		{"a short fixed token", "nginx", map[string]string{"release.preview.token": "secret"}, "16 to 128"},
		{"a token that would break a config file", "nginx", map[string]string{"release.preview.token": "abcdefghijklmnop\"; return 200; #"}, "16 to 128"},
		{"not blue-green", "nginx", map[string]string{"release.strategy": "canary", "release.preview.token": "auto"}, "blue-green only"},
		{"nginx-proxy", "nginx", map[string]string{"release.provider": "nginx-proxy", "release.preview.token": "auto"}, "nginx-proxy"},
		{"separate proxy", "", map[string]string{"release.preview.token": "auto"}, "proxy bundled"},
		{"another proxy bundled", "angie", map[string]string{"release.preview.token": "auto"}, "proxy bundled"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("DR_BUNDLED_PROXY", c.bundled)
			_, err := ParseLabels(blueGreenLabels(c.labels))
			if c.wantErr == "" && err != nil {
				t.Fatal(err)
			}
			if c.wantErr != "" && (err == nil || !strings.Contains(err.Error(), c.wantErr)) {
				t.Fatalf("want error containing %q, got %v", c.wantErr, err)
			}
		})
	}
}

func TestPreviewAddresses(t *testing.T) {
	for _, c := range []struct {
		name         string
		host, path   string
		cert         string
		origin, base string
	}{
		{"plain http", "app.example.com", "", "", "http://next.example.com", "http://app.example.com/"},
		{"with a certificate", "app.example.com, www.example.com", "", "/certs/app.pem", "https://next.example.com", "https://app.example.com/"},
		{"under a path", "app.example.com", "/api", "", "http://next.example.com", "http://app.example.com/api/"},
		{"no host", "", "/api", "", "http://next.example.com", ""},
		{"only a wildcard host", "*.example.com", "", "", "http://next.example.com", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			cfg := &ServiceConfig{RouteHost: c.host, RoutePath: c.path, TLSCert: c.cert}
			if got := cfg.Origin("next.example.com"); got != c.origin {
				t.Errorf("origin: got %q, want %q", got, c.origin)
			}
			if got := cfg.PreviewBase(); got != c.base {
				t.Errorf("link base: got %q, want %q", got, c.base)
			}
		})
	}
}
