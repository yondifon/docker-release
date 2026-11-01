package config

import (
	"fmt"
	"slices"
	"strings"
)

// HostList splits a `release.host` value into lower-case hosts.
func HostList(hosts string) []string {
	var list []string
	for _, host := range strings.Split(hosts, ",") {
		if host = strings.ToLower(strings.TrimSpace(host)); host != "" {
			list = append(list, host)
		}
	}
	return list
}

// UsesHost reports whether `release.host` or `release.preview.host` names host.
func (c *ServiceConfig) UsesHost(host string) bool {
	host = strings.ToLower(host)
	for _, own := range append(HostList(c.RouteHost), strings.ToLower(c.PreviewHost)) {
		if own == host {
			return true
		}
	}
	return false
}

func (c *ServiceConfig) validatePreviewHost() error {
	if c.PreviewHost == "" {
		return nil
	}
	if c.Strategy != StrategyBlueGreen {
		return fmt.Errorf("release.preview.host works with release.strategy=blue-green only")
	}
	if c.Provider == ProviderNginxProxy {
		return fmt.Errorf("release.preview.host does not work with the nginx-proxy provider, because nginx-proxy takes its hosts from VIRTUAL_HOST; use a docker-release image with the proxy bundled, or another provider")
	}
	if strings.ContainsAny(c.PreviewHost, ", *") || !validRouteHost.MatchString(c.PreviewHost) || strings.Contains(c.PreviewHost, "..") {
		return fmt.Errorf("release.preview.host %q must be one hostname, without wildcards", c.PreviewHost)
	}
	if slices.Contains(HostList(c.RouteHost), strings.ToLower(c.PreviewHost)) {
		return fmt.Errorf("release.preview.host %q is the same as release.host; use a different host", c.PreviewHost)
	}
	return nil
}
