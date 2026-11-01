package config

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// The names a bundled proxy uses for the preview token.
const (
	PreviewCookie     = "release_preview"   // set by the link, read on every request
	PreviewHeader     = "X-Release-Preview" // for scripts
	PreviewQueryParam = "release-preview"   // the link: `/?release-preview=<token>`
)

// PreviewTokenAuto asks for a new random token on every release.
const PreviewTokenAuto = "auto"

// A fixed token goes into proxy config files, cookies and URLs as it is, so it
// uses only characters that need no escaping anywhere.
var validPreviewToken = regexp.MustCompile(`^[A-Za-z0-9_-]{16,128}$`)

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

func (c *ServiceConfig) validatePreviewToken() error {
	if c.PreviewToken == "" {
		return nil
	}
	if c.Strategy != StrategyBlueGreen {
		return fmt.Errorf("release.preview.token works with release.strategy=blue-green only")
	}
	if c.Provider == ProviderNginxProxy {
		return fmt.Errorf("release.preview.token does not work with the nginx-proxy provider; use a docker-release image with the proxy bundled")
	}
	if bundled := BundledProxy(); bundled == "" || bundled == "none" || bundled != string(c.Provider) {
		return fmt.Errorf("release.preview.token needs a docker-release image with the proxy bundled and release.provider set to that proxy; with a separate proxy, use release.preview.host instead")
	}
	if c.PreviewToken != PreviewTokenAuto && !validPreviewToken.MatchString(c.PreviewToken) {
		return fmt.Errorf("release.preview.token must be %q, or a value of 16 to 128 letters, digits, `-` and `_`", PreviewTokenAuto)
	}
	return nil
}

// Origin is `https://host` when the service has a certificate and `http://host`
// otherwise.
func (c *ServiceConfig) Origin(host string) string {
	if c.TLSCert != "" {
		return "https://" + host
	}
	return "http://" + host
}

// PreviewBase is the address the preview link starts from: the first
// `release.host` and `release.path`. It is empty when the service has no
// host, or only a wildcard, to put the link on.
func (c *ServiceConfig) PreviewBase() string {
	host, _, _ := strings.Cut(c.RouteHost, ",")
	host = strings.TrimSpace(host)
	if host == "" || strings.Contains(host, "*") {
		return ""
	}
	path := c.RoutePath
	if !strings.HasSuffix(path, "/") {
		path += "/"
	}
	return c.Origin(host) + path
}

// HasPreview reports whether a blue/green release lets the new containers
// answer anywhere before users move.
func (c *ServiceConfig) HasPreview() bool {
	return c.PreviewHost != "" || c.PreviewToken != ""
}
