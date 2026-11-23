package config

import (
	"fmt"
	"path/filepath"
	"strings"
)

func validateDatabases(dbs []DatabaseConfig) error {
	seen := make(map[string]struct{}, len(dbs))
	for i := range dbs {
		db := &dbs[i]
		if err := validateIdentifier(db.ID, "database id"); err != nil {
			return err
		}
		if _, dup := seen[db.ID]; dup {
			return fmt.Errorf("duplicate database id %q", db.ID)
		}
		seen[db.ID] = struct{}{}

		if err := validateDatabaseDriver(db); err != nil {
			return err
		}
		if err := validateDBSecrets(db.ID, db.Secrets); err != nil {
			return err
		}
	}
	return nil
}

func validateDatabaseDriver(db *DatabaseConfig) error {
	switch db.Driver {
	case "postgres", "mariadb", "wp-cli":
		if strings.TrimSpace(db.Service) == "" {
			return fmt.Errorf("database %q requires service for driver %q", db.ID, db.Driver)
		}
		return nil
	default:
		return fmt.Errorf("database %q has unsupported driver %q", db.ID, db.Driver)
	}
}

func validateDBSecrets(dbID string, secrets *DBSecretsConfig) error {
	if secrets == nil {
		return nil
	}
	switch secrets.Source {
	case "container", "host":
	default:
		return fmt.Errorf("database %q secrets.source %q must be 'container' or 'host'", dbID, secrets.Source)
	}
	if secrets.PasswordFile == "" && secrets.UserFile == "" && secrets.DBFile == "" {
		return fmt.Errorf("database %q secrets block must define at least one secret file path", dbID)
	}
	return nil
}

func validateHooks(hooks *HooksConfig) error {
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

func validateHookStep(phase string, step *HookStep) error {
	switch step.Action {
	case "remove_path", "wait_for_file":
		if !filepath.IsAbs(step.Path) {
			return fmt.Errorf("%s hook action %q requires an absolute path", phase, step.Action)
		}
		if step.Action == "wait_for_file" && step.TimeoutSec <= 0 {
			return fmt.Errorf("%s wait_for_file hook requires positive timeout_sec", phase)
		}
	case "compose_kill":
		if strings.TrimSpace(step.Service) == "" || strings.TrimSpace(step.Signal) == "" {
			return fmt.Errorf("%s compose_kill hook requires service and signal", phase)
		}
	case "compose_exec", "compose_run":
		if strings.TrimSpace(step.Service) == "" || len(step.Command) == 0 {
			return fmt.Errorf("%s %s hook requires service and command", phase, step.Action)
		}
	default:
		return fmt.Errorf("%s hook has unsupported action %q", phase, step.Action)
	}
	return nil
}

func validateRestoreSteps(steps []RestoreStep, dbs []DatabaseConfig) error {
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

func validateRestoreStep(idx int, step *RestoreStep, dbIDs map[string]struct{}) error {
	switch step.Action {
	case "compose_down", "extract_files", "apply_dac", "extract_host_files", "deploy_angie", "compose_up", "wait_healthy", "compose_restart":
		return nil
	case "move_contents":
		return validateMoveContentsStep(idx, step)
	case "db_import":
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
	case "compose_exec", "compose_run":
		if strings.TrimSpace(step.Service) == "" || (len(step.Command) == 0 && len(step.Args) == 0) {
			return fmt.Errorf("restore_steps[%d] %s requires service and command or args", idx, step.Action)
		}
		return nil
	default:
		return fmt.Errorf("restore_steps[%d] has unsupported action %q", idx, step.Action)
	}
}

func validateMoveContentsStep(idx int, step *RestoreStep) error {
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
