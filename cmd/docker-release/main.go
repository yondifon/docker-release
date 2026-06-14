package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/malico/docker-release/internal/config"
	"github.com/malico/docker-release/internal/controller"
	"github.com/malico/docker-release/internal/docker"
	"github.com/malico/docker-release/internal/health"
	"github.com/malico/docker-release/internal/server"
	"github.com/malico/docker-release/internal/state"
)

var version = "dev"

func setupLogging() {
	level := slog.LevelInfo
	switch strings.ToLower(os.Getenv("DR_LOG_LEVEL")) {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})))
}

func main() {
	setupLogging()
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	switch os.Args[1] {
	case "watch":
		run("", cmdWatch)
	case "release":
		if len(os.Args) < 3 {
			fmt.Fprintln(os.Stderr, "usage: dr release <service> [--force] [--project <name>]")
			os.Exit(1)
		}
		if os.Args[2] == "--help" || os.Args[2] == "-h" {
			printUsage()
			return
		}
		opts, err := parseReleaseOptions(os.Args[3:])
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		run(opts.project, func(ctrl *controller.Controller) error {
			return runRelease(ctrl, opts.project, os.Args[2], opts)
		})
	case "rollback":
		if len(os.Args) < 3 {
			fmt.Fprintln(os.Stderr, "usage: dr rollback <service> [--project <name>]")
			os.Exit(1)
		}
		if os.Args[2] == "--help" || os.Args[2] == "-h" {
			printUsage()
			return
		}
		opts, err := parseReleaseOptions(os.Args[3:])
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		run(opts.project, func(ctrl *controller.Controller) error {
			return ctrl.Rollback(context.Background(), opts.project, os.Args[2])
		})
	case "status":
		if len(os.Args) >= 3 && (os.Args[2] == "--help" || os.Args[2] == "-h") {
			printUsage()
			return
		}
		opts, err := parseReleaseOptions(os.Args[2:])
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		run(opts.project, func(ctrl *controller.Controller) error {
			return ctrl.Status(context.Background(), opts.project, opts.service)
		})
	case "healthcheck":
		if !health.IsReady() {
			os.Exit(1)
		}
	case "version":
		fmt.Printf("dr %s\n", version)
	case "help", "--help", "-h":
		printUsage()
	default:
		// Positional shorthand: dr <service> [--force]
		// Anything not matching a reserved command is treated as a service name.
		// If your service is named after a reserved word, use: dr release <service>
		if strings.HasPrefix(os.Args[1], "-") {
			fmt.Fprintf(os.Stderr, "error: unknown flag %q\n\n", os.Args[1])
			printUsage()
			os.Exit(1)
		}
		opts, err := parseReleaseOptions(os.Args[2:])
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		run(opts.project, func(ctrl *controller.Controller) error {
			return runRelease(ctrl, opts.project, os.Args[1], opts)
		})
	}
}

type releaseOptions struct {
	force   bool
	detach  bool
	project string // --project <name>; overrides auto-detect in global mode
	service string // used by status when service is mixed into args
}

func parseReleaseOptions(args []string) (releaseOptions, error) {
	var opts releaseOptions

	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch arg {
		case "--force":
			opts.force = true
		case "--detach", "-d":
			opts.detach = true
		case "--project", "-p":
			if i+1 >= len(args) {
				return releaseOptions{}, fmt.Errorf("--project requires a value")
			}
			i++
			opts.project = args[i]
		default:
			// For status, a bare arg is the service name.
			if !strings.HasPrefix(arg, "-") && opts.service == "" {
				opts.service = arg
				continue
			}
			return releaseOptions{}, fmt.Errorf("unknown option %q", arg)
		}
	}

	return opts, nil
}

func runRelease(ctrl *controller.Controller, project, service string, opts releaseOptions) error {
	if opts.detach {
		return ctrl.EnqueueRelease(project, service, opts.force)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	if err := ctrl.Release(ctx, project, service, opts.force); err != nil {
		return err
	}

	ctrl.WaitDeployments()
	return nil
}

// run resolves the compose project and creates the controller, then calls fn.
// explicitProject, when non-empty, overrides auto-detection (used with
// --project flag in global mode). When DR_ALL_PROJECTS=true, project is "" and
// the controller manages all projects.
func run(explicitProject string, fn func(*controller.Controller) error) {
	dockerClient, err := docker.NewClient()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	defer dockerClient.Close()

	globalMode := os.Getenv("DR_ALL_PROJECTS") == "true" || os.Getenv("DR_ALL_PROJECTS") == "1"

	var project string
	switch {
	case explicitProject != "":
		// Explicit --project flag always wins.
		project = explicitProject
	case globalMode:
		// Watch command in global mode: no project filter.
		project = ""
	default:
		// Normal per-project mode: auto-detect from compose stack.
		project, err = config.DetectProject(context.Background(), dockerClient)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: cannot determine compose project name: %v\n", err)
			os.Exit(1)
		}
		slog.Info("detected compose project", "component", "main", "project", project)
	}

	mgr := state.NewManager("/var/lib/docker-release", project)
	ctrl := controller.New(dockerClient, mgr, project)

	if err := fn(ctrl); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func cmdWatch(ctrl *controller.Controller) error {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	cfg := server.ConfigFromEnv()
	cfg.Version = version
	if cfg.APIEnabled || cfg.WebEnabled {
		if ctrl.Project() == "" {
			slog.Warn("DR_ALL_PROJECTS mode: API/web server not supported; ignoring DR_EXPOSE_API/DR_EXPOSE_WEB", "component", "main")
		} else {
			srv := server.New(cfg, ctrl, ctrl.StateManager(), ctrl.Project())
			go func() {
				if err := srv.Start(ctx); err != nil {
					slog.Error("server error", "component", "main", "err", err)
				}
			}()
		}
	}

	return ctrl.Watch(ctx)
}

func printUsage() {
	fmt.Printf(`dr %s — deployment controller for Docker Compose

Usage:
  dr <service> [--force]                     Deploy a service (short form)
  dr <command> [options]

Commands:
  <service>                                  Deploy the named service (alias for release)
  release <service> [--force] [--project P]  Deploy a service explicitly
                                             --force overrides an in-progress deployment
                                             --detach queues work for watch and returns
                                             --project overrides auto-detect (global mode)
  rollback <service> [--project P]           Roll back a service to its previous deployment
  status [service] [--project P]             Show deployment state
  watch                                      Start the controller (run via compose, not manually)
  version                                    Print version
  help, --help, -h                           Show this help

Global mode:
  Set DR_ALL_PROJECTS=true to watch all Compose projects from a single instance.
  CLI commands in global mode require --project <name> to target a specific project.

Note: if a service name collides with a reserved command (e.g. a service named
"status"), use the explicit form: dr release status

`, version)
}
