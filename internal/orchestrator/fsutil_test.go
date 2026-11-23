package orchestrator

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
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

	if err := copyDirectoryContents(filepath.Join(root, "missing_src"), dstDir); err == nil {
		t.Fatal("expected error on missing source directory")
	}

	symDir := filepath.Join(root, "src_symlink")
	if err := os.MkdirAll(symDir, 0o750); err != nil {
		t.Fatalf("mkdir symDir: %v", err)
	}
	if err := os.Symlink(srcFile, filepath.Join(symDir, "symlink_entry")); err != nil {
		t.Fatalf("create symlink: %v", err)
	}
	if err := copyDirectoryContents(symDir, filepath.Join(root, "dst_symlink")); err == nil {
		t.Fatal("expected error on symlink")
	}

	conflictDirSrc := filepath.Join(root, "conflict_src")
	if err := os.MkdirAll(filepath.Join(conflictDirSrc, "folder"), 0o750); err != nil {
		t.Fatalf("mkdir conflict src: %v", err)
	}
	conflictDirDst := filepath.Join(root, "conflict_dst")
	if err := os.WriteFile(conflictDirDst, []byte("parent_file"), 0o600); err != nil {
		t.Fatalf("write conflict file: %v", err)
	}
	if err := copyDirectoryContents(conflictDirSrc, conflictDirDst); err == nil {
		t.Fatal("expected mkdir error on destination conflict")
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
	if statErr != nil {
		t.Fatalf("stat dstFile: %v", statErr)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("expected mode 0600, got %04o", info.Mode().Perm())
	}
}

func TestCopyPlainFileErrors(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	srcFile := filepath.Join(root, "valid.txt")
	if err := os.WriteFile(srcFile, []byte("valid"), 0o600); err != nil {
		t.Fatalf("write source: %v", err)
	}
	dstFile := filepath.Join(root, "out.txt")

	if err := copyPlainFile(filepath.Join(root, "missing.txt"), dstFile, 0o600); err == nil {
		t.Fatal("expected error on missing source file")
	}

	fileParent := filepath.Join(root, "parent_as_file")
	if err := os.WriteFile(fileParent, []byte("blocker"), 0o600); err != nil {
		t.Fatalf("write fileParent: %v", err)
	}
	invalidDst := filepath.Join(fileParent, "child", "target.txt")
	if err := copyPlainFile(srcFile, invalidDst, 0o600); err == nil {
		t.Fatal("expected error when parent cannot be created")
	}

	dirOpenErr := filepath.Join(root, "unreadable_dir")
	if err := os.MkdirAll(dirOpenErr, 0o750); err != nil {
		t.Fatalf("mkdir unreadable_dir: %v", err)
	}
	resetPerm := os.FileMode(0o700)
	t.Cleanup(func() {
		if cErr := os.Chmod(dirOpenErr, resetPerm); cErr != nil {
			t.Errorf("cleanup chmod: %v", cErr)
		}
	})
	if err := os.Chmod(dirOpenErr, 0o000); err == nil {
		if err := copyPlainFile(srcFile, filepath.Join(dirOpenErr, "file.txt"), 0o600); err == nil || !strings.Contains(err.Error(), "open destination dir") {
			t.Fatalf("expected open destination dir error, got %v", err)
		}
		if cErr := os.Chmod(dirOpenErr, resetPerm); cErr != nil {
			t.Errorf("restore chmod: %v", cErr)
		}
	}

	existingDirAsTarget := filepath.Join(root, "dir_target")
	if err := os.MkdirAll(existingDirAsTarget, 0o750); err != nil {
		t.Fatalf("mkdir dir_target: %v", err)
	}
	if err := copyPlainFile(srcFile, existingDirAsTarget, 0o600); err == nil || !strings.Contains(err.Error(), "open") {
		t.Fatalf("expected open file error when target is dir, got %v", err)
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

type fakeDirEntry struct {
	infoErr error
	name    string
	isDir   bool
}

func (f fakeDirEntry) Name() string               { return f.name }
func (f fakeDirEntry) IsDir() bool                { return f.isDir }
func (f fakeDirEntry) Type() fs.FileMode          { return 0 }
func (f fakeDirEntry) Info() (fs.FileInfo, error) { return nil, f.infoErr }

func TestCopyDirectoryContentsStatErrors(t *testing.T) {
	root := t.TempDir()
	srcDir := filepath.Join(root, "src")
	if err := os.MkdirAll(srcDir, 0o750); err != nil {
		t.Fatalf("mkdir src: %v", err)
	}

	origWalk := filepathWalkDir
	t.Cleanup(func() { filepathWalkDir = origWalk })

	filepathWalkDir = func(_ string, fn fs.WalkDirFunc) error {
		return fn(filepath.Join(srcDir, "mock_dir"), fakeDirEntry{
			infoErr: errors.New("stat dir error"),
			name:    "mock_dir",
			isDir:   true,
		}, nil)
	}
	if err := copyDirectoryContents(srcDir, filepath.Join(root, "dst")); err == nil {
		t.Fatal("expected stat dir error")
	}

	filepathWalkDir = func(_ string, fn fs.WalkDirFunc) error {
		return fn(filepath.Join(srcDir, "mock_file"), fakeDirEntry{
			infoErr: errors.New("stat file error"),
			name:    "mock_file",
			isDir:   false,
		}, nil)
	}
	if err := copyDirectoryContents(srcDir, filepath.Join(root, "dst")); err == nil {
		t.Fatal("expected stat file error")
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

	rel, err := defaultFilepathRel("/abs/path", "/abs/path/nested")
	if err != nil || rel != "nested" {
		t.Fatalf("expected nested, got %q err=%v", rel, err)
	}

	if _, err := defaultFilepathRel("/abs/path", "relative/path"); err == nil {
		t.Fatal("expected error between abs and rel paths")
	}
}
