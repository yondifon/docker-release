package provider

import (
	"strings"
	"testing"
)

func TestRenderUpstreamAffinityExcludesLeastConn(t *testing.T) {
	for _, affinity := range []string{"ip", "cookie"} {
		got := renderUpstream(&UpstreamState{Service: "app", Affinity: affinity, Servers: []Server{{Addr: "172.18.0.5:80"}}})
		if !strings.Contains(got, "ip_hash;") || strings.Contains(got, "least_conn") {
			t.Fatalf("affinity %q produces conflicting nginx balancers: %s", affinity, got)
		}
	}
}

func TestRenderUpstreamBackupSkippedWithIpHash(t *testing.T) {
	for _, affinity := range []string{"ip", "cookie"} {
		got := renderUpstream(&UpstreamState{
			Service: "app", Affinity: affinity,
			Servers: []Server{{Addr: "172.18.0.10:80"}, {Addr: "172.18.0.2:80", Backup: true}},
		})
		if strings.Contains(got, "172.18.0.2:80") || !strings.Contains(got, "172.18.0.10:80") {
			t.Fatalf("affinity %q generates nginx's unsupported backup/hash combination: %s", affinity, got)
		}
	}
}
