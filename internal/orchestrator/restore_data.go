package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/webstudiobond/backup-recovery-orchestrator/internal/archive"
	"github.com/webstudiobond/backup-recovery-orchestrator/internal/config"
)

func defaultOsRemove(name string) error {
	if err := os.Remove(name); err != nil {
		return fmt.Errorf("remove %q: %w", name, err)
	}
	return nil
}

func defaultOsReadDir(name string) ([]os.DirEntry, error) {
	entries, err := os.ReadDir(name)
	if err != nil {
		return nil, fmt.Errorf("readdir %q: %w", name, err)
	}
	return entries, nil
}

func defaultOsLstat(name string) (os.FileInfo, error) {
	info, err := os.Lstat(name)
	if err != nil {
		return nil, fmt.Errorf("lstat %q: %w", name, err)
	}
	return info, nil
}

func defaultOsRename(oldpath, newpath string) error {
	if err := os.Rename(oldpath, newpath); err != nil {
		return fmt.Errorf("rename %q -> %q: %w", oldpath, newpath, err)
	}
	return nil
}

func defaultOsMkdirAll(path string, perm os.FileMode) error {
	if err := os.MkdirAll(path, perm); err != nil {
		return fmt.Errorf("mkdirall %q: %w", path, err)
	}
	return nil
}

var (
	filepathAbs = filepath.Abs
	osRemove    = defaultOsRemove
	osReadDir   = defaultOsReadDir
	osLstat     = defaultOsLstat
	osRename    = defaultOsRename
	osMkdirAll  = defaultOsMkdirAll
)

func (o *Orchestrator) restoreHostFiles(ctx context.Context, rctx *restoreExecutionContext) error {
	if len(rctx.hostFiles) == 0 {
		return nil
	}
	if rctx.manifest.HostFilesArchive == "" {
		return errors.New("manifest missing host_files_archive")
	}
	hostTar := filepath.Join(rctx.unpackDir, rctx.manifest.HostFilesArchive)
	if err := o.Archive.ExtractHostFiles(ctx, hostTar, rctx.hostFiles); err != nil {
		return fmt.Errorf("extract host_files: %w", err)
	}

	var hostConfigs []config.HostFileConfig
	for i := range rctx.manifest.HostFiles {
		hf := &rctx.manifest.HostFiles[i]
		if !hf.Present {
			continue
		}
		hostConfigs = append(hostConfigs, config.HostFileConfig{
			Path:     hf.Path,
			Owner:    hf.Owner,
			DirMode:  hf.DirMode,
			FileMode: hf.FileMode,
			Optional: hf.Optional,
		})
	}
	if _, err := o.DAC.ApplyHostFiles(ctx, rctx.manifest.User, hostConfigs, false); err != nil {
		return fmt.Errorf("apply host_files DAC: %w", err)
	}
	return nil
}

func moveStackContents(ctx context.Context, stackDir, fromRel, toRel string) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("context canceled before moving contents: %w", err)
	}

	absRoot, err := filepathAbs(filepath.Clean(stackDir))
	if err != nil {
		return fmt.Errorf("resolve stack_dir %q: %w", stackDir, err)
	}
	srcDir, err := resolveSafeStackSubdir(absRoot, fromRel)
	if err != nil {
		return fmt.Errorf("source path: %w", err)
	}
	dstDir, err := resolveSafeStackSubdir(absRoot, toRel)
	if err != nil {
		return fmt.Errorf("destination path: %w", err)
	}
	if srcDir == dstDir {
		return fmt.Errorf("source and destination must differ (%q)", fromRel)
	}
	if strings.HasPrefix(dstDir, srcDir+string(filepath.Separator)) {
		return fmt.Errorf("destination %q cannot reside inside source %q", toRel, fromRel)
	}

	srcInfo, err := osLstat(srcDir)
	if err != nil {
		return fmt.Errorf("stat source directory %q: %w", srcDir, err)
	}
	if srcInfo.Mode()&os.ModeSymlink != 0 || !srcInfo.IsDir() {
		return fmt.Errorf("source %q must be a real directory", srcDir)
	}

	if err := prepareMoveTargetDir(dstDir); err != nil {
		return err
	}
	if err := moveDirectoryEntries(ctx, srcDir, dstDir); err != nil {
		return err
	}

	if rmErr := osRemove(srcDir); rmErr != nil {
		return fmt.Errorf("remove empty source directory %q: %w", srcDir, rmErr)
	}
	return nil
}

func moveDirectoryEntries(ctx context.Context, srcDir, dstDir string) error {
	entries, err := osReadDir(srcDir)
	if err != nil {
		return fmt.Errorf("read source directory %q: %w", srcDir, err)
	}
	for _, entry := range entries {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return fmt.Errorf("context canceled during move_contents: %w", ctxErr)
		}
		srcEntry := filepath.Join(srcDir, entry.Name())
		dstEntry := filepath.Join(dstDir, entry.Name())
		if _, statErr := osLstat(dstEntry); statErr == nil {
			return fmt.Errorf("destination entry %q already exists", dstEntry)
		} else if !errors.Is(statErr, os.ErrNotExist) {
			return fmt.Errorf("stat destination entry %q: %w", dstEntry, statErr)
		}
		if renErr := osRename(srcEntry, dstEntry); renErr != nil {
			return fmt.Errorf("move %q to %q: %w", srcEntry, dstEntry, renErr)
		}
	}
	return nil
}

func resolveSafeStackSubdir(absRoot, rawRel string) (string, error) {
	trimmed := strings.TrimSpace(rawRel)
	if trimmed == "" || filepath.IsAbs(trimmed) {
		return "", fmt.Errorf("path %q must be non-empty and relative", rawRel)
	}
	candidate := filepath.Join(absRoot, filepath.Clean(trimmed))
	rel, err := filepathRel(absRoot, candidate)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q escapes stack_dir %q", rawRel, absRoot)
	}
	return candidate, nil
}

func prepareMoveTargetDir(dstDir string) error {
	dstInfo, err := osLstat(dstDir)
	if err == nil {
		if dstInfo.Mode()&os.ModeSymlink != 0 || !dstInfo.IsDir() {
			return fmt.Errorf("destination %q must be a real directory", dstDir)
		}
		return nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("stat destination directory %q: %w", dstDir, err)
	}
	if mkErr := osMkdirAll(dstDir, 0o700); mkErr != nil {
		return fmt.Errorf("create destination directory %q: %w", dstDir, mkErr)
	}
	return nil
}

func (o *Orchestrator) restoreDatabaseStep(ctx context.Context, rctx *restoreExecutionContext, databaseID string) error {
	if databaseID == "" {
		if len(rctx.manifest.Databases) == 0 {
			return errors.New("no databases recorded in manifest")
		}
		for i := range rctx.manifest.Databases {
			if err := o.restoreSingleManifestDB(ctx, rctx, &rctx.manifest.Databases[i]); err != nil {
				return err
			}
		}
		return nil
	}

	dbMeta, ok := findManifestDB(rctx.manifest.Databases, databaseID)
	if !ok {
		return fmt.Errorf("database_id %q not found in manifest", databaseID)
	}
	return o.restoreSingleManifestDB(ctx, rctx, &dbMeta)
}

func (o *Orchestrator) restoreSingleManifestDB(ctx context.Context, rctx *restoreExecutionContext, dbMeta *archive.ManifestDB) error {
	dbCfg := config.DatabaseConfig{
		ID:      dbMeta.ID,
		Driver:  dbMeta.Driver,
		Service: dbMeta.Service,
		Host:    dbMeta.Host,
		DBUser:  dbMeta.DBUser,
		DBName:  dbMeta.DBName,
		Secrets: dbMeta.Secrets,
	}
	dumpPath := filepath.Join(rctx.unpackDir, filepath.FromSlash(dbMeta.ArtifactPath))
	if err := o.Database.RestoreFromFile(ctx, rctx.manifest.ComposeFile, &dbCfg, dumpPath); err != nil {
		return fmt.Errorf("import database %q: %w", dbMeta.ID, err)
	}
	return nil
}

func findManifestDB(dbs []archive.ManifestDB, id string) (archive.ManifestDB, bool) {
	for i := range dbs {
		if dbs[i].ID == id {
			return dbs[i], true
		}
	}
	return archive.ManifestDB{}, false
}

func resolveHostFileTargets(items []archive.ManifestHostFile, mode string) ([]string, error) {
	switch mode {
	case "", RestoreModeNone:
		return nil, nil
	case RestoreModeAll:
		var rels []string
		for i := range items {
			if items[i].Present {
				rels = append(rels, items[i].TarPath)
			}
		}
		return rels, nil
	default:
		return nil, fmt.Errorf("unsupported host-files mode %q (expected 'none' or 'all')", mode)
	}
}
