package storage

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/webstudiobond/backup-recovery-orchestrator/internal/config"
)

func TestNewManager(t *testing.T) {
	t.Parallel()

	mgr := NewManager(nil, nil)
	if mgr == nil || mgr.Runner == nil || mgr.Verifier == nil {
		t.Fatalf("expected non-nil defaults from NewManager(nil, nil), got %+v", mgr)
	}
}

func TestSyncArchiveAndRetention(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := t.TempDir()
	localDir := filepath.Join(root, "local", "app-alpha")
	mountPoint := filepath.Join(root, "mnt", "cloud-primary")
	remoteBackupRoot := filepath.Join(mountPoint, "backups")
	if err := os.MkdirAll(remoteBackupRoot, 0o700); err != nil {
		t.Fatalf("mkdir remote root: %v", err)
	}

	dest := config.DestinationConfig{
		Name:                "cloud-primary",
		Type:                config.DestTypeFUSE,
		MountPoint:          mountPoint,
		BackupDir:           remoteBackupRoot,
		RcloneCleanupRemote: "cloud-primary-enc",
	}

	remoteAlphaDir := filepath.Join(remoteBackupRoot, "app-alpha")
	createFakeArchive(t, remoteAlphaDir, "app-alpha_2026-11-10_01-00-00.tar", "old1")
	createFakeArchive(t, remoteAlphaDir, "app-alpha_2026-11-11_01-00-00.tar", "old2")
	createFakeArchive(t, remoteAlphaDir, "app-alpha_unrelated_file.tar", "keep")
	createFakeArchive(t, localDir, "app-alpha_2026-11-15_01-00-00.tar", "older-local")
	latestName := "app-alpha_2026-11-16_01-00-00.tar"
	newArchive := createFakeArchive(t, localDir, latestName, "new-archive")

	runner := &fakeStorageRunner{}
	mgr := NewManager(runner, newMockVerifier())

	res, err := mgr.SyncArchive(ctx, &dest, "app-alpha", newArchive, 2)
	if err != nil {
		t.Fatalf("SyncArchive error: %v", err)
	}
	if !slices.Equal(res.Copied, []string{latestName}) {
		t.Fatalf("expected copied archive, got %+v", res)
	}
	if !slices.Equal(res.Pruned, []string{"app-alpha_2026-11-10_01-00-00.tar"}) {
		t.Fatalf("expected oldest remote archive pruned, got %+v", res)
	}

	res2, err2 := mgr.SyncArchive(ctx, &dest, "app-alpha", newArchive, 2)
	if err2 != nil {
		t.Fatalf("second SyncArchive error: %v", err2)
	}
	if !slices.Equal(res2.Skipped, []string{latestName}) {
		t.Fatalf("expected identical-size archive skipped on second sync, got %+v", res2)
	}

	if cleanErr := mgr.CleanupRemote(ctx, &dest); cleanErr != nil {
		t.Fatalf("CleanupRemote error: %v", cleanErr)
	}
	if len(runner.calls) != 1 || !slices.Equal(runner.calls[0], []string{"rclone", "cleanup", "cloud-primary-enc:"}) {
		t.Fatalf("unexpected rclone cleanup call: %+v", runner.calls)
	}

	localPruned, pruneErr := PruneLocal(ctx, localDir, "app-alpha", 1)
	if pruneErr != nil {
		t.Fatalf("PruneLocal error: %v", pruneErr)
	}
	if !slices.Equal(localPruned, []string{"app-alpha_2026-11-15_01-00-00.tar"}) {
		t.Fatalf("expected older local archive pruned while keeping 1 copy, got %+v", localPruned)
	}
}

func TestSyncStackAndLocalPruneGuard(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := t.TempDir()
	localDir := filepath.Join(root, "local", "app-gamma")
	remoteBackupRoot := filepath.Join(root, "local-usb", "backups")

	createFakeArchive(t, localDir, "app-gamma_2026-11-14_01-00-00.tar", "d1")
	createFakeArchive(t, localDir, "app-gamma_2026-11-15_01-00-00.tar", "d2")
	createFakeArchive(t, localDir, "app-gamma_2026-11-16_01-00-00.tar", "d3")

	if _, err := PruneLocal(ctx, localDir, "app-gamma", 0); err == nil || !strings.Contains(err.Error(), "local_count=0") {
		t.Fatalf("expected PruneLocal zero-retention error, got %v", err)
	}

	dest := config.DestinationConfig{
		Name:      "usb-drive",
		Type:      config.DestTypeDir,
		BackupDir: remoteBackupRoot,
	}
	mgr := NewManager(&fakeStorageRunner{}, newMockVerifier())
	res, err := mgr.SyncStack(ctx, &dest, localDir, "app-gamma", 2)
	if err != nil {
		t.Fatalf("SyncStack error: %v", err)
	}
	if len(res.Copied) != 2 {
		t.Fatalf("expected 2 newest archives copied, got %+v", res.Copied)
	}

	unlimPruned, unlimErr := PruneLocal(ctx, localDir, "app-gamma", -1)
	if unlimErr != nil || len(unlimPruned) != 0 {
		t.Fatalf("expected unlimited retention (-1) to prune nothing, got %v err=%v", unlimPruned, unlimErr)
	}
}

func TestSyncArchiveErrors(t *testing.T) {
	t.Parallel()

	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()

	validDest := config.DestinationConfig{
		Name:      "dest-sa-valid",
		Type:      config.DestTypeDir,
		BackupDir: t.TempDir(),
	}

	archiveCases := []struct {
		dest    *config.DestinationConfig
		srcPath string
		ctx     context.Context
		name    string
	}{
		{name: "archive_canceled_context", ctx: canceledCtx, dest: &validDest},
		{name: "nil_destination", ctx: context.Background(), dest: nil},
		{name: "verifier_error", ctx: context.Background(), dest: &config.DestinationConfig{Name: "dest-sa-ver", Type: "unsupported-sa-type"}},
		{name: "copy_error", ctx: context.Background(), dest: &validDest, srcPath: "/nonexistent/src.tar"},
	}

	for _, tc := range archiveCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			src := tc.srcPath
			if src == "" {
				src = "/tmp/arch.tar"
			}
			mgr := NewManager(&fakeStorageRunner{}, newMockVerifier())
			if _, err := mgr.SyncArchive(tc.ctx, tc.dest, "app-sa-err", src, 1); err == nil {
				t.Fatal("expected SyncArchive error")
			}
		})
	}

	t.Run("mkdir_error", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		stackName := "app-sa-mk"
		if err := os.WriteFile(filepath.Join(root, stackName), []byte("x"), 0o600); err != nil {
			t.Fatalf("write file: %v", err)
		}
		dest := config.DestinationConfig{Name: "dest-sa-mk", Type: config.DestTypeDir, BackupDir: root}
		mgr := NewManager(&fakeStorageRunner{}, newMockVerifier())
		archive := createFakeArchive(t, t.TempDir(), stackName+"_2026-11-16_01-00-00.tar", "d")
		if _, err := mgr.SyncArchive(context.Background(), &dest, stackName, archive, 1); err == nil {
			t.Fatal("expected mkdir error")
		}
	})

	t.Run("prune_error", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		stackName := "app-sa-pr"
		remoteStackDir := filepath.Join(root, stackName)
		createFakeArchive(t, remoteStackDir, stackName+"_2026-11-10_01-00-00.tar", "old")
		latestName := stackName + "_2026-11-16_01-00-00.tar"
		createFakeArchive(t, remoteStackDir, latestName, "payload")
		localArchive := createFakeArchive(t, t.TempDir(), latestName, "payload")
		makeReadOnlyDir(t, remoteStackDir)

		dest := config.DestinationConfig{Name: "dest-sa-pr", Type: config.DestTypeDir, BackupDir: root}
		mgr := NewManager(&fakeStorageRunner{}, newMockVerifier())
		res, err := mgr.SyncArchive(context.Background(), &dest, stackName, localArchive, 1)
		if err == nil || !slices.Equal(res.Skipped, []string{latestName}) {
			t.Fatalf("expected prune error and skipped archive, got res=%+v err=%v", res, err)
		}
	})
}

func TestSyncStackValidationErrors(t *testing.T) {
	t.Parallel()

	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()

	validDest := config.DestinationConfig{
		Name:      "dest-stk-valid",
		Type:      config.DestTypeDir,
		BackupDir: t.TempDir(),
	}

	fileAsDir := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(fileAsDir, []byte("x"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}

	stackCases := []struct {
		dest     *config.DestinationConfig
		stackDir string
		ctx      context.Context
		name     string
	}{
		{name: "stack_canceled_context", ctx: canceledCtx, dest: &validDest},
		{name: "nil_destination", ctx: context.Background(), dest: nil},
		{name: "verifier_error", ctx: context.Background(), dest: &config.DestinationConfig{Name: "dest-stk-ver", Type: "unsupported-stk-type"}},
		{name: "list_error", ctx: context.Background(), dest: &validDest, stackDir: fileAsDir},
		{name: "empty_archives", ctx: context.Background(), dest: &validDest, stackDir: t.TempDir()},
	}

	for _, tc := range stackCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := tc.stackDir
			if dir == "" {
				dir = "/tmp"
			}
			mgr := NewManager(&fakeStorageRunner{}, newMockVerifier())
			if _, err := mgr.SyncStack(tc.ctx, tc.dest, dir, "app-stk-err", 1); err == nil {
				t.Fatal("expected SyncStack error")
			}
		})
	}
}

func TestSyncStackExecutionBranches(t *testing.T) {
	t.Parallel()

	t.Run("mkdir_error", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		stackName := "app-stk-mk"
		if err := os.WriteFile(filepath.Join(root, stackName), []byte("x"), 0o600); err != nil {
			t.Fatalf("write file: %v", err)
		}
		dest := config.DestinationConfig{Name: "dest-stk-mk", Type: config.DestTypeDir, BackupDir: root}
		localDir := t.TempDir()
		createFakeArchive(t, localDir, stackName+"_2026-11-16_01-00-00.tar", "d")
		mgr := NewManager(&fakeStorageRunner{}, newMockVerifier())
		if _, err := mgr.SyncStack(context.Background(), &dest, localDir, stackName, 1); err == nil {
			t.Fatal("expected mkdir error")
		}
	})

	t.Run("copy_error", func(t *testing.T) {
		t.Parallel()
		stackName := "app-stk-cp"
		localDir := t.TempDir()
		createFakeArchive(t, localDir, stackName+"_2026-11-16_01-00-00.tar", "d")

		root := t.TempDir()
		remoteStackDir := filepath.Join(root, stackName)
		if err := os.MkdirAll(remoteStackDir, 0o700); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		makeReadOnlyDir(t, remoteStackDir)

		dest := config.DestinationConfig{Name: "dest-stk-cp", Type: config.DestTypeDir, BackupDir: root}
		mgr := NewManager(&fakeStorageRunner{}, newMockVerifier())
		if _, err := mgr.SyncStack(context.Background(), &dest, localDir, stackName, 1); err == nil {
			t.Fatal("expected copy error")
		}
	})

	t.Run("prune_error_and_skip", func(t *testing.T) {
		t.Parallel()
		stackName := "app-stk-pr"
		root := t.TempDir()
		remoteStackDir := filepath.Join(root, stackName)
		createFakeArchive(t, remoteStackDir, stackName+"_2026-11-10_01-00-00.tar", "old")
		latestName := stackName + "_2026-11-16_01-00-00.tar"
		createFakeArchive(t, remoteStackDir, latestName, "payload")
		localDir := t.TempDir()
		createFakeArchive(t, localDir, latestName, "payload")
		makeReadOnlyDir(t, remoteStackDir)

		dest := config.DestinationConfig{Name: "dest-stk-pr", Type: config.DestTypeDir, BackupDir: root}
		mgr := NewManager(&fakeStorageRunner{}, newMockVerifier())
		res, err := mgr.SyncStack(context.Background(), &dest, localDir, stackName, 1)
		if err == nil || !slices.Equal(res.Skipped, []string{latestName}) {
			t.Fatalf("expected prune error and skipped archive, got res=%+v err=%v", res, err)
		}
	})
}

func TestCleanupRemoteValidationAndErrors(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	testCases := []struct {
		dest          *config.DestinationConfig
		runnerErr     error
		name          string
		errSubstr     string
		isInvalidName bool
	}{
		{name: "nil_dest", dest: nil},
		{name: "empty_remote", dest: &config.DestinationConfig{RcloneCleanupRemote: ""}},
		{name: "whitespace_remote", dest: &config.DestinationConfig{RcloneCleanupRemote: "   \t\n"}},
		{name: "colon_in_remote", dest: &config.DestinationConfig{RcloneCleanupRemote: "bad:remote"}, isInvalidName: true},
		{name: "slash_in_remote", dest: &config.DestinationConfig{RcloneCleanupRemote: "bad/remote"}, isInvalidName: true},
		{name: "space_in_remote", dest: &config.DestinationConfig{RcloneCleanupRemote: "bad remote"}, isInvalidName: true},
		{name: "runner_error", dest: &config.DestinationConfig{RcloneCleanupRemote: "cloud-fail"}, runnerErr: errors.New("simulated failure"), errSubstr: "rclone cleanup on"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runner := &fakeStorageRunner{runErr: tc.runnerErr}
			mgr := NewManager(runner, newMockVerifier())
			err := mgr.CleanupRemote(ctx, tc.dest)
			if tc.isInvalidName {
				if err == nil || !strings.Contains(err.Error(), "invalid rclone cleanup remote name") {
					t.Fatalf("expected invalid remote name error, got %v", err)
				}
				return
			}
			if tc.errSubstr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.errSubstr) {
					t.Fatalf("expected error containing %q, got %v", tc.errSubstr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("expected nil error, got %v", err)
			}
		})
	}
}

func TestCopyArchiveIfChangedBranches(t *testing.T) {
	t.Parallel()

	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()

	root := t.TempDir()
	validFile := filepath.Join(root, "valid.tar")
	if err := os.WriteFile(validFile, []byte("content"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}

	unreadableFile := filepath.Join(root, "unreadable.tar")
	if err := os.WriteFile(unreadableFile, []byte("content"), 0o000); err != nil {
		t.Fatalf("write file: %v", err)
	}
	t.Cleanup(func() {
		if cErr := os.Chmod(unreadableFile, 0o600); cErr != nil {
			t.Errorf("cleanup chmod: %v", cErr)
		}
	})

	readOnlyDir := filepath.Join(root, "readonly-dir")
	if err := os.MkdirAll(readOnlyDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	makeReadOnlyDir(t, readOnlyDir)

	dirAsSrc := filepath.Join(root, "dir-as-src.tar")
	if err := os.MkdirAll(dirAsSrc, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	copyCases := []struct {
		src  string
		dst  string
		ctx  context.Context
		name string
	}{
		{name: "copy_canceled_context", ctx: canceledCtx, src: validFile, dst: t.TempDir()},
		{name: "stat_source_error", ctx: context.Background(), src: "/nonexistent/source.tar", dst: t.TempDir()},
		{name: "open_source_error", ctx: context.Background(), src: unreadableFile, dst: t.TempDir()},
		{name: "open_destination_root_error", ctx: context.Background(), src: validFile, dst: "/nonexistent/root/dir"},
		{name: "create_destination_archive_error", ctx: context.Background(), src: validFile, dst: readOnlyDir},
		{name: "copy_directory_fails", ctx: context.Background(), src: dirAsSrc, dst: t.TempDir()},
	}

	for _, tc := range copyCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			copied, err := copyArchiveIfChanged(tc.ctx, tc.src, tc.dst)
			if copied || err == nil {
				t.Fatalf("expected copyArchiveIfChanged to fail, got copied=%v err=%v", copied, err)
			}
		})
	}
}

func TestCopyArchiveIfChangedMockedErrors(t *testing.T) {
	origRootClose := rootClose
	origOSRemove := osRemove
	t.Cleanup(func() {
		rootClose = origRootClose
		osRemove = origOSRemove
	})

	root := t.TempDir()
	validFile := filepath.Join(root, "valid_mock.tar")
	if err := os.WriteFile(validFile, []byte("content"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}

	t.Run("root_close_error", func(t *testing.T) {
		rootClose = failingRootClose
		t.Cleanup(func() {
			rootClose = origRootClose
		})
		dstDir := t.TempDir()
		_, err := copyArchiveIfChanged(context.Background(), validFile, dstDir)
		if err == nil || !strings.Contains(err.Error(), "simulated root close error") {
			t.Fatalf("expected root close error, got err=%v", err)
		}
	})

	t.Run("os_remove_error_on_cleanup", func(t *testing.T) {
		dirAsSrc := filepath.Join(root, "dir_mock_src.tar")
		if err := os.MkdirAll(dirAsSrc, 0o700); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		osRemove = failingOSRemove
		t.Cleanup(func() {
			osRemove = origOSRemove
		})
		dstDir := t.TempDir()
		copied, err := copyArchiveIfChanged(context.Background(), dirAsSrc, dstDir)
		if copied || err == nil || !strings.Contains(err.Error(), "simulated os remove error") {
			t.Fatalf("expected os remove error, got copied=%v err=%v", copied, err)
		}
	})
}
