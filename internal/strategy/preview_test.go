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

func (p *previewProbe) RunCheck(ctx context.Context, target ContainerInfo, token string) (CheckResult, error) {
	*p.seen = addrs(p.prov.configs[len(p.prov.configs)-1].Preview)
	return p.fakeChecks.RunCheck(ctx, target, token)
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

// tokenProbe records, at the moment the check runs, the token the check got, the
// token the proxy config carries and the token the state file shows.
type tokenProbe struct {
	prov  *mockProvider
	mgr   *state.Manager
	check string
	proxy string
	saved string
}

func (p *tokenProbe) RunCheck(_ context.Context, _ ContainerInfo, token string) (CheckResult, error) {
	p.check = token
	p.proxy = p.prov.configs[len(p.prov.configs)-1].PreviewToken
	ds, _ := p.mgr.Load("app")
	p.saved = ds.PreviewToken
	return CheckResult{}, nil
}

func tokenDeployment(label string) *Deployment {
	d := checkDeployment(config.CheckConfig{Command: "./smoke.sh", Timeout: time.Second})
	d.Config.PreviewToken = label
	return d
}

// A release with `auto` gets a fresh token each time. It reaches the check, the
// proxy and the state file while green waits, and is gone from all three at the switch.
func TestAutoTokenLivesOnlyWhileGreenWaits(t *testing.T) {
	seen := map[string]bool{}
	for range 2 {
		d := tokenDeployment(config.PreviewTokenAuto)
		prov := &mockProvider{}
		mgr := state.NewManager(t.TempDir(), "")
		probe := &tokenProbe{prov: prov, mgr: mgr}
		d.Checks = probe

		if err := NewBlueGreen(&mockDocker{}, prov, mgr).Execute(context.Background(), d); err != nil {
			t.Fatal(err)
		}

		if len(probe.check) < 32 || probe.proxy != probe.check || probe.saved != probe.check {
			t.Fatalf("check, proxy and state must share one token, got %q / %q / %q", probe.check, probe.proxy, probe.saved)
		}
		seen[probe.check] = true
		for i, c := range prov.configs[1:] {
			if c.PreviewToken != "" || len(c.Preview) != 0 {
				t.Errorf("config %d after the preview still has the token or its servers", i+1)
			}
		}
		if ds, _ := mgr.Load("app"); ds.PreviewToken != "" || ds.PreviewURL != "" {
			t.Errorf("the state file keeps the token after the switch: %+v", ds)
		}
	}
	if len(seen) != 2 {
		t.Errorf("every release needs its own token, got %v", seen)
	}
}

func TestFixedTokenIsUsedAsGiven(t *testing.T) {
	d := tokenDeployment("fixed-token-from-env-1234")
	prov := &mockProvider{}
	mgr := state.NewManager(t.TempDir(), "")
	probe := &tokenProbe{prov: prov, mgr: mgr}
	d.Checks = probe

	if err := NewBlueGreen(&mockDocker{}, prov, mgr).Execute(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	if probe.check != "fixed-token-from-env-1234" || probe.proxy != probe.check {
		t.Errorf("want the fixed token everywhere, got check %q proxy %q", probe.check, probe.proxy)
	}
}

// A failed check withdraws the token with the preview, so the old link stops working.
func TestFailedCheckWithdrawsTheToken(t *testing.T) {
	d := tokenDeployment(config.PreviewTokenAuto)
	d.Checks = &fakeChecks{result: CheckResult{ExitCode: 1, Output: "sign-up failed"}}
	prov := &mockProvider{}
	mgr := state.NewManager(t.TempDir(), "")

	err := NewBlueGreen(&mockDocker{}, prov, mgr).Execute(context.Background(), d)

	if !IsUndone(err) {
		t.Fatalf("want an undone release, got %v", err)
	}
	if last := prov.configs[len(prov.configs)-1]; last.PreviewToken != "" || len(last.Preview) != 0 {
		t.Error("the last config must not carry the token")
	}
	if ds, _ := mgr.Load("app"); ds.PreviewToken != "" {
		t.Error("the state file keeps the token after the release was undone")
	}
}
