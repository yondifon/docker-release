package provider

import (
	"fmt"
	"strings"
)

func renderHAProxyBundledBackend(state *UpstreamState) string {
	var b strings.Builder
	b.WriteString(haproxyHeader)
	fmt.Fprintf(&b, "backend %s_be\n", state.ResolveUpstreamName())
	// Client connections stay open; backend connections must not outlive a slot's address.
	b.WriteString("    mode http\n    option http-server-close\n    http-reuse never\n")
	switch state.Affinity {
	case "cookie":
		fmt.Fprintf(&b, "    balance roundrobin\n    cookie %s insert indirect nocache\n", stickyCookieName(state))
	case "ip":
		b.WriteString("    balance source\n    hash-type consistent\n")
	default:
		b.WriteString("    balance roundrobin\n")
	}
	for i, server := range state.Servers {
		prefix := "s"
		if server.Backup {
			prefix = "b"
		}
		fmt.Fprintf(&b, "    server %s%d %s", prefix, i+1, server.Addr)
		if server.Weight > 0 {
			fmt.Fprintf(&b, " weight %d", server.Weight)
		}
		if server.Down {
			b.WriteString(" disabled")
		} else if server.Backup {
			b.WriteString(" backup")
		}
		if state.Affinity == "cookie" && !server.Backup {
			fmt.Fprintf(&b, " cookie s%d", i+1)
		}
		b.WriteString("\n")
	}
	return b.String()
}
