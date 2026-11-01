package controller

import (
	"context"
	"fmt"
	"strings"

	"github.com/malico/docker-release/internal/config"
	"github.com/malico/docker-release/internal/docker"
	"github.com/malico/docker-release/internal/strategy"
)

// checkRunner runs `release.check.command` for one service.
type checkRunner struct {
	docker *docker.Client
	key    serviceKey
	cfg    *config.ServiceConfig
}

// RunCheck runs the command inside the new container, or, with
// `release.check.service`, in a one-off container of that Compose service on
// the new container's network.
func (r checkRunner) RunCheck(ctx context.Context, target strategy.ContainerInfo, previewToken string) (strategy.CheckResult, error) {
	check := r.cfg.Check
	env := []string{
		"RELEASE_TARGET=http://" + target.Addr,
		"RELEASE_HOST=" + firstHost(r.cfg.RouteHost),
	}

	if r.cfg.PreviewHost != "" {
		env = append(env, "RELEASE_PREVIEW_URL="+r.cfg.Origin(r.cfg.PreviewHost))
	}
	if previewToken != "" {
		env = append(env, "RELEASE_PREVIEW_TOKEN="+previewToken)
	}

	var result docker.CommandResult
	var err error
	if check.Service == "" {
		result, err = r.docker.ExecShell(ctx, target.ID, check.Command, env)
	} else {
		ref, findErr := r.docker.FindAnyContainerByServiceInProject(ctx, r.key.project, check.Service)
		if findErr != nil {
			return strategy.CheckResult{}, fmt.Errorf("no container of service %q exists to copy; create it with `docker compose create %s`", check.Service, check.Service)
		}
		result, err = r.docker.RunOneOff(ctx, ref, target.ID, check.Command, env)
	}
	return strategy.CheckResult{Output: result.Output, ExitCode: result.ExitCode}, err
}

// firstHost returns the first host of `release.host`, which can list several.
func firstHost(hosts string) string {
	first, _, _ := strings.Cut(hosts, ",")
	return strings.TrimSpace(first)
}
