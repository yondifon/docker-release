package strategy

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/malico/docker-release/internal/accesslog"
	"github.com/malico/docker-release/internal/config"
	"github.com/malico/docker-release/internal/state"
)

type fakeChecks struct {
	targets []string
	result  CheckResult
	err     error
}

func (f *fakeChecks) RunCheck(_ context.Context, target ContainerInfo, _ string) (CheckResult, error) {
	f.targets = append(f.targets, target.Addr)
	return f.result, f.err
}

type fakeRequests struct {
	subscribers []func(accesslog.Entry)
	lost        chan struct{}
	err         error
}

func (f *fakeRequests) Subscribe(fn func(accesslog.Entry)) (func(), <-chan struct{}, error) {
	f.subscribers = append(f.subscribers, fn)
	return func() {}, f.lost, f.err
}

func (f *fakeRequests) publish(e accesslog.Entry) {
	for _, fn := range f.subscribers {
		fn(e)
	}
}

// reloadHook runs after each reload, so a test can send requests at the point
// users would be on the config just written.
type reloadHook struct {
	*mockProvider
	after func(reloads int)
}

func (h *reloadHook) Reload(ctx context.Context) error {
	err := h.mockProvider.Reload(ctx)
	h.after(h.reloads)
	return err
}

// orderDocker records how many configs had been written when each container was stopped.
type orderDocker struct {
	mockDocker
	prov          *mockProvider
	configsAtStop map[string]int
}

func (o *orderDocker) Stop(ctx context.Context, id string, timeout int) error {
	o.configsAtStop[id] = len(o.prov.configs)
	return o.mockDocker.Stop(ctx, id, timeout)
}

func checkDeployment(rules config.CheckConfig) *Deployment {
	d := bgDeployment()
	d.Old = d.Old[:1]
	d.New = d.New[:1]
	d.Config.Check = rules
	return d
}

func TestFailingCheckKeepsUsersOnBlue(t *testing.T) {
	docker := &mockDocker{}
	prov := &mockProvider{}
	stateMgr := state.NewManager(t.TempDir(), "")
	d := checkDeployment(config.CheckConfig{Command: "./smoke.sh", Timeout: time.Second})
	d.Checks = &fakeChecks{result: CheckResult{Output: "signup page returned 500", ExitCode: 1}}

	err := NewBlueGreen(docker, prov, stateMgr).Execute(context.Background(), d)

	if !IsUndone(err) || !strings.Contains(err.Error(), "signup page returned 500") {
		t.Fatalf("want an undone release that carries the command's output, got %v", err)
	}
	if len(prov.configs) != 0 || prov.reloads != 0 {
		t.Errorf("the proxy must not be touched: %d configs, %d reloads", len(prov.configs), prov.reloads)
	}
	if len(docker.stopCalls) != 1 || !strings.HasPrefix(docker.stopCalls[0], "green_") {
		t.Errorf("only green should be stopped, got %v", docker.stopCalls)
	}
	ds, _ := stateMgr.Load("app")
	if ds.Status != state.StatusIdle || !strings.Contains(ds.Failure, "signup page returned 500") {
		t.Errorf("state should be idle and keep the output, got %q %q", ds.Status, ds.Failure)
	}
	if len(ds.Containers.Stable) != 1 || ds.Containers.Stable[0] != d.Old[0].ID {
		t.Errorf("state should list blue as stable, got %v", ds.Containers.Stable)
	}
}

func TestCheckRunsOncePerGreenContainer(t *testing.T) {
	d := bgDeployment()
	d.Config.Check = config.CheckConfig{Command: "./smoke.sh", Timeout: time.Second}
	checks := &fakeChecks{}
	d.Checks = checks

	err := NewBlueGreen(&mockDocker{}, &mockProvider{}, state.NewManager(t.TempDir(), "")).Execute(context.Background(), d)

	if err != nil {
		t.Fatalf("a passing check must let the release continue: %v", err)
	}
	if len(checks.targets) != 2 || checks.targets[0] == checks.targets[1] {
		t.Errorf("want one run per green container, got %v", checks.targets)
	}
}

func TestSlowCheckFailsTheRelease(t *testing.T) {
	d := checkDeployment(config.CheckConfig{Command: "sleep 99", Timeout: time.Second})
	d.Checks = &fakeChecks{result: CheckResult{Output: "still going"}, err: context.DeadlineExceeded}

	err := NewBlueGreen(&mockDocker{}, &mockProvider{}, state.NewManager(t.TempDir(), "")).Execute(context.Background(), d)

	if !IsUndone(err) || !strings.Contains(err.Error(), "timed out") || !strings.Contains(err.Error(), "still going") {
		t.Fatalf("want a timeout reason with the output, got %v", err)
	}
}

func errorRules() config.CheckConfig {
	return config.CheckConfig{
		Watch:     true,
		FailOn:    []config.StatusMatch{{Code: 500, Class: true}},
		Paths:     []string{"/signup"},
		MaxErrors: 1,
		Window:    time.Minute,
	}
}

func TestErrorsAfterSwitchRollBackToBlue(t *testing.T) {
	prov := &mockProvider{}
	docker := &orderDocker{prov: prov, configsAtStop: map[string]int{}}
	requests := &fakeRequests{}
	d := checkDeployment(errorRules())
	d.Requests = requests
	green := d.New[0].Addr
	hooked := &reloadHook{mockProvider: prov, after: func(reloads int) {
		if reloads == 1 {
			// Users reach green after the switch, and /signup fails twice.
			requests.publish(accesslog.Entry{Status: 500, Upstream: green, Path: "/signup"})
			requests.publish(accesslog.Entry{Status: 500, Upstream: green, Path: "/signup"})
		}
	}}
	stateMgr := state.NewManager(t.TempDir(), "")

	err := NewBlueGreen(docker, hooked, stateMgr).Execute(context.Background(), d)

	if !IsUndone(err) {
		t.Fatalf("want an undone release, got %v", err)
	}
	if len(prov.configs) != 2 {
		t.Fatalf("want a switch config and a rollback config, got %d", len(prov.configs))
	}
	if sw := prov.configs[0]; len(sw.Servers) != 1 || sw.Servers[0].Addr != green {
		t.Errorf("after the switch users must go to green only, got %+v", sw.Servers)
	}
	if back := prov.configs[1]; len(back.Servers) != 1 || back.Servers[0].Addr != d.Old[0].Addr {
		t.Errorf("rollback must send users to blue, got %+v", back.Servers)
	}
	if got := docker.configsAtStop[d.New[0].ID]; got != 2 {
		t.Errorf("green stopped after %d configs; it must be stopped only after users moved back (2)", got)
	}
	if _, stopped := docker.configsAtStop[d.Old[0].ID]; stopped {
		t.Error("blue must keep running")
	}
	ds, _ := stateMgr.Load("app")
	if ds.Status != state.StatusIdle || !strings.Contains(ds.Failure, "/signup returned 500") {
		t.Errorf("state should be idle and say what failed, got %q %q", ds.Status, ds.Failure)
	}
}

func TestErrorsThatDoNotMatchTheRulesAreIgnored(t *testing.T) {
	prov := &mockProvider{}
	docker := &orderDocker{prov: prov, configsAtStop: map[string]int{}}
	requests := &fakeRequests{}
	rules := errorRules()
	rules.Window = 50 * time.Millisecond
	d := checkDeployment(rules)
	d.Requests = requests
	green := d.New[0].Addr
	hooked := &reloadHook{mockProvider: prov, after: func(reloads int) {
		if reloads == 1 {
			requests.publish(accesslog.Entry{Status: 500, Upstream: d.Old[0].Addr, Path: "/signup"}) // blue, not counted
			requests.publish(accesslog.Entry{Status: 500, Upstream: green, Path: "/about"})          // other path
			requests.publish(accesslog.Entry{Status: 404, Upstream: green, Path: "/signup"})         // not a 5xx
			requests.publish(accesslog.Entry{Status: 500, Upstream: green, Path: "/signup"})         // one error is allowed
		}
	}}
	stateMgr := state.NewManager(t.TempDir(), "")

	if err := NewBlueGreen(docker, hooked, stateMgr).Execute(context.Background(), d); err != nil {
		t.Fatalf("release should finish: %v", err)
	}

	if len(prov.configs) != 1 {
		t.Errorf("a clean window needs no second config, got %d", len(prov.configs))
	}
	if _, stopped := docker.configsAtStop[d.Old[0].ID]; !stopped {
		t.Error("blue should be stopped when the window ends cleanly")
	}
	if _, stopped := docker.configsAtStop[d.New[0].ID]; stopped {
		t.Error("green must keep running")
	}
	ds, _ := stateMgr.Load("app")
	if ds.Status != state.StatusIdle || ds.Failure != "" || ds.Containers.Stable[0] != d.New[0].ID {
		t.Errorf("green should be the stable version with no failure, got %+v", ds)
	}
}

func TestCancelledWindowIsNotACleanFinish(t *testing.T) {
	prov := &mockProvider{}
	docker := &orderDocker{prov: prov, configsAtStop: map[string]int{}}
	d := checkDeployment(errorRules())
	d.Requests = &fakeRequests{}
	ctx, cancel := context.WithCancel(context.Background())
	hooked := &reloadHook{mockProvider: prov, after: func(int) { cancel() }}

	err := NewBlueGreen(docker, hooked, state.NewManager(t.TempDir(), "")).Execute(ctx, d)

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want the cancel error so the controller rolls back, got %v", err)
	}
	if _, stopped := docker.configsAtStop[d.Old[0].ID]; stopped {
		t.Error("blue must keep running when the window is cut short")
	}
}

func TestLostAccessLogRollsBack(t *testing.T) {
	prov := &mockProvider{}
	docker := &orderDocker{prov: prov, configsAtStop: map[string]int{}}
	requests := &fakeRequests{lost: make(chan struct{})}
	d := checkDeployment(errorRules())
	d.Requests = requests
	hooked := &reloadHook{mockProvider: prov, after: func(reloads int) {
		if reloads == 1 {
			close(requests.lost)
		}
	}}

	err := NewBlueGreen(docker, hooked, state.NewManager(t.TempDir(), "")).Execute(context.Background(), d)

	if !IsUndone(err) || !strings.Contains(err.Error(), "access log") {
		t.Fatalf("a window nobody can watch must undo the release, got %v", err)
	}
	if _, stopped := docker.configsAtStop[d.Old[0].ID]; stopped {
		t.Error("blue must keep running")
	}
}

func TestNoAccessLogStopsTheReleaseBeforeTheSwitch(t *testing.T) {
	prov := &mockProvider{}
	d := checkDeployment(errorRules())
	d.Requests = &fakeRequests{err: errors.New("no socket")}

	err := NewBlueGreen(&mockDocker{}, prov, state.NewManager(t.TempDir(), "")).Execute(context.Background(), d)

	if err == nil || len(prov.configs) != 0 {
		t.Fatalf("want an error and an untouched proxy, got %v and %d configs", err, len(prov.configs))
	}
}
