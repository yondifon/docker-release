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

type nginxConfigDirs struct {
	upstreams string
	routes    string
}

func (c *Controller) cleanStaleNginxConfigs(ctx context.Context, configs map[string]*config.ServiceConfig) error {
	if c.project == "" {
		return nil
	}
	if c.nginxConfigs == nil {
		c.nginxConfigs = make(map[nginxConfigDirs]*config.ServiceConfig)
	}
	active := make(map[string]bool)
	for name, cfg := range configs {
		active[filepath.Base(name)] = true
		if cfg.Provider == config.ProviderNginx {
			c.nginxConfigs[nginxConfigDirs{cfg.NginxConfigDir, cfg.NginxRouteDir}] = cfg
		} else if cfg.Provider == config.ProviderAngie && cfg.AngieRouteDir != "" {
			c.nginxConfigs[nginxConfigDirs{cfg.AngieConfigDir, cfg.AngieRouteDir}] = cfg
		}
	}
	name := strings.ToLower(strings.TrimSpace(os.Getenv("DR_BUNDLED_PROXY")))
	if name == "nginx" || name == "angie" {
		cfg, err := config.ParseLabels(map[string]string{"release.enable": "true", "release.provider": name})
		if err != nil {
			return err
		}
		dirs := nginxConfigDirs{cfg.NginxConfigDir, cfg.NginxRouteDir}
		if name == "angie" {
			dirs = nginxConfigDirs{cfg.AngieConfigDir, cfg.AngieRouteDir}
		}
		c.nginxConfigs[dirs] = cfg
	}
	var syncErr error
	for _, cfg := range c.nginxConfigs {
		p := provider.NewNginx(cfg.NginxConfigDir, cfg.NginxRouteDir, provider.NginxRoute{}, c.docker, cfg.NginxService, c.project)
		if cfg.Provider == config.ProviderAngie {
			p = provider.NewAngieRoutes(cfg.AngieConfigDir, cfg.AngieRouteDir, provider.NginxRoute{}, c.docker, cfg.AngieService, c.project)
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
			slog.Error("error removing stale nginx configs", "component", "controller", "err", err)
			syncErr = errors.Join(syncErr, err)
		}
	}
	return syncErr
}
