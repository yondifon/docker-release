package provider

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"

	"github.com/docker/docker/api/types"
	"github.com/malico/docker-release/internal/docker"
)

var validName = regexp.MustCompile(`^[a-zA-Z0-9._-]+$`)

func (u *UpstreamState) Validate() error {
	if !validName.MatchString(u.Service) {
		return fmt.Errorf("service name %q contains invalid characters", u.Service)
	}
	if name := u.ResolveUpstreamName(); !validName.MatchString(name) {
		return fmt.Errorf("upstream name %q contains invalid characters", name)
	}
	return nil
}

// matchesImage checks if image (e.g. "nginx:alpine") contains any of the expected keywords.
// Used in Reload to guard against reloading the wrong container.
func matchesImage(image string, keywords ...string) bool {
	img := strings.ToLower(image)
	for _, kw := range keywords {
		if strings.Contains(img, strings.ToLower(kw)) {
			return true
		}
	}
	return false
}

func stickyCookieName(state *UpstreamState) string {
	h := sha256.Sum256([]byte(state.ResolveUpstreamName()))
	return "_srr_" + hex.EncodeToString(h[:])[:10]
}

type Server struct {
	Addr   string // e.g. "172.18.0.5:80"
	Weight int    // 0 means no weight directive
	Down   bool   // marks server as draining
	Backup bool   // fallback-only; skipped when balancer uses hash (ip_hash/hash/random)
	Group  string // optional traffic group for weighted providers
}

// supportsNginxBackup reports whether nginx can emit the `backup` keyword.
// nginx forbids it with ip_hash/hash/random; our renderer maps both "ip" and "cookie" → ip_hash.
func supportsNginxBackup(affinity string) bool { return affinity == "" }

// supportsAngieBackup is like supportsNginxBackup but angie's "cookie" uses sticky cookie (not hash).
func supportsAngieBackup(affinity string) bool { return affinity == "" || affinity == "cookie" }

type UpstreamState struct {
	Service      string
	UpstreamName string // overrides Service for upstream naming (e.g. VIRTUAL_HOST for nginx-proxy)
	Servers      []Server
	Affinity     string // "ip" (default), "cookie", or "" (disabled)
	// cookie: nginx→ip_hash (OSS has no sticky), angie/caddy/traefik/haproxy→sticky cookie
	// ip: nginx/angie/nginx-proxy→ip_hash, traefik→hrw, caddy→ip_hash, haproxy→source
	Keepalive       int    // 0 disables keepalive
	StickyLearnName string // angie only: if set, use sticky learn with this app cookie name
}

func (u *UpstreamState) ResolveUpstreamName() string {
	if u.UpstreamName != "" {
		return u.UpstreamName
	}
	return u.Service
}

type Provider interface {
	GenerateConfig(ctx context.Context, state *UpstreamState) error
	Reload(ctx context.Context) error
}

// resolveProxyContainer finds the proxy container, trying running containers first.
// If not running, falls back to any state (stopped/exited) so the caller can start it.
func resolveProxyContainer(ctx context.Context, d *docker.Client, project, serviceName, imageKeyword string) (ctr types.Container, running bool, err error) {
	if serviceName != "" {
		ctr, err = d.FindContainerByServiceInProject(ctx, project, serviceName)
		if err == nil {
			return ctr, true, nil
		}
		ctr, err = d.FindAnyContainerByServiceInProject(ctx, project, serviceName)
	} else {
		ctr, err = d.FindContainerByImage(ctx, project, imageKeyword)
		if err == nil {
			return ctr, true, nil
		}
		ctr, err = d.FindAnyContainerByImage(ctx, project, imageKeyword)
	}
	if err != nil {
		return types.Container{}, false, err
	}
	return ctr, false, nil
}
