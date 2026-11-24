package dac

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"

	"github.com/webstudiobond/backup-recovery-orchestrator/internal/config"
)

func TestApplyStackAndHostFiles(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	stackDir := filepath.Join(root, "angie")
	composePath := filepath.Join(stackDir, "docker-compose.yaml")
	confDir := filepath.Join(stackDir, "data", "conf.d")
	confFile := filepath.Join(confDir, "site.example.com.conf")
	secretFile := filepath.Join(stackDir, "secrets", "db_secret")
	symlinkPath := filepath.Join(confDir, "link.conf")
	hostCron := filepath.Join(root, "etc-cron-angie")

	for _, dir := range []string{confDir, filepath.Dir(secretFile)} {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			t.Fatalf("mkdir %q: %v", dir, err)
		}
	}
	for _, file := range []string{composePath, confFile, secretFile, hostCron} {
		if err := os.WriteFile(file, []byte("data"), 0o600); err != nil {
			t.Fatalf("write %q: %v", file, err)
		}
	}
	if err := os.Symlink(confFile, symlinkPath); err != nil {
		t.Fatalf("symlink %q: %v", symlinkPath, err)
	}

	var mu sync.Mutex
	var chownCalls []ownerRecord

	applier := &Applier{
		Lchown: func(path string, uid, gid int) error {
			mu.Lock()
			defer mu.Unlock()
			chownCalls = append(chownCalls, ownerRecord{path: path, uid: uid, gid: gid})
			return nil
		},
	}

	userCfg := config.UserConfig{Name: "angie", UID: 1002, GID: 1002}
	rules := []config.DACRule{
		{Path: ".", Owner: "root:stack", DirMode: "0750"},
		{Path: "docker-compose.yaml", Owner: "root:root", FileMode: "0600"},
		{Path: "secrets", Owner: "root:stack", DirMode: "0750", FileMode: "0440", Recursive: true},
		{Path: "data/conf.d", Owner: "root:angie", DirMode: "0750", FileMode: "0440", Recursive: true},
		{Path: "missing.env", Owner: "stack:stack", FileMode: "0600", Optional: true},
	}

	dryChanges, dryErr := applier.ApplyStack(context.Background(), stackDir, userCfg, rules, true)
	if dryErr != nil {
		t.Fatalf("unexpected dry-run error: %v", dryErr)
	}
	if len(chownCalls) != 0 {
		t.Fatalf("expected zero lchown calls during dry-run, got %d", len(chownCalls))
	}
	if len(dryChanges) == 0 {
		t.Fatalf("expected planned changes in dry-run")
	}
	assertDryChangesHavePrev(t, dryChanges)

	if _, err := applier.ApplyStack(context.Background(), stackDir, userCfg, rules, false); err != nil {
		t.Fatalf("unexpected error applying DAC: %v", err)
	}

	assertPerm(t, stackDir, 0o750)
	assertPerm(t, composePath, 0o600)
	assertPerm(t, filepath.Dir(secretFile), 0o750)
	assertPerm(t, secretFile, 0o440)
	assertPerm(t, confDir, 0o750)
	assertPerm(t, confFile, 0o440)

	hostItems := []config.HostFileConfig{
		{Path: hostCron, Owner: "0:0", FileMode: "0400"},
		{Path: filepath.Join(root, "absent-optional"), Owner: "0:0", FileMode: "0400", Optional: true},
	}
	if _, err := applier.ApplyHostFiles(context.Background(), userCfg, hostItems, false); err != nil {
		t.Fatalf("unexpected host_files DAC error: %v", err)
	}
	assertPerm(t, hostCron, 0o400)

	assertOwnerRecorded(t, chownCalls, composePath, 0, 0)
	assertOwnerRecorded(t, chownCalls, secretFile, 0, 1002)
	assertOwnerRecorded(t, chownCalls, hostCron, 0, 0)
	for _, rec := range chownCalls {
		if rec.path == symlinkPath {
			t.Fatalf("expected symlink %q not to be chowned", symlinkPath)
		}
	}
}

func TestApplyStackErrors(t *testing.T) {
	t.Parallel()

	stackDir := t.TempDir()
	userCfg := config.UserConfig{Name: "app_tester", UID: 1001, GID: 1001}

	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()

	cases := []struct {
		ctx         context.Context
		name        string
		targetPath  string
		errContains string
	}{
		{
			ctx:         canceledCtx,
			name:        "canceled context",
			errContains: "context canceled",
		},
		{
			ctx:         context.Background(),
			name:        "required missing path",
			targetPath:  "does-not-exist",
			errContains: "stat",
		},
		{
			ctx:         context.Background(),
			name:        "path traversal rejected",
			targetPath:  "../outside",
			errContains: "escapes stack_dir",
		},
		{
			ctx:         context.Background(),
			name:        "absolute path rejected",
			targetPath:  "/etc/passwd",
			errContains: "must be relative",
		},
	}

	applier := NewApplier()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var rules []config.DACRule
			if tc.targetPath != "" {
				rules = []config.DACRule{{Path: tc.targetPath, Owner: "admin:admin", FileMode: "0600"}}
			}
			_, err := applier.ApplyStack(tc.ctx, stackDir, userCfg, rules, false)
			if err == nil || !strings.Contains(err.Error(), tc.errContains) {
				t.Fatalf("expected error containing %q, got %v", tc.errContains, err)
			}
		})
	}
}

func TestApplySingleRuleBranches(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	targetFile := filepath.Join(root, "regular_file.txt")
	if err := os.WriteFile(targetFile, []byte("content"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}

	symlinkTarget := filepath.Join(root, "symlink_file.txt")
	if err := os.Symlink(targetFile, symlinkTarget); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	baseRoot, openErr := os.OpenRoot(root)
	if openErr != nil {
		t.Fatalf("open root: %v", openErr)
	}
	defer closeRoot(t, baseRoot)

	applier := NewApplier()
	userCfg := config.UserConfig{Name: "rule_app", UID: 1000, GID: 1000}

	changes, err := applier.applyRuleInRoot(context.Background(), baseRoot, root, "symlink_file.txt", userCfg, &config.DACRule{}, false)
	if err != nil {
		t.Fatalf("unexpected error for symlink rule: %v", err)
	}
	if len(changes) != 1 || !changes[0].Skipped || changes[0].SkipCause != "refusing to modify symlink target" {
		t.Fatalf("expected symlink refusal, got %+v", changes)
	}

	invalidOwnerRule := config.DACRule{Owner: "invalid_owner_spec"}
	if _, err := applier.applyRuleInRoot(context.Background(), baseRoot, root, "regular_file.txt", userCfg, &invalidOwnerRule, false); err == nil {
		t.Fatalf("expected error on invalid owner in single rule")
	}

	invalidDirModeRule := config.DACRule{DirMode: "0999"}
	if _, err := applier.applyRuleInRoot(context.Background(), baseRoot, root, "regular_file.txt", userCfg, &invalidDirModeRule, false); err == nil {
		t.Fatalf("expected error on invalid dir_mode in single rule")
	}

	invalidFileModeRule := config.DACRule{FileMode: "0999"}
	if _, err := applier.applyRuleInRoot(context.Background(), baseRoot, root, "regular_file.txt", userCfg, &invalidFileModeRule, false); err == nil {
		t.Fatalf("expected error on invalid file_mode in single rule")
	}

	failingApplier := &Applier{
		Chmod: func(_ string, _ os.FileMode) error {
			return errors.New("chmod failed")
		},
	}
	failingRule := config.DACRule{FileMode: "0644"}
	if _, err := failingApplier.applyRuleInRoot(context.Background(), baseRoot, root, "regular_file.txt", userCfg, &failingRule, false); err == nil {
		t.Fatalf("expected commit error in single rule")
	}
}

func TestPopulatePrevAttrsNil(t *testing.T) {
	t.Parallel()

	var ch Change
	populatePrevAttrs(&ch, nil)
	if ch.HasPrev {
		t.Fatalf("expected HasPrev to be false for nil info")
	}
}

func TestApplyBranchesWithMocks(t *testing.T) {
	origRootStat := rootStat
	origRootClose := rootClose
	origFileChmod := fileChmod
	origRootOpenRoot := rootOpenRoot
	t.Cleanup(func() {
		rootStat = origRootStat
		rootClose = origRootClose
		fileChmod = origFileChmod
		rootOpenRoot = origRootOpenRoot
	})

	stackDir := t.TempDir()
	userCfg := config.UserConfig{Name: "app_tester", UID: 1001, GID: 1001}
	applier := NewApplier()

	for _, d := range []string{"subfolder", "subclose", "subopen"} {
		if err := os.Mkdir(filepath.Join(stackDir, d), 0o750); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
	sub := filepath.Join(stackDir, "subfolder")
	deepFile := filepath.Join(sub, "deep.txt")
	if err := os.WriteFile(deepFile, []byte("ok"), 0o600); err != nil {
		t.Fatalf("write deep: %v", err)
	}
	symSub := filepath.Join(stackDir, "symsub")
	if err := os.Symlink(sub, symSub); err != nil {
		t.Fatalf("symlink symsub: %v", err)
	}
	fifoPath := filepath.Join(stackDir, "leaf.fifo")
	if err := syscall.Mkfifo(fifoPath, 0o600); err != nil {
		t.Fatalf("mkfifo: %v", err)
	}

	cases := []struct {
		setup   func()
		cleanup func()
		rule    config.DACRule
		wantErr bool
	}{
		{setup: func() { rootStat = mockFailingRootStat }, cleanup: func() { rootStat = origRootStat }, rule: config.DACRule{Path: ".", FileMode: "0600"}, wantErr: true},
		{setup: func() { rootStat = func(_ *os.Root, _ string) (os.FileInfo, error) { return nil, os.ErrNotExist } }, cleanup: func() { rootStat = origRootStat }, rule: config.DACRule{Path: ".", FileMode: "0600", Optional: true}, wantErr: false},
		{rule: config.DACRule{Path: ".", Owner: "invalid_spec"}, wantErr: true},
		{setup: func() { fileChmod = mockFailingFileChmod }, cleanup: func() { fileChmod = origFileChmod }, rule: config.DACRule{Path: ".", DirMode: "0700"}, wantErr: true},
		{setup: func() { rootClose = mockFailingRootClose }, cleanup: func() { rootClose = origRootClose }, rule: config.DACRule{Path: ".", DirMode: "0700"}, wantErr: true},
		{rule: config.DACRule{Path: ".", DirMode: "0700", Recursive: true}, wantErr: false},
		{rule: config.DACRule{Path: "subfolder", DirMode: "0700"}, wantErr: false},
		{setup: func() { rootClose = mockFailingRootClose }, cleanup: func() { rootClose = origRootClose }, rule: config.DACRule{Path: "subclose", DirMode: "0700"}, wantErr: true},
		{rule: config.DACRule{Path: "missing_int/file.txt", FileMode: "0600"}, wantErr: true},
		{rule: config.DACRule{Path: "missing_int/file.txt", FileMode: "0600", Optional: true}, wantErr: false},
		{rule: config.DACRule{Path: "missing_leaf.txt", FileMode: "0600"}, wantErr: true},
		{rule: config.DACRule{Path: "symsub/deep.txt", FileMode: "0600"}, wantErr: true},
		{rule: config.DACRule{Path: "leaf.fifo", FileMode: "0640"}, wantErr: false},
		{setup: func() { rootOpenRoot = mockFailingRootOpenRoot }, cleanup: func() { rootOpenRoot = origRootOpenRoot }, rule: config.DACRule{Path: "subopen", DirMode: "0700"}, wantErr: true},
		{setup: func() { rootClose = mockFailingRootClose }, cleanup: func() { rootClose = origRootClose }, rule: config.DACRule{Path: "subfolder/deep.txt", FileMode: "0600"}, wantErr: true},
	}
	for i, tc := range cases {
		if tc.setup != nil {
			tc.setup()
		}
		_, err := applier.ApplyStack(context.Background(), stackDir, userCfg, []config.DACRule{tc.rule}, false)
		if tc.cleanup != nil {
			tc.cleanup()
		}
		if (err != nil) != tc.wantErr {
			t.Fatalf("case %d (%+v): wantErr=%v, got=%v", i, tc.rule, tc.wantErr, err)
		}
	}

	fifoChanges, fifoErr := applier.ApplyStack(context.Background(), stackDir, userCfg, []config.DACRule{{Path: "leaf.fifo", FileMode: "0640"}}, false)
	if fifoErr != nil || len(fifoChanges) != 1 || !fifoChanges[0].Skipped {
		t.Fatalf("expected single skipped change for FIFO, got %+v, err: %v", fifoChanges, fifoErr)
	}
	assertPerm(t, fifoPath, 0o600)
}
