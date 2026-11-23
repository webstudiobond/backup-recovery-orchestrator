package mount

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/webstudiobond/backup-recovery-orchestrator/internal/cmdexec"
	"github.com/webstudiobond/backup-recovery-orchestrator/internal/config"
)

type fakeMountRunner struct {
	errByCmd map[string]error
	calls    [][]string
	mu       sync.Mutex
}

func (f *fakeMountRunner) Run(_ context.Context, spec *cmdexec.Spec) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, append([]string{spec.Name}, slices.Clone(spec.Args)...))
	if err, ok := f.errByCmd[spec.Name]; ok {
		return err
	}
	return nil
}

func (f *fakeMountRunner) Output(_ context.Context, _ *cmdexec.Spec) ([]byte, error) {
	return nil, nil
}

func TestEnsureReadyFUSE(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	t.Run("already mounted skips systemctl", func(t *testing.T) {
		t.Parallel()

		mountDir := t.TempDir()
		runner := &fakeMountRunner{}
		verifier := &Verifier{
			Runner:       runner,
			StatFSType:   func(_ string) (int64, error) { return fuseSuperMagic, nil },
			PollInterval: time.Millisecond,
		}

		dest := config.DestinationConfig{
			Name:           "cloud-primary",
			Type:           config.DestTypeFUSE,
			MountPoint:     mountDir,
			SystemdUnit:    "mnt-cloud-primary.mount",
			WaitTimeoutSec: 1,
		}
		if err := verifier.EnsureReady(ctx, &dest); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(runner.calls) != 0 {
			t.Fatalf("expected no systemctl calls when already FUSE-mounted, got %+v", runner.calls)
		}
	})

	t.Run("starts systemd unit and becomes active", func(t *testing.T) {
		t.Parallel()

		mountDir := t.TempDir()
		runner := &fakeMountRunner{}
		var checks int
		verifier := &Verifier{
			Runner: runner,
			StatFSType: func(_ string) (int64, error) {
				checks++
				if checks < 3 {
					return 0xef53, nil
				}
				return fuseSuperMagic, nil
			},
			PollInterval: time.Millisecond,
		}

		dest := config.DestinationConfig{
			Name:           "cloud-secondary",
			Type:           config.DestTypeFUSE,
			MountPoint:     mountDir,
			SystemdUnit:    "mnt-cloud-secondary.mount",
			WaitTimeoutSec: 1,
		}
		if err := verifier.EnsureReady(ctx, &dest); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(runner.calls) != 1 || !slices.Equal(runner.calls[0], []string{"systemctl", "start", "mnt-cloud-secondary.mount"}) {
			t.Fatalf("expected systemctl start call, got %+v", runner.calls)
		}
	})

	t.Run("timeout when never becomes FUSE", func(t *testing.T) {
		t.Parallel()

		mountDir := t.TempDir()
		runner := &fakeMountRunner{errByCmd: map[string]error{"systemctl": errors.New("unit masked")}}
		verifier := &Verifier{
			Runner:       runner,
			StatFSType:   func(_ string) (int64, error) { return 0xef53, nil },
			PollInterval: 5 * time.Millisecond,
		}

		waitCtx, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
		defer cancel()

		dest := config.DestinationConfig{
			Name:           "cloud-primary",
			Type:           config.DestTypeFUSE,
			MountPoint:     mountDir,
			SystemdUnit:    "mnt-cloud-primary.mount",
			WaitTimeoutSec: 1,
		}
		err := verifier.EnsureReady(waitCtx, &dest)
		if err == nil || !strings.Contains(err.Error(), "unit masked") || !strings.Contains(err.Error(), "not FUSE") {
			t.Fatalf("expected detailed mount error, got %v", err)
		}
	})
}

func TestEnsureReadyMountDirAndRclone(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	t.Run("generic mount verifies via mountpoint", func(t *testing.T) {
		t.Parallel()

		mountDir := t.TempDir()
		runner := &fakeMountRunner{}
		verifier := &Verifier{Runner: runner, PollInterval: time.Millisecond}

		dest := config.DestinationConfig{
			Name:       "nfs-box",
			Type:       config.DestTypeMount,
			MountPoint: mountDir,
		}
		if err := verifier.EnsureReady(ctx, &dest); err != nil {
			t.Fatalf("unexpected mount error: %v", err)
		}
		if len(runner.calls) != 1 || !slices.Equal(runner.calls[0], []string{"mountpoint", "-q", mountDir}) {
			t.Fatalf("expected mountpoint check, got %+v", runner.calls)
		}
	})

	t.Run("generic mount failure timeout without systemd unit", func(t *testing.T) {
		t.Parallel()

		mountDir := t.TempDir()
		runner := &fakeMountRunner{errByCmd: map[string]error{"mountpoint": errors.New("not mounted")}}
		verifier := &Verifier{Runner: runner}

		waitCtx, cancel := context.WithTimeout(ctx, 10*time.Millisecond)
		defer cancel()

		dest := config.DestinationConfig{
			Name:       "nfs-fail",
			Type:       config.DestTypeMount,
			MountPoint: mountDir,
		}
		err := verifier.EnsureReady(waitCtx, &dest)
		if err == nil || !strings.Contains(err.Error(), "not ready within") {
			t.Fatalf("expected mount timeout error, got %v", err)
		}
	})

	t.Run("dir and rclone destinations succeed without mount checks", func(t *testing.T) {
		t.Parallel()

		runner := &fakeMountRunner{}
		verifier := &Verifier{Runner: runner}

		for _, dest := range []config.DestinationConfig{
			{Name: "localdisk", Type: config.DestTypeDir, BackupDir: t.TempDir()},
			{Name: "s3", Type: config.DestTypeRclone, Remote: "s3:bucket/bro"},
		} {
			d := dest
			if err := verifier.EnsureReady(ctx, &d); err != nil {
				t.Fatalf("unexpected error for %s: %v", d.Name, err)
			}
		}
		if len(runner.calls) != 0 {
			t.Fatalf("expected zero mount calls, got %+v", runner.calls)
		}
	})
}

func TestEnsureReadyValidation(t *testing.T) {
	t.Parallel()

	verifier := NewVerifier(&fakeMountRunner{})
	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()

	fileAsDir := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(fileAsDir, []byte("x"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}

	cases := []struct {
		ctx  context.Context
		dest *config.DestinationConfig
		name string
	}{
		{ctx: context.Background(), dest: nil, name: "nil destination"},
		{ctx: context.Background(), dest: &config.DestinationConfig{Type: config.DestTypeFUSE, MountPoint: ""}, name: "empty mount point"},
		{ctx: context.Background(), dest: &config.DestinationConfig{Type: ""}, name: "missing explicit type"},
		{ctx: context.Background(), dest: &config.DestinationConfig{Type: config.DestTypeDir, BackupDir: fileAsDir}, name: "dir backup_dir is a file"},
		{ctx: context.Background(), dest: &config.DestinationConfig{Type: config.DestTypeDir, BackupDir: ""}, name: "dir empty backup_dir"},
		{ctx: context.Background(), dest: &config.DestinationConfig{Type: config.DestTypeMount, MountPoint: ""}, name: "mount empty mount point"},
		{ctx: canceledCtx, dest: &config.DestinationConfig{Type: config.DestTypeFUSE, MountPoint: t.TempDir()}, name: "canceled context"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if err := verifier.EnsureReady(tc.ctx, tc.dest); err == nil {
				t.Fatalf("expected error for %s", tc.name)
			}
		})
	}
}

func TestDefaultStatFSType(t *testing.T) {
	t.Parallel()

	if got := toInt64(uint32(42)); got != 42 {
		t.Fatalf("expected 42, got %d", got)
	}

	dir := t.TempDir()
	if _, err := defaultStatFSType(dir); err != nil {
		t.Fatalf("unexpected statfs error: %v", err)
	}

	nonExistent := filepath.Join(dir, "missing")
	if _, err := defaultStatFSType(nonExistent); err == nil {
		t.Fatal("expected error for nonexistent path")
	}
}

func TestNewVerifier(t *testing.T) {
	t.Parallel()

	v := NewVerifier(nil)
	if v == nil || v.Runner == nil || v.StatFSType == nil || v.PollInterval != defaultPoll {
		t.Fatalf("unexpected verifier state: %+v", v)
	}
}

func TestIsFUSEActive(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	filePath := filepath.Join(tempDir, "file")
	if err := os.WriteFile(filePath, []byte("x"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}

	tests := []struct {
		statFn     func(string) (int64, error)
		name       string
		path       string
		wantReason string
		wantActive bool
	}{
		{name: "stat error on missing path", path: filepath.Join(tempDir, "missing"), wantReason: "stat error"},
		{name: "not directory error", path: filePath, wantReason: "mount point is not a directory"},
		{name: "fallback to default statfs not fuse", path: tempDir, wantReason: "is not FUSE"},
		{name: "statfs error returned", path: tempDir, statFn: func(_ string) (int64, error) { return 0, errors.New("statfs fail") }, wantReason: "statfs error"},
		{name: "active fuse mount", path: tempDir, statFn: func(_ string) (int64, error) { return fuseSuperMagic, nil }, wantActive: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			v := &Verifier{StatFSType: tc.statFn}
			active, reason := v.isFUSEActive(tc.path)
			if active != tc.wantActive {
				t.Fatalf("expected active %v, got %v (reason: %q)", tc.wantActive, active, reason)
			}
			if tc.wantReason != "" && !strings.Contains(reason, tc.wantReason) {
				t.Fatalf("expected reason containing %q, got %q", tc.wantReason, reason)
			}
		})
	}
}
