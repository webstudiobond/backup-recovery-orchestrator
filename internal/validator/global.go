package validator

import (
	"errors"
	"fmt"
	"path/filepath"

	"github.com/webstudiobond/backup-recovery-orchestrator/internal/config"
	"github.com/webstudiobond/backup-recovery-orchestrator/internal/lvm"
	"github.com/webstudiobond/backup-recovery-orchestrator/internal/reverseproxy"
	"github.com/webstudiobond/backup-recovery-orchestrator/internal/storage"
)

func validateGlobalConfig(cfg *config.GlobalConfig) (map[string]struct{}, error) {
	if !filepath.IsAbs(cfg.BackupRoot) {
		return nil, fmt.Errorf("backup_root %q must be an absolute path", cfg.BackupRoot)
	}
	if cfg.RestoreWorkDir != "" && (!filepath.IsAbs(cfg.RestoreWorkDir) || filepath.Clean(cfg.RestoreWorkDir) == string(filepath.Separator)) {
		return nil, fmt.Errorf("restore_work_dir %q must be an absolute non-root path", cfg.RestoreWorkDir)
	}
	if !filepath.IsAbs(cfg.LockFile) {
		return nil, fmt.Errorf("lock_file %q must be an absolute path", cfg.LockFile)
	}
	if cfg.MaxArchiveGB == 0 {
		return nil, errors.New("max_archive_gb must be greater than zero")
	}
	if err := validateZstdLevel(cfg.ZstdLevel); err != nil {
		return nil, err
	}
	if err := validateTimezone(cfg.Timezone); err != nil {
		return nil, err
	}
	if err := lvm.ValidateConfig(&cfg.LVM); err != nil {
		return nil, fmt.Errorf("validate global lvm: %w", err)
	}
	if err := storage.ValidateDestinations(cfg.Destinations); err != nil {
		return nil, fmt.Errorf("validate destinations: %w", err)
	}

	knownDests := make(map[string]struct{}, len(cfg.Destinations))
	for i := range cfg.Destinations {
		knownDests[cfg.Destinations[i].Name] = struct{}{}
	}
	if err := storage.ValidateRetention(cfg.Retention, knownDests); err != nil {
		return nil, fmt.Errorf("validate global retention: %w", err)
	}
	if err := reverseproxy.ValidateGlobalStackName(cfg.ReverseProxyStack); err != nil {
		return nil, fmt.Errorf("validate reverse_proxy_stack: %w", err)
	}
	return knownDests, nil
}

func validateTimezone(tz string) error {
	switch tz {
	case config.TimezoneLocal, config.TimezoneUTC:
		return nil
	default:
		return fmt.Errorf("timezone %q must be 'local' or 'utc'", tz)
	}
}

func validateZstdLevel(level int) error {
	if level < 1 || level > 19 {
		return fmt.Errorf("zstd_level %d must be between 1 and 19", level)
	}
	return nil
}
