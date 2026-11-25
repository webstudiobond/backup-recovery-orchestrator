package config

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadBundleErrors(t *testing.T) {
	t.Parallel()

	baseGlobal := `{
  "lvm": {
    "vg_name": "ubuntu-vg",
    "lv_name": "lv_home",
    "snapshot_name": "home_snap",
    "snapshot_size": "35G",
    "mount_point": "/mnt/home_snap"
  },
  "destinations": [
    { "name": "nas", "type": "mount", "mount_point": "/mnt/nas", "backup_dir": "/mnt/nas/backups" }
  ]
}`

	cases := []struct {
		profiles    map[string]string
		extraStacks map[string]string
		name        string
		globalJSON  string
		singleStack string
		errContains string
	}{
		{
			name:        "unknown field in global config",
			globalJSON:  `{"unknown_key": true, "lvm": {"vg_name":"vg","lv_name":"lv","snapshot_name":"s","snapshot_size":"1G","mount_point":"/mnt/s"}}`,
			errContains: "unknown field",
		},
		{
			name:        "invalid timezone setting",
			globalJSON:  `{"timezone": "mars", "lvm": {"vg_name":"vg","lv_name":"lv","snapshot_name":"s","snapshot_size":"1G","mount_point":"/mnt/s"}}`,
			errContains: "must be 'local' or 'utc'",
		},
		{
			name:        "destination missing explicit type",
			globalJSON:  `{"lvm":{"vg_name":"vg","lv_name":"lv","snapshot_name":"s","snapshot_size":"1G","mount_point":"/mnt/s"},"destinations":[{"name":"cloud","mount_point":"/mnt/cloud","backup_dir":"/mnt/cloud/backups"}]}`,
			errContains: "must be explicitly set",
		},
		{
			name:        "rclone destination invalid remote",
			globalJSON:  `{"lvm":{"vg_name":"vg","lv_name":"lv","snapshot_name":"s","snapshot_size":"1G","mount_point":"/mnt/s"},"destinations":[{"name":"cloud","type":"rclone","remote":"invalid_no_colon"}]}`,
			errContains: "requires remote in 'name:path' format",
		},
		{
			name:        "invalid local retention zero",
			globalJSON:  `{"retention":{"local_count":0,"remote_count":2},"lvm":{"vg_name":"vg","lv_name":"lv","snapshot_name":"s","snapshot_size":"1G","mount_point":"/mnt/s"}}`,
			errContains: "retention.local_count",
		},
		{
			name:        "invalid remote retention zero",
			globalJSON:  `{"retention":{"local_count":1,"remote_count":0},"lvm":{"vg_name":"vg","lv_name":"lv","snapshot_name":"s","snapshot_size":"1G","mount_point":"/mnt/s"}}`,
			errContains: "retention.remote_count",
		},
		{
			name:        "unknown destination in stack destinations",
			globalJSON:  baseGlobal,
			singleStack: `{"destinations":["ghost"]}`,
			errContains: "unknown destination",
		},
		{
			name:        "unknown destination in remote_overrides",
			globalJSON:  baseGlobal,
			singleStack: `{"retention":{"remote_overrides":{"ghost":4}}}`,
			errContains: "unknown destination",
		},
		{
			name:        "missing profile",
			globalJSON:  baseGlobal,
			singleStack: `{"extends": "does-not-exist"}`,
			errContains: "not found",
		},
		{
			name:       "cyclic profile inheritance",
			globalJSON: baseGlobal,
			profiles: map[string]string{
				"a.json": `{"extends": "b"}`,
				"b.json": `{"extends": "a"}`,
			},
			singleStack: `{"extends": "a"}`,
			errContains: "cyclic profile inheritance",
		},
		{
			name:       "db_import without configured databases",
			globalJSON: baseGlobal,
			singleStack: `{
  "restore_steps": [
    { "action": "db_import" }
  ]
}`,
			errContains: "requires at least one configured database",
		},
		{
			name:       "invalid database id reference in restore step",
			globalJSON: baseGlobal,
			singleStack: `{
  "restore_steps": [
    { "action": "db_import", "database_id": "missing" }
  ]
}`,
			errContains: "unknown database_id",
		},
		{
			name:       "path traversal in reverse_proxy domain conf",
			globalJSON: baseGlobal,
			singleStack: `{
  "reverse_proxy": {
    "domain_confs": ["../escape.example.com.conf"]
  }
}`,
			errContains: "must not start with '.' or '-' or contain '..'",
		},
		{
			name:       "dac path escaping stack dir",
			globalJSON: baseGlobal,
			singleStack: `{
  "dac": [
    { "path": "../etc/shadow", "owner": "root:root", "file_mode": "0600" }
  ]
}`,
			errContains: "escapes stack_dir",
		},
		{
			name:       "dac invalid octal mode",
			globalJSON: baseGlobal,
			singleStack: `{
  "dac": [
    { "path": "data", "owner": "root:stack", "dir_mode": "0999" }
  ]
}`,
			errContains: "invalid dir_mode",
		},
		{
			name:       "protected host file path",
			globalJSON: baseGlobal,
			singleStack: `{
  "host_files": [
    { "path": "/etc/shadow" }
  ]
}`,
			errContains: "protected system location",
		},
		{
			name:       "invalid hook action",
			globalJSON: baseGlobal,
			singleStack: `{
  "hooks": {
    "pre_snapshot": [
      { "action": "rm_rf_everything" }
    ]
  }
}`,
			errContains: "unsupported action",
		},
		{
			name:       "duplicate stack name across files",
			globalJSON: baseGlobal,
			extraStacks: map[string]string{
				"one.json": `{"name": "shared"}`,
				"two.json": `{"name": "shared"}`,
			},
			errContains: "duplicate stack name",
		},
		{
			name:        "trailing json payload",
			globalJSON:  baseGlobal,
			singleStack: `{"enabled": true}{"extra": true}`,
			errContains: "unexpected trailing data",
		},
		{
			name:       "move_contents missing from",
			globalJSON: baseGlobal,
			singleStack: `{
  "restore_steps": [
    { "action": "move_contents", "to": "daemon/data" }
  ]
}`,
			errContains: "requires from",
		},
		{
			name:       "move_contents escaping path",
			globalJSON: baseGlobal,
			singleStack: `{
  "restore_steps": [
    { "action": "move_contents", "from": "../outside", "to": "daemon/data" }
  ]
}`,
			errContains: "escapes stack_dir",
		},
		{
			name:       "move_contents absolute target",
			globalJSON: baseGlobal,
			singleStack: `{
  "restore_steps": [
    { "action": "move_contents", "from": "daemon/ckpt", "to": "/etc/evil" }
  ]
}`,
			errContains: "must be relative",
		},
		{
			name:       "move_contents target inside source",
			globalJSON: baseGlobal,
			singleStack: `{
  "restore_steps": [
    { "action": "move_contents", "from": "daemon/data", "to": "daemon/data/sub" }
  ]
}`,
			errContains: "cannot reside inside source",
		},
		{
			name:       "move_contents identical paths",
			globalJSON: baseGlobal,
			singleStack: `{
  "restore_steps": [
    { "action": "move_contents", "from": "daemon/data", "to": "daemon/data" }
  ]
}`,
			errContains: "must differ",
		},
		{
			name:        "invalid reverse_proxy_stack identifier",
			globalJSON:  `{"backup_root":"/backups","reverse_proxy_stack":"../bad","lvm":{"vg_name":"vg","lv_name":"lv","snapshot_name":"snap","snapshot_size":"35G","mount_point":"/mnt/snap"}}`,
			errContains: "reverse_proxy_stack",
		},
		{
			name:       "reverse_proxy escaping stream_confs",
			globalJSON: baseGlobal,
			singleStack: `{
  "reverse_proxy": {
    "stream_confs": ["../escape.inc"]
  }
}`,
			errContains: "reverse_proxy.stream_confs entry",
		},
		{
			name:        "reserved selfbackup stack name",
			globalJSON:  baseGlobal,
			singleStack: `{"name": "bro-self"}`,
			errContains: "reserved for selfbackup",
		},
		{
			name:        "vars reserved stack key",
			globalJSON:  baseGlobal,
			singleStack: `{"vars": {"stack": "other"}}`,
			errContains: `vars key "stack" is reserved`,
		},
		{
			name:        "vars absolute value rejected",
			globalJSON:  baseGlobal,
			singleStack: `{"vars": {"data_dir": "/etc/evil"}}`,
			errContains: "must be relative",
		},
		{
			name:        "vars parent traversal rejected",
			globalJSON:  baseGlobal,
			singleStack: `{"vars": {"data_dir": "../escape"}}`,
			errContains: "escapes base path",
		},
		{
			name:        "unresolved placeholder rejected",
			globalJSON:  baseGlobal,
			singleStack: `{"files": {"excludes": ["./{missing_dir}/*"]}}`,
			errContains: "unresolved variable placeholder",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			dir := t.TempDir()
			cfgPath := filepath.Join(dir, "config.json")
			writeTestFile(t, cfgPath, tc.globalJSON)

			for name, body := range tc.profiles {
				writeTestFile(t, filepath.Join(dir, "profiles", name), body)
			}
			if tc.singleStack != "" {
				writeTestFile(t, filepath.Join(dir, "conf.d", "app.json"), tc.singleStack)
			}
			for name, body := range tc.extraStacks {
				writeTestFile(t, filepath.Join(dir, "conf.d", name), body)
			}
			if tc.singleStack == "" && len(tc.extraStacks) == 0 {
				if err := os.MkdirAll(filepath.Join(dir, "conf.d"), 0o750); err != nil {
					t.Fatalf("mkdir conf.d: %v", err)
				}
			}

			_, err := LoadBundle(context.Background(), cfgPath)
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.errContains)
			}
			if !strings.Contains(err.Error(), tc.errContains) {
				t.Fatalf("expected error containing %q, got %v", tc.errContains, err)
			}
		})
	}
}

func TestValidateIdentifier(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		value   string
		wantErr bool
	}{
		{name: "empty identifier string", value: "", wantErr: true},
		{name: "identifier with leading dot", value: ".hidden_name", wantErr: true},
		{name: "identifier with double dot traversal", value: "parent..child", wantErr: true},
		{name: "identifier with illegal symbol", value: "invalid@symbol", wantErr: true},
		{name: "valid alphanumeric with dot dash underscore", value: "valid-name_01.sub", wantErr: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := validateIdentifier(tc.value, "ident_label")
			if (err != nil) != tc.wantErr {
				t.Fatalf("validateIdentifier(%q) error = %v, wantErr = %v", tc.value, err, tc.wantErr)
			}
		})
	}
}

func TestValidateVarValue(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		val     string
		wantErr bool
	}{
		{name: "whitespace only value", val: "   ", wantErr: true},
		{name: "surrounding whitespace in value", val: " untrimmed ", wantErr: true},
		{name: "curly bracket character in value", val: "val{bracket}", wantErr: true},
		{name: "newline control character in value", val: "val\nline", wantErr: true},
		{name: "current directory dot value", val: ".", wantErr: true},
		{name: "parent directory double dot value", val: "..", wantErr: true},
		{name: "relative path traversal escaping base", val: "../escaped_dir", wantErr: true},
		{name: "absolute path value", val: "/var/data", wantErr: true},
		{name: "valid nested relative value", val: "sub/dir-data_01", wantErr: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := validateVarValue("custom_var_key", tc.val)
			if (err != nil) != tc.wantErr {
				t.Fatalf("validateVarValue(%q) error = %v, wantErr = %v", tc.val, err, tc.wantErr)
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

func TestValidateLVMConfig(t *testing.T) {
	t.Parallel()

	cases := []struct {
		modify  func(*LVMConfig)
		name    string
		wantErr bool
	}{
		{
			name: "invalid vg_name identifier",
			modify: func(c *LVMConfig) {
				c.VGName = ""
			},
			wantErr: true,
		},
		{
			name: "invalid lv_name identifier",
			modify: func(c *LVMConfig) {
				c.LVName = "bad lv name"
			},
			wantErr: true,
		},
		{
			name: "invalid snapshot_name identifier",
			modify: func(c *LVMConfig) {
				c.SnapshotName = "..bad_snap"
			},
			wantErr: true,
		},
		{
			name: "empty snapshot_size string",
			modify: func(c *LVMConfig) {
				c.SnapshotSize = "   "
			},
			wantErr: true,
		},
		{
			name: "relative mount_point path",
			modify: func(c *LVMConfig) {
				c.MountPoint = "relative/mount"
			},
			wantErr: true,
		},
		{
			name: "valid complete lvm configuration",
			modify: func(*LVMConfig) {
			},
			wantErr: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := LVMConfig{
				VGName:       "vg_test",
				LVName:       "lv_test",
				SnapshotName: "snap_test",
				SnapshotSize: "22G",
				MountPoint:   "/mnt/lvm_snap",
			}
			tc.modify(&cfg)
			err := validateLVMConfig(&cfg)
			if (err != nil) != tc.wantErr {
				t.Fatalf("validateLVMConfig() error = %v, wantErr = %v", err, tc.wantErr)
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
			name:    "local count less than minus one",
			local:   -3,
			remote:  1,
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
			ret := RetentionConfig{
				LocalCount:      tc.local,
				RemoteCount:     tc.remote,
				RemoteOverrides: tc.overrides,
			}
			err := validateRetention(ret, known)
			if (err != nil) != tc.wantErr {
				t.Fatalf("validateRetention() error = %v, wantErr = %v", err, tc.wantErr)
			}
		})
	}
}

func TestValidateStackReverseProxy(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name         string
		reverseProxy StackReverseProxyConfig
		wantErr      bool
	}{
		{
			name: "invalid acme names identifier",
			reverseProxy: StackReverseProxyConfig{
				ACMENames: []string{".illegal_acme"},
			},
			wantErr: true,
		},
		{
			name: "invalid domain confs identifier",
			reverseProxy: StackReverseProxyConfig{
				DomainConfs: []string{"nested/domain.conf"},
			},
			wantErr: true,
		},
		{
			name: "invalid stream confs identifier",
			reverseProxy: StackReverseProxyConfig{
				StreamConfs: []string{"bad stream conf"},
			},
			wantErr: true,
		},
		{
			name: "valid stack reverse_proxy entries",
			reverseProxy: StackReverseProxyConfig{
				ACMENames:   []string{"app.example.com"},
				DomainConfs: []string{"app.example.com.conf"},
				StreamConfs: []string{"stream.example.org.conf"},
			},
			wantErr: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := validateStackReverseProxy(&tc.reverseProxy)
			if (err != nil) != tc.wantErr {
				t.Fatalf("validateStackReverseProxy() error = %v, wantErr = %v", err, tc.wantErr)
			}
		})
	}
}

func TestValidateDestinations(t *testing.T) {
	t.Parallel()

	zeroCount := 0
	invalidCount := -4
	validCount := 7

	cases := []struct {
		name    string
		dests   []DestinationConfig
		wantErr bool
	}{
		{
			name: "invalid destination identifier name",
			dests: []DestinationConfig{
				{Name: ".hidden_dst", Type: DestTypeDir, BackupDir: "/mnt/data_backups"},
			},
			wantErr: true,
		},
		{
			name: "duplicate destination identifier in slice",
			dests: []DestinationConfig{
				{Name: "shared_dest_name", Type: DestTypeDir, BackupDir: "/mnt/dir_one"},
				{Name: "shared_dest_name", Type: DestTypeDir, BackupDir: "/mnt/dir_two"},
			},
			wantErr: true,
		},
		{
			name: "fuse destination relative mount point",
			dests: []DestinationConfig{
				{Name: "fuse_rel_mnt", Type: DestTypeFUSE, MountPoint: "mnt/fuse_point", BackupDir: "/mnt/storage"},
			},
			wantErr: true,
		},
		{
			name: "fuse destination relative backup dir",
			dests: []DestinationConfig{
				{Name: "fuse_rel_dir", Type: DestTypeFUSE, MountPoint: "/mnt/fuse_point", BackupDir: "mnt/storage"},
			},
			wantErr: true,
		},
		{
			name: "mount destination relative mount point",
			dests: []DestinationConfig{
				{Name: "mount_rel_mnt", Type: DestTypeMount, MountPoint: "mnt/nfs_point", BackupDir: "/mnt/nfs_backup"},
			},
			wantErr: true,
		},
		{
			name: "mount destination relative backup dir",
			dests: []DestinationConfig{
				{Name: "mount_rel_dir", Type: DestTypeMount, MountPoint: "/mnt/nfs_point", BackupDir: "nfs_backup"},
			},
			wantErr: true,
		},
		{
			name: "dir destination relative backup dir",
			dests: []DestinationConfig{
				{Name: "dir_rel_dir", Type: DestTypeDir, BackupDir: "var/local_backups"},
			},
			wantErr: true,
		},
		{
			name: "negative wait timeout sec value",
			dests: []DestinationConfig{
				{Name: "neg_timeout", Type: DestTypeDir, BackupDir: "/mnt/timeout_dir", WaitTimeoutSec: -10},
			},
			wantErr: true,
		},
		{
			name: "remote count zero rejected",
			dests: []DestinationConfig{
				{Name: "zero_remote", Type: DestTypeDir, BackupDir: "/mnt/zero_dir", RemoteCount: &zeroCount},
			},
			wantErr: true,
		},
		{
			name: "remote count below minus one rejected",
			dests: []DestinationConfig{
				{Name: "neg_remote", Type: DestTypeDir, BackupDir: "/mnt/neg_dir", RemoteCount: &invalidCount},
			},
			wantErr: true,
		},
		{
			name: "rclone destination traversal in remote",
			dests: []DestinationConfig{
				{Name: "rclone_trav", Type: DestTypeRclone, Remote: "cloud_s3:bucket/../escape"},
			},
			wantErr: true,
		},
		{
			name: "rclone destination whitespace in remote",
			dests: []DestinationConfig{
				{Name: "rclone_spaces", Type: DestTypeRclone, Remote: "cloud_s3:bucket/path with tab\t"},
			},
			wantErr: true,
		},
		{
			name: "rclone destination relative rclone config",
			dests: []DestinationConfig{
				{Name: "rclone_rel_cfg", Type: DestTypeRclone, Remote: "cloud_s3:bucket/safe", RcloneConfig: "rclone.conf"},
			},
			wantErr: true,
		},
		{
			name: "valid mixed destination collection",
			dests: []DestinationConfig{
				{Name: "valid_fuse_dst", Type: DestTypeFUSE, MountPoint: "/mnt/fuse_abs", BackupDir: "/mnt/fuse_abs/data", RemoteCount: &validCount},
				{Name: "valid_mount_dst", Type: DestTypeMount, MountPoint: "/mnt/nfs_abs", BackupDir: "/mnt/nfs_abs/data"},
				{Name: "valid_dir_dst", Type: DestTypeDir, BackupDir: "/var/backups_abs"},
				{Name: "valid_rclone_dst", Type: DestTypeRclone, Remote: "remote_box:data/archive", RcloneConfig: "/etc/rclone/rclone.conf"},
			},
			wantErr: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := validateDestinations(tc.dests)
			if (err != nil) != tc.wantErr {
				t.Fatalf("validateDestinations() error = %v, wantErr = %v", err, tc.wantErr)
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
			err := validateStackDestinations(tc.dests, known)
			if (err != nil) != tc.wantErr {
				t.Fatalf("validateStackDestinations() error = %v, wantErr = %v", err, tc.wantErr)
			}
		})
	}
}

func TestValidateGlobalConfig(t *testing.T) {
	t.Parallel()

	baseGlobal := func() GlobalConfig {
		return GlobalConfig{
			BackupRoot:   "/var/backups",
			LockFile:     "/var/run/bro.lock",
			MaxArchiveGB: DefaultMaxArchiveGB,
			ZstdLevel:    3,
			Timezone:     TimezoneUTC,
			LVM: LVMConfig{
				VGName:       "vg_system",
				LVName:       "lv_system",
				SnapshotName: "snap_system",
				SnapshotSize: "10G",
				MountPoint:   "/mnt/snap_point",
			},
			Retention: RetentionConfig{
				LocalCount:  1,
				RemoteCount: 1,
			},
		}
	}

	cases := []struct {
		modify  func(*GlobalConfig)
		name    string
		wantErr bool
	}{
		{
			name: "relative backup_root path",
			modify: func(c *GlobalConfig) {
				c.BackupRoot = "relative/backup_root"
			},
			wantErr: true,
		},
		{
			name: "relative restore_work_dir path",
			modify: func(c *GlobalConfig) {
				c.RestoreWorkDir = "relative/work"
			},
			wantErr: true,
		},
		{
			name: "root restore_work_dir path",
			modify: func(c *GlobalConfig) {
				c.RestoreWorkDir = "/"
			},
			wantErr: true,
		},
		{
			name: "valid restore_work_dir path",
			modify: func(c *GlobalConfig) {
				c.RestoreWorkDir = "/home/.bro-work"
			},
			wantErr: false,
		},
		{
			name: "zero max_archive_gb",
			modify: func(c *GlobalConfig) {
				c.MaxArchiveGB = 0
			},
			wantErr: true,
		},
		{
			name: "relative lock_file path",
			modify: func(c *GlobalConfig) {
				c.LockFile = "relative/lock.file"
			},
			wantErr: true,
		},
		{
			name: "invalid zstd level in global",
			modify: func(c *GlobalConfig) {
				c.ZstdLevel = -1
			},
			wantErr: true,
		},
		{
			name: "invalid lvm in global config",
			modify: func(c *GlobalConfig) {
				c.LVM.VGName = ""
			},
			wantErr: true,
		},
		{
			name: "valid global configuration object",
			modify: func(*GlobalConfig) {
			},
			wantErr: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := baseGlobal()
			tc.modify(&cfg)
			err := validateGlobalConfig(&cfg)
			if (err != nil) != tc.wantErr {
				t.Fatalf("validateGlobalConfig() error = %v, wantErr = %v", err, tc.wantErr)
			}
		})
	}
}

func TestValidateStackConfig(t *testing.T) {
	t.Parallel()

	known := map[string]struct{}{"main_target": {}}

	baseStack := func() StackConfig {
		return StackConfig{
			Name:        "service_stack",
			User:        UserConfig{Name: "app_deployer"},
			StackDir:    "/opt/service",
			ComposeFile: "/opt/service/docker-compose.yml",
			ZstdLevel:   3,
			LVM: LVMConfig{
				VGName:       "vg_srv",
				LVName:       "lv_srv",
				SnapshotName: "snap_srv",
				SnapshotSize: "12G",
				MountPoint:   "/mnt/srv_snap",
			},
			Retention: RetentionConfig{
				LocalCount:  2,
				RemoteCount: 2,
			},
			Destinations: []string{"main_target"},
		}
	}

	cases := []struct {
		modify  func(*StackConfig)
		name    string
		wantErr bool
	}{
		{
			name: "invalid stack name format",
			modify: func(c *StackConfig) {
				c.Name = "bad name with space"
			},
			wantErr: true,
		},
		{
			name: "reserved self backup name identifier",
			modify: func(c *StackConfig) {
				c.Name = ReservedSelfBackupName
			},
			wantErr: true,
		},
		{
			name: "invalid user name format",
			modify: func(c *StackConfig) {
				c.User.Name = ""
			},
			wantErr: true,
		},
		{
			name: "relative stack_dir path",
			modify: func(c *StackConfig) {
				c.StackDir = "opt/service"
			},
			wantErr: true,
		},
		{
			name: "relative compose_file path",
			modify: func(c *StackConfig) {
				c.ComposeFile = "docker-compose.yml"
			},
			wantErr: true,
		},
		{
			name: "invalid zstd level in stack",
			modify: func(c *StackConfig) {
				c.ZstdLevel = 25
			},
			wantErr: true,
		},
		{
			name: "invalid lvm in stack config",
			modify: func(c *StackConfig) {
				c.LVM.VGName = ""
			},
			wantErr: true,
		},
		{
			name: "invalid database config in stack",
			modify: func(c *StackConfig) {
				c.Databases = []DatabaseConfig{
					{ID: "invalid id with spaces"},
				}
			},
			wantErr: true,
		},
		{
			name: "valid complete stack configuration",
			modify: func(*StackConfig) {
			},
			wantErr: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := baseStack()
			tc.modify(&cfg)
			err := validateStackConfig(&cfg, known)
			if (err != nil) != tc.wantErr {
				t.Fatalf("validateStackConfig() error = %v, wantErr = %v", err, tc.wantErr)
			}
		})
	}
}
