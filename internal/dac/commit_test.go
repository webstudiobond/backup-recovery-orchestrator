package dac

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestCommitFileAndHookedChanges(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	testPath := filepath.Join(tmpDir, "commit_test.txt")
	if err := os.WriteFile(testPath, []byte("data"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}

	root, openErr := os.OpenRoot(tmpDir)
	if openErr != nil {
		t.Fatalf("open root: %v", openErr)
	}
	defer closeRoot(t, root)
	info, statErr := root.Lstat("commit_test.txt")
	if statErr != nil {
		t.Fatalf("lstat: %v", statErr)
	}

	defaultApplier := &Applier{
		Lchown: func(_ string, _, _ int) error { return nil },
	}
	realChange := Change{
		Path:     testPath,
		Mode:     0o640,
		HasMode:  true,
		UID:      os.Getuid(),
		GID:      os.Getgid(),
		HasOwner: true,
	}
	if err := defaultApplier.commitFileChange(root, "commit_test.txt", info, &realChange); err != nil {
		t.Fatalf("expected fd-safe file commit to succeed: %v", err)
	}
	assertPerm(t, testPath, 0o640)

	if err := NewApplier().commitParentRootEntry(root, "nonexistent_dac_entry", nil, &realChange); err == nil {
		t.Fatalf("expected error from commitParentRootEntry on nonexistent path")
	}

	failChmodApplier := &Applier{
		Lchown: func(_ string, _, _ int) error { return nil },
		Chmod:  func(_ string, _ os.FileMode) error { return errors.New("chmod error") },
	}
	if err := failChmodApplier.commitHookedChange(&realChange); err == nil {
		t.Fatalf("expected error from failChmodApplier")
	}

	failChownApplier := &Applier{
		Lchown: func(_ string, _ int, _ int) error { return errors.New("lchown error") },
		Chmod:  func(_ string, _ os.FileMode) error { return nil },
	}
	if err := failChownApplier.commitHookedChange(&realChange); err == nil {
		t.Fatalf("expected error from failChownApplier")
	}
}

func TestCommitParentRootEntryRejectsSymlink(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	realFile := filepath.Join(tmpDir, "real.txt")
	if err := os.WriteFile(realFile, []byte("ok"), 0o600); err != nil {
		t.Fatalf("write real: %v", err)
	}
	symFile := filepath.Join(tmpDir, "sym.txt")
	if err := os.Symlink(realFile, symFile); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	root, openErr := os.OpenRoot(tmpDir)
	if openErr != nil {
		t.Fatalf("open root: %v", openErr)
	}
	defer closeRoot(t, root)

	change := Change{Path: symFile, Mode: 0o644, HasMode: true}
	err := NewApplier().commitParentRootEntry(root, "sym.txt", nil, &change)
	if err == nil || !strings.Contains(err.Error(), "concurrent path replacement") {
		t.Fatalf("expected symlink rejection error, got %v", err)
	}
}

func TestCommitFileChangeRejectsNonRegularAndHandlesUnreadable(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	fifoPath := filepath.Join(tmpDir, "test.fifo")
	if err := syscall.Mkfifo(fifoPath, 0o600); err != nil {
		t.Fatalf("mkfifo: %v", err)
	}
	unreadablePath := filepath.Join(tmpDir, "mode0000.txt")
	if err := os.WriteFile(unreadablePath, []byte("secret"), 0o000); err != nil {
		t.Fatalf("write unreadable: %v", err)
	}

	root, openErr := os.OpenRoot(tmpDir)
	if openErr != nil {
		t.Fatalf("open root: %v", openErr)
	}
	defer closeRoot(t, root)

	fifoInfo, fifoStatErr := root.Lstat("test.fifo")
	if fifoStatErr != nil {
		t.Fatalf("lstat fifo: %v", fifoStatErr)
	}
	applier := NewApplier()
	if err := applier.commitFileChange(root, "test.fifo", fifoInfo, &Change{Path: fifoPath, Mode: 0o644, HasMode: true}); err == nil || !strings.Contains(err.Error(), "non-regular file") {
		t.Fatalf("expected non-regular file rejection, got %v", err)
	}

	unreadableInfo, unreadableStatErr := root.Lstat("mode0000.txt")
	if unreadableStatErr != nil {
		t.Fatalf("lstat unreadable: %v", unreadableStatErr)
	}
	if err := applier.commitFileChange(root, "mode0000.txt", unreadableInfo, &Change{Path: unreadablePath, Mode: 0o640, HasMode: true}); err != nil {
		t.Fatalf("expected permission-restricted chmod fallback to succeed: %v", err)
	}
	assertPerm(t, unreadablePath, 0o640)
}

func TestCommitBranchesWithMocks(t *testing.T) {
	origOpenDot := rootOpenDot
	origFileStat := fileStat
	origSameFile := rootSameFile
	origFileChown := fileChown
	origFileChmod := fileChmod
	origFileClose := fileClose
	origRootLstat := rootLstat
	origRootLchown := rootLchown
	origRootChmod := rootChmod
	origRootOpenFile := rootOpenFile
	t.Cleanup(func() {
		rootOpenDot = origOpenDot
		fileStat = origFileStat
		rootSameFile = origSameFile
		fileChown = origFileChown
		fileChmod = origFileChmod
		fileClose = origFileClose
		rootLstat = origRootLstat
		rootLchown = origRootLchown
		rootChmod = origRootChmod
		rootOpenFile = origRootOpenFile
	})

	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "file.txt")
	if err := os.WriteFile(filePath, []byte("ok"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}
	root, err := os.OpenRoot(tmpDir)
	if err != nil {
		t.Fatalf("open root: %v", err)
	}
	defer closeRoot(t, root)

	info, err := root.Lstat("file.txt")
	if err != nil {
		t.Fatalf("lstat: %v", err)
	}

	applier := NewApplier()
	ch := Change{Path: filePath, Mode: 0o644, HasMode: true, UID: 1, GID: 1, HasOwner: true}
	chModeOnly := Change{Path: filePath, Mode: 0o644, HasMode: true}

	hookedApplier := &Applier{
		Lchown: func(_ string, _, _ int) error { return nil },
		Chmod:  func(_ string, _ os.FileMode) error { return nil },
	}
	if err := errors.Join(
		hookedApplier.commitDirChange(root, &ch),
		hookedApplier.commitFileChange(root, "file.txt", info, &ch),
		hookedApplier.commitParentRootEntry(root, "file.txt", info, &ch),
	); err != nil {
		t.Fatalf("hookedApplier failed: %v", err)
	}

	hookFailChown := &Applier{
		Lchown: func(_ string, _, _ int) error { return errors.New("fail") },
		Chmod:  func(_ string, _ os.FileMode) error { return nil },
	}
	hookFailChmod := &Applier{
		Lchown: func(_ string, _, _ int) error { return nil },
		Chmod:  func(_ string, _ os.FileMode) error { return errors.New("fail") },
	}

	cases := []struct {
		setup func()
		fn    func() error
	}{
		{func() { rootOpenDot = mockFailingOpenDot }, func() error { return applier.commitDirChange(root, &ch) }},
		{func() { rootOpenDot = origOpenDot; fileStat = mockFailingFileStat }, func() error { return applier.commitFileChange(root, "file.txt", info, &ch) }},
		{func() { fileStat = origFileStat; rootSameFile = mockDifferentSameFile }, func() error { return applier.commitFileChange(root, "file.txt", info, &ch) }},
		{func() {}, func() error { return applier.commitParentRootEntry(root, "file.txt", info, &ch) }},
		{func() { rootSameFile = origSameFile; fileChown = mockFailingFileChown }, func() error { return applier.commitFileChange(root, "file.txt", info, &ch) }},
		{func() { fileChown = origFileChown; fileChmod = mockFailingFileChmod }, func() error { return applier.commitFileChange(root, "file.txt", info, &ch) }},
		{func() { fileChmod = origFileChmod; fileClose = mockFailingFileClose }, func() error { return applier.commitFileChange(root, "file.txt", info, &ch) }},
		{func() { fileClose = origFileClose; rootLstat = mockFailingRootLstat }, func() error { return applier.commitParentRootEntry(root, "file.txt", info, &ch) }},
		{func() { rootLstat = origRootLstat; rootLchown = mockFailingRootLchown }, func() error { return applier.commitParentRootEntry(root, "file.txt", info, &ch) }},
		{func() { rootLchown = origRootLchown; rootChmod = mockFailingRootChmod }, func() error { return applier.commitParentRootEntry(root, "file.txt", info, &chModeOnly) }},
		{func() { rootChmod = origRootChmod; rootOpenFile = mockFailingRootOpenFile }, func() error { return applier.commitFileChange(root, "file.txt", info, &ch) }},
		{func() { rootOpenFile = origRootOpenFile }, func() error { return hookFailChown.commitParentRootEntry(root, "file.txt", info, &ch) }},
		{func() {}, func() error { return hookFailChmod.commitParentRootEntry(root, "file.txt", info, &ch) }},
	}
	for i, tc := range cases {
		tc.setup()
		if err := tc.fn(); err == nil {
			t.Fatalf("expected error for case %d", i)
		}
	}
}

func TestWrapCloseErr(t *testing.T) {
	t.Parallel()

	if err := wrapCloseErr("path", nil); err != nil {
		t.Fatalf("expected nil from wrapCloseErr with nil error, got %v", err)
	}
	testErr := errors.New("boom")
	if err := wrapCloseErr("path", testErr); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("expected wrapped error, got %v", err)
	}
}
