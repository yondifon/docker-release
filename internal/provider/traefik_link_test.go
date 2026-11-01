package provider

import (
	"context"
	"strings"
	"testing"
)

const linkTestToken = "tok-0123456789abcdef"

func traefikLinkOutput(t *testing.T, route NginxRoute, state *UpstreamState) string {
	t.Helper()
	p := NewTraefikRoutes(t.TempDir(), route)
	if err := p.GenerateConfig(context.Background(), state); err != nil {
		t.Fatal(err)
	}
	for _, data := range p.pending {
		return string(data)
	}
	t.Fatal("no config written")
	return ""
}

// Without the label the output is what it was before the link existed, even if a
// token reaches the provider.
func TestTraefikLinkOffRendersAsBefore(t *testing.T) {
	route := NginxRoute{Host: "app.example.com", Path: "/app/", PreviewHost: "next.app.example.com"}
	green := []Server{{Addr: "10.0.0.2:80"}}
	plain := traefikLinkOutput(t, route, &UpstreamState{Service: "app", Servers: []Server{{Addr: "10.0.0.1:80"}}, Preview: green})
	route.PreviewLink = false
	withToken := traefikLinkOutput(t, route, &UpstreamState{Service: "app", Servers: []Server{{Addr: "10.0.0.1:80"}}, Preview: green, PreviewToken: linkTestToken})
	if plain != withToken {
		t.Fatalf("a token changed the output while the link is off:\n%s\n---\n%s", plain, withToken)
	}
}

// The token is in the config only while green waits, and the config differs on
// every state, so an old API snapshot cannot confirm a new one.
func TestTraefikLinkTokenOnlyWhileLive(t *testing.T) {
	route := NginxRoute{Host: "app.example.com", PreviewLink: true}
	blue := []Server{{Addr: "10.0.0.1:80"}}
	green := []Server{{Addr: "10.0.0.2:80"}}
	idle := traefikLinkOutput(t, route, &UpstreamState{Service: "app", Servers: blue})
	live := traefikLinkOutput(t, route, &UpstreamState{Service: "app", Servers: blue, Preview: green, PreviewToken: linkTestToken})
	if strings.Contains(idle, linkTestToken) || !strings.Contains(live, linkTestToken) {
		t.Fatalf("token must be in the live config only:\nidle:\n%s\nlive:\n%s", idle, live)
	}
	noToken := traefikLinkOutput(t, route, &UpstreamState{Service: "app", Servers: blue, Preview: green})
	if strings.Contains(noToken, "-link-") || strings.Contains(noToken, "-token-") {
		t.Fatalf("link routes rendered without a token:\n%s", noToken)
	}
}
