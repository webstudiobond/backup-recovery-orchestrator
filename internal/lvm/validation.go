package lvm

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/webstudiobond/backup-recovery-orchestrator/internal/config"
	"github.com/webstudiobond/backup-recovery-orchestrator/internal/identifier"
)

// ValidateConfig verifies LVM volume group, logical volume, snapshot identifiers, size, and mount point path.
func ValidateConfig(lvm *config.LVMConfig) error {
	if lvm == nil {
		return errors.New("lvm config must not be nil")
	}
	if err := identifier.Validate(lvm.VGName, "lvm.vg_name"); err != nil {
		return fmt.Errorf("validate lvm.vg_name: %w", err)
	}
	if err := identifier.Validate(lvm.LVName, "lvm.lv_name"); err != nil {
		return fmt.Errorf("validate lvm.lv_name: %w", err)
	}
	if err := identifier.Validate(lvm.SnapshotName, "lvm.snapshot_name"); err != nil {
		return fmt.Errorf("validate lvm.snapshot_name: %w", err)
	}
	if strings.TrimSpace(lvm.SnapshotSize) == "" {
		return errors.New("lvm.snapshot_size must not be empty")
	}
	if !filepath.IsAbs(lvm.MountPoint) {
		return fmt.Errorf("lvm.mount_point %q must be an absolute path", lvm.MountPoint)
	}
	return nil
}
