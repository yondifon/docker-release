package provider

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func (p *NginxProvider) applyConfigs(ctx context.Context, changes map[string][]byte) error {
	unlock, err := lockProxyConfigs(ctx, p.configDir, ".nginx.lock")
	if err != nil {
		return err
	}
	defer unlock()
	return p.applyConfigsLocked(ctx, changes)
}

func (p *NginxProvider) applyConfigsLocked(ctx context.Context, changes map[string][]byte) error {
	if err := p.checkRouteConflicts(changes); err != nil {
		return err
	}
	return applyProxyConfigFiles(ctx, changes, p.reload)
}

func (p *NginxProvider) checkRouteConflicts(changes map[string][]byte) error {
	hosts := make(map[string]bool)
	for path, data := range changes {
		if strings.HasSuffix(path, ".server") {
			for _, host := range nginxServerNames(data) {
				hosts[host] = true
			}
		}
	}
	if len(hosts) == 0 || p.routeDir == "" {
		return nil
	}
	entries, err := os.ReadDir(p.routeDir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		path := filepath.Join(p.routeDir, entry.Name())
		if _, changing := changes[path]; changing || entry.IsDir() || !strings.HasSuffix(path, ".server") {
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, host := range nginxServerNames(data) {
			if hosts[host] {
				return fmt.Errorf("nginx host %q is already used by %s; use a different host or a custom server block", host, path)
			}
		}
	}
	return nil
}

func nginxServerNames(data []byte) []string {
	var hosts []string
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(strings.TrimSuffix(strings.TrimSpace(line), ";"))
		if len(fields) > 1 && fields[0] == "server_name" {
			for _, host := range fields[1:] {
				hosts = append(hosts, strings.ToLower(host))
			}
		}
	}
	return hosts
}

func (p *NginxProvider) RemoveStaleConfigs(ctx context.Context, active map[string]bool, canRemove func(string) bool) error {
	unlock, err := lockProxyConfigs(ctx, p.configDir, ".nginx.lock")
	if err != nil {
		return err
	}
	defer unlock()
	changes := make(map[string][]byte)
	files, err := p.configFilesByService()
	if err != nil {
		return err
	}
	for name, paths := range files {
		if active[name] || !canRemove(name) {
			continue
		}
		for _, path := range paths {
			changes[path] = nil
		}
	}
	if len(changes) == 0 {
		return nil
	}
	return p.applyConfigsLocked(ctx, changes)
}

func (p *NginxProvider) configFilesByService() (map[string][]string, error) {
	dirs := map[string][]string{p.configDir: {".conf"}}
	dirs[p.routeDir] = append(dirs[p.routeDir], ".server", ".location")
	return proxyConfigFilesByService(dirs)
}
