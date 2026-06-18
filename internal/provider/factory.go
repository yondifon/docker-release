package provider

import (
	"fmt"
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
	switch cfg.Provider {
	case config.ProviderNginx:
		return NewNginx(cfg.NginxConfigDir, cfg.NginxRouteDir, cfg.NginxPath, f.docker, cfg.NginxService, f.project), nil
	case config.ProviderAngie:
		return NewAngie(cfg.AngieConfigDir, f.docker, cfg.AngieService, f.project), nil
	case config.ProviderTraefik:
		return NewTraefik(cfg.TraefikConfigDir), nil
	case config.ProviderNginxProxy:
		return f.nginxProxyProvider(cfg)
	case config.ProviderCaddy:
		return NewCaddy(cfg.CaddyConfigDir, f.docker, cfg.CaddyService, f.project), nil
	case config.ProviderHAProxy:
		return NewHAProxy(cfg.HAProxyConfigDir, f.docker, cfg.HAProxyService, f.project), nil
	case config.ProviderNone:
		return NewNoop(), nil
	default:
		return nil, fmt.Errorf("unknown provider %q", cfg.Provider)
	}
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
