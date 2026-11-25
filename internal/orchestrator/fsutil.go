package orchestrator

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"syscall"
)

func defaultFilepathRel(basepath, targpath string) (string, error) {
	rel, err := filepath.Rel(basepath, targpath)
	if err != nil {
		return "", fmt.Errorf("rel: %w", err)
	}
	return rel, nil
}

func defaultFilepathWalkDir(root string, fn fs.WalkDirFunc) error {
	if err := filepath.WalkDir(root, fn); err != nil {
		return fmt.Errorf("walk: %w", err)
	}
	return nil
}

func defaultWriteFilePayload(file *os.File, data []byte, mode os.FileMode) error {
	_, writeErr := file.Write(data)
	chmodErr := file.Chmod(mode)
	closeFileErr := file.Close()
	return errors.Join(writeErr, chmodErr, closeFileErr)
}

func defaultRootRename(stackRoot, _, _ *os.Root, srcRel, dstRel, name string) error {
	srcPath := path.Join(srcRel, name)
	dstPath := path.Join(dstRel, name)
	if err := stackRoot.Rename(srcPath, dstPath); err != nil {
		return fmt.Errorf("rename %q to %q: %w", srcPath, dstPath, err)
	}
	return nil
}

var (
	filepathRel      = defaultFilepathRel
	filepathWalkDir  = defaultFilepathWalkDir
	writeFilePayload = defaultWriteFilePayload
	osOpenRoot       = os.OpenRoot
	rootSameFile     = os.SameFile
	rootClose        = (*os.Root).Close
	rootLstat        = (*os.Root).Lstat
	rootStat         = (*os.Root).Stat
	rootOpenDot      = (*os.Root).Open
	rootOpenRoot     = (*os.Root).OpenRoot
	rootOpenFile     = (*os.Root).OpenFile
	rootMkdir        = (*os.Root).Mkdir
	rootRename       = defaultRootRename
	rootRemove       = defaultRootRemove
	rootReadDir      = defaultRootReadDir
	fileClose        = (*os.File).Close
	fileStat         = (*os.File).Stat
	fileReadDir      = (*os.File).ReadDir
)

func defaultRootRemove(root *os.Root, name string) error {
	if err := root.Remove(name); err != nil {
		return fmt.Errorf("remove %q: %w", name, err)
	}
	return nil
}

func defaultRootReadDir(root *os.Root) ([]fs.DirEntry, error) {
	dirFile, openErr := rootOpenDot(root, ".")
	if openErr != nil {
		return nil, fmt.Errorf("open directory: %w", openErr)
	}
	entries, readErr := fileReadDir(dirFile, -1)
	closeErr := fileClose(dirFile)
	if err := errors.Join(readErr, closeErr); err != nil {
		return nil, fmt.Errorf("readdir: %w", err)
	}
	return entries, nil
}

func openStackDirNoSymlink(baseRoot *os.Root, slashRel string, createMissing bool) (*os.Root, error) {
	parts := strings.Split(slashRel, "/")
	curr := baseRoot
	closeCurr := false

	for _, seg := range parts {
		next, stepErr := stepStackDir(curr, seg, slashRel, createMissing)
		if closeCurr {
			if closeErr := rootClose(curr); closeErr != nil {
				var nextCloseErr error
				if next != nil {
					nextCloseErr = rootClose(next)
				}
				return nil, errors.Join(stepErr, fmt.Errorf("close directory %q: %w", slashRel, closeErr), nextCloseErr)
			}
		}
		if stepErr != nil {
			return nil, stepErr
		}
		curr = next
		closeCurr = true
	}
	return curr, nil
}

func stepStackDir(curr *os.Root, seg, fullRel string, createMissing bool) (*os.Root, error) {
	role := "source"
	if createMissing {
		role = "destination"
	}
	info, statErr := rootLstat(curr, seg)
	if statErr != nil {
		if !createMissing || !errors.Is(statErr, os.ErrNotExist) {
			return nil, fmt.Errorf("stat %s directory %q (%q): %w", role, seg, fullRel, statErr)
		}
		if mkErr := rootMkdir(curr, seg, 0o700); mkErr != nil && !errors.Is(mkErr, os.ErrExist) {
			return nil, fmt.Errorf("create %s directory %q (%q): %w", role, seg, fullRel, mkErr)
		}
		info, statErr = rootLstat(curr, seg)
		if statErr != nil {
			return nil, fmt.Errorf("stat %s directory %q (%q): %w", role, seg, fullRel, statErr)
		}
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return nil, fmt.Errorf("%s directory %q (%q) must be a real directory", role, seg, fullRel)
	}

	return openVerifiedStackChildDir(curr, seg, info, role, fullRel)
}

func openVerifiedStackChildDir(curr *os.Root, seg string, lstatInfo fs.FileInfo, role, fullRel string) (*os.Root, error) {
	probe, probeErr := rootOpenFile(curr, seg, os.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NONBLOCK, 0)
	if probeErr != nil {
		return nil, fmt.Errorf("open %s directory %q (%q): %w", role, seg, fullRel, probeErr)
	}
	probeInfo, statErr := fileStat(probe)
	closeProbeErr := fileClose(probe)
	if err := errors.Join(statErr, closeProbeErr); err != nil {
		return nil, fmt.Errorf("stat probed %s directory %q (%q): %w", role, seg, fullRel, err)
	}
	if !probeInfo.IsDir() || !rootSameFile(lstatInfo, probeInfo) {
		return nil, fmt.Errorf("concurrent path replacement detected at %q", fullRel)
	}
	return openVerifiedStackChildDirFallback(curr, seg, lstatInfo, role, fullRel)
}

func openVerifiedStackChildDirFallback(curr *os.Root, seg string, lstatInfo fs.FileInfo, role, fullRel string) (*os.Root, error) {
	child, openErr := rootOpenRoot(curr, seg)
	if openErr != nil {
		return nil, fmt.Errorf("open %s directory %q (%q): %w", role, seg, fullRel, openErr)
	}
	dotInfo, dotErr := rootStat(child, ".")
	if dotErr != nil {
		return nil, errors.Join(fmt.Errorf("stat opened %s directory %q (%q): %w", role, seg, fullRel, dotErr), rootClose(child))
	}
	if !dotInfo.IsDir() || !rootSameFile(lstatInfo, dotInfo) {
		return nil, errors.Join(fmt.Errorf("concurrent path replacement detected at %q", fullRel), rootClose(child))
	}
	return child, nil
}

func removeEmptyStackDir(stackRoot *os.Root, slashRel string, expectedInfo fs.FileInfo) error {
	parts := strings.Split(slashRel, "/")
	parent := stackRoot
	closeParent := false
	if len(parts) > 1 {
		var err error
		parent, err = openStackDirNoSymlink(stackRoot, strings.Join(parts[:len(parts)-1], "/"), false)
		if err != nil {
			return err
		}
		closeParent = true
	}

	lastSeg := parts[len(parts)-1]
	rmErr := verifyAndRemoveEmptyDir(parent, lastSeg, slashRel, expectedInfo)
	var closeErr error
	if closeParent {
		closeErr = rootClose(parent)
	}
	if combined := errors.Join(rmErr, closeErr); combined != nil {
		return fmt.Errorf("remove empty source directory %q: %w", slashRel, combined)
	}
	return nil
}

func verifyAndRemoveEmptyDir(parent *os.Root, lastSeg, slashRel string, expectedInfo fs.FileInfo) error {
	recheck, statErr := rootLstat(parent, lastSeg)
	if statErr != nil {
		return fmt.Errorf("stat %q before removal: %w", slashRel, statErr)
	}
	if recheck.Mode()&os.ModeSymlink != 0 || !recheck.IsDir() || (expectedInfo != nil && !rootSameFile(expectedInfo, recheck)) {
		return fmt.Errorf("concurrent path replacement detected at %q", slashRel)
	}
	return rootRemove(parent, lastSeg)
}

func copyDirectoryContents(srcDir, dstDir string) error {
	cleanSrc := filepath.Clean(srcDir)
	if err := filepathWalkDir(cleanSrc, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("refusing symlink %q in reverse-proxy asset tree", path)
		}
		rel, relErr := filepathRel(cleanSrc, path)
		if relErr != nil {
			return fmt.Errorf("relative path for %q: %w", path, relErr)
		}
		target := filepath.Join(dstDir, rel)
		if entry.IsDir() {
			info, infoErr := entry.Info()
			if infoErr != nil {
				return fmt.Errorf("stat dir %q: %w", path, infoErr)
			}
			if err := os.MkdirAll(target, info.Mode().Perm()); err != nil {
				return fmt.Errorf("mkdir %q: %w", target, err)
			}
			return nil
		}
		info, infoErr := entry.Info()
		if infoErr != nil {
			return fmt.Errorf("stat file %q: %w", path, infoErr)
		}
		return copyPlainFile(path, target, info.Mode().Perm())
	}); err != nil {
		return fmt.Errorf("copy dir %q: %w", cleanSrc, err)
	}
	return nil
}

func copyPlainFile(src, dst string, mode os.FileMode) error {
	data, err := os.ReadFile(filepath.Clean(src))
	if err != nil {
		return fmt.Errorf("read %q: %w", src, err)
	}
	return writeBytesToOSFile(dst, data, mode)
}

func writeBytesToOSFile(dst string, data []byte, mode os.FileMode) error {
	cleanDst := filepath.Clean(dst)
	dstDir := filepath.Dir(cleanDst)
	if mkErr := os.MkdirAll(dstDir, 0o750); mkErr != nil {
		return fmt.Errorf("mkdir parent for %q: %w", dst, mkErr)
	}

	dstRoot, rootErr := osOpenRoot(dstDir)
	if rootErr != nil {
		return fmt.Errorf("open destination dir %q: %w", dstDir, rootErr)
	}

	file, openErr := dstRoot.OpenFile(filepath.Base(cleanDst), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if openErr != nil {
		closeRootErr := dstRoot.Close()
		return errors.Join(fmt.Errorf("open %q: %w", dst, openErr), closeRootErr)
	}

	payloadErr := writeFilePayload(file, data, mode)
	closeRootErr := dstRoot.Close()
	if combined := errors.Join(payloadErr, closeRootErr); combined != nil {
		return fmt.Errorf("write %q: %w", dst, combined)
	}
	return nil
}
