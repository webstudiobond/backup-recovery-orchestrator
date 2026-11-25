package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/webstudiobond/backup-recovery-orchestrator/internal/archive"
	"github.com/webstudiobond/backup-recovery-orchestrator/internal/cmdexec"
	"github.com/webstudiobond/backup-recovery-orchestrator/internal/config"
	"github.com/webstudiobond/backup-recovery-orchestrator/internal/orchestrator"
)

var (
	jsonMarshalIndent       = json.MarshalIndent
	newOrchestrator         = orchestrator.New
	osStat                  = os.Stat
	defaultVerifyConfigPath = config.DefaultConfigPath
)

func runBackupCmd(ctx context.Context, args []string, out, errOut io.Writer, runner cmdexec.Runner) error {
	fs := flag.NewFlagSet("backup", flag.ContinueOnError)
	fs.SetOutput(errOut)
	cfgPath := fs.String("config", config.DefaultConfigPath, "path to global config.json")
	allFlag := fs.Bool("all", false, "back up all enabled stacks")
	noSync := fs.Bool("no-sync", false, "skip offsite FUSE synchronization")
	dryRun := fs.Bool("dry-run", false, "preview backup plan without modifying data")

	stacks, parseErr := parseFlagsWithPositional(fs, args)
	if parseErr != nil {
		return fmt.Errorf("parse backup flags: %w", parseErr)
	}
	if !*allFlag && len(stacks) == 0 {
		return errors.New("specify --all or one or more stack names")
	}
	if *allFlag && len(stacks) > 0 {
		return errors.New("cannot combine --all with explicit stack names")
	}

	bundle, err := config.LoadBundle(ctx, *cfgPath)
	if err != nil {
		return fmt.Errorf("load configuration: %w", err)
	}

	orch := newOrchestrator(runner)
	execFn := func() error {
		if runErr := orch.Backup(ctx, &bundle, &orchestrator.BackupOptions{
			Out:    out,
			Stacks: stacks,
			NoSync: *noSync,
			DryRun: *dryRun,
		}); runErr != nil {
			return fmt.Errorf("execute backup: %w", runErr)
		}
		return nil
	}

	if *dryRun {
		return execFn()
	}
	return withExclusiveLock(ctx, bundle.Global.LockFile, errOut, execFn)
}

func runSelfBackupCmd(ctx context.Context, args []string, out, errOut io.Writer, runner cmdexec.Runner) error {
	fs := flag.NewFlagSet("selfbackup", flag.ContinueOnError)
	fs.SetOutput(errOut)
	cfgPath := fs.String("config", config.DefaultConfigPath, "path to global config.json")
	noSync := fs.Bool("no-sync", false, "skip offsite FUSE synchronization")
	dryRun := fs.Bool("dry-run", false, "preview selfbackup plan without modifying data")

	pos, parseErr := parseFlagsWithPositional(fs, args)
	if parseErr != nil {
		return fmt.Errorf("parse selfbackup flags: %w", parseErr)
	}
	if len(pos) > 0 {
		return fmt.Errorf("selfbackup does not accept positional arguments: %v", pos)
	}

	bundle, err := config.LoadBundle(ctx, *cfgPath)
	if err != nil {
		return fmt.Errorf("load configuration: %w", err)
	}

	orch := newOrchestrator(runner)
	execFn := func() error {
		if selfErr := orch.SelfBackup(ctx, &bundle, &orchestrator.SelfBackupOptions{
			Out:    out,
			NoSync: *noSync,
			DryRun: *dryRun,
		}); selfErr != nil {
			return fmt.Errorf("execute selfbackup: %w", selfErr)
		}
		return nil
	}

	if *dryRun {
		return execFn()
	}
	return withExclusiveLock(ctx, bundle.Global.LockFile, errOut, execFn)
}

func runSyncCmd(ctx context.Context, args []string, out, errOut io.Writer, runner cmdexec.Runner) error {
	fs := flag.NewFlagSet("sync", flag.ContinueOnError)
	fs.SetOutput(errOut)
	cfgPath := fs.String("config", config.DefaultConfigPath, "path to global config.json")
	allFlag := fs.Bool("all", false, "synchronize all enabled stacks")
	destName := fs.String("destination", "", "optional single destination name")

	stacks, parseErr := parseFlagsWithPositional(fs, args)
	if parseErr != nil {
		return fmt.Errorf("parse sync flags: %w", parseErr)
	}
	if !*allFlag && len(stacks) == 0 {
		return errors.New("specify --all or one or more stack names")
	}
	if *allFlag && len(stacks) > 0 {
		return errors.New("cannot combine --all with explicit stack names")
	}

	bundle, err := config.LoadBundle(ctx, *cfgPath)
	if err != nil {
		return fmt.Errorf("load configuration: %w", err)
	}

	orch := newOrchestrator(runner)
	return withExclusiveLock(ctx, bundle.Global.LockFile, errOut, func() error {
		if syncErr := orch.Sync(ctx, &bundle, &orchestrator.SyncOptions{
			Out:         out,
			Destination: *destName,
			Stacks:      stacks,
		}); syncErr != nil {
			return fmt.Errorf("execute sync: %w", syncErr)
		}
		return nil
	})
}

func runRestoreCmd(ctx context.Context, args []string, out, errOut io.Writer, runner cmdexec.Runner) error {
	fs := flag.NewFlagSet("restore", flag.ContinueOnError)
	fs.SetOutput(errOut)
	cfgPath := fs.String("config", config.DefaultConfigPath, "path to global config.json")
	archiveFlag := fs.String("f", "", "path to backup archive (.tar)")
	dryRun := fs.Bool("dry-run", false, "validate archive and print restore plan without modifying host")
	angieMode := fs.String("angie", orchestrator.RestoreModeNone, "angie restore policy: none or all")
	angieDomains := fs.String("angie-domains", "", "comma-separated angie domain config filenames to restore")
	angieStreams := fs.String("angie-streams", "", "comma-separated angie stream config filenames to restore")
	angieCerts := fs.String("angie-certs", "", "comma-separated angie certificate directory names to restore")
	hostFilesMode := fs.String("host-files", orchestrator.RestoreModeNone, "host_files restore policy: none or all")
	skipSteps := fs.String("skip-steps", "", "comma-separated restore step actions to skip")
	onlySteps := fs.String("only-steps", "", "comma-separated restore step actions to run exclusively")

	pos, parseErr := parseFlagsWithPositional(fs, args)
	if parseErr != nil {
		return fmt.Errorf("parse restore flags: %w", parseErr)
	}

	archivePath, archErr := resolveArchiveArg(*archiveFlag, pos, "restore")
	if archErr != nil {
		return archErr
	}

	bundle, err := config.LoadBundle(ctx, *cfgPath)
	if err != nil {
		return fmt.Errorf("load configuration: %w", err)
	}

	orch := newOrchestrator(runner)
	execFn := func() error {
		if resErr := orch.Restore(ctx, &bundle.Global, &orchestrator.RestoreOptions{
			Out:           out,
			AngieStack:    bundle.AngieStack(),
			ArchivePath:   archivePath,
			AngieMode:     *angieMode,
			AngieDomains:  splitCSV(*angieDomains),
			AngieStreams:  splitCSV(*angieStreams),
			AngieCerts:    splitCSV(*angieCerts),
			HostFilesMode: *hostFilesMode,
			SkipSteps:     splitCSV(*skipSteps),
			OnlySteps:     splitCSV(*onlySteps),
			DryRun:        *dryRun,
		}); resErr != nil {
			return fmt.Errorf("execute restore: %w", resErr)
		}
		return nil
	}

	if *dryRun {
		return execFn()
	}
	return withExclusiveLock(ctx, bundle.Global.LockFile, errOut, execFn)
}

func runDACCmd(ctx context.Context, args []string, out, errOut io.Writer, runner cmdexec.Runner) error {
	fs := flag.NewFlagSet("dac", flag.ContinueOnError)
	fs.SetOutput(errOut)
	cfgPath := fs.String("config", config.DefaultConfigPath, "path to global config.json")
	dryRun := fs.Bool("dry-run", false, "preview DAC ownership and mode changes without modifying host")

	stacks, parseErr := parseFlagsWithPositional(fs, args)
	if parseErr != nil {
		return fmt.Errorf("parse dac flags: %w", parseErr)
	}
	if len(stacks) != 1 {
		return errors.New("dac requires exactly one <stack> argument")
	}

	bundle, err := config.LoadBundle(ctx, *cfgPath)
	if err != nil {
		return fmt.Errorf("load configuration: %w", err)
	}

	orch := newOrchestrator(runner)
	execFn := func() error {
		if dacErr := orch.ApplyDAC(ctx, &bundle, &orchestrator.DACOptions{
			Out:    out,
			Stack:  stacks[0],
			DryRun: *dryRun,
		}); dacErr != nil {
			return fmt.Errorf("execute dac: %w", dacErr)
		}
		return nil
	}

	if *dryRun {
		return execFn()
	}
	return withExclusiveLock(ctx, bundle.Global.LockFile, errOut, execFn)
}

func runVerifyCmd(ctx context.Context, args []string, out, errOut io.Writer) error {
	fs := flag.NewFlagSet("verify", flag.ContinueOnError)
	fs.SetOutput(errOut)
	cfgPath := fs.String("config", "", "optional path to global config.json (defaults to "+config.DefaultConfigPath+" if present)")
	archiveFlag := fs.String("f", "", "path to backup archive (.tar)")
	pos, parseErr := parseFlagsWithPositional(fs, args)
	if parseErr != nil {
		return fmt.Errorf("parse verify flags: %w", parseErr)
	}

	archivePath, archErr := resolveArchiveArg(*archiveFlag, pos, "verify")
	if archErr != nil {
		return archErr
	}

	limits, limErr := resolveVerifyLimits(ctx, *cfgPath)
	if limErr != nil {
		return limErr
	}

	manifest, err := archive.VerifyContainerWithLimits(ctx, archivePath, limits)
	if err != nil {
		return fmt.Errorf("verify container: %w", err)
	}

	kind := manifest.Kind
	if kind == "" {
		kind = "stack"
	}
	writeFormatted(out, "verified archive %s: kind=%s stack=%s created_at=%s artifacts=%d angie_domains=%v angie_streams=%v angie_certs=%v host_files=%d\n",
		archivePath, kind, manifest.StackName, manifest.CreatedAt, len(manifest.Artifacts),
		manifest.Angie.DomainConfs, manifest.Angie.StreamConfs, manifest.Angie.ACMENames, len(manifest.HostFiles))
	return nil
}

func resolveVerifyLimits(ctx context.Context, cfgPath string) (archive.ContainerLimits, error) {
	targetPath := cfgPath
	if targetPath == "" {
		_, statErr := osStat(defaultVerifyConfigPath)
		if errors.Is(statErr, os.ErrNotExist) {
			return archive.ContainerLimits{}, nil
		}
		if statErr != nil {
			return archive.ContainerLimits{}, fmt.Errorf("stat default configuration %q: %w", defaultVerifyConfigPath, statErr)
		}
		targetPath = defaultVerifyConfigPath
	}
	global, err := config.LoadGlobal(ctx, targetPath)
	if err != nil {
		return archive.ContainerLimits{}, fmt.Errorf("load configuration: %w", err)
	}
	return archive.ContainerLimitsFromGB(global.MaxArchiveGB), nil
}

func runConfigCmd(ctx context.Context, args []string, out, errOut io.Writer) error {
	if len(args) == 0 {
		return errors.New("config subcommand requires 'validate' or 'show'")
	}

	switch args[0] {
	case "validate":
		fs := flag.NewFlagSet("config validate", flag.ContinueOnError)
		fs.SetOutput(errOut)
		cfgPath := fs.String("config", config.DefaultConfigPath, "path to global config.json")
		if err := fs.Parse(args[1:]); err != nil {
			return fmt.Errorf("parse config validate flags: %w", err)
		}
		bundle, err := config.LoadBundle(ctx, *cfgPath)
		if err != nil {
			return fmt.Errorf("config validation failed: %w", err)
		}
		writeFormatted(out, "configuration valid: %d stack(s), %d destination(s)\n", len(bundle.Stacks), len(bundle.Global.Destinations))
		return nil

	case "show":
		fs := flag.NewFlagSet("config show", flag.ContinueOnError)
		fs.SetOutput(errOut)
		cfgPath := fs.String("config", config.DefaultConfigPath, "path to global config.json")
		pos, parseErr := parseFlagsWithPositional(fs, args[1:])
		if parseErr != nil {
			return fmt.Errorf("parse config show flags: %w", parseErr)
		}
		if len(pos) != 1 {
			return errors.New("config show requires exactly one <stack> argument")
		}
		bundle, err := config.LoadBundle(ctx, *cfgPath)
		if err != nil {
			return fmt.Errorf("load configuration: %w", err)
		}
		stack, ok := bundle.StackByName(pos[0])
		if !ok {
			return fmt.Errorf("stack %q not found", pos[0])
		}
		raw, err := jsonMarshalIndent(stack, "", "  ")
		if err != nil {
			return fmt.Errorf("marshal resolved config: %w", err)
		}
		writeFormatted(out, "%s\n", string(raw))
		return nil

	default:
		return fmt.Errorf("unknown config subcommand %q (expected 'validate' or 'show')", args[0])
	}
}
