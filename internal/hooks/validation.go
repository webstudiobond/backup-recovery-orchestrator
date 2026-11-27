package hooks

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/webstudiobond/backup-recovery-orchestrator/internal/config"
)

// ValidateHooks validates every configured hook step across pre-dump, pre-snapshot, and post-snapshot phases.
func ValidateHooks(hooks *config.HooksConfig) error {
	if hooks == nil {
		return nil
	}
	for i := range hooks.PreDump {
		if err := validateHookStep("pre_dump", &hooks.PreDump[i]); err != nil {
			return err
		}
	}
	for i := range hooks.PreSnapshot {
		if err := validateHookStep("pre_snapshot", &hooks.PreSnapshot[i]); err != nil {
			return err
		}
	}
	for i := range hooks.PostSnapshot {
		if err := validateHookStep("post_snapshot", &hooks.PostSnapshot[i]); err != nil {
			return err
		}
	}
	return nil
}

func validateHookStep(phase string, step *config.HookStep) error {
	switch step.Action {
	case hookActionRemovePath, hookActionWaitForFile:
		if !filepath.IsAbs(step.Path) {
			return fmt.Errorf("%s hook action %q requires an absolute path", phase, step.Action)
		}
		if step.Action == hookActionWaitForFile && step.TimeoutSec <= 0 {
			return fmt.Errorf("%s wait_for_file hook requires positive timeout_sec", phase)
		}
	case hookActionComposeKill:
		if strings.TrimSpace(step.Service) == "" || strings.TrimSpace(step.Signal) == "" {
			return fmt.Errorf("%s compose_kill hook requires service and signal", phase)
		}
	case hookActionComposeExec, hookActionComposeRun:
		if strings.TrimSpace(step.Service) == "" || len(step.Command) == 0 {
			return fmt.Errorf("%s %s hook requires service and command", phase, step.Action)
		}
	default:
		return fmt.Errorf("%s hook has unsupported action %q", phase, step.Action)
	}
	return nil
}
