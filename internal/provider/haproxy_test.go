package provider

import (
	"strings"
	"testing"
)

func TestHAProxyRenderBackendCookieAffinity(t *testing.T) {
	state := &UpstreamState{
		Service:  "app",
		Affinity: "cookie",
		Servers: []Server{
			{Addr: "172.18.0.5:80"},
			{Addr: "172.18.0.6:80"},
		},
	}

	got := renderHAProxyBackend(state)

	if !strings.Contains(got, "cookie _srr_a172cedcae insert indirect nocache") {
		t.Error("missing cookie affinity directive")
	}
	if !strings.Contains(got, "server s1 172.18.0.5:80 cookie s1") {
		t.Error("missing per-server cookie s1")
	}
	if !strings.Contains(got, "server s2 172.18.0.6:80 cookie s2") {
		t.Error("missing per-server cookie s2")
	}
}

func TestHAProxyRenderBackendIPAffinity(t *testing.T) {
	state := &UpstreamState{
		Service:  "app",
		Affinity: "ip",
		Servers:  []Server{{Addr: "172.18.0.5:80"}},
	}

	got := renderHAProxyBackend(state)

	if !strings.Contains(got, "balance source") {
		t.Error("missing source balance for ip affinity")
	}
	if strings.Contains(got, "roundrobin") {
		t.Error("roundrobin should not appear with ip affinity")
	}
}

func TestHAProxyRenderBackendWeighted(t *testing.T) {
	state := &UpstreamState{
		Service: "app",
		Servers: []Server{
			{Addr: "172.18.0.5:80", Weight: 75},
			{Addr: "172.18.0.8:80", Weight: 25},
		},
	}

	got := renderHAProxyBackend(state)

	if !strings.Contains(got, "server s1 172.18.0.5:80 weight 75") {
		t.Error("missing weighted server 1")
	}
	if !strings.Contains(got, "server s2 172.18.0.8:80 weight 25") {
		t.Error("missing weighted canary server")
	}
}

func TestHAProxyRenderBackendDrain(t *testing.T) {
	state := &UpstreamState{
		Service: "app",
		Servers: []Server{
			{Addr: "172.18.0.5:80"},
			{Addr: "172.18.0.6:80", Down: true},
		},
	}

	got := renderHAProxyBackend(state)

	if !strings.Contains(got, "server s2 172.18.0.6:80 disabled") {
		t.Error("down server should be rendered with disabled keyword")
	}
	if strings.Contains(got, "server s1 172.18.0.5:80 disabled") {
		t.Error("active server should not be disabled")
	}
}

func TestHAProxyRenderBackendCustomName(t *testing.T) {
	state := &UpstreamState{
		Service:      "app",
		UpstreamName: "myapp",
		Servers:      []Server{{Addr: "172.18.0.5:80"}},
	}

	got := renderHAProxyBackend(state)

	if !strings.Contains(got, "backend myapp_be") {
		t.Error("should use UpstreamName override in backend name")
	}
	if strings.Contains(got, "backend app_be") {
		t.Error("should not use service name when UpstreamName is set")
	}
}

func TestHAProxyRenderBackendBackup(t *testing.T) {
	state := &UpstreamState{
		Service: "app",
		Servers: []Server{
			{Addr: "172.18.0.10:80"},
			{Addr: "172.18.0.2:80", Backup: true},
		},
	}

	got := renderHAProxyBackend(state)

	if !strings.Contains(got, "server s1 172.18.0.10:80") {
		t.Error("missing primary server")
	}
	if strings.Contains(got, "server s1 172.18.0.10:80 backup") {
		t.Error("primary server should not have backup flag")
	}
	if !strings.Contains(got, "server s2 172.18.0.2:80 backup") {
		t.Error("backup server should have backup keyword")
	}
}

func TestHAProxyRenderBackendBackupWithIPAffinity(t *testing.T) {
	state := &UpstreamState{
		Service:  "app",
		Affinity: "ip",
		Servers: []Server{
			{Addr: "172.18.0.10:80"},
			{Addr: "172.18.0.2:80", Backup: true},
		},
	}

	got := renderHAProxyBackend(state)

	if !strings.Contains(got, "server s2 172.18.0.2:80 backup") {
		t.Error("haproxy backup should work regardless of affinity")
	}
}

func TestHAProxyRenderBackendBackupNoCookieDirective(t *testing.T) {
	state := &UpstreamState{
		Service:  "app",
		Affinity: "cookie",
		Servers: []Server{
			{Addr: "172.18.0.10:80"},
			{Addr: "172.18.0.2:80", Backup: true},
		},
	}

	got := renderHAProxyBackend(state)

	if !strings.Contains(got, "server s1 172.18.0.10:80 cookie s1") {
		t.Error("primary server should have cookie directive")
	}
	if strings.Contains(got, "server s2 172.18.0.2:80 cookie") {
		t.Error("backup server should not have cookie directive")
	}
	if !strings.Contains(got, "server s2 172.18.0.2:80 backup") {
		t.Error("backup server should have backup keyword")
	}
}
