package config

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestProfileVarsExpansion(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	writeTestFile(t, cfgPath, `{
  "lvm": {
    "vg_name": "ubuntu-vg",
    "lv_name": "lv_home",
    "snapshot_name": "home_snap",
    "snapshot_size": "35G",
    "mount_point": "/mnt/home_snap"
  }
}`)
	writeTestFile(t, filepath.Join(dir, "profiles", "cms.json"), `{
  "vars": {
    "data_dir": "data",
    "content_dir": "wp-content"
  },
  "files": {
    "no_wildcards_match_slash": true,
    "excludes": ["./{data_dir}/{content_dir}/cache/*"]
  },
  "dac": [
    { "path": "{data_dir}/{content_dir}/mu-plugins/loader.php", "owner": "root:stack", "file_mode": "0440", "optional": true }
  ],
  "host_files": [
    { "path": "/etc/cron.d/cms-{stack}", "owner": "root:root", "file_mode": "0644", "optional": true }
  ],
  "hooks": {
    "pre_snapshot": [
      { "action": "remove_path", "path": "/home/{stack}/{data_dir}/ckpt" }
    ]
  },
  "restore_steps": [
    { "action": "extract_files" },
    { "action": "move_contents", "from": "{data_dir}/ckpt", "to": "{data_dir}/live" }
  ]
}`)
	writeTestFile(t, filepath.Join(dir, "conf.d", "portal.json"), `{
  "extends": "cms",
  "vars": {
    "content_dir": "assets"
  }
}`)

	bundle, err := LoadBundle(context.Background(), cfgPath)
	if err != nil {
		t.Fatalf("LoadBundle failed: %v", err)
	}
	stack, ok := bundle.StackByName("portal")
	if !ok {
		t.Fatalf("expected stack portal")
	}
	if len(stack.Files.Excludes) != 1 || stack.Files.Excludes[0] != "./data/assets/cache/*" {
		t.Fatalf("unexpected expanded excludes: %+v", stack.Files.Excludes)
	}
	if len(stack.DAC) != 1 || stack.DAC[0].Path != "data/assets/mu-plugins/loader.php" {
		t.Fatalf("unexpected expanded dac: %+v", stack.DAC)
	}
	if len(stack.HostFiles) != 1 || stack.HostFiles[0].Path != "/etc/cron.d/cms-portal" {
		t.Fatalf("unexpected expanded host_files: %+v", stack.HostFiles)
	}
	if len(stack.Hooks.PreSnapshot) != 1 || stack.Hooks.PreSnapshot[0].Path != "/home/portal/data/ckpt" {
		t.Fatalf("unexpected expanded hooks: %+v", stack.Hooks.PreSnapshot)
	}
	if len(stack.RestoreSteps) != 2 || stack.RestoreSteps[1].From != "data/ckpt" || stack.RestoreSteps[1].To != "data/live" {
		t.Fatalf("unexpected expanded restore_steps: %+v", stack.RestoreSteps)
	}
}

func TestExpandStackVarsErrors(t *testing.T) {
	t.Parallel()

	cases := []struct {
		stack       *StackConfig
		name        string
		errContains string
	}{
		{
			name: "invalid stack vars identifier",
			stack: &StackConfig{
				Vars: map[string]string{"invalid var": "val"},
			},
			errContains: "contains invalid character",
		},
		{
			name: "unresolved variable in files excludes",
			stack: &StackConfig{
				Files: FilesConfig{
					Excludes: []string{"/cache/{unknown_ex}"},
				},
			},
			errContains: "files.excludes",
		},
		{
			name: "unresolved variable in dac path",
			stack: &StackConfig{
				DAC: []DACRule{
					{Path: "/home/{missing_dac}"},
				},
			},
			errContains: "dac.path",
		},
		{
			name: "unresolved variable in host files path",
			stack: &StackConfig{
				HostFiles: []HostFileConfig{
					{Path: "/etc/{missing_host}"},
				},
			},
			errContains: "host_files.path",
		},
		{
			name: "unresolved variable in hooks pre dump",
			stack: &StackConfig{
				Hooks: HooksConfig{
					PreDump: []HookStep{
						{Path: "/bin/{missing_pre_dump}"},
					},
				},
			},
			errContains: "hooks.pre_dump",
		},
		{
			name: "unresolved variable in hooks pre snapshot",
			stack: &StackConfig{
				Hooks: HooksConfig{
					PreSnapshot: []HookStep{
						{Path: "/bin/{missing_pre_snap}"},
					},
				},
			},
			errContains: "hooks.pre_snapshot",
		},
		{
			name: "unresolved variable in hooks post snapshot",
			stack: &StackConfig{
				Hooks: HooksConfig{
					PostSnapshot: []HookStep{
						{Path: "/bin/{missing_post_snap}"},
					},
				},
			},
			errContains: "hooks.post_snapshot",
		},
		{
			name: "unresolved variable in restore steps from",
			stack: &StackConfig{
				RestoreSteps: []RestoreStep{
					{From: "{missing_from}"},
				},
			},
			errContains: "restore_steps.from",
		},
		{
			name: "unresolved variable in restore steps to",
			stack: &StackConfig{
				RestoreSteps: []RestoreStep{
					{To: "{missing_to}"},
				},
			},
			errContains: "restore_steps.to",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if tc.stack.Name == "" {
				tc.stack.Name = "app"
			}
			err := expandStackVars(tc.stack)
			if err == nil || !strings.Contains(err.Error(), tc.errContains) {
				t.Fatalf("expected error containing %q, got %v", tc.errContains, err)
			}
		})
	}
}
