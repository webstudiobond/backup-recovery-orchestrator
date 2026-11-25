package orchestrator

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/webstudiobond/backup-recovery-orchestrator/internal/config"
)

const (
	workDirName           = ".bro-work"
	stageDirPrefix        = "bro-stage-"
	restoreDirPrefix      = "bro-restore-"
	maxWorkSubdirAttempts = 16
	workSubdirRandBytes   = 8
	restoreWorkBufferByte = uint64(1 << 30)
)

var (
	osLstat          = os.Lstat
	osStat           = os.Stat
	osMkdirAll       = os.MkdirAll
	osGeteuid        = os.Geteuid
	randRead         = rand.Read
	rootRemoveAll    = (*os.Root).RemoveAll
	sameFilesystemFn = defaultSameFilesystem
)

func backupWorkDir(backupRoot string) string {
	return filepath.Join(filepath.Clean(backupRoot), workDirName)
}

func resolveRestoreWorkDir(global *config.GlobalConfig) string {
	if trimmed := strings.TrimSpace(global.RestoreWorkDir); trimmed != "" {
		return filepath.Clean(trimmed)
	}
	return backupWorkDir(global.BackupRoot)
}

func validateRestoreWorkDirSeparation(workDir, stackDir string) error {
	cleanWork := filepath.Clean(workDir)
	cleanStack := filepath.Clean(stackDir)
	if cleanWork == cleanStack ||
		strings.HasPrefix(cleanWork, cleanStack+string(filepath.Separator)) ||
		strings.HasPrefix(cleanStack, cleanWork+string(filepath.Separator)) {
		return fmt.Errorf("restore work directory %q must not overlap stack_dir %q", cleanWork, cleanStack)
	}
	return nil
}

func ensureWorkRoot(dir string) (*os.Root, error) {
	cleanDir := filepath.Clean(dir)
	if cleanDir == "" || cleanDir == "." || cleanDir == string(filepath.Separator) {
		return nil, fmt.Errorf("refusing unsafe work directory %q", dir)
	}

	info, statErr := osLstat(cleanDir)
	if statErr != nil {
		if !errors.Is(statErr, os.ErrNotExist) {
			return nil, fmt.Errorf("stat work directory %q: %w", cleanDir, statErr)
		}
		if mkErr := osMkdirAll(cleanDir, 0o700); mkErr != nil {
			return nil, fmt.Errorf("create work directory %q: %w", cleanDir, mkErr)
		}
		info, statErr = osLstat(cleanDir)
		if statErr != nil {
			return nil, fmt.Errorf("stat created work directory %q: %w", cleanDir, statErr)
		}
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return nil, fmt.Errorf("work directory %q must be a real directory", cleanDir)
	}

	root, openErr := osOpenRoot(cleanDir)
	if openErr != nil {
		return nil, fmt.Errorf("open work directory %q: %w", cleanDir, openErr)
	}
	dotInfo, dotErr := rootStat(root, ".")
	if dotErr != nil {
		return nil, errors.Join(fmt.Errorf("stat opened work directory %q: %w", cleanDir, dotErr), rootClose(root))
	}
	if !dotInfo.IsDir() || !rootSameFile(info, dotInfo) {
		return nil, errors.Join(fmt.Errorf("concurrent path replacement detected at %q", cleanDir), rootClose(root))
	}
	if secErr := verifyWorkRootOwnershipAndMode(cleanDir, dotInfo); secErr != nil {
		return nil, errors.Join(secErr, rootClose(root))
	}
	return root, nil
}

func verifyWorkRootOwnershipAndMode(cleanDir string, dotInfo fs.FileInfo) error {
	stat, ok := dotInfo.Sys().(*syscall.Stat_t)
	if !ok || stat == nil {
		return fmt.Errorf("work directory %q missing ownership metadata", cleanDir)
	}
	expectedUID := osGeteuid()
	if expectedUID < 0 || uint64(stat.Uid) != uint64(expectedUID) {
		return fmt.Errorf("work directory %q owned by uid %d, expected %d", cleanDir, stat.Uid, expectedUID)
	}
	if perm := dotInfo.Mode().Perm(); perm&0o077 != 0 {
		return fmt.Errorf("work directory %q has overly permissive mode %04o", cleanDir, perm)
	}
	return nil
}

func pruneStaleWorkDirs(dir string) error {
	root, openErr := ensureWorkRoot(dir)
	if openErr != nil {
		return openErr
	}

	entries, readErr := rootReadDir(root)
	if readErr != nil {
		return errors.Join(fmt.Errorf("read work directory %q: %w", dir, readErr), rootClose(root))
	}

	var errs []error
	for _, entry := range entries {
		name := entry.Name()
		if !isManagedWorkEntry(name) {
			continue
		}
		if rmErr := rootRemoveAll(root, name); rmErr != nil {
			errs = append(errs, fmt.Errorf("remove stale work entry %q in %q: %w", name, dir, rmErr))
		}
	}
	if closeErr := rootClose(root); closeErr != nil {
		errs = append(errs, fmt.Errorf("close work directory %q: %w", dir, closeErr))
	}
	return errors.Join(errs...)
}

func createWorkSubdir(workDir, pattern string) (string, error) {
	prefix, suffix, patErr := splitWorkPattern(pattern)
	if patErr != nil {
		return "", patErr
	}

	cleanDir := filepath.Clean(workDir)
	root, openErr := ensureWorkRoot(cleanDir)
	if openErr != nil {
		return "", openErr
	}

	for range maxWorkSubdirAttempts {
		token, randErr := randomWorkToken()
		if randErr != nil {
			return "", errors.Join(randErr, rootClose(root))
		}
		name := prefix + token + suffix
		mkErr := rootMkdir(root, name, 0o700)
		if mkErr == nil {
			if closeErr := rootClose(root); closeErr != nil {
				return "", fmt.Errorf("close work directory %q: %w", cleanDir, closeErr)
			}
			return filepath.Join(cleanDir, name), nil
		}
		if errors.Is(mkErr, os.ErrExist) {
			continue
		}
		return "", errors.Join(fmt.Errorf("create temporary directory in %q: %w", cleanDir, mkErr), rootClose(root))
	}
	return "", errors.Join(fmt.Errorf("create temporary directory in %q: exhausted unique name attempts", cleanDir), rootClose(root))
}

func splitWorkPattern(pattern string) (prefix, suffix string, err error) {
	if pattern == "" || strings.HasPrefix(pattern, ".") || strings.Contains(pattern, "..") ||
		strings.ContainsAny(pattern, `/\`+"\x00") {
		return "", "", fmt.Errorf("invalid work directory pattern %q", pattern)
	}
	if before, after, ok := strings.CutLast(pattern, "*"); ok {
		return before, after, nil
	}
	return pattern, "", nil
}

func randomWorkToken() (string, error) {
	var raw [workSubdirRandBytes]byte
	if _, err := randRead(raw[:]); err != nil {
		return "", fmt.Errorf("generate temporary directory suffix: %w", err)
	}
	return hex.EncodeToString(raw[:]), nil
}

func isManagedWorkEntry(name string) bool {
	return strings.HasPrefix(name, stageDirPrefix) || strings.HasPrefix(name, restoreDirPrefix)
}

func (o *Orchestrator) checkRestoreFreeSpace(ctx context.Context, global *config.GlobalConfig, archivePath, workDir, stackDir string) error {
	info, statErr := osStat(filepath.Clean(archivePath))
	if statErr != nil {
		return fmt.Errorf("stat archive %q: %w", archivePath, statErr)
	}
	archiveBytes := uint64(max(info.Size(), 0))

	workRequired := archiveBytes
	if workDir == backupWorkDir(global.BackupRoot) && global.MinFreeGB > 0 {
		workRequired += restoreWorkBufferByte
	}

	if sameFilesystemFn(workDir, stackDir) {
		combinedRequired, ok := addUint64Checked(workRequired, archiveBytes)
		if !ok {
			return fmt.Errorf("combined required free space for %q overflows uint64", workDir)
		}
		if err := o.LVM.CheckFreeSpaceBytes(ctx, workDir, combinedRequired); err != nil {
			return fmt.Errorf("check free space for shared restore filesystem: %w", err)
		}
		return nil
	}

	if err := o.LVM.CheckFreeSpaceBytes(ctx, workDir, workRequired); err != nil {
		return fmt.Errorf("check free space for restore work directory: %w", err)
	}
	if err := o.LVM.CheckFreeSpaceBytes(ctx, stackDir, archiveBytes); err != nil {
		return fmt.Errorf("check free space for stack_dir: %w", err)
	}
	return nil
}

func addUint64Checked(a, b uint64) (uint64, bool) {
	if a > math.MaxUint64-b {
		return 0, false
	}
	return a + b, true
}

func defaultSameFilesystem(pathA, pathB string) bool {
	statA, okA := existingAncestorStat(filepath.Clean(pathA))
	statB, okB := existingAncestorStat(filepath.Clean(pathB))
	if !okA || !okB {
		return false
	}
	return statA.Dev == statB.Dev
}

func existingAncestorStat(path string) (*syscall.Stat_t, bool) {
	curr := path
	for {
		info, err := osStat(curr)
		if err == nil {
			stat, ok := info.Sys().(*syscall.Stat_t)
			return stat, ok && stat != nil
		}
		parent := filepath.Dir(curr)
		if parent == curr {
			return nil, false
		}
		curr = parent
	}
}
