package controller

import (
	"context"

	"github.com/malico/docker-release/internal/config"
	"github.com/malico/docker-release/internal/docker"
)

// proxyAddrs resolves upstream addresses on a network the proxy is also on, so
// a container on several networks is never given an address the proxy can't reach.
type proxyAddrs struct {
	docker  *docker.Client
	proxyID string
}

func (c *Controller) addrResolver(ctx context.Context, cfg *config.ServiceConfig) proxyAddrs {
	return proxyAddrs{docker: c.docker, proxyID: c.factory.ProxyContainerID(ctx, cfg)}
}

func (p proxyAddrs) ContainerAddr(ctx context.Context, containerID string) (string, error) {
	return p.docker.ContainerAddrVia(ctx, containerID, p.proxyID)
}
