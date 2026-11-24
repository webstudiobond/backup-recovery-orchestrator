package hooks

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/webstudiobond/backup-recovery-orchestrator/internal/config"
	"github.com/webstudiobond/backup-recovery-orchestrator/internal/docker"
)

func TestRunPhaseSuccess(t *testing.T) {
	t.Parallel()

	stackDir := t.TempDir()
	composeFile := filepath.Join(stackDir, "docker-compose.yaml")
	checkpointDir := filepath.Join(stackDir, "daemon", "data", "backup_checkpoint")
	markerFile := filepath.Join(checkpointDir, "CURRENT")

	if err := os.MkdirAll(checkpointDir, 0o750); err != nil {
		t.Fatalf("mkdir checkpoint: %v", err)
	}

	go func() {
		time.Sleep(10 * time.Millisecond)
		if writeErr := os.WriteFile(markerFile, []byte("ok"), 0o600); writeErr != nil {
			t.Errorf("write marker: %v", writeErr)
		}
	}()

	fake := &recordedDocker{}
	runner := &Runner{
		Docker:       docker.NewClient(fake),
		PollInterval: 2 * time.Millisecond,
	}

	preSteps := []config.HookStep{
		{Action: "compose_kill", Service: "daemon", Signal: "USR2"},
		{Action: "wait_for_file", Path: markerFile, TimeoutSec: 2},
		{Action: "compose_exec", Service: "daemon", Command: []string{"echo", "ready"}},
		{Action: "compose_run", Service: "helper", Command: []string{"check"}},
	}

	if err := runner.RunPhase(context.Background(), "pre_snapshot", stackDir, composeFile, preSteps); err != nil {
		t.Fatalf("unexpected pre_snapshot hook error: %v", err)
	}

	if len(fake.args) != 3 {
		t.Fatalf("expected 3 docker calls, got %d", len(fake.args))
	}

	postSteps := []config.HookStep{
		{Action: "remove_path", Path: checkpointDir},
	}
	if err := runner.RunPhase(context.Background(), "post_snapshot", stackDir, composeFile, postSteps); err != nil {
		t.Fatalf("unexpected post_snapshot hook error: %v", err)
	}
	if _, statErr := os.Stat(checkpointDir); !os.IsNotExist(statErr) {
		t.Fatalf("expected checkpoint dir %q to be removed, statErr=%v", checkpointDir, statErr)
	}
}

func TestRunPhaseErrors(t *testing.T) {
	t.Parallel()

	stackDir := t.TempDir()
	composeFile := filepath.Join(stackDir, "docker-compose.yaml")

	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()

	cases := []struct {
		ctx         context.Context
		name        string
		removePath  string
		errContains string
		steps       []config.HookStep
	}{
		{
			ctx:         canceledCtx,
			name:        "canceled context",
			steps:       []config.HookStep{{Action: "cancel_probe"}},
			errContains: "canceled",
		},
		{
			ctx:         context.Background(),
			name:        "remove_path refuses stack root",
			removePath:  stackDir,
			errContains: "outside stack_dir",
		},
		{
			ctx:         context.Background(),
			name:        "remove_path refuses escaping path",
			removePath:  filepath.Dir(stackDir),
			errContains: "outside stack_dir",
		},
		{
			ctx:         context.Background(),
			name:        "wait_for_file times out",
			steps:       []config.HookStep{{Action: "wait_for_file", Path: filepath.Join(stackDir, "missing"), TimeoutSec: 1}},
			errContains: "timed out",
		},
		{
			ctx:         context.Background(),
			name:        "unsupported action",
			steps:       []config.HookStep{{Action: "unknown_hook"}},
			errContains: "unsupported hook action",
		},
	}

	runner := &Runner{
		Docker:       docker.NewClient(&recordedDocker{}),
		PollInterval: 5 * time.Millisecond,
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			steps := tc.steps
			if tc.removePath != "" {
				steps = []config.HookStep{{Action: "remove_path", Path: tc.removePath}}
			}
			err := runner.RunPhase(tc.ctx, "pre_snapshot", stackDir, composeFile, steps)
			if err == nil || !strings.Contains(err.Error(), tc.errContains) {
				t.Fatalf("expected error containing %q, got %v", tc.errContains, err)
			}
		})
	}
}

func TestNewRunnerDefaults(t *testing.T) {
	t.Parallel()

	r1 := NewRunner(nil)
	if r1.Docker == nil || r1.PollInterval != defaultWaitPollInterval {
		t.Fatalf("expected default runner and poll interval, got docker=%v interval=%v", r1.Docker, r1.PollInterval)
	}

	customClient := docker.NewClient(nil)
	r2 := NewRunner(customClient)
	if r2.Docker != customClient {
		t.Fatalf("expected injected docker client")
	}
}

func TestRunStepDockerErrors(t *testing.T) {
	t.Parallel()

	failingDocker := &recordedDocker{runErr: errors.New("simulated docker error")}
	runner := &Runner{
		Docker:       docker.NewClient(failingDocker),
		PollInterval: time.Millisecond,
	}

	cases := []struct {
		name        string
		action      string
		service     string
		signal      string
		errContains string
		command     []string
	}{
		{
			name:        "compose kill error",
			action:      "compose_kill",
			service:     "kill-svc",
			signal:      "USR1",
			errContains: "compose kill",
		},
		{
			name:        "compose exec error",
			action:      "compose_exec",
			service:     "exec-svc",
			command:     []string{"true"},
			errContains: "compose exec",
		},
		{
			name:        "compose run error",
			action:      "compose_run",
			service:     "run-svc",
			command:     []string{"true"},
			errContains: "compose run",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			step := &config.HookStep{
				Action:  tc.action,
				Service: tc.service,
				Signal:  tc.signal,
				Command: tc.command,
			}
			err := runner.runStep(context.Background(), "/home/app", "/home/app/docker-compose.yaml", step)
			if err == nil || !strings.Contains(err.Error(), tc.errContains) {
				t.Fatalf("expected error containing %q, got %v", tc.errContains, err)
			}
		})
	}
}

func TestWaitForFileDefaultsAndErrors(t *testing.T) {
	t.Parallel()

	runner := &Runner{
		Docker:       docker.NewClient(&recordedDocker{}),
		PollInterval: 0,
	}

	tmpFile := filepath.Join(t.TempDir(), "ready")
	if err := os.WriteFile(tmpFile, []byte("ok"), 0o600); err != nil {
		t.Fatalf("write temp: %v", err)
	}

	if err := runner.waitForFile(context.Background(), tmpFile, time.Second); err != nil {
		t.Fatalf("expected waitForFile to succeed with default poll interval, got %v", err)
	}

	if err := runner.waitForFile(context.Background(), tmpFile, 0); err == nil {
		t.Fatal("expected error on non-positive timeout")
	}
}

func TestRemovePathMockErrors(t *testing.T) {
	origAbs := filepathAbs
	origOpenRoot := openRoot
	origSameFile := rootSameFile
	t.Cleanup(func() {
		filepathAbs = origAbs
		openRoot = origOpenRoot
		rootSameFile = origSameFile
	})

	stackDir := t.TempDir()
	nestedDir := filepath.Join(stackDir, "sub", "dir")
	if err := os.MkdirAll(nestedDir, 0o750); err != nil {
		t.Fatalf("mkdir nested: %v", err)
	}
	validTarget := filepath.Join(stackDir, "child")
	nestedTarget := filepath.Join(nestedDir, "leaf")
	failTarget := filepath.Join(stackDir, "fail_target_child")

	cases := []struct {
		absMock      func(string) (string, error)
		openRootMock func(string) (*os.Root, error)
		sameFileMock func(os.FileInfo, os.FileInfo) bool
		name         string
		targetPath   string
		errContains  string
	}{
		{
			name:        "stack dir abs failure",
			targetPath:  validTarget,
			absMock:     mockFailingAbs,
			errContains: "resolve stack_dir",
		},
		{
			name:        "remove path abs failure",
			targetPath:  failTarget,
			absMock:     mockFailingTargetPathAbs,
			errContains: "resolve remove_path",
		},
		{
			name:         "open root failure",
			targetPath:   validTarget,
			openRootMock: mockFailingOpenRoot,
			errContains:  "open stack_dir",
		},
		{
			name:         "concurrent replacement detected",
			targetPath:   nestedTarget,
			sameFileMock: mockDifferentSameFile,
			errContains:  "concurrent path replacement",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			filepathAbs = origAbs
			openRoot = origOpenRoot
			rootSameFile = origSameFile
			if tc.absMock != nil {
				filepathAbs = tc.absMock
			}
			if tc.openRootMock != nil {
				openRoot = tc.openRootMock
			}
			if tc.sameFileMock != nil {
				rootSameFile = tc.sameFileMock
			}

			err := removePathWithinStack(stackDir, tc.targetPath)
			if err == nil || !strings.Contains(err.Error(), tc.errContains) {
				t.Fatalf("expected error containing %q, got %v", tc.errContains, err)
			}
		})
	}
}

func TestRemovePathRejectsSymlinksAndAllowsMissing(t *testing.T) {
	t.Parallel()

	baseDir := t.TempDir()
	stackDir := filepath.Join(baseDir, "stack")
	outsideDir := filepath.Join(baseDir, "outside")
	outsideSecret := filepath.Join(outsideDir, "keep.txt")
	internalSecret := filepath.Join(stackDir, "secrets", "key.txt")

	for _, dir := range []string{stackDir, outsideDir, filepath.Dir(internalSecret)} {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			t.Fatalf("mkdir %q: %v", dir, err)
		}
	}
	for _, file := range []string{outsideSecret, internalSecret} {
		if err := os.WriteFile(file, []byte("secret"), 0o600); err != nil {
			t.Fatalf("write %q: %v", file, err)
		}
	}

	symlinkOutside := filepath.Join(stackDir, "escape_dir")
	if err := os.Symlink(outsideDir, symlinkOutside); err != nil {
		t.Fatalf("symlink outside: %v", err)
	}
	symlinkInternal := filepath.Join(stackDir, "internal_link")
	if err := os.Symlink(filepath.Dir(internalSecret), symlinkInternal); err != nil {
		t.Fatalf("symlink internal: %v", err)
	}
	leafSymlink := filepath.Join(stackDir, "leaf_link")
	if err := os.Symlink(outsideSecret, leafSymlink); err != nil {
		t.Fatalf("symlink leaf: %v", err)
	}
	regFile := filepath.Join(stackDir, "regular_file")
	if err := os.WriteFile(regFile, []byte("x"), 0o600); err != nil {
		t.Fatalf("write regular file: %v", err)
	}

	cases := []struct {
		name        string
		target      string
		errContains string
	}{
		{
			name:        "intermediate external symlink rejected",
			target:      filepath.Join(symlinkOutside, "keep.txt"),
			errContains: "refusing symlink",
		},
		{
			name:        "intermediate internal symlink rejected",
			target:      filepath.Join(symlinkInternal, "key.txt"),
			errContains: "refusing symlink",
		},
		{
			name:        "leaf symlink rejected",
			target:      leafSymlink,
			errContains: "refusing symlink target",
		},
		{
			name:        "non-directory intermediate segment rejected",
			target:      filepath.Join(regFile, "child"),
			errContains: "non-directory segment",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := removePathWithinStack(stackDir, tc.target)
			if err == nil || !strings.Contains(err.Error(), tc.errContains) {
				t.Fatalf("expected error containing %q, got %v", tc.errContains, err)
			}
		})
	}

	assertFilesExist(t, outsideSecret, internalSecret)
}

func TestRemovePathAllowsMissing(t *testing.T) {
	t.Parallel()

	baseDir := t.TempDir()
	stackDir := filepath.Join(baseDir, "stack")
	if err := os.MkdirAll(stackDir, 0o750); err != nil {
		t.Fatalf("mkdir stack: %v", err)
	}

	missingCases := []struct {
		name   string
		root   string
		target string
	}{
		{
			name:   "missing stack_dir",
			root:   filepath.Join(baseDir, "nonexistent_stack"),
			target: filepath.Join(baseDir, "nonexistent_stack", "sub"),
		},
		{
			name:   "missing intermediate directory",
			root:   stackDir,
			target: filepath.Join(stackDir, "missing_dir", "missing_leaf"),
		},
		{
			name:   "missing leaf target",
			root:   stackDir,
			target: filepath.Join(stackDir, "missing_leaf"),
		},
	}

	for _, tc := range missingCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if err := removePathWithinStack(tc.root, tc.target); err != nil {
				t.Fatalf("expected nil for %s, got %v", tc.name, err)
			}
		})
	}
}

func TestOpenVerifiedChildDirRejectsFIFOWithoutBlocking(t *testing.T) {
	t.Parallel()

	stackDir := t.TempDir()
	fifoPath := filepath.Join(stackDir, "pipe.fifo")
	if err := syscall.Mkfifo(fifoPath, 0o600); err != nil {
		t.Fatalf("mkfifo: %v", err)
	}

	stackRoot, openErr := os.OpenRoot(stackDir)
	if openErr != nil {
		t.Fatalf("open root: %v", openErr)
	}
	defer func() {
		if err := stackRoot.Close(); err != nil {
			t.Fatalf("close root: %v", err)
		}
	}()

	if _, _, err := openVerifiedChildDir(stackRoot, "pipe.fifo", nil, fifoPath); err == nil {
		t.Fatalf("expected openVerifiedChildDir on FIFO to fail without blocking")
	}
}

func TestRemovePathBranchesWithMocks(t *testing.T) {
	origRootClose := rootClose
	origRootRemoveAll := rootRemoveAll
	origRootOpenFile := rootOpenFile
	origFileStat := fileStat
	origSameFile := rootSameFile
	t.Cleanup(func() {
		rootClose = origRootClose
		rootRemoveAll = origRootRemoveAll
		rootOpenFile = origRootOpenFile
		fileStat = origFileStat
		rootSameFile = origSameFile
	})

	stackDir := t.TempDir()
	subDir := filepath.Join(stackDir, "sub")
	if err := os.MkdirAll(subDir, 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	for _, name := range []string{"a", "b", "c", "d", "e"} {
		if err := os.WriteFile(filepath.Join(subDir, name), []byte("x"), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	cases := []struct {
		setup func()
		fn    func() error
	}{
		{func() { rootClose = mockFailingRootClose }, func() error { return removePathWithinStack(stackDir, filepath.Join(subDir, "a")) }},
		{func() { rootClose = origRootClose; rootRemoveAll = mockFailingRootRemoveAll }, func() error { return removePathWithinStack(stackDir, filepath.Join(subDir, "b")) }},
		{func() { rootRemoveAll = origRootRemoveAll; rootOpenFile = mockFailingRootOpenFile }, func() error { return removePathWithinStack(stackDir, filepath.Join(subDir, "c")) }},
		{func() { rootOpenFile = origRootOpenFile; fileStat = mockFailingFileStat }, func() error { return removePathWithinStack(stackDir, filepath.Join(subDir, "d")) }},
		{func() { fileStat = origFileStat; rootSameFile = mockDifferentSameFile }, func() error { return removePathWithinStack(stackDir, filepath.Join(subDir, "e")) }},
	}
	for i, tc := range cases {
		tc.setup()
		if err := tc.fn(); err == nil {
			t.Fatalf("expected error for case %d", i)
		}
	}
}

func TestRemovePathDirBranchesWithMocks(t *testing.T) {
	origRootOpenFile := rootOpenFile
	origSameFile := rootSameFile
	origRootOpenRoot := rootOpenRoot
	origRootStat := rootStat
	t.Cleanup(func() {
		rootOpenFile = origRootOpenFile
		rootSameFile = origSameFile
		rootOpenRoot = origRootOpenRoot
		rootStat = origRootStat
	})

	stackDir := t.TempDir()
	subDir := filepath.Join(stackDir, "sub")
	if err := os.MkdirAll(subDir, 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(subDir, "a"), []byte("x"), 0o600); err != nil {
		t.Fatalf("write a: %v", err)
	}

	root, openErr := os.OpenRoot(stackDir)
	if openErr != nil {
		t.Fatalf("open root: %v", openErr)
	}
	defer func() {
		if cErr := root.Close(); cErr != nil {
			t.Fatalf("close root: %v", cErr)
		}
	}()
	info, statErr := root.Lstat("sub")
	if statErr != nil {
		t.Fatalf("lstat: %v", statErr)
	}

	cases := []struct {
		fn      func() error
		wantErr bool
	}{
		{fn: func() error {
			rootOpenRoot = mockFailingRootOpenRoot
			defer func() { rootOpenRoot = origRootOpenRoot }()
			_, _, err := openVerifiedChildDirFallback(root, "sub", info, filepath.Join(subDir, "a"))
			return err
		}, wantErr: true},
		{fn: func() error {
			rootStat = mockFailingRootStat
			defer func() { rootStat = origRootStat }()
			_, _, err := openVerifiedChildDirFallback(root, "sub", info, filepath.Join(subDir, "a"))
			return err
		}, wantErr: true},
		{fn: func() error {
			rootOpenFile = func(_ *os.Root, _ string, _ int, _ os.FileMode) (*os.File, error) { return nil, os.ErrNotExist }
			defer func() { rootOpenFile = origRootOpenFile }()
			res, found, err := openVerifiedChildDir(root, "sub", info, "orig")
			if err != nil || found || res != nil {
				return errors.New("expected nil, false, nil")
			}
			return nil
		}, wantErr: false},
		{fn: func() error {
			rootOpenRoot = func(_ *os.Root, _ string) (*os.Root, error) { return nil, os.ErrNotExist }
			defer func() { rootOpenRoot = origRootOpenRoot }()
			res, found, err := openVerifiedChildDirFallback(root, "sub", info, "orig")
			if err != nil || found || res != nil {
				return errors.New("expected nil, false, nil")
			}
			return nil
		}, wantErr: false},
		{fn: func() error {
			rootSameFile = mockDifferentSameFile
			defer func() { rootSameFile = origSameFile }()
			_, _, err := openVerifiedChildDirFallback(root, "sub", info, "orig")
			return err
		}, wantErr: true},
	}
	for i, tc := range cases {
		err := tc.fn()
		if (err != nil) != tc.wantErr {
			t.Fatalf("case %d: wantErr=%v, got=%v", i, tc.wantErr, err)
		}
	}
}

func TestRemovePathSubpathBranchesWithMocks(t *testing.T) {
	origRootClose := rootClose
	origRootLstat := rootLstat
	t.Cleanup(func() {
		rootClose = origRootClose
		rootLstat = origRootLstat
	})

	stackDir := t.TempDir()
	subDir := filepath.Join(stackDir, "sub")
	if err := os.MkdirAll(subDir, 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	for _, name := range []string{"a", "nested.txt"} {
		if err := os.WriteFile(filepath.Join(subDir, name), []byte("x"), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	sub2 := filepath.Join(subDir, "sub2")
	if err := os.Mkdir(sub2, 0o750); err != nil {
		t.Fatalf("mkdir sub2: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sub2, "nested.txt"), []byte("n"), 0o600); err != nil {
		t.Fatalf("write nested: %v", err)
	}

	root, openErr := os.OpenRoot(stackDir)
	if openErr != nil {
		t.Fatalf("open root: %v", openErr)
	}
	defer func() {
		if cErr := root.Close(); cErr != nil {
			t.Fatalf("close root: %v", cErr)
		}
	}()

	cases := []struct {
		fn func() error
	}{
		{fn: func() error {
			rootClose = mockFailingRootClose
			defer func() { rootClose = origRootClose }()
			return removeSubpathInRoot(root, "sub/sub2/nested.txt", "abs", "orig")
		}},
		{fn: func() error {
			rootLstat = mockFailingRootLstat
			defer func() { rootLstat = origRootLstat }()
			return removeSubpathInRoot(root, "a", "abs", "orig")
		}},
		{fn: func() error {
			rootLstat = mockFailingRootLstat
			defer func() { rootLstat = origRootLstat }()
			_, _, err := openChildDirNoSymlink(root, "sub", "orig")
			return err
		}},
	}
	for i, tc := range cases {
		if err := tc.fn(); err == nil {
			t.Fatalf("expected error for case %d", i)
		}
	}
}
