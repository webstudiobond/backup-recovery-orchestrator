package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/webstudiobond/backup-recovery-orchestrator/internal/archive"
	"github.com/webstudiobond/backup-recovery-orchestrator/internal/config"
	"github.com/webstudiobond/backup-recovery-orchestrator/internal/storage"
)

const (
	defaultSystemdDir    = "/etc/systemd/system"
	selfBackupLocalKeep  = 3
	selfBackupRemoteKeep = 7
)

// SelfBackupOptions controls destination sync skipping and dry-run planning for SelfBackup.
type SelfBackupOptions struct {
	Out    io.Writer
	NoSync bool
	DryRun bool
}

// SelfBackup archives bro configuration and bro-related systemd units into a reference-only container.
func (o *Orchestrator) SelfBackup(ctx context.Context, bundle *config.Bundle, opts *SelfBackupOptions) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("context canceled before selfbackup: %w", err)
	}
	if bundle == nil {
		return errors.New("configuration bundle must not be nil")
	}
	if opts == nil {
		opts = &SelfBackupOptions{}
	}
	out := resolveWriter(opts.Out)

	items, err := o.collectSelfBackupItems(bundle)
	if err != nil {
		return err
	}
	if opts.DryRun {
		printSelfBackupDryRun(out, bundle, items, opts.NoSync)
		return nil
	}

	if err := pruneStaleWorkDirs(backupWorkDir(bundle.Global.BackupRoot)); err != nil {
		return fmt.Errorf("prepare selfbackup work directory: %w", err)
	}
	if err := o.LVM.CheckFreeSpace(ctx, bundle.Global.BackupRoot, bundle.Global.MinFreeGB); err != nil {
		return fmt.Errorf("check free space: %w", err)
	}

	nowFn := o.Now
	if nowFn == nil {
		nowFn = defaultOrchestratorNow
	}
	backupNow, stamp := resolveBackupTime(nowFn(), bundle.Global.Timezone)
	targetTarPath, localDir, packErr := o.buildSelfBackupContainer(ctx, &bundle.Global, items, backupNow, stamp)
	if packErr != nil {
		return packErr
	}

	writeLine(out, fmt.Sprintf("[backup] stack=%s archive=%s", config.ReservedSelfBackupName, targetTarPath))

	var syncErrs []error
	if !opts.NoSync {
		for dIdx := range bundle.Global.Destinations {
			dest := &bundle.Global.Destinations[dIdx]
			if syncErr := o.syncSingleArchiveToDest(ctx, out, dest, config.ReservedSelfBackupName, targetTarPath, selfBackupRemoteKeep); syncErr != nil {
				syncErrs = append(syncErrs, syncErr)
			}
		}
	}

	pruned, pruneErr := storage.PruneLocal(ctx, localDir, config.ReservedSelfBackupName, selfBackupLocalKeep)
	if pruneErr != nil {
		syncErrs = append(syncErrs, fmt.Errorf("prune local selfbackups: %w", pruneErr))
	} else if len(pruned) > 0 {
		writeLine(out, fmt.Sprintf("[prune] stack=%s local_removed=%d", config.ReservedSelfBackupName, len(pruned)))
	}

	return errors.Join(syncErrs...)
}

func (o *Orchestrator) buildSelfBackupContainer(
	ctx context.Context,
	global *config.GlobalConfig,
	items []config.HostFileConfig,
	backupNow time.Time,
	stamp string,
) (targetTarPath, localDir string, err error) {
	localDir = filepath.Join(global.BackupRoot, config.ReservedSelfBackupName)
	if err := os.MkdirAll(localDir, 0o700); err != nil {
		return "", "", fmt.Errorf("create local selfbackup directory %q: %w", localDir, err)
	}

	stagingDir, mkErr := createWorkSubdir(backupWorkDir(global.BackupRoot), stageDirPrefix+"self-*")
	if mkErr != nil {
		return "", "", fmt.Errorf("create selfbackup staging directory: %w", mkErr)
	}

	hostRel := archive.StackHostFilesArchiveName(config.ReservedSelfBackupName, stamp)
	hostOut := filepath.Join(stagingDir, hostRel)
	records, hostUncompressed, archErr := o.Archive.ArchiveHostFiles(ctx, items, global.ZstdLevel, hostOut)
	if archErr != nil {
		rmErr := os.RemoveAll(stagingDir)
		return "", "", errors.Join(fmt.Errorf("archive selfbackup paths: %w", archErr), wrapRemoveErr(rmErr, stagingDir))
	}

	manifest := archive.Manifest{
		SchemaVersion:              archive.ManifestSchemaVersion,
		Kind:                       archive.SelfBackupKind,
		CreatedAt:                  backupNow.Format(time.RFC3339),
		StackName:                  config.ReservedSelfBackupName,
		HostFilesArchive:           hostRel,
		HostFilesUncompressedBytes: hostUncompressed,
		HostFiles:                  records,
	}

	archiveName := fmt.Sprintf("%s_%s.tar", config.ReservedSelfBackupName, stamp)
	targetTarPath = filepath.Join(localDir, archiveName)
	packErr := o.Archive.AssembleContainer(ctx, &manifest, stagingDir, targetTarPath)
	rmErr := os.RemoveAll(stagingDir)
	if combined := errors.Join(packErr, wrapRemoveErr(rmErr, stagingDir)); combined != nil {
		return "", "", combined
	}
	return targetTarPath, localDir, nil
}

func (o *Orchestrator) collectSelfBackupItems(bundle *config.Bundle) ([]config.HostFileConfig, error) {
	cfgDir := strings.TrimSpace(bundle.ConfigDir)
	if cfgDir == "" {
		cfgDir = filepath.Dir(config.DefaultConfigPath)
	}

	systemdDir := strings.TrimSpace(o.SystemdDir)
	if systemdDir == "" {
		systemdDir = defaultSystemdDir
	}

	items := []config.HostFileConfig{{Path: filepath.Clean(cfgDir)}}
	seen := []string{items[0].Path}

	entries, readErr := os.ReadDir(systemdDir)
	if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
		return nil, fmt.Errorf("read systemd unit directory %q: %w", systemdDir, readErr)
	}
	for _, entry := range entries {
		if !entry.Type().IsRegular() || !strings.HasPrefix(entry.Name(), "bro") {
			continue
		}
		unitPath := filepath.Join(systemdDir, entry.Name())
		if !slices.Contains(seen, unitPath) {
			seen = append(seen, unitPath)
			items = append(items, config.HostFileConfig{Path: unitPath, Optional: true})
		}
	}

	for i := range bundle.Global.Destinations {
		unit := strings.TrimSpace(bundle.Global.Destinations[i].SystemdUnit)
		if unit == "" {
			continue
		}
		for _, candidate := range destinationUnitCandidates(unit) {
			unitPath := filepath.Join(systemdDir, candidate)
			if !slices.Contains(seen, unitPath) {
				seen = append(seen, unitPath)
				items = append(items, config.HostFileConfig{Path: unitPath, Optional: true})
			}
		}
	}

	return items, nil
}

func destinationUnitCandidates(unit string) []string {
	base := filepath.Base(unit)
	if stem, ok := strings.CutSuffix(base, ".mount"); ok {
		return []string{base, stem + ".automount"}
	}
	if stem, ok := strings.CutSuffix(base, ".automount"); ok {
		return []string{base, stem + ".mount"}
	}
	return []string{base}
}

func printSelfBackupDryRun(out io.Writer, bundle *config.Bundle, items []config.HostFileConfig, noSync bool) {
	paths := make([]string, 0, len(items))
	for i := range items {
		paths = append(paths, items[i].Path)
	}
	destNames := make([]string, 0, len(bundle.Global.Destinations))
	for i := range bundle.Global.Destinations {
		dest := &bundle.Global.Destinations[i]
		destNames = append(destNames, fmt.Sprintf("%s(%s:keep=%d)", dest.Name, dest.Type, selfBackupRemoteKeep))
	}
	writeLine(out, fmt.Sprintf("  - stack=%s kind=%s paths=%v zstd=%d local_keep=%d no_sync=%t destinations=[%s]",
		config.ReservedSelfBackupName, archive.SelfBackupKind, paths, bundle.Global.ZstdLevel, selfBackupLocalKeep, noSync, strings.Join(destNames, ", ")))
}
