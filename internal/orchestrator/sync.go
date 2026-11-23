package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/webstudiobond/backup-recovery-orchestrator/internal/config"
)

// SyncOptions controls stack and destination selection for standalone offsite synchronization.
type SyncOptions struct {
	Out         io.Writer
	Destination string
	Stacks      []string
}

// Sync pushes existing local archives to configured destinations and enforces per-destination retention.
func (o *Orchestrator) Sync(ctx context.Context, bundle *config.Bundle, opts *SyncOptions) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("context canceled before sync: %w", err)
	}
	if opts == nil {
		opts = &SyncOptions{}
	}
	out := resolveWriter(opts.Out)

	targets, err := selectStacks(bundle, opts.Stacks)
	if err != nil {
		return err
	}
	dests, destErr := selectDestinations(bundle.Global.Destinations, opts.Destination)
	if destErr != nil {
		return destErr
	}

	var syncErrs []error
	syncAll := len(opts.Stacks) == 0
	for dIdx := range dests {
		dest := &dests[dIdx]
		destErrs := o.syncDestinationStacks(ctx, out, bundle.Global.BackupRoot, dest, targets, opts.Destination != "", syncAll)
		syncErrs = append(syncErrs, destErrs...)
		if ctx.Err() != nil {
			break
		}
	}
	return errors.Join(syncErrs...)
}

func (o *Orchestrator) syncDestinationStacks(
	ctx context.Context,
	out io.Writer,
	backupRoot string,
	dest *config.DestinationConfig,
	targets []config.StackConfig,
	explicitDest bool,
	includeSelf bool,
) []error {
	var errs []error
	destActive := false

	for sIdx := range targets {
		stack := &targets[sIdx]
		if !explicitDest && !stackTargetsDestination(stack, dest.Name) {
			continue
		}
		destActive = true
		localStackDir := filepath.Join(backupRoot, stack.Name)
		remoteKeep := stack.RemoteCountFor(dest)
		res, syncErr := o.Storage.SyncStack(ctx, dest, localStackDir, stack.Name, remoteKeep)
		if syncErr != nil {
			writeLine(out, fmt.Sprintf("[sync-error] dest=%s stack=%s err=%v", dest.Name, stack.Name, syncErr))
			errs = append(errs, fmt.Errorf("sync stack %q to %q: %w", stack.Name, dest.Name, syncErr))
			continue
		}
		writeLine(out, fmt.Sprintf("[sync] dest=%s stack=%s copied=%d skipped=%d pruned=%d", dest.Name, stack.Name, len(res.Copied), len(res.Skipped), len(res.Pruned)))
	}

	if includeSelf {
		localSelfDir := filepath.Join(backupRoot, config.ReservedSelfBackupName)
		if info, statErr := os.Stat(localSelfDir); statErr == nil && info.IsDir() {
			destActive = true
			res, syncErr := o.Storage.SyncStack(ctx, dest, localSelfDir, config.ReservedSelfBackupName, selfBackupRemoteKeep)
			if syncErr != nil {
				writeLine(out, fmt.Sprintf("[sync-error] dest=%s stack=%s err=%v", dest.Name, config.ReservedSelfBackupName, syncErr))
				errs = append(errs, fmt.Errorf("sync %s to %q: %w", config.ReservedSelfBackupName, dest.Name, syncErr))
			} else {
				writeLine(out, fmt.Sprintf("[sync] dest=%s stack=%s copied=%d skipped=%d pruned=%d", dest.Name, config.ReservedSelfBackupName, len(res.Copied), len(res.Skipped), len(res.Pruned)))
			}
		}
	}

	if destActive && len(errs) == 0 {
		if cleanErr := o.Storage.CleanupRemote(ctx, dest); cleanErr != nil {
			errs = append(errs, fmt.Errorf("cleanup destination %q: %w", dest.Name, cleanErr))
		}
	}
	return errs
}

func (o *Orchestrator) syncArchiveAcrossDestinations(
	ctx context.Context,
	out io.Writer,
	allDests []config.DestinationConfig,
	stack *config.StackConfig,
	targetTarPath string,
) []error {
	var syncErrs []error
	for dIdx := range allDests {
		dest := &allDests[dIdx]
		if !stackTargetsDestination(stack, dest.Name) {
			continue
		}
		if err := o.syncSingleArchiveToDest(ctx, out, dest, stack.Name, targetTarPath, stack.RemoteCountFor(dest)); err != nil {
			syncErrs = append(syncErrs, err)
		}
	}
	return syncErrs
}

func (o *Orchestrator) syncSingleArchiveToDest(
	ctx context.Context,
	out io.Writer,
	dest *config.DestinationConfig,
	stackName, targetTarPath string,
	remoteKeep int,
) error {
	res, syncErr := o.Storage.SyncArchive(ctx, dest, stackName, targetTarPath, remoteKeep)
	if syncErr != nil {
		writeLine(out, fmt.Sprintf("[sync-error] dest=%s stack=%s err=%v", dest.Name, stackName, syncErr))
		return fmt.Errorf("offsite sync to %q failed: %w", dest.Name, syncErr)
	}
	writeLine(out, fmt.Sprintf("[sync] dest=%s stack=%s copied=%d skipped=%d pruned=%d", dest.Name, stackName, len(res.Copied), len(res.Skipped), len(res.Pruned)))

	if cleanErr := o.Storage.CleanupRemote(ctx, dest); cleanErr != nil {
		writeLine(out, fmt.Sprintf("[sync-error] dest=%s stack=%s cleanup_err=%v", dest.Name, stackName, cleanErr))
		return fmt.Errorf("rclone cleanup on %q: %w", dest.Name, cleanErr)
	}
	return nil
}
