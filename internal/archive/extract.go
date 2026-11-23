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
	openRoot    = os.OpenRoot
	osRemoveAll = os.RemoveAll
	osMkdirAll  = os.MkdirAll
)

// UnpackContainer extracts all regular members of tarPath into dstDir after enforcing safe member paths.
func UnpackContainer(ctx context.Context, tarPath, dstDir string) error {
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

	unpackErr := extractTarMembers(ctx, file, cleanDst, root)
	closeFileErr := file.Close()
	closeRootErr := root.Close()
	return errors.Join(unpackErr, closeFileErr, closeRootErr)
}

func extractTarMembers(ctx context.Context, r io.Reader, cleanDst string, root *os.Root) error {
	tr := tar.NewReader(r)
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
		if hdr.Typeflag == tar.TypeDir {
			continue
		}
		if hdr.Typeflag != tar.TypeReg {
			return fmt.Errorf("unsupported non-regular tar entry %q", hdr.Name)
		}

		relOS, target, pathErr := cleanArchiveMember(cleanDst, hdr.Name)
		if pathErr != nil {
			return pathErr
		}

		targetDir := filepath.Dir(target)
		if mkErr := os.MkdirAll(targetDir, 0o750); mkErr != nil {
			return fmt.Errorf("create member directory for %q: %w", relOS, mkErr)
		}

		if writeErr := writeRootFile(root, relOS, tr, hdr.FileInfo().Mode().Perm()); writeErr != nil {
			return fmt.Errorf("extract %q: %w", relOS, writeErr)
		}
	}
}

func writeRootFile(root *os.Root, relPath string, src io.Reader, mode os.FileMode) error {
	out, err := root.OpenFile(relPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return fmt.Errorf("open output %q: %w", relPath, err)
	}

	_, copyErr := io.Copy(out, src)
	chmodErr := out.Chmod(mode)
	closeErr := out.Close()
	return errors.Join(copyErr, chmodErr, closeErr)
}

// ExtractStackFilesCleanSlate wipes stackDir contents and extracts filesTarZstPath with numeric ownership preserved.
func (e *Engine) ExtractStackFilesCleanSlate(ctx context.Context, filesTarZstPath, stackDir string) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("context canceled before extracting stack files: %w", err)
	}

	cleanStackDir := filepath.Clean(stackDir)
	if cleanStackDir == "" || cleanStackDir == string(filepath.Separator) || cleanStackDir == "." {
		return fmt.Errorf("refusing clean-slate wipe on unsafe stack_dir %q", stackDir)
	}

	if err := clearDirectoryContents(cleanStackDir); err != nil {
		return err
	}
	if err := osMkdirAll(cleanStackDir, 0o750); err != nil {
		return fmt.Errorf("ensure stack_dir %q: %w", cleanStackDir, err)
	}

	if runErr := e.runTar(ctx, nil, []string{"--numeric-owner", "-xpf", filesTarZstPath, "-C", cleanStackDir}); runErr != nil {
		return fmt.Errorf("extract stack archive %q into %q: %w", filesTarZstPath, cleanStackDir, runErr)
	}
	return nil
}

// ExtractHostFiles extracts explicitly allowlisted host paths from hostFilesTarZstPath relative to root.
func (e *Engine) ExtractHostFiles(ctx context.Context, hostFilesTarZstPath string, allowedRelPaths []string) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("context canceled before extracting host_files: %w", err)
	}
	if len(allowedRelPaths) == 0 {
		return nil
	}

	cleanTargets := make([]string, 0, len(allowedRelPaths))
	for _, rel := range allowedRelPaths {
		sanitized, err := sanitizeTarMemberPath(filepath.ToSlash(rel))
		if err != nil {
			return fmt.Errorf("unsafe host_file target %q: %w", rel, err)
		}
		cleanTargets = append(cleanTargets, filepath.FromSlash(sanitized))
	}

	args := make([]string, 0, 5+len(cleanTargets))
	args = append(args, "--numeric-owner", "-xpf", hostFilesTarZstPath, "-C", string(filepath.Separator))
	args = append(args, cleanTargets...)

	if runErr := e.runTar(ctx, nil, args); runErr != nil {
		return fmt.Errorf("extract host_files from %q: %w", hostFilesTarZstPath, runErr)
	}
	return nil
}

func clearDirectoryContents(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("read directory %q for clean slate: %w", dir, err)
	}
	for _, entry := range entries {
		target := filepath.Join(dir, entry.Name())
		if rmErr := osRemoveAll(target); rmErr != nil {
			return fmt.Errorf("remove %q during clean slate: %w", target, rmErr)
		}
	}
	return nil
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
