// Package hooks executes declarative pre-dump, pre-snapshot, and post-snapshot lifecycle actions.
package hooks

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/webstudiobond/backup-recovery-orchestrator/internal/config"
	"github.com/webstudiobond/backup-recovery-orchestrator/internal/docker"
)

const defaultWaitPollInterval = 250 * time.Millisecond

var (
	filepathAbs = filepath.Abs
	removeAll   = os.RemoveAll
)

// Runner executes configured lifecycle hooks against a stack's compose project and filesystem.
type Runner struct {
	Docker       *docker.Client
	PollInterval time.Duration
}

// NewRunner creates a lifecycle hook runner backed by the provided Docker Compose client.
func NewRunner(dockerClient *docker.Client) *Runner {
	if dockerClient == nil {
		dockerClient = docker.NewClient(nil)
	}
	return &Runner{
		Docker:       dockerClient,
		PollInterval: defaultWaitPollInterval,
	}
}

// RunPhase executes every hook step for the named phase sequentially, aborting on the first failure.
func (r *Runner) RunPhase(ctx context.Context, phase, stackDir, composeFile string, steps []config.HookStep) error {
	for idx := range steps {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("%s hook[%d] canceled: %w", phase, idx, err)
		}
		if err := r.runStep(ctx, stackDir, composeFile, &steps[idx]); err != nil {
			return fmt.Errorf("%s hook[%d] (%s): %w", phase, idx, steps[idx].Action, err)
		}
	}
	return nil
}

func (r *Runner) runStep(ctx context.Context, stackDir, composeFile string, step *config.HookStep) error {
	switch step.Action {
	case "remove_path":
		return removePathWithinStack(stackDir, step.Path)
	case "compose_kill":
		if err := r.Docker.Kill(ctx, composeFile, step.Service, step.Signal); err != nil {
			return fmt.Errorf("compose kill: %w", err)
		}
		return nil
	case "wait_for_file":
		return r.waitForFile(ctx, step.Path, time.Duration(step.TimeoutSec)*time.Second)
	case "compose_exec":
		if err := r.Docker.Exec(ctx, composeFile, step.Service, nil, step.Command, nil, nil); err != nil {
			return fmt.Errorf("compose exec: %w", err)
		}
		return nil
	case "compose_run":
		if err := r.Docker.Run(ctx, composeFile, step.Service, nil, step.Command, nil, nil); err != nil {
			return fmt.Errorf("compose run: %w", err)
		}
		return nil
	default:
		return fmt.Errorf("unsupported hook action %q", step.Action)
	}
}

func removePathWithinStack(stackDir, targetPath string) error {
	absRoot, err := filepathAbs(filepath.Clean(stackDir))
	if err != nil {
		return fmt.Errorf("resolve stack_dir %q: %w", stackDir, err)
	}
	absTarget, targetErr := filepathAbs(filepath.Clean(targetPath))
	if targetErr != nil {
		return fmt.Errorf("resolve remove_path %q: %w", targetPath, targetErr)
	}

	rel, relErr := filepath.Rel(absRoot, absTarget)
	if relErr != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("refusing to remove %q outside stack_dir %q", targetPath, stackDir)
	}

	if rmErr := removeAll(absTarget); rmErr != nil {
		return fmt.Errorf("remove %q: %w", absTarget, rmErr)
	}
	return nil
}

func (r *Runner) waitForFile(ctx context.Context, path string, timeout time.Duration) error {
	if timeout <= 0 {
		return errors.New("wait_for_file timeout must be positive")
	}

	interval := r.PollInterval
	if interval <= 0 {
		interval = defaultWaitPollInterval
	}

	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cleanPath := filepath.Clean(path)
	for {
		info, err := os.Stat(cleanPath)
		if err == nil && !info.IsDir() {
			return nil
		}

		timer := time.NewTimer(interval)
		select {
		case <-waitCtx.Done():
			timer.Stop()
			return fmt.Errorf("timed out waiting for %q after %s: %w", cleanPath, timeout, waitCtx.Err())
		case <-timer.C:
		}
	}
}
