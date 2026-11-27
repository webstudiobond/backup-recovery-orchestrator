package config

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/webstudiobond/backup-recovery-orchestrator/internal/identifier"
)

func expandStackVars(out *StackConfig) error {
	if err := validateStackVars(out.Vars); err != nil {
		return err
	}

	pairs := make([]string, 0, (len(out.Vars)+1)*2)
	pairs = append(pairs, "{stack}", out.Name)
	for k, v := range out.Vars {
		pairs = append(pairs, "{"+k+"}", v)
	}
	replacer := strings.NewReplacer(pairs...)

	for i := range out.Files.Excludes {
		expanded, err := expandVarField(replacer, out.Files.Excludes[i], "files.excludes")
		if err != nil {
			return err
		}
		out.Files.Excludes[i] = expanded
	}
	for i := range out.DAC {
		expanded, err := expandVarField(replacer, out.DAC[i].Path, "dac.path")
		if err != nil {
			return err
		}
		out.DAC[i].Path = expanded
	}
	for i := range out.HostFiles {
		expanded, err := expandVarField(replacer, out.HostFiles[i].Path, "host_files.path")
		if err != nil {
			return err
		}
		out.HostFiles[i].Path = expanded
	}
	if err := expandHookPaths(replacer, out.Hooks.PreDump, "hooks.pre_dump"); err != nil {
		return err
	}
	if err := expandHookPaths(replacer, out.Hooks.PreSnapshot, "hooks.pre_snapshot"); err != nil {
		return err
	}
	if err := expandHookPaths(replacer, out.Hooks.PostSnapshot, "hooks.post_snapshot"); err != nil {
		return err
	}
	return expandRestoreStepPaths(replacer, out.RestoreSteps)
}

func expandHookPaths(replacer *strings.Replacer, steps []HookStep, field string) error {
	for i := range steps {
		if steps[i].Path == "" {
			continue
		}
		expanded, err := expandVarField(replacer, steps[i].Path, field)
		if err != nil {
			return err
		}
		steps[i].Path = expanded
	}
	return nil
}

func expandRestoreStepPaths(replacer *strings.Replacer, steps []RestoreStep) error {
	for i := range steps {
		if steps[i].From != "" {
			expanded, err := expandVarField(replacer, steps[i].From, "restore_steps.from")
			if err != nil {
				return err
			}
			steps[i].From = expanded
		}
		if steps[i].To != "" {
			expanded, err := expandVarField(replacer, steps[i].To, "restore_steps.to")
			if err != nil {
				return err
			}
			steps[i].To = expanded
		}
	}
	return nil
}

func expandVarField(replacer *strings.Replacer, raw, field string) (string, error) {
	expanded := replacer.Replace(raw)
	if hasUnresolvedPlaceholder(expanded) {
		return "", fmt.Errorf("unresolved variable placeholder in %s %q", field, expanded)
	}
	return expanded, nil
}

func hasUnresolvedPlaceholder(value string) bool {
	_, after, ok := strings.Cut(value, "{")
	return ok && strings.Contains(after, "}")
}

func validateStackVars(vars map[string]string) error {
	for key, val := range vars {
		if err := identifier.Validate(key, "vars key"); err != nil {
			return fmt.Errorf("validate vars key %q: %w", key, err)
		}
		if key == "stack" {
			return errors.New("vars key \"stack\" is reserved")
		}
		if err := validateVarValue(key, val); err != nil {
			return err
		}
	}
	return nil
}

func validateVarValue(key, val string) error {
	trimmed := strings.TrimSpace(val)
	if trimmed == "" || trimmed != val {
		return fmt.Errorf("vars[%q] must be non-empty without surrounding whitespace", key)
	}
	if strings.ContainsAny(val, "{}\r\n\t") {
		return fmt.Errorf("vars[%q] value %q contains forbidden characters", key, val)
	}
	if filepath.IsAbs(val) {
		return fmt.Errorf("vars[%q] value %q must be relative", key, val)
	}
	cleaned := filepath.Clean(val)
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		return fmt.Errorf("vars[%q] value %q escapes base path", key, val)
	}
	return nil
}
