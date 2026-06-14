package state

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type ReleaseCommand struct {
	ID        string    `json:"id"`
	Project   string    `json:"project,omitempty"`
	Service   string    `json:"service"`
	Force     bool      `json:"force"`
	CreatedAt time.Time `json:"created_at"`
}

type QueuedReleaseCommand struct {
	ReleaseCommand
	path string
}

func (m *Manager) EnqueueReleaseCommand(service string, force bool) (*ReleaseCommand, error) {
	cmd := &ReleaseCommand{
		ID:        GenerateDeploymentID(),
		Project:   m.project,
		Service:   service,
		Force:     force,
		CreatedAt: time.Now(),
	}

	dir := m.commandsDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("creating command dir: %w", err)
	}

	data, err := json.MarshalIndent(cmd, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshaling command: %w", err)
	}

	path := filepath.Join(dir, cmd.ID+".json")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return nil, fmt.Errorf("writing temp command file: %w", err)
	}

	if err := os.Rename(tmp, path); err != nil {
		return nil, fmt.Errorf("renaming command file: %w", err)
	}

	return cmd, nil
}

func (m *Manager) PendingReleaseCommands() ([]QueuedReleaseCommand, error) {
	dir := m.commandsDir()
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading command dir: %w", err)
	}

	commands := make([]QueuedReleaseCommand, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}

		path := filepath.Join(dir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("reading command file: %w", err)
		}

		var cmd ReleaseCommand
		if err := json.Unmarshal(data, &cmd); err != nil {
			return nil, fmt.Errorf("parsing command file: %w", err)
		}

		commands = append(commands, QueuedReleaseCommand{ReleaseCommand: cmd, path: path})
	}

	sort.Slice(commands, func(i, j int) bool {
		return commands[i].CreatedAt.Before(commands[j].CreatedAt)
	})

	return commands, nil
}

func (m *Manager) ClaimReleaseCommand(cmd QueuedReleaseCommand) (QueuedReleaseCommand, bool, error) {
	processingPath := cmd.path + ".processing"
	if err := os.Rename(cmd.path, processingPath); err != nil {
		if os.IsNotExist(err) {
			return QueuedReleaseCommand{}, false, nil
		}
		return QueuedReleaseCommand{}, false, fmt.Errorf("claiming command file: %w", err)
	}

	cmd.path = processingPath
	return cmd, true, nil
}

func (m *Manager) CompleteReleaseCommand(cmd QueuedReleaseCommand) error {
	if err := os.Remove(cmd.path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("removing command file: %w", err)
	}

	return nil
}

func (m *Manager) commandsDir() string {
	name := "commands"
	if m.project != "" {
		name = m.project + "_commands"
	}
	return filepath.Join(m.dir, name)
}

// ScanAllPendingCommands scans every project command directory under baseDir
// (`<project>_commands/`) and returns all pending (unclaimed) commands across
// all projects. The Project field on each command is populated from the JSON
// when present, falling back to the directory name.
//
// This is used by the global-mode controller to poll commands from all
// projects with a single call instead of iterating known managers.
func ScanAllPendingCommands(baseDir string) ([]QueuedReleaseCommand, error) {
	entries, err := os.ReadDir(baseDir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading state dir: %w", err)
	}

	var all []QueuedReleaseCommand
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasSuffix(entry.Name(), "_commands") {
			continue
		}
		dirProject := strings.TrimSuffix(entry.Name(), "_commands")

		cmdDir := filepath.Join(baseDir, entry.Name())
		files, err := os.ReadDir(cmdDir)
		if err != nil {
			continue
		}
		for _, f := range files {
			if f.IsDir() || !strings.HasSuffix(f.Name(), ".json") {
				continue
			}
			path := filepath.Join(cmdDir, f.Name())
			data, err := os.ReadFile(path)
			if err != nil {
				continue
			}
			var cmd ReleaseCommand
			if err := json.Unmarshal(data, &cmd); err != nil {
				continue
			}
			if cmd.Project == "" {
				cmd.Project = dirProject
			}
			all = append(all, QueuedReleaseCommand{ReleaseCommand: cmd, path: path})
		}
	}

	sort.Slice(all, func(i, j int) bool {
		return all[i].CreatedAt.Before(all[j].CreatedAt)
	})

	return all, nil
}
