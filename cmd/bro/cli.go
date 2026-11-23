package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/webstudiobond/backup-recovery-orchestrator/internal/cmdexec"
	"github.com/webstudiobond/backup-recovery-orchestrator/internal/lock"
)

func run(ctx context.Context, args []string, out, errOut io.Writer, runner cmdexec.Runner) error {
	if len(args) == 0 {
		printUsage(errOut)
		return errors.New("subcommand is required")
	}

	switch args[0] {
	case "-h", "--help", "help":
		printUsage(out)
		return nil
	case "version", "-v", "--version":
		writeFormatted(out, "bro version %s\n", AppVersion)
		return nil
	case "backup":
		return runBackupCmd(ctx, args[1:], out, errOut, runner)
	case "selfbackup":
		return runSelfBackupCmd(ctx, args[1:], out, errOut, runner)
	case "sync":
		return runSyncCmd(ctx, args[1:], out, errOut, runner)
	case "restore":
		return runRestoreCmd(ctx, args[1:], out, errOut, runner)
	case "dac":
		return runDACCmd(ctx, args[1:], out, errOut, runner)
	case "verify":
		return runVerifyCmd(ctx, args[1:], out, errOut)
	case "config":
		return runConfigCmd(ctx, args[1:], out, errOut)
	default:
		printUsage(errOut)
		return fmt.Errorf("unknown subcommand %q", args[0])
	}
}

type lockHandle interface {
	Release() error
}

func defaultLockAcquire(ctx context.Context, path string) (lockHandle, error) {
	held, err := lock.Acquire(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("acquire lock: %w", err)
	}
	return held, nil
}

var lockAcquire = defaultLockAcquire

func withExclusiveLock(ctx context.Context, lockPath string, errOut io.Writer, fn func() error) error {
	held, err := lockAcquire(ctx, lockPath)
	if err != nil {
		return err
	}
	runErr := fn()
	if relErr := held.Release(); relErr != nil {
		writeFormatted(errOut, "warning: release lock: %v\n", relErr)
	}
	return runErr
}

func splitCSV(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		trimmed := strings.TrimSpace(p)
		if trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func writeFormatted(w io.Writer, format string, args ...any) {
	if _, err := fmt.Fprintf(w, format, args...); err != nil {
		return
	}
}

func resolveArchiveArg(flagPath string, pos []string, cmd string) (string, error) {
	trimmed := strings.TrimSpace(flagPath)
	if trimmed != "" && len(pos) > 0 {
		return "", fmt.Errorf("%s cannot combine -f with positional <archive.tar>", cmd)
	}
	if trimmed != "" {
		return trimmed, nil
	}
	if len(pos) == 1 {
		return pos[0], nil
	}
	return "", fmt.Errorf("%s requires exactly one <archive.tar> argument (or -f <archive.tar>)", cmd)
}

func parseFlagsWithPositional(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	rem := args
	for len(rem) > 0 {
		if err := fs.Parse(rem); err != nil {
			return nil, fmt.Errorf("parse flags: %w", err)
		}
		rest := fs.Args()
		if len(rest) == 0 {
			break
		}
		consumed := len(rem) - len(rest)
		if consumed > 0 && rem[consumed-1] == "--" {
			pos = append(pos, rest...)
			break
		}
		pos = append(pos, rest[0])
		rem = rest[1:]
	}
	return pos, nil
}

func printUsage(w io.Writer) {
	writeFormatted(w, `Usage: bro <subcommand> [options]

Subcommands:
  backup      [--config FILE] [--all | STACK...] [--no-sync] [--dry-run]
  selfbackup  [--config FILE] [--no-sync] [--dry-run]
  sync        [--config FILE] [--all | STACK...] [--destination NAME]
  restore     [--config FILE] [--dry-run] [--angie=none|all] [--angie-domains=...] [--angie-streams=...] [--angie-certs=...] [--host-files=none|all] [--skip-steps=...] [--only-steps=...] [-f FILE | <archive.tar>]
  dac         [--config FILE] [--dry-run] <stack>
  verify      [-f FILE | <archive.tar>]
  config      validate [--config FILE]
  config      show [--config FILE] <stack>
  version
`)
}
