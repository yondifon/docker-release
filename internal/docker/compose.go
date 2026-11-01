package docker

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

const (
	composeProjectLabel     = "com.docker.compose.project"
	composeWorkingDirLabel  = "com.docker.compose.project.working_dir"
	composeConfigFilesLabel = "com.docker.compose.project.config_files"
	composeEnvFileLabel     = "com.docker.compose.project.environment_file"
)

// ComposeProject is where a service's Compose project lives, read from the
// labels Compose puts on every container it creates. The paths are host paths.
type ComposeProject struct {
	Name        string
	WorkingDir  string
	ConfigFiles []string
	EnvFiles    []string
}

// ComposeProjectOf reads the project from container labels. It reports false
// when the labels do not name a project directory and at least one file.
func ComposeProjectOf(labels map[string]string) (ComposeProject, bool) {
	p := ComposeProject{
		Name:        labels[composeProjectLabel],
		WorkingDir:  labels[composeWorkingDirLabel],
		ConfigFiles: splitList(labels[composeConfigFilesLabel]),
		EnvFiles:    splitList(labels[composeEnvFileLabel]),
	}
	return p, p.Name != "" && p.WorkingDir != "" && len(p.ConfigFiles) > 0
}

func splitList(value string) []string {
	var out []string
	for _, part := range strings.Split(value, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

// Available checks that Compose can run from here: the CLI is installed and
// this process sees the project the way the host does, with the directory,
// every Compose file and every env file at the same paths.
func (p ComposeProject) Available() error {
	if _, err := composeCommand(); err != nil {
		return err
	}
	paths := append([]string{p.WorkingDir}, p.ConfigFiles...)
	for _, path := range append(paths, p.EnvFiles...) {
		if _, err := os.Stat(path); err != nil {
			return err
		}
	}
	return nil
}

// composeCommand returns the Compose CLI to run: the standalone binary shipped
// in the images, or the `docker compose` plugin when only the docker CLI exists.
func composeCommand() ([]string, error) {
	if path, err := exec.LookPath("docker-compose"); err == nil {
		return []string{path}, nil
	}
	if path, err := exec.LookPath("docker"); err == nil {
		return []string{path, "compose"}, nil
	}
	return nil, fmt.Errorf("docker-compose is not installed")
}

// ScaleService runs `docker compose up` for one service so Compose creates the
// extra containers itself: with the compose file's current image, env, labels,
// networks and aliases. Running containers are left alone.
func (c *Client) ScaleService(ctx context.Context, p ComposeProject, service string, replicas int) error {
	base, err := composeCommand()
	if err != nil {
		return err
	}

	args := append(base[1:], "-p", p.Name, "--project-directory", p.WorkingDir)
	for _, file := range p.ConfigFiles {
		args = append(args, "-f", file)
	}
	for _, file := range p.EnvFiles {
		args = append(args, "--env-file", file)
	}
	args = append(args, "up", "-d", "--no-deps", "--no-recreate",
		"--scale", service+"="+strconv.Itoa(replicas), service)

	cmd := exec.CommandContext(ctx, base[0], args...)
	cmd.Dir = p.WorkingDir
	// PWD is the project directory, as it is in the shell that ran the first
	// `docker compose up`, so a `${PWD}` in the compose file means the same here.
	cmd.Env = append(os.Environ(), "COMPOSE_IGNORE_ORPHANS=true", "PWD="+p.WorkingDir)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("docker compose up %s: %w: %s", service, err, tail(out.String(), 2000))
	}
	return nil
}

func tail(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) > n {
		return "..." + s[len(s)-n:]
	}
	return s
}
