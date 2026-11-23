package dac

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
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
		Chmod: defaultChmod,
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

	applier := NewApplier()
	userCfg := config.UserConfig{Name: "rule_app", UID: 1000, GID: 1000}

	changes, err := applier.applySingleRule(context.Background(), symlinkTarget, userCfg, &config.DACRule{}, false)
	if err != nil {
		t.Fatalf("unexpected error for symlink rule: %v", err)
	}
	if len(changes) != 1 || !changes[0].Skipped || changes[0].SkipCause != "refusing to modify symlink target" {
		t.Fatalf("expected symlink refusal, got %+v", changes)
	}

	invalidOwnerRule := config.DACRule{Owner: "invalid_owner_spec"}
	if _, err := applier.applySingleRule(context.Background(), targetFile, userCfg, &invalidOwnerRule, false); err == nil {
		t.Fatalf("expected error on invalid owner in single rule")
	}

	invalidDirModeRule := config.DACRule{DirMode: "0999"}
	if _, err := applier.applySingleRule(context.Background(), targetFile, userCfg, &invalidDirModeRule, false); err == nil {
		t.Fatalf("expected error on invalid dir_mode in single rule")
	}

	invalidFileModeRule := config.DACRule{FileMode: "0999"}
	if _, err := applier.applySingleRule(context.Background(), targetFile, userCfg, &invalidFileModeRule, false); err == nil {
		t.Fatalf("expected error on invalid file_mode in single rule")
	}

	failingApplier := &Applier{
		Chmod: func(_ string, _ os.FileMode) error {
			return errors.New("chmod failed")
		},
	}
	failingRule := config.DACRule{FileMode: "0644"}
	if _, err := failingApplier.applySingleRule(context.Background(), targetFile, userCfg, &failingRule, false); err == nil {
		t.Fatalf("expected commitChange error in single rule")
	}
}

func TestWalkAndApplyErrors(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	subDir := filepath.Join(root, "walk_sub")
	if err := os.MkdirAll(subDir, 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	firstFile := filepath.Join(subDir, "1_file.txt")
	secondFile := filepath.Join(subDir, "2_file.txt")
	for _, f := range []string{firstFile, secondFile} {
		if err := os.WriteFile(f, []byte("data"), 0o600); err != nil {
			t.Fatalf("write file: %v", err)
		}
	}

	applier := NewApplier()
	if _, err := applier.walkAndApply(context.Background(), "/nonexistent/dac_walk_root", 0, 0, false, 0, false, 0, false, false); err == nil {
		t.Fatalf("expected error walking nonexistent root")
	}

	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()

	_, ctxErr := applier.walkAndApply(canceledCtx, root, 0, 0, false, 0, false, 0, false, false)
	if ctxErr == nil {
		t.Fatalf("expected context canceled error during walk")
	}

	commitFailApplier := &Applier{
		Chmod: func(_ string, _ os.FileMode) error {
			return errors.New("walk chmod failed")
		},
	}
	_, commitErr := commitFailApplier.walkAndApply(context.Background(), root, 0, 0, false, 0o755, true, 0o644, true, false)
	if commitErr == nil {
		t.Fatalf("expected commitChange error during walk")
	}

	removedApplier := &Applier{
		Chmod: func(p string, _ os.FileMode) error {
			if filepath.Base(p) == "1_file.txt" {
				if rmErr := os.Remove(secondFile); rmErr != nil {
					return rmErr
				}
			}
			return nil
		},
	}
	_, statErr := removedApplier.walkAndApply(context.Background(), subDir, 0, 0, false, 0, false, 0o600, true, false)
	if statErr == nil {
		t.Fatalf("expected entry.Info error after file deletion")
	}
}

func TestPopulatePrevAttrsAndCommitChangeFallbacks(t *testing.T) {
	t.Parallel()

	var ch Change
	populatePrevAttrs(&ch, nil)
	if ch.HasPrev {
		t.Fatalf("expected HasPrev to be false for nil info")
	}

	tmpDir := t.TempDir()
	testPath := filepath.Join(tmpDir, "commit_test.txt")
	if err := os.WriteFile(testPath, []byte("data"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}

	defaultApplier := &Applier{}
	dryChange := Change{
		Path:     testPath,
		Mode:     0o644,
		HasMode:  true,
		UID:      os.Getuid(),
		GID:      os.Getgid(),
		HasOwner: true,
	}
	if err := defaultApplier.commitChange(&dryChange, true); err != nil {
		t.Fatalf("unexpected error in dry-run commitChange: %v", err)
	}

	skippedChange := Change{Path: testPath, Skipped: true}
	if err := defaultApplier.commitChange(&skippedChange, false); err != nil {
		t.Fatalf("unexpected error on skipped change commit: %v", err)
	}

	realChange := Change{
		Path:     testPath,
		Mode:     0o640,
		HasMode:  true,
		UID:      os.Getuid(),
		GID:      os.Getgid(),
		HasOwner: true,
	}
	if err := defaultApplier.commitChange(&realChange, false); err != nil {
		t.Fatalf("expected default syscall fallback to succeed: %v", err)
	}

	failChmodApplier := &Applier{
		Chmod: func(_ string, _ os.FileMode) error {
			return errors.New("chmod error")
		},
	}
	if err := failChmodApplier.commitChange(&realChange, false); err == nil {
		t.Fatalf("expected error from failChmodApplier")
	}

	failChownApplier := &Applier{
		Lchown: func(_ string, _ int, _ int) error {
			return errors.New("lchown error")
		},
	}
	if err := failChownApplier.commitChange(&realChange, false); err == nil {
		t.Fatalf("expected error from failChownApplier")
	}
}
