package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/malico/docker-release/internal/docker"
)

type pluginModel struct {
	Name     string                   `json:"name"`
	Services map[string]pluginService `json:"services"`
}

type pluginService struct {
	Image       string   `json:"image,omitempty"`
	Build       string   `json:"build,omitempty"`
	Profiles    []string `json:"profiles,omitempty"`
	SecurityOpt []string `json:"security_opt,omitempty"`
}

type pluginFixture struct {
	Controllers map[string]string `json:"controllers"`
	Source      string            `json:"source"`
	Files       string            `json:"files"`
}

type pluginOutcome struct {
	Controller string      `json:"controller"`
	Project    string      `json:"project"`
	Image      string      `json:"image"`
	Model      pluginModel `json:"model"`
}

func writePluginJSON(t *testing.T, path string, value any) {
	t.Helper()
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
}

func newPluginHarness(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	if err := os.Mkdir(bin, 0700); err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"docker", "docker-compose"} {
		wrapper := "#!/bin/sh\nexec \"$PLUGIN_TEST_BINARY\" -test.run=^TestPluginDockerBoundary$ -- \"$@\"\n"
		if err := os.WriteFile(filepath.Join(bin, name), []byte(wrapper), 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("PLUGIN_TEST_BINARY", exe)
	t.Setenv("PLUGIN_FIXTURE_DIR", dir)
	for _, key := range []string{"COMPOSE_FILE", "COMPOSE_PROJECT_NAME", "COMPOSE_ENV_FILES", "COMPOSE_PROFILES"} {
		t.Setenv(key, "")
	}
	return dir
}

func runPlugin(t *testing.T, cwd string, args ...string) pluginOutcome {
	t.Helper()
	plugin, err := filepath.Abs("../../scripts/docker-release")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sh", append([]string{plugin}, args...)...)
	cmd.Dir = cwd
	cmd.Env = append(os.Environ(), "PWD="+cwd)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("host command failed: %v\n%s", err, out)
	}
	return readPluginOutcome(t)
}

func readPluginOutcome(t *testing.T) pluginOutcome {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(os.Getenv("PLUGIN_FIXTURE_DIR"), "outcome.json"))
	if err != nil {
		t.Fatal(err)
	}
	var result pluginOutcome
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestHostReleaseUsesSelectedProjectBeforeFindingController(t *testing.T) {
	dir := newPluginHarness(t)
	cwd := filepath.Join(dir, "a")
	if err := os.Mkdir(cwd, 0700); err != nil {
		t.Fatal(err)
	}
	a := filepath.Join(cwd, "compose.json")
	b := filepath.Join(dir, "other.json")
	writePluginJSON(t, a, pluginModel{Name: "a", Services: map[string]pluginService{"app": {Image: "a:old"}}})
	writePluginJSON(t, b, pluginModel{Name: "b", Services: map[string]pluginService{"app": {Image: "b:new"}}})
	writePluginJSON(t, filepath.Join(dir, "fixture.json"), pluginFixture{
		Controllers: map[string]string{"a": "controller-a", "b": "controller-b"}, Source: dir, Files: b,
	})
	t.Setenv("COMPOSE_FILE", b)
	result := runPlugin(t, cwd, "app")
	if result.Controller != "controller-b" || result.Project != "b" || result.Image != "b:new" {
		t.Fatalf("release changed the wrong project: %+v", result)
	}
}

func TestHostGlobalStatusUsesControllerProjectAndKeepsTarget(t *testing.T) {
	dir := newPluginHarness(t)
	compose := filepath.Join(dir, "compose.json")
	if err := os.WriteFile(compose, []byte("name: infra\nservices: {}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	writePluginJSON(t, filepath.Join(dir, "fixture.json"), pluginFixture{
		Controllers: map[string]string{"infra": "global-controller"}, Source: dir, Files: compose,
	})
	result := runPlugin(t, dir, "-f", compose, "status", "--project", "myapp")
	if result.Controller != "global-controller" || result.Project != "myapp" {
		t.Fatalf("status did not reach the global controller's target: %+v", result)
	}
}

func TestHostReleaseIncludesInactiveProfileService(t *testing.T) {
	dir := newPluginHarness(t)
	compose := filepath.Join(dir, "compose.json")
	writePluginJSON(t, compose, pluginModel{Name: "profile", Services: map[string]pluginService{
		"app": {Image: "profile:new", Profiles: []string{"optional"}},
	}})
	writePluginJSON(t, filepath.Join(dir, "fixture.json"), pluginFixture{
		Controllers: map[string]string{"profile": "controller"}, Source: dir, Files: compose,
	})
	result := runPlugin(t, dir, "-f", compose, "release", "app")
	if result.Image != "profile:new" {
		t.Fatalf("profile service was not released: %+v", result)
	}
}

func TestHostReleaseKeepsGlobCharactersInComposePaths(t *testing.T) {
	dir := newPluginHarness(t)
	compose := filepath.Join(dir, "compose[prod].json")
	writePluginJSON(t, compose, pluginModel{Name: "literal", Services: map[string]pluginService{"app": {Image: "literal:new"}}})
	writePluginJSON(t, filepath.Join(dir, "composep.json"), pluginModel{Name: "wrong", Services: map[string]pluginService{"app": {Image: "wrong:new"}}})
	writePluginJSON(t, filepath.Join(dir, "fixture.json"), pluginFixture{
		Controllers: map[string]string{"literal": "controller"}, Source: dir, Files: compose,
	})
	result := runPlugin(t, dir, "-f", compose, "app")
	if result.Image != "literal:new" {
		t.Fatalf("a matching filename replaced the selected Compose file: %+v", result)
	}
}

func TestHostModelKeepsMountedBuildOnlyRelease(t *testing.T) {
	dir := newPluginHarness(t)
	compose := filepath.Join(dir, "compose.json")
	model := pluginModel{Name: "mounted", Services: map[string]pluginService{"app": {Build: dir}}}
	writePluginJSON(t, compose, model)
	writePluginJSON(t, filepath.Join(dir, "fixture.json"), pluginFixture{})
	releasePluginModel(t, model, docker.ComposeProject{Name: "mounted", WorkingDir: dir, ConfigFiles: []string{compose}})
	if result := readPluginOutcome(t); result.Image != "mounted-app:built" {
		t.Fatalf("mounted build-only release did not create its replacement: %+v", result)
	}
}

func TestHostModelResolvesRelativeSeccompAgainstProject(t *testing.T) {
	dir := newPluginHarness(t)
	if err := os.WriteFile(filepath.Join(dir, "seccomp.json"), []byte(`{"defaultAction":"SCMP_ACT_ALLOW"}`), 0600); err != nil {
		t.Fatal(err)
	}
	model := pluginModel{Name: "secured", Services: map[string]pluginService{
		"app": {Image: "secured:new", SecurityOpt: []string{"seccomp=seccomp.json"}},
	}}
	writePluginJSON(t, filepath.Join(dir, "fixture.json"), pluginFixture{})
	releasePluginModel(t, model, docker.ComposeProject{Name: "secured", WorkingDir: dir})
	if result := readPluginOutcome(t); result.Image != "secured:new" {
		t.Fatalf("relative seccomp file blocked the replacement: %+v", result)
	}
}

func releasePluginModel(t *testing.T, model pluginModel, source docker.ComposeProject) {
	t.Helper()
	data, err := json.Marshal(model)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := docker.ReadComposeConfig(strings.NewReader(string(data)))
	if err != nil {
		t.Fatal(err)
	}
	project, err := cfg.Project(model.Name, "app", source)
	if err != nil {
		t.Fatal(err)
	}
	if err := new(docker.Client).ScaleService(t.Context(), project, "app", 2); err != nil {
		t.Fatal(err)
	}
}

func TestPluginDockerBoundary(t *testing.T) {
	dir := os.Getenv("PLUGIN_FIXTURE_DIR")
	if dir == "" {
		return
	}
	var args []string
	for i, arg := range os.Args {
		if arg == "--" {
			args = os.Args[i+1:]
			break
		}
	}
	if err := pluginDockerBoundary(dir, args); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(0)
}

func pluginDockerBoundary(dir string, args []string) error {
	data, err := os.ReadFile(filepath.Join(dir, "fixture.json"))
	if err != nil {
		return err
	}
	var fixture pluginFixture
	if err := json.Unmarshal(data, &fixture); err != nil {
		return err
	}
	if args[0] == "compose" {
		args = args[1:]
	}
	switch args[0] {
	case "ps":
		for project, controller := range fixture.Controllers {
			if strings.Contains(strings.Join(args, " "), "label=com.docker.compose.project="+project+" ") {
				fmt.Println(controller)
			}
		}
		return nil
	case "inspect":
		fmt.Printf("%s\n%s\n", fixture.Source, fixture.Files)
		return nil
	case "exec":
		return pluginExecOutcome(dir, args)
	default:
		return pluginComposeOutcome(dir, args)
	}
}

func pluginExecCommand(args []string) (string, []string, bool) {
	i := 1
	modelInput := false
	for i < len(args) {
		if args[i] == "-i" {
			i++
			continue
		}
		if args[i] == "-e" {
			modelInput = modelInput || args[i+1] == "DR_COMPOSE_CONFIG_STDIN=1"
			i += 2
			continue
		}
		break
	}
	return args[i], args[i+2:], modelInput
}

func pluginExecOutcome(dir string, args []string) error {
	controller, command, modelInput := pluginExecCommand(args)
	result := pluginOutcome{Controller: controller}
	for i, arg := range command {
		if arg == "--project" || arg == "-p" {
			result.Project = command[i+1]
		}
	}
	if modelInput {
		if err := json.NewDecoder(os.Stdin).Decode(&result.Model); err != nil {
			return err
		}
		service := command[0]
		if service == "release" {
			service = command[1]
		}
		selected, ok := result.Model.Services[service]
		if !ok {
			return fmt.Errorf("service %q missing from resolved model", service)
		}
		result.Image = selected.Image
		if result.Project == "" {
			result.Project = result.Model.Name
		}
	}
	data, err := json.Marshal(result)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "outcome.json"), data, 0600)
}

type pluginComposeCommand struct {
	files                             []string
	project, workdir, action, service string
}

func parsePluginCompose(args []string) pluginComposeCommand {
	var command pluginComposeCommand
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-p":
			i++
			command.project = args[i]
		case "--project-directory":
			i++
			command.workdir = args[i]
		case "-f":
			i++
			command.files = append(command.files, args[i])
		case "--env-file", "--format", "--scale":
			i++
		case "config", "up":
			command.action = args[i]
		default:
			if !strings.HasPrefix(args[i], "-") {
				command.service = args[i]
			}
		}
	}
	return command
}

func loadPluginModel(files []string) (pluginModel, error) {
	if len(files) == 0 {
		files = filepath.SplitList(os.Getenv("COMPOSE_FILE"))
	}
	if len(files) == 0 {
		files = []string{"compose.json"}
	}
	var model pluginModel
	for _, file := range files {
		var data []byte
		var err error
		if file == "-" {
			data, err = io.ReadAll(os.Stdin)
		} else {
			data, err = os.ReadFile(file)
		}
		if err != nil {
			return model, err
		}
		if err := json.Unmarshal(data, &model); err != nil {
			return model, err
		}
	}
	return model, nil
}

func pluginComposeOutcome(dir string, args []string) error {
	command := parsePluginCompose(args)
	model, err := loadPluginModel(command.files)
	if err != nil {
		return err
	}
	if command.project != "" {
		model.Name = command.project
	}
	if command.action == "config" {
		return printPluginConfig(model, command.service)
	}
	service := model.Services[command.service]
	if service.Image == "" && service.Build != "" {
		if _, err := os.Stat(service.Build); err != nil {
			return err
		}
		service.Image = model.Name + "-" + command.service + ":built"
	}
	if err := validatePluginSecurity(service, command.workdir); err != nil {
		return err
	}
	data, err := json.Marshal(pluginOutcome{Project: model.Name, Image: service.Image, Model: model})
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "outcome.json"), data, 0600)
}

func printPluginConfig(model pluginModel, selected string) error {
	for name, service := range model.Services {
		if len(service.Profiles) > 0 && name != selected {
			delete(model.Services, name)
		}
	}
	data, err := json.MarshalIndent(model, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(data))
	return nil
}

func validatePluginSecurity(service pluginService, workdir string) error {
	for _, option := range service.SecurityOpt {
		if strings.HasPrefix(option, "seccomp=") {
			path := strings.TrimPrefix(option, "seccomp=")
			if !filepath.IsAbs(path) {
				path = filepath.Join(workdir, path)
			}
			if _, err := os.ReadFile(path); err != nil {
				return err
			}
		}
	}
	return nil
}
