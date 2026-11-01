package config

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
)

const (
	defaultCheckTimeout = 2 * time.Minute
	defaultCheckWindow  = 5 * time.Minute
)

// checkLabels are all the names after `release.check.`.
var checkLabels = []string{"command", "service", "timeout", "fail_on", "paths", "max_errors", "window"}

// checkErrorLabels are the labels that need the proxy's access log.
var checkErrorLabels = []string{"fail_on", "paths", "max_errors", "window"}

// CheckConfig holds the `release.check.*` labels: a command that must pass
// before users move to the new version, and error rules watched afterwards.
type CheckConfig struct {
	// Configured is true when any `release.check.*` label is set.
	Configured bool

	Command    string
	Service    string
	Timeout    time.Duration
	timeoutSet bool

	// Watch is true when any error rule is set. The rules then apply for Window.
	Watch     bool
	FailOn    []StatusMatch
	Paths     []string
	MaxErrors int
	Window    time.Duration
}

// StatusMatch matches one status (`502`) or a whole class (`5xx`).
type StatusMatch struct {
	Code  int
	Class bool
}

// matchesStatus reports whether status counts as an error under any of the matches.
func matchesStatus(matches []StatusMatch, status int) bool {
	for _, m := range matches {
		if m.Class && status/100 == m.Code/100 || !m.Class && status == m.Code {
			return true
		}
	}
	return false
}

// Counts reports whether a response with this status for this path is an error
// under the rules.
func (c CheckConfig) Counts(status int, path string) bool {
	if !matchesStatus(c.FailOn, status) {
		return false
	}
	if len(c.Paths) == 0 {
		return true
	}
	for _, prefix := range c.Paths {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}
	return false
}

func parseCheck(labels map[string]string) (CheckConfig, error) {
	value := func(name string) string { return strings.TrimSpace(labels["release.check."+name]) }

	check := CheckConfig{
		Command: value("command"),
		Service: value("service"),
		Timeout: defaultCheckTimeout,
		Window:  defaultCheckWindow,
		FailOn:  []StatusMatch{{Code: 500, Class: true}},
	}

	for name, v := range labels {
		short, ok := strings.CutPrefix(name, "release.check.")
		if !ok || strings.TrimSpace(v) == "" {
			continue
		}
		if !slices.Contains(checkLabels, short) {
			return check, fmt.Errorf("unknown label %s; the check labels are %s", name, strings.Join(checkLabels, ", "))
		}
		check.Configured = true
	}
	for _, name := range checkErrorLabels {
		if value(name) != "" {
			check.Watch = true
		}
	}

	if v := value("timeout"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d <= 0 {
			return check, fmt.Errorf("release.check.timeout %q must be a duration such as 2m", v)
		}
		check.Timeout = d
		check.timeoutSet = true
	}
	if v := value("window"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d <= 0 {
			return check, fmt.Errorf("release.check.window %q must be a duration such as 5m", v)
		}
		check.Window = d
	}
	if v := value("max_errors"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			return check, fmt.Errorf("release.check.max_errors %q must be a whole number, 0 or more", v)
		}
		check.MaxErrors = n
	}
	if v := value("fail_on"); v != "" {
		matches, err := parseStatusMatches(v)
		if err != nil {
			return check, err
		}
		check.FailOn = matches
	}
	if v := value("paths"); v != "" {
		for _, p := range strings.Split(v, ",") {
			p = strings.TrimSpace(p)
			if !validRoutePath.MatchString(p) {
				return check, fmt.Errorf("release.check.paths %q must be comma-separated paths that start with /", v)
			}
			check.Paths = append(check.Paths, p)
		}
	}
	return check, nil
}

func parseStatusMatches(v string) ([]StatusMatch, error) {
	bad := fmt.Errorf("release.check.fail_on %q must be comma-separated statuses such as 502 or classes such as 5xx", v)
	var matches []StatusMatch
	for _, part := range strings.Split(v, ",") {
		part = strings.ToLower(strings.TrimSpace(part))
		if len(part) != 3 {
			return nil, bad
		}
		if strings.HasSuffix(part, "xx") {
			class, err := strconv.Atoi(part[:1])
			if err != nil || class < 1 || class > 5 {
				return nil, bad
			}
			matches = append(matches, StatusMatch{Code: class * 100, Class: true})
			continue
		}
		code, err := strconv.Atoi(part)
		if err != nil || code < 100 || code > 599 {
			return nil, bad
		}
		matches = append(matches, StatusMatch{Code: code})
	}
	return matches, nil
}

func (c *ServiceConfig) validateCheck() error {
	check := c.Check
	if !check.Configured {
		return nil
	}
	if c.Strategy != StrategyBlueGreen {
		return fmt.Errorf("release.check.* labels work with release.strategy=blue-green only")
	}
	if check.Command == "" && (check.Service != "" || check.timeoutSet) {
		return fmt.Errorf("release.check.service and release.check.timeout need release.check.command")
	}
	if check.Service != "" && !validUpstreamName.MatchString(check.Service) {
		return fmt.Errorf("release.check.service %q must match [a-zA-Z0-9._-]+", check.Service)
	}
	if check.Watch {
		bundled := BundledProxy()
		if bundled == "" || bundled == "none" || bundled != string(c.Provider) {
			return fmt.Errorf("release.check.fail_on, paths, max_errors and window need a docker-release image with the proxy bundled and release.provider set to that proxy; with a separate proxy only release.check.command works")
		}
	}
	return nil
}
