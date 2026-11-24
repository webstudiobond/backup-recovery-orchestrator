package dac

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
)

func (a *Applier) walkRootAndApply(
	ctx context.Context,
	dirRoot *os.Root,
	dirFullPath string,
	dirInfo fs.FileInfo,
	policy *dacPolicy,
) ([]Change, error) {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, fmt.Errorf("context canceled during DAC walk: %w", ctxErr)
	}

	dirChange, err := a.applyDirNode(dirRoot, dirFullPath, dirInfo, policy)
	if err != nil {
		return nil, err
	}
	changes := []Change{dirChange}

	entries, readErr := readSortedDirEntries(dirRoot, dirFullPath)
	if readErr != nil {
		return nil, readErr
	}

	for _, entry := range entries {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, fmt.Errorf("context canceled during DAC walk: %w", ctxErr)
		}

		name := entry.Name()
		childFullPath := filepath.Join(dirFullPath, name)
		if entry.Type()&os.ModeSymlink != 0 {
			changes = append(changes, symlinkSkippedChange(childFullPath))
			continue
		}
		if entryType := entry.Type(); entryType != 0 && !entryType.IsDir() && !entryType.IsRegular() {
			changes = append(changes, nonRegularSkippedChange(childFullPath))
			continue
		}

		childChanges, childErr := a.applyWalkEntry(ctx, dirRoot, name, childFullPath, policy)
		if childErr != nil {
			return nil, childErr
		}
		changes = append(changes, childChanges...)
	}
	return changes, nil
}

func (a *Applier) applyWalkEntry(
	ctx context.Context,
	parentRoot *os.Root,
	name string,
	childFullPath string,
	policy *dacPolicy,
) ([]Change, error) {
	info, statErr := rootLstat(parentRoot, name)
	if statErr != nil {
		return nil, fmt.Errorf("stat %q: %w", childFullPath, statErr)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return []Change{symlinkSkippedChange(childFullPath)}, nil
	}
	if !info.IsDir() && !info.Mode().IsRegular() {
		return []Change{nonRegularSkippedChange(childFullPath)}, nil
	}
	if !info.IsDir() {
		change, err := a.applyFileNode(parentRoot, name, childFullPath, info, policy)
		if err != nil {
			return nil, err
		}
		return []Change{change}, nil
	}

	childRoot, openErr := openExistingChildDir(parentRoot, name, info, childFullPath)
	if openErr != nil {
		return nil, openErr
	}
	subChanges, walkErr := a.walkRootAndApply(ctx, childRoot, childFullPath, info, policy)
	if closeErr := rootClose(childRoot); closeErr != nil {
		return nil, errors.Join(walkErr, fmt.Errorf("close directory %q: %w", childFullPath, closeErr))
	}
	return subChanges, walkErr
}

func readSortedDirEntries(dirRoot *os.Root, dirFullPath string) ([]fs.DirEntry, error) {
	dirFile, openErr := rootOpenDot(dirRoot, ".")
	if openErr != nil {
		return nil, fmt.Errorf("open directory %q: %w", dirFullPath, openErr)
	}
	entries, readErr := fileReadDir(dirFile, -1)
	closeErr := fileClose(dirFile)
	if err := errors.Join(readErr, closeErr); err != nil {
		return nil, fmt.Errorf("read directory %q: %w", dirFullPath, err)
	}
	slices.SortFunc(entries, func(a, b fs.DirEntry) int {
		return strings.Compare(a.Name(), b.Name())
	})
	return entries, nil
}

func navigateParentRoot(baseRoot *os.Root, segments []string, fullTarget string) (parent *os.Root, closeParent, found bool, err error) {
	curr := baseRoot
	closeCurr := false

	for _, seg := range segments {
		next, stepFound, stepErr := stepIntoChildDir(curr, seg, fullTarget)
		if closeCurr {
			if closeErr := rootClose(curr); closeErr != nil {
				var nextCloseErr error
				if next != nil {
					nextCloseErr = rootClose(next)
				}
				return nil, false, false, errors.Join(stepErr, fmt.Errorf("close intermediate directory: %w", closeErr), nextCloseErr)
			}
		}
		if stepErr != nil {
			return nil, false, false, stepErr
		}
		if !stepFound {
			return nil, false, false, nil
		}
		curr = next
		closeCurr = true
	}

	return curr, closeCurr, true, nil
}

func stepIntoChildDir(curr *os.Root, seg, fullTarget string) (*os.Root, bool, error) {
	info, statErr := rootLstat(curr, seg)
	if statErr != nil {
		if errors.Is(statErr, os.ErrNotExist) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("stat %q in %q: %w", seg, fullTarget, statErr)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, false, fmt.Errorf("refusing symlink %q in path %q", seg, fullTarget)
	}
	if !info.IsDir() {
		return nil, false, fmt.Errorf("non-directory segment %q in path %q", seg, fullTarget)
	}
	child, err := openExistingChildDir(curr, seg, info, fullTarget)
	if err != nil {
		return nil, false, err
	}
	return child, true, nil
}

func openExistingChildDir(parentRoot *os.Root, seg string, lstatInfo fs.FileInfo, fullPath string) (*os.Root, error) {
	probe, probeErr := rootOpenFile(parentRoot, seg, os.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NONBLOCK, 0)
	if probeErr != nil {
		return nil, fmt.Errorf("open directory %q: %w", fullPath, probeErr)
	}
	probeInfo, statErr := fileStat(probe)
	closeProbeErr := fileClose(probe)
	if err := errors.Join(statErr, closeProbeErr); err != nil {
		return nil, fmt.Errorf("stat probed directory %q: %w", fullPath, err)
	}
	if !probeInfo.IsDir() || !rootSameFile(lstatInfo, probeInfo) {
		return nil, fmt.Errorf("concurrent path replacement detected at %q", fullPath)
	}
	return openExistingChildDirFallback(parentRoot, seg, lstatInfo, fullPath)
}

func openExistingChildDirFallback(parentRoot *os.Root, seg string, lstatInfo fs.FileInfo, fullPath string) (*os.Root, error) {
	child, openErr := rootOpenRoot(parentRoot, seg)
	if openErr != nil {
		return nil, fmt.Errorf("open directory %q: %w", fullPath, openErr)
	}
	dotInfo, dotErr := rootStat(child, ".")
	if dotErr != nil {
		return nil, errors.Join(fmt.Errorf("stat opened directory %q: %w", fullPath, dotErr), rootClose(child))
	}
	if !dotInfo.IsDir() || !rootSameFile(lstatInfo, dotInfo) {
		return nil, errors.Join(fmt.Errorf("concurrent path replacement detected at %q", fullPath), rootClose(child))
	}
	return child, nil
}
