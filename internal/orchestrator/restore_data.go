package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/webstudiobond/backup-recovery-orchestrator/internal/archive"
	"github.com/webstudiobond/backup-recovery-orchestrator/internal/config"
)

var filepathAbs = filepath.Abs

func (o *Orchestrator) restoreHostFiles(ctx context.Context, rctx *restoreExecutionContext) error {
	if len(rctx.hostFiles) == 0 {
		return nil
	}
	if rctx.manifest.HostFilesArchive == "" {
		return errors.New("manifest missing host_files_archive")
	}
	hostTar := filepath.Join(rctx.unpackDir, rctx.manifest.HostFilesArchive)
	limit := effectiveInnerExtractLimit(rctx.manifest.HostFilesUncompressedBytes, rctx.global)
	if err := o.Archive.ExtractHostFilesWithLimits(ctx, hostTar, rctx.hostFiles, limit); err != nil {
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

func moveStackContents(ctx context.Context, stackDir, fromRel, toRel string) (err error) {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return fmt.Errorf("context canceled before moving contents: %w", ctxErr)
	}

	absRoot, absErr := filepathAbs(filepath.Clean(stackDir))
	if absErr != nil {
		return fmt.Errorf("resolve stack_dir %q: %w", stackDir, absErr)
	}
	srcRel, srcPathErr := resolveSafeStackSubdir(absRoot, fromRel)
	if srcPathErr != nil {
		return fmt.Errorf("source path: %w", srcPathErr)
	}
	dstRel, dstPathErr := resolveSafeStackSubdir(absRoot, toRel)
	if dstPathErr != nil {
		return fmt.Errorf("destination path: %w", dstPathErr)
	}
	if srcRel == dstRel {
		return fmt.Errorf("source and destination must differ (%q)", fromRel)
	}
	if strings.HasPrefix(dstRel, srcRel+"/") {
		return fmt.Errorf("destination %q cannot reside inside source %q", toRel, fromRel)
	}

	stackRoot, openErr := osOpenRoot(absRoot)
	if openErr != nil {
		return fmt.Errorf("open stack_dir %q: %w", absRoot, openErr)
	}
	defer func() {
		if closeErr := rootClose(stackRoot); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("close stack_dir %q: %w", absRoot, closeErr))
		}
	}()

	srcRoot, srcErr := openStackDirNoSymlink(stackRoot, srcRel, false)
	if srcErr != nil {
		return srcErr
	}
	srcInfo, srcStatErr := rootStat(srcRoot, ".")
	if srcStatErr != nil {
		return errors.Join(fmt.Errorf("stat source directory %q: %w", srcRel, srcStatErr), rootClose(srcRoot))
	}
	dstRoot, dstErr := openStackDirNoSymlink(stackRoot, dstRel, true)
	if dstErr != nil {
		return errors.Join(dstErr, rootClose(srcRoot))
	}

	moveErr := moveDirectoryEntries(ctx, stackRoot, srcRoot, dstRoot, srcRel, dstRel)
	closeErr := errors.Join(rootClose(srcRoot), rootClose(dstRoot))
	if combined := errors.Join(moveErr, closeErr); combined != nil {
		return combined
	}

	return removeEmptyStackDir(stackRoot, srcRel, srcInfo)
}

func moveDirectoryEntries(ctx context.Context, stackRoot, srcRoot, dstRoot *os.Root, srcRel, dstRel string) error {
	entries, err := rootReadDir(srcRoot)
	if err != nil {
		return fmt.Errorf("read source directory %q: %w", srcRel, err)
	}
	for _, entry := range entries {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return fmt.Errorf("context canceled during move_contents: %w", ctxErr)
		}
		name := entry.Name()
		if _, statErr := rootLstat(dstRoot, name); statErr == nil {
			return fmt.Errorf("destination entry %q already exists", path.Join(dstRel, name))
		} else if !errors.Is(statErr, os.ErrNotExist) {
			return fmt.Errorf("stat destination entry %q: %w", path.Join(dstRel, name), statErr)
		}
		if renErr := rootRename(stackRoot, srcRoot, dstRoot, srcRel, dstRel, name); renErr != nil {
			return fmt.Errorf("move %q to %q: %w", path.Join(srcRel, name), path.Join(dstRel, name), renErr)
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
	return filepath.ToSlash(rel), nil
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
	limit := effectiveInnerExtractLimit(dbMeta.UncompressedBytes, rctx.global)
	if err := o.Database.RestoreFromFileWithLimit(ctx, rctx.manifest.ComposeFile, &dbCfg, dumpPath, limit); err != nil {
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
