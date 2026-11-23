package storage

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// PruneLocal deletes expired local archives while enforcing that at least one local copy always remains.
func PruneLocal(ctx context.Context, localStackDir, stackName string, keepCount int) ([]string, error) {
	if keepCount == 0 {
		return nil, fmt.Errorf("refusing local_count=0 for %q: at least 1 local copy must be retained", stackName)
	}
	return pruneDirectory(ctx, localStackDir, stackName, keepCount)
}

func pruneDirectory(ctx context.Context, dir, stackName string, keepCount int) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("context canceled before pruning %q: %w", dir, err)
	}
	if keepCount < 0 {
		return nil, nil
	}

	archives, err := listStackArchives(dir, stackName)
	if err != nil {
		return nil, err
	}
	if len(archives) <= keepCount {
		return nil, nil
	}

	expired := archives[keepCount:]
	pruned := make([]string, 0, len(expired))
	for _, path := range expired {
		if rmErr := os.Remove(path); rmErr != nil {
			return pruned, fmt.Errorf("remove expired archive %q: %w", path, rmErr)
		}
		pruned = append(pruned, filepath.Base(path))
	}
	return pruned, nil
}

func listStackArchives(dir, stackName string) ([]string, error) {
	entries, err := os.ReadDir(filepath.Clean(dir))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read backup directory %q: %w", dir, err)
	}

	var matched []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if isStackArchiveName(entry.Name(), stackName) {
			matched = append(matched, filepath.Join(dir, entry.Name()))
		}
	}

	sortArchivesNewestFirst(matched, stackName)
	return matched, nil
}

func isStackArchiveName(fileName, stackName string) bool {
	_, ok := parseStackArchiveTime(fileName, stackName)
	return ok
}

func parseStackArchiveTime(fileName, stackName string) (time.Time, bool) {
	prefix := stackName + "_"
	const suffix = ".tar"
	if !strings.HasPrefix(fileName, prefix) || !strings.HasSuffix(fileName, suffix) {
		return time.Time{}, false
	}
	stamp := strings.TrimSuffix(strings.TrimPrefix(fileName, prefix), suffix)
	if baseStamp, ok := strings.CutSuffix(stamp, "_UTC"); ok {
		parsed, err := time.Parse(timestampLayout, baseStamp)
		return parsed, err == nil
	}
	parsed, err := time.ParseInLocation(timestampLayout, stamp, time.Local)
	return parsed, err == nil
}

func sortArchivesNewestFirst(items []string, stackName string) {
	slices.SortFunc(items, func(a, b string) int {
		ta, okA := parseStackArchiveTime(filepath.Base(a), stackName)
		tb, okB := parseStackArchiveTime(filepath.Base(b), stackName)
		if okA && okB && !ta.Equal(tb) {
			if ta.After(tb) {
				return -1
			}
			return 1
		}
		return strings.Compare(b, a)
	})
}
