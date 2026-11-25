package archive

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/webstudiobond/backup-recovery-orchestrator/internal/config"
)

func TestStageAssembleVerifyAndUnpack(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := t.TempDir()
	stagingDir := filepath.Join(root, "staging")

	reverseProxyStack, stackReverseProxy := prepareReverseProxyFixtures(t, root)

	if err := StageReverseProxyAssets(ctx, &reverseProxyStack, &stackReverseProxy, stagingDir); err != nil {
		t.Fatalf("StageReverseProxyAssets error: %v", err)
	}
	const testStamp = "2026-11-16_12-00-00"
	filesName := StackFilesArchiveName("app-beta", testStamp)
	dbName := StackDBArchiveName("app-beta", "", testStamp, 1)
	if err := os.WriteFile(filepath.Join(stagingDir, filesName), []byte("files-zst"), 0o600); err != nil {
		t.Fatalf("write staged files: %v", err)
	}
	if err := os.WriteFile(filepath.Join(stagingDir, dbName), []byte("db-zst"), 0o600); err != nil {
		t.Fatalf("write staged db: %v", err)
	}

	engine := &Engine{
		Runner: &fakeArchiveRunner{},
		Now:    func() time.Time { return time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC) },
	}
	manifest := Manifest{
		StackName:    "app-beta",
		StackDir:     "/home/app-beta",
		ComposeFile:  "/home/app-beta/docker-compose.yaml",
		FilesArchive: filesName,
		User:         config.UserConfig{Name: "app-beta", UID: 1015, GID: 1015},
		ReverseProxy: stackReverseProxy,
	}

	containerPath := filepath.Join(root, "backups", "app-beta_"+testStamp+".tar")
	if err := engine.AssembleContainer(ctx, &manifest, stagingDir, containerPath); err != nil {
		t.Fatalf("AssembleContainer error: %v", err)
	}

	verified, verifyErr := VerifyContainer(ctx, containerPath)
	if verifyErr != nil {
		t.Fatalf("VerifyContainer error: %v", verifyErr)
	}
	if verified.StackName != manifest.StackName || len(verified.Artifacts) != 5 {
		t.Fatalf("unexpected verified manifest: %+v", verified)
	}

	unpackDir := filepath.Join(root, "unpacked")
	if err := UnpackContainer(ctx, containerPath, unpackDir); err != nil {
		t.Fatalf("UnpackContainer error: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(unpackDir, "certs", "svc-chat", "certificate.pem")); statErr != nil {
		t.Fatalf("expected unpacked cert: %v", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(unpackDir, "stream", "rtc-turn.inc")); statErr != nil {
		t.Fatalf("expected unpacked stream conf: %v", statErr)
	}
}

func TestAssembleContainerErrors(t *testing.T) {
	root := t.TempDir()
	validStaging := filepath.Join(root, "valid_staging")
	if err := os.MkdirAll(validStaging, 0o750); err != nil {
		t.Fatalf("mkdir valid staging: %v", err)
	}
	sampleFile := filepath.Join(validStaging, "sample.txt")
	if err := os.WriteFile(sampleFile, []byte("sample"), 0o600); err != nil {
		t.Fatalf("write sample: %v", err)
	}

	blockingFile := filepath.Join(root, "container_blocker")
	if err := os.WriteFile(blockingFile, []byte("data"), 0o600); err != nil {
		t.Fatalf("write blocker: %v", err)
	}

	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()

	origJSONMarshal := jsonMarshalIndent
	origSHA256 := sha256FileFn
	t.Cleanup(func() {
		jsonMarshalIndent = origJSONMarshal
		sha256FileFn = origSHA256
	})

	baseManifest := &Manifest{
		StackName: "test-stack",
	}

	prefilledManifest := &Manifest{
		SchemaVersion: ManifestSchemaVersion,
		CreatedAt:     "2026-11-22T10:00:00Z",
		StackName:     "prefilled-stack",
	}

	tests := []struct {
		manifest   *Manifest
		ctx        context.Context
		setupMock  func(t *testing.T)
		name       string
		stagingDir string
		targetPath string
		wantErr    bool
	}{
		{
			name:       "canceled_container_context",
			ctx:        canceledCtx,
			manifest:   baseManifest,
			stagingDir: validStaging,
			targetPath: filepath.Join(root, "tar1.tar"),
			wantErr:    true,
		},
		{
			name:       "nil_manifest",
			ctx:        context.Background(),
			manifest:   nil,
			stagingDir: validStaging,
			targetPath: filepath.Join(root, "tar2.tar"),
			wantErr:    true,
		},
		{
			name:       "nil_engine_now_and_prefilled_manifest",
			ctx:        context.Background(),
			manifest:   prefilledManifest,
			stagingDir: validStaging,
			targetPath: filepath.Join(root, "tar3.tar"),
			wantErr:    false,
		},
		{
			name:       "collect_staged_files_error",
			ctx:        context.Background(),
			manifest:   baseManifest,
			stagingDir: filepath.Join(root, "nonexistent_staging"),
			targetPath: filepath.Join(root, "tar4.tar"),
			wantErr:    true,
		},
		{
			name:       "sha256_hash_error",
			ctx:        context.Background(),
			manifest:   baseManifest,
			stagingDir: validStaging,
			targetPath: filepath.Join(root, "tar5.tar"),
			setupMock: func(t *testing.T) {
				t.Helper()
				sha256FileFn = func(string) (string, error) {
					return "", errors.New("simulated sha256 error")
				}
			},
			wantErr: true,
		},
		{
			name:       "json_marshal_error",
			ctx:        context.Background(),
			manifest:   baseManifest,
			stagingDir: validStaging,
			targetPath: filepath.Join(root, "tar6.tar"),
			setupMock: func(t *testing.T) {
				t.Helper()
				jsonMarshalIndent = func(any, string, string) ([]byte, error) {
					return nil, errors.New("simulated marshal error")
				}
			},
			wantErr: true,
		},
		{
			name:       "target_dir_mkdir_error",
			ctx:        context.Background(),
			manifest:   baseManifest,
			stagingDir: validStaging,
			targetPath: filepath.Join(blockingFile, "tar7.tar"),
			wantErr:    true,
		},
		{
			name:       "write_tar_error",
			ctx:        context.Background(),
			manifest:   baseManifest,
			stagingDir: validStaging,
			targetPath: "/dev/null/impossible/tar8.tar",
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			jsonMarshalIndent = origJSONMarshal
			sha256FileFn = origSHA256
			if tt.setupMock != nil {
				tt.setupMock(t)
			}

			engine := &Engine{}
			err := engine.AssembleContainer(tt.ctx, tt.manifest, tt.stagingDir, tt.targetPath)
			if (err != nil) != tt.wantErr {
				t.Fatalf("AssembleContainer error = %v, wantErr = %v", err, tt.wantErr)
			}
		})
	}
}

func TestCollectStagedFiles(t *testing.T) {
	root := t.TempDir()

	validDir := filepath.Join(root, "stage_valid")
	if err := os.MkdirAll(filepath.Join(validDir, "sub"), 0o750); err != nil {
		t.Fatalf("mkdir validDir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(validDir, "manifest.json"), []byte("{}"), 0o600); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	if err := os.WriteFile(filepath.Join(validDir, "sub", "app.tar.zst"), []byte("payload"), 0o600); err != nil {
		t.Fatalf("write app.tar.zst: %v", err)
	}

	symlinkDir := filepath.Join(root, "stage_symlink")
	if err := os.MkdirAll(symlinkDir, 0o750); err != nil {
		t.Fatalf("mkdir symlinkDir: %v", err)
	}
	targetFile := filepath.Join(root, "symlink_target.txt")
	if err := os.WriteFile(targetFile, []byte("target"), 0o600); err != nil {
		t.Fatalf("write target: %v", err)
	}
	if err := os.Symlink(targetFile, filepath.Join(symlinkDir, "link.txt")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	origWalk := walkDir
	t.Cleanup(func() {
		walkDir = origWalk
	})

	tests := []struct {
		setupMock func(t *testing.T)
		name      string
		stagePath string
		wantCount int
		wantErr   bool
	}{
		{
			name:      "skips_manifest_json_and_includes_sub_files",
			stagePath: validDir,
			wantCount: 1,
			wantErr:   false,
		},
		{
			name:      "rejects_symlink_entry",
			stagePath: symlinkDir,
			wantErr:   true,
		},
		{
			name:      "walk_directory_initial_error",
			stagePath: filepath.Join(root, "missing_stage"),
			wantErr:   true,
		},
		{
			name:      "stat_entry_info_error",
			stagePath: validDir,
			setupMock: func(t *testing.T) {
				t.Helper()
				walkDir = func(_ string, fn fs.WalkDirFunc) error {
					return fn(filepath.Join(validDir, "bad.txt"), fakeDirEntry{
						name:    "bad.txt",
						infoErr: errors.New("simulated info error"),
					}, nil)
				}
			},
			wantErr: true,
		},
		{
			name:      "compute_relative_path_error",
			stagePath: validDir,
			setupMock: func(t *testing.T) {
				t.Helper()
				walkDir = func(_ string, fn fs.WalkDirFunc) error {
					return fn("relative/path", fakeDirEntry{name: "relative"}, nil)
				}
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			walkDir = origWalk
			if tt.setupMock != nil {
				tt.setupMock(t)
			}

			files, err := collectStagedFiles(tt.stagePath)
			if (err != nil) != tt.wantErr {
				t.Fatalf("collectStagedFiles error = %v, wantErr = %v", err, tt.wantErr)
			}
			if !tt.wantErr && len(files) != tt.wantCount {
				t.Fatalf("collectStagedFiles count = %d, want %d", len(files), tt.wantCount)
			}
		})
	}
}

func TestWriteUncompressedTarErrors(t *testing.T) {
	root := t.TempDir()
	validData := filepath.Join(root, "valid.txt")
	if err := os.WriteFile(validData, []byte("data"), 0o600); err != nil {
		t.Fatalf("write valid data: %v", err)
	}

	origRemove := osRemove
	t.Cleanup(func() {
		osRemove = origRemove
	})

	tests := []struct {
		setupMock  func(t *testing.T)
		name       string
		targetPath string
		files      []stagedFile
		wantErr    bool
	}{
		{
			name:       "open_file_failure",
			targetPath: "/dev/null/impossible/archive.tar",
			files:      nil,
			wantErr:    true,
		},
		{
			name:       "staged_file_write_error",
			targetPath: filepath.Join(root, "tar_fail1.tar"),
			files: []stagedFile{
				{absPath: filepath.Join(root, "nonexistent.txt"), relPath: "missing.txt"},
			},
			wantErr: true,
		},
		{
			name:       "remove_failure_on_error",
			targetPath: filepath.Join(root, "tar_fail2.tar"),
			files: []stagedFile{
				{absPath: filepath.Join(root, "nonexistent.txt"), relPath: "missing.txt"},
			},
			setupMock: func(t *testing.T) {
				t.Helper()
				osRemove = func(string) error {
					return errors.New("simulated remove error")
				}
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			osRemove = origRemove
			if tt.setupMock != nil {
				tt.setupMock(t)
			}

			err := writeUncompressedTar(tt.targetPath, []byte("manifest"), tt.files, time.Now())
			if (err != nil) != tt.wantErr {
				t.Fatalf("writeUncompressedTar error = %v, wantErr = %v", err, tt.wantErr)
			}
		})
	}
}

func TestWriteManifestEntryErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		writer   io.Writer
		name     string
		closeTar bool
		wantErr  bool
	}{
		{
			name:     "header_write_error_on_closed_tar",
			writer:   &bytes.Buffer{},
			closeTar: true,
			wantErr:  true,
		},
		{
			name: "body_write_error_on_failing_writer",
			writer: &failAfterBytesWriter{
				err:      errors.New("simulated body write failure"),
				maxBytes: 512,
			},
			closeTar: false,
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			tw := tar.NewWriter(tt.writer)
			if tt.closeTar {
				if cErr := tw.Close(); cErr != nil {
					t.Fatalf("close tar: %v", cErr)
				}
			}

			err := writeManifestEntry(tw, []byte("manifest content"), time.Now())
			if (err != nil) != tt.wantErr {
				t.Fatalf("writeManifestEntry error = %v, wantErr = %v", err, tt.wantErr)
			}
		})
	}
}

func TestWriteStagedFileEntryErrors(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	validFile := filepath.Join(root, "source.txt")
	if err := os.WriteFile(validFile, []byte("payload content"), 0o600); err != nil {
		t.Fatalf("write source: %v", err)
	}

	tests := []struct {
		writer   io.Writer
		name     string
		item     stagedFile
		closeTar bool
		wantErr  bool
	}{
		{
			name: "header_write_error_on_closed_tar",
			item: stagedFile{
				absPath: validFile,
				relPath: "source.txt",
				size:    7,
				mode:    0o600,
			},
			writer:   &bytes.Buffer{},
			closeTar: true,
			wantErr:  true,
		},
		{
			name: "open_source_failure_on_nonexistent_file",
			item: stagedFile{
				absPath: filepath.Join(root, "missing_source.txt"),
				relPath: "missing_source.txt",
				size:    7,
				mode:    0o600,
			},
			writer:   &bytes.Buffer{},
			closeTar: false,
			wantErr:  true,
		},
		{
			name: "copy_error_on_failing_writer",
			item: stagedFile{
				absPath: validFile,
				relPath: "source.txt",
				size:    15,
				mode:    0o600,
			},
			writer: &failAfterBytesWriter{
				err:      errors.New("simulated copy write failure"),
				maxBytes: 512,
			},
			closeTar: false,
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			tw := tar.NewWriter(tt.writer)
			if tt.closeTar {
				if cErr := tw.Close(); cErr != nil {
					t.Fatalf("close tar: %v", cErr)
				}
			}

			err := writeStagedFileEntry(tw, tt.item, time.Now())
			if (err != nil) != tt.wantErr {
				t.Fatalf("writeStagedFileEntry error = %v, wantErr = %v", err, tt.wantErr)
			}
		})
	}
}
