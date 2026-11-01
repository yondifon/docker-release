package strategy

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/malico/docker-release/internal/accesslog"
	"github.com/malico/docker-release/internal/config"
)

// CheckResult is what the check command printed and how it exited.
type CheckResult struct {
	Output   string
	ExitCode int
}

// CheckRunner runs `release.check.command` against one new container. ctx ends
// when the check's timeout does. previewToken is the live preview token, or ""
// when the release has none.
type CheckRunner interface {
	RunCheck(ctx context.Context, target ContainerInfo, previewToken string) (CheckResult, error)
}

// RequestSource delivers every request the proxy answers, until cancel is
// called. lost closes if the stream breaks before that.
type RequestSource interface {
	Subscribe(fn func(accesslog.Entry)) (cancel func(), lost <-chan struct{}, err error)
}

// UndoneError means the release failed a check and was undone: the old
// containers serve, the new ones are gone, and the state records why. Its
// message is meant for the person who ran the release.
type UndoneError struct{ Reason string }

func (e *UndoneError) Error() string { return e.Reason }

// IsUndone reports whether err is an UndoneError.
func IsUndone(err error) bool {
	var undone *UndoneError
	return errors.As(err, &undone)
}

// runChecks runs the check command against each new container, one after the
// other. It returns nil when all pass and an UndoneError reason otherwise.
func runChecks(ctx context.Context, d *Deployment, previewToken string) (reason string, err error) {
	check := d.Config.Check
	if d.Checks == nil {
		return "", fmt.Errorf("release.check.command is set but this deployment has no way to run it")
	}
	for _, target := range d.New {
		short := target.ID[:12]
		slog.Info("running check", "component", "blue-green", "service", d.Service, "container", short)

		checkCtx, cancel := context.WithTimeout(ctx, check.Timeout)
		result, runErr := d.Checks.RunCheck(checkCtx, target, previewToken)
		cancel()

		switch {
		case ctx.Err() != nil:
			return "", ctx.Err()
		case errors.Is(runErr, context.DeadlineExceeded):
			return withOutput(fmt.Sprintf("Check timed out after %s on new container %s.", plainDuration(check.Timeout), short), result.Output), nil
		case runErr != nil:
			return withOutput(fmt.Sprintf("Check could not run on new container %s: %v.", short, runErr), result.Output), nil
		case result.ExitCode != 0:
			return withOutput(fmt.Sprintf("Check failed on new container %s with exit code %d.", short, result.ExitCode), result.Output), nil
		}
	}
	return "", nil
}

// plainDuration prints 5m, not 5m0s.
func plainDuration(d time.Duration) string {
	s := d.String()
	if strings.HasSuffix(s, "m0s") {
		s = strings.TrimSuffix(s, "0s")
	}
	if strings.HasSuffix(s, "h0m") {
		s = strings.TrimSuffix(s, "0m")
	}
	return s
}

func withOutput(summary, output string) string {
	if output == "" {
		return summary
	}
	return summary + "\n" + output
}

// errorWatch counts the errors users get from the new containers.
type errorWatch struct {
	rules  config.CheckConfig
	greens map[string]bool

	mu       sync.Mutex
	errors   int
	last     accesslog.Entry
	exceeded chan struct{}
}

func newErrorWatch(rules config.CheckConfig, greens []ContainerInfo) *errorWatch {
	w := &errorWatch{rules: rules, greens: make(map[string]bool, len(greens)), exceeded: make(chan struct{})}
	for _, g := range greens {
		w.greens[g.Addr] = true
	}
	return w
}

func (w *errorWatch) observe(e accesslog.Entry) {
	if !w.greens[e.Upstream] || !w.rules.Counts(e.Status, e.Path) {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.errors++
	w.last = e
	if w.errors == w.rules.MaxErrors+1 {
		close(w.exceeded)
	}
}

type windowEnd int

const (
	windowClean windowEnd = iota
	windowTooManyErrors
	windowBlind
)

// wait ends when the window does, when the errors pass the limit, or when the
// log stream breaks, which leaves nothing to watch.
func (w *errorWatch) wait(ctx context.Context, lost <-chan struct{}) (windowEnd, error) {
	timer := time.NewTimer(w.rules.Window)
	defer timer.Stop()
	select {
	case <-w.exceeded:
		return windowTooManyErrors, nil
	case <-lost:
		return windowBlind, nil
	case <-timer.C:
		return windowClean, nil
	case <-ctx.Done():
		return windowClean, ctx.Err()
	}
}

func (w *errorWatch) reason() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return fmt.Sprintf("The new version returned %d errors within %s (limit %d). Last: %s returned %d.",
		w.errors, plainDuration(w.rules.Window), w.rules.MaxErrors, w.last.Path, w.last.Status)
}
