//go:build bundled_nginx

package main

import (
	"strings"
	"testing"
)

func TestRenderBundledNginxConfigHTTPOnly(t *testing.T) {
	cfg := bundledNginxConfig{HTTPPort: 8080, HTTPSPort: 443, ServerName: "_"}

	got := renderBundledNginxConfig(cfg)

	if !strings.Contains(got, "listen 8080 default_server;") {
		t.Error("missing HTTP listener")
	}
	if strings.Contains(got, "ssl_certificate") {
		t.Error("HTTP-only config should not include SSL directives")
	}
	if !strings.Contains(got, "include /shared/nginx-routes/*.location;") {
		t.Error("missing generated route include")
	}
}

func TestRenderBundledNginxConfigHTTPS(t *testing.T) {
	cfg := bundledNginxConfig{
		HTTPPort:      80,
		HTTPSPort:     8443,
		ServerName:    "example.com",
		SSLCert:       "/certs/fullchain.pem",
		SSLKey:        "/certs/privkey.pem",
		RedirectHTTPS: true,
	}

	got := renderBundledNginxConfig(cfg)

	if !strings.Contains(got, "listen 8443 ssl default_server;") {
		t.Error("missing HTTPS listener")
	}
	if !strings.Contains(got, "ssl_certificate /certs/fullchain.pem;") {
		t.Error("missing SSL cert")
	}
	if !strings.Contains(got, "return 308 https://$host$request_uri;") {
		t.Error("missing HTTP to HTTPS redirect")
	}
}

func TestBundledNginxConfigRejectsRedirectWithoutCert(t *testing.T) {
	t.Setenv("DR_NGINX_REDIRECT_HTTPS", "true")

	_, err := bundledNginxConfigFromEnv()
	if err == nil {
		t.Fatal("expected redirect without cert/key to fail")
	}
}
