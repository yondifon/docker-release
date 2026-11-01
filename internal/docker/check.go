package docker

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/pkg/stdcopy"
)

// maxOutput is how much of a command's output is kept: the end of it, where
// a failing script says why.
const maxOutput = 8 << 10

// CommandResult is what a finished command printed and how it exited.
type CommandResult struct {
	Output   string
	ExitCode int
}

// tailBuffer keeps the last maxOutput bytes written to it.
type tailBuffer struct{ buf []byte }

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.buf = append(t.buf, p...)
	if len(t.buf) > maxOutput {
		t.buf = t.buf[len(t.buf)-maxOutput:]
	}
	return len(p), nil
}

func (t *tailBuffer) String() string { return strings.TrimSpace(string(t.buf)) }

// ExecShell runs `sh -c script` inside a running container with the extra env
// and returns its output and exit code. When ctx ends first, it returns what
// was printed so far and the context error.
func (c *Client) ExecShell(ctx context.Context, containerID, script string, env []string) (CommandResult, error) {
	created, err := c.api.ContainerExecCreate(ctx, containerID, container.ExecOptions{
		Cmd:          []string{"sh", "-c", script},
		Env:          env,
		AttachStdout: true,
		AttachStderr: true,
	})
	if err != nil {
		return CommandResult{}, fmt.Errorf("exec create: %w", err)
	}
	attached, err := c.api.ContainerExecAttach(ctx, created.ID, container.ExecAttachOptions{})
	if err != nil {
		return CommandResult{}, fmt.Errorf("exec start: %w", err)
	}
	defer attached.Close()

	var out tailBuffer
	copied := make(chan error, 1)
	go func() {
		_, err := stdcopy.StdCopy(&out, &out, attached.Reader)
		copied <- err
	}()
	select {
	case err := <-copied:
		if err != nil && err != io.EOF {
			return CommandResult{Output: out.String()}, fmt.Errorf("reading exec output: %w", err)
		}
	case <-ctx.Done():
		attached.Close()
		<-copied
		return CommandResult{Output: out.String()}, ctx.Err()
	}

	// The output closes just before the exit code is recorded.
	for {
		inspect, err := c.api.ContainerExecInspect(ctx, created.ID)
		if err != nil {
			return CommandResult{Output: out.String()}, fmt.Errorf("exec inspect: %w", err)
		}
		if !inspect.Running {
			return CommandResult{Output: out.String(), ExitCode: inspect.ExitCode}, nil
		}
		select {
		case <-ctx.Done():
			return CommandResult{Output: out.String()}, ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// RunOneOff runs `sh -c script` in a new container made from ref, a container
// of the Compose service that should run it. The new container joins the
// networks of joinOf, has the extra env, and is removed when it ends. When ctx
// ends first, it returns what was printed so far and the context error.
func (c *Client) RunOneOff(ctx context.Context, ref types.Container, joinOf, script string, env []string) (CommandResult, error) {
	refInfo, err := c.api.ContainerInspect(ctx, ref.ID)
	if err != nil {
		return CommandResult{}, fmt.Errorf("inspecting %s: %w", ref.Labels["com.docker.compose.service"], err)
	}
	target, err := c.api.ContainerInspect(ctx, joinOf)
	if err != nil {
		return CommandResult{}, fmt.Errorf("inspecting container: %w", err)
	}
	endpoints := cloneEndpoints(target)
	if len(endpoints) == 0 {
		return CommandResult{}, fmt.Errorf("container %s is not on a network a check can join", joinOf[:12])
	}
	for _, endpoint := range endpoints {
		endpoint.Aliases = nil
	}
	primary := sortedKeys(endpoints)[0]

	cfg := oneOffConfig(refInfo, script, env)
	hostCfg := oneOffHostConfig(refInfo, primary)
	created, err := c.api.ContainerCreate(ctx, cfg, hostCfg, &network.NetworkingConfig{
		EndpointsConfig: map[string]*network.EndpointSettings{primary: endpoints[primary]},
	}, nil, "")
	if err != nil {
		return CommandResult{}, fmt.Errorf("creating check container: %w", err)
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = c.api.ContainerRemove(cleanup, created.ID, container.RemoveOptions{Force: true})
	}()

	delete(endpoints, primary)
	for _, name := range sortedKeys(endpoints) {
		if err := c.api.NetworkConnect(ctx, endpoints[name].NetworkID, created.ID, endpoints[name]); err != nil {
			return CommandResult{}, fmt.Errorf("connecting check container to network %s: %w", name, err)
		}
	}
	if err := c.api.ContainerStart(ctx, created.ID, container.StartOptions{}); err != nil {
		return CommandResult{}, fmt.Errorf("starting check container: %w", err)
	}

	exited, errs := c.api.ContainerWait(ctx, created.ID, container.WaitConditionNotRunning)
	var waitErr error
	var exitCode int
	select {
	case res := <-exited:
		exitCode = int(res.StatusCode)
	case waitErr = <-errs:
	}

	// Read the output on a fresh context: ctx may have ended, and a timed-out
	// check still needs to show what it printed.
	logCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	output := c.containerOutput(logCtx, created.ID)
	if waitErr != nil {
		return CommandResult{Output: output}, waitErr
	}
	return CommandResult{Output: output, ExitCode: exitCode}, nil
}

func (c *Client) containerOutput(ctx context.Context, id string) string {
	logs, err := c.api.ContainerLogs(ctx, id, container.LogsOptions{ShowStdout: true, ShowStderr: true})
	if err != nil {
		return ""
	}
	defer logs.Close()
	var out tailBuffer
	_, _ = stdcopy.StdCopy(&out, &out, logs)
	return out.String()
}

// oneOffConfig is ref's container config with the entrypoint replaced to run
// the script, and the labels and ports that would make it look like a replica.
func oneOffConfig(ref types.ContainerJSON, script string, env []string) *container.Config {
	cfg := *ref.Config
	cfg.Labels = make(map[string]string, len(ref.Config.Labels)+1)
	for k, v := range ref.Config.Labels {
		if !strings.HasPrefix(k, "release.") {
			cfg.Labels[k] = v
		}
	}
	cfg.Labels["com.docker.compose.oneoff"] = "True"
	cfg.Entrypoint = []string{"sh", "-c"}
	cfg.Cmd = []string{script}
	cfg.Env = append(append([]string(nil), cfg.Env...), env...)
	cfg.Healthcheck = &container.HealthConfig{Test: []string{"NONE"}}
	cfg.Hostname = ""
	cfg.Tty, cfg.OpenStdin = false, false
	cfg.ExposedPorts = nil
	return &cfg
}

// oneOffHostConfig keeps the mounts, capabilities and limits ref has, and joins
// the given network. It drops published ports and the restart policy.
func oneOffHostConfig(ref types.ContainerJSON, networkName string) *container.HostConfig {
	return &container.HostConfig{
		Binds:       ref.HostConfig.Binds,
		Mounts:      ref.HostConfig.Mounts,
		Tmpfs:       ref.HostConfig.Tmpfs,
		NetworkMode: container.NetworkMode(networkName),
		CapAdd:      ref.HostConfig.CapAdd,
		CapDrop:     ref.HostConfig.CapDrop,
		ExtraHosts:  ref.HostConfig.ExtraHosts,
		Resources:   ref.HostConfig.Resources,
	}
}
