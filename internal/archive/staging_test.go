package archive

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/webstudiobond/backup-recovery-orchestrator/internal/config"
)

func TestStageAngieAssets(t *testing.T) {
	t.Parallel()

	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()

	root := t.TempDir()
	domainsDir := filepath.Join(root, "angie_domains")
	if err := os.MkdirAll(domainsDir, 0o750); err != nil {
		t.Fatalf("mkdir domains: %v", err)
	}
	for _, conf := range []string{"app.conf", "app-fallback.conf", "app-main.conf"} {
		if err := os.WriteFile(filepath.Join(domainsDir, conf), []byte("server {}"), 0o600); err != nil {
			t.Fatalf("write %s: %v", conf, err)
		}
	}

	streamDir := filepath.Join(root, "angie_stream")
	if err := os.MkdirAll(streamDir, 0o750); err != nil {
		t.Fatalf("mkdir stream: %v", err)
	}
	if err := os.WriteFile(filepath.Join(streamDir, "stream.inc"), []byte("server {}"), 0o600); err != nil {
		t.Fatalf("write stream.inc: %v", err)
	}

	certsDir := filepath.Join(root, "angie_certs", "site1")
	if err := os.MkdirAll(certsDir, 0o750); err != nil {
		t.Fatalf("mkdir certs: %v", err)
	}
	if err := os.WriteFile(filepath.Join(certsDir, "cert.pem"), []byte("CERT"), 0o600); err != nil {
		t.Fatalf("write cert: %v", err)
	}

	validGlobal := &config.GlobalAngieConfig{
		DomainsDir: domainsDir,
		StreamDir:  streamDir,
		CertsDir:   filepath.Dir(certsDir),
	}

	tests := []struct {
		ctx         context.Context
		globalAngie *config.GlobalAngieConfig
		stackAngie  *config.StackAngieConfig
		name        string
		stagingDir  string
		wantErr     bool
	}{
		{
			ctx:         canceledCtx,
			globalAngie: validGlobal,
			stackAngie:  &config.StackAngieConfig{DomainConfs: []string{"app.conf"}},
			name:        "canceled_context_returns_error",
			stagingDir:  filepath.Join(root, "stg1"),
			wantErr:     true,
		},
		{
			ctx:         context.Background(),
			globalAngie: validGlobal,
			stackAngie:  &config.StackAngieConfig{},
			name:        "no_assets_configured_returns_nil",
			stagingDir:  filepath.Join(root, "stg2"),
			wantErr:     false,
		},
		{
			ctx: context.Background(),
			globalAngie: &config.GlobalAngieConfig{
				DomainsDir: "",
			},
			stackAngie: &config.StackAngieConfig{
				DomainConfs: []string{"app-fallback.conf"},
			},
			name:       "missing_domains_dir_returns_error",
			stagingDir: filepath.Join(root, "stg3"),
			wantErr:    true,
		},
		{
			ctx: context.Background(),
			globalAngie: &config.GlobalAngieConfig{
				DomainsDir: domainsDir,
				StreamDir:  "",
			},
			stackAngie: &config.StackAngieConfig{
				StreamConfs: []string{"stream.inc"},
			},
			name:       "missing_stream_dir_returns_error",
			stagingDir: filepath.Join(root, "stg4"),
			wantErr:    true,
		},
		{
			ctx: context.Background(),
			globalAngie: &config.GlobalAngieConfig{
				DomainsDir: domainsDir,
				StreamDir:  streamDir,
				CertsDir:   "",
			},
			stackAngie: &config.StackAngieConfig{
				ACMENames: []string{"site1"},
			},
			name:       "missing_certs_dir_returns_error",
			stagingDir: filepath.Join(root, "stg5"),
			wantErr:    true,
		},
		{
			ctx:         context.Background(),
			globalAngie: validGlobal,
			stackAngie: &config.StackAngieConfig{
				DomainConfs: []string{"app-main.conf"},
				StreamConfs: []string{"stream.inc"},
				ACMENames:   []string{"site1"},
			},
			name:       "all_assets_staged_successfully",
			stagingDir: filepath.Join(root, "stg6"),
			wantErr:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := StageAngieAssets(tt.ctx, tt.globalAngie, tt.stackAngie, tt.stagingDir)
			if (err != nil) != tt.wantErr {
				t.Fatalf("StageAngieAssets error = %v, wantErr = %v", err, tt.wantErr)
			}
		})
	}
}

func TestStageAngieConfFiles(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	srcDir := filepath.Join(root, "confs_src")
	if err := os.MkdirAll(srcDir, 0o750); err != nil {
		t.Fatalf("mkdir confs_src: %v", err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "valid.conf"), []byte("content"), 0o600); err != nil {
		t.Fatalf("write valid.conf: %v", err)
	}

	blockerFile := filepath.Join(root, "file_blocker")
	if err := os.WriteFile(blockerFile, []byte("data"), 0o600); err != nil {
		t.Fatalf("write blocker: %v", err)
	}

	tests := []struct {
		name       string
		srcDir     string
		dstDir     string
		confNames  []string
		emptyNames bool
		wantErr    bool
	}{
		{
			name:       "empty_conf_names_returns_nil",
			srcDir:     srcDir,
			dstDir:     filepath.Join(root, "dst1"),
			emptyNames: true,
			wantErr:    false,
		},
		{
			name:    "empty_src_dir_returns_error",
			srcDir:  "   ",
			dstDir:  filepath.Join(root, "dst2"),
			wantErr: true,
		},
		{
			name:    "mkdir_dst_dir_failure",
			srcDir:  srcDir,
			dstDir:  filepath.Join(blockerFile, "child"),
			wantErr: true,
		},
		{
			name:      "missing_conf_file_in_src_returns_error",
			srcDir:    srcDir,
			dstDir:    filepath.Join(root, "dst3"),
			confNames: []string{"nonexistent.conf"},
			wantErr:   true,
		},
		{
			name:    "successful_conf_copy",
			srcDir:  srcDir,
			dstDir:  filepath.Join(root, "dst4"),
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			confNames := tt.confNames
			if !tt.emptyNames && len(confNames) == 0 {
				confNames = []string{"valid.conf"}
			}
			err := stageAngieConfFiles(tt.srcDir, confNames, tt.dstDir, "domains_dir", "domain")
			if (err != nil) != tt.wantErr {
				t.Fatalf("stageAngieConfFiles error = %v, wantErr = %v", err, tt.wantErr)
			}
		})
	}
}

func TestStageAngieCerts(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	certsBase := filepath.Join(root, "certs_root")
	certDir := filepath.Join(certsBase, "svc-web")
	if err := os.MkdirAll(certDir, 0o750); err != nil {
		t.Fatalf("mkdir certDir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(certDir, "cert.crt"), []byte("CERT"), 0o600); err != nil {
		t.Fatalf("write cert.crt: %v", err)
	}

	symlinkCertDir := filepath.Join(certsBase, "svc-symlink")
	if err := os.MkdirAll(symlinkCertDir, 0o750); err != nil {
		t.Fatalf("mkdir symlinkCertDir: %v", err)
	}
	if err := os.Symlink(filepath.Join(certDir, "cert.crt"), filepath.Join(symlinkCertDir, "link.crt")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	fileAsCertDir := filepath.Join(certsBase, "file_as_cert")
	if err := os.WriteFile(fileAsCertDir, []byte("plain file"), 0o600); err != nil {
		t.Fatalf("write fileAsCertDir: %v", err)
	}

	tests := []struct {
		name       string
		certsDir   string
		dstBaseDir string
		acmeNames  []string
		wantErr    bool
	}{
		{
			name:       "empty_acme_names_returns_nil",
			certsDir:   certsBase,
			dstBaseDir: filepath.Join(root, "out_certs1"),
			acmeNames:  nil,
			wantErr:    false,
		},
		{
			name:       "empty_certs_dir_returns_error",
			certsDir:   "",
			dstBaseDir: filepath.Join(root, "out_certs2"),
			acmeNames:  []string{"svc-web"},
			wantErr:    true,
		},
		{
			name:       "nonexistent_cert_dir_returns_error",
			certsDir:   certsBase,
			dstBaseDir: filepath.Join(root, "out_certs3"),
			acmeNames:  []string{"nonexistent-svc"},
			wantErr:    true,
		},
		{
			name:       "cert_dir_is_regular_file_returns_error",
			certsDir:   certsBase,
			dstBaseDir: filepath.Join(root, "out_certs4"),
			acmeNames:  []string{"file_as_cert"},
			wantErr:    true,
		},
		{
			name:       "symlink_in_cert_dir_triggers_copy_error",
			certsDir:   certsBase,
			dstBaseDir: filepath.Join(root, "out_certs5"),
			acmeNames:  []string{"svc-symlink"},
			wantErr:    true,
		},
		{
			name:       "successful_cert_staging",
			certsDir:   certsBase,
			dstBaseDir: filepath.Join(root, "out_certs6"),
			acmeNames:  []string{"svc-web"},
			wantErr:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := stageAngieCerts(tt.certsDir, tt.acmeNames, tt.dstBaseDir)
			if (err != nil) != tt.wantErr {
				t.Fatalf("stageAngieCerts error = %v, wantErr = %v", err, tt.wantErr)
			}
		})
	}
}

func TestCopyDirTree(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	sourceDir := filepath.Join(root, "tree_source")
	subDir := filepath.Join(sourceDir, "sub_folder")
	if err := os.MkdirAll(subDir, 0o750); err != nil {
		t.Fatalf("mkdir subDir: %v", err)
	}
	sampleFile := filepath.Join(subDir, "payload.txt")
	if err := os.WriteFile(sampleFile, []byte("tree data"), 0o600); err != nil {
		t.Fatalf("write payload.txt: %v", err)
	}

	symlinkDir := filepath.Join(root, "symlink_source")
	if err := os.MkdirAll(symlinkDir, 0o750); err != nil {
		t.Fatalf("mkdir symlinkDir: %v", err)
	}
	if err := os.Symlink(sampleFile, filepath.Join(symlinkDir, "link.txt")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	conflictDst := filepath.Join(root, "conflict_dst")
	if err := os.MkdirAll(conflictDst, 0o750); err != nil {
		t.Fatalf("mkdir conflictDst: %v", err)
	}
	if err := os.WriteFile(filepath.Join(conflictDst, "sub_folder"), []byte("file conflict"), 0o600); err != nil {
		t.Fatalf("write file conflict: %v", err)
	}

	tests := []struct {
		name    string
		srcDir  string
		dstDir  string
		wantErr bool
	}{
		{
			name:    "nonexistent_source_returns_error",
			srcDir:  filepath.Join(root, "nonexistent_tree"),
			dstDir:  filepath.Join(root, "dst_tree1"),
			wantErr: true,
		},
		{
			name:    "symlink_in_source_returns_error",
			srcDir:  symlinkDir,
			dstDir:  filepath.Join(root, "dst_tree2"),
			wantErr: true,
		},
		{
			name:    "mkdir_failure_when_dst_has_conflicting_file",
			srcDir:  sourceDir,
			dstDir:  conflictDst,
			wantErr: true,
		},
		{
			name:    "successful_copy_dir_tree",
			srcDir:  sourceDir,
			dstDir:  filepath.Join(root, "dst_tree3"),
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := copyDirTree(tt.srcDir, tt.dstDir)
			if (err != nil) != tt.wantErr {
				t.Fatalf("copyDirTree error = %v, wantErr = %v", err, tt.wantErr)
			}
		})
	}
}

func TestCopyRegularFile(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	validSrc := filepath.Join(root, "valid.src")
	if err := os.WriteFile(validSrc, []byte("file content"), 0o600); err != nil {
		t.Fatalf("write valid.src: %v", err)
	}

	symlinkSrc := filepath.Join(root, "symlink.src")
	if err := os.Symlink(validSrc, symlinkSrc); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	dirSrc := filepath.Join(root, "dir.src")
	if err := os.MkdirAll(dirSrc, 0o750); err != nil {
		t.Fatalf("mkdir dirSrc: %v", err)
	}

	unreadableSrc := filepath.Join(root, "unreadable.src")
	if err := os.WriteFile(unreadableSrc, []byte("secret"), 0o000); err != nil {
		t.Fatalf("write unreadable.src: %v", err)
	}
	t.Cleanup(func() {
		if cErr := os.Chmod(unreadableSrc, 0o600); cErr != nil {
			t.Fatalf("chmod unreadableSrc: %v", cErr)
		}
	})

	blockerFile := filepath.Join(root, "dst_blocker")
	if err := os.WriteFile(blockerFile, []byte("blocker"), 0o600); err != nil {
		t.Fatalf("write dst_blocker: %v", err)
	}

	existingDstDir := filepath.Join(root, "existing_dst_dir")
	if err := os.MkdirAll(existingDstDir, 0o750); err != nil {
		t.Fatalf("mkdir existingDstDir: %v", err)
	}

	tests := []struct {
		name    string
		src     string
		dst     string
		wantErr bool
	}{
		{
			name:    "missing_source_file_returns_error",
			src:     filepath.Join(root, "missing.src"),
			dst:     filepath.Join(root, "dst_f1"),
			wantErr: true,
		},
		{
			name:    "symlink_source_rejected",
			src:     symlinkSrc,
			dst:     filepath.Join(root, "dst_f2"),
			wantErr: true,
		},
		{
			name:    "directory_source_rejected",
			src:     dirSrc,
			dst:     filepath.Join(root, "dst_f3"),
			wantErr: true,
		},
		{
			name:    "mkdir_parent_dst_failure",
			src:     validSrc,
			dst:     filepath.Join(blockerFile, "child"),
			wantErr: true,
		},
		{
			name:    "unreadable_source_file_open_error",
			src:     unreadableSrc,
			dst:     filepath.Join(root, "dst_f4"),
			wantErr: true,
		},
		{
			name:    "create_dst_error_when_dst_is_directory",
			src:     validSrc,
			dst:     existingDstDir,
			wantErr: true,
		},
		{
			name:    "successful_copy_regular_file",
			src:     validSrc,
			dst:     filepath.Join(root, "dst_f5", "copied.txt"),
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := copyRegularFile(tt.src, tt.dst)
			if (err != nil) != tt.wantErr {
				t.Fatalf("copyRegularFile error = %v, wantErr = %v", err, tt.wantErr)
			}
		})
	}
}

func TestCopyDirTreeMockedErrors(t *testing.T) {
	root := t.TempDir()
	validSrc := filepath.Join(root, "valid_tree")
	if err := os.MkdirAll(validSrc, 0o750); err != nil {
		t.Fatalf("mkdir valid_tree: %v", err)
	}

	origWalk := walkDir
	t.Cleanup(func() {
		walkDir = origWalk
	})

	tests := []struct {
		setupMock func()
		name      string
		wantErr   bool
	}{
		{
			setupMock: func() {
				walkDir = func(_ string, fn fs.WalkDirFunc) error {
					return fn("relative/path", fakeDirEntry{name: "relative"}, nil)
				}
			},
			name:    "relative_path_error",
			wantErr: true,
		},
		{
			setupMock: func() {
				walkDir = func(_ string, fn fs.WalkDirFunc) error {
					return fn(filepath.Join(validSrc, "sub"), fakeDirEntry{
						name:    "sub",
						isDir:   true,
						infoErr: errors.New("simulated dir info error"),
					}, nil)
				}
			},
			name:    "stat_dir_info_error",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			walkDir = origWalk
			tt.setupMock()
			err := copyDirTree(validSrc, filepath.Join(root, "dst_tree"))
			if (err != nil) != tt.wantErr {
				t.Fatalf("copyDirTree error = %v, wantErr = %v", err, tt.wantErr)
			}
		})
	}
}

func TestCopyRegularFileErrors(t *testing.T) {
	root := t.TempDir()
	validSrc := filepath.Join(root, "valid.src")
	if err := os.WriteFile(validSrc, []byte("file content"), 0o600); err != nil {
		t.Fatalf("write valid.src: %v", err)
	}

	origIOCopy := ioCopy
	t.Cleanup(func() {
		ioCopy = origIOCopy
	})

	ioCopy = func(io.Writer, io.Reader) (int64, error) {
		return 0, errors.New("simulated io.Copy error")
	}

	err := copyRegularFile(validSrc, filepath.Join(root, "failed_copy.txt"))
	if err == nil {
		t.Fatal("expected error on failed ioCopy")
	}
}
