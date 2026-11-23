// Package lvm manages temporary read-only LVM snapshots and host disk-space pre-flight checks.
package lvm

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

const (
	cleanupTimeout = 30 * time.Second
	bytesPerGiB    = uint64(1024 * 1024 * 1024)
)

// Manager orchestrates LVM snapshot creation, teardown, and filesystem free-space validation.
type Manager struct {
	Runner       cmdexec.Runner
	AvailBytesFn func(path string) (uint64, error)
}

func defaultStatfs(path string, stat *syscall.Statfs_t) error {
	if err := syscall.Statfs(path, stat); err != nil {
		return fmt.Errorf("statfs: %w", err)
	}
	return nil
}

var (
	statfsFn = defaultStatfs
	osStat   = os.Stat
)

func defaultAvailBytes(path string) (uint64, error) {
	probePath := existingAncestor(filepath.Clean(path))
	var stat syscall.Statfs_t
	if err := statfsFn(probePath, &stat); err != nil {
		return 0, fmt.Errorf("statfs %q: %w", probePath, err)
	}
	if stat.Bsize <= 0 {
		return 0, fmt.Errorf("invalid filesystem block size %d on %q", stat.Bsize, probePath)
	}
	blockSize := uint64(stat.Bsize)
	return stat.Bavail * blockSize, nil
}

func existingAncestor(path string) string {
	curr := path
	for {
		if _, err := osStat(curr); err == nil {
			return curr
		}
		parent := filepath.Dir(curr)
		if parent == curr {
			return curr
		}
		curr = parent
	}
}

// NewManager constructs an LVM Manager using OSRunner and syscall.Statfs when arguments are nil.
func NewManager(runner cmdexec.Runner) *Manager {
	if runner == nil {
		runner = cmdexec.OSRunner{}
	}
	return &Manager{
		Runner:       runner,
		AvailBytesFn: defaultAvailBytes,
	}
}

// CheckFreeSpace verifies that dir has at least minFreeGB GiB available before starting a backup.
func (m *Manager) CheckFreeSpace(ctx context.Context, dir string, minFreeGB uint64) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("context canceled before free-space check: %w", err)
	}
	if minFreeGB == 0 {
		return nil
	}

	availFn := m.AvailBytesFn
	if availFn == nil {
		availFn = defaultAvailBytes
	}

	availBytes, err := availFn(dir)
	if err != nil {
		return err
	}

	requiredBytes := minFreeGB * bytesPerGiB
	if availBytes < requiredBytes {
		availGiB := float64(availBytes) / float64(bytesPerGiB)
		return fmt.Errorf("insufficient free space at %q: %.2f GiB available, %d GiB required", dir, availGiB, minFreeGB)
	}
	return nil
}

// CreateSnapshot removes any leftover snapshot, creates a fresh LVM snapshot, and mounts it read-only.
func (m *Manager) CreateSnapshot(ctx context.Context, cfg *config.LVMConfig) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("context canceled before creating LVM snapshot: %w", err)
	}
	if err := validateLVMPaths(cfg); err != nil {
		return err
	}

	if err := m.CleanupSnapshot(ctx, cfg); err != nil {
		return fmt.Errorf("pre-create snapshot cleanup: %w", err)
	}

	originDev := fmt.Sprintf("/dev/%s/%s", cfg.VGName, cfg.LVName)
	snapDev := fmt.Sprintf("/dev/%s/%s", cfg.VGName, cfg.SnapshotName)

	if err := m.Runner.Run(ctx, &cmdexec.Spec{
		Name: "lvcreate",
		Args: []string{"--size", cfg.SnapshotSize, "--snapshot", "--name", cfg.SnapshotName, originDev},
	}); err != nil {
		return fmt.Errorf("create LVM snapshot %q: %w", snapDev, err)
	}

	if err := os.MkdirAll(cfg.MountPoint, 0o750); err != nil {
		cleanErr := m.CleanupSnapshot(ctx, cfg)
		return errors.Join(fmt.Errorf("create snapshot mount point %q: %w", cfg.MountPoint, err), cleanErr)
	}

	if err := m.Runner.Run(ctx, &cmdexec.Spec{
		Name: "mount",
		Args: []string{"-o", "ro", snapDev, cfg.MountPoint},
	}); err != nil {
		cleanErr := m.CleanupSnapshot(ctx, cfg)
		return errors.Join(fmt.Errorf("mount snapshot %q at %q: %w", snapDev, cfg.MountPoint, err), cleanErr)
	}

	return nil
}

// CleanupSnapshot unmounts the snapshot mount point if active and removes the snapshot logical volume idempotently.
func (m *Manager) CleanupSnapshot(ctx context.Context, cfg *config.LVMConfig) error {
	if err := validateLVMPaths(cfg); err != nil {
		return err
	}

	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
	defer cancel()

	var umountErr error
	if m.Runner.Run(cleanupCtx, &cmdexec.Spec{
		Name: "mountpoint",
		Args: []string{"-q", cfg.MountPoint},
	}) == nil {
		if err := m.Runner.Run(cleanupCtx, &cmdexec.Spec{
			Name: "umount",
			Args: []string{cfg.MountPoint},
		}); err != nil {
			umountErr = fmt.Errorf("unmount snapshot %q: %w", cfg.MountPoint, err)
		}
	}

	snapDev := fmt.Sprintf("/dev/%s/%s", cfg.VGName, cfg.SnapshotName)
	var lvRemoveErr error
	if m.Runner.Run(cleanupCtx, &cmdexec.Spec{
		Name: "lvs",
		Args: []string{snapDev},
	}) == nil {
		if err := m.Runner.Run(cleanupCtx, &cmdexec.Spec{
			Name: "lvremove",
			Args: []string{"-y", snapDev},
		}); err != nil {
			lvRemoveErr = fmt.Errorf("remove LVM snapshot %q: %w", snapDev, err)
		}
	}

	return errors.Join(umountErr, lvRemoveErr)
}

func validateLVMPaths(cfg *config.LVMConfig) error {
	if cfg == nil {
		return errors.New("lvm config must not be nil")
	}
	if strings.TrimSpace(cfg.VGName) == "" || strings.TrimSpace(cfg.LVName) == "" ||
		strings.TrimSpace(cfg.SnapshotName) == "" || strings.TrimSpace(cfg.MountPoint) == "" {
		return errors.New("lvm vg_name, lv_name, snapshot_name, and mount_point must not be empty")
	}
	return nil
}
