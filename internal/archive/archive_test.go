package archive

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/webstudiobond/backup-recovery-orchestrator/internal/cmdexec"
	"github.com/webstudiobond/backup-recovery-orchestrator/internal/config"
)

func TestNewEngine(t *testing.T) {
	t.Parallel()

	engine := NewEngine(nil)
	if engine.Runner == nil {
		t.Fatal("expected non-nil runner in NewEngine(nil)")
	}
	if _, ok := engine.Runner.(cmdexec.OSRunner); !ok {
		t.Fatalf("expected OSRunner, got %T", engine.Runner)
	}
	if engine.Now == nil || engine.Now().IsZero() {
		t.Fatal("expected valid Now function in NewEngine")
	}
}

func TestStackSnapshotSubpath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		stackDir string
		want     string
	}{
		{
			name:     "home_prefix_path",
			stackDir: "/home/user1/site",
			want:     "user1/site",
		},
		{
			name:     "non_home_fallback_path",
			stackDir: "/srv/apps/site",
			want:     "site",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := stackSnapshotSubpath(tt.stackDir)
			if got != tt.want {
				t.Fatalf("stackSnapshotSubpath(%q) = %q, want %q", tt.stackDir, got, tt.want)
			}
		})
	}
}

func TestEnsureNonEmptyFile(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	emptyPath := filepath.Join(root, "empty.tar")
	if err := os.WriteFile(emptyPath, nil, 0o600); err != nil {
		t.Fatalf("write empty file: %v", err)
	}

	validPath := filepath.Join(root, "valid.tar")
	if err := os.WriteFile(validPath, []byte("content"), 0o600); err != nil {
		t.Fatalf("write valid file: %v", err)
	}

	missingPath := filepath.Join(root, "nonexistent.tar")

	tests := []struct {
		name    string
		path    string
		wantErr bool
	}{
		{
			name:    "valid_file",
			path:    validPath,
			wantErr: false,
		},
		{
			name:    "empty_file",
			path:    emptyPath,
			wantErr: true,
		},
		{
			name:    "missing_file",
			path:    missingPath,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := ensureNonEmptyFile(tt.path)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ensureNonEmptyFile(%q) error = %v, wantErr = %v", tt.path, err, tt.wantErr)
			}
		})
	}
}

func TestArchiveStackFiles(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := t.TempDir()
	snapMount := filepath.Join(root, "mnt_snap")
	if err := os.MkdirAll(filepath.Join(snapMount, "site1"), 0o750); err != nil {
		t.Fatalf("mkdir snap source: %v", err)
	}

	runner := &fakeArchiveRunner{}
	engine := NewEngine(runner)

	filesOut := filepath.Join(root, "stage", StackFilesArchiveName("site1", "2026-11-16_12-00-00"))
	filesCfg := config.FilesConfig{
		ExcludesFile: "/etc/bro/rules/app-excludes.conf",
		Excludes:     []string{"*.tmp", "cache/*"},
	}
	filesBytes, err := engine.ArchiveStackFiles(ctx, snapMount, "/home/site1", &filesCfg, 9, filesOut)
	if err != nil {
		t.Fatalf("ArchiveStackFiles error: %v", err)
	}
	if filesBytes != int64(len("raw-tar-payload")) {
		t.Fatalf("ArchiveStackFiles bytes = %d, want %d", filesBytes, len("raw-tar-payload"))
	}

	tarCall, ok := findRecordedCall(runner.calls, "tar")
	if !ok || !slices.Contains(tarCall.args, "--") || !slices.Contains(tarCall.args, "--exclude=cache/*") {
		t.Fatalf("expected tar -- --exclude=cache/* call, got %+v", runner.calls)
	}
	if slices.Contains(tarCall.args, "--no-wildcards-match-slash") {
		t.Fatalf("did not expect --no-wildcards-match-slash by default, got %+v", tarCall.args)
	}
	zstdCall, zstdOK := findRecordedCall(runner.calls, "zstd")
	if !zstdOK || !slices.Equal(zstdCall.args, []string{"-9", "-q"}) {
		t.Fatalf("expected zstd -9 -q call, got %+v", runner.calls)
	}

	runner.calls = nil
	filesNoSlashCfg := config.FilesConfig{
		NoWildcardsMatchSlash: true,
		Excludes:              []string{"./tuwunel/data/*.sst"},
	}
	if _, err := engine.ArchiveStackFiles(ctx, snapMount, "/home/site1", &filesNoSlashCfg, 9, filesOut); err != nil {
		t.Fatalf("ArchiveStackFiles with no-wildcards-match-slash error: %v", err)
	}
	tarCall2, ok2 := findRecordedCall(runner.calls, "tar")
	if !ok2 || !slices.Contains(tarCall2.args, "--no-wildcards-match-slash") {
		t.Fatalf("expected --no-wildcards-match-slash in tar call, got %+v", runner.calls)
	}
}

func TestArchiveHostFiles(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := t.TempDir()
	hostFile := filepath.Join(root, "cron-job")
	if err := os.WriteFile(hostFile, []byte("0 2 * * * root true\n"), 0o600); err != nil {
		t.Fatalf("write host file: %v", err)
	}

	runner := &fakeArchiveRunner{}
	engine := NewEngine(runner)

	hostOut := filepath.Join(root, "stage", StackHostFilesArchiveName("site1", "2026-11-16_12-00-00"))
	hostRecords, hostBytes, hostErr := engine.ArchiveHostFiles(ctx, []config.HostFileConfig{
		{Path: hostFile, Owner: "root:root", FileMode: "0400"},
		{Path: filepath.Join(root, "missing-optional"), Optional: true},
	}, 6, hostOut)
	if hostErr != nil {
		t.Fatalf("ArchiveHostFiles error: %v", hostErr)
	}
	if hostBytes != int64(len("raw-tar-payload")) {
		t.Fatalf("ArchiveHostFiles bytes = %d, want %d", hostBytes, len("raw-tar-payload"))
	}
	if len(hostRecords) != 2 || !hostRecords[0].Present || hostRecords[1].Present {
		t.Fatalf("unexpected host records: %+v", hostRecords)
	}
	hostTarCall, hostTarOK := findRecordedCall(runner.calls, "tar")
	if !hostTarOK || !slices.Contains(hostTarCall.args, "--") {
		t.Fatalf("expected host tar call with -- delimiter, got %+v", runner.calls)
	}
}

func findRecordedCall(calls []recordedTarCall, name string) (recordedTarCall, bool) {
	for _, call := range calls {
		if call.name == name {
			return call, true
		}
	}
	return recordedTarCall{}, false
}

func TestArchiveStackFilesErrors(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	validSnapMount := filepath.Join(root, "valid_snap")
	validSnap := filepath.Join(validSnapMount, "site")
	if err := os.MkdirAll(validSnap, 0o750); err != nil {
		t.Fatalf("mkdir valid snap: %v", err)
	}

	fileAsSnap := filepath.Join(root, "file_snap")
	if err := os.WriteFile(fileAsSnap, []byte("not-a-dir"), 0o600); err != nil {
		t.Fatalf("write file snap: %v", err)
	}

	blockingFile := filepath.Join(root, "blocker")
	if err := os.WriteFile(blockingFile, []byte("data"), 0o600); err != nil {
		t.Fatalf("write blocking file: %v", err)
	}

	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()

	tests := []struct {
		runnerErr  error
		ctx        context.Context
		name       string
		snapMount  string
		stackDir   string
		outDir     string
		excludes   []string
		skipWrite  bool
		writeEmpty bool
		wantErr    bool
	}{
		{
			name:    "canceled_stack_context",
			ctx:     canceledCtx,
			outDir:  filepath.Join(root, "out1"),
			wantErr: true,
		},
		{
			name:      "missing_snap_dir",
			ctx:       context.Background(),
			snapMount: filepath.Join(root, "missing_snap"),
			outDir:    filepath.Join(root, "out2"),
			wantErr:   true,
		},
		{
			name:      "snap_source_is_file",
			ctx:       context.Background(),
			snapMount: root,
			stackDir:  "/home/file_snap",
			outDir:    filepath.Join(root, "out3"),
			wantErr:   true,
		},
		{
			name:    "mkdir_outpath_failure",
			ctx:     context.Background(),
			outDir:  blockingFile,
			wantErr: true,
		},
		{
			name:     "empty_excludes_and_non_home_stack",
			ctx:      context.Background(),
			stackDir: "/srv/site",
			outDir:   filepath.Join(root, "out4"),
			excludes: []string{"", "   ", "*.bak"},
			wantErr:  false,
		},
		{
			name:      "runner_failure",
			ctx:       context.Background(),
			outDir:    filepath.Join(root, "out5"),
			runnerErr: errors.New("simulated tar run error"),
			wantErr:   true,
		},
		{
			name:      "missing_output_file",
			ctx:       context.Background(),
			outDir:    filepath.Join(root, "out6"),
			skipWrite: true,
			wantErr:   true,
		},
		{
			name:       "empty_output_file",
			ctx:        context.Background(),
			outDir:     filepath.Join(root, "out7"),
			writeEmpty: true,
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			runner := &fakeArchiveRunner{
				runErr:     tt.runnerErr,
				skipWrite:  tt.skipWrite,
				writeEmpty: tt.writeEmpty,
			}
			engine := NewEngine(runner)
			cfg := &config.FilesConfig{
				Excludes: tt.excludes,
			}

			snapMount := tt.snapMount
			if snapMount == "" {
				snapMount = validSnapMount
			}
			stackDir := tt.stackDir
			if stackDir == "" {
				stackDir = "/home/site"
			}

			outPath := filepath.Join(tt.outDir, "archive.tar.zst")
			_, err := engine.ArchiveStackFiles(tt.ctx, snapMount, stackDir, cfg, 3, outPath)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ArchiveStackFiles error = %v, wantErr = %v", err, tt.wantErr)
			}
		})
	}
}

func TestArchiveHostFilesErrors(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	validFile := filepath.Join(root, "existing.txt")
	if err := os.WriteFile(validFile, []byte("host-data"), 0o600); err != nil {
		t.Fatalf("write valid host file: %v", err)
	}

	blockingFile := filepath.Join(root, "dir_blocker")
	if err := os.WriteFile(blockingFile, []byte("data"), 0o600); err != nil {
		t.Fatalf("write blocking file: %v", err)
	}

	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()

	tests := []struct {
		runnerErr  error
		ctx        context.Context
		name       string
		outDir     string
		items      []config.HostFileConfig
		emptyItems bool
		writeEmpty bool
		wantErr    bool
	}{
		{
			name:    "canceled_host_context",
			ctx:     canceledCtx,
			outDir:  filepath.Join(root, "host_out1"),
			wantErr: true,
		},
		{
			name:       "empty_items_list",
			ctx:        context.Background(),
			emptyItems: true,
			outDir:     filepath.Join(root, "host_out2"),
			wantErr:    false,
		},
		{
			name:    "missing_required_file",
			ctx:     context.Background(),
			items:   []config.HostFileConfig{{Path: filepath.Join(root, "missing_req.txt"), Optional: false}},
			outDir:  filepath.Join(root, "host_out3"),
			wantErr: true,
		},
		{
			name:    "all_optional_files_missing",
			ctx:     context.Background(),
			items:   []config.HostFileConfig{{Path: filepath.Join(root, "missing_opt.txt"), Optional: true}},
			outDir:  filepath.Join(root, "host_out4"),
			wantErr: false,
		},
		{
			name:    "mkdir_outpath_failure",
			ctx:     context.Background(),
			outDir:  blockingFile,
			wantErr: true,
		},
		{
			name:      "runner_failure",
			ctx:       context.Background(),
			outDir:    filepath.Join(root, "host_out5"),
			runnerErr: errors.New("simulated tar run error"),
			wantErr:   true,
		},
		{
			name:       "empty_output_file",
			ctx:        context.Background(),
			outDir:     filepath.Join(root, "host_out6"),
			writeEmpty: true,
			wantErr:    true,
		},
		{
			name:    "unsafe_leading_dash_host_path",
			ctx:     context.Background(),
			items:   []config.HostFileConfig{{Path: "/-checkpoint-action=exec=sh"}},
			outDir:  filepath.Join(root, "host_out7"),
			wantErr: true,
		},
		{
			name:    "relative_host_path_rejected",
			ctx:     context.Background(),
			items:   []config.HostFileConfig{{Path: "etc/passwd"}},
			outDir:  filepath.Join(root, "host_out8"),
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			runner := &fakeArchiveRunner{
				runErr:     tt.runnerErr,
				writeEmpty: tt.writeEmpty,
			}
			engine := NewEngine(runner)
			outPath := filepath.Join(tt.outDir, "host.tar.zst")

			items := tt.items
			if !tt.emptyItems && len(items) == 0 {
				items = []config.HostFileConfig{{Path: validFile}}
			}

			records, bytesCount, err := engine.ArchiveHostFiles(tt.ctx, items, 3, outPath)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ArchiveHostFiles error = %v, wantErr = %v", err, tt.wantErr)
			}
			if !tt.wantErr && tt.emptyItems && (records != nil || bytesCount != 0) {
				t.Fatalf("expected nil records and zero bytes for empty items, got %+v, %d", records, bytesCount)
			}
		})
	}
}

func TestStreamTarToZstdFileErrors(t *testing.T) {
	root := t.TempDir()
	outPath := filepath.Join(root, "stream.tar.zst")

	origPipe := archivePipe
	origCreate := archiveCreateFile
	origClose := archiveCloseFile
	t.Cleanup(func() {
		archivePipe = origPipe
		archiveCreateFile = origCreate
		archiveCloseFile = origClose
	})

	cases := []struct {
		setup   func()
		zstdErr error
		name    string
	}{
		{
			name:    "zstd_error",
			zstdErr: errors.New("zstd failed"),
		},
		{
			name: "pipe_error",
			setup: func() {
				archivePipe = func() (*os.File, *os.File, error) {
					return nil, nil, errors.New("pipe failed")
				}
			},
		},
		{
			name: "create_file_error",
			setup: func() {
				archiveCreateFile = func(string) (*os.File, error) {
					return nil, errors.New("create failed")
				}
			},
		},
		{
			name: "close_output_error",
			setup: func() {
				archiveCloseFile = func(f *os.File) error {
					if err := f.Close(); err != nil {
						return err
					}
					if f.Name() == outPath {
						return errors.New("close output failed")
					}
					return nil
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			archivePipe = origPipe
			archiveCreateFile = origCreate
			archiveCloseFile = origClose
			if tc.setup != nil {
				tc.setup()
			}
			engine := NewEngine(&fakeArchiveRunner{zstdErr: tc.zstdErr})
			if _, err := engine.streamTarToZstdFile(context.Background(), []string{"-cpf", "-"}, 3, outPath); err == nil {
				t.Fatalf("expected streamTarToZstdFile error for %s", tc.name)
			}
		})
	}

	if _, err := defaultArchiveCreateFile(t.TempDir()); err == nil {
		t.Fatal("expected error creating archive at directory path")
	}
	closedTmp, openErr := os.CreateTemp(t.TempDir(), "already-closed")
	if openErr != nil {
		t.Fatalf("create temp: %v", openErr)
	}
	if closeErr := closedTmp.Close(); closeErr != nil {
		t.Fatalf("close temp: %v", closeErr)
	}
	if err := defaultArchiveCloseFile(closedTmp); err == nil {
		t.Fatal("expected error closing already closed archive file")
	}
}
