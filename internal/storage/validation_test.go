package storage

import (
	"testing"

	"github.com/webstudiobond/backup-recovery-orchestrator/internal/config"
)

func TestValidateDestinations(t *testing.T) {
	t.Parallel()

	zeroCount := 0
	invalidCount := -4
	validCount := 7

	cases := []struct {
		name    string
		dests   []config.DestinationConfig
		wantErr bool
	}{
		{
			name: "invalid destination identifier name",
			dests: []config.DestinationConfig{
				{Name: ".hidden_dst", Type: config.DestTypeDir, BackupDir: "/mnt/data_backups"},
			},
			wantErr: true,
		},
		{
			name: "duplicate destination identifier in slice",
			dests: []config.DestinationConfig{
				{Name: "shared_dest_name", Type: config.DestTypeDir, BackupDir: "/mnt/dir_one"},
				{Name: "shared_dest_name", Type: config.DestTypeDir, BackupDir: "/mnt/dir_two"},
			},
			wantErr: true,
		},
		{
			name: "missing explicit destination type",
			dests: []config.DestinationConfig{
				{Name: "untyped_dst", MountPoint: "/mnt/untyped", BackupDir: "/mnt/untyped/backups"},
			},
			wantErr: true,
		},
		{
			name: "fuse destination relative mount point",
			dests: []config.DestinationConfig{
				{Name: "fuse_rel_mnt", Type: config.DestTypeFUSE, MountPoint: "mnt/fuse_point", BackupDir: "/mnt/storage"},
			},
			wantErr: true,
		},
		{
			name: "fuse destination relative backup dir",
			dests: []config.DestinationConfig{
				{Name: "fuse_rel_dir", Type: config.DestTypeFUSE, MountPoint: "/mnt/fuse_point", BackupDir: "mnt/storage"},
			},
			wantErr: true,
		},
		{
			name: "mount destination relative mount point",
			dests: []config.DestinationConfig{
				{Name: "mount_rel_mnt", Type: config.DestTypeMount, MountPoint: "mnt/nfs_point", BackupDir: "/mnt/nfs_backup"},
			},
			wantErr: true,
		},
		{
			name: "mount destination relative backup dir",
			dests: []config.DestinationConfig{
				{Name: "mount_rel_dir", Type: config.DestTypeMount, MountPoint: "/mnt/nfs_point", BackupDir: "nfs_backup"},
			},
			wantErr: true,
		},
		{
			name: "dir destination relative backup dir",
			dests: []config.DestinationConfig{
				{Name: "dir_rel_dir", Type: config.DestTypeDir, BackupDir: "var/local_backups"},
			},
			wantErr: true,
		},
		{
			name: "negative wait timeout sec value",
			dests: []config.DestinationConfig{
				{Name: "neg_timeout", Type: config.DestTypeDir, BackupDir: "/mnt/timeout_dir", WaitTimeoutSec: -10},
			},
			wantErr: true,
		},
		{
			name: "remote count zero rejected",
			dests: []config.DestinationConfig{
				{Name: "zero_remote", Type: config.DestTypeDir, BackupDir: "/mnt/zero_dir", RemoteCount: &zeroCount},
			},
			wantErr: true,
		},
		{
			name: "remote count below minus one rejected",
			dests: []config.DestinationConfig{
				{Name: "neg_remote", Type: config.DestTypeDir, BackupDir: "/mnt/neg_dir", RemoteCount: &invalidCount},
			},
			wantErr: true,
		},
		{
			name: "rclone destination missing colon in remote",
			dests: []config.DestinationConfig{
				{Name: "rclone_no_colon", Type: config.DestTypeRclone, Remote: "cloud_only"},
			},
			wantErr: true,
		},
		{
			name: "rclone destination traversal in remote",
			dests: []config.DestinationConfig{
				{Name: "rclone_trav", Type: config.DestTypeRclone, Remote: "cloud_s3:bucket/../escape"},
			},
			wantErr: true,
		},
		{
			name: "rclone destination whitespace in remote",
			dests: []config.DestinationConfig{
				{Name: "rclone_spaces", Type: config.DestTypeRclone, Remote: "cloud_s3:bucket/path with tab\t"},
			},
			wantErr: true,
		},
		{
			name: "rclone destination relative rclone config",
			dests: []config.DestinationConfig{
				{Name: "rclone_rel_cfg", Type: config.DestTypeRclone, Remote: "cloud_s3:bucket/safe", RcloneConfig: "rclone.conf"},
			},
			wantErr: true,
		},
		{
			name: "valid mixed destination collection",
			dests: []config.DestinationConfig{
				{Name: "valid_fuse_dst", Type: config.DestTypeFUSE, MountPoint: "/mnt/fuse_abs", BackupDir: "/mnt/fuse_abs/data", RemoteCount: &validCount},
				{Name: "valid_mount_dst", Type: config.DestTypeMount, MountPoint: "/mnt/nfs_abs", BackupDir: "/mnt/nfs_abs/data"},
				{Name: "valid_dir_dst", Type: config.DestTypeDir, BackupDir: "/var/backups_abs"},
				{Name: "valid_rclone_dst", Type: config.DestTypeRclone, Remote: "remote_box:data/archive", RcloneConfig: "/etc/rclone/rclone.conf"},
			},
			wantErr: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := ValidateDestinations(tc.dests)
			if (err != nil) != tc.wantErr {
				t.Fatalf("ValidateDestinations() error = %v, wantErr = %v", err, tc.wantErr)
			}
		})
	}
}

func TestValidateStackDestinations(t *testing.T) {
	t.Parallel()

	known := map[string]struct{}{
		"active_nas":   {},
		"active_cloud": {},
	}

	cases := []struct {
		name    string
		dests   []string
		wantErr bool
	}{
		{
			name:    "invalid identifier character",
			dests:   []string{"dest@invalid"},
			wantErr: true,
		},
		{
			name:    "unknown destination reference",
			dests:   []string{"ghost_destination"},
			wantErr: true,
		},
		{
			name:    "valid registered destinations",
			dests:   []string{"active_nas", "active_cloud"},
			wantErr: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := ValidateStackDestinations(tc.dests, known)
			if (err != nil) != tc.wantErr {
				t.Fatalf("ValidateStackDestinations() error = %v, wantErr = %v", err, tc.wantErr)
			}
		})
	}
}

func TestValidateRetention(t *testing.T) {
	t.Parallel()

	known := map[string]struct{}{
		"target_alpha": {},
		"target_beta":  {},
		"target_gamma": {},
	}

	cases := []struct {
		overrides map[string]int
		name      string
		local     int
		remote    int
		wantErr   bool
	}{
		{
			name:    "local count zero",
			local:   0,
			remote:  1,
			wantErr: true,
		},
		{
			name:    "local count less than minus one",
			local:   -3,
			remote:  1,
			wantErr: true,
		},
		{
			name:    "remote count zero",
			local:   1,
			remote:  0,
			wantErr: true,
		},
		{
			name:    "remote count less than minus one",
			local:   1,
			remote:  -2,
			wantErr: true,
		},
		{
			name:      "remote override references unknown target",
			local:     1,
			remote:    1,
			overrides: map[string]int{"missing_target": 2},
			wantErr:   true,
		},
		{
			name:      "remote override count zero",
			local:     1,
			remote:    1,
			overrides: map[string]int{"target_alpha": 0},
			wantErr:   true,
		},
		{
			name:      "remote override count negative invalid",
			local:     1,
			remote:    1,
			overrides: map[string]int{"target_beta": -4},
			wantErr:   true,
		},
		{
			name:      "valid unlimited local and remote override",
			local:     -1,
			remote:    3,
			overrides: map[string]int{"target_gamma": -1},
			wantErr:   false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ret := config.RetentionConfig{
				LocalCount:      tc.local,
				RemoteCount:     tc.remote,
				RemoteOverrides: tc.overrides,
			}
			err := ValidateRetention(ret, known)
			if (err != nil) != tc.wantErr {
				t.Fatalf("ValidateRetention() error = %v, wantErr = %v", err, tc.wantErr)
			}
		})
	}
}
