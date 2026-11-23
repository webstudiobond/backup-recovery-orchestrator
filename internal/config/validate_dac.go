package config

import (
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
)

func validateDACRules(rules []DACRule) error {
	for idx := range rules {
		rule := &rules[idx]
		if strings.TrimSpace(rule.Path) == "" {
			return fmt.Errorf("dac[%d] path must not be empty", idx)
		}
		if filepath.IsAbs(rule.Path) {
			return fmt.Errorf("dac[%d] path %q must be relative to stack_dir", idx, rule.Path)
		}
		cleaned := filepath.Clean(rule.Path)
		if cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
			return fmt.Errorf("dac[%d] path %q escapes stack_dir", idx, rule.Path)
		}
		if err := validateDACAttributes(fmt.Sprintf("dac[%d]", idx), rule.Owner, rule.DirMode, rule.FileMode, true); err != nil {
			return err
		}
	}
	return nil
}

func validateHostFiles(items []HostFileConfig) error {
	seen := make(map[string]struct{}, len(items))
	for idx := range items {
		item := &items[idx]
		if !filepath.IsAbs(item.Path) {
			return fmt.Errorf("host_files[%d] path %q must be absolute", idx, item.Path)
		}
		cleaned := filepath.Clean(item.Path)
		if isProtectedHostPath(cleaned) {
			return fmt.Errorf("host_files[%d] path %q targets a protected system location", idx, item.Path)
		}
		if _, dup := seen[cleaned]; dup {
			return fmt.Errorf("host_files[%d] duplicate path %q", idx, cleaned)
		}
		seen[cleaned] = struct{}{}

		if err := validateDACAttributes(fmt.Sprintf("host_files[%d]", idx), item.Owner, item.DirMode, item.FileMode, false); err != nil {
			return err
		}
	}
	return nil
}

func isProtectedHostPath(cleanPath string) bool {
	switch cleanPath {
	case "/", "/etc", "/usr", "/usr/local", "/usr/local/bin", "/usr/local/sbin",
		"/var", "/boot", "/proc", "/sys", "/dev", "/run",
		"/etc/passwd", "/etc/shadow", "/etc/group", "/etc/gshadow", "/etc/sudoers", "/etc/sudoers.d":
		return true
	default:
		return strings.HasPrefix(cleanPath, "/etc/sudoers.d/")
	}
}

func validateDACAttributes(label, owner, dirMode, fileMode string, requireOne bool) error {
	if requireOne && owner == "" && dirMode == "" && fileMode == "" {
		return fmt.Errorf("%s must specify owner, dir_mode, or file_mode", label)
	}
	if owner != "" {
		if err := validateOwnerSpec(owner); err != nil {
			return fmt.Errorf("%s invalid owner %q: %w", label, owner, err)
		}
	}
	if dirMode != "" {
		if err := validateOctalMode(dirMode); err != nil {
			return fmt.Errorf("%s invalid dir_mode %q: %w", label, dirMode, err)
		}
	}
	if fileMode != "" {
		if err := validateOctalMode(fileMode); err != nil {
			return fmt.Errorf("%s invalid file_mode %q: %w", label, fileMode, err)
		}
	}
	return nil
}

func validateOwnerSpec(spec string) error {
	parts := strings.Split(spec, ":")
	if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
		return errors.New("expected 'user:group'")
	}
	return nil
}

func validateOctalMode(mode string) error {
	parsed, err := strconv.ParseUint(mode, 8, 32)
	if err != nil {
		return fmt.Errorf("parse octal mode: %w", err)
	}
	if parsed > 0o7777 {
		return errors.New("mode exceeds 07777")
	}
	return nil
}
