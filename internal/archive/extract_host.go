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
)

func defaultOpenHostRoot() (*os.Root, error) {
	return openRoot(string(filepath.Separator))
}

var openHostRoot = defaultOpenHostRoot

// ExtractHostFilesWithLimits extracts allowlisted paths from hostFilesTarZstPath under host root confinement with a byte ceiling.
func (e *Engine) ExtractHostFilesWithLimits(ctx context.Context, hostFilesTarZstPath string, allowedRelPaths []string, maxBytes int64) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("context canceled before extracting host_files: %w", err)
	}
	if len(allowedRelPaths) == 0 {
		return nil
	}

	allowedSet := make(map[string]struct{}, len(allowedRelPaths))
	for _, raw := range allowedRelPaths {
		cleanRel, err := sanitizeTarMemberPath(raw)
		if err != nil {
			return err
		}
		allowedSet[cleanRel] = struct{}{}
	}

	root, err := openHostRoot()
	if err != nil {
		return fmt.Errorf("open host root: %w", err)
	}

	extractErr := e.streamDecompressTar(ctx, hostFilesTarZstPath, maxBytes, func(tr *tar.Reader) error {
		return extractHostTarStream(ctx, tr, root, allowedSet)
	})
	return errors.Join(extractErr, root.Close())
}

func extractHostTarStream(ctx context.Context, tr *tar.Reader, root *os.Root, allowedSet map[string]struct{}) error {
	for {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return fmt.Errorf("context canceled during host_files extraction: %w", ctxErr)
		}
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read host_files archive entry: %w", err)
		}
		if hdr.Typeflag == tar.TypeDir && path.Clean(hdr.Name) == "." {
			continue
		}
		slashRel, pathErr := sanitizeTarMemberPath(hdr.Name)
		if pathErr != nil {
			return pathErr
		}
		if err := extractHostEntry(root, hdr, slashRel, tr, allowedSet); err != nil {
			return err
		}
	}
}

func extractHostEntry(root *os.Root, hdr *tar.Header, slashRel string, tr io.Reader, allowedSet map[string]struct{}) error {
	mode := os.FileMode(hdr.Mode & 0o777)
	relOS := filepath.FromSlash(slashRel)

	switch hdr.Typeflag {
	case tar.TypeDir:
		if _, ok := allowedSet[slashRel]; !ok {
			return nil
		}
		if mkErr := root.MkdirAll(relOS, 0o750); mkErr != nil {
			return fmt.Errorf("create host directory %q: %w", relOS, mkErr)
		}
		if err := errors.Join(root.Chmod(relOS, mode), applyNumericOwner(root, relOS, hdr.Uid, hdr.Gid)); err != nil {
			return fmt.Errorf("apply host directory metadata %q: %w", relOS, err)
		}
		return nil
	case tar.TypeReg:
		if _, ok := allowedSet[slashRel]; !ok {
			return nil
		}
		if parent := filepath.Dir(relOS); parent != "." {
			if mkErr := root.MkdirAll(parent, 0o750); mkErr != nil {
				return fmt.Errorf("create host parent directory for %q: %w", relOS, mkErr)
			}
		}
		return writeRegularRootEntryNoFollow(root, relOS, tr, mode, hdr.Uid, hdr.Gid)
	default:
		return fmt.Errorf("unsupported host tar entry type %q for %q", hdr.Typeflag, hdr.Name)
	}
}
