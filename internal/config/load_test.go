package config

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
)

func TestLoadBundleWithProfilesAndOverrides(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	globalPath := filepath.Join(root, "config.json")

	writeTestFile(t, globalPath, `{
  "backup_root": "/backups",
  "lock_file": "/run/lock/bro.lock",
  "zstd_level": 6,
  "min_free_gb": 15,
  "lvm": {
    "vg_name": "ubuntu-vg",
    "lv_name": "lv_home",
    "snapshot_name": "home_snap",
    "snapshot_size": "35G",
    "mount_point": "/mnt/home_snap"
  },
  "reverse_proxy_stack": "edge-proxy",
  "retention": {
    "local_count": 3,
    "remote_count": 7
  },
  "destinations": [
    {
      "name": "cloud-primary",
      "type": "fuse",
      "mount_point": "/mnt/cloud-primary",
      "backup_dir": "/mnt/cloud-primary/backups",
      "systemd_unit": "mnt-cloud-primary.mount",
      "remote_count": 14,
      "rclone_cleanup_remote": "cloud-primary-enc"
    },
    {
      "name": "s3-archive",
      "type": "rclone",
      "remote": "s3crypt:bucket/bro",
      "rclone_config": "/etc/rclone/rclone.conf",
      "remote_count": 30
    }
  ]
}`)

	writeTestFile(t, filepath.Join(root, "profiles", "base-docker.json"), `{
  "files": {
    "excludes": ["*.tmp"]
  },
  "dac": [
    { "path": "docker-compose.yaml", "owner": "root:root", "file_mode": "0600" }
  ],
  "restore_steps": [
    { "action": "compose_down" },
    { "action": "extract_files" },
    { "action": "apply_dac" },
    { "action": "deploy_reverse_proxy" },
    { "action": "compose_up" },
    { "action": "wait_healthy" }
  ]
}`)

	writeTestFile(t, filepath.Join(root, "profiles", "postgres-secrets.json"), `{
  "extends": "base-docker",
  "files": {
    "excludes": ["postgres/*"]
  },
  "dac": [
    { "path": "secrets", "owner": "root:stack", "dir_mode": "0750", "file_mode": "0440", "recursive": true, "optional": true }
  ],
  "host_files": [
    { "path": "/etc/cron.d/svc-{stack}", "owner": "root:root", "file_mode": "0644", "optional": true }
  ],
  "databases": [
    {
      "driver": "postgres",
      "service": "postgres",
      "secrets": {
        "source": "container",
        "user_file": "/run/secrets/db_user",
        "db_file": "/run/secrets/db_name",
        "password_file": "/run/secrets/db_password"
      }
    }
  ],
  "restore_steps": [
    { "action": "compose_down" },
    { "action": "extract_files" },
    { "action": "apply_dac" },
    { "action": "deploy_reverse_proxy" },
    { "action": "compose_up", "services": ["postgres"] },
    { "action": "wait_healthy", "services": ["postgres"] },
    { "action": "db_import" },
    { "action": "compose_up" },
    { "action": "wait_healthy" }
  ]
}`)

	writeTestFile(t, filepath.Join(root, "conf.d", "app-alpha.json"), `{
  "extends": "postgres-secrets",
  "user": { "name": "app-alpha", "uid": 1010, "gid": 1010 },
  "zstd_level": 1,
  "destinations": ["cloud-primary", "s3-archive"],
  "retention": {
    "local_count": 1,
    "remote_count": 5,
    "remote_overrides": { "s3-archive": 2 }
  },
  "files": { "excludes": ["logs/*"] },
  "dac": [
    { "path": "upload", "owner": "app-alpha:app-alpha", "dir_mode": "0700", "file_mode": "0600", "recursive": true }
  ],
  "host_files": [
    { "path": "/etc/logrotate.d/app-alpha", "owner": "root:root", "file_mode": "0400", "optional": true }
  ],
  "reverse_proxy": {
    "acme_names": ["app-alpha"],
    "domain_confs": ["alpha.example.com.conf"]
  }
}`)

	writeTestFile(t, filepath.Join(root, "conf.d", "app-beta.json"), `{
  "enabled": false,
  "extends": "base-docker",
  "user": { "name": "app-beta", "uid": 1015, "gid": 1015 },
  "files": {
    "no_wildcards_match_slash": true,
    "excludes": ["./daemon/data/*.sst"]
  },
  "hooks": {
    "pre_snapshot": [
      { "action": "remove_path", "path": "/home/app-beta/daemon/data/backup_checkpoint" },
      { "action": "compose_kill", "service": "daemon", "signal": "USR2" },
      { "action": "wait_for_file", "path": "/home/app-beta/daemon/data/backup_checkpoint/CURRENT", "timeout_sec": 60 }
    ],
    "post_snapshot": [
      { "action": "remove_path", "path": "/home/app-beta/daemon/data/backup_checkpoint" }
    ]
  },
  "reverse_proxy": {
    "acme_names": ["beta", "webui"],
    "domain_confs": ["beta.example.net.conf", "webui.example.org.conf"],
    "stream_confs": ["beta-turn.inc"]
  }
}`)

	writeTestFile(t, filepath.Join(root, "conf.d", "edge-proxy.json"), `{
  "user": { "name": "edge", "uid": 1020, "gid": 1020 },
  "stack_dir": "/srv/edge-proxy",
  "reverse_proxy_provider": {
    "type": "angie",
    "service_name": "angie-edge",
    "certs_dir": "data/certs",
    "domains_dir": "data/conf.d/domains",
    "stream_dir": "data/conf.d/stream"
  }
}`)

	bundle, err := LoadBundle(context.Background(), globalPath)
	if err != nil {
		t.Fatalf("unexpected error loading bundle: %v", err)
	}

	if bundle.Global.ReverseProxyStack != "edge-proxy" {
		t.Fatalf("expected reverse_proxy_stack edge-proxy, got %q", bundle.Global.ReverseProxyStack)
	}
	reverseProxyStack := bundle.ReverseProxyStack()
	if reverseProxyStack == nil || reverseProxyStack.ReverseProxyProvider == nil || reverseProxyStack.ReverseProxyProvider.ServiceName != "angie-edge" {
		t.Fatalf("expected resolved reverse proxy provider, got %+v", reverseProxyStack)
	}
	if bundle.Global.Timezone != DefaultTimezone {
		t.Fatalf("expected default timezone %q, got %q", DefaultTimezone, bundle.Global.Timezone)
	}
	if len(bundle.Global.Destinations) != 2 || bundle.Global.Destinations[0].WaitTimeoutSec != DefaultWaitTimeoutSec {
		t.Fatalf("unexpected destination defaults: %+v", bundle.Global.Destinations)
	}
	if len(bundle.Stacks) != 3 {
		t.Fatalf("expected 3 stacks, got %d", len(bundle.Stacks))
	}

	assertAlphaStack(t, &bundle, &bundle.Stacks[0])
	assertBetaStack(t, &bundle.Stacks[1])

	found, ok := bundle.StackByName("app-alpha")
	if !ok || found.Name != "app-alpha" {
		t.Fatalf("expected StackByName to locate app-alpha")
	}
	if _, missing := bundle.StackByName("nonexistent"); missing {
		t.Fatalf("expected StackByName to return false for missing stack")
	}
}

func TestLoadBundleContextCanceled(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := LoadBundle(ctx, filepath.Join(t.TempDir(), "config.json"))
	if err == nil {
		t.Fatalf("expected error for canceled context, got nil")
	}
}

func TestResolveConfigDir(t *testing.T) {
	cases := []struct {
		name       string
		input      string
		wantSuffix string
		mockAbsErr bool
	}{
		{
			name:       "successful absolute path resolution",
			input:      "configs/app.json",
			mockAbsErr: false,
		},
		{
			name:       "fallback to dir on filepath abs error",
			input:      "configs/app.json",
			wantSuffix: "configs",
			mockAbsErr: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.mockAbsErr {
				orig := filepathAbs
				filepathAbs = func(string) (string, error) {
					return "", errors.New("abs error")
				}
				defer func() { filepathAbs = orig }()
			}

			got := resolveConfigDir(tc.input)
			if tc.mockAbsErr && got != tc.wantSuffix {
				t.Fatalf("expected fallback dir %q, got %q", tc.wantSuffix, got)
			}
			if !tc.mockAbsErr && got == "" {
				t.Fatalf("expected non-empty config dir")
			}
		})
	}
}

func TestLoadProfilesErrorsAndFiltering(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	filePath := filepath.Join(root, "regular_file.txt")
	writeTestFile(t, filePath, "not a dir")

	if _, err := loadProfiles(filePath); err == nil {
		t.Fatalf("expected error loading profiles from regular file, got nil")
	}

	profilesDir := filepath.Join(root, "profiles")
	writeTestFile(t, filepath.Join(profilesDir, "sub", "ignored.json"), "{}")
	writeTestFile(t, filepath.Join(profilesDir, "readme.txt"), "documentation")
	writeTestFile(t, filepath.Join(profilesDir, "valid.json"), `{"vars":{"env":"prod"}}`)

	profiles, err := loadProfiles(profilesDir)
	if err != nil {
		t.Fatalf("unexpected error loading profiles: %v", err)
	}
	if len(profiles) != 1 || profiles["valid"].Vars["env"] != "prod" {
		t.Fatalf("unexpected loaded profiles: %+v", profiles)
	}

	badProfilesDir := filepath.Join(root, "bad_profiles")
	writeTestFile(t, filepath.Join(badProfilesDir, "corrupted.json"), "{broken")
	if _, err := loadProfiles(badProfilesDir); err == nil {
		t.Fatalf("expected error for corrupted profile, got nil")
	}
}

func TestLoadStacksErrorsAndFiltering(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	global := GlobalConfig{
		ConfDir:   filepath.Join(root, "absent_conf_d"),
		ZstdLevel: 3,
		MinFreeGB: 5,
		Retention: RetentionConfig{
			LocalCount:  1,
			RemoteCount: 1,
		},
		LVM: LVMConfig{
			VGName:       "vg_test",
			LVName:       "lv_test",
			SnapshotName: "snap_test",
			SnapshotSize: "5G",
			MountPoint:   "/mnt/test",
		},
	}

	if _, err := loadStacks(&global, nil); err == nil {
		t.Fatalf("expected error for missing conf_dir, got nil")
	}

	confFile := filepath.Join(root, "file_as_conf_dir.txt")
	writeTestFile(t, confFile, "file")
	global.ConfDir = confFile
	if _, err := loadStacks(&global, nil); err == nil {
		t.Fatalf("expected error for regular file as conf_dir, got nil")
	}

	validConfDir := filepath.Join(root, "conf_valid")
	writeTestFile(t, filepath.Join(validConfDir, "sub", "ignored.json"), "{}")
	writeTestFile(t, filepath.Join(validConfDir, "ignore.txt"), "text")
	writeTestFile(t, filepath.Join(validConfDir, "app.json"), "{}")

	global.ConfDir = validConfDir
	stacks, err := loadStacks(&global, nil)
	if err != nil {
		t.Fatalf("unexpected error loading valid stacks: %v", err)
	}
	if len(stacks) != 1 || stacks[0].Name != "app" {
		t.Fatalf("expected 1 stack named app, got %+v", stacks)
	}

	badConfDir := filepath.Join(root, "conf_bad")
	writeTestFile(t, filepath.Join(badConfDir, "bad name.json"), "{}")
	global.ConfDir = badConfDir
	if _, err := loadStacks(&global, nil); err == nil {
		t.Fatalf("expected validation error for invalid stack file name, got nil")
	}
}

func TestDecodeStrictJSONFileAndGlobalConfig(t *testing.T) {
	t.Parallel()

	var dummy any
	if err := decodeStrictJSONFile(filepath.Join(t.TempDir(), "nonexistent.json"), &dummy); err == nil {
		t.Fatalf("expected error reading nonexistent file, got nil")
	}

	dir := t.TempDir()
	absProfilesDir := filepath.Join(dir, "abs_profiles")
	absConfDir := filepath.Join(dir, "abs_conf_d")
	cfgPath := filepath.Join(dir, "config.json")

	writeTestFile(t, cfgPath, fmt.Sprintf(`{
		"profiles_dir": %q,
		"conf_dir": %q,
		"lvm": {
			"vg_name": "vg_main",
			"lv_name": "lv_main",
			"snapshot_name": "snap_main",
			"snapshot_size": "10G",
			"mount_point": "/mnt/main"
		}
	}`, absProfilesDir, absConfDir))

	global, err := loadGlobalConfig(cfgPath)
	if err != nil {
		t.Fatalf("unexpected error loading global config: %v", err)
	}
	if global.ProfilesDir != absProfilesDir || global.ConfDir != absConfDir {
		t.Fatalf("expected absolute paths to be preserved, got profiles=%q conf=%q", global.ProfilesDir, global.ConfDir)
	}
	if global.MaxArchiveGB != DefaultMaxArchiveGB || global.RestoreWorkDir != "" {
		t.Fatalf("expected default max_archive_gb=%d and empty restore_work_dir, got %d and %q", DefaultMaxArchiveGB, global.MaxArchiveGB, global.RestoreWorkDir)
	}

	customCfgPath := filepath.Join(dir, "custom.json")
	writeTestFile(t, customCfgPath, `{
		"restore_work_dir": " /home/.bro-work/ ",
		"max_archive_gb": 250,
		"lvm": {
			"vg_name": "vg_main",
			"lv_name": "lv_main",
			"snapshot_name": "snap_main",
			"snapshot_size": "10G",
			"mount_point": "/mnt/main"
		}
	}`)
	customGlobal, err := loadGlobalConfig(customCfgPath)
	if err != nil {
		t.Fatalf("unexpected error loading custom global config: %v", err)
	}
	if customGlobal.RestoreWorkDir != "/home/.bro-work" || customGlobal.MaxArchiveGB != 250 {
		t.Fatalf("unexpected custom fields: restore_work_dir=%q max_archive_gb=%d", customGlobal.RestoreWorkDir, customGlobal.MaxArchiveGB)
	}
}

func TestLoadBundleWithProfilesError(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	badProfilesDir := filepath.Join(dir, "profiles")
	writeTestFile(t, filepath.Join(badProfilesDir, "bad.json"), "{corrupted")
	cfgPath := filepath.Join(dir, "config.json")
	writeTestFile(t, cfgPath, fmt.Sprintf(`{
		"profiles_dir": %q,
		"lvm": {
			"vg_name": "vg",
			"lv_name": "lv",
			"snapshot_name": "snap",
			"snapshot_size": "5G",
			"mount_point": "/mnt"
		}
	}`, badProfilesDir))

	if _, err := LoadBundle(context.Background(), cfgPath); err == nil {
		t.Fatalf("expected LoadBundle to fail when profile decoding fails, got nil")
	}
}

func TestLoadGlobalStandalone(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	canceledCtx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := LoadGlobal(canceledCtx, "/nonexistent/config.json"); err == nil {
		t.Fatal("expected canceled context error from LoadGlobal")
	}

	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "standalone.json")
	writeTestFile(t, cfgPath, fmt.Sprintf(`{
		"conf_dir": %q,
		"max_archive_gb": 300,
		"lvm": {
			"vg_name": "vg_dr",
			"lv_name": "lv_dr",
			"snapshot_name": "snap_dr",
			"snapshot_size": "5G",
			"mount_point": "/mnt/dr"
		}
	}`, filepath.Join(dir, "missing_conf_d")))

	global, err := LoadGlobal(ctx, cfgPath)
	if err != nil {
		t.Fatalf("expected LoadGlobal to succeed without conf_dir, got: %v", err)
	}
	if global.MaxArchiveGB != 300 || global.MinFreeGB != DefaultMinFreeGB {
		t.Fatalf("unexpected global config limits: max_archive_gb=%d min_free_gb=%d", global.MaxArchiveGB, global.MinFreeGB)
	}

	invalidCfgPath := filepath.Join(dir, "invalid.json")
	writeTestFile(t, invalidCfgPath, `{
		"max_archive_gb": 0,
		"lvm": {
			"vg_name": "vg_dr",
			"lv_name": "lv_dr",
			"snapshot_name": "snap_dr",
			"snapshot_size": "5G",
			"mount_point": "/mnt/dr"
		}
	}`)
	if _, err := LoadGlobal(ctx, invalidCfgPath); err == nil {
		t.Fatal("expected validation error for max_archive_gb=0")
	}
}

func TestLoadExampleConfigs(t *testing.T) {
	t.Parallel()

	bundle, err := LoadBundle(context.Background(), filepath.Join("..", "..", "examples", "etc", "bro", "config.json"))
	if err != nil {
		t.Fatalf("expected example bundle to load cleanly, got: %v", err)
	}
	if bundle.ReverseProxyStack() == nil {
		t.Fatal("expected example bundle to define a reverse_proxy_stack")
	}
}
