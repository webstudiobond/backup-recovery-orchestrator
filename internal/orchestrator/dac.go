package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/webstudiobond/backup-recovery-orchestrator/internal/config"
	"github.com/webstudiobond/backup-recovery-orchestrator/internal/dac"
)

// DACOptions configures standalone DAC enforcement for a single named stack without an archive.
type DACOptions struct {
	Out    io.Writer
	Stack  string
	DryRun bool
}

// ApplyDAC verifies the host stack user and applies configured DAC and host_files permissions to a live stack directory.
func (o *Orchestrator) ApplyDAC(ctx context.Context, bundle *config.Bundle, opts *DACOptions) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("context canceled before applying DAC: %w", err)
	}
	if bundle == nil {
		return errors.New("configuration bundle must not be nil")
	}
	if opts == nil || strings.TrimSpace(opts.Stack) == "" {
		return errors.New("stack name is required")
	}

	stackName := strings.TrimSpace(opts.Stack)
	stack, ok := bundle.StackByName(stackName)
	if !ok {
		return fmt.Errorf("stack %q not found in configuration", stackName)
	}
	if err := o.verifyLiveStackPrereqs(ctx, &stack); err != nil {
		return err
	}

	out := resolveWriter(opts.Out)
	return o.applyStackLiveDAC(ctx, &stack, out, opts.DryRun)
}

func (o *Orchestrator) verifyLiveStackPrereqs(ctx context.Context, stack *config.StackConfig) error {
	if err := o.SysUser.VerifyExistingUser(ctx, stack.User); err != nil {
		return fmt.Errorf("stack %q user check failed: %w", stack.Name, err)
	}
	info, statErr := os.Stat(stack.StackDir)
	if statErr != nil {
		return fmt.Errorf("stack %q directory %q check failed: %w", stack.Name, stack.StackDir, statErr)
	}
	if !info.IsDir() {
		return fmt.Errorf("stack %q path %q is not a directory", stack.Name, stack.StackDir)
	}
	return nil
}

func (o *Orchestrator) applyStackLiveDAC(ctx context.Context, stack *config.StackConfig, out io.Writer, dryRun bool) error {
	stackChanges, stackErr := o.DAC.ApplyStack(ctx, stack.StackDir, stack.User, stack.DAC, dryRun)
	if stackErr != nil {
		return fmt.Errorf("stack %q DAC failed: %w", stack.Name, stackErr)
	}

	hostChanges, hostErr := o.DAC.ApplyHostFiles(ctx, stack.User, stack.HostFiles, dryRun)
	if hostErr != nil {
		return fmt.Errorf("stack %q host_files DAC failed: %w", stack.Name, hostErr)
	}

	combined := make([]dac.Change, 0, len(stackChanges)+len(hostChanges))
	combined = append(combined, stackChanges...)
	combined = append(combined, hostChanges...)
	collapsed := dac.CollapseChanges(combined)

	printDACReport(out, stack, collapsed, dryRun)
	return nil
}

func printDACReport(out io.Writer, stack *config.StackConfig, changes []dac.Change, dryRun bool) {
	if dryRun {
		writeLine(out, fmt.Sprintf("Dry-run DAC plan for stack %q (stack_dir=%s, user=%s uid=%d gid=%d):",
			stack.Name, stack.StackDir, stack.User.Name, stack.User.UID, stack.User.GID))
	} else {
		writeLine(out, fmt.Sprintf("Applied DAC for stack %q (stack_dir=%s, user=%s uid=%d gid=%d):",
			stack.Name, stack.StackDir, stack.User.Name, stack.User.UID, stack.User.GID))
	}

	verb := "UPDATED"
	if dryRun {
		verb = "CHANGE"
	}

	var changedCount, unchangedCount, skippedCount int
	for i := range changes {
		c := &changes[i]
		if c.Skipped {
			skippedCount++
			writeLine(out, fmt.Sprintf("  SKIP %s (%s)", c.Path, c.SkipCause))
			continue
		}
		if c.NeedsUpdate() {
			changedCount++
			writeLine(out, fmt.Sprintf("  %s %s: %s", verb, c.Path, formatDACDiff(c)))
			continue
		}
		unchangedCount++
	}

	writeLine(out, fmt.Sprintf("  summary: total=%d changed=%d unchanged=%d skipped=%d",
		len(changes), changedCount, unchangedCount, skippedCount))
}

func formatDACDiff(c *dac.Change) string {
	var parts []string
	if c.HasOwner && (!c.HasPrev || c.PrevUID != c.UID || c.PrevGID != c.GID) {
		if c.HasPrev {
			parts = append(parts, fmt.Sprintf("owner %d:%d -> %d:%d", c.PrevUID, c.PrevGID, c.UID, c.GID))
		} else {
			parts = append(parts, fmt.Sprintf("owner -> %d:%d", c.UID, c.GID))
		}
	}
	if c.HasMode && (!c.HasPrev || c.PrevMode != c.Mode) {
		if c.HasPrev {
			parts = append(parts, fmt.Sprintf("mode %04o -> %04o", c.PrevMode, c.Mode))
		} else {
			parts = append(parts, fmt.Sprintf("mode -> %04o", c.Mode))
		}
	}
	return strings.Join(parts, ", ")
}
