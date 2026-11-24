package orchestrator

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestCopyDirectoryContents(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	srcDir := filepath.Join(root, "src")
	subDir := filepath.Join(srcDir, "nested")
	if err := os.MkdirAll(subDir, 0o750); err != nil {
		t.Fatalf("mkdir sub: %v", err)
	}
	srcFile := filepath.Join(subDir, "payload.txt")
	if err := os.WriteFile(srcFile, []byte("content"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}

	dstDir := filepath.Join(root, "dst")
	if err := copyDirectoryContents(srcDir, dstDir); err != nil {
		t.Fatalf("copyDirectoryContents failed: %v", err)
	}

	dstFile := filepath.Join(dstDir, "nested", "payload.txt")
	data, readErr := os.ReadFile(filepath.Clean(dstFile))
	if readErr != nil || string(data) != "content" {
		t.Fatalf("expected payload content, got %q err=%v", string(data), readErr)
	}

	symDir := filepath.Join(root, "src_sym")
	if err := os.Mkdir(symDir, 0o750); err != nil {
		t.Fatalf("mkdir sym: %v", err)
	}
	if err := os.Symlink(srcFile, filepath.Join(symDir, "link")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	conflictDirSrc := filepath.Join(root, "conflict_src")
	if err := os.MkdirAll(filepath.Join(conflictDirSrc, "folder"), 0o750); err != nil {
		t.Fatalf("mkdir conflict src: %v", err)
	}
	conflictDirDst := filepath.Join(root, "conflict_dst")
	if err := os.WriteFile(conflictDirDst, []byte("parent_file"), 0o600); err != nil {
		t.Fatalf("write conflict file: %v", err)
	}

	errCases := []struct{ src, dst string }{
		{filepath.Join(root, "missing_src"), dstDir},
		{symDir, filepath.Join(root, "dst_symlink")},
		{conflictDirSrc, conflictDirDst},
	}
	for _, tc := range errCases {
		if err := copyDirectoryContents(tc.src, tc.dst); err == nil {
			t.Fatalf("expected error copying %q to %q", tc.src, tc.dst)
		}
	}
}

func TestCopyPlainFileSuccess(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	srcFile := filepath.Join(root, "source.txt")
	if err := os.WriteFile(srcFile, []byte("plain data"), 0o600); err != nil {
		t.Fatalf("write source: %v", err)
	}

	dstFile := filepath.Join(root, "dest", "copied.txt")
	if err := copyPlainFile(srcFile, dstFile, 0o600); err != nil {
		t.Fatalf("copyPlainFile failed: %v", err)
	}

	info, statErr := os.Stat(dstFile)
	if statErr != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("expected mode 0600 on %s, got %v err=%v", dstFile, info, statErr)
	}
}

func TestCopyPlainFileErrors(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	srcFile := filepath.Join(root, "valid.txt")
	if err := os.WriteFile(srcFile, []byte("valid"), 0o600); err != nil {
		t.Fatalf("write source: %v", err)
	}
	fileParent := filepath.Join(root, "parent_as_file")
	if err := os.WriteFile(fileParent, []byte("blocker"), 0o600); err != nil {
		t.Fatalf("write fileParent: %v", err)
	}
	existingDir := filepath.Join(root, "dir_target")
	if err := os.MkdirAll(existingDir, 0o750); err != nil {
		t.Fatalf("mkdir dir_target: %v", err)
	}

	cases := []struct {
		src, dst string
	}{
		{src: filepath.Join(root, "missing.txt"), dst: filepath.Join(root, "out.txt")},
		{src: srcFile, dst: filepath.Join(fileParent, "child", "target.txt")},
		{src: srcFile, dst: existingDir},
	}
	for _, tc := range cases {
		if err := copyPlainFile(tc.src, tc.dst, 0o600); err == nil {
			t.Fatalf("expected error copying %q to %q", tc.src, tc.dst)
		}
	}
}

func TestCopyDirectoryContentsRelError(t *testing.T) {
	root := t.TempDir()
	srcDir := filepath.Join(root, "src")
	if err := os.MkdirAll(srcDir, 0o750); err != nil {
		t.Fatalf("mkdir src: %v", err)
	}

	origRel := filepathRel
	filepathRel = func(_, _ string) (string, error) {
		return "", errors.New("simulated rel error")
	}
	t.Cleanup(func() { filepathRel = origRel })

	if err := copyDirectoryContents(srcDir, filepath.Join(root, "dst")); err == nil {
		t.Fatal("expected error from simulated rel error")
	}
}

func TestCopyDirectoryContentsStatErrors(t *testing.T) {
	root := t.TempDir()
	srcDir := filepath.Join(root, "src")
	if err := os.MkdirAll(srcDir, 0o750); err != nil {
		t.Fatalf("mkdir src: %v", err)
	}

	origWalk := filepathWalkDir
	t.Cleanup(func() { filepathWalkDir = origWalk })

	for _, isDir := range []bool{true, false} {
		filepathWalkDir = func(_ string, fn fs.WalkDirFunc) error {
			return fn(filepath.Join(srcDir, "mock_node"), fakeDirEntry{infoErr: errors.New("stat error"), name: "mock_node", isDir: isDir}, nil)
		}
		if err := copyDirectoryContents(srcDir, filepath.Join(root, "dst")); err == nil {
			t.Fatal("expected stat error")
		}
	}
}

func TestCopyPlainFilePayloadError(t *testing.T) {
	root := t.TempDir()
	srcFile := filepath.Join(root, "src.txt")
	if err := os.WriteFile(srcFile, []byte("data"), 0o600); err != nil {
		t.Fatalf("write src: %v", err)
	}

	origPayload := writeFilePayload
	writeFilePayload = func(_ *os.File, _ []byte, _ os.FileMode) error {
		return errors.New("simulated payload write error")
	}
	t.Cleanup(func() { writeFilePayload = origPayload })

	if err := copyPlainFile(srcFile, filepath.Join(root, "dst.txt"), 0o600); err == nil {
		t.Fatal("expected simulated payload write error")
	}
}

func TestDefaultFilepathRel(t *testing.T) {
	t.Parallel()

	if rel, err := defaultFilepathRel("/abs/path", "/abs/path/nested"); err != nil || rel != "nested" {
		t.Fatalf("expected nested, got %q err=%v", rel, err)
	}
	if _, err := defaultFilepathRel("/abs/path", "relative/path"); err == nil {
		t.Fatal("expected error between abs and rel paths")
	}
}

func TestDefaultRootHelpers(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	testFile := filepath.Join(root, "file.txt")
	if err := os.WriteFile(testFile, []byte("ok"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}

	osRoot, openErr := os.OpenRoot(root)
	if openErr != nil {
		t.Fatalf("open root: %v", openErr)
	}
	defer closeOrchRoot(t, osRoot)

	entries, errRead := defaultRootReadDir(osRoot)
	if errRead != nil || len(entries) != 1 || entries[0].Name() != "file.txt" {
		t.Fatalf("defaultRootReadDir failed: %v", errRead)
	}

	subDir := filepath.Join(root, "sub")
	if err := os.MkdirAll(subDir, 0o700); err != nil {
		t.Fatalf("mkdir sub: %v", err)
	}
	subRoot, subOpenErr := osRoot.OpenRoot("sub")
	if subOpenErr != nil {
		t.Fatalf("open subRoot: %v", subOpenErr)
	}
	defer closeOrchRoot(t, subRoot)

	if err := defaultRootRename(osRoot, osRoot, subRoot, ".", "sub", "file.txt"); err != nil {
		t.Fatalf("defaultRootRename across roots failed: %v", err)
	}
	if err := defaultRootRename(osRoot, osRoot, subRoot, ".", "sub", "missing_a"); err == nil {
		t.Fatal("expected defaultRootRename error")
	}

	if err := defaultRootRemove(subRoot, "file.txt"); err != nil {
		t.Fatalf("defaultRootRemove failed: %v", err)
	}
	if err := defaultRootRemove(osRoot, "missing_rm"); err == nil {
		t.Fatal("expected defaultRootRemove error")
	}
}

func TestOpenVerifiedStackChildDirRejectsFIFOWithoutBlocking(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	fifoPath := filepath.Join(root, "pipe.fifo")
	if err := syscall.Mkfifo(fifoPath, 0o600); err != nil {
		t.Fatalf("mkfifo: %v", err)
	}

	osRoot, openErr := os.OpenRoot(root)
	if openErr != nil {
		t.Fatalf("open root: %v", openErr)
	}
	defer closeOrchRoot(t, osRoot)

	if _, err := openVerifiedStackChildDir(osRoot, "pipe.fifo", nil, "source", "pipe.fifo"); err == nil {
		t.Fatalf("expected openVerifiedStackChildDir on FIFO to fail without blocking")
	}

	if err := verifyAndRemoveEmptyDir(osRoot, "pipe.fifo", "pipe.fifo", nil); err == nil || !strings.Contains(err.Error(), "concurrent path replacement") {
		t.Fatalf("expected verifyAndRemoveEmptyDir to reject non-directory, got %v", err)
	}
}

func TestFsutilBranchesWithMocks(t *testing.T) {
	origOpenDot, origReadDir := rootOpenDot, fileReadDir
	origRootClose, origRootMkdir := rootClose, rootMkdir
	origRootOpenFile, origFileStat := rootOpenFile, fileStat
	origSameFile, origRootOpenRoot := rootSameFile, rootOpenRoot
	origRootStat, origRootLstat := rootStat, rootLstat
	origOsOpenRoot := osOpenRoot
	t.Cleanup(func() {
		rootOpenDot = origOpenDot
		fileReadDir = origReadDir
		rootClose = origRootClose
		rootMkdir = origRootMkdir
		rootOpenFile = origRootOpenFile
		fileStat = origFileStat
		rootSameFile = origSameFile
		rootOpenRoot = origRootOpenRoot
		rootStat = origRootStat
		rootLstat = origRootLstat
		osOpenRoot = origOsOpenRoot
	})

	root := t.TempDir()
	osRoot, err := os.OpenRoot(root)
	if err != nil {
		t.Fatalf("open root: %v", err)
	}
	defer closeOrchRoot(t, osRoot)

	subDir := filepath.Join(root, "sub")
	if mkErr := os.Mkdir(subDir, 0o750); mkErr != nil {
		t.Fatalf("mkdir sub: %v", mkErr)
	}
	info, lstatErr := osRoot.Lstat("sub")
	if lstatErr != nil {
		t.Fatalf("lstat: %v", lstatErr)
	}

	cases := []struct {
		setup func()
		fn    func() error
	}{
		{func() { rootOpenDot = mockFailingRootOpenDot }, func() error { _, err := defaultRootReadDir(osRoot); return err }},
		{func() { rootOpenDot = origOpenDot; fileReadDir = mockFailingFileReadDir }, func() error { _, err := defaultRootReadDir(osRoot); return err }},
		{func() { fileReadDir = origReadDir; rootClose = mockFailingRootClose }, func() error { _, err := openStackDirNoSymlink(osRoot, "a/b", true); return err }},
		{func() { rootClose = origRootClose; rootMkdir = mockFailingRootMkdir }, func() error { _, err := openStackDirNoSymlink(osRoot, "newdir", true); return err }},
		{func() { rootMkdir = origRootMkdir; rootOpenFile = mockFailingRootOpenFile }, func() error { _, err := openVerifiedStackChildDir(osRoot, "sub", info, "source", "sub"); return err }},
		{func() { rootOpenFile = origRootOpenFile; fileStat = mockFailingFileStat }, func() error { _, err := openVerifiedStackChildDir(osRoot, "sub", info, "source", "sub"); return err }},
		{func() { fileStat = origFileStat; rootSameFile = mockDifferentSameFile }, func() error { _, err := openVerifiedStackChildDir(osRoot, "sub", info, "source", "sub"); return err }},
		{func() { rootSameFile = origSameFile; rootOpenRoot = mockFailingRootOpenRoot }, func() error {
			_, err := openVerifiedStackChildDirFallback(osRoot, "sub", info, "source", "sub")
			return err
		}},
		{func() { rootOpenRoot = origRootOpenRoot; rootStat = mockFailingRootStat }, func() error {
			_, err := openVerifiedStackChildDirFallback(osRoot, "sub", info, "source", "sub")
			return err
		}},
		{func() { rootStat = origRootStat; rootLstat = mockFailingRootLstat }, func() error { return verifyAndRemoveEmptyDir(osRoot, "sub", "sub", nil) }},
		{func() {
			calls := 0
			rootLstat = func(_ *os.Root, _ string) (fs.FileInfo, error) {
				calls++
				if calls == 1 {
					return nil, os.ErrNotExist
				}
				return nil, errors.New("simulated second stat failure")
			}
		}, func() error {
			defer func() { rootLstat = origRootLstat }()
			_, stepErr := stepStackDir(osRoot, "dir_sec_stat_fail", "dir_sec_stat_fail", true)
			return stepErr
		}},
		{func() { rootSameFile = mockDifferentSameFile }, func() error {
			defer func() { rootSameFile = origSameFile }()
			_, fbErr := openVerifiedStackChildDirFallback(osRoot, "sub", info, "source", "sub")
			return fbErr
		}},
		{func() {}, func() error {
			return removeEmptyStackDir(osRoot, "nonexistent_dir/sub", info)
		}},
		{func() { osOpenRoot = mockFailingOpenRoot }, func() error {
			defer func() { osOpenRoot = origOsOpenRoot }()
			srcFile := filepath.Join(root, "src.txt")
			if wErr := os.WriteFile(srcFile, []byte("x"), 0o600); wErr != nil {
				return wErr
			}
			return copyPlainFile(srcFile, filepath.Join(root, "dst", "out.txt"), 0o600)
		}},
	}

	for i, tc := range cases {
		tc.setup()
		if err := tc.fn(); err == nil {
			t.Fatalf("expected error for case %d", i)
		}
	}
}
