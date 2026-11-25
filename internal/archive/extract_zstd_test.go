package archive

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExtractStackFilesCleanSlateWithLimitsSuccessAndSymlinks(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()
	stackDir := filepath.Join(tmp, "stack")
	rawTar := makeCustomTarBytes(t, []customTarEntry{
		{header: &tar.Header{Name: "./", Mode: 0o755, Typeflag: tar.TypeDir}},
		{header: &tar.Header{Name: "./sub/file.txt", Mode: 0o640, Typeflag: tar.TypeReg, Size: 3}, body: []byte("sub")},
		{header: &tar.Header{Name: "ro_dir", Mode: 0o4750, Typeflag: tar.TypeDir}},
		{header: &tar.Header{Name: "ro_dir/inner", Mode: 0o750, Typeflag: tar.TypeDir}},
		{header: &tar.Header{Name: "ro_dir/inner/service.conf", Mode: 0o4640, Typeflag: tar.TypeReg, Size: 5}, body: []byte("cfg=1")},
		{header: &tar.Header{Name: "ro_dir/inner/current", Typeflag: tar.TypeSymlink, Linkname: "service.conf"}},
		{header: &tar.Header{Name: "top_link", Typeflag: tar.TypeSymlink, Linkname: "sub/file.txt"}},
	})
	archivePath := filepath.Join(tmp, "files.tar.zst")
	if err := os.WriteFile(archivePath, rawTar, 0o600); err != nil {
		t.Fatalf("write archive: %v", err)
	}

	engine := NewEngine(&fakeArchiveRunner{})
	if err := engine.ExtractStackFilesCleanSlateWithLimits(context.Background(), archivePath, stackDir, int64(len(rawTar))); err != nil {
		t.Fatalf("ExtractStackFilesCleanSlateWithLimits error: %v", err)
	}

	info, err := os.Stat(filepath.Join(stackDir, "ro_dir"))
	if err != nil {
		t.Fatalf("stat ro_dir: %v", err)
	}
	if info.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 {
		t.Fatalf("expected special mode bits stripped on ro_dir, got %v", info.Mode())
	}

	fileInfo, fileErr := os.Stat(filepath.Join(stackDir, "ro_dir", "inner", "service.conf"))
	if fileErr != nil {
		t.Fatalf("stat service.conf: %v", fileErr)
	}
	if fileInfo.Mode().Perm() != 0o640 || fileInfo.Mode()&os.ModeSetuid != 0 {
		t.Fatalf("expected stripped mode 0640 on service.conf, got %v", fileInfo.Mode())
	}
	linkTarget, linkErr := os.Readlink(filepath.Join(stackDir, "top_link"))
	if linkErr != nil || linkTarget != "sub/file.txt" {
		t.Fatalf("unexpected top_link target %q, err=%v", linkTarget, linkErr)
	}
}

func TestExtractStackTarSecurityRejections(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		entries  []customTarEntry
		maxBytes int64
		corrupt  bool
	}{
		{
			name: "absolute_symlink_rejected",
			entries: []customTarEntry{
				{header: &tar.Header{Name: "bad_abs", Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd"}},
			},
		},
		{
			name: "empty_symlink_rejected",
			entries: []customTarEntry{
				{header: &tar.Header{Name: "bad_empty", Typeflag: tar.TypeSymlink, Linkname: ""}},
			},
		},
		{
			name: "escaping_relative_symlink_rejected",
			entries: []customTarEntry{
				{header: &tar.Header{Name: "sub/bad_rel", Typeflag: tar.TypeSymlink, Linkname: "../../outside"}},
			},
		},
		{
			name: "hardlink_rejected",
			entries: []customTarEntry{
				{header: &tar.Header{Name: "hard", Typeflag: tar.TypeLink, Linkname: "hard_target"}},
			},
		},
		{
			name: "traversal_member_rejected",
			entries: []customTarEntry{
				{header: &tar.Header{Name: "../stack_escape.txt", Typeflag: tar.TypeReg, Size: 3}, body: []byte("abc")},
			},
		},
		{
			name: "symlink_dir_traversal_rejected",
			entries: []customTarEntry{
				{header: &tar.Header{Name: "sub", Typeflag: tar.TypeSymlink, Linkname: "real_dir_one"}},
				{header: &tar.Header{Name: "sub/child.txt", Typeflag: tar.TypeReg, Size: 3}, body: []byte("abc")},
			},
		},
		{
			name: "file_as_parent_dir_rejected",
			entries: []customTarEntry{
				{header: &tar.Header{Name: "blocker", Typeflag: tar.TypeReg, Mode: 0o600, Size: 3}, body: []byte("abc")},
				{header: &tar.Header{Name: "blocker/child.txt", Typeflag: tar.TypeReg, Size: 3}, body: []byte("abc")},
			},
		},
		{
			name: "overwrite_dir_with_file_rejected",
			entries: []customTarEntry{
				{header: &tar.Header{Name: "dir1", Typeflag: tar.TypeDir, Mode: 0o750}},
				{header: &tar.Header{Name: "dir1", Typeflag: tar.TypeReg, Mode: 0o600, Size: 3}, body: []byte("abc")},
			},
		},
		{
			name: "symlink_collision_with_existing_file_rejected",
			entries: []customTarEntry{
				{header: &tar.Header{Name: "dup", Typeflag: tar.TypeReg, Mode: 0o600, Size: 3}, body: []byte("abc")},
				{header: &tar.Header{Name: "dup", Typeflag: tar.TypeSymlink, Linkname: "other"}},
			},
		},
		{
			name: "parent_symlink_before_symlink_rejected",
			entries: []customTarEntry{
				{header: &tar.Header{Name: "sym", Typeflag: tar.TypeSymlink, Linkname: "real_dir_two"}},
				{header: &tar.Header{Name: "sym/nested", Typeflag: tar.TypeSymlink, Linkname: "nested_target"}},
			},
		},
		{
			name: "dir_through_symlink_rejected",
			entries: []customTarEntry{
				{header: &tar.Header{Name: "sym2", Typeflag: tar.TypeSymlink, Linkname: "real_dir_three"}},
				{header: &tar.Header{Name: "sym2/sub", Typeflag: tar.TypeDir, Mode: 0o750}},
			},
		},
		{
			name:     "decompressed_stream_exceeds_limit",
			maxBytes: 100,
			entries: []customTarEntry{
				{header: &tar.Header{Name: "big.bin", Typeflag: tar.TypeReg, Mode: 0o600, Size: 200}, body: bytes.Repeat([]byte("a"), 200)},
			},
		},
		{
			name:    "corrupt_tar_stream_rejected",
			corrupt: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			tmp := t.TempDir()
			payload := []byte("not-a-valid-tar-header-stream")
			if !tt.corrupt {
				payload = makeCustomTarBytes(t, tt.entries)
			}
			archivePath := filepath.Join(tmp, "case.tar.zst")
			if err := os.WriteFile(archivePath, payload, 0o600); err != nil {
				t.Fatalf("write archive: %v", err)
			}

			engine := NewEngine(&fakeArchiveRunner{})
			if err := engine.ExtractStackFilesCleanSlateWithLimits(context.Background(), archivePath, filepath.Join(tmp, "dst"), tt.maxBytes); err == nil {
				t.Fatalf("expected security/limit rejection for %s", tt.name)
			}
		})
	}
}

func TestStreamDecompressAndLimitedReaderEdgeCases(t *testing.T) {
	tmp := t.TempDir()
	engine := NewEngine(&fakeArchiveRunner{})

	if err := engine.streamDecompressTar(context.Background(), filepath.Join(tmp, "missing.tar.zst"), 1024, func(*tar.Reader) error {
		return nil
	}); err == nil {
		t.Fatal("expected missing archive open error")
	}

	stubPath := filepath.Join(tmp, "stub.tar.zst")
	if err := os.WriteFile(stubPath, []byte("stub"), 0o600); err != nil {
		t.Fatalf("write stub: %v", err)
	}

	origPipe := archivePipe
	t.Cleanup(func() { archivePipe = origPipe })
	archivePipe = func() (*os.File, *os.File, error) {
		return nil, nil, errors.New("pipe failed")
	}
	if err := engine.streamDecompressTar(context.Background(), stubPath, 1024, func(*tar.Reader) error {
		return nil
	}); err == nil {
		t.Fatal("expected pipe error")
	}
	archivePipe = origPipe

	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := extractStackTarStream(canceledCtx, tar.NewReader(bytes.NewReader(nil)), nil); err == nil {
		t.Fatal("expected canceled context error in extractStackTarStream")
	}
}

func TestLimitedByteReaderEdgeCases(t *testing.T) {
	t.Parallel()

	exact := &limitedByteReader{r: strings.NewReader("abcd"), remaining: 4, limit: 4}
	buf := make([]byte, 8)
	nExact, errExact := exact.Read(buf)
	if errExact != nil || nExact != 4 {
		t.Fatalf("expected 4 bytes read on exact stream, got (%d, %v)", nExact, errExact)
	}
	if nEOF, errEOF := exact.Read(buf); nEOF != 0 || !errors.Is(errEOF, io.EOF) {
		t.Fatalf("expected (0, io.EOF) at exact boundary, got (%d, %v)", nEOF, errEOF)
	}

	lr := &limitedByteReader{r: strings.NewReader("abcdef"), remaining: 4, limit: 4}
	n, err := lr.Read(buf)
	if err != nil || n != 4 {
		t.Fatalf("expected 4 bytes read, got (%d, %v)", n, err)
	}
	if _, err := lr.Read(buf); err == nil || !strings.Contains(err.Error(), "exceeds maximum allowed size") {
		t.Fatalf("expected size overflow error, got %v", err)
	}

	failTail := &limitedByteReader{r: &failingReader{err: errors.New("tail err")}, remaining: 0, limit: 4}
	if _, err := failTail.Read(buf); err == nil || !strings.Contains(err.Error(), "read decompressed tail") {
		t.Fatalf("expected tail error, got %v", err)
	}

	failMid := &limitedByteReader{r: &failingReader{err: errors.New("mid err")}, remaining: 4, limit: 4}
	if _, err := failMid.Read(buf); err == nil || !strings.Contains(err.Error(), "read decompressed stream") {
		t.Fatalf("expected mid read error, got %v", err)
	}
}

func TestEnsureRootDirAndPendingMetadata(t *testing.T) {
	tmp := t.TempDir()
	root, err := os.OpenRoot(tmp)
	if err != nil {
		t.Fatalf("open root: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := root.Close(); closeErr != nil {
			t.Errorf("close root: %v", closeErr)
		}
	})

	if err := ensureRootDirNoSymlink(root, strings.Repeat("a", 300)); err == nil {
		t.Fatal("expected Lstat non-ErrNotExist error for overlong component")
	}

	origMkdir := rootMkdir
	t.Cleanup(func() {
		rootMkdir = origMkdir
	})
	rootMkdir = func(*os.Root, string, os.FileMode) error {
		return errors.New("simulated root mkdir failure")
	}
	if err := ensureRootDirNoSymlink(root, "mkdir_err_dir"); err == nil {
		t.Fatal("expected rootMkdir error")
	}
	rootMkdir = origMkdir

	if err := applyPendingDirMetadata(root, []pendingDirMeta{{relOS: "missing_dir", mode: 0o750}}); err == nil {
		t.Fatal("expected Chmod error on missing directory")
	}
}

func TestApplyNumericOwnerAndTruncatedEntry(t *testing.T) {
	tmp := t.TempDir()
	fileForChown := "owned.txt"
	if wErr := os.WriteFile(filepath.Join(tmp, fileForChown), []byte("x"), 0o600); wErr != nil {
		t.Fatalf("write owned file: %v", wErr)
	}
	if wErr := os.MkdirAll(filepath.Join(tmp, "owned_dir"), 0o750); wErr != nil {
		t.Fatalf("mkdir owned_dir: %v", wErr)
	}

	root, err := os.OpenRoot(tmp)
	if err != nil {
		t.Fatalf("open root: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := root.Close(); closeErr != nil {
			t.Errorf("close root: %v", closeErr)
		}
	})

	origEuid := currentEuid
	origLchown := rootLchown
	t.Cleanup(func() {
		currentEuid = origEuid
		rootLchown = origLchown
	})

	currentEuid = func() int { return 0 }
	rootLchown = func(*os.Root, string, int, int) error {
		return errors.New("simulated lchown failure")
	}
	if err := applyPendingDirMetadata(root, []pendingDirMeta{{relOS: "owned_dir", mode: 0o750, uid: 1000, gid: 1000}}); err == nil {
		t.Fatal("expected applyNumericOwner failure in applyPendingDirMetadata")
	}

	rootLchown = func(*os.Root, string, int, int) error {
		return nil
	}
	if err := applyNumericOwner(root, fileForChown, 1000, 1000); err != nil {
		t.Fatalf("expected applyNumericOwner success, got %v", err)
	}

	truncatedTar := makeCustomTarBytes(t, []customTarEntry{
		{header: &tar.Header{Name: "part.txt", Mode: 0o600, Typeflag: tar.TypeReg, Size: 10}, body: []byte("0123456789")},
	})
	tr := tar.NewReader(bytes.NewReader(truncatedTar[:517]))
	if _, err := tr.Next(); err != nil {
		t.Fatalf("next header: %v", err)
	}
	if err := writeRegularRootEntryNoFollow(root, "trunc.txt", tr, 0o600, 0, 0); err == nil {
		t.Fatal("expected io.Copy error on truncated tar entry")
	}
}
