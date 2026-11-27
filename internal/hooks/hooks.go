// Package hooks executes declarative pre-dump, pre-snapshot, and post-snapshot lifecycle actions.
package hooks

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/webstudiobond/backup-recovery-orchestrator/internal/config"
	"github.com/webstudiobond/backup-recovery-orchestrator/internal/docker"
)

const (
	defaultWaitPollInterval = 250 * time.Millisecond
	hookActionRemovePath    = "remove_path"
	hookActionWaitForFile   = "wait_for_file"
	hookActionComposeKill   = "compose_kill"
	hookActionComposeExec   = "compose_exec"
	hookActionComposeRun    = "compose_run"
)

var (
	filepathAbs   = filepath.Abs
	openRoot      = os.OpenRoot
	rootSameFile  = os.SameFile
	rootClose     = (*os.Root).Close
	rootLstat     = (*os.Root).Lstat
	rootStat      = (*os.Root).Stat
	rootOpenRoot  = (*os.Root).OpenRoot
	rootOpenFile  = (*os.Root).OpenFile
	rootRemoveAll = (*os.Root).RemoveAll
	fileClose     = (*os.File).Close
	fileStat      = (*os.File).Stat
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
	case hookActionRemovePath:
		return removePathWithinStack(stackDir, step.Path)
	case hookActionComposeKill:
		if err := r.Docker.Kill(ctx, composeFile, step.Service, step.Signal); err != nil {
			return fmt.Errorf("compose kill: %w", err)
		}
		return nil
	case hookActionWaitForFile:
		return r.waitForFile(ctx, step.Path, time.Duration(step.TimeoutSec)*time.Second)
	case hookActionComposeExec:
		if err := r.Docker.Exec(ctx, composeFile, step.Service, nil, step.Command, nil, nil); err != nil {
			return fmt.Errorf("compose exec: %w", err)
		}
		return nil
	case hookActionComposeRun:
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

	root, openErr := openRoot(absRoot)
	if openErr != nil {
		if errors.Is(openErr, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("open stack_dir %q: %w", absRoot, openErr)
	}

	rmErr := removeSubpathInRoot(root, rel, absTarget, targetPath)
	closeErr := rootClose(root)
	if closeErr != nil {
		return errors.Join(rmErr, fmt.Errorf("close stack_dir %q: %w", absRoot, closeErr))
	}
	return rmErr
}

func removeSubpathInRoot(root *os.Root, relPath, absTarget, origTarget string) (err error) {
	parts := strings.Split(filepath.ToSlash(relPath), "/")
	curr := root
	closeCurr := false
	defer func() {
		if closeCurr {
			if closeErr := rootClose(curr); closeErr != nil {
				err = errors.Join(err, fmt.Errorf("close directory in stack_dir: %w", closeErr))
			}
		}
	}()

	for _, seg := range parts[:len(parts)-1] {
		next, found, openErr := openChildDirNoSymlink(curr, seg, origTarget)
		if openErr != nil {
			return openErr
		}
		if !found {
			return nil
		}
		if closeCurr {
			closeCurr = false
			if closeErr := rootClose(curr); closeErr != nil {
				curr = next
				closeCurr = true
				return fmt.Errorf("close directory %q in stack_dir: %w", seg, closeErr)
			}
		}
		curr = next
		closeCurr = true
	}

	lastSeg := parts[len(parts)-1]
	info, statErr := rootLstat(curr, lastSeg)
	if statErr != nil {
		if errors.Is(statErr, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("stat %q in remove_path %q: %w", lastSeg, origTarget, statErr)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("refusing symlink target %q in remove_path", origTarget)
	}
	if rmErr := rootRemoveAll(curr, lastSeg); rmErr != nil {
		return fmt.Errorf("remove %q: %w", absTarget, rmErr)
	}
	return nil
}

func openChildDirNoSymlink(parent *os.Root, seg, origTarget string) (*os.Root, bool, error) {
	info, statErr := rootLstat(parent, seg)
	if statErr != nil {
		if errors.Is(statErr, os.ErrNotExist) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("stat %q in remove_path %q: %w", seg, origTarget, statErr)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, false, fmt.Errorf("refusing symlink %q in remove_path %q", seg, origTarget)
	}
	if !info.IsDir() {
		return nil, false, fmt.Errorf("non-directory segment %q in remove_path %q", seg, origTarget)
	}

	return openVerifiedChildDir(parent, seg, info, origTarget)
}

func openVerifiedChildDir(parent *os.Root, seg string, lstatInfo fs.FileInfo, origTarget string) (*os.Root, bool, error) {
	probe, probeErr := rootOpenFile(parent, seg, os.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NONBLOCK, 0)
	if probeErr != nil {
		if errors.Is(probeErr, os.ErrNotExist) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("open directory %q in remove_path %q: %w", seg, origTarget, probeErr)
	}
	probeInfo, statErr := fileStat(probe)
	closeProbeErr := fileClose(probe)
	if err := errors.Join(statErr, closeProbeErr); err != nil {
		return nil, false, fmt.Errorf("stat probed directory %q in remove_path %q: %w", seg, origTarget, err)
	}
	if !probeInfo.IsDir() || !rootSameFile(lstatInfo, probeInfo) {
		return nil, false, fmt.Errorf("concurrent path replacement detected at %q in remove_path %q", seg, origTarget)
	}
	return openVerifiedChildDirFallback(parent, seg, lstatInfo, origTarget)
}

func openVerifiedChildDirFallback(parent *os.Root, seg string, lstatInfo fs.FileInfo, origTarget string) (*os.Root, bool, error) {
	child, openErr := rootOpenRoot(parent, seg)
	if openErr != nil {
		if errors.Is(openErr, os.ErrNotExist) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("open directory %q in remove_path %q: %w", seg, origTarget, openErr)
	}

	dotInfo, dotErr := rootStat(child, ".")
	if dotErr != nil {
		return nil, false, errors.Join(fmt.Errorf("stat opened directory %q in remove_path %q: %w", seg, origTarget, dotErr), rootClose(child))
	}
	if !dotInfo.IsDir() || !rootSameFile(lstatInfo, dotInfo) {
		return nil, false, errors.Join(fmt.Errorf("concurrent path replacement detected at %q in remove_path %q", seg, origTarget), rootClose(child))
	}
	return child, true, nil
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
