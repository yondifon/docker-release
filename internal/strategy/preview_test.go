package strategy

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/malico/docker-release/internal/config"
	"github.com/malico/docker-release/internal/provider"
	"github.com/malico/docker-release/internal/state"
)

func previewDeployment() *Deployment {
	d := checkDeployment(config.CheckConfig{})
	d.Config.PreviewHost = "next.example.com"
	return d
}

func addrs(servers []provider.Server) []string {
	var list []string
	for _, s := range servers {
		list = append(list, s.Addr)
	}
	return list
}

// The check runs while the preview host already serves green, and the
// switch config no longer has it.
func TestPreviewIsLiveDuringChecksAndGoneAtSwitch(t *testing.T) {
	d := previewDeployment()
	prov := &mockProvider{}
	checks := &fakeChecks{}
	var live []string
	d.Checks = &previewProbe{fakeChecks: checks, prov: prov, seen: &live}
	d.Config.Check = config.CheckConfig{Command: "./smoke.sh", Timeout: time.Second}

	if err := NewBlueGreen(&mockDocker{}, prov, state.NewManager(t.TempDir(), "")).Execute(context.Background(), d); err != nil {
		t.Fatal(err)
	}

	if len(live) != 1 || live[0] != "172.18.0.10:80" {
		t.Errorf("the check should run while the preview serves green only, saw %v", live)
	}
	first := prov.configs[0]
	if got := addrs(first.Servers); len(got) != 1 || got[0] != "172.18.0.2:80" {
		t.Errorf("the main host must stay on blue while previewing, got %v", got)
	}
	for i, c := range prov.configs[1:] {
		if len(c.Preview) != 0 {
			t.Errorf("config %d after the preview still has preview servers: %v", i+1, addrs(c.Preview))
		}
	}
}

// previewProbe records the preview servers of the last config at the moment the check runs.
type previewProbe struct {
	*fakeChecks
	prov *mockProvider
	seen *[]string
}

func (p *previewProbe) RunCheck(ctx context.Context, target ContainerInfo) (CheckResult, error) {
	*p.seen = addrs(p.prov.configs[len(p.prov.configs)-1].Preview)
	return p.fakeChecks.RunCheck(ctx, target)
}

func TestFailedCheckTakesThePreviewAway(t *testing.T) {
	d := previewDeployment()
	d.Config.Check = config.CheckConfig{Command: "./smoke.sh", Timeout: time.Second}
	d.Checks = &fakeChecks{result: CheckResult{ExitCode: 1, Output: "sign-up failed"}}
	prov := &mockProvider{}
	docker := &orderDocker{prov: prov, configsAtStop: map[string]int{}}

	err := NewBlueGreen(docker, prov, state.NewManager(t.TempDir(), "")).Execute(context.Background(), d)

	if !IsUndone(err) || !strings.Contains(err.Error(), "sign-up failed") {
		t.Fatalf("want an undone release, got %v", err)
	}
	if len(prov.configs) != 2 || len(prov.configs[0].Preview) != 1 || len(prov.configs[1].Preview) != 0 {
		t.Fatalf("want a config with the preview, then one without; got %d configs", len(prov.configs))
	}
	if docker.configsAtStop[d.New[0].ID] != 2 {
		t.Errorf("green must be stopped only after the preview is removed, configs written at stop: %d", docker.configsAtStop[d.New[0].ID])
	}
}

func TestNoPreviewHostChangesNothing(t *testing.T) {
	d := checkDeployment(config.CheckConfig{})
	prov := &mockProvider{}

	if err := NewBlueGreen(&mockDocker{}, prov, state.NewManager(t.TempDir(), "")).Execute(context.Background(), d); err != nil {
		t.Fatal(err)
	}

	for i, c := range prov.configs {
		if len(c.Preview) != 0 {
			t.Errorf("config %d has preview servers without a preview host", i)
		}
	}
	if len(prov.configs) != 3 {
		t.Errorf("want the usual 3 configs, got %d", len(prov.configs))
	}
}
