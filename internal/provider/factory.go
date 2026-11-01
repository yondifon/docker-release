package provider

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/malico/docker-release/internal/config"
	"github.com/malico/docker-release/internal/docker"
)

// Factory builds providers from a service config. It caches nginx-proxy
// providers per template path, since that provider holds shared template state
// that must be reused across services writing to the same template.
type Factory struct {
	docker  *docker.Client
	project string

	mu         sync.Mutex
	nginxProxy map[string]*NginxProxyProvider
}

func NewFactory(dockerClient *docker.Client, project string) *Factory {
	return &Factory{
		docker:     dockerClient,
		project:    project,
		nginxProxy: make(map[string]*NginxProxyProvider),
	}
}

// Provider returns the provider for a service config. nginx-proxy template-load
// failures are returned as errors (not silently degraded to no-op) so a
// misconfigured template fails the deployment loudly.
func (f *Factory) Provider(cfg *config.ServiceConfig) (Provider, error) {
	route := NginxRoute{
		Host:        cfg.RouteHost,
		Path:        cfg.RoutePath,
		SSLCert:     cfg.TLSCert,
		SSLKey:      cfg.TLSKey,
		SSLRedirect: cfg.TLSRedirect,
	}
	switch cfg.Provider {
	case config.ProviderNginx:
		return NewNginx(cfg.NginxConfigDir, cfg.NginxRouteDir, route, f.docker, cfg.NginxService, f.project), nil
	case config.ProviderAngie:
		if cfg.AngieRouteDir != "" {
			return NewAngieRoutes(cfg.AngieConfigDir, cfg.AngieRouteDir, route, f.docker, cfg.AngieService, f.project), nil
		}
		return NewAngie(cfg.AngieConfigDir, f.docker, cfg.AngieService, f.project), nil
	case config.ProviderTraefik:
		if strings.EqualFold(strings.TrimSpace(os.Getenv("DR_BUNDLED_PROXY")), "traefik") {
			return NewTraefikRoutes(cfg.TraefikConfigDir, route), nil
		}
		return NewTraefik(cfg.TraefikConfigDir), nil
	case config.ProviderNginxProxy:
		return f.nginxProxyProvider(cfg)
	case config.ProviderCaddy:
		if strings.EqualFold(strings.TrimSpace(os.Getenv("DR_BUNDLED_PROXY")), "caddy") {
			return NewCaddyRoutes(cfg.CaddyConfigDir, CaddyRoute{
				Host:        cfg.RouteHost,
				Path:        cfg.RoutePath,
				TLSCert:     cfg.TLSCert,
				TLSKey:      cfg.TLSKey,
				TLSRedirect: cfg.TLSRedirect,
				Keepalive:   cfg.CaddyKeepalive,
				Affinity:    cfg.Affinity,
			}, f.docker, cfg.CaddyService, f.project), nil
		}
		return NewCaddy(cfg.CaddyConfigDir, f.docker, cfg.CaddyService, f.project), nil
	case config.ProviderHAProxy:
		if strings.EqualFold(strings.TrimSpace(os.Getenv("DR_BUNDLED_PROXY")), "haproxy") {
			return NewHAProxyRoutes(cfg.HAProxyConfigDir, HAProxyRoute{
				Host:        cfg.RouteHost,
				Path:        cfg.RoutePath,
				TLSCert:     cfg.TLSCert,
				TLSKey:      cfg.TLSKey,
				TLSRedirect: cfg.TLSRedirect,
				Affinity:    cfg.Affinity,
			}, f.docker, cfg.HAProxyService, f.project), nil
		}
		return NewHAProxy(cfg.HAProxyConfigDir, f.docker, cfg.HAProxyService, f.project), nil
	case config.ProviderNone:
		return NewNoop(), nil
	default:
		return nil, fmt.Errorf("unknown provider %q", cfg.Provider)
	}
}

// ProxyContainerID returns the running container that serves traffic for the
// service's provider, found the same way the provider finds it to reload. It
// returns "" when there is none to find (no proxy, or one that is not running).
func (f *Factory) ProxyContainerID(ctx context.Context, cfg *config.ServiceConfig) string {
	var service, image string
	switch cfg.Provider {
	case config.ProviderNginx:
		service, image = cfg.NginxService, "nginx"
	case config.ProviderAngie:
		service, image = cfg.AngieService, "angie"
	case config.ProviderCaddy:
		service, image = cfg.CaddyService, "caddy"
	case config.ProviderHAProxy:
		service, image = cfg.HAProxyService, "haproxy"
	case config.ProviderTraefik:
		image = "traefik"
	case config.ProviderNginxProxy:
		image = "nginx-proxy"
	default:
		return ""
	}

	ctr, running, err := resolveProxyContainer(ctx, f.docker, f.project, service, image)
	if err != nil || !running {
		return ""
	}
	return ctr.ID
}

func (f *Factory) nginxProxyProvider(cfg *config.ServiceConfig) (Provider, error) {
	tmplPath := cfg.NginxConfigDir + "/nginx.tmpl"

	f.mu.Lock()
	defer f.mu.Unlock()

	if prov, ok := f.nginxProxy[tmplPath]; ok {
		return prov, nil
	}

	prov, err := NewNginxProxy(tmplPath)
	if err != nil {
		return nil, fmt.Errorf("loading nginx-proxy template at %s: %w", tmplPath, err)
	}

	f.nginxProxy[tmplPath] = prov
	return prov, nil
}

// ConfigExt returns the per-service config file extension a provider writes, and
// whether it writes per-service files at all. nginx-proxy (single template) and
// none (nothing) return false, so stale-config cleanup skips them.
func ConfigExt(p config.ProviderType) (ext string, perService bool) {
	switch p {
	case config.ProviderNginx, config.ProviderAngie:
		return ".conf", true
	case config.ProviderTraefik:
		return ".yml", true
	case config.ProviderCaddy:
		return ".caddy", true
	case config.ProviderHAProxy:
		return ".cfg", true
	default:
		return "", false
	}
}
