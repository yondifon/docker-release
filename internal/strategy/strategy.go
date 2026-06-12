package strategy

import (
	"context"

	"github.com/malico/docker-release/internal/config"
	"github.com/malico/docker-release/internal/provider"
	"github.com/malico/docker-release/internal/state"
)

type ContainerInfo struct {
	ID   string
	Addr string
}

type Deployment struct {
	Service string
	Config  *config.ServiceConfig
	Old     []ContainerInfo
	New     []ContainerInfo

	// DeployID identifies this deployment; the controller generates it and uses
	// the same value for its in-memory tracking map and the persisted state, so
	// the API can cancel by the ID it reports. PrevDeployID is the previous
	// deployment's ID, captured before this one overwrote the state file.
	DeployID     string
	PrevDeployID string
}

// resolveDeployID returns the controller-supplied ID, generating one only when
// a strategy is driven without one (e.g. direct use in tests).
func (d *Deployment) resolveDeployID() string {
	if d.DeployID != "" {
		return d.DeployID
	}
	return state.GenerateDeploymentID()
}

func (d *Deployment) UpstreamName() string {
	if d.Config != nil && d.Config.UpstreamName != "" {
		return d.Config.UpstreamName
	}
	return d.Service
}

// ApplyProviderSettings sets provider-specific upstream fields (keepalive and
// angie sticky-learn) so every config generated mid-deployment matches what the
// controller's steady-state refresh produces. This is the single source of
// truth; the controller calls it too.
func ApplyProviderSettings(cfg *config.ServiceConfig, upstream *provider.UpstreamState) {
	if cfg == nil || upstream == nil {
		return
	}

	switch cfg.Provider {
	case config.ProviderNginx, config.ProviderNginxProxy:
		upstream.Keepalive = cfg.ResolveNginxKeepalive(len(upstream.Servers))
	case config.ProviderAngie:
		upstream.Keepalive = cfg.ResolveAngieKeepalive(len(upstream.Servers))
		upstream.StickyLearnName = cfg.AngieStickyLearnName
	case config.ProviderCaddy:
		upstream.Keepalive = cfg.ResolveCaddyKeepalive(len(upstream.Servers))
	}
}

type Strategy interface {
	Execute(ctx context.Context, d *Deployment) error
	Rollback(ctx context.Context, d *Deployment) error
}

// New returns the strategy named by the config, defaulting to linear.
func New(cfg *config.ServiceConfig, docker DockerOps, prov provider.Provider, stateMgr *state.Manager) Strategy {
	switch cfg.Strategy {
	case config.StrategyBlueGreen:
		return NewBlueGreen(docker, prov, stateMgr)
	case config.StrategyCanary:
		return NewCanary(docker, prov, stateMgr)
	default:
		return NewLinear(docker, prov, stateMgr)
	}
}
