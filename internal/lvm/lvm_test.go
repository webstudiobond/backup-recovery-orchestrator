package lvm

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/webstudiobond/backup-recovery-orchestrator/internal/config"
)

func TestCheckFreeSpace(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	canceledCtx, cancel := context.WithCancel(ctx)
	cancel()

	cases := []struct {
		ctx         context.Context
		availErr    error
		name        string
		errContains string
		availBytes  uint64
		minFreeGB   uint64
	}{
		{
			ctx:        ctx,
			name:       "sufficient space",
			availBytes: 20 * bytesPerGiB,
			minFreeGB:  15,
		},
		{
			ctx:        ctx,
			name:       "zero requirement skipped",
			availBytes: 0,
			minFreeGB:  0,
		},
		{
			ctx:         ctx,
			name:        "insufficient space rejected",
			availBytes:  5 * bytesPerGiB,
			minFreeGB:   15,
			errContains: "insufficient free space",
		},
		{
			ctx:         ctx,
			name:        "statfs failure propagated",
			minFreeGB:   10,
			availErr:    errors.New("statfs failed"),
			errContains: "statfs failed",
		},
		{
			ctx:         canceledCtx,
			name:        "canceled context",
			minFreeGB:   10,
			errContains: "context canceled",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			mgr := &Manager{
				AvailBytesFn: func(_ string) (uint64, error) {
					return tc.availBytes, tc.availErr
				},
			}
			err := mgr.CheckFreeSpace(tc.ctx, "/backups", tc.minFreeGB)
			if tc.errContains == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.errContains) {
				t.Fatalf("expected error containing %q, got %v", tc.errContains, err)
			}
		})
	}

	realMgr := NewManager(nil)
	if err := realMgr.CheckFreeSpace(ctx, filepath.Join(t.TempDir(), "future-subdir"), 0); err != nil {
		t.Fatalf("unexpected error with real statfs: %v", err)
	}
}

func TestCreateAndCleanupSnapshot(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	mountDir := filepath.Join(t.TempDir(), "snap_mount")
	cfg := config.LVMConfig{
		VGName:       "ubuntu-vg",
		LVName:       "lv_home",
		SnapshotName: "home_snap",
		SnapshotSize: "35G",
		MountPoint:   mountDir,
	}

	t.Run("create when no prior snapshot exists", func(t *testing.T) {
		t.Parallel()

		runner := &fakeLVMRunner{
			failCommands: map[string]error{
				"mountpoint": errors.New("not mounted"),
				"lvs":        errors.New("not present"),
			},
		}
		mgr := NewManager(runner)
		if err := mgr.CreateSnapshot(ctx, &cfg); err != nil {
			t.Fatalf("CreateSnapshot error: %v", err)
		}

		wantNames := []string{"mountpoint", "lvs", "lvcreate", "mount"}
		if len(runner.calls) != len(wantNames) {
			t.Fatalf("expected %d calls, got %d (%+v)", len(wantNames), len(runner.calls), runner.calls)
		}
		for i, want := range wantNames {
			if runner.calls[i].name != want {
				t.Fatalf("call[%d] expected %q, got %q", i, want, runner.calls[i].name)
			}
		}
	})

	t.Run("mount failure triggers automatic lvremove cleanup", func(t *testing.T) {
		t.Parallel()

		runner := &fakeLVMRunner{
			failCommands: map[string]error{
				"mount": errors.New("mount error"),
			},
		}
		mgr := NewManager(runner)
		err := mgr.CreateSnapshot(ctx, &cfg)
		if err == nil || !strings.Contains(err.Error(), "mount error") {
			t.Fatalf("expected mount error, got %v", err)
		}

		lastCall := runner.calls[len(runner.calls)-1]
		if lastCall.name != "lvremove" {
			t.Fatalf("expected cleanup lvremove after mount failure, got %q", lastCall.name)
		}
	})

	t.Run("cleanup uses WithoutCancel when context is canceled", func(t *testing.T) {
		t.Parallel()

		canceledCtx, cancel := context.WithCancel(ctx)
		cancel()

		runner := &fakeLVMRunner{}
		mgr := NewManager(runner)
		if err := mgr.CleanupSnapshot(canceledCtx, &cfg); err != nil {
			t.Fatalf("expected CleanupSnapshot to proceed despite canceled parent ctx, got %v", err)
		}
		if len(runner.calls) != 4 {
			t.Fatalf("expected mountpoint, umount, lvs, lvremove calls, got %+v", runner.calls)
		}
	})
}

func TestDefaultAvailBytesAndAncestor(t *testing.T) {
	tmpDir := t.TempDir()
	bytesAvail, err := defaultAvailBytes(tmpDir)
	if err != nil {
		t.Fatalf("unexpected error from defaultAvailBytes: %v", err)
	}
	if bytesAvail == 0 {
		t.Fatal("expected non-zero bytes available on tempDir")
	}

	ancestor := existingAncestor(filepath.Join(tmpDir, "sub1", "sub2", "sub3"))
	if ancestor != tmpDir {
		t.Fatalf("expected ancestor %q, got %q", tmpDir, ancestor)
	}

	origStat := osStat
	t.Cleanup(func() {
		osStat = origStat
	})
	osStat = mockAlwaysMissingStat
	rootAncestor := existingAncestor("/some/nonexistent/deep/path")
	if rootAncestor == "" {
		t.Fatal("expected root ancestor when all stats fail")
	}
}

func TestDefaultStatfsDirect(t *testing.T) {
	t.Parallel()

	var stat syscall.Statfs_t
	if err := defaultStatfs(t.TempDir(), &stat); err != nil {
		t.Fatalf("unexpected error from defaultStatfs: %v", err)
	}

	if err := defaultStatfs("/nonexistent-path-for-statfs", &stat); err == nil {
		t.Fatal("expected error from defaultStatfs with nonexistent path")
	}
}

func TestDefaultAvailBytesMockErrors(t *testing.T) {
	origStatfs := statfsFn
	t.Cleanup(func() {
		statfsFn = origStatfs
	})

	statfsFn = mockFailingStatfs
	if _, err := defaultAvailBytes(t.TempDir()); err == nil || !strings.Contains(err.Error(), "statfs") {
		t.Fatalf("expected statfs error, got %v", err)
	}

	statfsFn = mockZeroBlockStatfs
	if _, err := defaultAvailBytes(t.TempDir()); err == nil || !strings.Contains(err.Error(), "invalid filesystem block size") {
		t.Fatalf("expected zero block size error, got %v", err)
	}
}

func TestCheckFreeSpaceDefaultAvailFn(t *testing.T) {
	t.Parallel()

	mgr := &Manager{Runner: &fakeLVMRunner{}}
	if err := mgr.CheckFreeSpace(context.Background(), t.TempDir(), 1); err != nil {
		t.Fatalf("unexpected error with default avail bytes: %v", err)
	}
}

func TestCreateSnapshotErrors(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	canceledCtx, cancel := context.WithCancel(ctx)
	cancel()

	validCfg := &config.LVMConfig{
		VGName:       "vg-snap",
		LVName:       "lv-snap",
		SnapshotName: "snap-test",
		SnapshotSize: "10G",
		MountPoint:   filepath.Join(t.TempDir(), "mount"),
	}

	blockingFile := filepath.Join(t.TempDir(), "blocked")
	if err := os.WriteFile(blockingFile, []byte("block"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}
	invalidMountCfg := &config.LVMConfig{
		VGName:       "vg-snap",
		LVName:       "lv-snap",
		SnapshotName: "snap-test",
		SnapshotSize: "10G",
		MountPoint:   filepath.Join(blockingFile, "sub", "mount"),
	}

	cases := []struct {
		runner      *fakeLVMRunner
		ctx         context.Context
		cfg         *config.LVMConfig
		name        string
		errContains string
	}{
		{
			name:        "canceled context",
			ctx:         canceledCtx,
			cfg:         validCfg,
			runner:      &fakeLVMRunner{},
			errContains: "context canceled",
		},
		{
			name:        "nil config",
			ctx:         ctx,
			cfg:         nil,
			runner:      &fakeLVMRunner{},
			errContains: "lvm config must not be nil",
		},
		{
			name:        "pre-create cleanup error",
			ctx:         ctx,
			cfg:         validCfg,
			runner:      &fakeLVMRunner{failCommands: map[string]error{"umount": errors.New("umount failed")}},
			errContains: "pre-create snapshot cleanup",
		},
		{
			name:        "lvcreate error",
			ctx:         ctx,
			cfg:         validCfg,
			runner:      &fakeLVMRunner{failCommands: map[string]error{"lvcreate": errors.New("lvcreate failed")}},
			errContains: "create LVM snapshot",
		},
		{
			name:        "mkdirall mount point error",
			ctx:         ctx,
			cfg:         invalidMountCfg,
			runner:      &fakeLVMRunner{},
			errContains: "create snapshot mount point",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			mgr := NewManager(tc.runner)
			err := mgr.CreateSnapshot(tc.ctx, tc.cfg)
			if err == nil || !strings.Contains(err.Error(), tc.errContains) {
				t.Fatalf("expected error containing %q, got %v", tc.errContains, err)
			}
		})
	}
}

func TestCleanupSnapshotErrors(t *testing.T) {
	t.Parallel()

	validCfg := &config.LVMConfig{
		VGName:       "vg-clean",
		LVName:       "lv-clean",
		SnapshotName: "snap-clean",
		MountPoint:   "/mnt/snap",
	}

	cases := []struct {
		runner      *fakeLVMRunner
		cfg         *config.LVMConfig
		name        string
		errContains string
	}{
		{
			name:        "nil config",
			cfg:         nil,
			runner:      &fakeLVMRunner{},
			errContains: "lvm config must not be nil",
		},
		{
			name:        "empty fields config",
			cfg:         &config.LVMConfig{VGName: "vg-clean"},
			runner:      &fakeLVMRunner{},
			errContains: "must not be empty",
		},
		{
			name:        "umount failure",
			cfg:         validCfg,
			runner:      &fakeLVMRunner{failCommands: map[string]error{"umount": errors.New("umount error")}},
			errContains: "unmount snapshot",
		},
		{
			name:        "lvremove failure",
			cfg:         validCfg,
			runner:      &fakeLVMRunner{failCommands: map[string]error{"lvremove": errors.New("lvremove error")}},
			errContains: "remove LVM snapshot",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			mgr := NewManager(tc.runner)
			err := mgr.CleanupSnapshot(context.Background(), tc.cfg)
			if err == nil || !strings.Contains(err.Error(), tc.errContains) {
				t.Fatalf("expected error containing %q, got %v", tc.errContains, err)
			}
		})
	}
}
