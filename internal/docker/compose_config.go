package docker

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const MaxComposeConfigBytes = 16 << 20 // 16 MiB

const (
	composeSourceDirLabel   = "io.docker-release.compose.working_dir"
	composeSourceFilesLabel = "io.docker-release.compose.config_files"
	composeSourceEnvLabel   = "io.docker-release.compose.environment_file"
)

type ComposeConfig struct {
	model  map[string]json.RawMessage
	name   string
	Source ComposeProject
}

func ReadComposeConfig(r io.Reader) (*ComposeConfig, error) {
	data, err := io.ReadAll(io.LimitReader(r, MaxComposeConfigBytes+1))
	if err != nil {
		return nil, fmt.Errorf("reading host Compose config: %w", err)
	}
	if len(data) > MaxComposeConfigBytes {
		return nil, fmt.Errorf("host Compose config exceeds 16 MiB")
	}
	var cfg ComposeConfig
	if err := json.Unmarshal(data, &cfg.model); err != nil {
		return nil, fmt.Errorf("host Compose config must be valid JSON")
	}
	if err := json.Unmarshal(cfg.model["name"], &cfg.name); err != nil || cfg.name == "" {
		return nil, fmt.Errorf("host Compose config must name a project")
	}
	return &cfg, nil
}

func (cfg *ComposeConfig) Project(project, service string, source ComposeProject) (ComposeProject, error) {
	if len(cfg.Source.ConfigFiles) > 0 {
		source = cfg.Source
	}
	if cfg.name != project {
		return ComposeProject{}, fmt.Errorf("host Compose project %q does not match project %q; select the matching project with --project", cfg.name, project)
	}
	var services map[string]map[string]json.RawMessage
	if err := json.Unmarshal(cfg.model["services"], &services); err != nil {
		return ComposeProject{}, fmt.Errorf("host Compose config has invalid services")
	}
	svc, ok := services[service]
	if !ok {
		return ComposeProject{}, fmt.Errorf("service %q is missing from host Compose config", service)
	}
	if err := cfg.validateService(service, svc); err != nil {
		return ComposeProject{}, err
	}
	delete(svc, "build")
	var labels map[string]string
	if data := svc["labels"]; len(data) > 0 {
		if err := json.Unmarshal(data, &labels); err != nil {
			return ComposeProject{}, fmt.Errorf("host Compose service %q has invalid labels", service)
		}
	}
	if labels == nil {
		labels = make(map[string]string)
	}
	if len(source.ConfigFiles) > 0 && source.ConfigFiles[0] != "-" {
		labels[composeSourceDirLabel] = strings.ReplaceAll(source.WorkingDir, "$", "$$")
		labels[composeSourceFilesLabel] = strings.ReplaceAll(strings.Join(source.ConfigFiles, ","), "$", "$$")
		labels[composeSourceEnvLabel] = strings.ReplaceAll(strings.Join(source.EnvFiles, ","), "$", "$$")
	}
	svc["labels"], _ = json.Marshal(labels)
	model := make(map[string]json.RawMessage, len(cfg.model))
	for key, value := range cfg.model {
		model[key] = value
	}
	model["services"], _ = json.Marshal(services)
	data, err := json.Marshal(model)
	if err != nil {
		return ComposeProject{}, fmt.Errorf("encoding host Compose config: %w", err)
	}
	source.Name = project
	if source.WorkingDir == "" {
		source.WorkingDir = "/"
	}
	source.Config = data
	return source, nil
}

func (cfg *ComposeConfig) validateService(name string, svc map[string]json.RawMessage) error {
	var image string
	_ = json.Unmarshal(svc["image"], &image)
	if image == "" {
		return fmt.Errorf("host Compose service %q requires an image; build the image on the host and set image before releasing", name)
	}
	if data := svc["env_file"]; len(data) > 0 && string(data) != "null" && string(data) != "[]" {
		return fmt.Errorf("host Compose service %q has unresolved env files; resolve them with docker compose config", name)
	}
	for _, kind := range []string{"configs", "secrets"} {
		if err := cfg.validateFiles(kind, svc[kind]); err != nil {
			return err
		}
	}
	var options []string
	_ = json.Unmarshal(svc["security_opt"], &options)
	for _, option := range options {
		key, value, ok := strings.Cut(option, "=")
		if !ok {
			key, value, _ = strings.Cut(option, ":")
		}
		if key == "seccomp" && value != "unconfined" && value != "builtin" {
			if err := readableComposeFile(value); err != nil {
				return err
			}
		}
	}
	return nil
}

func (cfg *ComposeConfig) validateFiles(kind string, refs json.RawMessage) error {
	if len(refs) == 0 || string(refs) == "null" {
		return nil
	}
	var sources []struct{ Source string }
	if err := json.Unmarshal(refs, &sources); err != nil {
		return fmt.Errorf("host Compose %s must be resolved with docker compose config", kind)
	}
	var definitions map[string]struct {
		File        string
		Environment string
		External    bool
	}
	if err := json.Unmarshal(cfg.model[kind], &definitions); err != nil {
		return fmt.Errorf("host Compose config has invalid %s", kind)
	}
	for _, ref := range sources {
		definition := definitions[ref.Source]
		if definition.Environment != "" || definition.External {
			return fmt.Errorf("host Compose %s %q uses environment or external storage; define it as a file mounted at the same path in the controller", kind, ref.Source)
		}
		if definition.File != "" {
			if err := readableComposeFile(definition.File); err != nil {
				return err
			}
		}
	}
	return nil
}

func readableComposeFile(path string) error {
	path = strings.ReplaceAll(path, "$$", "$")
	if !filepath.IsAbs(path) {
		return fmt.Errorf("Compose file dependency %q must be an absolute path", path)
	}
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("Compose file dependency %q is not readable in the controller; mount it at the same path or remove the dependency", path)
	}
	defer f.Close()
	return nil
}
