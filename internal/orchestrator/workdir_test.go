package orchestrator

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/webstudiobond/backup-recovery-orchestrator/internal/config"
)

type fakeWorkFileInfo struct {
	sys  any
	name string
	size int64
	mode fs.FileMode
}

func (f fakeWorkFileInfo) Name() string       { return f.name }
func (f fakeWorkFileInfo) Size() int64        { return f.size }
func (f fakeWorkFileInfo) Mode() fs.FileMode  { return f.mode }
func (f fakeWorkFileInfo) ModTime() time.Time { return time.Time{} }
func (f fakeWorkFileInfo) IsDir() bool        { return f.mode.IsDir() }
func (f fakeWorkFileInfo) Sys() any           { return f.sys }

func mockMaxInt64FileStat(_ string) (fs.FileInfo, error) {
	return fakeWorkFileInfo{name: "huge.tar", size: math.MaxInt64, mode: 0o600}, nil
}

func mockZeroRand(_ []byte) (int, error) {
	return workSubdirRandBytes, nil
}

func mockFailingRand(_ []byte) (int, error) {
	return 0, errors.New("simulated entropy failure")
}

func mockZeroEuid() int {
	return 0
}

func mockInvalidEuid() int {
	return -1
}

func mockMismatchedEuid() int {
	if os.Geteuid() == 0 {
		return 1000
	}
	return 0
}

func mockSameFS(_, _ string) bool {
	return true
}

func mockDifferentFS(_, _ string) bool {
	return false
}

func TestResolveRestoreWorkDir(t *testing.T) {
	t.Parallel()

	defaultCfg := &config.GlobalConfig{BackupRoot: "/backups"}
	if got := resolveRestoreWorkDir(defaultCfg); got != "/backups/.bro-work" {
		t.Fatalf("expected default work dir under backup_root, got %q", got)
	}

	customCfg := &config.GlobalConfig{BackupRoot: "/backups", RestoreWorkDir: "/home/.bro-work/"}
	if got := resolveRestoreWorkDir(customCfg); got != "/home/.bro-work" {
		t.Fatalf("expected custom restore_work_dir, got %q", got)
	}
}

func TestValidateRestoreWorkDirSeparation(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		workDir  string
		stackDir string
		wantErr  bool
	}{
		{name: "sibling dirs", workDir: "/home/.bro-work", stackDir: "/home/app-one", wantErr: false},
		{name: "different mount", workDir: "/backups/.bro-work", stackDir: "/home/app-two", wantErr: false},
		{name: "equal path", workDir: "/home/app-three", stackDir: "/home/app-three", wantErr: true},
		{name: "work inside stack", workDir: "/home/app-four/.bro-work", stackDir: "/home/app-four", wantErr: true},
		{name: "stack inside work", workDir: "/home", stackDir: "/home/app-five", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := validateRestoreWorkDirSeparation(tc.workDir, tc.stackDir)
			if (err != nil) != tc.wantErr {
				t.Fatalf("validateRestoreWorkDirSeparation(%q, %q) err = %v, wantErr = %v", tc.workDir, tc.stackDir, err, tc.wantErr)
			}
		})
	}
}

func TestWorkDirEnsurePruneAndCreate(t *testing.T) {
	t.Parallel()

	base := t.TempDir()
	workDir := backupWorkDir(base)

	subDir, err := createWorkSubdir(workDir, stageDirPrefix+"test-*")
	if err != nil {
		t.Fatalf("createWorkSubdir failed: %v", err)
	}
	if !strings.HasPrefix(subDir, workDir+string(filepath.Separator)) {
		t.Fatalf("expected subdir %q inside %q", subDir, workDir)
	}

	literalDir, litErr := createWorkSubdir(workDir, stageDirPrefix+"literal")
	if litErr != nil {
		t.Fatalf("createWorkSubdir literal failed: %v", litErr)
	}
	if !strings.HasPrefix(literalDir, filepath.Join(workDir, stageDirPrefix+"literal")) {
		t.Fatalf("unexpected literal subdir %q", literalDir)
	}

	staleRestore := filepath.Join(workDir, restoreDirPrefix+"old")
	staleAngie := filepath.Join(workDir, angieRollbackPrefix+"old")
	keepDir := filepath.Join(workDir, "unrelated_keep")
	for _, d := range []string{staleRestore, staleAngie, keepDir} {
		if mkErr := os.MkdirAll(d, 0o700); mkErr != nil {
			t.Fatalf("mkdir %q: %v", d, mkErr)
		}
	}

	if pruneErr := pruneStaleWorkDirs(workDir); pruneErr != nil {
		t.Fatalf("pruneStaleWorkDirs failed: %v", pruneErr)
	}
	for _, removed := range []string{subDir, literalDir, staleRestore, staleAngie} {
		if _, statErr := os.Stat(removed); !os.IsNotExist(statErr) {
			t.Fatalf("expected stale work entry %q to be removed, statErr=%v", removed, statErr)
		}
	}
	if _, statErr := os.Stat(keepDir); statErr != nil {
		t.Fatalf("expected unrelated directory %q to be preserved: %v", keepDir, statErr)
	}
}

func TestEnsureWorkRootRejectsInvalidTargets(t *testing.T) {
	t.Parallel()

	base := t.TempDir()
	if _, err := ensureWorkRoot("/"); err == nil || !strings.Contains(err.Error(), "refusing unsafe work directory") {
		t.Fatalf("expected unsafe root directory error, got %v", err)
	}

	realTarget := filepath.Join(base, "real_target")
	if mkErr := os.MkdirAll(realTarget, 0o700); mkErr != nil {
		t.Fatalf("mkdir real_target: %v", mkErr)
	}
	symWork := filepath.Join(base, "sym_work")
	if symErr := os.Symlink(realTarget, symWork); symErr != nil {
		t.Fatalf("symlink: %v", symErr)
	}
	if _, symErr := ensureWorkRoot(symWork); symErr == nil || !strings.Contains(symErr.Error(), "must be a real directory") {
		t.Fatalf("expected symlink work dir rejection, got %v", symErr)
	}

	fileWork := filepath.Join(base, "file_work")
	if wErr := os.WriteFile(fileWork, []byte("x"), 0o600); wErr != nil {
		t.Fatalf("write file_work: %v", wErr)
	}
	if _, fileErr := ensureWorkRoot(fileWork); fileErr == nil || !strings.Contains(fileErr.Error(), "must be a real directory") {
		t.Fatalf("expected regular file work dir rejection, got %v", fileErr)
	}
}

func TestCreateWorkSubdirInvalidPatterns(t *testing.T) {
	t.Parallel()

	target := t.TempDir()
	invalidPatterns := []string{"", ".hidden", "../escape", "sub/dir", `sub\dir`}
	for _, pat := range invalidPatterns {
		if _, err := createWorkSubdir(target, pat); err == nil || !strings.Contains(err.Error(), "invalid work directory pattern") {
			t.Fatalf("expected invalid pattern error for %q, got %v", pat, err)
		}
	}
}

func TestWorkDirMockErrors(t *testing.T) {
	origOpenRoot := osOpenRoot
	origRootStat := rootStat
	origSameFile := rootSameFile
	origRootClose := rootClose
	origRootRemoveAll := rootRemoveAll
	origRootOpenDot := rootOpenDot
	origOsLstat := osLstat
	origOsMkdirAll := osMkdirAll
	origOsGeteuid := osGeteuid
	origRandRead := randRead
	origRootMkdir := rootMkdir
	t.Cleanup(func() {
		osOpenRoot = origOpenRoot
		rootStat = origRootStat
		rootSameFile = origSameFile
		rootClose = origRootClose
		rootRemoveAll = origRootRemoveAll
		rootOpenDot = origRootOpenDot
		osLstat = origOsLstat
		osMkdirAll = origOsMkdirAll
		osGeteuid = origOsGeteuid
		randRead = origRandRead
		rootMkdir = origRootMkdir
	})

	workDir := filepath.Join(t.TempDir(), workDirName)
	if err := os.MkdirAll(filepath.Join(workDir, stageDirPrefix+"stale"), 0o700); err != nil {
		t.Fatalf("mkdir stale: %v", err)
	}
	missingWorkDir := filepath.Join(t.TempDir(), "missing", workDirName)

	cases := []struct {
		fn      func() error
		wantErr string
	}{
		{
			fn: func() error {
				osMkdirAll = func(string, os.FileMode) error { return os.ErrPermission }
				defer func() { osMkdirAll = origOsMkdirAll }()
				_, err := ensureWorkRoot(missingWorkDir)
				return err
			},
			wantErr: "create work directory",
		},
		{
			fn: func() error {
				calls := 0
				osLstat = func(string) (os.FileInfo, error) {
					calls++
					if calls == 1 {
						return nil, os.ErrNotExist
					}
					return nil, os.ErrPermission
				}
				defer func() { osLstat = origOsLstat }()
				_, err := ensureWorkRoot(filepath.Join(t.TempDir(), "created_stat_fail", workDirName))
				return err
			},
			wantErr: "stat created work directory",
		},
		{
			fn: func() error {
				osOpenRoot = mockFailingOpenRoot
				defer func() { osOpenRoot = origOpenRoot }()
				_, err := ensureWorkRoot(workDir)
				return err
			},
			wantErr: "open work directory",
		},
		{
			fn: func() error {
				rootStat = mockFailingRootStat
				defer func() { rootStat = origRootStat }()
				_, err := ensureWorkRoot(workDir)
				return err
			},
			wantErr: "stat opened work directory",
		},
		{
			fn: func() error {
				rootSameFile = mockDifferentSameFile
				defer func() { rootSameFile = origSameFile }()
				_, err := ensureWorkRoot(workDir)
				return err
			},
			wantErr: "concurrent path replacement",
		},
		{
			fn: func() error {
				return verifyWorkRootOwnershipAndMode(workDir, fakeWorkFileInfo{name: ".", mode: fs.ModeDir | 0o700})
			},
			wantErr: "missing ownership metadata",
		},
		{
			fn: func() error {
				osGeteuid = mockZeroEuid
				defer func() { osGeteuid = origOsGeteuid }()
				return verifyWorkRootOwnershipAndMode(workDir, fakeWorkFileInfo{
					name: ".",
					mode: fs.ModeDir | 0o755,
					sys:  &syscall.Stat_t{Uid: 0},
				})
			},
			wantErr: "overly permissive mode",
		},
		{
			fn: func() error {
				osGeteuid = mockMismatchedEuid
				defer func() { osGeteuid = origOsGeteuid }()
				_, err := ensureWorkRoot(workDir)
				return err
			},
			wantErr: "owned by uid",
		},
		{
			fn: func() error {
				osGeteuid = mockInvalidEuid
				defer func() { osGeteuid = origOsGeteuid }()
				_, err := ensureWorkRoot(workDir)
				return err
			},
			wantErr: "owned by uid",
		},
		{
			fn: func() error {
				rootOpenDot = mockFailingRootOpenDot
				defer func() { rootOpenDot = origRootOpenDot }()
				return pruneStaleWorkDirs(workDir)
			},
			wantErr: "read work directory",
		},
		{
			fn: func() error {
				rootRemoveAll = mockFailingRootRemoveAll
				defer func() { rootRemoveAll = origRootRemoveAll }()
				return pruneStaleWorkDirs(workDir)
			},
			wantErr: "remove stale work entry",
		},
		{
			fn: func() error {
				rootClose = mockFailingRootClose
				defer func() { rootClose = origRootClose }()
				return pruneStaleWorkDirs(workDir)
			},
			wantErr: "close work directory",
		},
		{
			fn: func() error {
				osOpenRoot = mockFailingOpenRoot
				defer func() { osOpenRoot = origOpenRoot }()
				_, err := createWorkSubdir(workDir, stageDirPrefix+"*")
				return err
			},
			wantErr: "open work directory",
		},
		{
			fn: func() error {
				randRead = mockFailingRand
				defer func() { randRead = origRandRead }()
				_, err := createWorkSubdir(workDir, stageDirPrefix+"*")
				return err
			},
			wantErr: "generate temporary directory suffix",
		},
		{
			fn: func() error {
				rootMkdir = mockFailingRootMkdir
				defer func() { rootMkdir = origRootMkdir }()
				_, err := createWorkSubdir(workDir, stageDirPrefix+"*")
				return err
			},
			wantErr: "create temporary directory",
		},
		{
			fn: func() error {
				randRead = mockZeroRand
				defer func() { randRead = origRandRead }()
				if _, err := createWorkSubdir(workDir, stageDirPrefix+"dup-*"); err != nil {
					return err
				}
				_, err := createWorkSubdir(workDir, stageDirPrefix+"dup-*")
				return err
			},
			wantErr: "exhausted unique name attempts",
		},
		{
			fn: func() error {
				rootClose = mockFailingRootClose
				defer func() { rootClose = origRootClose }()
				_, err := createWorkSubdir(workDir, stageDirPrefix+"*")
				return err
			},
			wantErr: "close work directory",
		},
	}
	for i, tc := range cases {
		err := tc.fn()
		if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
			t.Fatalf("case %d: expected error containing %q, got %v", i, tc.wantErr, err)
		}
	}
}

func TestCheckRestoreFreeSpaceSeparateFS(t *testing.T) {
	root := t.TempDir()
	archivePath := filepath.Join(root, "stack.tar")
	if err := os.WriteFile(archivePath, make([]byte, 4096), 0o600); err != nil {
		t.Fatalf("write archive: %v", err)
	}

	backupRoot := filepath.Join(root, "backups")
	stackDir := filepath.Join(root, "home", "app")
	customWork := filepath.Join(root, "home", ".bro-work")
	defaultWork := backupWorkDir(backupRoot)

	origSameFS := sameFilesystemFn
	origStat := osStat
	t.Cleanup(func() {
		sameFilesystemFn = origSameFS
		osStat = origStat
	})

	orch := New(&fakeOrchRunner{})
	global := &config.GlobalConfig{
		BackupRoot:     backupRoot,
		RestoreWorkDir: customWork,
		MinFreeGB:      15,
	}

	sameFilesystemFn = mockDifferentFS
	orch.LVM.AvailBytesFn = func(path string) (uint64, error) {
		if path == stackDir {
			return 1024, nil
		}
		return 10 << 30, nil
	}
	if err := orch.checkRestoreFreeSpace(context.Background(), global, archivePath, customWork, stackDir); err == nil || !strings.Contains(err.Error(), "check free space for stack_dir") {
		t.Fatalf("expected stack_dir free-space error, got %v", err)
	}

	orch.LVM.AvailBytesFn = func(string) (uint64, error) {
		return 5 << 30, nil
	}
	if err := orch.checkRestoreFreeSpace(context.Background(), global, archivePath, defaultWork, stackDir); err != nil {
		t.Fatalf("expected small archive restore to succeed below min_free_gb, got %v", err)
	}

	orch.LVM.AvailBytesFn = func(string) (uint64, error) {
		return 512 << 20, nil
	}
	if err := orch.checkRestoreFreeSpace(context.Background(), global, archivePath, defaultWork, stackDir); err == nil || !strings.Contains(err.Error(), "check free space for restore work directory") {
		t.Fatalf("expected work directory buffer error, got %v", err)
	}

	zeroMinGlobal := &config.GlobalConfig{BackupRoot: backupRoot, MinFreeGB: 0}
	if err := orch.checkRestoreFreeSpace(context.Background(), zeroMinGlobal, archivePath, defaultWork, stackDir); err != nil {
		t.Fatalf("expected zero min_free_gb restore to succeed without buffer, got %v", err)
	}

	osStat = mockFailingOSStat
	if err := orch.checkRestoreFreeSpace(context.Background(), global, archivePath, customWork, stackDir); err == nil || !strings.Contains(err.Error(), "stat archive") {
		t.Fatalf("expected stat archive error, got %v", err)
	}
}

func TestCheckRestoreFreeSpaceSharedFSAndHelpers(t *testing.T) {
	root := t.TempDir()
	largeArchive := filepath.Join(root, "large.tar")
	if err := os.WriteFile(largeArchive, make([]byte, 8192), 0o600); err != nil {
		t.Fatalf("write large archive: %v", err)
	}

	backupRoot := filepath.Join(root, "backups")
	stackDir := filepath.Join(root, "home", "app")
	customWork := filepath.Join(root, "home", ".bro-work")

	origSameFS := sameFilesystemFn
	origStat := osStat
	t.Cleanup(func() {
		sameFilesystemFn = origSameFS
		osStat = origStat
	})

	orch := New(&fakeOrchRunner{})
	zeroMinGlobal := &config.GlobalConfig{BackupRoot: backupRoot, MinFreeGB: 0}

	sameFilesystemFn = mockSameFS
	orch.LVM.AvailBytesFn = func(string) (uint64, error) {
		return 12000, nil
	}
	if err := orch.checkRestoreFreeSpace(context.Background(), zeroMinGlobal, largeArchive, customWork, stackDir); err == nil || !strings.Contains(err.Error(), "check free space for shared restore filesystem") {
		t.Fatalf("expected shared filesystem error when free < 2*archive, got %v", err)
	}

	orch.LVM.AvailBytesFn = func(string) (uint64, error) {
		return 20000, nil
	}
	if err := orch.checkRestoreFreeSpace(context.Background(), zeroMinGlobal, largeArchive, customWork, stackDir); err != nil {
		t.Fatalf("expected shared filesystem check to pass when free >= 2*archive, got %v", err)
	}

	osStat = mockMaxInt64FileStat
	overflowGlobal := &config.GlobalConfig{BackupRoot: backupRoot, MinFreeGB: 15}
	if err := orch.checkRestoreFreeSpace(context.Background(), overflowGlobal, largeArchive, backupWorkDir(backupRoot), stackDir); err == nil || !strings.Contains(err.Error(), "overflows uint64") {
		t.Fatalf("expected combined uint64 overflow error, got %v", err)
	}
	osStat = origStat

	if _, ok := addUint64Checked(math.MaxUint64, 1); ok {
		t.Fatal("expected uint64 addition overflow detection")
	}
	sameDirA := filepath.Join(root, "sub_a", "leaf")
	sameDirB := filepath.Join(root, "sub_b", "leaf")
	if !defaultSameFilesystem(sameDirA, sameDirB) {
		t.Fatal("expected paths under same temp root to report same filesystem")
	}

	osStat = mockFailingOSStat
	if defaultSameFilesystem(customWork, stackDir) {
		t.Fatal("expected defaultSameFilesystem to return false on stat failure")
	}
}

func TestPruneBeforeFreeSpaceInSelfBackup(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := t.TempDir()
	stackName := "app-prune-self"
	bundle := buildTestBundle(t, root, stackName)
	pPath, gPath := writeFakeAccounts(t, root, stackName)
	orch := newTestOrchestrator(&fakeOrchRunner{snapMount: bundle.Global.LVM.MountPoint, stackName: stackName}, pPath, gPath, nil)

	staleSelf := filepath.Join(backupWorkDir(bundle.Global.BackupRoot), stageDirPrefix+"crashed")
	if err := os.MkdirAll(staleSelf, 0o700); err != nil {
		t.Fatalf("mkdir staleSelf: %v", err)
	}
	orch.LVM.AvailBytesFn = func(string) (uint64, error) {
		if _, err := os.Stat(staleSelf); !os.IsNotExist(err) {
			t.Fatalf("expected stale dir %q to be pruned before free-space check, statErr=%v", staleSelf, err)
		}
		return 1024, nil
	}
	if err := orch.SelfBackup(ctx, &bundle, &SelfBackupOptions{NoSync: true, Out: io.Discard}); err == nil || !strings.Contains(err.Error(), "check free space") {
		t.Fatalf("expected check free space error, got %v", err)
	}

	fileWork := filepath.Join(root, "file_work_dir")
	if wErr := os.WriteFile(fileWork, []byte("x"), 0o600); wErr != nil {
		t.Fatalf("write file_work_dir: %v", wErr)
	}
	badBackupBundle := bundle
	badBackupBundle.Global.BackupRoot = fileWork
	if err := orch.Backup(ctx, &badBackupBundle, &BackupOptions{NoSync: true, Out: io.Discard}); err == nil || !strings.Contains(err.Error(), "prepare backup work directory") {
		t.Fatalf("expected prepare backup work directory error, got %v", err)
	}
}

func TestPruneBeforeFreeSpaceInRestore(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := t.TempDir()
	stackName := "app-prune-restore"
	bundle := buildTestBundle(t, root, stackName)
	pPath, gPath := writeFakeAccounts(t, root, stackName)
	orch := newTestOrchestrator(&fakeOrchRunner{snapMount: bundle.Global.LVM.MountPoint, stackName: stackName}, pPath, gPath, nil)

	if err := orch.Backup(ctx, &bundle, &BackupOptions{NoSync: true, Out: io.Discard}); err != nil {
		t.Fatalf("Backup failed: %v", err)
	}
	archivePath := filepath.Join(bundle.Global.BackupRoot, stackName, stackName+"_2026-11-16_15-00-00.tar")

	staleRestore := filepath.Join(backupWorkDir(bundle.Global.BackupRoot), restoreDirPrefix+"crashed")
	if err := os.MkdirAll(staleRestore, 0o700); err != nil {
		t.Fatalf("mkdir staleRestore: %v", err)
	}
	orch.LVM.AvailBytesFn = func(string) (uint64, error) {
		if _, err := os.Stat(staleRestore); !os.IsNotExist(err) {
			t.Fatalf("expected stale dir %q to be pruned before restore free-space check, statErr=%v", staleRestore, err)
		}
		return 1024, nil
	}
	if err := orch.Restore(ctx, &bundle.Global, &RestoreOptions{ArchivePath: archivePath, Out: io.Discard}); err == nil || !strings.Contains(err.Error(), "check free space") {
		t.Fatalf("expected check free space error in Restore, got %v", err)
	}

	fileWork := filepath.Join(root, "file_work_dir")
	if wErr := os.WriteFile(fileWork, []byte("x"), 0o600); wErr != nil {
		t.Fatalf("write file_work_dir: %v", wErr)
	}
	badRestoreGlobal := bundle.Global
	badRestoreGlobal.RestoreWorkDir = fileWork
	if err := orch.Restore(ctx, &badRestoreGlobal, &RestoreOptions{ArchivePath: archivePath, Out: io.Discard}); err == nil || !strings.Contains(err.Error(), "prepare restore work directory") {
		t.Fatalf("expected prepare restore work directory error, got %v", err)
	}

	overlapGlobal := bundle.Global
	overlapGlobal.RestoreWorkDir = filepath.Join(bundle.Stacks[0].StackDir, ".bro-work")
	if err := orch.Restore(ctx, &overlapGlobal, &RestoreOptions{ArchivePath: archivePath, Out: io.Discard}); err == nil || !strings.Contains(err.Error(), "must not overlap stack_dir") {
		t.Fatalf("expected overlapping restore_work_dir error, got %v", err)
	}
}
