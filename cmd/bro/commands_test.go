package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/webstudiobond/backup-recovery-orchestrator/internal/archive"
	"github.com/webstudiobond/backup-recovery-orchestrator/internal/config"
)

type commandTestCase struct {
	name      string
	wantOut   string
	wantErr   string
	customCfg string
	args      []string
	allFlag   bool
	withCfg   bool
	dryRun    bool
}

func runSubcommandSuite(ctx context.Context, t *testing.T, subcmd, cfgPath string, cases []commandTestCase) {
	t.Helper()

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			args := []string{subcmd}
			if tc.allFlag {
				args = append(args, "--all")
			}
			if tc.customCfg != "" {
				args = append(args, "--config", tc.customCfg)
			} else if tc.withCfg {
				args = append(args, "--config", cfgPath)
			}
			args = append(args, tc.args...)
			if tc.dryRun {
				args = append(args, "--dry-run")
			}

			var outBuf, errBuf bytes.Buffer
			err := run(ctx, args, &outBuf, &errBuf, fakeCLIRunner{})

			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("expected error containing %q, got %v", tc.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v (stderr=%s)", err, errBuf.String())
			}
			if tc.wantOut != "" && !strings.Contains(outBuf.String(), tc.wantOut) {
				t.Fatalf("expected stdout containing %q, got %q", tc.wantOut, outBuf.String())
			}
		})
	}
}

func runConfigSuite(ctx context.Context, t *testing.T, action, cfgPath string, cases []commandTestCase) {
	t.Helper()

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			args := []string{"config"}
			if action != "" {
				args = append(args, action)
			}
			if tc.customCfg != "" {
				args = append(args, "--config", tc.customCfg)
			} else if tc.withCfg {
				args = append(args, "--config", cfgPath)
			}
			args = append(args, tc.args...)

			var outBuf, errBuf bytes.Buffer
			err := run(ctx, args, &outBuf, &errBuf, fakeCLIRunner{})

			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("expected error containing %q, got %v", tc.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v (stderr=%s)", err, errBuf.String())
			}
			if tc.wantOut != "" && !strings.Contains(outBuf.String(), tc.wantOut) {
				t.Fatalf("expected stdout containing %q, got %q", tc.wantOut, outBuf.String())
			}
		})
	}
}

func TestGeneralCLICommands(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	cases := []struct {
		name    string
		wantOut string
		wantErr string
		args    []string
	}{
		{
			name:    "help output",
			args:    []string{"help"},
			wantOut: "Usage: bro",
		},
		{
			name:    "help short flag",
			args:    []string{"-h"},
			wantOut: "Subcommands:",
		},
		{
			name:    "help long flag",
			args:    []string{"--help"},
			wantOut: "backup      [--config FILE]",
		},
		{
			name:    "version subcommand",
			args:    []string{"version"},
			wantOut: "bro version " + AppVersion,
		},
		{
			name:    "version flag",
			args:    []string{"--version"},
			wantOut: "bro version " + AppVersion,
		},
		{
			name:    "short version flag",
			args:    []string{"-v"},
			wantOut: "bro version " + AppVersion,
		},
		{
			name:    "missing subcommand fails",
			wantErr: "subcommand is required",
		},
		{
			name:    "unknown subcommand fails",
			args:    []string{"bogus"},
			wantErr: "unknown subcommand",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var outBuf, errBuf bytes.Buffer
			err := run(ctx, tc.args, &outBuf, &errBuf, fakeCLIRunner{})
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("expected error containing %q, got %v", tc.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !strings.Contains(outBuf.String(), tc.wantOut) {
				t.Fatalf("expected stdout containing %q, got %q", tc.wantOut, outBuf.String())
			}
		})
	}
}

func TestBackupCommand(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := t.TempDir()
	cfgPath := writeFixtureConfig(t, root)

	runSubcommandSuite(ctx, t, "backup", cfgPath, []commandTestCase{
		{
			name:    "backup dry-run with all",
			allFlag: true,
			withCfg: true,
			dryRun:  true,
			wantOut: "stack=app-alpha",
		},
		{
			name:    "backup missing all or stack name",
			withCfg: true,
			wantErr: "specify --all",
		},
		{
			name:    "backup rejects unknown flag",
			args:    []string{"--bad-backup-flag"},
			wantErr: "parse backup flags",
		},
		{
			name:    "backup rejects all with explicit stack name",
			args:    []string{"extra-stack"},
			allFlag: true,
			withCfg: true,
			wantErr: "cannot combine --all",
		},
		{
			name:      "backup missing config path",
			args:      []string{"extra-stack"},
			customCfg: "/no-backup.json",
			wantErr:   "no-backup.json",
		},
		{
			name:    "backup unknown stack dry-run failure",
			args:    []string{"missing-stack"},
			withCfg: true,
			dryRun:  true,
			wantErr: "execute backup",
		},
		{
			name:    "backup unknown stack non-dry-run locking execution",
			args:    []string{"absent-stack"},
			withCfg: true,
			wantErr: "absent-stack",
		},
	})
}

func TestSelfBackupCommand(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := t.TempDir()
	cfgPath := writeFixtureConfig(t, root)

	badCfgPath := filepath.Join(root, "bad_backup_root_config.json")
	fileAsRoot := filepath.Join(root, "regular_file_root")
	if err := os.WriteFile(fileAsRoot, []byte("x"), 0o600); err != nil {
		t.Fatalf("write fileAsRoot: %v", err)
	}
	badJSON := `{"backup_root":"` + fileAsRoot + `","lock_file":"` + filepath.Join(root, "bad.lock") + `","lvm":{"vg_name":"ubuntu-vg","lv_name":"lv_home","snapshot_name":"home_snap","snapshot_size":"35G","mount_point":"` + filepath.Join(root, "mnt", "snap") + `"}}`
	if err := os.WriteFile(badCfgPath, []byte(badJSON), 0o600); err != nil {
		t.Fatalf("write bad config: %v", err)
	}

	runSubcommandSuite(ctx, t, "selfbackup", cfgPath, []commandTestCase{
		{
			name:    "selfbackup dry-run",
			withCfg: true,
			dryRun:  true,
			wantOut: "kind=self_backup",
		},
		{
			name:    "selfbackup rejects positional args",
			args:    []string{"extra"},
			withCfg: true,
			wantErr: "does not accept positional arguments",
		},
		{
			name:    "selfbackup rejects unknown flag",
			args:    []string{"--bad-self-flag"},
			wantErr: "parse selfbackup flags",
		},
		{
			name:      "selfbackup missing config path",
			customCfg: "/no-self.json",
			wantErr:   "no-self.json",
		},
		{
			name:    "selfbackup execution success",
			args:    []string{"--no-sync"},
			withCfg: true,
			wantOut: "stack=bro-self",
		},
		{
			name:      "selfbackup execution failure",
			args:      []string{"--no-sync"},
			customCfg: badCfgPath,
			wantErr:   "execute selfbackup",
		},
	})
}

func TestSyncCommand(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := t.TempDir()
	cfgPath := writeFixtureConfig(t, root)

	runSubcommandSuite(ctx, t, "sync", cfgPath, []commandTestCase{
		{
			name:    "sync missing all or stack name",
			withCfg: true,
			wantErr: "specify --all",
		},
		{
			name:    "sync rejects unknown flag",
			args:    []string{"--bad-sync-flag"},
			wantErr: "parse sync flags",
		},
		{
			name:    "sync rejects all with explicit stack name",
			args:    []string{"sync-alpha"},
			allFlag: true,
			withCfg: true,
			wantErr: "cannot combine --all",
		},
		{
			name:      "sync missing config path",
			args:      []string{"sync-beta"},
			customCfg: "/no-sync.json",
			wantErr:   "no-sync.json",
		},
		{
			name:    "sync execution success",
			allFlag: true,
			withCfg: true,
		},
		{
			name:    "sync missing destination failure",
			args:    []string{"--destination", "missing-remote", "sync-delta"},
			withCfg: true,
			wantErr: "execute sync",
		},
	})
}

func TestRestoreCommand(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	cfgPath := writeFixtureConfig(t, root)
	pPath, gPath := writeAccountFixtures(t, root)

	testPasswdPath = pPath
	testGroupPath = gPath
	orig := newOrchestrator
	newOrchestrator = mockNewOrchestrator
	t.Cleanup(func() {
		newOrchestrator = orig
		testPasswdPath = ""
		testGroupPath = ""
	})

	tarPath := createTestArchive(ctx, t, root, "app-beta", "")

	runSubcommandSuite(ctx, t, "restore", cfgPath, []commandTestCase{
		{
			name:    "restore missing archive argument",
			withCfg: true,
			wantErr: "requires exactly one <archive.tar>",
		},
		{
			name:    "restore rejects unknown flag",
			args:    []string{"--bad-restore-flag"},
			wantErr: "parse restore flags",
		},
		{
			name:      "restore missing config path",
			args:      []string{"-f", tarPath},
			customCfg: "/no-restore.json",
			wantErr:   "no-restore.json",
		},
		{
			name:    "restore dry-run success",
			args:    []string{"-f", tarPath},
			withCfg: true,
			dryRun:  true,
			wantOut: "Dry-run restore plan",
		},
		{
			name:    "restore bogus angie mode non-dry-run",
			args:    []string{"-f", tarPath, "--angie=bogus"},
			withCfg: true,
			wantErr: "unsupported angie restore mode",
		},
		{
			name:    "restore rejects unknown angie stream",
			args:    []string{"-f", tarPath, "--angie-streams=missing.inc"},
			withCfg: true,
			dryRun:  true,
			wantErr: "is not present in archive manifest",
		},
	})
}

func TestDACCommand(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	cfgPath := writeFixtureConfig(t, root)
	pPath, gPath := writeAccountFixtures(t, root)

	testPasswdPath = pPath
	testGroupPath = gPath
	orig := newOrchestrator
	newOrchestrator = mockNewOrchestrator
	t.Cleanup(func() {
		newOrchestrator = orig
		testPasswdPath = ""
		testGroupPath = ""
	})

	runSubcommandSuite(ctx, t, "dac", cfgPath, []commandTestCase{
		{
			name:    "dac missing stack argument",
			withCfg: true,
			wantErr: "dac requires exactly one <stack> argument",
		},
		{
			name:    "dac rejects multiple stack arguments",
			args:    []string{"stack-one", "stack-two"},
			withCfg: true,
			wantErr: "dac requires exactly one <stack> argument",
		},
		{
			name:    "dac rejects unknown flag",
			args:    []string{"--bad-dac-flag"},
			wantErr: "parse dac flags",
		},
		{
			name:      "dac missing config path",
			args:      []string{"dac-dummy"},
			customCfg: "/no-dac.json",
			wantErr:   "no-dac.json",
		},
		{
			name:    "dac unknown stack in config",
			args:    []string{"nonexistent"},
			withCfg: true,
			dryRun:  true,
			wantErr: "not found in configuration",
		},
		{
			name:    "dac host user check failure",
			args:    []string{"app-alpha"},
			withCfg: true,
			wantErr: "user check failed",
		},
		{
			name:    "dac dry-run success",
			args:    []string{"app-root"},
			withCfg: true,
			dryRun:  true,
			wantOut: "Dry-run DAC plan",
		},
	})
}

func TestVerifyCommand(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := t.TempDir()
	tarPath := createTestArchive(ctx, t, root, "app-beta", "")
	selfTarPath := createTestArchive(ctx, t, root, "self_backup", archive.SelfBackupKind)

	cases := []commandTestCase{
		{
			name:    "verify missing archive",
			wantErr: "(or -f <archive.tar>)",
		},
		{
			name:    "verify rejects unknown flag",
			args:    []string{"--bad-verify-flag"},
			wantErr: "parse verify flags",
		},
		{
			name:    "verify nonexistent archive",
			args:    []string{"/no-archive.tar"},
			wantErr: "verify container",
		},
		{
			name:    "verify valid archive positional",
			args:    []string{tarPath},
			wantOut: "verified archive",
		},
		{
			name:    "verify valid archive flag",
			args:    []string{"-f", tarPath},
			wantOut: "angie_streams=[]",
		},
		{
			name:    "verify self backup container",
			args:    []string{selfTarPath},
			wantOut: "kind=self_backup",
		},
	}

	runSubcommandSuite(ctx, t, "verify", "", cases)
}

func TestConfigCommand(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := t.TempDir()
	cfgPath := writeFixtureConfig(t, root)

	runConfigSuite(ctx, t, "", cfgPath, []commandTestCase{
		{
			name:    "config missing subcommand",
			wantErr: "config subcommand requires 'validate' or 'show'",
		},
		{
			name:    "config unknown subcommand",
			args:    []string{"bogus"},
			wantErr: "unknown config subcommand",
		},
	})

	runConfigSuite(ctx, t, "validate", cfgPath, []commandTestCase{
		{
			name:    "config validate success",
			withCfg: true,
			wantOut: "configuration valid:",
		},
		{
			name:    "config validate rejects unknown flag",
			args:    []string{"--bad-validate-flag"},
			wantErr: "parse config validate flags",
		},
		{
			name:      "config validate missing config",
			customCfg: "/no-validate.json",
			wantErr:   "config validation failed",
		},
	})

	runConfigSuite(ctx, t, "show", cfgPath, []commandTestCase{
		{
			name:    "config show success",
			args:    []string{"app-alpha"},
			withCfg: true,
			wantOut: `"name": "app-alpha"`,
		},
		{
			name:    "config show rejects unknown flag",
			args:    []string{"--bad-show-flag"},
			wantErr: "parse config show flags",
		},
		{
			name:    "config show requires stack argument",
			withCfg: true,
			wantErr: "config show requires exactly one <stack> argument",
		},
		{
			name:      "config show missing config",
			args:      []string{"cfg-dummy"},
			customCfg: "/no-show.json",
			wantErr:   "no-show.json",
		},
		{
			name:    "config show unknown stack",
			args:    []string{"unknown-stack"},
			withCfg: true,
			wantErr: "stack \"unknown-stack\" not found",
		},
	})
}

func TestConfigShowMarshalError(t *testing.T) {
	orig := jsonMarshalIndent
	jsonMarshalIndent = mockJSONMarshalIndentFail
	t.Cleanup(func() { jsonMarshalIndent = orig })

	root := t.TempDir()
	cfgPath := writeFixtureConfig(t, root)
	var outBuf, errBuf bytes.Buffer
	err := run(context.Background(), []string{"config", "show", "--config", cfgPath, "app-root"}, &outBuf, &errBuf, fakeCLIRunner{})
	if err == nil || !strings.Contains(err.Error(), "marshal resolved config") {
		t.Fatalf("expected marshal error, got %v", err)
	}
}

func createTestArchive(ctx context.Context, t *testing.T, root, stackName, kind string) string {
	t.Helper()

	stageDir := filepath.Join(root, "stage-"+stackName)
	if err := os.MkdirAll(stageDir, 0o700); err != nil {
		t.Fatalf("mkdir stage: %v", err)
	}
	filesName := archive.StackFilesArchiveName(stackName, "2026-11-16_10-00-00")
	if err := os.WriteFile(filepath.Join(stageDir, filesName), []byte("files"), 0o600); err != nil {
		t.Fatalf("write staged files: %v", err)
	}
	tarPath := filepath.Join(root, stackName+".tar")
	archEngine := &archive.Engine{Now: func() time.Time { return time.Date(2026, 11, 16, 10, 0, 0, 0, time.UTC) }}
	if err := archEngine.AssembleContainer(ctx, &archive.Manifest{
		SchemaVersion: archive.ManifestSchemaVersion,
		Kind:          kind,
		StackName:     stackName,
		StackDir:      filepath.Join(root, "home", stackName),
		ComposeFile:   filepath.Join(root, "home", stackName, "docker-compose.yaml"),
		FilesArchive:  filesName,
		User:          config.UserConfig{Name: "app-user", UID: 1011, GID: 1011},
	}, stageDir, tarPath); err != nil {
		t.Fatalf("AssembleContainer error: %v", err)
	}
	return tarPath
}
