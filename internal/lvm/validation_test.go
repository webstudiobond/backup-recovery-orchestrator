package lvm

import (
	"testing"

	"github.com/webstudiobond/backup-recovery-orchestrator/internal/config"
)

func TestValidateConfig(t *testing.T) {
	t.Parallel()

	cases := []struct {
		modify  func(*config.LVMConfig)
		name    string
		wantNil bool
		wantErr bool
	}{
		{
			name:    "nil lvm config",
			wantNil: true,
			wantErr: true,
		},
		{
			name: "invalid vg_name identifier",
			modify: func(c *config.LVMConfig) {
				c.VGName = ""
			},
			wantErr: true,
		},
		{
			name: "invalid lv_name identifier",
			modify: func(c *config.LVMConfig) {
				c.LVName = "bad lv name"
			},
			wantErr: true,
		},
		{
			name: "invalid snapshot_name identifier",
			modify: func(c *config.LVMConfig) {
				c.SnapshotName = "..bad_snap"
			},
			wantErr: true,
		},
		{
			name: "empty snapshot_size string",
			modify: func(c *config.LVMConfig) {
				c.SnapshotSize = "   "
			},
			wantErr: true,
		},
		{
			name: "relative mount_point path",
			modify: func(c *config.LVMConfig) {
				c.MountPoint = "relative/mount"
			},
			wantErr: true,
		},
		{
			name: "valid complete lvm configuration",
			modify: func(*config.LVMConfig) {
			},
			wantErr: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if tc.wantNil {
				err := ValidateConfig(nil)
				if (err != nil) != tc.wantErr {
					t.Fatalf("ValidateConfig(nil) error = %v, wantErr = %v", err, tc.wantErr)
				}
				return
			}
			cfg := config.LVMConfig{
				VGName:       "vg_test",
				LVName:       "lv_test",
				SnapshotName: "snap_test",
				SnapshotSize: "22G",
				MountPoint:   "/mnt/lvm_snap",
			}
			tc.modify(&cfg)
			err := ValidateConfig(&cfg)
			if (err != nil) != tc.wantErr {
				t.Fatalf("ValidateConfig() error = %v, wantErr = %v", err, tc.wantErr)
			}
		})
	}
}
