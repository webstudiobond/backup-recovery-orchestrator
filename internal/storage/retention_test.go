package storage

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestUTCAndMixedRetention(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	stackName := "app-utc"

	parseCases := []struct {
		wantTime time.Time
		wantLoc  *time.Location
		name     string
		fileName string
		wantOK   bool
	}{
		{
			name:     "utc suffix",
			fileName: "app-utc_2026-11-16_15-04-05_UTC.tar",
			wantLoc:  time.UTC,
			wantTime: time.Date(2026, 11, 16, 15, 4, 5, 0, time.UTC),
			wantOK:   true,
		},
		{
			name:     "local format",
			fileName: "app-utc_2026-11-16_15-04-05.tar",
			wantLoc:  time.Local,
			wantTime: time.Date(2026, 11, 16, 15, 4, 5, 0, time.Local),
			wantOK:   true,
		},
		{
			name:     "other stack",
			fileName: "other_2026-11-16_15-04-05_UTC.tar",
			wantOK:   false,
		},
		{
			name:     "unsupported tz suffix",
			fileName: "app-utc_2026-11-16_15-04-05_EST.tar",
			wantOK:   false,
		},
		{
			name:     "temp suffix",
			fileName: "app-utc_2026-11-16_15-04-05_UTC.tar.tmp",
			wantOK:   false,
		},
	}

	for _, tc := range parseCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, ok := parseStackArchiveTime(tc.fileName, stackName)
			if ok != tc.wantOK {
				t.Fatalf("expected ok=%v, got %v (time=%v)", tc.wantOK, ok, got)
			}
			if !tc.wantOK {
				return
			}
			if !got.Equal(tc.wantTime) || got.Location() != tc.wantLoc {
				t.Fatalf("expected %v (%v), got %v (%v)", tc.wantTime, tc.wantLoc, got, got.Location())
			}
		})
	}

	localDir := filepath.Join(t.TempDir(), stackName)
	createFakeArchive(t, localDir, "app-utc_2026-11-10_12-00-00_UTC.tar", "d1")
	createFakeArchive(t, localDir, "app-utc_2026-11-12_12-00-00.tar", "d2")
	createFakeArchive(t, localDir, "app-utc_2026-11-14_12-00-00_UTC.tar", "d3")
	createFakeArchive(t, localDir, "app-utc_2026-11-16_12-00-00_UTC.tar", "d4")
	createFakeArchive(t, localDir, "app-utc_not-a-timestamp.tar", "ignored")

	pruned, err := PruneLocal(ctx, localDir, stackName, 2)
	if err != nil {
		t.Fatalf("PruneLocal failed: %v", err)
	}
	if len(pruned) != 2 {
		t.Fatalf("expected 2 pruned archives, got %+v", pruned)
	}
	if !slices.Contains(pruned, "app-utc_2026-11-10_12-00-00_UTC.tar") || !slices.Contains(pruned, "app-utc_2026-11-12_12-00-00.tar") {
		t.Fatalf("unexpected pruned set: %+v", pruned)
	}
	for _, kept := range []string{"app-utc_2026-11-14_12-00-00_UTC.tar", "app-utc_2026-11-16_12-00-00_UTC.tar", "app-utc_not-a-timestamp.tar"} {
		if _, statErr := os.Stat(filepath.Join(localDir, kept)); statErr != nil {
			t.Fatalf("expected %q to remain after pruning: %v", kept, statErr)
		}
	}
}

func TestPruneDirectoryValidationAndErrors(t *testing.T) {
	root := t.TempDir()
	stackName := "app-prune-err"

	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := pruneDirectory(canceledCtx, root, stackName, 1); err == nil || !strings.Contains(err.Error(), "context canceled before pruning") {
		t.Fatalf("expected canceled context error, got %v", err)
	}

	fileAsDir := filepath.Join(root, "file_as_dir")
	if err := os.WriteFile(fileAsDir, []byte("x"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}
	if _, err := pruneDirectory(context.Background(), fileAsDir, stackName, 1); err == nil || !strings.Contains(err.Error(), "read backup directory") {
		t.Fatalf("expected read backup directory error, got %v", err)
	}

	pruneDir := filepath.Join(root, "prune_fail_dir")
	createFakeArchive(t, pruneDir, stackName+"_2026-11-10_10-00-00.tar", "d1")
	createFakeArchive(t, pruneDir, stackName+"_2026-11-11_10-00-00.tar", "d2")

	readOnlyPerm := os.FileMode(0o500)
	writablePerm := os.FileMode(0o700)

	if err := os.Chmod(pruneDir, readOnlyPerm); err != nil {
		t.Fatalf("chmod 0500: %v", err)
	}
	t.Cleanup(func() {
		if cErr := os.Chmod(pruneDir, writablePerm); cErr != nil {
			t.Errorf("cleanup chmod: %v", cErr)
		}
	})

	if _, err := pruneDirectory(context.Background(), pruneDir, stackName, 1); err == nil || !strings.Contains(err.Error(), "remove expired archive") {
		t.Fatalf("expected remove expired archive error, got %v", err)
	}
	if cErr := os.Chmod(pruneDir, writablePerm); cErr != nil {
		t.Fatalf("restore chmod: %v", cErr)
	}
}

func TestListStackArchives(t *testing.T) {
	t.Parallel()

	stackName := "app-list"
	missingDir := filepath.Join(t.TempDir(), "nonexistent_dir")
	archives, err := listStackArchives(missingDir, stackName)
	if err != nil || archives != nil {
		t.Fatalf("expected nil, nil for nonexistent dir, got %v, err=%v", archives, err)
	}

	root := t.TempDir()
	listDir := filepath.Join(root, stackName)
	createFakeArchive(t, listDir, stackName+"_2026-11-15_10-00-00.tar", "payload1")
	subDir := filepath.Join(listDir, "sub_directory")
	if err := os.MkdirAll(subDir, 0o700); err != nil {
		t.Fatalf("mkdir subDir: %v", err)
	}

	matched, listErr := listStackArchives(listDir, stackName)
	if listErr != nil {
		t.Fatalf("expected success, got %v", listErr)
	}
	if len(matched) != 1 {
		t.Fatalf("expected 1 matched archive, got %v", matched)
	}

	fileTarget := filepath.Join(root, "regular_file_target")
	if err := os.WriteFile(fileTarget, []byte("data"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}
	if _, err := listStackArchives(fileTarget, stackName); err == nil || !strings.Contains(err.Error(), "read backup directory") {
		t.Fatalf("expected read backup directory error, got %v", err)
	}
}

func TestSortArchivesNewestFirst(t *testing.T) {
	t.Parallel()

	stackName := "app-sort"
	oldest := stackName + "_2026-11-10_10-00-00.tar"
	middle := stackName + "_2026-11-12_10-00-00.tar"
	newest := stackName + "_2026-11-15_10-00-00.tar"
	items := []string{newest, oldest, middle}
	sortArchivesNewestFirst(items, stackName)
	if items[0] != newest || items[1] != middle || items[2] != oldest {
		t.Fatalf("expected [%s %s %s], got %v", newest, middle, oldest, items)
	}

	unparseableA := stackName + "_invalid_stamp_a.tar"
	unparseableB := stackName + "_invalid_stamp_b.tar"
	fallbackItems := []string{unparseableA, unparseableB}
	sortArchivesNewestFirst(fallbackItems, stackName)
	if fallbackItems[0] != unparseableB || fallbackItems[1] != unparseableA {
		t.Fatalf("expected fallback order [%s %s], got %v", unparseableB, unparseableA, fallbackItems)
	}
}
