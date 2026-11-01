package provider

import (
	"strings"
	"testing"
)

func TestCaddyRenderUpstreamCookieAffinity(t *testing.T) {
	state := &UpstreamState{
		Service:  "app",
		Affinity: "cookie",
		Servers:  []Server{{Addr: "172.18.0.5:80"}},
	}

	got := renderCaddyUpstream(state)

	if !strings.Contains(got, "lb_policy cookie _srr_a172cedcae") {
		t.Error("missing cookie affinity policy")
	}
	if strings.Contains(got, "round_robin") {
		t.Error("round_robin should not appear with cookie affinity")
	}
}

func TestCaddyRenderUpstreamWeightedNoCookieAffinity(t *testing.T) {
	state := &UpstreamState{
		Service: "app",
		Servers: []Server{
			{Addr: "172.18.0.5:80", Weight: 75},
			{Addr: "172.18.0.6:80", Weight: 75},
			{Addr: "172.18.0.8:80", Weight: 25},
		},
	}

	got := renderCaddyUpstream(state)

	if !strings.Contains(got, "lb_policy weighted_round_robin") {
		t.Error("missing weighted_round_robin policy")
	}
	if !strings.Contains(got, "172.18.0.5:80") || !strings.Contains(got, "172.18.0.8:80") {
		t.Error("missing servers in weighted upstream")
	}
}

func TestCaddyRenderUpstreamCookieAffinityWithWeights(t *testing.T) {
	state := &UpstreamState{
		Service:  "app",
		Affinity: "cookie",
		Servers: []Server{
			{Addr: "172.18.0.5:80", Weight: 75},
			{Addr: "172.18.0.8:80", Weight: 25},
		},
	}

	got := renderCaddyUpstream(state)

	if !strings.Contains(got, "lb_policy cookie") {
		t.Error("cookie affinity must win over weights")
	}
	if strings.Contains(got, "weighted_round_robin") {
		t.Error("weighted_round_robin must not appear when affinity is cookie")
	}
}

func TestCaddyRenderUpstreamDrain(t *testing.T) {
	state := &UpstreamState{
		Service: "app",
		Servers: []Server{
			{Addr: "172.18.0.5:80"},
			{Addr: "172.18.0.6:80", Down: true},
		},
	}

	got := renderCaddyUpstream(state)

	if strings.Contains(got, "172.18.0.6:80") {
		t.Error("down server should be omitted from Caddy upstream")
	}
	if !strings.Contains(got, "172.18.0.5:80") {
		t.Error("active server must be present")
	}
}

func TestCaddyRenderUpstreamCustomName(t *testing.T) {
	state := &UpstreamState{
		Service:      "app",
		UpstreamName: "myapp",
		Servers:      []Server{{Addr: "172.18.0.5:80"}},
	}

	got := renderCaddyUpstream(state)

	if !strings.Contains(got, "(myapp_upstream) {") {
		t.Error("should use UpstreamName override in snippet name")
	}
	if strings.Contains(got, "(app_upstream)") {
		t.Error("should not use service name when UpstreamName is set")
	}
}

func TestCaddyBackupServersDropped(t *testing.T) {
	state := &UpstreamState{
		Service: "app",
		Servers: []Server{
			{Addr: "172.18.0.10:80"},
			{Addr: "172.18.0.2:80", Backup: true},
			{Addr: "172.18.0.3:80", Backup: true},
		},
	}

	got := renderCaddyUpstream(state)

	if !strings.Contains(got, "172.18.0.10:80") {
		t.Error("missing primary server")
	}
	if strings.Contains(got, "172.18.0.2:80") {
		t.Error("backup server should be silently dropped for caddy")
	}
	if strings.Contains(got, "172.18.0.3:80") {
		t.Error("backup server should be silently dropped for caddy")
	}
}
