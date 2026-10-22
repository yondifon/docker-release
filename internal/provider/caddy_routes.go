package provider

import (
	"context"
	"fmt"
	"strings"

	"github.com/malico/docker-release/internal/docker"
)

type CaddyRoute struct {
	Host        string
	Path        string
	TLSCert     string
	TLSKey      string
	TLSRedirect bool
}

func NewCaddyRoutes(configDir string, route CaddyRoute, dockerClient *docker.Client, serviceName, project string) *CaddyProvider {
	p := NewCaddy(configDir, dockerClient, serviceName, project)
	p.bundled = true
	p.route = route
	return p
}

func renderCaddySites(state *UpstreamState, route CaddyRoute) string {
	if route.Host == "" {
		return ""
	}
	var httpHosts, httpsHosts []string
	for _, host := range strings.Split(route.Host, ",") {
		host = strings.TrimSpace(host)
		httpHosts = append(httpHosts, "http://"+host)
		httpsHosts = append(httpsHosts, "https://"+host)
	}
	var b strings.Builder
	if route.TLSRedirect {
		fmt.Fprintf(&b, "\n%s {\n    redir https://{host}{uri} 308\n}\n", strings.Join(httpHosts, ", "))
	} else {
		httpsHosts = append(httpHosts, httpsHosts...)
	}
	fmt.Fprintf(&b, "\n%s {\n", strings.Join(httpsHosts, ", "))
	if route.TLSCert != "" {
		fmt.Fprintf(&b, "    tls %s %s\n", route.TLSCert, route.TLSKey)
	}
	b.WriteString(renderCaddyHandler(state, route.Path))
	b.WriteString("}\n")
	return b.String()
}

func renderCaddyHandler(state *UpstreamState, path string) string {
	if path == "" || path == "/" {
		return fmt.Sprintf("    import %s_upstream\n", state.ResolveUpstreamName())
	}
	return fmt.Sprintf("    handle_path %s* {\n        import %s_upstream\n    }\n", path, state.ResolveUpstreamName())
}

func (p *CaddyProvider) RemoveStaleConfigs(ctx context.Context, active map[string]bool, canRemove func(string) bool) error {
	unlock, err := lockProxyConfigs(ctx, p.configDir, ".caddy.lock")
	if err != nil {
		return err
	}
	defer unlock()
	files, err := proxyConfigFilesByService(map[string][]string{p.configDir: {".caddy", ".location"}})
	if err != nil {
		return err
	}
	changes := make(map[string][]byte)
	for service, paths := range files {
		if active[service] || !canRemove(service) {
			continue
		}
		for _, path := range paths {
			changes[path] = nil
		}
	}
	if len(changes) == 0 {
		return nil
	}
	return applyProxyConfigFiles(ctx, changes, p.reload)
}
