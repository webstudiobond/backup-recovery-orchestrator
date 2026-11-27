package validator

import (
	"strings"
	"testing"

	"github.com/webstudiobond/backup-recovery-orchestrator/internal/config"
)

func TestValidateGlobalConfig(t *testing.T) {
	t.Parallel()

	cases := []struct {
		modify  func(*config.GlobalConfig)
		name    string
		wantErr string
	}{
		{
			name:    "relative backup_root path",
			modify:  func(c *config.GlobalConfig) { c.BackupRoot = "relative/backup_root" },
			wantErr: "backup_root",
		},
		{
			name:    "relative restore_work_dir path",
			modify:  func(c *config.GlobalConfig) { c.RestoreWorkDir = "relative/work" },
			wantErr: "restore_work_dir",
		},
		{
			name:    "root restore_work_dir path",
			modify:  func(c *config.GlobalConfig) { c.RestoreWorkDir = "/" },
			wantErr: "restore_work_dir",
		},
		{
			name:    "relative lock_file path",
			modify:  func(c *config.GlobalConfig) { c.LockFile = "relative/lock.file" },
			wantErr: "lock_file",
		},
		{
			name:    "zero max_archive_gb",
			modify:  func(c *config.GlobalConfig) { c.MaxArchiveGB = 0 },
			wantErr: "max_archive_gb",
		},
		{
			name:    "invalid zstd level in global",
			modify:  func(c *config.GlobalConfig) { c.ZstdLevel = -1 },
			wantErr: "zstd_level",
		},
		{
			name:    "invalid timezone",
			modify:  func(c *config.GlobalConfig) { c.Timezone = "mars" },
			wantErr: "must be 'local' or 'utc'",
		},
		{
			name:    "delegated lvm error",
			modify:  func(c *config.GlobalConfig) { c.LVM.VGName = "" },
			wantErr: "validate global lvm",
		},
		{
			name: "delegated destinations error",
			modify: func(c *config.GlobalConfig) {
				c.Destinations = []config.DestinationConfig{{Name: ".bad", Type: config.DestTypeDir, BackupDir: "/var/dest"}}
			},
			wantErr: "validate destinations",
		},
		{
			name:    "delegated retention error",
			modify:  func(c *config.GlobalConfig) { c.Retention.LocalCount = 0 },
			wantErr: "validate global retention",
		},
		{
			name:    "delegated reverse_proxy_stack error",
			modify:  func(c *config.GlobalConfig) { c.ReverseProxyStack = "-bad" },
			wantErr: "validate reverse_proxy_stack",
		},
		{
			name:   "valid restore_work_dir and local timezone",
			modify: func(c *config.GlobalConfig) { c.RestoreWorkDir = "/home/.bro-work"; c.Timezone = config.TimezoneLocal },
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := validTestGlobalConfig()
			tc.modify(&cfg)
			known, err := validateGlobalConfig(&cfg)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("expected error containing %q, got %v", tc.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if known == nil {
				t.Fatal("expected non-nil known destinations map")
			}
		})
	}
}

func TestValidateZstdLevel(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		level   int
		wantErr bool
	}{
		{name: "level zero below minimum", level: 0, wantErr: true},
		{name: "negative level below minimum", level: -5, wantErr: true},
		{name: "level twenty above maximum", level: 20, wantErr: true},
		{name: "minimum valid level", level: 1, wantErr: false},
		{name: "default valid compression level", level: 3, wantErr: false},
		{name: "maximum valid compression level", level: 19, wantErr: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := validateZstdLevel(tc.level)
			if (err != nil) != tc.wantErr {
				t.Fatalf("validateZstdLevel(%d) error = %v, wantErr = %v", tc.level, err, tc.wantErr)
			}
		})
	}
}
