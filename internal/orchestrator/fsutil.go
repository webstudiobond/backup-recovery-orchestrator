package orchestrator

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
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

var (
	filepathRel      = defaultFilepathRel
	filepathWalkDir  = defaultFilepathWalkDir
	writeFilePayload = defaultWriteFilePayload
)

func copyDirectoryContents(srcDir, dstDir string) error {
	cleanSrc := filepath.Clean(srcDir)
	if err := filepathWalkDir(cleanSrc, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("refusing symlink %q in angie asset tree", path)
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

	cleanDst := filepath.Clean(dst)
	dstDir := filepath.Dir(cleanDst)
	if mkErr := os.MkdirAll(dstDir, 0o750); mkErr != nil {
		return fmt.Errorf("mkdir parent for %q: %w", dst, mkErr)
	}

	dstRoot, rootErr := os.OpenRoot(dstDir)
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
