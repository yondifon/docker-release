package controller

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/malico/docker-release/internal/config"
	"github.com/malico/docker-release/internal/provider"
	"github.com/malico/docker-release/internal/state"
)

type proxyConfigDirs struct {
	upstreams string
	routes    string
}

func (c *Controller) cleanStaleProxyConfigs(ctx context.Context, configs map[string]*config.ServiceConfig) error {
	if c.project == "" {
		return nil
	}
	if c.proxyConfigs == nil {
		c.proxyConfigs = make(map[proxyConfigDirs]*config.ServiceConfig)
	}
	active := make(map[string]bool)
	for name, cfg := range configs {
		active[filepath.Base(name)] = true
		if dirs, transactional := transactionalProxyDirs(cfg); transactional {
			c.proxyConfigs[dirs] = cfg
		}
	}
	name := strings.ToLower(strings.TrimSpace(os.Getenv("DR_BUNDLED_PROXY")))
	if name == "nginx" || name == "angie" || name == "caddy" || name == "haproxy" {
		cfg, err := config.ParseLabels(map[string]string{"release.enable": "true", "release.provider": name})
		if err != nil {
			return err
		}
		dirs, _ := transactionalProxyDirs(cfg)
		c.proxyConfigs[dirs] = cfg
	}
	var syncErr error
	for _, cfg := range c.proxyConfigs {
		var p interface {
			RemoveStaleConfigs(context.Context, map[string]bool, func(string) bool) error
		}
		p = provider.NewNginx(cfg.NginxConfigDir, cfg.NginxRouteDir, provider.NginxRoute{}, c.docker, cfg.NginxService, c.project)
		if cfg.Provider == config.ProviderAngie {
			p = provider.NewAngieRoutes(cfg.AngieConfigDir, cfg.AngieRouteDir, provider.NginxRoute{}, c.docker, cfg.AngieService, c.project)
		} else if cfg.Provider == config.ProviderCaddy {
			p = provider.NewCaddyRoutes(cfg.CaddyConfigDir, provider.CaddyRoute{}, c.docker, cfg.CaddyService, c.project)
		} else if cfg.Provider == config.ProviderHAProxy {
			p = provider.NewHAProxyRoutes(cfg.HAProxyConfigDir, provider.HAProxyRoute{}, c.docker, cfg.HAProxyService, c.project)
		}
		opCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
		var releases []func()
		err := p.RemoveStaleConfigs(opCtx, active, func(service string) bool {
			release, err := c.managerFor(c.project).AcquireDeployLock(service)
			if errors.Is(err, state.ErrDeployLocked) {
				return false
			}
			if err != nil {
				syncErr = errors.Join(syncErr, err)
				return false
			}
			releases = append(releases, release)
			latest, err := c.docker.ListManagedContainers(opCtx, c.project)
			if err != nil {
				syncErr = errors.Join(syncErr, err)
				return false
			}
			for _, ctr := range latest {
				if ctr.Labels["com.docker.compose.service"] == service {
					return false
				}
			}
			return true
		})
		for _, release := range releases {
			release()
		}
		cancel()
		if err != nil {
			slog.Error("error removing stale proxy configs", "component", "controller", "err", err)
			syncErr = errors.Join(syncErr, err)
		}
	}
	return syncErr
}

func transactionalProxyDirs(cfg *config.ServiceConfig) (proxyConfigDirs, bool) {
	switch cfg.Provider {
	case config.ProviderNginx:
		return proxyConfigDirs{cfg.NginxConfigDir, cfg.NginxRouteDir}, true
	case config.ProviderAngie:
		return proxyConfigDirs{cfg.AngieConfigDir, cfg.AngieRouteDir}, cfg.AngieRouteDir != ""
	case config.ProviderCaddy:
		bundled := strings.EqualFold(strings.TrimSpace(os.Getenv("DR_BUNDLED_PROXY")), "caddy")
		return proxyConfigDirs{cfg.CaddyConfigDir, ""}, bundled
	case config.ProviderHAProxy:
		bundled := strings.EqualFold(strings.TrimSpace(os.Getenv("DR_BUNDLED_PROXY")), "haproxy")
		return proxyConfigDirs{cfg.HAProxyConfigDir, ""}, bundled
	default:
		return proxyConfigDirs{}, false
	}
}
