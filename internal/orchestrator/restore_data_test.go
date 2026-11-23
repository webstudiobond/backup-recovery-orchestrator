package orchestrator

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/webstudiobond/backup-recovery-orchestrator/internal/archive"
	"github.com/webstudiobond/backup-recovery-orchestrator/internal/config"
	"github.com/webstudiobond/backup-recovery-orchestrator/internal/dac"
)

func TestRestoreHostFilesAndErrors(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := t.TempDir()
	pPath, gPath := writeFakeAccounts(t, root, "app-hf")
	orch := newTestOrchestrator(&fakeOrchRunner{}, pPath, gPath, nil)

	rctx := &restoreExecutionContext{
		manifest: &archive.Manifest{
			StackName: "app-hf",
			User:      config.UserConfig{Name: "app-hf", UID: 1010, GID: 1010},
		},
	}

	if err := orch.restoreHostFiles(ctx, rctx); err != nil {
		t.Fatalf("expected nil for empty hostFiles, got %v", err)
	}

	rctx.hostFiles = []string{"etc/logrotate.d/app"}
	if err := orch.restoreHostFiles(ctx, rctx); err == nil || !strings.Contains(err.Error(), "manifest missing host_files_archive") {
		t.Fatalf("expected missing host_files_archive error, got %v", err)
	}

	rctx.manifest.HostFilesArchive = "host_files.tar.zst"
	rctx.unpackDir = root
	failRunner := &fakeOrchRunner{failMatch: "--numeric-owner"}
	failOrch := newTestOrchestrator(failRunner, pPath, gPath, nil)
	if err := failOrch.restoreHostFiles(ctx, rctx); err == nil || !strings.Contains(err.Error(), "extract host_files") {
		t.Fatalf("expected extract host_files error, got %v", err)
	}

	realFile := filepath.Join(root, "host_file")
	if err := os.WriteFile(realFile, []byte("data"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}

	rctx.manifest.HostFiles = []archive.ManifestHostFile{
		{Path: "/etc/logrotate.d/absent", Present: false},
		{Path: realFile, Present: true, Owner: "unknown-user", FileMode: "0600"},
	}
	dacErrOrch := newTestOrchestrator(&fakeOrchRunner{}, pPath, gPath, nil)
	dacErrOrch.DAC = &dac.Applier{
		LookupUser: func(_ string) (int, error) { return 0, os.ErrNotExist },
	}
	if err := dacErrOrch.restoreHostFiles(ctx, rctx); err == nil || !strings.Contains(err.Error(), "apply host_files DAC") {
		t.Fatalf("expected apply host_files DAC error, got %v", err)
	}

	rctx.manifest.HostFiles[1].Owner = ""
	okOrch := newTestOrchestrator(&fakeOrchRunner{}, pPath, gPath, nil)
	if err := okOrch.restoreHostFiles(ctx, rctx); err != nil {
		t.Fatalf("expected success, got %v", err)
	}
}

func TestMoveStackContentsValidation(t *testing.T) {
	root := t.TempDir()
	stackDir := filepath.Join(root, "stack")
	srcSub := filepath.Join(stackDir, "src_data")
	if err := os.MkdirAll(srcSub, 0o700); err != nil {
		t.Fatalf("mkdir src: %v", err)
	}

	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := moveStackContents(canceledCtx, stackDir, "src_data", "dst_data"); err == nil || !strings.Contains(err.Error(), "context canceled before moving contents") {
		t.Fatalf("expected canceled context error, got %v", err)
	}

	origAbs := filepathAbs
	filepathAbs = func(_ string) (string, error) { return "", errors.New("simulated abs error") }
	errAbs := moveStackContents(context.Background(), stackDir, "src_data", "dst_data")
	filepathAbs = origAbs
	if errAbs == nil || !strings.Contains(errAbs.Error(), "resolve stack_dir") {
		t.Fatalf("expected resolve stack_dir error, got %v", errAbs)
	}

	cases := []struct {
		fromRel string
		toRel   string
		wantErr string
		name    string
	}{
		{fromRel: "", toRel: "dst_v1", wantErr: "source path", name: "empty source"},
		{fromRel: "src_v1", toRel: "", wantErr: "destination path", name: "empty destination"},
		{fromRel: "src_v2", toRel: "src_v2", wantErr: "source and destination must differ", name: "same paths"},
		{fromRel: "src_v3", toRel: "src_v3/nested", wantErr: "cannot reside inside source", name: "nested destination"},
		{fromRel: "missing_dir", toRel: "dst_v2", wantErr: "stat source directory", name: "missing source"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := moveStackContents(context.Background(), stackDir, tc.fromRel, tc.toRel)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("expected error containing %q, got %v", tc.wantErr, err)
			}
		})
	}
}

func TestMoveStackContentsExecution(t *testing.T) {
	root := t.TempDir()
	stackDir := filepath.Join(root, "stack_exec")
	srcSub := filepath.Join(stackDir, "src_exec")
	if err := os.MkdirAll(srcSub, 0o700); err != nil {
		t.Fatalf("mkdir src: %v", err)
	}

	filePath := filepath.Join(stackDir, "file_as_dir")
	if err := os.WriteFile(filePath, []byte("data"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}
	if err := moveStackContents(context.Background(), stackDir, "file_as_dir", "dst_exec"); err == nil || !strings.Contains(err.Error(), "must be a real directory") {
		t.Fatalf("expected file as source error, got %v", err)
	}

	if err := moveStackContents(context.Background(), stackDir, "src_exec", "file_as_dir"); err == nil {
		t.Fatal("expected file as destination error, got nil")
	}

	origRemove := osRemove
	osRemove = func(_ string) error { return errors.New("simulated rm error") }
	errRm := moveStackContents(context.Background(), stackDir, "src_exec", "dst_rm")
	osRemove = origRemove
	if errRm == nil || !strings.Contains(errRm.Error(), "remove empty source directory") {
		t.Fatalf("expected remove empty source error, got %v", errRm)
	}

	failEntryDir := filepath.Join(stackDir, "src_fail_entry")
	if err := os.MkdirAll(failEntryDir, 0o700); err != nil {
		t.Fatalf("mkdir src_fail_entry: %v", err)
	}
	if err := os.WriteFile(filepath.Join(failEntryDir, "file.txt"), []byte("data"), 0o600); err != nil {
		t.Fatalf("write fail entry: %v", err)
	}

	origRename := osRename
	osRename = func(_, _ string) error { return errors.New("simulated rename error") }
	errEntry := moveStackContents(context.Background(), stackDir, "src_fail_entry", "dst_fail_entry")
	osRename = origRename
	if errEntry == nil || !strings.Contains(errEntry.Error(), "simulated rename error") {
		t.Fatalf("expected moveDirectoryEntries error in moveStackContents, got %v", errEntry)
	}

	remDir := filepath.Join(stackDir, "src_ok")
	if err := os.MkdirAll(remDir, 0o700); err != nil {
		t.Fatalf("mkdir src_ok: %v", err)
	}
	if err := os.WriteFile(filepath.Join(remDir, "moved.txt"), []byte("data"), 0o600); err != nil {
		t.Fatalf("write moved.txt: %v", err)
	}
	if err := moveStackContents(context.Background(), stackDir, "src_ok", "dst_ok"); err != nil {
		t.Fatalf("expected moveStackContents success, got %v", err)
	}
}

func TestMoveDirectoryEntriesErrors(t *testing.T) {
	root := t.TempDir()
	srcDir := filepath.Join(root, "src_entries")
	dstDir := filepath.Join(root, "dst_entries")
	if err := os.MkdirAll(srcDir, 0o700); err != nil {
		t.Fatalf("mkdir src: %v", err)
	}
	if err := os.MkdirAll(dstDir, 0o700); err != nil {
		t.Fatalf("mkdir dst: %v", err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "entry1.txt"), []byte("val1"), 0o600); err != nil {
		t.Fatalf("write entry1: %v", err)
	}

	origReadDir := osReadDir
	osReadDir = func(_ string) ([]os.DirEntry, error) { return nil, errors.New("simulated readdir error") }
	errRead := moveDirectoryEntries(context.Background(), srcDir, dstDir)
	osReadDir = origReadDir
	if errRead == nil || !strings.Contains(errRead.Error(), "read source directory") {
		t.Fatalf("expected read source directory error, got %v", errRead)
	}

	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := moveDirectoryEntries(canceledCtx, srcDir, dstDir); err == nil || !strings.Contains(err.Error(), "context canceled during move_contents") {
		t.Fatalf("expected canceled context error, got %v", err)
	}

	if err := os.WriteFile(filepath.Join(dstDir, "entry1.txt"), []byte("dup"), 0o600); err != nil {
		t.Fatalf("write dup entry: %v", err)
	}
	if err := moveDirectoryEntries(context.Background(), srcDir, dstDir); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("expected already exists error, got %v", err)
	}
	if err := os.Remove(filepath.Join(dstDir, "entry1.txt")); err != nil {
		t.Fatalf("remove dup entry: %v", err)
	}

	origLstat := osLstat
	osLstat = func(name string) (os.FileInfo, error) {
		if strings.Contains(name, "entry1.txt") {
			return nil, errors.New("simulated lstat error")
		}
		return defaultOsLstat(name)
	}
	errStat := moveDirectoryEntries(context.Background(), srcDir, dstDir)
	osLstat = origLstat
	if errStat == nil || !strings.Contains(errStat.Error(), "stat destination entry") {
		t.Fatalf("expected stat destination entry error, got %v", errStat)
	}
}

func TestMoveDirectoryEntriesRenameFailure(t *testing.T) {
	root := t.TempDir()
	srcDir := filepath.Join(root, "src_ren")
	dstDir := filepath.Join(root, "dst_ren")
	if err := os.MkdirAll(srcDir, 0o700); err != nil {
		t.Fatalf("mkdir src: %v", err)
	}
	if err := os.MkdirAll(dstDir, 0o700); err != nil {
		t.Fatalf("mkdir dst: %v", err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "item_ren.txt"), []byte("val"), 0o600); err != nil {
		t.Fatalf("write item: %v", err)
	}

	origRename := osRename
	osRename = func(_, _ string) error { return errors.New("simulated rename error") }
	errRen := moveDirectoryEntries(context.Background(), srcDir, dstDir)
	osRename = origRename
	if errRen == nil || !strings.Contains(errRen.Error(), "move") {
		t.Fatalf("expected rename error, got %v", errRen)
	}
}

func TestResolveSafeStackSubdir(t *testing.T) {
	root := t.TempDir()

	origRel := filepathRel
	filepathRel = func(_, _ string) (string, error) { return "", errors.New("simulated rel error") }
	_, errRel := resolveSafeStackSubdir(root, "valid/sub")
	filepathRel = origRel
	if errRel == nil || !strings.Contains(errRel.Error(), "escapes") {
		t.Fatalf("expected escapes error on rel failure, got %v", errRel)
	}

	cases := []struct {
		rawRel  string
		wantErr string
		name    string
	}{
		{rawRel: " \t ", wantErr: "must be non-empty and relative", name: "whitespace"},
		{rawRel: "/abs/path", wantErr: "must be non-empty and relative", name: "absolute path"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := resolveSafeStackSubdir(root, tc.rawRel)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("expected error containing %q, got %v", tc.wantErr, err)
			}
		})
	}

	for _, raw := range []string{".", "..", "../sibling"} {
		if _, err := resolveSafeStackSubdir(root, raw); err == nil || !strings.Contains(err.Error(), "escapes stack_dir") {
			t.Fatalf("expected escapes error for %q, got %v", raw, err)
		}
	}

	got, err := resolveSafeStackSubdir(root, "valid/sub")
	if err != nil || got != filepath.Join(root, "valid", "sub") {
		t.Fatalf("expected valid subdir path, got %q, err=%v", got, err)
	}
}

func TestPrepareMoveTargetDir(t *testing.T) {
	root := t.TempDir()
	validDir := filepath.Join(root, "valid_dir")
	if err := os.MkdirAll(validDir, 0o700); err != nil {
		t.Fatalf("mkdir validDir: %v", err)
	}
	if err := prepareMoveTargetDir(validDir); err != nil {
		t.Fatalf("expected success for existing dir, got %v", err)
	}

	symDir := filepath.Join(root, "sym_dir")
	if err := os.Symlink(validDir, symDir); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	if err := prepareMoveTargetDir(symDir); err == nil || !strings.Contains(err.Error(), "must be a real directory") {
		t.Fatalf("expected symlink rejected error, got %v", err)
	}

	regFile := filepath.Join(root, "regular_file")
	if err := os.WriteFile(regFile, []byte("data"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}
	if err := prepareMoveTargetDir(regFile); err == nil {
		t.Fatal("expected file rejected error, got nil")
	}

	badStatPath := filepath.Join(regFile, "sub")
	if err := prepareMoveTargetDir(badStatPath); err == nil || !strings.Contains(err.Error(), "stat destination directory") {
		t.Fatalf("expected stat destination directory error, got %v", err)
	}

	origMkdir := osMkdirAll
	osMkdirAll = func(_ string, _ os.FileMode) error { return errors.New("simulated mkdir error") }
	mkdirErrPath := filepath.Join(root, "fail_mkdir", "sub")
	errMkdir := prepareMoveTargetDir(mkdirErrPath)
	osMkdirAll = origMkdir
	if errMkdir == nil || !strings.Contains(errMkdir.Error(), "create destination directory") {
		t.Fatalf("expected create destination directory error, got %v", errMkdir)
	}

	newDir := filepath.Join(root, "new_dir", "nested")
	if err := prepareMoveTargetDir(newDir); err != nil {
		t.Fatalf("expected new dir created, got %v", err)
	}
}

func TestRestoreDatabaseStep(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := t.TempDir()
	pPath, gPath := writeFakeAccounts(t, root, "app-db")
	okRunner := &fakeOrchRunner{}
	okOrch := newTestOrchestrator(okRunner, pPath, gPath, nil)
	failRunner := &fakeOrchRunner{failMatch: "docker"}
	failOrch := newTestOrchestrator(failRunner, pPath, gPath, nil)

	rctx := &restoreExecutionContext{
		manifest: &archive.Manifest{
			StackName:   "app-db",
			ComposeFile: filepath.Join(root, "docker-compose.yaml"),
		},
		unpackDir: root,
	}

	if err := okOrch.restoreDatabaseStep(ctx, rctx, ""); err == nil || !strings.Contains(err.Error(), "no databases recorded in manifest") {
		t.Fatalf("expected no databases error, got %v", err)
	}

	dumpFile := filepath.Join(root, "db_main.sql.zst")
	if err := os.WriteFile(dumpFile, []byte("DUMP"), 0o600); err != nil {
		t.Fatalf("write dump: %v", err)
	}

	dbMeta := archive.ManifestDB{
		ID:           "db_main",
		Driver:       "mariadb",
		Service:      "pg",
		ArtifactPath: "db_main.sql.zst",
	}
	rctx.manifest.Databases = []archive.ManifestDB{dbMeta}

	if err := okOrch.restoreDatabaseStep(ctx, rctx, "nonexistent"); err == nil || !strings.Contains(err.Error(), "not found in manifest") {
		t.Fatalf("expected database_id not found error, got %v", err)
	}

	if err := failOrch.restoreDatabaseStep(ctx, rctx, ""); err == nil || !strings.Contains(err.Error(), "import database") {
		t.Fatalf("expected import database error on empty ID, got %v", err)
	}

	if err := okOrch.restoreDatabaseStep(ctx, rctx, ""); err != nil {
		t.Fatalf("expected import database success on empty ID, got %v", err)
	}

	if err := okOrch.restoreDatabaseStep(ctx, rctx, "db_main"); err != nil {
		t.Fatalf("expected import database success on explicit ID, got %v", err)
	}
}

func TestFindManifestDBAndHostTargets(t *testing.T) {
	t.Parallel()

	dbs := []archive.ManifestDB{
		{ID: "primary", Driver: "mariadb"},
		{ID: "secondary", Driver: "redis"},
	}

	got, ok := findManifestDB(dbs, "secondary")
	if !ok || got.Driver != "redis" {
		t.Fatalf("expected secondary found, got ok=%v, db=%+v", ok, got)
	}

	_, notFound := findManifestDB(dbs, "missing_db")
	if notFound {
		t.Fatal("expected missing_db not found")
	}

	items := []archive.ManifestHostFile{
		{TarPath: "etc/conf.d", Present: true},
		{TarPath: "etc/absent.d", Present: false},
	}

	relsNone, errNone := resolveHostFileTargets(items, "")
	if errNone != nil || relsNone != nil {
		t.Fatalf("expected nil for empty mode, got %v, err=%v", relsNone, errNone)
	}

	relsAll, errAll := resolveHostFileTargets(items, RestoreModeAll)
	if errAll != nil || len(relsAll) != 1 || relsAll[0] != "etc/conf.d" {
		t.Fatalf("expected 1 item for RestoreModeAll, got %+v, err=%v", relsAll, errAll)
	}

	_, errUnknown := resolveHostFileTargets(items, "unsupported_mode")
	if errUnknown == nil || !strings.Contains(errUnknown.Error(), "unsupported host-files mode") {
		t.Fatalf("expected unsupported host-files mode error, got %v", errUnknown)
	}
}

func TestDefaultOsLstatAndRename(t *testing.T) {
	root := t.TempDir()

	testFile := filepath.Join(root, "test.txt")
	if err := os.WriteFile(testFile, []byte("ok"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}

	info, errLstat := defaultOsLstat(testFile)
	if errLstat != nil || info.Name() != "test.txt" {
		t.Fatalf("defaultOsLstat failed: %v", errLstat)
	}
	if _, err := defaultOsLstat(filepath.Join(root, "missing_lstat")); err == nil {
		t.Fatal("expected defaultOsLstat error")
	}

	renamedFile := filepath.Join(root, "renamed.txt")
	if err := defaultOsRename(testFile, renamedFile); err != nil {
		t.Fatalf("defaultOsRename failed: %v", err)
	}
	if err := defaultOsRename(filepath.Join(root, "m1"), filepath.Join(root, "m2")); err == nil {
		t.Fatal("expected defaultOsRename error")
	}
}

func TestDefaultOsReadDirRemoveAndMkdir(t *testing.T) {
	root := t.TempDir()

	testFile := filepath.Join(root, "file.txt")
	if err := os.WriteFile(testFile, []byte("ok"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}

	entries, errRead := defaultOsReadDir(root)
	if errRead != nil || len(entries) != 1 || entries[0].Name() != "file.txt" {
		t.Fatalf("defaultOsReadDir failed: %v", errRead)
	}
	if _, err := defaultOsReadDir(filepath.Join(root, "missing_readdir")); err == nil {
		t.Fatal("expected defaultOsReadDir error")
	}

	if err := defaultOsRemove(testFile); err != nil {
		t.Fatalf("defaultOsRemove failed: %v", err)
	}
	if err := defaultOsRemove(filepath.Join(root, "missing_rm")); err == nil {
		t.Fatal("expected defaultOsRemove error")
	}

	subMkdir := filepath.Join(root, "a", "b")
	if err := defaultOsMkdirAll(subMkdir, 0o700); err != nil {
		t.Fatalf("defaultOsMkdirAll failed: %v", err)
	}
	fileParent := filepath.Join(root, "file_as_parent")
	if err := os.WriteFile(fileParent, []byte("data"), 0o600); err != nil {
		t.Fatalf("write file_as_parent: %v", err)
	}
	if err := defaultOsMkdirAll(filepath.Join(fileParent, "child"), 0o700); err == nil {
		t.Fatal("expected defaultOsMkdirAll error")
	}
}
