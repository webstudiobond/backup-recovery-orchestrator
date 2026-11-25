package archive

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
)

var (
	openRoot      = os.OpenRoot
	rootRemoveAll = (*os.Root).RemoveAll
	osMkdirAll    = os.MkdirAll
)

// UnpackContainer extracts all regular members of tarPath into dstDir after enforcing safe member paths and default bounds.
func UnpackContainer(ctx context.Context, tarPath, dstDir string) error {
	return UnpackContainerWithLimits(ctx, tarPath, dstDir, ContainerLimits{})
}

// UnpackContainerWithLimits extracts all regular members of tarPath into dstDir under explicit entry and byte ceilings.
func UnpackContainerWithLimits(ctx context.Context, tarPath, dstDir string, limits ContainerLimits) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("context canceled before unpacking container %q: %w", tarPath, err)
	}

	cleanDst := filepath.Clean(dstDir)
	if err := os.MkdirAll(cleanDst, 0o700); err != nil {
		return fmt.Errorf("create unpack directory %q: %w", cleanDst, err)
	}

	root, rootErr := openRoot(cleanDst)
	if rootErr != nil {
		return fmt.Errorf("open unpack root %q: %w", cleanDst, rootErr)
	}

	file, openErr := os.Open(filepath.Clean(tarPath))
	if openErr != nil {
		closeRootErr := root.Close()
		return errors.Join(fmt.Errorf("open container %q: %w", tarPath, openErr), closeRootErr)
	}

	unpackErr := extractTarMembers(ctx, file, cleanDst, root, normalizeContainerLimits(limits))
	closeFileErr := file.Close()
	closeRootErr := root.Close()
	return errors.Join(unpackErr, closeFileErr, closeRootErr)
}

func extractTarMembers(ctx context.Context, r io.Reader, cleanDst string, root *os.Root, limits ContainerLimits) error {
	tr := tar.NewReader(r)
	var (
		entries    int
		totalBytes int64
	)
	for {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return fmt.Errorf("context canceled during unpack: %w", ctxErr)
		}

		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read container entry: %w", err)
		}

		entries++
		if entries > limits.MaxEntries {
			return fmt.Errorf("container entry count exceeds limit %d", limits.MaxEntries)
		}

		if hdr.Typeflag == tar.TypeDir {
			continue
		}
		remaining := limits.MaxBytes - totalBytes
		if valErr := validateContainerEntryHeader(hdr, remaining, limits); valErr != nil {
			return valErr
		}

		relOS, target, pathErr := cleanArchiveMember(cleanDst, hdr.Name)
		if pathErr != nil {
			return pathErr
		}

		targetDir := filepath.Dir(target)
		if mkErr := os.MkdirAll(targetDir, 0o750); mkErr != nil {
			return fmt.Errorf("create member directory for %q: %w", relOS, mkErr)
		}

		written, writeErr := writeRootFileFn(root, relOS, tr, hdr.FileInfo().Mode().Perm(), remaining)
		if writeErr != nil {
			return fmt.Errorf("extract %q: %w", relOS, writeErr)
		}
		if written > remaining {
			return fmt.Errorf("container payload exceeds maximum allowed size of %d bytes", limits.MaxBytes)
		}
		totalBytes += written
	}
}

var writeRootFileFn = writeRootFile

func writeRootFile(root *os.Root, relPath string, src io.Reader, mode os.FileMode, remaining int64) (int64, error) {
	out, err := root.OpenFile(relPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return 0, fmt.Errorf("open output %q: %w", relPath, err)
	}

	written, copyErr := io.Copy(out, io.LimitReader(src, cappedReadLimit(remaining)))
	chmodErr := out.Chmod(mode)
	closeErr := out.Close()
	return written, errors.Join(copyErr, chmodErr, closeErr)
}

// ExtractStackFilesCleanSlate wipes stackDir contents and extracts filesTarZstPath with numeric ownership preserved.
func (e *Engine) ExtractStackFilesCleanSlate(ctx context.Context, filesTarZstPath, stackDir string) error {
	return e.ExtractStackFilesCleanSlateWithLimits(ctx, filesTarZstPath, stackDir, 0)
}

// ExtractHostFiles extracts explicitly allowlisted host paths from hostFilesTarZstPath relative to root.
func (e *Engine) ExtractHostFiles(ctx context.Context, hostFilesTarZstPath string, allowedRelPaths []string) error {
	return e.ExtractHostFilesWithLimits(ctx, hostFilesTarZstPath, allowedRelPaths, 0)
}

func clearDirectoryContents(dir string) error {
	cleanDir := filepath.Clean(dir)
	entries, err := os.ReadDir(cleanDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("read directory %q for clean slate: %w", dir, err)
	}
	if len(entries) == 0 {
		return nil
	}
	root, rootErr := openRoot(cleanDir)
	if rootErr != nil {
		return fmt.Errorf("open directory %q for clean slate: %w", dir, rootErr)
	}
	var rmErr error
	for _, entry := range entries {
		if err := rootRemoveAll(root, entry.Name()); err != nil {
			rmErr = fmt.Errorf("remove %q in %q during clean slate: %w", entry.Name(), dir, err)
			break
		}
	}
	return errors.Join(rmErr, root.Close())
}

func cleanArchiveMember(cleanDst, raw string) (relOS, targetPath string, err error) {
	if raw == "" || strings.HasPrefix(raw, "/") || strings.Contains(raw, "\x00") {
		return "", "", fmt.Errorf("invalid archive member path %q", raw)
	}
	cleanRel := filepath.Clean(filepath.FromSlash(raw))
	target := filepath.Join(cleanDst, cleanRel)
	if !strings.HasPrefix(target, cleanDst+string(filepath.Separator)) {
		return "", "", fmt.Errorf("unsafe archive member path %q", raw)
	}
	return cleanRel, target, nil
}

func sanitizeTarMemberPath(raw string) (string, error) {
	if raw == "" || strings.HasPrefix(raw, "/") || strings.Contains(raw, "\x00") {
		return "", fmt.Errorf("invalid archive member path %q", raw)
	}
	if !filepath.IsLocal(filepath.FromSlash(raw)) {
		return "", fmt.Errorf("unsafe archive member path %q", raw)
	}
	cleaned := path.Clean(raw)
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") || strings.HasPrefix(cleaned, "/") {
		return "", fmt.Errorf("unsafe archive member path %q", raw)
	}
	return cleaned, nil
}
