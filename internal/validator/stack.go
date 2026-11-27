package validator

import (
	"fmt"
	"path/filepath"

	"github.com/webstudiobond/backup-recovery-orchestrator/internal/config"
	"github.com/webstudiobond/backup-recovery-orchestrator/internal/dac"
	"github.com/webstudiobond/backup-recovery-orchestrator/internal/database"
	"github.com/webstudiobond/backup-recovery-orchestrator/internal/hooks"
	"github.com/webstudiobond/backup-recovery-orchestrator/internal/identifier"
	"github.com/webstudiobond/backup-recovery-orchestrator/internal/lvm"
	"github.com/webstudiobond/backup-recovery-orchestrator/internal/reverseproxy"
	"github.com/webstudiobond/backup-recovery-orchestrator/internal/storage"
)

func validateStackConfig(cfg *config.StackConfig, knownDests map[string]struct{}) error {
	if err := validateStackIdentityAndPaths(cfg); err != nil {
		return err
	}
	if err := validateZstdLevel(cfg.ZstdLevel); err != nil {
		return err
	}
	if err := lvm.ValidateConfig(&cfg.LVM); err != nil {
		return fmt.Errorf("validate stack lvm: %w", err)
	}
	if err := storage.ValidateRetention(cfg.Retention, knownDests); err != nil {
		return fmt.Errorf("validate stack retention: %w", err)
	}
	if err := storage.ValidateStackDestinations(cfg.Destinations, knownDests); err != nil {
		return fmt.Errorf("validate stack destinations: %w", err)
	}
	if err := reverseproxy.ValidateStackAssets(&cfg.ReverseProxy); err != nil {
		return fmt.Errorf("validate reverse_proxy assets: %w", err)
	}
	if cfg.ReverseProxyProvider != nil {
		if err := reverseproxy.ValidateProviderConfig(cfg.ReverseProxyProvider); err != nil {
			return fmt.Errorf("validate reverse_proxy_provider: %w", err)
		}
	}
	if err := dac.ValidateRules(cfg.DAC); err != nil {
		return fmt.Errorf("validate dac: %w", err)
	}
	if err := dac.ValidateHostFiles(cfg.HostFiles); err != nil {
		return fmt.Errorf("validate host_files: %w", err)
	}
	if err := database.ValidateDatabases(cfg.Databases); err != nil {
		return fmt.Errorf("validate databases: %w", err)
	}
	if err := hooks.ValidateHooks(&cfg.Hooks); err != nil {
		return fmt.Errorf("validate hooks: %w", err)
	}
	return validateRestoreSteps(cfg.RestoreSteps, cfg.Databases)
}

func validateStackIdentityAndPaths(cfg *config.StackConfig) error {
	if err := identifier.Validate(cfg.Name, "stack name"); err != nil {
		return fmt.Errorf("validate stack name: %w", err)
	}
	if cfg.Name == config.ReservedSelfBackupName {
		return fmt.Errorf("stack name %q is reserved for selfbackup", cfg.Name)
	}
	if err := identifier.Validate(cfg.User.Name, "user name"); err != nil {
		return fmt.Errorf("validate user name: %w", err)
	}
	if !filepath.IsAbs(cfg.StackDir) {
		return fmt.Errorf("stack_dir %q must be an absolute path", cfg.StackDir)
	}
	if !filepath.IsAbs(cfg.ComposeFile) {
		return fmt.Errorf("compose_file %q must be an absolute path", cfg.ComposeFile)
	}
	return nil
}
