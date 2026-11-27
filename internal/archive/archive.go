// Package archive creates, checksum-verifies, and extracts modular uncompressed TAR backup containers.
package archive

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/webstudiobond/backup-recovery-orchestrator/internal/cmdexec"
	"github.com/webstudiobond/backup-recovery-orchestrator/internal/config"
)

// Engine packages and verifies modular uncompressed TAR backup containers.
type Engine struct {
	Runner cmdexec.Runner
	Now    func() time.Time
}

// NewEngine builds an archive Engine backed by OSRunner when runner is nil.
func NewEngine(runner cmdexec.Runner) *Engine {
	if runner == nil {
		runner = cmdexec.OSRunner{}
	}
	return &Engine{
		Runner: runner,
		Now:    time.Now,
	}
}

// ArchiveStackFiles creates a zstd-compressed tar archive of stackDir from inside the LVM snapshot mount
// and returns the uncompressed tar byte count.
func (e *Engine) ArchiveStackFiles(
	ctx context.Context,
	snapshotMount string,
	stackDir string,
	filesCfg *config.FilesConfig,
	zstdLevel int,
	outPath string,
) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, fmt.Errorf("context canceled before archiving stack files: %w", err)
	}

	snapSourceDir := filepath.Join(filepath.Clean(snapshotMount), stackSnapshotSubpath(stackDir))
	info, statErr := os.Stat(snapSourceDir)
	if statErr != nil || !info.IsDir() {
		return 0, fmt.Errorf("snapshot source directory %q not found: %w", snapSourceDir, statErr)
	}
	if mkErr := os.MkdirAll(filepath.Dir(outPath), 0o700); mkErr != nil {
		return 0, fmt.Errorf("create archive staging directory: %w", mkErr)
	}

	args := make([]string, 0, 12+len(filesCfg.Excludes))
	args = append(args, "--wildcards")
	if filesCfg.NoWildcardsMatchSlash {
		args = append(args, "--no-wildcards-match-slash")
	}
	args = append(args, "-cpf", "-", "--numeric-owner")
	if filesCfg.ExcludesFile != "" {
		args = append(args, "--exclude-from="+filesCfg.ExcludesFile)
	}
	for _, pat := range filesCfg.Excludes {
		if strings.TrimSpace(pat) == "" {
			continue
		}
		args = append(args, "--exclude="+pat)
	}
	args = append(args, "-C", snapSourceDir, "--", ".")

	n, runErr := e.streamTarToZstdFile(ctx, args, zstdLevel, outPath)
	if runErr != nil {
		return 0, fmt.Errorf("archive stack files from %q: %w", snapSourceDir, runErr)
	}
	return n, nil
}

// ArchiveHostFiles captures configured host paths outside stack_dir into outPath relative to root
// and returns host metadata alongside the uncompressed tar byte count.
func (e *Engine) ArchiveHostFiles(
	ctx context.Context,
	items []config.HostFileConfig,
	zstdLevel int,
	outPath string,
) ([]ManifestHostFile, int64, error) {
	if err := ctx.Err(); err != nil {
		return nil, 0, fmt.Errorf("context canceled before archiving host_files: %w", err)
	}
	if len(items) == 0 {
		return nil, 0, nil
	}

	records := make([]ManifestHostFile, 0, len(items))
	var existingRel []string

	for i := range items {
		item := &items[i]
		cleanPath := filepath.Clean(item.Path)
		tarRel := strings.TrimPrefix(cleanPath, string(filepath.Separator))
		if err := validateHostRelPath(cleanPath, tarRel); err != nil {
			return nil, 0, err
		}

		rec := ManifestHostFile{
			Path:     cleanPath,
			TarPath:  tarRel,
			Owner:    item.Owner,
			DirMode:  item.DirMode,
			FileMode: item.FileMode,
			Optional: item.Optional,
		}

		if _, statErr := os.Lstat(cleanPath); statErr != nil {
			if errors.Is(statErr, os.ErrNotExist) && item.Optional {
				records = append(records, rec)
				continue
			}
			return nil, 0, fmt.Errorf("stat host_file %q: %w", cleanPath, statErr)
		}

		rec.Present = true
		records = append(records, rec)
		existingRel = append(existingRel, tarRel)
	}

	if len(existingRel) == 0 {
		return records, 0, nil
	}
	if mkErr := os.MkdirAll(filepath.Dir(outPath), 0o700); mkErr != nil {
		return nil, 0, fmt.Errorf("create host_files archive directory: %w", mkErr)
	}

	args := make([]string, 0, 7+len(existingRel))
	args = append(args, "-cpf", "-", "--numeric-owner", "-C", string(filepath.Separator), "--")
	args = append(args, existingRel...)

	n, runErr := e.streamTarToZstdFile(ctx, args, zstdLevel, outPath)
	if runErr != nil {
		return nil, 0, fmt.Errorf("archive host_files: %w", runErr)
	}
	return records, n, nil
}

type countingWriter struct {
	n int64
}

func (w *countingWriter) Write(p []byte) (int, error) {
	w.n += int64(len(p))
	return len(p), nil
}

func defaultArchiveCreateFile(path string) (*os.File, error) {
	f, err := os.OpenFile(filepath.Clean(path), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return nil, fmt.Errorf("create archive output %q: %w", path, err)
	}
	return f, nil
}

func defaultArchiveCloseFile(f *os.File) error {
	if err := f.Close(); err != nil {
		return fmt.Errorf("close archive file: %w", err)
	}
	return nil
}

var (
	archivePipe       = os.Pipe
	archiveCreateFile = defaultArchiveCreateFile
	archiveCloseFile  = defaultArchiveCloseFile
)

func (e *Engine) streamTarToZstdFile(ctx context.Context, tarArgs []string, zstdLevel int, outPath string) (int64, error) {
	pr, pw, err := archivePipe()
	if err != nil {
		return 0, fmt.Errorf("create archive pipe: %w", err)
	}

	outFile, err := archiveCreateFile(outPath)
	if err != nil {
		return 0, errors.Join(err, archiveCloseFile(pr), archiveCloseFile(pw))
	}

	streamCtx, cancelStream := context.WithCancel(ctx)
	defer cancelStream()

	cw := &countingWriter{}
	tarDone := make(chan error, 1)
	go func() {
		tarErr := e.Runner.Run(streamCtx, &cmdexec.Spec{
			Name:   cmdexec.BinTar,
			Args:   tarArgs,
			Stdout: io.MultiWriter(pw, cw),
		})
		if tarErr != nil {
			tarErr = fmt.Errorf("tar %s: %w", strings.Join(tarArgs, " "), tarErr)
		}
		tarDone <- errors.Join(tarErr, archiveCloseFile(pw))
	}()

	zstdErr := e.Runner.Run(streamCtx, &cmdexec.Spec{
		Name:   cmdexec.BinZstd,
		Args:   []string{fmt.Sprintf("-%d", zstdLevel), "-q"},
		Stdin:  pr,
		Stdout: outFile,
	})
	closePrErr := archiveCloseFile(pr)
	if zstdErr != nil {
		cancelStream()
		zstdErr = fmt.Errorf("zstd -%d -q: %w", zstdLevel, zstdErr)
	}

	tarErr := <-tarDone
	closeOutErr := archiveCloseFile(outFile)

	if combinedErr := errors.Join(tarErr, zstdErr, closePrErr, closeOutErr); combinedErr != nil {
		return 0, combinedErr
	}
	if err := ensureNonEmptyFile(outPath); err != nil {
		return 0, err
	}
	return cw.n, nil
}

func validateHostRelPath(cleanPath, tarRel string) error {
	if !filepath.IsAbs(cleanPath) || tarRel == "" || tarRel == "." || strings.HasPrefix(tarRel, "-") {
		return fmt.Errorf("unsafe host_file path %q", cleanPath)
	}
	return nil
}

func stackSnapshotSubpath(stackDir string) string {
	clean := filepath.Clean(stackDir)
	homePrefix := config.DefaultHomeBaseDir + string(filepath.Separator)
	if sub, ok := strings.CutPrefix(clean, homePrefix); ok {
		return sub
	}
	return filepath.Base(clean)
}

func ensureNonEmptyFile(path string) error {
	info, err := os.Stat(filepath.Clean(path))
	if err != nil {
		return fmt.Errorf("stat archive %q: %w", path, err)
	}
	if info.Size() == 0 {
		return fmt.Errorf("archive %q is unexpectedly empty", path)
	}
	return nil
}
