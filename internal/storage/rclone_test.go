package storage

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/webstudiobond/backup-recovery-orchestrator/internal/config"
)

func TestSyncRcloneDriver(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	localDir := filepath.Join(t.TempDir(), "app-beta")
	latestName := "app-beta_2026-11-16_02-00-00.tar"
	archivePath := createFakeArchive(t, localDir, latestName, "app-beta-tar")

	runner := &fakeStorageRunner{
		lsfSeq: [][]byte{
			[]byte("10;app-beta_2026-11-14_02-00-00.tar\n10;app-beta_2026-11-15_02-00-00.tar"),
			[]byte("10;app-beta_2026-11-14_02-00-00.tar\n10;app-beta_2026-11-15_02-00-00.tar\n10;" + latestName),
		},
	}
	mgr := NewManager(runner, newMockVerifier())

	dest := config.DestinationConfig{
		Name:         "s3",
		Type:         config.DestTypeRclone,
		Remote:       "s3crypt:backups",
		RcloneConfig: "/etc/rclone/rclone.conf",
	}

	res, err := mgr.SyncArchive(ctx, &dest, "app-beta", archivePath, 2)
	if err != nil {
		t.Fatalf("rclone SyncArchive error: %v", err)
	}
	if !slices.Equal(res.Copied, []string{latestName}) {
		t.Fatalf("expected copied archive on size mismatch, got %+v", res)
	}
	if !slices.Equal(res.Pruned, []string{"app-beta_2026-11-14_02-00-00.tar"}) {
		t.Fatalf("expected oldest rclone archive pruned, got %+v", res.Pruned)
	}
	if len(runner.calls) != 4 {
		t.Fatalf("expected 4 rclone calls (lsf, copyto, lsf, deletefile), got %+v", runner.calls)
	}
	if !slices.Contains(runner.calls[1], "--config") || !slices.Contains(runner.calls[1], "copyto") {
		t.Fatalf("expected --config copyto call, got %+v", runner.calls[1])
	}
}

func TestSyncArchiveRcloneBranches(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	dest := config.DestinationConfig{
		Name:         "s3-test",
		Type:         config.DestTypeRclone,
		Remote:       "s3test:bucket",
		RcloneConfig: "/etc/rclone.conf",
	}

	t.Run("stat_error", func(t *testing.T) {
		t.Parallel()
		mgr := NewManager(&fakeStorageRunner{}, newMockVerifier())
		_, err := mgr.SyncArchive(ctx, &dest, "app-stat", "/nonexistent/file.tar", 1)
		if err == nil || !strings.Contains(err.Error(), "stat source archive") {
			t.Fatalf("expected stat error, got %v", err)
		}
	})

	t.Run("list_error", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		archive := createFakeArchive(t, dir, "app-list_2026-11-16_02-00-00.tar", "data")
		runner := &fakeStorageRunner{outputErr: errors.New("list failed")}
		mgr := NewManager(runner, newMockVerifier())
		_, err := mgr.SyncArchive(ctx, &dest, "app-list", archive, 1)
		if err == nil || !strings.Contains(err.Error(), "list failed") {
			t.Fatalf("expected list error, got %v", err)
		}
	})

	t.Run("skipped_identical_size", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		name := "app-skip_2026-11-16_02-00-00.tar"
		archive := createFakeArchive(t, dir, name, "same-payload")
		runner := &fakeStorageRunner{
			lsfSeq: [][]byte{
				[]byte("12;" + name),
			},
		}
		mgr := NewManager(runner, newMockVerifier())
		res, err := mgr.SyncArchive(ctx, &dest, "app-skip", archive, 1)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !slices.Equal(res.Skipped, []string{name}) {
			t.Fatalf("expected skipped archive, got %+v", res)
		}
		if len(res.Copied) != 0 {
			t.Fatalf("expected no copied archive, got %+v", res)
		}
	})

	t.Run("copy_error", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		name := "app-copyerr_2026-11-16_02-00-00.tar"
		archive := createFakeArchive(t, dir, name, "data")
		runner := &fakeStorageRunner{runErr: errors.New("copy fail")}
		mgr := NewManager(runner, newMockVerifier())
		_, err := mgr.SyncArchive(ctx, &dest, "app-copyerr", archive, 1)
		if err == nil || !strings.Contains(err.Error(), "copy fail") {
			t.Fatalf("expected copy error, got %v", err)
		}
	})

	t.Run("prune_error", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		name := "app-pruneerr_2026-11-16_02-00-00.tar"
		archive := createFakeArchive(t, dir, name, "data")
		runner := &fakeStorageRunner{
			lsfSeq: [][]byte{
				nil,
				[]byte("4;app-pruneerr_2026-11-14_02-00-00.tar\n4;" + name),
			},
			failMatch: "app-pruneerr_2026-11-14",
		}
		mgr := NewManager(runner, newMockVerifier())
		res, err := mgr.SyncArchive(ctx, &dest, "app-pruneerr", archive, 1)
		if err == nil || !strings.Contains(err.Error(), "app-pruneerr_2026-11-14") {
			t.Fatalf("expected deletefile error, got %v", err)
		}
		if !slices.Equal(res.Copied, []string{name}) {
			t.Fatalf("expected copied archive in partial result, got %+v", res)
		}
	})
}

func TestSyncStackRcloneErrors(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	dest := config.DestinationConfig{
		Name:         "s3-stack",
		Type:         config.DestTypeRclone,
		Remote:       "s3stack:bucket",
		RcloneConfig: "/etc/rclone/custom.conf",
	}

	t.Run("list_local_error", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		filePath := filepath.Join(dir, "not-a-dir")
		if err := os.WriteFile(filePath, []byte("x"), 0o600); err != nil {
			t.Fatalf("write file: %v", err)
		}
		mgr := NewManager(&fakeStorageRunner{}, newMockVerifier())
		_, err := mgr.SyncStack(ctx, &dest, filePath, "app-badpath", 1)
		if err == nil {
			t.Fatal("expected error for file instead of dir")
		}
	})

	t.Run("empty_archives", func(t *testing.T) {
		t.Parallel()
		emptyDir := t.TempDir()
		mgr := NewManager(&fakeStorageRunner{}, newMockVerifier())
		_, err := mgr.SyncStack(ctx, &dest, emptyDir, "app-empty", 1)
		if err == nil || !strings.Contains(err.Error(), "no local archives found") {
			t.Fatalf("expected no local archives error, got %v", err)
		}
	})

	t.Run("remote_list_error", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		createFakeArchive(t, dir, "app-remlist_2026-11-16_02-00-00.tar", "data")
		runner := &fakeStorageRunner{outputErr: errors.New("remote list err")}
		mgr := NewManager(runner, newMockVerifier())
		_, err := mgr.SyncStack(ctx, &dest, dir, "app-remlist", 1)
		if err == nil || !strings.Contains(err.Error(), "remote list err") {
			t.Fatalf("expected remote list error, got %v", err)
		}
	})

	t.Run("stat_error_in_loop", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		archPath := createFakeArchive(t, dir, "app-statloop_2026-11-16_02-00-00.tar", "data")
		runner := &fakeStorageRunner{
			onOutput: func() {
				if rmErr := os.Remove(archPath); rmErr != nil {
					panic(rmErr)
				}
			},
		}
		mgr := NewManager(runner, newMockVerifier())
		_, err := mgr.SyncStack(ctx, &dest, dir, "app-statloop", 1)
		if err == nil || !strings.Contains(err.Error(), "stat local archive") {
			t.Fatalf("expected stat local archive error, got %v", err)
		}
	})

	t.Run("copy_error_in_loop", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		createFakeArchive(t, dir, "app-stkcopy_2026-11-16_02-00-00.tar", "data")
		runner := &fakeStorageRunner{runErr: errors.New("stkcopy fail")}
		mgr := NewManager(runner, newMockVerifier())
		_, err := mgr.SyncStack(ctx, &dest, dir, "app-stkcopy", 1)
		if err == nil || !strings.Contains(err.Error(), "stkcopy fail") {
			t.Fatalf("expected copy error, got %v", err)
		}
	})

	t.Run("prune_error_after_loop", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		name := "app-stkprune_2026-11-16_02-00-00.tar"
		createFakeArchive(t, dir, name, "data")
		runner := &fakeStorageRunner{
			lsfSeq: [][]byte{
				nil,
				[]byte("4;app-stkprune_2026-11-14_02-00-00.tar\n4;" + name),
			},
			failMatch: "app-stkprune_2026-11-14",
		}
		mgr := NewManager(runner, newMockVerifier())
		res, err := mgr.SyncStack(ctx, &dest, dir, "app-stkprune", 1)
		if err == nil || !strings.Contains(err.Error(), "app-stkprune_2026-11-14") {
			t.Fatalf("expected deletefile error, got %v", err)
		}
		if !slices.Equal(res.Copied, []string{name}) {
			t.Fatalf("expected copied archive in partial result, got %+v", res)
		}
	})
}

func TestSyncStackRcloneSuccess(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	dest := config.DestinationConfig{
		Name:         "s3-stack",
		Type:         config.DestTypeRclone,
		Remote:       "s3stack:bucket",
		RcloneConfig: "/etc/rclone/custom.conf",
	}

	dir := t.TempDir()
	name1 := "app-stksucc_2026-11-16_03-00-00.tar"
	name2 := "app-stksucc_2026-11-16_02-00-00.tar"
	name3 := "app-stksucc_2026-11-16_01-00-00.tar"
	createFakeArchive(t, dir, name1, "data-newest")
	createFakeArchive(t, dir, name2, "data-second")
	createFakeArchive(t, dir, name3, "data-third")

	runner := &fakeStorageRunner{
		lsfSeq: [][]byte{
			[]byte("11;" + name2),
			[]byte("11;app-stksucc_2026-11-10_01-00-00.tar\n11;" + name2 + "\n11;" + name1),
		},
	}
	mgr := NewManager(runner, newMockVerifier())
	res, err := mgr.SyncStack(ctx, &dest, dir, "app-stksucc", 2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !slices.Equal(res.Copied, []string{name1}) {
		t.Fatalf("expected copied %v, got %v", name1, res.Copied)
	}
	if !slices.Equal(res.Skipped, []string{name2}) {
		t.Fatalf("expected skipped %v, got %v", name2, res.Skipped)
	}
	if !slices.Equal(res.Pruned, []string{"app-stksucc_2026-11-10_01-00-00.tar"}) {
		t.Fatalf("expected pruned old archive, got %v", res.Pruned)
	}
}

func TestPruneRcloneStackBranches(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	dest := config.DestinationConfig{
		Name:   "s3-prune",
		Type:   config.DestTypeRclone,
		Remote: "s3prune:bucket",
	}

	t.Run("negative_keep_count", func(t *testing.T) {
		t.Parallel()
		mgr := NewManager(&fakeStorageRunner{}, newMockVerifier())
		pruned, err := mgr.pruneRcloneStack(ctx, &dest, "s3prune:bucket/app-neg", "app-neg", -1)
		if err != nil || pruned != nil {
			t.Fatalf("expected nil, nil for keepCount < 0, got %v, %v", pruned, err)
		}
	})

	t.Run("list_error", func(t *testing.T) {
		t.Parallel()
		runner := &fakeStorageRunner{outputErr: errors.New("list failed")}
		mgr := NewManager(runner, newMockVerifier())
		pruned, err := mgr.pruneRcloneStack(ctx, &dest, "s3prune:bucket/app-listerr", "app-listerr", 1)
		if err == nil || pruned != nil {
			t.Fatalf("expected error from list, got %v, %v", pruned, err)
		}
	})

	t.Run("fewer_than_or_equal_to_keep", func(t *testing.T) {
		t.Parallel()
		runner := &fakeStorageRunner{
			lsfSeq: [][]byte{
				[]byte("10;app-few_2026-11-16_01-00-00.tar"),
			},
		}
		mgr := NewManager(runner, newMockVerifier())
		pruned, err := mgr.pruneRcloneStack(ctx, &dest, "s3prune:bucket/app-few", "app-few", 2)
		if err != nil || pruned != nil {
			t.Fatalf("expected nil, nil when entries <= keepCount, got %v, %v", pruned, err)
		}
	})

	t.Run("delete_file_error", func(t *testing.T) {
		t.Parallel()
		runner := &fakeStorageRunner{
			lsfSeq: [][]byte{
				[]byte("10;app-delerr_2026-11-14_01-00-00.tar\n10;app-delerr_2026-11-15_01-00-00.tar"),
			},
			failMatch: "app-delerr_2026-11-14",
		}
		mgr := NewManager(runner, newMockVerifier())
		pruned, err := mgr.pruneRcloneStack(ctx, &dest, "s3prune:bucket/app-delerr", "app-delerr", 1)
		if err == nil || !strings.Contains(err.Error(), "app-delerr_2026-11-14") {
			t.Fatalf("expected deletefile error, got %v", err)
		}
		if len(pruned) != 0 {
			t.Fatalf("expected empty pruned list on immediate failure, got %v", pruned)
		}
	})
}

func TestListRcloneStackFilesBranches(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	dest := config.DestinationConfig{
		Name:   "s3-lsf",
		Type:   config.DestTypeRclone,
		Remote: "s3lsf:bucket",
	}

	t.Run("directory_not_found", func(t *testing.T) {
		t.Parallel()
		runner := &fakeStorageRunner{outputErr: errors.New("Directory not found")}
		mgr := NewManager(runner, newMockVerifier())
		files, err := mgr.listRcloneStackFiles(ctx, &dest, "s3lsf:bucket/app-dnf", "app-dnf")
		if err != nil {
			t.Fatalf("expected nil error for directory not found, got %v", err)
		}
		if len(files) != 0 {
			t.Fatalf("expected empty files map, got %v", files)
		}
	})

	t.Run("general_output_error", func(t *testing.T) {
		t.Parallel()
		runner := &fakeStorageRunner{outputErr: errors.New("connection reset")}
		mgr := NewManager(runner, newMockVerifier())
		_, err := mgr.listRcloneStackFiles(ctx, &dest, "s3lsf:bucket/app-gen", "app-gen")
		if err == nil || !strings.Contains(err.Error(), "rclone lsf") {
			t.Fatalf("expected rclone lsf error, got %v", err)
		}
	})

	t.Run("parsing_varieties", func(t *testing.T) {
		t.Parallel()
		runner := &fakeStorageRunner{
			lsfSeq: [][]byte{
				[]byte("\n\ncorrupted_no_semicolon\n100;otherstack_2026-11-16_01-00-00.tar\n250;app-parse_2026-11-16_02-00-00.tar\n"),
			},
		}
		mgr := NewManager(runner, newMockVerifier())
		files, err := mgr.listRcloneStackFiles(ctx, &dest, "s3lsf:bucket/app-parse", "app-parse")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(files) != 1 || files["app-parse_2026-11-16_02-00-00.tar"] != 250 {
			t.Fatalf("expected only matching entry parsed, got %+v", files)
		}
	})

	t.Run("invalid_size_integer", func(t *testing.T) {
		t.Parallel()
		runner := &fakeStorageRunner{
			lsfSeq: [][]byte{
				[]byte("badsize;app-badsize_2026-11-16_02-00-00.tar\n"),
			},
		}
		mgr := NewManager(runner, newMockVerifier())
		_, err := mgr.listRcloneStackFiles(ctx, &dest, "s3lsf:bucket/app-badsize", "app-badsize")
		if err == nil || !strings.Contains(err.Error(), "parse rclone size") {
			t.Fatalf("expected parse size error, got %v", err)
		}
	})

	t.Run("scanner_error", func(t *testing.T) {
		t.Parallel()
		runner := &fakeStorageRunner{
			lsfSeq: [][]byte{
				bytes.Repeat([]byte("a"), 70000),
			},
		}
		mgr := NewManager(runner, newMockVerifier())
		_, err := mgr.listRcloneStackFiles(ctx, &dest, "s3lsf:bucket/app-scanerr", "app-scanerr")
		if err == nil || !strings.Contains(err.Error(), "scan rclone listing") {
			t.Fatalf("expected scan rclone listing error, got %v", err)
		}
	})
}

func TestRcloneTargetAndHelpers(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name       string
		baseRemote string
		stackName  string
		expected   string
	}{
		{
			name:       "colon_suffix",
			baseRemote: "s3:",
			stackName:  "web",
			expected:   "s3:web",
		},
		{
			name:       "trailing_slash_trimmed",
			baseRemote: "remote:backups/",
			stackName:  "db",
			expected:   "remote:backups/db",
		},
		{
			name:       "plain_remote_path",
			baseRemote: "remote:backups",
			stackName:  "cache",
			expected:   "remote:backups/cache",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			actual := rcloneStackTarget(tc.baseRemote, tc.stackName)
			if actual != tc.expected {
				t.Fatalf("rcloneStackTarget(%q, %q) = %q; expected %q", tc.baseRemote, tc.stackName, actual, tc.expected)
			}
		})
	}

	ctx := context.Background()

	t.Run("run_rclone_error", func(t *testing.T) {
		t.Parallel()
		mgr := NewManager(&fakeStorageRunner{runErr: errors.New("exec error")}, newMockVerifier())
		err := mgr.runRclone(ctx, "", "version")
		if err == nil || !strings.Contains(err.Error(), "run rclone") {
			t.Fatalf("expected run rclone error, got %v", err)
		}
	})

	t.Run("output_rclone_error", func(t *testing.T) {
		t.Parallel()
		mgr := NewManager(&fakeStorageRunner{outputErr: errors.New("out error")}, newMockVerifier())
		_, err := mgr.outputRclone(ctx, "", "version")
		if err == nil || !strings.Contains(err.Error(), "output rclone") {
			t.Fatalf("expected output rclone error, got %v", err)
		}
	})

	t.Run("copy_file_rclone_error", func(t *testing.T) {
		t.Parallel()
		mgr := NewManager(&fakeStorageRunner{runErr: errors.New("copyto fail")}, newMockVerifier())
		dest := config.DestinationConfig{Name: "dest-test"}
		err := mgr.copyFileRclone(ctx, &dest, "/tmp/src", "s3:target")
		if err == nil || !strings.Contains(err.Error(), "rclone copyto") {
			t.Fatalf("expected copyto error, got %v", err)
		}
	})
}
