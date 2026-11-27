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
	"slices"
	"strings"
	"syscall"

	"github.com/webstudiobond/backup-recovery-orchestrator/internal/cmdexec"
)

var (
	currentEuid = os.Geteuid
	rootLchown  = (*os.Root).Lchown
	rootMkdir   = (*os.Root).Mkdir
)

type pendingDirMeta struct {
	relOS string
	mode  os.FileMode
	uid   int
	gid   int
}

type limitedByteReader struct {
	r         io.Reader
	remaining int64
	limit     int64
}

func (l *limitedByteReader) Read(p []byte) (int, error) {
	if l.remaining <= 0 {
		var probe [1]byte
		n, err := l.r.Read(probe[:])
		if n > 0 {
			return 0, fmt.Errorf("decompressed stream exceeds maximum allowed size of %d bytes", l.limit)
		}
		if err != nil && !errors.Is(err, io.EOF) {
			return 0, fmt.Errorf("read decompressed tail: %w", err)
		}
		return 0, io.EOF
	}
	if int64(len(p)) > l.remaining {
		p = p[:l.remaining]
	}
	n, err := l.r.Read(p)
	l.remaining -= int64(n)
	if errors.Is(err, io.EOF) {
		return n, io.EOF
	}
	if err != nil {
		return n, fmt.Errorf("read decompressed stream: %w", err)
	}
	return n, nil
}

// ExtractStackFilesCleanSlateWithLimits wipes stackDir contents and extracts filesTarZstPath under root confinement with a byte ceiling.
func (e *Engine) ExtractStackFilesCleanSlateWithLimits(ctx context.Context, filesTarZstPath, stackDir string, maxBytes int64) error {
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

	root, err := openRoot(cleanStackDir)
	if err != nil {
		return fmt.Errorf("open stack_dir root %q: %w", cleanStackDir, err)
	}

	extractErr := e.streamDecompressTar(ctx, filesTarZstPath, maxBytes, func(tr *tar.Reader) error {
		return extractStackTarStream(ctx, tr, root)
	})
	return errors.Join(extractErr, root.Close())
}

func effectiveStreamMaxBytes(maxBytes int64) int64 {
	if maxBytes > 0 {
		return maxBytes
	}
	return ContainerLimitsFromGB(0).MaxBytes
}

func (e *Engine) streamDecompressTar(ctx context.Context, tarZstPath string, maxBytes int64, consume func(*tar.Reader) error) error {
	inFile, err := os.Open(filepath.Clean(tarZstPath))
	if err != nil {
		return fmt.Errorf("open compressed archive %q: %w", tarZstPath, err)
	}

	pr, pw, pipeErr := archivePipe()
	if pipeErr != nil {
		return errors.Join(fmt.Errorf("create decompress pipe: %w", pipeErr), archiveCloseFile(inFile))
	}

	streamCtx, cancelStream := context.WithCancel(ctx)
	defer cancelStream()

	zstdDone := make(chan error, 1)
	go func() {
		zstdErr := e.Runner.Run(streamCtx, &cmdexec.Spec{
			Name:   cmdexec.BinZstd,
			Args:   []string{"-dc"},
			Stdin:  inFile,
			Stdout: pw,
		})
		if zstdErr != nil {
			zstdErr = fmt.Errorf("decompress %q: %w", tarZstPath, zstdErr)
		}
		zstdDone <- errors.Join(zstdErr, archiveCloseFile(pw))
	}()

	limit := effectiveStreamMaxBytes(maxBytes)
	tr := tar.NewReader(&limitedByteReader{
		r:         pr,
		remaining: limit,
		limit:     limit,
	})
	consumeErr := consume(tr)
	if consumeErr != nil {
		cancelStream()
	}
	closePrErr := archiveCloseFile(pr)
	zstdErr := <-zstdDone
	closeInErr := archiveCloseFile(inFile)

	if consumeErr != nil {
		return errors.Join(consumeErr, closePrErr, closeInErr)
	}
	return errors.Join(zstdErr, closePrErr, closeInErr)
}

func extractStackTarStream(ctx context.Context, tr *tar.Reader, root *os.Root) error {
	var dirs []pendingDirMeta
	for {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return fmt.Errorf("context canceled during stack extraction: %w", ctxErr)
		}
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("read stack archive entry: %w", err)
		}
		if hdr.Typeflag == tar.TypeDir && path.Clean(hdr.Name) == "." {
			continue
		}
		slashRel, pathErr := sanitizeTarMemberPath(hdr.Name)
		if pathErr != nil {
			return pathErr
		}
		if err := extractStackEntry(root, hdr, tr, slashRel, &dirs); err != nil {
			return err
		}
	}
	return applyPendingDirMetadata(root, dirs)
}

func extractStackEntry(root *os.Root, hdr *tar.Header, tr io.Reader, slashRel string, dirs *[]pendingDirMeta) error {
	mode := os.FileMode(hdr.Mode & 0o777)
	relOS := filepath.FromSlash(slashRel)

	switch hdr.Typeflag {
	case tar.TypeDir:
		if err := ensureRootDirNoSymlink(root, slashRel); err != nil {
			return err
		}
		*dirs = append(*dirs, pendingDirMeta{relOS: relOS, mode: mode, uid: hdr.Uid, gid: hdr.Gid})
		return nil
	case tar.TypeReg:
		if err := ensureParentDirNoSymlink(root, slashRel); err != nil {
			return err
		}
		return writeRegularRootEntryNoFollow(root, relOS, tr, mode, hdr.Uid, hdr.Gid)
	case tar.TypeSymlink:
		if err := validateStackSymlinkTarget(slashRel, hdr.Linkname); err != nil {
			return err
		}
		if err := ensureParentDirNoSymlink(root, slashRel); err != nil {
			return err
		}
		if err := root.Symlink(hdr.Linkname, relOS); err != nil {
			return fmt.Errorf("create symlink %q: %w", relOS, err)
		}
		return applyNumericOwner(root, relOS, hdr.Uid, hdr.Gid)
	default:
		return fmt.Errorf("unsupported stack tar entry type %q for %q", hdr.Typeflag, hdr.Name)
	}
}

func ensureParentDirNoSymlink(root *os.Root, slashRel string) error {
	parent := path.Dir(slashRel)
	if parent == "." {
		return nil
	}
	return ensureRootDirNoSymlink(root, parent)
}

func ensureRootDirNoSymlink(root *os.Root, slashDir string) error {
	var cur string
	for part := range strings.SplitSeq(slashDir, "/") {
		if cur == "" {
			cur = part
		} else {
			cur = filepath.Join(cur, part)
		}
		info, err := root.Lstat(cur)
		if err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("stat directory %q: %w", cur, err)
			}
			if mkErr := rootMkdir(root, cur, 0o750); mkErr != nil {
				return fmt.Errorf("create directory %q: %w", cur, mkErr)
			}
			continue
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("refusing symlink directory component %q", cur)
		}
		if !info.IsDir() {
			return fmt.Errorf("path component %q is not a directory", cur)
		}
	}
	return nil
}

func validateStackSymlinkTarget(slashRel, linkname string) error {
	if linkname == "" || strings.Contains(linkname, "\x00") || strings.HasPrefix(linkname, "/") {
		return fmt.Errorf("unsafe symlink target %q for %q", linkname, slashRel)
	}
	resolved := path.Clean(path.Join(path.Dir(slashRel), filepath.ToSlash(linkname)))
	if resolved == ".." || strings.HasPrefix(resolved, "../") || strings.HasPrefix(resolved, "/") {
		return fmt.Errorf("symlink target %q for %q escapes stack directory", linkname, slashRel)
	}
	return nil
}

func writeRegularRootEntryNoFollow(root *os.Root, relOS string, src io.Reader, mode os.FileMode, uid, gid int) error {
	out, err := root.OpenFile(relOS, os.O_CREATE|os.O_WRONLY|os.O_TRUNC|syscall.O_NOFOLLOW, mode)
	if err != nil {
		return fmt.Errorf("open output file %q: %w", relOS, err)
	}
	_, copyErr := io.Copy(out, src)
	chmodErr := out.Chmod(mode)
	closeErr := out.Close()
	if err := errors.Join(copyErr, chmodErr, closeErr); err != nil {
		return fmt.Errorf("write output file %q: %w", relOS, err)
	}
	return applyNumericOwner(root, relOS, uid, gid)
}

func applyPendingDirMetadata(root *os.Root, dirs []pendingDirMeta) error {
	slices.SortStableFunc(dirs, func(a, b pendingDirMeta) int {
		da := strings.Count(a.relOS, string(filepath.Separator))
		db := strings.Count(b.relOS, string(filepath.Separator))
		return db - da
	})
	for _, d := range dirs {
		if err := root.Chmod(d.relOS, d.mode); err != nil {
			return fmt.Errorf("chmod directory %q: %w", d.relOS, err)
		}
		if err := applyNumericOwner(root, d.relOS, d.uid, d.gid); err != nil {
			return err
		}
	}
	return nil
}

func applyNumericOwner(root *os.Root, relPath string, uid, gid int) error {
	if currentEuid() != 0 {
		return nil
	}
	if err := rootLchown(root, relPath, uid, gid); err != nil {
		return fmt.Errorf("set numeric owner (%d:%d) on %q: %w", uid, gid, relPath, err)
	}
	return nil
}
