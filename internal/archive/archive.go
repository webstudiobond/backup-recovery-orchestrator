// Package archive creates, checksum-verifies, and extracts modular uncompressed TAR backup containers.
package archive

import (
	"context"
	"errors"
	"fmt"
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

// ArchiveStackFiles creates a zstd-compressed tar archive of stackDir from inside the LVM snapshot mount.
func (e *Engine) ArchiveStackFiles(
	ctx context.Context,
	snapshotMount string,
	stackDir string,
	filesCfg *config.FilesConfig,
	zstdLevel int,
	outPath string,
) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("context canceled before archiving stack files: %w", err)
	}

	snapSourceDir := filepath.Join(filepath.Clean(snapshotMount), stackSnapshotSubpath(stackDir))
	info, statErr := os.Stat(snapSourceDir)
	if statErr != nil || !info.IsDir() {
		return fmt.Errorf("snapshot source directory %q not found: %w", snapSourceDir, statErr)
	}
	if mkErr := os.MkdirAll(filepath.Dir(outPath), 0o700); mkErr != nil {
		return fmt.Errorf("create archive staging directory: %w", mkErr)
	}

	args := make([]string, 0, 11+len(filesCfg.Excludes))
	args = append(args, "--wildcards")
	if filesCfg.NoWildcardsMatchSlash {
		args = append(args, "--no-wildcards-match-slash")
	}
	args = append(args, "-capf", outPath, "--numeric-owner")
	if filesCfg.ExcludesFile != "" {
		args = append(args, "--exclude-from="+filesCfg.ExcludesFile)
	}
	for _, pat := range filesCfg.Excludes {
		if strings.TrimSpace(pat) == "" {
			continue
		}
		args = append(args, "--exclude="+pat)
	}
	args = append(args, "-C", snapSourceDir, ".")

	if runErr := e.runTar(ctx, []string{fmt.Sprintf("ZSTD_CLEVEL=%d", zstdLevel)}, args); runErr != nil {
		return fmt.Errorf("archive stack files from %q: %w", snapSourceDir, runErr)
	}
	return ensureNonEmptyFile(outPath)
}

// ArchiveHostFiles captures configured host paths outside stack_dir into outPath relative to root.
func (e *Engine) ArchiveHostFiles(
	ctx context.Context,
	items []config.HostFileConfig,
	zstdLevel int,
	outPath string,
) ([]ManifestHostFile, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("context canceled before archiving host_files: %w", err)
	}
	if len(items) == 0 {
		return nil, nil
	}

	records := make([]ManifestHostFile, 0, len(items))
	var existingRel []string

	for i := range items {
		item := &items[i]
		cleanPath := filepath.Clean(item.Path)
		tarRel := strings.TrimPrefix(cleanPath, string(filepath.Separator))

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
			return nil, fmt.Errorf("stat host_file %q: %w", cleanPath, statErr)
		}

		rec.Present = true
		records = append(records, rec)
		existingRel = append(existingRel, tarRel)
	}

	if len(existingRel) == 0 {
		return records, nil
	}
	if mkErr := os.MkdirAll(filepath.Dir(outPath), 0o700); mkErr != nil {
		return nil, fmt.Errorf("create host_files archive directory: %w", mkErr)
	}

	args := make([]string, 0, 6+len(existingRel))
	args = append(args, "-capf", outPath, "--numeric-owner", "-C", string(filepath.Separator))
	args = append(args, existingRel...)

	if runErr := e.runTar(ctx, []string{fmt.Sprintf("ZSTD_CLEVEL=%d", zstdLevel)}, args); runErr != nil {
		return nil, fmt.Errorf("archive host_files: %w", runErr)
	}
	if err := ensureNonEmptyFile(outPath); err != nil {
		return nil, err
	}
	return records, nil
}

func (e *Engine) runTar(ctx context.Context, env, args []string) error {
	if err := e.Runner.Run(ctx, &cmdexec.Spec{
		Name: "tar",
		Args: args,
		Env:  env,
	}); err != nil {
		return fmt.Errorf("tar %s: %w", strings.Join(args, " "), err)
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
