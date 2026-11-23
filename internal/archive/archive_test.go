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

func TestArchiveStackFilesAndHostFiles(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := t.TempDir()
	snapMount := filepath.Join(root, "mnt_snap")
	if err := os.MkdirAll(filepath.Join(snapMount, "site1"), 0o750); err != nil {
		t.Fatalf("mkdir snap source: %v", err)
	}

	hostFile := filepath.Join(root, "cron-job")
	if err := os.WriteFile(hostFile, []byte("0 2 * * * root true\n"), 0o600); err != nil {
		t.Fatalf("write host file: %v", err)
	}

	runner := &fakeArchiveRunner{}
	engine := NewEngine(runner)

	filesOut := filepath.Join(root, "stage", StackFilesArchiveName("site1", "2026-11-16_12-00-00"))
	filesCfg := config.FilesConfig{
		ExcludesFile: "/etc/bro/rules/app-excludes.conf",
		Excludes:     []string{"*.tmp", "cache/*"},
	}
	if err := engine.ArchiveStackFiles(ctx, snapMount, "/home/site1", &filesCfg, 9, filesOut); err != nil {
		t.Fatalf("ArchiveStackFiles error: %v", err)
	}

	if len(runner.calls) != 1 || !slices.Contains(runner.calls[0].env, "ZSTD_CLEVEL=9") {
		t.Fatalf("expected tar call with ZSTD_CLEVEL=9, got %+v", runner.calls)
	}
	if !slices.Contains(runner.calls[0].args, "--exclude=cache/*") {
		t.Fatalf("expected exclude arg in tar call, got %+v", runner.calls[0].args)
	}
	if slices.Contains(runner.calls[0].args, "--no-wildcards-match-slash") {
		t.Fatalf("did not expect --no-wildcards-match-slash by default, got %+v", runner.calls[0].args)
	}

	filesNoSlashCfg := config.FilesConfig{
		NoWildcardsMatchSlash: true,
		Excludes:              []string{"./tuwunel/data/*.sst"},
	}
	if err := engine.ArchiveStackFiles(ctx, snapMount, "/home/site1", &filesNoSlashCfg, 9, filesOut); err != nil {
		t.Fatalf("ArchiveStackFiles with no-wildcards-match-slash error: %v", err)
	}
	if !slices.Contains(runner.calls[1].args, "--no-wildcards-match-slash") {
		t.Fatalf("expected --no-wildcards-match-slash in tar call, got %+v", runner.calls[1].args)
	}

	hostOut := filepath.Join(root, "stage", StackHostFilesArchiveName("site1", "2026-11-16_12-00-00"))
	hostRecords, hostErr := engine.ArchiveHostFiles(ctx, []config.HostFileConfig{
		{Path: hostFile, Owner: "root:root", FileMode: "0400"},
		{Path: filepath.Join(root, "missing-optional"), Optional: true},
	}, 6, hostOut)
	if hostErr != nil {
		t.Fatalf("ArchiveHostFiles error: %v", hostErr)
	}
	if len(hostRecords) != 2 || !hostRecords[0].Present || hostRecords[1].Present {
		t.Fatalf("unexpected host records: %+v", hostRecords)
	}
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
			err := engine.ArchiveStackFiles(tt.ctx, snapMount, stackDir, cfg, 3, outPath)
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

			records, err := engine.ArchiveHostFiles(tt.ctx, items, 3, outPath)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ArchiveHostFiles error = %v, wantErr = %v", err, tt.wantErr)
			}
			if !tt.wantErr && tt.emptyItems && records != nil {
				t.Fatalf("expected nil records for empty items, got %+v", records)
			}
		})
	}
}
