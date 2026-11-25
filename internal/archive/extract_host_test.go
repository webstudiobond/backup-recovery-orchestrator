package archive

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestExtractHostFilesSecurityAndFiltering(t *testing.T) {
	hostDir := t.TempDir()
	origOpenHostRoot := openHostRoot
	openHostRoot = func() (*os.Root, error) {
		return os.OpenRoot(hostDir)
	}
	t.Cleanup(func() {
		openHostRoot = origOpenHostRoot
	})

	tmp := t.TempDir()
	rawTar := makeCustomTarBytes(t, []customTarEntry{
		{header: &tar.Header{Name: ".", Mode: 0o755, Typeflag: tar.TypeDir}},
		{header: &tar.Header{Name: "etc/ignored_dir", Mode: 0o755, Typeflag: tar.TypeDir}},
		{header: &tar.Header{Name: "etc/allowed_dir", Mode: 0o700, Typeflag: tar.TypeDir}},
		{header: &tar.Header{Name: "etc/cron.d/allowed_job", Mode: 0o4600, Typeflag: tar.TypeReg, Size: 7}, body: []byte("allowed")},
		{header: &tar.Header{Name: "etc/cron.d/unlisted_job", Mode: 0o600, Typeflag: tar.TypeReg, Size: 8}, body: []byte("unlisted")},
	})
	archivePath := filepath.Join(tmp, "host.tar.zst")
	if err := os.WriteFile(archivePath, rawTar, 0o600); err != nil {
		t.Fatalf("write host archive: %v", err)
	}

	engine := NewEngine(&fakeArchiveRunner{})
	err := engine.ExtractHostFilesWithLimits(
		context.Background(),
		archivePath,
		[]string{"etc/allowed_dir", "etc/cron.d/allowed_job"},
		int64(len(rawTar)),
	)
	if err != nil {
		t.Fatalf("ExtractHostFilesWithLimits error: %v", err)
	}

	hostRoot, openErr := os.OpenRoot(hostDir)
	if openErr != nil {
		t.Fatalf("open hostDir root: %v", openErr)
	}
	defer func() {
		if closeErr := hostRoot.Close(); closeErr != nil {
			t.Errorf("close hostDir root: %v", closeErr)
		}
	}()
	got, readErr := hostRoot.ReadFile(filepath.Join("etc", "cron.d", "allowed_job"))
	if readErr != nil || string(got) != "allowed" {
		t.Fatalf("expected allowed_job extracted, got %q, err=%v", string(got), readErr)
	}
	info, statErr := os.Stat(filepath.Join(hostDir, "etc", "cron.d", "allowed_job"))
	if statErr != nil || info.Mode()&os.ModeSetuid != 0 {
		t.Fatalf("expected stripped setuid bit on host file, err=%v, mode=%v", statErr, info.Mode())
	}
	if _, err := os.Stat(filepath.Join(hostDir, "etc", "cron.d", "unlisted_job")); !os.IsNotExist(err) {
		t.Fatal("expected unlisted_job to be skipped")
	}
	if _, err := os.Stat(filepath.Join(hostDir, "etc", "ignored_dir")); !os.IsNotExist(err) {
		t.Fatal("expected ignored_dir to be skipped")
	}

	hostFS, rootErr := defaultOpenHostRoot()
	if rootErr != nil {
		t.Fatalf("defaultOpenHostRoot error: %v", rootErr)
	}
	if closeErr := hostFS.Close(); closeErr != nil {
		t.Fatalf("close defaultOpenHostRoot: %v", closeErr)
	}
}

func TestExtractHostFilesRejections(t *testing.T) {
	tmp := t.TempDir()

	tests := []struct {
		setupHost func(t *testing.T, hostDir string)
		entries   []customTarEntry
		name      string
		allowed   []string
		corrupt   bool
	}{
		{
			name:    "symlink_entry_rejected",
			allowed: []string{"etc/link"},
			entries: []customTarEntry{
				{header: &tar.Header{Name: "etc/link", Typeflag: tar.TypeSymlink, Linkname: "host_symlink_target"}},
			},
		},
		{
			name:    "hardlink_entry_rejected",
			allowed: []string{"etc/hard"},
			entries: []customTarEntry{
				{header: &tar.Header{Name: "etc/hard", Typeflag: tar.TypeLink, Linkname: "host_hardlink_target"}},
			},
		},
		{
			name:    "unsafe_member_path_rejected",
			allowed: []string{"etc/ok"},
			entries: []customTarEntry{
				{header: &tar.Header{Name: "../escape", Typeflag: tar.TypeReg, Size: 2}, body: []byte("no")},
			},
		},
		{
			name:    "corrupt_tar_stream_rejected",
			allowed: []string{"etc/ok"},
			corrupt: true,
		},
		{
			name:    "dir_mkdir_blocked_by_file",
			allowed: []string{"etc/blocked_dir"},
			setupHost: func(t *testing.T, hostDir string) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(hostDir, "etc"), []byte("file"), 0o600); err != nil {
					t.Fatalf("write blocker: %v", err)
				}
			},
			entries: []customTarEntry{
				{header: &tar.Header{Name: "etc/blocked_dir", Mode: 0o750, Typeflag: tar.TypeDir}},
			},
		},
		{
			name:    "file_parent_mkdir_blocked_by_file",
			allowed: []string{"etc/sub/file.conf"},
			setupHost: func(t *testing.T, hostDir string) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(hostDir, "etc"), []byte("file"), 0o600); err != nil {
					t.Fatalf("write blocker: %v", err)
				}
			},
			entries: []customTarEntry{
				{header: &tar.Header{Name: "etc/sub/file.conf", Mode: 0o600, Typeflag: tar.TypeReg, Size: 2}, body: []byte("ok")},
			},
		},
	}

	origOpenHostRoot := openHostRoot
	t.Cleanup(func() {
		openHostRoot = origOpenHostRoot
	})

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hostDir := t.TempDir()
			if tt.setupHost != nil {
				tt.setupHost(t, hostDir)
			}
			openHostRoot = func() (*os.Root, error) {
				return os.OpenRoot(hostDir)
			}

			payload := []byte("corrupt-host-tar")
			if !tt.corrupt {
				payload = makeCustomTarBytes(t, tt.entries)
			}
			archivePath := filepath.Join(tmp, tt.name+".tar.zst")
			if err := os.WriteFile(archivePath, payload, 0o600); err != nil {
				t.Fatalf("write host tar: %v", err)
			}

			engine := NewEngine(&fakeArchiveRunner{})
			if err := engine.ExtractHostFilesWithLimits(context.Background(), archivePath, tt.allowed, 1024); err == nil {
				t.Fatalf("expected error for %s", tt.name)
			}
		})
	}

	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := extractHostTarStream(canceledCtx, tar.NewReader(bytes.NewReader(nil)), nil, nil); err == nil {
		t.Fatal("expected canceled context error in extractHostTarStream")
	}

	t.Run("dir_lchown_error", func(t *testing.T) {
		dirRoot := t.TempDir()
		root, err := os.OpenRoot(dirRoot)
		if err != nil {
			t.Fatalf("open root: %v", err)
		}
		t.Cleanup(func() {
			if closeErr := root.Close(); closeErr != nil {
				t.Errorf("close root: %v", closeErr)
			}
		})

		roBase := filepath.Join(dirRoot, "ro")
		if err := os.MkdirAll(filepath.Join(roBase, "locked_dir"), 0o750); err != nil {
			t.Fatalf("mkdir ro: %v", err)
		}

		origEuid := currentEuid
		origLchown := rootLchown
		t.Cleanup(func() {
			currentEuid = origEuid
			rootLchown = origLchown
		})
		currentEuid = func() int { return 0 }
		rootLchown = func(*os.Root, string, int, int) error {
			return errors.New("simulated host lchown error")
		}
		allowed := map[string]struct{}{"ro/locked_dir": {}}
		hdr := &tar.Header{Name: "ro/locked_dir", Typeflag: tar.TypeDir, Mode: 0o700, Uid: 1000, Gid: 1000}
		if err := extractHostEntry(root, hdr, "ro/locked_dir", bytes.NewReader(nil), allowed); err == nil {
			t.Fatal("expected lchown error for allowed host dir")
		}
	})
}
