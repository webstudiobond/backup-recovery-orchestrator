package storage

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/webstudiobond/backup-recovery-orchestrator/internal/config"
	"github.com/webstudiobond/backup-recovery-orchestrator/internal/identifier"
)

// ValidateDestinations verifies unique destination identifiers, explicit transport fields, and retention overrides.
func ValidateDestinations(destinations []config.DestinationConfig) error {
	seen := make(map[string]struct{}, len(destinations))
	for i := range destinations {
		dest := &destinations[i]
		if err := identifier.Validate(dest.Name, "destination name"); err != nil {
			return fmt.Errorf("validate destination name %q: %w", dest.Name, err)
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

// ValidateStackDestinations ensures every stack destination entry is a valid identifier referencing a configured destination.
func ValidateStackDestinations(stackDestinations []string, knownDestinations map[string]struct{}) error {
	for _, name := range stackDestinations {
		if err := identifier.Validate(name, "stack destinations entry"); err != nil {
			return fmt.Errorf("validate stack destination %q: %w", name, err)
		}
		if _, ok := knownDestinations[name]; !ok {
			return fmt.Errorf("stack destinations references unknown destination %q", name)
		}
	}
	return nil
}

// ValidateRetention verifies local and offsite retention counts as well as per-destination override keys.
func ValidateRetention(retention config.RetentionConfig, knownDestinations map[string]struct{}) error {
	if retention.LocalCount == 0 || retention.LocalCount < -1 {
		return fmt.Errorf("retention.local_count %d must be >= 1 or -1 for unlimited", retention.LocalCount)
	}
	if retention.RemoteCount == 0 || retention.RemoteCount < -1 {
		return fmt.Errorf("retention.remote_count %d must be > 0 or -1 for unlimited", retention.RemoteCount)
	}
	for destName, count := range retention.RemoteOverrides {
		if _, ok := knownDestinations[destName]; !ok {
			return fmt.Errorf("retention.remote_overrides references unknown destination %q", destName)
		}
		if count == 0 || count < -1 {
			return fmt.Errorf("retention.remote_overrides[%q] %d must be > 0 or -1 for unlimited", destName, count)
		}
	}
	return nil
}

func validateSingleDestination(dest *config.DestinationConfig) error {
	switch dest.Type {
	case config.DestTypeFUSE, config.DestTypeMount:
		if !filepath.IsAbs(dest.MountPoint) {
			return fmt.Errorf("destination %q mount_point %q must be absolute", dest.Name, dest.MountPoint)
		}
		if !filepath.IsAbs(dest.BackupDir) {
			return fmt.Errorf("destination %q backup_dir %q must be absolute", dest.Name, dest.BackupDir)
		}
	case config.DestTypeDir:
		if !filepath.IsAbs(dest.BackupDir) {
			return fmt.Errorf("destination %q backup_dir %q must be absolute", dest.Name, dest.BackupDir)
		}
	case config.DestTypeRclone:
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

func validateRcloneDestination(dest *config.DestinationConfig) error {
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
