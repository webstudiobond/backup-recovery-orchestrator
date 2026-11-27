package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/webstudiobond/backup-recovery-orchestrator/internal/archive"
	"github.com/webstudiobond/backup-recovery-orchestrator/internal/config"
	"github.com/webstudiobond/backup-recovery-orchestrator/internal/storage"
	"github.com/webstudiobond/backup-recovery-orchestrator/internal/validator"
)

// BackupOptions controls target selection, offsite sync skipping, and dry-run planning for Backup.
type BackupOptions struct {
	Out    io.Writer
	Stacks []string
	NoSync bool
	DryRun bool
}

// Backup runs the sequential backup -> sync -> local_prune pipeline across selected stacks, continuing past isolated errors.
func (o *Orchestrator) Backup(ctx context.Context, bundle *validator.Bundle, opts *BackupOptions) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("context canceled before backup: %w", err)
	}
	if bundle == nil {
		return errors.New("configuration bundle must not be nil")
	}
	if opts == nil {
		opts = &BackupOptions{}
	}
	out := resolveWriter(opts.Out)
	rawBundle := bundle.Config()

	targets, err := selectStacks(rawBundle, opts.Stacks)
	if err != nil {
		return err
	}

	if opts.DryRun {
		printBackupDryRun(out, rawBundle, targets, opts.NoSync)
		if len(opts.Stacks) == 0 {
			return o.SelfBackup(ctx, bundle, &SelfBackupOptions{
				Out:    out,
				NoSync: opts.NoSync,
				DryRun: true,
			})
		}
		return nil
	}

	if err := pruneStaleWorkDirs(backupWorkDir(rawBundle.Global.BackupRoot)); err != nil {
		return fmt.Errorf("prepare backup work directory: %w", err)
	}

	reverseProxyStack := rawBundle.ReverseProxyStack()
	var stackErrs []error
	for i := range targets {
		if stackErr := o.backupSingleStack(ctx, out, &rawBundle.Global, reverseProxyStack, &targets[i], opts.NoSync); stackErr != nil {
			wrapped := fmt.Errorf("backup stack %q: %w", targets[i].Name, stackErr)
			writeLine(out, fmt.Sprintf("[backup-error] stack=%s err=%v", targets[i].Name, stackErr))
			stackErrs = append(stackErrs, wrapped)
			if ctx.Err() != nil {
				break
			}
		}
	}
	if len(opts.Stacks) == 0 && ctx.Err() == nil {
		if selfErr := o.SelfBackup(ctx, bundle, &SelfBackupOptions{
			Out:    out,
			NoSync: opts.NoSync,
		}); selfErr != nil {
			writeLine(out, fmt.Sprintf("[backup-error] stack=%s err=%v", config.ReservedSelfBackupName, selfErr))
			stackErrs = append(stackErrs, fmt.Errorf("backup %s: %w", config.ReservedSelfBackupName, selfErr))
		}
	}
	return errors.Join(stackErrs...)
}

func (o *Orchestrator) backupSingleStack(ctx context.Context, out io.Writer, global *config.GlobalConfig, reverseProxyStack, stack *config.StackConfig, noSync bool) error {
	if err := o.LVM.CheckFreeSpace(ctx, global.BackupRoot, stack.MinFreeGB); err != nil {
		return fmt.Errorf("check free space: %w", err)
	}

	nowFn := o.Now
	if nowFn == nil {
		nowFn = defaultOrchestratorNow
	}
	backupNow, stamp := resolveBackupTime(nowFn(), global.Timezone)
	archiveName := fmt.Sprintf("%s_%s.tar", stack.Name, stamp)
	localStackDir := filepath.Join(global.BackupRoot, stack.Name)
	targetTarPath := filepath.Join(localStackDir, archiveName)

	if err := os.MkdirAll(localStackDir, 0o700); err != nil {
		return fmt.Errorf("create local backup directory %q: %w", localStackDir, err)
	}

	stagingDir, mkErr := createWorkSubdir(backupWorkDir(global.BackupRoot), fmt.Sprintf("%s%s-*", stageDirPrefix, stack.Name))
	if mkErr != nil {
		return fmt.Errorf("create staging directory: %w", mkErr)
	}

	manifest, stageErr := o.stageStackBackup(ctx, reverseProxyStack, stack, stagingDir, backupNow, stamp)
	if stageErr != nil {
		rmErr := os.RemoveAll(stagingDir)
		return errors.Join(stageErr, wrapRemoveErr(rmErr, stagingDir))
	}

	packErr := o.Archive.AssembleContainer(ctx, &manifest, stagingDir, targetTarPath)
	rmErr := os.RemoveAll(stagingDir)
	if combined := errors.Join(packErr, wrapRemoveErr(rmErr, stagingDir)); combined != nil {
		return combined
	}

	writeLine(out, fmt.Sprintf("[backup] stack=%s archive=%s", stack.Name, targetTarPath))

	var syncErrs []error
	if !noSync {
		syncErrs = o.syncArchiveAcrossDestinations(ctx, out, global.Destinations, stack, targetTarPath)
	}

	pruned, pruneErr := storage.PruneLocal(ctx, localStackDir, stack.Name, stack.Retention.LocalCount)
	if pruneErr != nil {
		syncErrs = append(syncErrs, fmt.Errorf("prune local backups: %w", pruneErr))
	} else if len(pruned) > 0 {
		writeLine(out, fmt.Sprintf("[prune] stack=%s local_removed=%d", stack.Name, len(pruned)))
	}

	return errors.Join(syncErrs...)
}

func (o *Orchestrator) stageStackBackup(
	ctx context.Context,
	reverseProxyStack *config.StackConfig,
	stack *config.StackConfig,
	stagingDir string,
	backupNow time.Time,
	stamp string,
) (archive.Manifest, error) {
	if err := o.Hooks.RunPhase(ctx, "pre_dump", stack.StackDir, stack.ComposeFile, stack.Hooks.PreDump); err != nil {
		return archive.Manifest{}, fmt.Errorf("run pre_dump hooks: %w", err)
	}

	manifestDBs, dbErr := o.dumpAllDatabases(ctx, stack, stagingDir, stamp)
	if dbErr != nil {
		return archive.Manifest{}, dbErr
	}

	if err := o.Hooks.RunPhase(ctx, "pre_snapshot", stack.StackDir, stack.ComposeFile, stack.Hooks.PreSnapshot); err != nil {
		return archive.Manifest{}, fmt.Errorf("run pre_snapshot hooks: %w", err)
	}

	filesRel := archive.StackFilesArchiveName(stack.Name, stamp)
	filesOut := filepath.Join(stagingDir, filesRel)
	filesUncompressed, snapErr := o.snapshotAndArchiveFiles(ctx, stack, filesOut)
	if snapErr != nil {
		return archive.Manifest{}, snapErr
	}

	hostRel := archive.StackHostFilesArchiveName(stack.Name, stamp)
	hostOut := filepath.Join(stagingDir, hostRel)
	hostRecords, hostUncompressed, hostErr := o.Archive.ArchiveHostFiles(ctx, stack.HostFiles, stack.ZstdLevel, hostOut)
	if hostErr != nil {
		return archive.Manifest{}, fmt.Errorf("archive host_files: %w", hostErr)
	}
	if !hasPresentHostFiles(hostRecords) {
		hostRel = ""
	}

	if reverseProxyErr := archive.StageReverseProxyAssets(ctx, reverseProxyStack, &stack.ReverseProxy, stagingDir); reverseProxyErr != nil {
		return archive.Manifest{}, fmt.Errorf("stage reverse-proxy assets: %w", reverseProxyErr)
	}

	return archive.Manifest{
		SchemaVersion:              archive.ManifestSchemaVersion,
		CreatedAt:                  backupNow.Format(time.RFC3339),
		StackName:                  stack.Name,
		StackDir:                   stack.StackDir,
		ComposeFile:                stack.ComposeFile,
		FilesArchive:               filesRel,
		HostFilesArchive:           hostRel,
		FilesUncompressedBytes:     filesUncompressed,
		HostFilesUncompressedBytes: hostUncompressed,
		User:                       stack.User,
		Databases:                  manifestDBs,
		ReverseProxy:               stack.ReverseProxy,
		HostFiles:                  hostRecords,
		DAC:                        stack.DAC,
		RestoreSteps:               stack.RestoreSteps,
	}, nil
}

func hasPresentHostFiles(records []archive.ManifestHostFile) bool {
	for i := range records {
		if records[i].Present {
			return true
		}
	}
	return false
}

func (o *Orchestrator) dumpAllDatabases(ctx context.Context, stack *config.StackConfig, stagingDir, stamp string) ([]archive.ManifestDB, error) {
	if len(stack.Databases) == 0 {
		return nil, nil
	}

	manifestDBs := make([]archive.ManifestDB, 0, len(stack.Databases))
	for idx := range stack.Databases {
		db := &stack.Databases[idx]
		relArtifact := archive.StackDBArchiveName(stack.Name, db.ID, stamp, len(stack.Databases))
		outPath := filepath.Join(stagingDir, relArtifact)
		uncompressedBytes, err := o.Database.DumpToFile(ctx, stack.ComposeFile, db, stack.ZstdLevel, outPath)
		if err != nil {
			return nil, fmt.Errorf("dump database %q: %w", db.ID, err)
		}
		manifestDBs = append(manifestDBs, archive.ManifestDB{
			ID:                db.ID,
			Driver:            db.Driver,
			Service:           db.Service,
			Host:              db.Host,
			DBUser:            db.DBUser,
			DBName:            db.DBName,
			Secrets:           db.Secrets,
			ArtifactPath:      relArtifact,
			UncompressedBytes: uncompressedBytes,
		})
	}
	return manifestDBs, nil
}

func (o *Orchestrator) snapshotAndArchiveFiles(ctx context.Context, stack *config.StackConfig, filesOut string) (filesUncompressed int64, err error) {
	if createErr := o.LVM.CreateSnapshot(ctx, &stack.LVM); createErr != nil {
		return 0, fmt.Errorf("create snapshot: %w", createErr)
	}
	defer func() {
		if cleanErr := o.LVM.CleanupSnapshot(ctx, &stack.LVM); cleanErr != nil {
			err = errors.Join(err, fmt.Errorf("cleanup snapshot: %w", cleanErr))
		}
	}()

	if hookErr := o.Hooks.RunPhase(ctx, "post_snapshot", stack.StackDir, stack.ComposeFile, stack.Hooks.PostSnapshot); hookErr != nil {
		return 0, fmt.Errorf("run post_snapshot hooks: %w", hookErr)
	}

	filesUncompressed, archErr := o.Archive.ArchiveStackFiles(ctx, stack.LVM.MountPoint, stack.StackDir, &stack.Files, stack.ZstdLevel, filesOut)
	if archErr != nil {
		return 0, fmt.Errorf("archive snapshot files: %w", archErr)
	}
	return filesUncompressed, nil
}

func printBackupDryRun(out io.Writer, bundle *config.Bundle, targets []config.StackConfig, noSync bool) {
	writeLine(out, fmt.Sprintf("Dry-run backup plan (%d stack(s), backup_root=%s, no_sync=%t):", len(targets), bundle.Global.BackupRoot, noSync))
	for i := range targets {
		s := &targets[i]
		destSummary := formatStackDestinations(bundle.Global.Destinations, s)
		writeLine(out, fmt.Sprintf("  - stack=%s dir=%s dbs=%d host_files=%d reverse_proxy_domains=%d reverse_proxy_streams=%d reverse_proxy_certs=%d zstd=%d local_keep=%d destinations=[%s]",
			s.Name, s.StackDir, len(s.Databases), len(s.HostFiles), len(s.ReverseProxy.DomainConfs), len(s.ReverseProxy.StreamConfs), len(s.ReverseProxy.ACMENames),
			s.ZstdLevel, s.Retention.LocalCount, destSummary))
	}
}

func formatStackDestinations(all []config.DestinationConfig, stack *config.StackConfig) string {
	var parts []string
	for i := range all {
		dest := &all[i]
		if !stackTargetsDestination(stack, dest.Name) {
			continue
		}
		parts = append(parts, fmt.Sprintf("%s(%s:keep=%d)", dest.Name, dest.Type, stack.RemoteCountFor(dest)))
	}
	return strings.Join(parts, ", ")
}

func resolveBackupTime(base time.Time, tz string) (t time.Time, stamp string) {
	if tz == config.TimezoneUTC {
		utc := base.UTC()
		return utc, utc.Format(config.ArchiveTimestampLayout) + "_UTC"
	}
	return base, base.Format(config.ArchiveTimestampLayout)
}
