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
