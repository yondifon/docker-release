package controller

import (
	"fmt"

	"github.com/docker/docker/api/types"
	"github.com/malico/docker-release/internal/config"
)

// checkPreviewHost refuses a preview host that another service already uses,
// as its host or as its own preview host.
func checkPreviewHost(key serviceKey, cfg *config.ServiceConfig, containers []types.Container) error {
	if cfg.PreviewHost == "" {
		return nil
	}
	for other, group := range groupContainersByService(containers) {
		if other.service == key.service || len(group) == 0 {
			continue
		}
		otherCfg, err := config.ParseLabels(group[0].Labels)
		if err != nil {
			continue
		}
		if otherCfg.UsesHost(cfg.PreviewHost) {
			return fmt.Errorf("release.preview.host %q is already used by service %q; use a different host", cfg.PreviewHost, other.service)
		}
	}
	return nil
}
