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
)

// Restore mode constants control auxiliary asset deployment strategies for Angie and host-level files during recovery.
const (
	RestoreModeNone = "none"
	RestoreModeAll  = "all"
)

// RestoreOptions configures single-stack point-in-time disaster recovery and explicit Angie/host asset selection.
type RestoreOptions struct {
	Out           io.Writer
	AngieStack    *config.StackConfig
	ArchivePath   string
	AngieMode     string
	HostFilesMode string
	AngieDomains  []string
	AngieStreams  []string
	AngieCerts    []string
	SkipSteps     []string
	OnlySteps     []string
	DryRun        bool
}

type restoreExecutionContext struct {
	manifest   *archive.Manifest
	global     *config.GlobalConfig
	angieStack *config.StackConfig
	out        io.Writer
	unpackDir  string
	angieSel   angieSelection
	hostFiles  []string
}

// Restore verifies a single TAR backup container, checks UID/GID safety, and executes declarative restore_steps.
func (o *Orchestrator) Restore(ctx context.Context, global *config.GlobalConfig, opts *RestoreOptions) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("context canceled before restore: %w", err)
	}
	if global == nil {
		return errors.New("global config must not be nil")
	}
	if opts == nil || strings.TrimSpace(opts.ArchivePath) == "" {
		return errors.New("archive path is required for restore")
	}
	out := resolveWriter(opts.Out)

	limits := archive.ContainerLimitsFromGB(global.MaxArchiveGB)
	manifest, verifyErr := archive.VerifyContainerWithLimits(ctx, opts.ArchivePath, limits)
	if verifyErr != nil {
		return fmt.Errorf("verify archive %q: %w", opts.ArchivePath, verifyErr)
	}
	if manifest.Kind == archive.SelfBackupKind {
		return errors.New("self-backup archives are reference-only and cannot be restored via 'bro restore'")
	}

	angieSel, selErr := resolveAngieSelection(&manifest.Angie, opts.AngieMode, opts.AngieDomains, opts.AngieStreams, opts.AngieCerts)
	if selErr != nil {
		return selErr
	}

	selectedHostFiles, hostErr := resolveHostFileTargets(manifest.HostFiles, opts.HostFilesMode)
	if hostErr != nil {
		return hostErr
	}

	steps := filterRestoreSteps(effectiveRestoreSteps(&manifest), opts.OnlySteps, opts.SkipSteps)
	if len(steps) == 0 {
		return errors.New("no restore steps selected after filtering")
	}

	userStatus, userErr := o.SysUser.EnsureUser(ctx, manifest.User, manifest.StackDir, opts.DryRun)
	if userErr != nil {
		return fmt.Errorf("system user validation failed: %w", userErr)
	}

	if opts.DryRun {
		printRestoreDryRun(out, &manifest, string(userStatus), angieSel, selectedHostFiles, steps)
		return nil
	}

	workDir, workErr := o.prepareRestoreWorkDir(ctx, global, opts.ArchivePath, manifest.StackDir, manifest.FilesUncompressedBytes)
	if workErr != nil {
		return workErr
	}

	unpackDir, mkErr := createWorkSubdir(workDir, fmt.Sprintf("%s%s-*", restoreDirPrefix, manifest.StackName))
	if mkErr != nil {
		return fmt.Errorf("create restore unpack directory: %w", mkErr)
	}

	rctx := &restoreExecutionContext{
		manifest:   &manifest,
		global:     global,
		angieStack: opts.AngieStack,
		out:        out,
		unpackDir:  unpackDir,
		angieSel:   angieSel,
		hostFiles:  selectedHostFiles,
	}
	execErr := o.runRestoreSequence(ctx, opts.ArchivePath, rctx, steps)
	rmErr := os.RemoveAll(unpackDir)
	return errors.Join(execErr, wrapRemoveErr(rmErr, unpackDir))
}

func (o *Orchestrator) prepareRestoreWorkDir(
	ctx context.Context,
	global *config.GlobalConfig,
	archivePath string,
	stackDir string,
	filesUncompressedBytes int64,
) (string, error) {
	workDir := resolveRestoreWorkDir(global)
	if err := validateRestoreWorkDirSeparation(workDir, stackDir); err != nil {
		return "", err
	}
	if err := pruneStaleWorkDirs(workDir); err != nil {
		return "", fmt.Errorf("prepare restore work directory: %w", err)
	}
	if err := o.checkRestoreFreeSpace(ctx, global, archivePath, workDir, stackDir, filesUncompressedBytes); err != nil {
		return "", err
	}
	return workDir, nil
}

func (o *Orchestrator) runRestoreSequence(
	ctx context.Context,
	archivePath string,
	rctx *restoreExecutionContext,
	steps []config.RestoreStep,
) error {
	limits := archive.ContainerLimitsFromGB(rctx.global.MaxArchiveGB)
	if err := archive.UnpackContainerWithLimits(ctx, archivePath, rctx.unpackDir, limits); err != nil {
		return fmt.Errorf("unpack container %q: %w", archivePath, err)
	}

	for idx := range steps {
		step := &steps[idx]
		writeLine(rctx.out, fmt.Sprintf("[restore %d/%d] action=%s", idx+1, len(steps), step.Action))
		if err := o.executeRestoreStep(ctx, rctx, step); err != nil {
			return fmt.Errorf("restore step[%d] (%s): %w", idx, step.Action, err)
		}
	}
	return nil
}

func (o *Orchestrator) executeRestoreStep(ctx context.Context, rctx *restoreExecutionContext, step *config.RestoreStep) error {
	if handled, err := o.executeDataOrInfraStep(ctx, rctx, step); handled {
		return err
	}
	if handled, err := o.executeComposeLifecycleStep(ctx, rctx.manifest.ComposeFile, step); handled {
		return err
	}
	return fmt.Errorf("unsupported restore step action %q", step.Action)
}

func (o *Orchestrator) executeDataOrInfraStep(ctx context.Context, rctx *restoreExecutionContext, step *config.RestoreStep) (bool, error) {
	switch step.Action {
	case "extract_files":
		if rctx.manifest.FilesArchive == "" {
			return true, errors.New("manifest missing files_archive")
		}
		filesTar := filepath.Join(rctx.unpackDir, rctx.manifest.FilesArchive)
		limit := effectiveInnerExtractLimit(rctx.manifest.FilesUncompressedBytes, rctx.global)
		if err := o.Archive.ExtractStackFilesCleanSlateWithLimits(ctx, filesTar, rctx.manifest.StackDir, limit); err != nil {
			return true, fmt.Errorf("extract stack files: %w", err)
		}
		return true, nil
	case "apply_dac":
		if len(rctx.manifest.DAC) == 0 {
			return true, nil
		}
		if _, err := o.DAC.ApplyStack(ctx, rctx.manifest.StackDir, rctx.manifest.User, rctx.manifest.DAC, false); err != nil {
			return true, fmt.Errorf("apply stack DAC: %w", err)
		}
		return true, nil
	case "extract_host_files":
		return true, o.restoreHostFiles(ctx, rctx)
	case "move_contents":
		return true, moveStackContents(ctx, rctx.manifest.StackDir, step.From, step.To)
	case "deploy_angie":
		return true, deployAngieWithRollback(ctx, o.Docker, o.DAC, &rctx.global.Angie, rctx.angieStack, resolveRestoreWorkDir(rctx.global), rctx.unpackDir, rctx.angieSel)
	case "db_import":
		return true, o.restoreDatabaseStep(ctx, rctx, step.DatabaseID)
	default:
		return false, nil
	}
}

func (o *Orchestrator) executeComposeLifecycleStep(ctx context.Context, composeFile string, step *config.RestoreStep) (bool, error) {
	switch step.Action {
	case "compose_down":
		if _, err := os.Stat(composeFile); errors.Is(err, os.ErrNotExist) {
			return true, nil
		}
		if err := o.Docker.Down(ctx, composeFile); err != nil {
			return true, fmt.Errorf("compose down: %w", err)
		}
		return true, nil
	case "compose_up":
		if err := o.Docker.Up(ctx, composeFile, step.Services...); err != nil {
			return true, fmt.Errorf("compose up: %w", err)
		}
		return true, nil
	case "wait_healthy":
		timeout := time.Duration(step.TimeoutSec) * time.Second
		if err := o.Docker.WaitHealthy(ctx, composeFile, step.Services, timeout); err != nil {
			return true, fmt.Errorf("wait healthy: %w", err)
		}
		return true, nil
	case "compose_restart":
		if err := o.Docker.Restart(ctx, composeFile, step.Services...); err != nil {
			return true, fmt.Errorf("compose restart: %w", err)
		}
		return true, nil
	case "compose_exec", "compose_run":
		return true, o.executeComposeRestoreCommand(ctx, composeFile, step)
	default:
		return false, nil
	}
}

func (o *Orchestrator) executeComposeRestoreCommand(ctx context.Context, composeFile string, step *config.RestoreStep) error {
	cmdArgs := step.Command
	if len(cmdArgs) == 0 {
		cmdArgs = step.Args
	}
	if step.Action == "compose_exec" {
		if err := o.Docker.Exec(ctx, composeFile, step.Service, nil, cmdArgs, nil, nil); err != nil {
			return fmt.Errorf("compose exec %s: %w", step.Service, err)
		}
		return nil
	}
	if err := o.Docker.Run(ctx, composeFile, step.Service, nil, cmdArgs, nil, nil); err != nil {
		return fmt.Errorf("compose run %s: %w", step.Service, err)
	}
	return nil
}

func effectiveRestoreSteps(manifest *archive.Manifest) []config.RestoreStep {
	if len(manifest.RestoreSteps) > 0 {
		return manifest.RestoreSteps
	}
	return []config.RestoreStep{
		{Action: "compose_down"},
		{Action: "extract_files"},
		{Action: "apply_dac"},
		{Action: "extract_host_files"},
		{Action: "deploy_angie"},
		{Action: "compose_up"},
		{Action: "wait_healthy"},
	}
}

func filterRestoreSteps(steps []config.RestoreStep, only, skip []string) []config.RestoreStep {
	filtered := make([]config.RestoreStep, 0, len(steps))
	for i := range steps {
		action := steps[i].Action
		if len(only) > 0 && !slices.Contains(only, action) {
			continue
		}
		if len(skip) > 0 && slices.Contains(skip, action) {
			continue
		}
		filtered = append(filtered, steps[i])
	}
	return filtered
}

func printRestoreDryRun(
	out io.Writer,
	manifest *archive.Manifest,
	userStatus string,
	angieSel angieSelection,
	hostFiles []string,
	steps []config.RestoreStep,
) {
	writeLine(out, fmt.Sprintf("Dry-run restore plan for stack %q (created_at=%s):", manifest.StackName, manifest.CreatedAt))
	writeLine(out, fmt.Sprintf("  user: %s (uid=%d, gid=%d, status=%s)", manifest.User.Name, manifest.User.UID, manifest.User.GID, userStatus))
	writeLine(out, "  stack_dir: "+manifest.StackDir)
	writeLine(out, fmt.Sprintf("  angie_domains: %v | angie_streams: %v | angie_certs: %v", angieSel.Domains, angieSel.Streams, angieSel.Certs))
	writeLine(out, fmt.Sprintf("  host_files: %v", hostFiles))
	writeLine(out, fmt.Sprintf("  dac_rules: %d", len(manifest.DAC)))
	writeLine(out, "  steps:")
	for idx := range steps {
		step := &steps[idx]
		writeLine(out, fmt.Sprintf("    %d. %s (service=%s services=%v db=%s)", idx+1, step.Action, step.Service, step.Services, step.DatabaseID))
	}
}

func effectiveInnerExtractLimit(declaredBytes int64, global *config.GlobalConfig) int64 {
	if declaredBytes > 0 {
		return declaredBytes
	}
	var maxArchiveGB uint64
	if global != nil {
		maxArchiveGB = global.MaxArchiveGB
	}
	return archive.ContainerLimitsFromGB(maxArchiveGB).MaxBytes
}
