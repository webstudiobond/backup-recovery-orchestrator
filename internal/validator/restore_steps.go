package validator

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/webstudiobond/backup-recovery-orchestrator/internal/config"
)

func validateRestoreSteps(steps []config.RestoreStep, dbs []config.DatabaseConfig) error {
	dbIDs := make(map[string]struct{}, len(dbs))
	for i := range dbs {
		dbIDs[dbs[i].ID] = struct{}{}
	}

	for idx := range steps {
		if err := validateRestoreStep(idx, &steps[idx], dbIDs); err != nil {
			return err
		}
	}
	return nil
}

func validateRestoreStep(idx int, step *config.RestoreStep, dbIDs map[string]struct{}) error {
	switch step.Action {
	case config.RestoreActionComposeDown, config.RestoreActionExtractFiles, config.RestoreActionApplyDAC,
		config.RestoreActionExtractHostFiles, config.RestoreActionDeployReverseProxy, config.RestoreActionComposeUp,
		config.RestoreActionWaitHealthy, config.RestoreActionComposeRestart:
		return nil
	case config.RestoreActionMoveContents:
		return validateMoveContentsStep(idx, step)
	case config.RestoreActionDBImport:
		if step.DatabaseID == "" {
			if len(dbIDs) == 0 {
				return fmt.Errorf("restore_steps[%d] db_import requires at least one configured database", idx)
			}
			return nil
		}
		if _, ok := dbIDs[step.DatabaseID]; !ok {
			return fmt.Errorf("restore_steps[%d] db_import references unknown database_id %q", idx, step.DatabaseID)
		}
		return nil
	case config.RestoreActionComposeExec, config.RestoreActionComposeRun:
		if strings.TrimSpace(step.Service) == "" || (len(step.Command) == 0 && len(step.Args) == 0) {
			return fmt.Errorf("restore_steps[%d] %s requires service and command or args", idx, step.Action)
		}
		return nil
	default:
		return fmt.Errorf("restore_steps[%d] has unsupported action %q", idx, step.Action)
	}
}

func validateMoveContentsStep(idx int, step *config.RestoreStep) error {
	cleanFrom, err := validateStackRelativeSubdir(idx, "from", step.From)
	if err != nil {
		return err
	}
	cleanTo, err := validateStackRelativeSubdir(idx, "to", step.To)
	if err != nil {
		return err
	}
	if cleanFrom == cleanTo {
		return fmt.Errorf("restore_steps[%d] move_contents from and to must differ (%q)", idx, cleanFrom)
	}
	if strings.HasPrefix(cleanTo, cleanFrom+string(filepath.Separator)) {
		return fmt.Errorf("restore_steps[%d] move_contents target %q cannot reside inside source %q", idx, cleanTo, cleanFrom)
	}
	return nil
}

func validateStackRelativeSubdir(idx int, field, raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", fmt.Errorf("restore_steps[%d] move_contents requires %s", idx, field)
	}
	if filepath.IsAbs(trimmed) {
		return "", fmt.Errorf("restore_steps[%d] move_contents %s %q must be relative to stack_dir", idx, field, raw)
	}
	cleaned := filepath.Clean(trimmed)
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("restore_steps[%d] move_contents %s %q escapes stack_dir", idx, field, raw)
	}
	return cleaned, nil
}
