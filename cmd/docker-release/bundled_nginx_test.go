//go:build bundled_nginx

package main

import (
	"strings"
	"testing"
)

func TestRenderBundledNginxConfigHTTPOnly(t *testing.T) {
	got := renderBundledNginxConfig(bundledNginxConfig{})

	if !strings.Contains(got, "listen 80 default_server;") {
		t.Error("missing HTTP listener")
	}
	if strings.Contains(got, "ssl_certificate") {
		t.Error("base config should not include SSL directives")
	}
	if !strings.Contains(got, "include /shared/nginx-routes/*.server;") {
		t.Error("missing generated server include")
	}
	if !strings.Contains(got, "include /shared/nginx-routes/*.location;") {
		t.Error("missing fallback location include")
	}
}
