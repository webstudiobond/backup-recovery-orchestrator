// Package mount verifies FUSE, block, or directory backup destinations and activates systemd mount units.
package mount

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/webstudiobond/backup-recovery-orchestrator/internal/cmdexec"
	"github.com/webstudiobond/backup-recovery-orchestrator/internal/config"
)

// Mount verification constants define filesystem magic signatures and unit activation polling cadences.
const (
	fuseSuperMagic = int64(0x65735546)
	defaultPoll    = 500 * time.Millisecond
)

// Verifier ensures offsite destination paths or mounts match their configured type before writes occur.
type Verifier struct {
	Runner       cmdexec.Runner
	StatFSType   func(path string) (int64, error)
	PollInterval time.Duration
}

func toInt64[T ~int | ~int32 | ~int64 | ~uint | ~uint32 | ~uint64](v T) int64 {
	return int64(v)
}

func defaultStatFSType(path string) (int64, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(filepath.Clean(path), &stat); err != nil {
		return 0, fmt.Errorf("statfs %q: %w", path, err)
	}
	return toInt64(stat.Type), nil
}

// NewVerifier builds a destination Verifier using OSRunner and syscall.Statfs.
func NewVerifier(runner cmdexec.Runner) *Verifier {
	if runner == nil {
		runner = cmdexec.OSRunner{}
	}
	return &Verifier{
		Runner:       runner,
		StatFSType:   defaultStatFSType,
		PollInterval: defaultPoll,
	}
}

// EnsureReady verifies dest according to its explicit Type ('fuse', 'mount', 'dir', or 'rclone').
func (v *Verifier) EnsureReady(ctx context.Context, dest *config.DestinationConfig) error {
	if dest == nil {
		return errors.New("destination config must not be nil")
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("context canceled before verifying destination %q: %w", dest.Name, err)
	}

	switch dest.Type {
	case config.DestTypeRclone:
		return nil
	case config.DestTypeDir:
		if strings.TrimSpace(dest.BackupDir) == "" {
			return fmt.Errorf("destination %q backup_dir must not be empty", dest.Name)
		}
		info, err := os.Stat(filepath.Clean(dest.BackupDir))
		if err == nil && !info.IsDir() {
			return fmt.Errorf("destination %q backup_dir %q exists and is not a directory", dest.Name, dest.BackupDir)
		}
		return nil
	case config.DestTypeFUSE:
		if strings.TrimSpace(dest.MountPoint) == "" {
			return errors.New("destination mount_point must not be empty")
		}
		return v.waitForMount(ctx, dest, func(_ context.Context) (bool, string) {
			return v.isFUSEActive(dest.MountPoint)
		})
	case config.DestTypeMount:
		if strings.TrimSpace(dest.MountPoint) == "" {
			return errors.New("destination mount_point must not be empty")
		}
		return v.waitForMount(ctx, dest, func(checkCtx context.Context) (bool, string) {
			return v.isGenericMountActive(checkCtx, dest.MountPoint)
		})
	default:
		return fmt.Errorf("unsupported destination type %q", dest.Type)
	}
}

func (v *Verifier) waitForMount(ctx context.Context, dest *config.DestinationConfig, checkFn func(context.Context) (bool, string)) error {
	if ok, _ := checkFn(ctx); ok {
		return nil
	}

	var startErr error
	if strings.TrimSpace(dest.SystemdUnit) != "" {
		startErr = v.Runner.Run(ctx, &cmdexec.Spec{
			Name: cmdexec.BinSystemctl,
			Args: []string{"start", dest.SystemdUnit},
		})
	}

	timeout := time.Duration(dest.WaitTimeoutSec) * time.Second
	if timeout <= 0 {
		timeout = time.Duration(config.DefaultWaitTimeoutSec) * time.Second
	}
	interval := v.PollInterval
	if interval <= 0 {
		interval = defaultPoll
	}

	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var lastReason string
	for {
		active, reason := checkFn(waitCtx)
		if active {
			return nil
		}
		lastReason = reason

		timer := time.NewTimer(interval)
		select {
		case <-waitCtx.Done():
			timer.Stop()
			if startErr != nil {
				return fmt.Errorf("mount %q (%s) failed (%s; systemctl start: %w): %w", dest.MountPoint, dest.Type, lastReason, startErr, waitCtx.Err())
			}
			return fmt.Errorf("mount %q (%s) not ready within %s (%s): %w", dest.MountPoint, dest.Type, timeout, lastReason, waitCtx.Err())
		case <-timer.C:
		}
	}
}

func (v *Verifier) isGenericMountActive(ctx context.Context, mountPoint string) (active bool, reason string) {
	cleanPath := filepath.Clean(mountPoint)
	if err := v.Runner.Run(ctx, &cmdexec.Spec{
		Name: cmdexec.BinMountpoint,
		Args: []string{"-q", cleanPath},
	}); err != nil {
		return false, fmt.Sprintf("mountpoint check failed: %v", err)
	}
	return true, ""
}

func (v *Verifier) isFUSEActive(mountPoint string) (active bool, reason string) {
	cleanPath := filepath.Clean(mountPoint)
	info, err := os.Stat(cleanPath)
	if err != nil {
		return false, fmt.Sprintf("stat error: %v", err)
	}
	if !info.IsDir() {
		return false, "mount point is not a directory"
	}

	statFn := v.StatFSType
	if statFn == nil {
		statFn = defaultStatFSType
	}
	fsType, fsErr := statFn(cleanPath)
	if fsErr != nil {
		return false, fmt.Sprintf("statfs error: %v", fsErr)
	}
	if fsType != fuseSuperMagic {
		return false, fmt.Sprintf("filesystem magic 0x%x is not FUSE (0x%x)", fsType, fuseSuperMagic)
	}
	return true, ""
}
