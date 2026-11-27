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

func TestStageReverseProxyAssets(t *testing.T) {
	t.Parallel()

	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()

	root := t.TempDir()
	reverseProxyStackDir := filepath.Join(root, "reverse_proxy_stack")
	domainsDir := filepath.Join(reverseProxyStackDir, "data", "domains")
	if err := os.MkdirAll(domainsDir, 0o750); err != nil {
		t.Fatalf("mkdir domains: %v", err)
	}
	for _, conf := range []string{"app.conf", "app-fallback.conf", "app-main.conf", "app-alt.conf"} {
		if err := os.WriteFile(filepath.Join(domainsDir, conf), []byte("server {}"), 0o600); err != nil {
			t.Fatalf("write %s: %v", conf, err)
		}
	}

	streamDir := filepath.Join(reverseProxyStackDir, "data", "stream")
	if err := os.MkdirAll(streamDir, 0o750); err != nil {
		t.Fatalf("mkdir stream: %v", err)
	}
	if err := os.WriteFile(filepath.Join(streamDir, "stream.inc"), []byte("server {}"), 0o600); err != nil {
		t.Fatalf("write stream.inc: %v", err)
	}

	certsDir := filepath.Join(reverseProxyStackDir, "data", "certs", "site1")
	if err := os.MkdirAll(certsDir, 0o750); err != nil {
		t.Fatalf("mkdir certs: %v", err)
	}
	if err := os.WriteFile(filepath.Join(certsDir, "cert.pem"), []byte("CERT"), 0o600); err != nil {
		t.Fatalf("write cert: %v", err)
	}

	makeReverseProxyStack := func(name, stackDir, domainsRel, streamRel, certsRel string) *config.StackConfig {
		return &config.StackConfig{
			Name:     name,
			StackDir: stackDir,
			ReverseProxyProvider: &config.ReverseProxyProviderConfig{
				Type:        "angie",
				ServiceName: "reverse-proxy-" + name,
				DomainsDir:  domainsRel,
				StreamDir:   streamRel,
				CertsDir:    certsRel,
			},
		}
	}
	validStack := makeReverseProxyStack("host", reverseProxyStackDir, "data/domains", "data/stream", "data/certs")

	tests := []struct {
		ctx               context.Context
		reverseProxyStack *config.StackConfig
		stackReverseProxy *config.StackReverseProxyConfig
		name              string
		stagingDir        string
		wantErr           bool
	}{
		{
			ctx:               canceledCtx,
			reverseProxyStack: validStack,
			stackReverseProxy: &config.StackReverseProxyConfig{DomainConfs: []string{"app.conf"}},
			name:              "canceled_context_returns_error",
			stagingDir:        filepath.Join(root, "stg1"),
			wantErr:           true,
		},
		{
			ctx:               context.Background(),
			reverseProxyStack: validStack,
			stackReverseProxy: &config.StackReverseProxyConfig{},
			name:              "no_assets_configured_returns_nil",
			stagingDir:        filepath.Join(root, "stg2"),
			wantErr:           false,
		},
		{
			ctx:               context.Background(),
			reverseProxyStack: nil,
			stackReverseProxy: &config.StackReverseProxyConfig{DomainConfs: []string{"app-fallback.conf"}},
			name:              "missing_reverse_proxy_stack_returns_error",
			stagingDir:        filepath.Join(root, "stg3"),
			wantErr:           true,
		},
		{
			ctx:               context.Background(),
			reverseProxyStack: makeReverseProxyStack("missing-dir", filepath.Join(root, "missing_reverse_proxy_stack"), "d", "", "c"),
			stackReverseProxy: &config.StackReverseProxyConfig{DomainConfs: []string{"app-missing.conf"}},
			name:              "missing_reverse_proxy_stack_dir_returns_error",
			stagingDir:        filepath.Join(root, "stg4"),
			wantErr:           true,
		},
		{
			ctx:               context.Background(),
			reverseProxyStack: makeReverseProxyStack("no-domains", reverseProxyStackDir, "", "", "c"),
			stackReverseProxy: &config.StackReverseProxyConfig{DomainConfs: []string{"app-alt.conf"}},
			name:              "missing_domains_dir_returns_error",
			stagingDir:        filepath.Join(root, "stg5"),
			wantErr:           true,
		},
		{
			ctx:               context.Background(),
			reverseProxyStack: makeReverseProxyStack("no-stream", reverseProxyStackDir, "d", "", "c"),
			stackReverseProxy: &config.StackReverseProxyConfig{StreamConfs: []string{"stream.inc"}},
			name:              "missing_stream_dir_returns_error",
			stagingDir:        filepath.Join(root, "stg6"),
			wantErr:           true,
		},
		{
			ctx:               context.Background(),
			reverseProxyStack: makeReverseProxyStack("no-certs", reverseProxyStackDir, "d", "s", ""),
			stackReverseProxy: &config.StackReverseProxyConfig{ACMENames: []string{"site1"}},
			name:              "missing_certs_dir_returns_error",
			stagingDir:        filepath.Join(root, "stg7"),
			wantErr:           true,
		},
		{
			ctx:               context.Background(),
			reverseProxyStack: validStack,
			stackReverseProxy: &config.StackReverseProxyConfig{
				DomainConfs: []string{"app-main.conf"},
				StreamConfs: []string{"stream.inc"},
				ACMENames:   []string{"site1"},
			},
			name:       "all_assets_staged_successfully",
			stagingDir: filepath.Join(root, "stg8"),
			wantErr:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := StageReverseProxyAssets(tt.ctx, tt.reverseProxyStack, tt.stackReverseProxy, tt.stagingDir)
			if (err != nil) != tt.wantErr {
				t.Fatalf("StageReverseProxyAssets error = %v, wantErr = %v", err, tt.wantErr)
			}
		})
	}
}

func TestOpenReverseProxySubRootAndConfFiles(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	reverseProxyDir := filepath.Join(root, "reverse_proxy")
	srcDir := filepath.Join(reverseProxyDir, "confs_src")
	if err := os.MkdirAll(srcDir, 0o750); err != nil {
		t.Fatalf("mkdir confs_src: %v", err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "valid.conf"), []byte("content"), 0o600); err != nil {
		t.Fatalf("write valid.conf: %v", err)
	}

	reverseProxyRoot, err := os.OpenRoot(reverseProxyDir)
	if err != nil {
		t.Fatalf("open root: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := reverseProxyRoot.Close(); closeErr != nil {
			t.Errorf("close reverseProxyRoot: %v", closeErr)
		}
	})

	blockerFile := filepath.Join(root, "file_blocker")
	if err := os.WriteFile(blockerFile, []byte("data"), 0o600); err != nil {
		t.Fatalf("write blocker: %v", err)
	}

	tests := []struct {
		name       string
		srcRelDir  string
		dstDir     string
		confNames  []string
		emptyNames bool
		wantErr    bool
	}{
		{
			name:       "empty_conf_names_returns_nil",
			srcRelDir:  "unused_dir",
			dstDir:     filepath.Join(root, "dst1"),
			emptyNames: true,
			wantErr:    false,
		},
		{
			name:      "empty_src_dir_returns_error",
			srcRelDir: "   ",
			dstDir:    filepath.Join(root, "dst2"),
			wantErr:   true,
		},
		{
			name:      "absolute_src_dir_returns_error",
			srcRelDir: "/etc/proxy/domains",
			dstDir:    filepath.Join(root, "dst_abs"),
			wantErr:   true,
		},
		{
			name:      "dot_src_dir_returns_error",
			srcRelDir: ".",
			dstDir:    filepath.Join(root, "dst_dot"),
			wantErr:   true,
		},
		{
			name:      "missing_rel_dir_returns_error",
			srcRelDir: "no_such_dir",
			dstDir:    filepath.Join(root, "dst_missing_dir"),
			wantErr:   true,
		},
		{
			name:      "mkdir_dst_dir_failure",
			srcRelDir: "",
			dstDir:    filepath.Join(blockerFile, "child"),
			wantErr:   true,
		},
		{
			name:      "missing_conf_file_in_src_returns_error",
			srcRelDir: "",
			dstDir:    filepath.Join(root, "dst3"),
			confNames: []string{"nonexistent.conf"},
			wantErr:   true,
		},
		{
			name:      "successful_conf_copy",
			srcRelDir: "",
			dstDir:    filepath.Join(root, "dst4"),
			wantErr:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			srcRelDir := tt.srcRelDir
			if srcRelDir == "" {
				srcRelDir = "confs_src"
			}
			confNames := tt.confNames
			if !tt.emptyNames && len(confNames) == 0 {
				confNames = []string{"valid.conf"}
			}
			err := stageReverseProxyConfFiles(reverseProxyRoot, srcRelDir, confNames, tt.dstDir, "domains_dir", "domain")
			if (err != nil) != tt.wantErr {
				t.Fatalf("stageReverseProxyConfFiles error = %v, wantErr = %v", err, tt.wantErr)
			}
		})
	}
}

func setupStageReverseProxyCertsDir(t *testing.T, root string) *os.Root {
	t.Helper()

	reverseProxyDir := filepath.Join(root, "reverse_proxy")
	certsBase := filepath.Join(reverseProxyDir, "certs_root")
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

	reverseProxyRoot, err := os.OpenRoot(reverseProxyDir)
	if err != nil {
		t.Fatalf("open reverseProxyRoot: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := reverseProxyRoot.Close(); closeErr != nil {
			t.Errorf("close reverseProxyRoot: %v", closeErr)
		}
	})
	return reverseProxyRoot
}

func TestStageReverseProxyCerts(t *testing.T) {
	root := t.TempDir()
	reverseProxyRoot := setupStageReverseProxyCertsDir(t, root)

	origOpenSub := rootOpenSubRoot
	t.Cleanup(func() {
		rootOpenSubRoot = origOpenSub
	})

	tests := []struct {
		setupMock  func()
		name       string
		certsRel   string
		dstBaseDir string
		acmeNames  []string
		emptyCerts bool
		wantErr    bool
	}{
		{
			name:       "empty_acme_names_returns_nil",
			certsRel:   "unused_certs",
			dstBaseDir: filepath.Join(root, "out_certs1"),
			acmeNames:  nil,
			wantErr:    false,
		},
		{
			name:       "empty_certs_dir_returns_error",
			emptyCerts: true,
			dstBaseDir: filepath.Join(root, "out_certs2"),
			acmeNames:  []string{"svc-web-empty"},
			wantErr:    true,
		},
		{
			name:       "nonexistent_cert_dir_returns_error",
			dstBaseDir: filepath.Join(root, "out_certs3"),
			acmeNames:  []string{"nonexistent-svc"},
			wantErr:    true,
		},
		{
			name:       "cert_dir_is_regular_file_returns_error",
			dstBaseDir: filepath.Join(root, "out_certs4"),
			acmeNames:  []string{"file_as_cert"},
			wantErr:    true,
		},
		{
			name: "open_acme_subroot_failure",
			setupMock: func() {
				calls := 0
				rootOpenSubRoot = func(r *os.Root, name string) (*os.Root, error) {
					calls++
					if calls == 2 {
						return nil, errors.New("simulated open subroot failure")
					}
					return r.OpenRoot(name)
				}
			},
			dstBaseDir: filepath.Join(root, "out_certs_suberr"),
			wantErr:    true,
		},
		{
			name:       "symlink_in_cert_dir_triggers_copy_error",
			dstBaseDir: filepath.Join(root, "out_certs5"),
			acmeNames:  []string{"svc-symlink"},
			wantErr:    true,
		},
		{
			name:       "successful_cert_staging",
			dstBaseDir: filepath.Join(root, "out_certs6"),
			wantErr:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rootOpenSubRoot = origOpenSub
			if tt.setupMock != nil {
				tt.setupMock()
			}
			certsRel := tt.certsRel
			if certsRel == "" && !tt.emptyCerts {
				certsRel = "certs_root"
			}
			acmeNames := tt.acmeNames
			if tt.name != "empty_acme_names_returns_nil" && len(acmeNames) == 0 {
				acmeNames = []string{"svc-web"}
			}
			err := stageReverseProxyCerts(reverseProxyRoot, certsRel, acmeNames, tt.dstBaseDir)
			if (err != nil) != tt.wantErr {
				t.Fatalf("stageReverseProxyCerts error = %v, wantErr = %v", err, tt.wantErr)
			}
		})
	}
}

func TestCopyRootDirTree(t *testing.T) {
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

	openTestRoot := func(dir string) *os.Root {
		r, err := os.OpenRoot(dir)
		if err != nil {
			t.Fatalf("open root %q: %v", dir, err)
		}
		t.Cleanup(func() {
			if cErr := r.Close(); cErr != nil {
				t.Errorf("close root %q: %v", dir, cErr)
			}
		})
		return r
	}

	srcRoot := openTestRoot(sourceDir)
	symlinkRoot := openTestRoot(symlinkDir)

	tests := []struct {
		srcRoot *os.Root
		name    string
		dstDir  string
		wantErr bool
	}{
		{
			srcRoot: symlinkRoot,
			name:    "symlink_in_source_returns_error",
			dstDir:  filepath.Join(root, "dst_tree2"),
			wantErr: true,
		},
		{
			srcRoot: srcRoot,
			name:    "mkdir_failure_when_dst_has_conflicting_file",
			dstDir:  conflictDst,
			wantErr: true,
		},
		{
			srcRoot: srcRoot,
			name:    "successful_copy_dir_tree",
			dstDir:  filepath.Join(root, "dst_tree3"),
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := copyRootDirTree(tt.srcRoot, tt.dstDir)
			if (err != nil) != tt.wantErr {
				t.Fatalf("copyRootDirTree error = %v, wantErr = %v", err, tt.wantErr)
			}
		})
	}
}

func TestCopyRootRegularFile(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	srcRootDir := filepath.Join(root, "src_root")
	if err := os.MkdirAll(srcRootDir, 0o750); err != nil {
		t.Fatalf("mkdir src_root: %v", err)
	}
	if err := os.WriteFile(filepath.Join(srcRootDir, "valid.src"), []byte("file content"), 0o600); err != nil {
		t.Fatalf("write valid file: %v", err)
	}
	if err := os.Symlink("valid.src", filepath.Join(srcRootDir, "symlink.src")); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(srcRootDir, "dir.src"), 0o750); err != nil {
		t.Fatalf("mkdir dirSrc: %v", err)
	}
	unreadableSrc := filepath.Join(srcRootDir, "unreadable.src")
	if err := os.WriteFile(unreadableSrc, []byte("secret"), 0o000); err != nil {
		t.Fatalf("write unreadable.src: %v", err)
	}
	t.Cleanup(func() {
		if cErr := os.Chmod(unreadableSrc, 0o600); cErr != nil {
			t.Errorf("chmod unreadableSrc: %v", cErr)
		}
	})

	srcRoot, openErr := os.OpenRoot(srcRootDir)
	if openErr != nil {
		t.Fatalf("open srcRoot: %v", openErr)
	}
	t.Cleanup(func() {
		if cErr := srcRoot.Close(); cErr != nil {
			t.Errorf("close srcRoot: %v", cErr)
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
		relSrc  string
		dst     string
		wantErr bool
	}{
		{
			name:    "missing_source_file_returns_error",
			relSrc:  "missing.src",
			dst:     filepath.Join(root, "dst_f1"),
			wantErr: true,
		},
		{
			name:    "symlink_source_rejected",
			relSrc:  "symlink.src",
			dst:     filepath.Join(root, "dst_f2"),
			wantErr: true,
		},
		{
			name:    "directory_source_rejected",
			relSrc:  "dir.src",
			dst:     filepath.Join(root, "dst_f3"),
			wantErr: true,
		},
		{
			name:    "mkdir_parent_dst_failure",
			dst:     filepath.Join(blockerFile, "child"),
			wantErr: true,
		},
		{
			name:    "unreadable_source_file_open_error",
			relSrc:  "unreadable.src",
			dst:     filepath.Join(root, "dst_f4"),
			wantErr: true,
		},
		{
			name:    "create_dst_error_when_dst_is_directory",
			dst:     existingDstDir,
			wantErr: true,
		},
		{
			name:    "successful_copy_regular_file",
			dst:     filepath.Join(root, "dst_f5", "copied.txt"),
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			relSrc := tt.relSrc
			if relSrc == "" {
				relSrc = "valid.src"
			}
			err := copyRootRegularFile(srcRoot, relSrc, tt.dst)
			if (err != nil) != tt.wantErr {
				t.Fatalf("copyRootRegularFile error = %v, wantErr = %v", err, tt.wantErr)
			}
		})
	}
}

func TestCopyRootDirTreeMockedErrors(t *testing.T) {
	root := t.TempDir()
	validSrc := filepath.Join(root, "valid_tree")
	if err := os.MkdirAll(validSrc, 0o750); err != nil {
		t.Fatalf("mkdir valid_tree: %v", err)
	}
	srcRoot, openErr := os.OpenRoot(validSrc)
	if openErr != nil {
		t.Fatalf("open srcRoot: %v", openErr)
	}
	t.Cleanup(func() {
		if cErr := srcRoot.Close(); cErr != nil {
			t.Errorf("close srcRoot: %v", cErr)
		}
	})

	origWalk := walkRootFS
	t.Cleanup(func() {
		walkRootFS = origWalk
	})

	tests := []struct {
		setupMock func()
		name      string
		wantErr   bool
	}{
		{
			setupMock: func() {
				walkRootFS = func(_ fs.FS, _ string, fn fs.WalkDirFunc) error {
					return fn("walk_err_entry", fakeDirEntry{name: "walk_err_entry"}, errors.New("simulated walk error"))
				}
			},
			name:    "walk_callback_error",
			wantErr: true,
		},
		{
			setupMock: func() {
				walkRootFS = func(_ fs.FS, _ string, fn fs.WalkDirFunc) error {
					return fn("dir_info_entry", fakeDirEntry{
						name:    "dir_info_entry",
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
			walkRootFS = origWalk
			tt.setupMock()
			err := copyRootDirTree(srcRoot, filepath.Join(root, "dst_tree"))
			if (err != nil) != tt.wantErr {
				t.Fatalf("copyRootDirTree error = %v, wantErr = %v", err, tt.wantErr)
			}
		})
	}
}

func TestCopyRootRegularFileIOCopyError(t *testing.T) {
	root := t.TempDir()
	srcDir := filepath.Join(root, "src")
	if err := os.MkdirAll(srcDir, 0o750); err != nil {
		t.Fatalf("mkdir src: %v", err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "payload.src"), []byte("file content"), 0o600); err != nil {
		t.Fatalf("write payload file: %v", err)
	}
	srcRoot, openErr := os.OpenRoot(srcDir)
	if openErr != nil {
		t.Fatalf("open srcRoot: %v", openErr)
	}
	t.Cleanup(func() {
		if cErr := srcRoot.Close(); cErr != nil {
			t.Errorf("close srcRoot: %v", cErr)
		}
	})

	origIOCopy := ioCopy
	t.Cleanup(func() {
		ioCopy = origIOCopy
	})

	ioCopy = func(io.Writer, io.Reader) (int64, error) {
		return 0, errors.New("simulated io.Copy error")
	}

	err := copyRootRegularFile(srcRoot, "payload.src", filepath.Join(root, "failed_copy.txt"))
	if err == nil {
		t.Fatal("expected error on failed ioCopy")
	}
}

func TestStageReverseProxyRootCloseErrors(t *testing.T) {
	root := t.TempDir()
	reverseProxyDir := filepath.Join(root, "reverse_proxy")
	if err := os.MkdirAll(filepath.Join(reverseProxyDir, "close_domains"), 0o750); err != nil {
		t.Fatalf("mkdir close_domains: %v", err)
	}
	if err := os.WriteFile(filepath.Join(reverseProxyDir, "close_domains", "a.conf"), []byte("server {}"), 0o600); err != nil {
		t.Fatalf("write a.conf: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(reverseProxyDir, "close_certs", "site-a"), 0o750); err != nil {
		t.Fatalf("mkdir close_certs: %v", err)
	}
	if err := os.WriteFile(filepath.Join(reverseProxyDir, "close_certs", "site-a", "fullchain.pem"), []byte("PEM"), 0o600); err != nil {
		t.Fatalf("write pem: %v", err)
	}

	reverseProxyRoot, openErr := os.OpenRoot(reverseProxyDir)
	if openErr != nil {
		t.Fatalf("open reverseProxyRoot: %v", openErr)
	}
	t.Cleanup(func() {
		if cErr := reverseProxyRoot.Close(); cErr != nil {
			t.Errorf("close reverseProxyRoot: %v", cErr)
		}
	})

	origClose := closeReverseProxyRoot
	t.Cleanup(func() { closeReverseProxyRoot = origClose })
	closeReverseProxyRoot = func(r *os.Root) error {
		if cErr := r.Close(); cErr != nil {
			return cErr
		}
		return errors.New("simulated root close failure")
	}

	if err := stageReverseProxyConfFiles(reverseProxyRoot, "close_domains", []string{"a.conf"}, filepath.Join(root, "out_dom"), "domains_dir", "domain"); err == nil {
		t.Fatal("expected close error from stageReverseProxyConfFiles")
	}
	if err := stageReverseProxyCerts(reverseProxyRoot, "close_certs", []string{"site-a"}, filepath.Join(root, "out_crt")); err == nil {
		t.Fatal("expected close error from stageReverseProxyCerts")
	}
}
