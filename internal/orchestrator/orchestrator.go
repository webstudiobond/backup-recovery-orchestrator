// Package orchestrator coordinates full stack backup pipelines, offsite sync, and deterministic single-stack recovery.
package orchestrator

import (
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/webstudiobond/backup-recovery-orchestrator/internal/archive"
	"github.com/webstudiobond/backup-recovery-orchestrator/internal/cmdexec"
	"github.com/webstudiobond/backup-recovery-orchestrator/internal/config"
	"github.com/webstudiobond/backup-recovery-orchestrator/internal/dac"
	"github.com/webstudiobond/backup-recovery-orchestrator/internal/database"
	"github.com/webstudiobond/backup-recovery-orchestrator/internal/docker"
	"github.com/webstudiobond/backup-recovery-orchestrator/internal/hooks"
	"github.com/webstudiobond/backup-recovery-orchestrator/internal/lvm"
	"github.com/webstudiobond/backup-recovery-orchestrator/internal/mount"
	"github.com/webstudiobond/backup-recovery-orchestrator/internal/storage"
	"github.com/webstudiobond/backup-recovery-orchestrator/internal/sysuser"
)

const archiveTimestampLayout = "2006-01-02_15-04-05"

// Orchestrator ties together LVM, Docker, databases, hooks, archival, DAC, sysuser, and offsite storage.
type Orchestrator struct {
	Docker     *docker.Client
	Hooks      *hooks.Runner
	Database   *database.Engine
	LVM        *lvm.Manager
	Archive    *archive.Engine
	Storage    *storage.Manager
	SysUser    *sysuser.Manager
	DAC        *dac.Applier
	Now        func() time.Time
	SystemdDir string
}

func defaultOrchestratorNow() time.Time {
	return time.Now()
}

// New constructs an Orchestrator wired with production implementations backed by OSRunner.
func New(runner cmdexec.Runner) *Orchestrator {
	if runner == nil {
		runner = cmdexec.OSRunner{}
	}
	dockerClient := docker.NewClient(runner)
	verifier := mount.NewVerifier(runner)

	return &Orchestrator{
		Docker:   dockerClient,
		Hooks:    hooks.NewRunner(dockerClient),
		Database: database.NewEngine(runner, dockerClient),
		LVM:      lvm.NewManager(runner),
		Archive:  archive.NewEngine(runner),
		Storage:  storage.NewManager(runner, verifier),
		SysUser:  sysuser.NewManager(runner),
		DAC:      dac.NewApplier(),
		Now:      defaultOrchestratorNow,
	}
}

func stackTargetsDestination(stack *config.StackConfig, destName string) bool {
	if len(stack.Destinations) == 0 {
		return true
	}
	return slices.Contains(stack.Destinations, destName)
}

func selectStacks(bundle *config.Bundle, requested []string) ([]config.StackConfig, error) {
	if bundle == nil {
		return nil, errors.New("config bundle must not be nil")
	}

	if len(requested) > 0 {
		selected := make([]config.StackConfig, 0, len(requested))
		seen := make(map[string]struct{}, len(requested))
		for _, name := range requested {
			if _, dup := seen[name]; dup {
				continue
			}
			stack, ok := bundle.StackByName(name)
			if !ok {
				return nil, fmt.Errorf("stack %q not found in configuration", name)
			}
			seen[name] = struct{}{}
			selected = append(selected, stack)
		}
		return selected, nil
	}

	var enabled []config.StackConfig
	for i := range bundle.Stacks {
		if bundle.Stacks[i].Enabled {
			enabled = append(enabled, bundle.Stacks[i])
		}
	}
	if len(enabled) == 0 {
		return nil, errors.New("no enabled stacks found in configuration")
	}
	return enabled, nil
}

func selectDestinations(all []config.DestinationConfig, filter string) ([]config.DestinationConfig, error) {
	if len(all) == 0 {
		return nil, errors.New("no offsite destinations configured")
	}
	if strings.TrimSpace(filter) == "" {
		return all, nil
	}
	for i := range all {
		if all[i].Name == filter {
			return []config.DestinationConfig{all[i]}, nil
		}
	}
	return nil, fmt.Errorf("destination %q not found in configuration", filter)
}

func resolveWriter(w io.Writer) io.Writer {
	if w != nil {
		return w
	}
	return os.Stdout
}

func writeLine(w io.Writer, line string) {
	if _, err := fmt.Fprintln(w, line); err != nil {
		return
	}
}

func wrapRemoveErr(err error, path string) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("remove temporary directory %q: %w", path, err)
}
