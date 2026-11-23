package archive

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestExtractStackFilesCleanSlateAndHostFiles(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	stackDir := filepath.Join(t.TempDir(), "site1")
	staleFile := filepath.Join(stackDir, "stale.txt")
	if err := os.MkdirAll(stackDir, 0o750); err != nil {
		t.Fatalf("mkdir stack: %v", err)
	}
	if err := os.WriteFile(staleFile, []byte("stale"), 0o600); err != nil {
		t.Fatalf("write stale: %v", err)
	}

	runner := &fakeArchiveRunner{}
	engine := NewEngine(runner)
	filesPath := "/tmp/site1_files_2026-11-16_13-00-00.tar.zst"
	if err := engine.ExtractStackFilesCleanSlate(ctx, filesPath, stackDir); err != nil {
		t.Fatalf("ExtractStackFilesCleanSlate error: %v", err)
	}
	if _, statErr := os.Stat(staleFile); !os.IsNotExist(statErr) {
		t.Fatalf("expected stale file to be cleared before extraction")
	}
	if len(runner.calls) != 1 || !slices.Equal(runner.calls[0].args, []string{"--numeric-owner", "-xpf", filesPath, "-C", stackDir}) {
		t.Fatalf("unexpected tar extract call: %+v", runner.calls)
	}

	if err := engine.ExtractHostFiles(ctx, "/tmp/site1_host_files_2026-11-16_13-00-00.tar.zst", []string{"../etc/passwd"}); err == nil {
		t.Fatalf("expected path traversal rejection in ExtractHostFiles")
	}
}

func TestUnpackContainerErrors(t *testing.T) {
	root := t.TempDir()
	validTar := filepath.Join(root, "valid.tar")
	writeRawTar(t, validTar, map[string]string{
		"sample.txt": "sample content",
	})

	corruptTar := filepath.Join(root, "corrupt.tar")
	if err := os.WriteFile(corruptTar, []byte("corrupt-tar-payload"), 0o600); err != nil {
		t.Fatalf("write corrupt tar: %v", err)
	}

	blockingFile := filepath.Join(root, "file_blocker")
	if err := os.WriteFile(blockingFile, []byte("data"), 0o600); err != nil {
		t.Fatalf("write blocker: %v", err)
	}

	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()

	origOpenRoot := openRoot
	t.Cleanup(func() {
		openRoot = origOpenRoot
	})

	tests := []struct {
		ctx       context.Context
		setupMock func(t *testing.T)
		name      string
		tarPath   string
		dstDir    string
		wantErr   bool
	}{
		{
			ctx:     canceledCtx,
			name:    "canceled_unpack_context",
			tarPath: validTar,
			dstDir:  filepath.Join(root, "dst1"),
			wantErr: true,
		},
		{
			ctx:     context.Background(),
			name:    "mkdir_dst_failure",
			tarPath: validTar,
			dstDir:  filepath.Join(blockingFile, "sub"),
			wantErr: true,
		},
		{
			ctx:     context.Background(),
			name:    "open_root_failure",
			tarPath: validTar,
			dstDir:  filepath.Join(root, "dst2"),
			setupMock: func(t *testing.T) {
				t.Helper()
				openRoot = func(string) (*os.Root, error) {
					return nil, errors.New("simulated open root failure")
				}
			},
			wantErr: true,
		},
		{
			ctx:     context.Background(),
			name:    "open_container_file_failure",
			tarPath: filepath.Join(root, "nonexistent.tar"),
			dstDir:  filepath.Join(root, "dst3"),
			wantErr: true,
		},
		{
			ctx:     context.Background(),
			name:    "extract_members_failure",
			tarPath: corruptTar,
			dstDir:  filepath.Join(root, "dst4"),
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			openRoot = origOpenRoot
			if tt.setupMock != nil {
				tt.setupMock(t)
			}

			err := UnpackContainer(tt.ctx, tt.tarPath, tt.dstDir)
			if (err != nil) != tt.wantErr {
				t.Fatalf("UnpackContainer error = %v, wantErr = %v", err, tt.wantErr)
			}
		})
	}
}

func createTestDstRoot(t *testing.T, dir string) (string, *os.Root) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatalf("mkdir dst: %v", err)
	}
	r, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatalf("open root: %v", err)
	}
	t.Cleanup(func() {
		if cErr := r.Close(); cErr != nil {
			t.Fatalf("close root: %v", cErr)
		}
	})
	return dir, r
}

func makeTestTar(t *testing.T, headers []*tar.Header, bodies [][]byte) io.Reader {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for i, hdr := range headers {
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("write header: %v", err)
		}
		if i < len(bodies) && len(bodies[i]) > 0 {
			if _, err := tw.Write(bodies[i]); err != nil {
				t.Fatalf("write body: %v", err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tw: %v", err)
	}
	return &buf
}

func TestExtractTarMembersBasic(t *testing.T) {
	t.Parallel()

	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()

	tests := []struct {
		ctx     context.Context
		reader  io.Reader
		name    string
		wantErr bool
	}{
		{
			ctx:     canceledCtx,
			reader:  &bytes.Buffer{},
			name:    "canceled_context_during_extraction",
			wantErr: true,
		},
		{
			ctx:     context.Background(),
			reader:  strings.NewReader("corrupted tar payload bytes"),
			name:    "corrupted_reader_data",
			wantErr: true,
		},
		{
			ctx: context.Background(),
			reader: makeTestTar(t,
				[]*tar.Header{
					{Name: "subfolder/", Typeflag: tar.TypeDir, Mode: 0o750},
					{Name: "subfolder/app.txt", Typeflag: tar.TypeReg, Size: 4, Mode: 0o600},
				},
				[][]byte{nil, []byte("data")},
			),
			name:    "directory_entry_skipped_and_file_extracted",
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			dst, rootObj := createTestDstRoot(t, filepath.Join(t.TempDir(), "dst"))
			err := extractTarMembers(tt.ctx, tt.reader, dst, rootObj)
			if (err != nil) != tt.wantErr {
				t.Fatalf("extractTarMembers error = %v, wantErr = %v", err, tt.wantErr)
			}
		})
	}
}

func TestExtractTarMembersValidation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		setupDst func(t *testing.T, base string) (string, *os.Root)
		reader   io.Reader
		name     string
		wantErr  bool
	}{
		{
			reader: makeTestTar(t,
				[]*tar.Header{{Name: "link.txt", Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd"}},
				nil,
			),
			name:    "unsupported_non_regular_entry_rejected",
			wantErr: true,
		},
		{
			reader: makeTestTar(t,
				[]*tar.Header{{Name: "../escape.txt", Typeflag: tar.TypeReg, Size: 4}},
				[][]byte{[]byte("data")},
			),
			name:    "traversal_member_path_rejected",
			wantErr: true,
		},
		{
			setupDst: func(t *testing.T, base string) (string, *os.Root) {
				t.Helper()
				dst, rootObj := createTestDstRoot(t, filepath.Join(base, "dst"))
				if err := os.WriteFile(filepath.Join(dst, "blocker"), []byte("data"), 0o600); err != nil {
					t.Fatalf("write blocker: %v", err)
				}
				return dst, rootObj
			},
			reader: makeTestTar(t,
				[]*tar.Header{{Name: "blocker/child.txt", Typeflag: tar.TypeReg, Size: 4}},
				[][]byte{[]byte("data")},
			),
			name:    "member_directory_mkdir_failure",
			wantErr: true,
		},
		{
			setupDst: func(t *testing.T, base string) (string, *os.Root) {
				t.Helper()
				dst, rootObj := createTestDstRoot(t, filepath.Join(base, "dst"))
				if err := os.MkdirAll(filepath.Join(dst, "file_as_dir"), 0o750); err != nil {
					t.Fatalf("mkdir file_as_dir: %v", err)
				}
				return dst, rootObj
			},
			reader: makeTestTar(t,
				[]*tar.Header{{Name: "file_as_dir", Typeflag: tar.TypeReg, Size: 4}},
				[][]byte{[]byte("data")},
			),
			name:    "write_root_file_failure_when_target_is_dir",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			base := t.TempDir()
			var dst string
			var rootObj *os.Root
			if tt.setupDst != nil {
				dst, rootObj = tt.setupDst(t, base)
			} else {
				dst, rootObj = createTestDstRoot(t, filepath.Join(base, "dst"))
			}

			err := extractTarMembers(context.Background(), tt.reader, dst, rootObj)
			if (err != nil) != tt.wantErr {
				t.Fatalf("extractTarMembers error = %v, wantErr = %v", err, tt.wantErr)
			}
		})
	}
}

func TestWriteRootFile(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	r, err := os.OpenRoot(root)
	if err != nil {
		t.Fatalf("open root: %v", err)
	}
	t.Cleanup(func() {
		if cErr := r.Close(); cErr != nil {
			t.Fatalf("close root: %v", cErr)
		}
	})

	if err := os.WriteFile(filepath.Join(root, "existing_file"), []byte("file"), 0o600); err != nil {
		t.Fatalf("write existing: %v", err)
	}

	tests := []struct {
		src     io.Reader
		name    string
		relPath string
		wantErr bool
	}{
		{
			src:     strings.NewReader("hello world"),
			name:    "successful_root_file_write",
			relPath: "success.txt",
			wantErr: false,
		},
		{
			src:     strings.NewReader("payload"),
			name:    "open_file_failure_through_file_as_dir",
			relPath: "existing_file/child.txt",
			wantErr: true,
		},
		{
			src:     &failingReader{err: errors.New("simulated read failure")},
			name:    "copy_error_from_failing_src_reader",
			relPath: "failing_read.txt",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := writeRootFile(r, tt.relPath, tt.src, 0o600)
			if (err != nil) != tt.wantErr {
				t.Fatalf("writeRootFile error = %v, wantErr = %v", err, tt.wantErr)
			}
		})
	}
}

func TestExtractStackFilesCleanSlateErrors(t *testing.T) {
	root := t.TempDir()
	validDir := filepath.Join(root, "valid_stack")
	if err := os.MkdirAll(validDir, 0o750); err != nil {
		t.Fatalf("mkdir validDir: %v", err)
	}

	fileAsDir := filepath.Join(root, "file_as_dir")
	if err := os.WriteFile(fileAsDir, []byte("data"), 0o600); err != nil {
		t.Fatalf("write fileAsDir: %v", err)
	}

	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()

	origMkdirAll := osMkdirAll
	t.Cleanup(func() {
		osMkdirAll = origMkdirAll
	})

	tests := []struct {
		ctx       context.Context
		setupMock func(t *testing.T)
		runnerErr error
		name      string
		stackDir  string
		wantErr   bool
	}{
		{
			ctx:      canceledCtx,
			name:     "canceled_extract_stack_context",
			stackDir: validDir,
			wantErr:  true,
		},
		{
			ctx:      context.Background(),
			name:     "unsafe_stack_dir_empty",
			stackDir: "",
			wantErr:  true,
		},
		{
			ctx:      context.Background(),
			name:     "unsafe_stack_dir_dot",
			stackDir: ".",
			wantErr:  true,
		},
		{
			ctx:      context.Background(),
			name:     "unsafe_stack_dir_root_slash",
			stackDir: "/",
			wantErr:  true,
		},
		{
			ctx:      context.Background(),
			name:     "clear_directory_contents_failure",
			stackDir: fileAsDir,
			wantErr:  true,
		},
		{
			ctx: context.Background(),
			setupMock: func(t *testing.T) {
				t.Helper()
				osMkdirAll = func(string, os.FileMode) error {
					return errors.New("simulated mkdir failure")
				}
			},
			name:     "mkdir_stack_dir_failure",
			stackDir: validDir,
			wantErr:  true,
		},
		{
			ctx:       context.Background(),
			runnerErr: errors.New("simulated tar run failure"),
			name:      "tar_execution_failure",
			stackDir:  validDir,
			wantErr:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			osMkdirAll = origMkdirAll
			if tt.setupMock != nil {
				tt.setupMock(t)
			}
			runner := &fakeArchiveRunner{runErr: tt.runnerErr}
			engine := NewEngine(runner)

			err := engine.ExtractStackFilesCleanSlate(tt.ctx, "/dummy/path.tar.zst", tt.stackDir)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ExtractStackFilesCleanSlate error = %v, wantErr = %v", err, tt.wantErr)
			}
		})
	}
}

func TestExtractHostFiles(t *testing.T) {
	t.Parallel()

	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()

	tests := []struct {
		ctx       context.Context
		runnerErr error
		name      string
		paths     []string
		wantErr   bool
	}{
		{
			ctx:     canceledCtx,
			name:    "canceled_extract_host_context",
			paths:   []string{"etc/app.conf"},
			wantErr: true,
		},
		{
			ctx:     context.Background(),
			name:    "empty_allowed_rel_paths",
			paths:   nil,
			wantErr: false,
		},
		{
			ctx:     context.Background(),
			name:    "unsafe_tar_member_path_rejected",
			paths:   []string{"/absolute/path"},
			wantErr: true,
		},
		{
			ctx:     context.Background(),
			name:    "successful_host_files_extraction",
			paths:   []string{"etc/cron.d/job"},
			wantErr: false,
		},
		{
			ctx:       context.Background(),
			runnerErr: errors.New("simulated tar extract error"),
			name:      "runner_execution_failure",
			paths:     []string{"etc/cron.d/job"},
			wantErr:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			runner := &fakeArchiveRunner{runErr: tt.runnerErr}
			engine := NewEngine(runner)

			err := engine.ExtractHostFiles(tt.ctx, "/dummy/host.tar.zst", tt.paths)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ExtractHostFiles error = %v, wantErr = %v", err, tt.wantErr)
			}
		})
	}
}

func TestClearDirectoryContents(t *testing.T) {
	root := t.TempDir()
	validDir := filepath.Join(root, "to_clear")
	if err := os.MkdirAll(validDir, 0o750); err != nil {
		t.Fatalf("mkdir to_clear: %v", err)
	}
	if err := os.WriteFile(filepath.Join(validDir, "file.txt"), []byte("content"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}

	fileAsDir := filepath.Join(root, "regular_file")
	if err := os.WriteFile(fileAsDir, []byte("file"), 0o600); err != nil {
		t.Fatalf("write regular_file: %v", err)
	}

	origRemoveAll := osRemoveAll
	t.Cleanup(func() {
		osRemoveAll = origRemoveAll
	})

	tests := []struct {
		setupMock func(t *testing.T)
		name      string
		dir       string
		wantErr   bool
	}{
		{
			name:    "nonexistent_directory_returns_nil",
			dir:     filepath.Join(root, "nonexistent_dir"),
			wantErr: false,
		},
		{
			name:    "reading_regular_file_returns_error",
			dir:     fileAsDir,
			wantErr: true,
		},
		{
			name: "remove_all_error_returns_failure",
			dir:  validDir,
			setupMock: func(t *testing.T) {
				t.Helper()
				osRemoveAll = func(string) error {
					return errors.New("simulated remove all error")
				}
			},
			wantErr: true,
		},
		{
			name:    "successful_clear_directory",
			dir:     validDir,
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			osRemoveAll = origRemoveAll
			if tt.setupMock != nil {
				tt.setupMock(t)
			}

			err := clearDirectoryContents(tt.dir)
			if (err != nil) != tt.wantErr {
				t.Fatalf("clearDirectoryContents(%q) error = %v, wantErr = %v", tt.dir, err, tt.wantErr)
			}
		})
	}
}

func TestSanitizeTarMemberPath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		raw     string
		want    string
		wantErr bool
	}{
		{
			name:    "empty_raw_string",
			raw:     "",
			wantErr: true,
		},
		{
			name:    "leading_slash_raw_string",
			raw:     "/etc/config",
			wantErr: true,
		},
		{
			name:    "null_byte_in_raw_string",
			raw:     "file\x00name",
			wantErr: true,
		},
		{
			name:    "current_dir_dot",
			raw:     ".",
			wantErr: true,
		},
		{
			name:    "parent_dir_double_dot",
			raw:     "..",
			wantErr: true,
		},
		{
			name:    "path_traversal_prefix",
			raw:     "../parent/file.txt",
			wantErr: true,
		},
		{
			name:    "valid_sanitized_member_path",
			raw:     "nested/dir/app.txt",
			want:    "nested/dir/app.txt",
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := sanitizeTarMemberPath(tt.raw)
			if (err != nil) != tt.wantErr {
				t.Fatalf("sanitizeTarMemberPath(%q) error = %v, wantErr = %v", tt.raw, err, tt.wantErr)
			}
			if !tt.wantErr && got != tt.want {
				t.Fatalf("sanitizeTarMemberPath(%q) = %q, want %q", tt.raw, got, tt.want)
			}
		})
	}
}
