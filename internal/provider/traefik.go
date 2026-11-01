package provider

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
)

type TraefikProvider struct {
	configDir string
	bundled   bool
	route     NginxRoute
	pending   map[string][]byte
	apiURL    string
}

func NewTraefik(configDir string) *TraefikProvider {
	return &TraefikProvider{configDir: configDir}
}

func (p *TraefikProvider) GenerateConfig(ctx context.Context, state *UpstreamState) error {
	if err := state.Validate(); err != nil {
		return err
	}
	if p.bundled {
		return p.generateRoutes(state)
	}

	return writeConfigFile(filepath.Join(p.configDir, state.Service+".yml"), []byte(renderTraefikYAML(state)), "config")
}

func (p *TraefikProvider) Reload(ctx context.Context) error {
	if p.bundled {
		return p.applyRoutes(ctx, p.pending)
	}
	return nil
}

func renderTraefikYAML(state *UpstreamState) string {
	var b strings.Builder

	b.WriteString(generatedHeader)
	b.WriteString("http:\n")

	if hasWeightedServers(state) {
		renderWeightedService(&b, state)
	} else {
		renderLoadBalancerService(&b, state)
	}

	return b.String()
}

func hasWeightedServers(state *UpstreamState) bool {
	for _, s := range state.Servers {
		if s.Weight > 0 {
			return true
		}
	}
	return false
}

func renderLoadBalancerService(b *strings.Builder, state *UpstreamState) {
	b.WriteString("  services:\n")
	fmt.Fprintf(b, "    %s:\n", state.Service)
	b.WriteString("      loadBalancer:\n")

	if state.Affinity == "cookie" || state.Affinity == "ip" {
		writeTraefikStickyCookie(b, stickyCookieName(state))
	}

	b.WriteString("        servers:\n")
	for _, s := range state.Servers {
		if s.Down || s.Backup {
			continue
		}
		fmt.Fprintf(b, "          - url: \"http://%s\"\n", s.Addr)
	}
}

func renderWeightedService(b *strings.Builder, state *UpstreamState) {
	b.WriteString("  services:\n")

	stableName := state.Service + "-stable"
	canaryName := state.Service + "-canary"

	fmt.Fprintf(b, "    %s:\n", state.Service)
	if state.Affinity == "ip" {
		b.WriteString("      highestRandomWeight:\n")
	} else {
		b.WriteString("      weighted:\n")
	}
	b.WriteString("        services:\n")

	var stableServers, canaryServers []Server
	stableWeight := -1
	canaryWeight := -1

	for _, s := range state.Servers {
		if s.Down || s.Backup {
			continue
		}

		if s.Group == "stable" {
			stableWeight = s.Weight
			stableServers = append(stableServers, s)
			continue
		}

		if s.Group == "canary" {
			canaryWeight = s.Weight
			canaryServers = append(canaryServers, s)
			continue
		}

		if stableWeight == -1 {
			stableWeight = s.Weight
			stableServers = append(stableServers, s)
			continue
		}

		if s.Weight == stableWeight {
			stableServers = append(stableServers, s)
			continue
		}

		if canaryWeight == -1 {
			canaryWeight = s.Weight
			canaryServers = append(canaryServers, s)
			continue
		}

		if s.Weight == canaryWeight {
			canaryServers = append(canaryServers, s)
		}
	}

	if stableWeight == -1 {
		stableWeight = 100
	}
	if canaryWeight == -1 {
		canaryWeight = 0
	}

	if stableWeight > 0 {
		fmt.Fprintf(b, "          - name: %s\n", stableName)
		fmt.Fprintf(b, "            weight: %d\n", stableWeight)
	}
	if len(canaryServers) > 0 && canaryWeight > 0 {
		fmt.Fprintf(b, "          - name: %s\n", canaryName)
		fmt.Fprintf(b, "            weight: %d\n", canaryWeight)
	}

	if state.Affinity == "cookie" {
		writeTraefikStickyCookie(b, stickyCookieName(state))
	}

	fmt.Fprintf(b, "    %s:\n", stableName)
	b.WriteString("      loadBalancer:\n")
	b.WriteString("        servers:\n")
	for _, s := range stableServers {
		fmt.Fprintf(b, "          - url: \"http://%s\"\n", s.Addr)
	}

	if len(canaryServers) > 0 {
		fmt.Fprintf(b, "    %s:\n", canaryName)
		b.WriteString("      loadBalancer:\n")
		b.WriteString("        servers:\n")
		for _, s := range canaryServers {
			fmt.Fprintf(b, "          - url: \"http://%s\"\n", s.Addr)
		}
	}
}

func writeTraefikStickyCookie(b *strings.Builder, name string) {
	b.WriteString("        sticky:\n")
	b.WriteString("          cookie:\n")
	fmt.Fprintf(b, "            name: %q\n", name)
}
