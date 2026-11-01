package provider

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

const previewTestToken = "tok-0123456789abcdefABCDEF"

// previewFiles generates the route files of `route` for a release whose green servers wait.
func previewFiles(t *testing.T, kind string, route NginxRoute, token string) map[string]string {
	t.Helper()
	p, _ := nginxReloadFixture(t)
	p.proxyName = kind
	p.route = route
	state := &UpstreamState{
		Service: "app", Servers: []Server{{Addr: "10.0.0.1:80"}},
		Preview: []Server{{Addr: "10.0.0.2:80"}}, PreviewToken: token,
	}
	if err := p.GenerateConfig(context.Background(), state); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{}
	for path, data := range p.pending {
		files[filepath.Base(path)] = string(data)
	}
	return files
}

// Without the preview link, a token handed to the provider changes nothing.
func TestNginxWithoutPreviewLinkIgnoresTheToken(t *testing.T) {
	routes := map[string]NginxRoute{
		"host": previewRoute(),
		"path": {Path: "/app/", PreviewHost: "next.example.com"},
	}
	for _, kind := range []string{"nginx", "angie"} {
		for name, route := range routes {
			with := previewFiles(t, kind, route, previewTestToken)
			without := previewFiles(t, kind, route, "")
			for file, content := range without {
				if with[file] != content {
					t.Fatalf("%s %s: %s changed by a token the route does not use:\n%s", kind, name, file, with[file])
				}
				if strings.Contains(content, "dr_pv_") || strings.Contains(content, "release_preview") {
					t.Fatalf("%s %s: %s has preview link rules without a preview link", kind, name, file)
				}
			}
		}
	}
}

// With the preview link, the token is written to the upstream file while green waits
// and to no other file, and the config has the same shape without it.
func TestNginxPreviewLinkTokenIsInTheConfigOnlyWhileLive(t *testing.T) {
	routes := map[string]NginxRoute{
		"host":      {Host: "app.example.com", PreviewLink: true},
		"path":      {Path: "/app/", PreviewLink: true},
		"tls+host":  {Host: "app.example.com", PreviewHost: "next.example.com", PreviewLink: true, SSLCert: "/c.pem", SSLKey: "/c.key"},
		"path+host": {Path: "/app/", PreviewHost: "next.example.com", PreviewLink: true},
	}
	for _, kind := range []string{"nginx", "angie"} {
		for name, route := range routes {
			live := previewFiles(t, kind, route, previewTestToken)
			idle := previewFiles(t, kind, route, "")
			if !strings.Contains(live["app.conf"], previewTestToken) {
				t.Fatalf("%s %s: the live upstream file has no token:\n%s", kind, name, live["app.conf"])
			}
			for file, content := range live {
				if file != "app.conf" && strings.Contains(content, previewTestToken) {
					t.Fatalf("%s %s: %s holds the token", kind, name, file)
				}
			}
			for file, content := range idle {
				if strings.Contains(content, previewTestToken) {
					t.Fatalf("%s %s: %s holds a token that is not live", kind, name, file)
				}
				if !strings.Contains(content, "upstream app_preview_upstream {") && file == "app.conf" {
					t.Fatalf("%s %s: no preview upstream for the rules to name", kind, name)
				}
			}
			for _, file := range []string{"app.server", "app.location"} {
				if live[file] != idle[file] {
					t.Fatalf("%s %s: %s changes with the token", kind, name, file)
				}
			}
		}
	}
}
