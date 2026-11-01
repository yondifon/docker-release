package config

import (
	"strings"
	"testing"
	"time"
)

func blueGreenLabels(extra map[string]string) map[string]string {
	labels := map[string]string{
		"release.enable":   "true",
		"release.provider": "nginx",
		"release.strategy": "blue-green",
	}
	for k, v := range extra {
		labels[k] = v
	}
	return labels
}

func TestCheckLabelsRule(t *testing.T) {
	t.Setenv("DR_BUNDLED_PROXY", "nginx")
	cfg, err := ParseLabels(blueGreenLabels(map[string]string{
		"release.check.command":    "./smoke.sh",
		"release.check.fail_on":    "502, 5xx",
		"release.check.paths":      "/signup,/checkout",
		"release.check.max_errors": "1",
		"release.check.window":     "90s",
	}))
	if err != nil {
		t.Fatal(err)
	}
	check := cfg.Check
	if !check.Watch || check.Window != 90*time.Second || check.MaxErrors != 1 || check.Timeout != 2*time.Minute {
		t.Fatalf("unexpected check config: %+v", check)
	}
	for _, c := range []struct {
		status int
		path   string
		want   bool
	}{
		{500, "/signup", true},
		{503, "/signup/step-2", true},
		{502, "/checkout", true},
		{500, "/about", false},
		{404, "/signup", false},
		{200, "/signup", false},
	} {
		if got := check.Counts(c.status, c.path); got != c.want {
			t.Errorf("Counts(%d, %q) = %v, want %v", c.status, c.path, got, c.want)
		}
	}
}

func TestOnlyOneErrorRuleStartsTheWatch(t *testing.T) {
	t.Setenv("DR_BUNDLED_PROXY", "nginx")
	cfg, err := ParseLabels(blueGreenLabels(map[string]string{"release.check.max_errors": "3"}))
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Check.Watch || cfg.Check.Window != 5*time.Minute || !cfg.Check.Counts(500, "/") {
		t.Errorf("a lone rule should watch 5xx for 5m, got %+v", cfg.Check)
	}
}

func TestBlueGreenWithoutCheckLabelsIsUnchanged(t *testing.T) {
	cfg, err := ParseLabels(blueGreenLabels(nil))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Check.Configured || cfg.Check.Watch || cfg.Check.Command != "" {
		t.Errorf("no labels should mean no checks, got %+v", cfg.Check)
	}
}

func TestCheckLabelsRefused(t *testing.T) {
	cases := []struct {
		name    string
		bundled string
		labels  map[string]string
		want    string
	}{
		{"error rules need a bundled image", "", map[string]string{"release.check.window": "5m"}, "proxy bundled"},
		{"error rules need the provider to be the bundled proxy", "angie", map[string]string{"release.check.fail_on": "5xx"}, "proxy bundled"},
		{"the command alone works with a separate proxy", "", map[string]string{"release.check.command": "true"}, ""},
		{"linear has no checks", "nginx", map[string]string{"release.strategy": "linear", "release.check.command": "true"}, "blue-green only"},
		{"service needs a command", "nginx", map[string]string{"release.check.service": "smoke"}, "need release.check.command"},
		{"unknown class", "nginx", map[string]string{"release.check.fail_on": "7xx"}, "fail_on"},
		{"bad status", "nginx", map[string]string{"release.check.fail_on": "50"}, "fail_on"},
		{"path without slash", "nginx", map[string]string{"release.check.paths": "signup"}, "paths"},
		{"negative limit", "nginx", map[string]string{"release.check.max_errors": "-1"}, "max_errors"},
		{"window not a duration", "nginx", map[string]string{"release.check.window": "soon"}, "window"},
		{"typo in the label name", "nginx", map[string]string{"release.check.failon": "5xx"}, "unknown label"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("DR_BUNDLED_PROXY", c.bundled)
			_, err := ParseLabels(blueGreenLabels(c.labels))
			if c.want == "" {
				if err != nil {
					t.Fatalf("want no error, got %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("want an error containing %q, got %v", c.want, err)
			}
		})
	}
}
