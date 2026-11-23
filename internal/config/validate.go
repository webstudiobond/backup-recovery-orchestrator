package config

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

func validateGlobalConfig(cfg *GlobalConfig) error {
	if !filepath.IsAbs(cfg.BackupRoot) {
		return fmt.Errorf("backup_root %q must be an absolute path", cfg.BackupRoot)
	}
	if !filepath.IsAbs(cfg.LockFile) {
		return fmt.Errorf("lock_file %q must be an absolute path", cfg.LockFile)
	}
	if err := validateZstdLevel(cfg.ZstdLevel); err != nil {
		return err
	}
	if err := validateTimezone(cfg.Timezone); err != nil {
		return err
	}
	if err := validateLVMConfig(&cfg.LVM); err != nil {
		return err
	}
	if err := validateDestinations(cfg.Destinations); err != nil {
		return err
	}

	knownDests := make(map[string]struct{}, len(cfg.Destinations))
	for i := range cfg.Destinations {
		knownDests[cfg.Destinations[i].Name] = struct{}{}
	}
	if err := validateRetention(cfg.Retention, knownDests); err != nil {
		return err
	}
	return validateGlobalAngie(&cfg.Angie)
}

func validateTimezone(tz string) error {
	switch tz {
	case TimezoneLocal, TimezoneUTC:
		return nil
	default:
		return fmt.Errorf("timezone %q must be 'local' or 'utc'", tz)
	}
}

func validateGlobalAngie(angie *GlobalAngieConfig) error {
	if angie.ComposeFile != "" && !filepath.IsAbs(angie.ComposeFile) {
		return fmt.Errorf("angie.compose_file %q must be an absolute path", angie.ComposeFile)
	}
	if angie.CertsDir != "" && !filepath.IsAbs(angie.CertsDir) {
		return fmt.Errorf("angie.certs_dir %q must be an absolute path", angie.CertsDir)
	}
	if angie.DomainsDir != "" && !filepath.IsAbs(angie.DomainsDir) {
		return fmt.Errorf("angie.domains_dir %q must be an absolute path", angie.DomainsDir)
	}
	if angie.StreamDir != "" && !filepath.IsAbs(angie.StreamDir) {
		return fmt.Errorf("angie.stream_dir %q must be an absolute path", angie.StreamDir)
	}
	return nil
}

func validateStackConfig(cfg *StackConfig, knownDests map[string]struct{}) error {
	if err := validateIdentifier(cfg.Name, "stack name"); err != nil {
		return err
	}
	if cfg.Name == ReservedSelfBackupName {
		return fmt.Errorf("stack name %q is reserved for selfbackup", cfg.Name)
	}
	if err := validateIdentifier(cfg.User.Name, "user name"); err != nil {
		return err
	}
	if !filepath.IsAbs(cfg.StackDir) {
		return fmt.Errorf("stack_dir %q must be an absolute path", cfg.StackDir)
	}
	if !filepath.IsAbs(cfg.ComposeFile) {
		return fmt.Errorf("compose_file %q must be an absolute path", cfg.ComposeFile)
	}
	if err := validateZstdLevel(cfg.ZstdLevel); err != nil {
		return err
	}
	if err := validateLVMConfig(&cfg.LVM); err != nil {
		return err
	}
	if err := validateRetention(cfg.Retention, knownDests); err != nil {
		return err
	}
	if err := validateStackDestinations(cfg.Destinations, knownDests); err != nil {
		return err
	}
	if err := validateStackAngie(&cfg.Angie); err != nil {
		return err
	}
	if err := validateDACRules(cfg.DAC); err != nil {
		return err
	}
	if err := validateHostFiles(cfg.HostFiles); err != nil {
		return err
	}
	if err := validateDatabases(cfg.Databases); err != nil {
		return err
	}
	if err := validateHooks(&cfg.Hooks); err != nil {
		return err
	}
	return validateRestoreSteps(cfg.RestoreSteps, cfg.Databases)
}

func validateDestinations(dests []DestinationConfig) error {
	seen := make(map[string]struct{}, len(dests))
	for i := range dests {
		dest := &dests[i]
		if err := validateIdentifier(dest.Name, "destination name"); err != nil {
			return err
		}
		if _, dup := seen[dest.Name]; dup {
			return fmt.Errorf("duplicate destination name %q", dest.Name)
		}
		seen[dest.Name] = struct{}{}

		if err := validateSingleDestination(dest); err != nil {
			return err
		}
	}
	return nil
}

func validateSingleDestination(dest *DestinationConfig) error {
	switch dest.Type {
	case DestTypeFUSE, DestTypeMount:
		if !filepath.IsAbs(dest.MountPoint) {
			return fmt.Errorf("destination %q mount_point %q must be absolute", dest.Name, dest.MountPoint)
		}
		if !filepath.IsAbs(dest.BackupDir) {
			return fmt.Errorf("destination %q backup_dir %q must be absolute", dest.Name, dest.BackupDir)
		}
	case DestTypeDir:
		if !filepath.IsAbs(dest.BackupDir) {
			return fmt.Errorf("destination %q backup_dir %q must be absolute", dest.Name, dest.BackupDir)
		}
	case DestTypeRclone:
		if err := validateRcloneDestination(dest); err != nil {
			return err
		}
	default:
		return fmt.Errorf("destination %q type %q must be explicitly set to 'fuse', 'mount', 'dir', or 'rclone'", dest.Name, dest.Type)
	}

	if dest.WaitTimeoutSec < 0 {
		return fmt.Errorf("destination %q wait_timeout_sec must be non-negative", dest.Name)
	}
	if dest.RemoteCount != nil && (*dest.RemoteCount == 0 || *dest.RemoteCount < -1) {
		return fmt.Errorf("destination %q remote_count %d must be > 0 or -1 for unlimited", dest.Name, *dest.RemoteCount)
	}
	return nil
}

func validateRcloneDestination(dest *DestinationConfig) error {
	remote := strings.TrimSpace(dest.Remote)
	if remote == "" || !strings.Contains(remote, ":") {
		return fmt.Errorf("destination %q of type 'rclone' requires remote in 'name:path' format", dest.Name)
	}
	if strings.ContainsAny(remote, " \t\r\n") || strings.Contains(remote, "..") {
		return fmt.Errorf("destination %q remote %q contains unsafe characters", dest.Name, dest.Remote)
	}
	if dest.RcloneConfig != "" && !filepath.IsAbs(dest.RcloneConfig) {
		return fmt.Errorf("destination %q rclone_config %q must be absolute", dest.Name, dest.RcloneConfig)
	}
	return nil
}

func validateStackDestinations(stackDests []string, knownDests map[string]struct{}) error {
	for _, name := range stackDests {
		if err := validateIdentifier(name, "stack destinations entry"); err != nil {
			return err
		}
		if _, ok := knownDests[name]; !ok {
			return fmt.Errorf("stack destinations references unknown destination %q", name)
		}
	}
	return nil
}

func validateLVMConfig(lvm *LVMConfig) error {
	if err := validateIdentifier(lvm.VGName, "lvm.vg_name"); err != nil {
		return err
	}
	if err := validateIdentifier(lvm.LVName, "lvm.lv_name"); err != nil {
		return err
	}
	if err := validateIdentifier(lvm.SnapshotName, "lvm.snapshot_name"); err != nil {
		return err
	}
	if strings.TrimSpace(lvm.SnapshotSize) == "" {
		return errors.New("lvm.snapshot_size must not be empty")
	}
	if !filepath.IsAbs(lvm.MountPoint) {
		return fmt.Errorf("lvm.mount_point %q must be an absolute path", lvm.MountPoint)
	}
	return nil
}

func validateRetention(ret RetentionConfig, knownDests map[string]struct{}) error {
	if ret.LocalCount == 0 || ret.LocalCount < -1 {
		return fmt.Errorf("retention.local_count %d must be >= 1 or -1 for unlimited", ret.LocalCount)
	}
	if ret.RemoteCount == 0 || ret.RemoteCount < -1 {
		return fmt.Errorf("retention.remote_count %d must be > 0 or -1 for unlimited", ret.RemoteCount)
	}
	for destName, count := range ret.RemoteOverrides {
		if _, ok := knownDests[destName]; !ok {
			return fmt.Errorf("retention.remote_overrides references unknown destination %q", destName)
		}
		if count == 0 || count < -1 {
			return fmt.Errorf("retention.remote_overrides[%q] %d must be > 0 or -1 for unlimited", destName, count)
		}
	}
	return nil
}

func validateZstdLevel(level int) error {
	if level < 1 || level > 19 {
		return fmt.Errorf("zstd_level %d must be between 1 and 19", level)
	}
	return nil
}

func validateStackAngie(angie *StackAngieConfig) error {
	if err := validateIdentifierList(angie.ACMENames, "angie.acme_names entry"); err != nil {
		return err
	}
	if err := validateIdentifierList(angie.DomainConfs, "angie.domain_confs entry"); err != nil {
		return err
	}
	return validateIdentifierList(angie.StreamConfs, "angie.stream_confs entry")
}

func validateIdentifierList(items []string, field string) error {
	for _, item := range items {
		if err := validateIdentifier(item, field); err != nil {
			return err
		}
	}
	return nil
}

func validateStackVars(vars map[string]string) error {
	for key, val := range vars {
		if err := validateIdentifier(key, "vars key"); err != nil {
			return err
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

func validateIdentifier(value, label string) error {
	if value == "" {
		return fmt.Errorf("%s must not be empty", label)
	}
	if strings.HasPrefix(value, ".") || strings.Contains(value, "..") {
		return fmt.Errorf("%s %q must not start with '.' or contain '..'", label, value)
	}
	for _, r := range value {
		if !isAllowedIdentifierRune(r) {
			return fmt.Errorf("%s %q contains invalid character %q", label, value, r)
		}
	}
	return nil
}

func isAllowedIdentifierRune(r rune) bool {
	return (r >= 'a' && r <= 'z') ||
		(r >= 'A' && r <= 'Z') ||
		(r >= '0' && r <= '9') ||
		r == '_' || r == '-' || r == '.'
}
