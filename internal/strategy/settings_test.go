package strategy

import (
	"testing"

	"github.com/malico/docker-release/internal/config"
	"github.com/malico/docker-release/internal/provider"
)

func TestApplyProviderSettingsAngieStickyLearn(t *testing.T) {
	cfg := &config.ServiceConfig{
		Provider:             config.ProviderAngie,
		AngieKeepalive:       8,
		AngieStickyLearnName: "session",
	}
	upstream := &provider.UpstreamState{Servers: []provider.Server{{Addr: "10.0.0.1:80"}}}

	ApplyProviderSettings(cfg, upstream)

	if upstream.StickyLearnName != "session" {
		t.Errorf("StickyLearnName: want session, got %q", upstream.StickyLearnName)
	}
	if upstream.Keepalive != 8 {
		t.Errorf("Keepalive: want 8, got %d", upstream.Keepalive)
	}
}

func TestApplyProviderSettingsCaddyKeepalive(t *testing.T) {
	cfg := &config.ServiceConfig{Provider: config.ProviderCaddy, CaddyKeepalive: 5}
	upstream := &provider.UpstreamState{Servers: []provider.Server{{Addr: "10.0.0.1:80"}}}

	ApplyProviderSettings(cfg, upstream)

	if upstream.Keepalive != 5 {
		t.Errorf("Keepalive: want 5, got %d", upstream.Keepalive)
	}
}

func TestApplyProviderSettingsNilSafe(t *testing.T) {
	ApplyProviderSettings(nil, &provider.UpstreamState{})
	ApplyProviderSettings(&config.ServiceConfig{}, nil)
}
