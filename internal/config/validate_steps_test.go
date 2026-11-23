package config

import (
	"path/filepath"
	"testing"
)

func TestValidateDatabases(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		dbs     []DatabaseConfig
		wantErr bool
	}{
		{
			name: "invalid database identifier format",
			dbs: []DatabaseConfig{
				{ID: ".hidden_db", Driver: "couchdb", Service: "maria_service"},
			},
			wantErr: true,
		},
		{
			name: "duplicate database identifier detected",
			dbs: []DatabaseConfig{
				{ID: "shared_db_entry", Driver: "mariadb", Service: "maria_one"},
				{ID: "shared_db_entry", Driver: "sqlite", Service: "sqlite_two"},
			},
			wantErr: true,
		},
		{
			name: "driver validation failure",
			dbs: []DatabaseConfig{
				{ID: "db_bad_driver", Driver: "sqlite", Service: "sqlite_svc"},
			},
			wantErr: true,
		},
		{
			name: "secrets validation failure",
			dbs: []DatabaseConfig{
				{
					ID:      "db_bad_secrets",
					Driver:  "mariadb",
					Service: "maria_svc",
					Secrets: &DBSecretsConfig{Source: "invalid_src"},
				},
			},
			wantErr: true,
		},
		{
			name: "valid distinct databases configuration",
			dbs: []DatabaseConfig{
				{ID: "main_wp", Driver: "wp-cli", Service: "wp_primary"},
				{ID: "main_pg", Driver: "postgres", Service: "postgres_primary"},
			},
			wantErr: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := validateDatabases(tc.dbs)
			if (err != nil) != tc.wantErr {
				t.Fatalf("validateDatabases() error = %v, wantErr = %v", err, tc.wantErr)
			}
		})
	}
}

func TestValidateDatabaseDriver(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		db      DatabaseConfig
		wantErr bool
	}{
		{
			name:    "wp driver missing service definition",
			db:      DatabaseConfig{ID: "db_wp_nosvc", Driver: "wp-cli", Service: "\t "},
			wantErr: true,
		},
		{
			name:    "unsupported driver type",
			db:      DatabaseConfig{ID: "db_unsupported", Driver: "redis_db", Service: "redis_svc"},
			wantErr: true,
		},
		{
			name:    "valid postgres driver with service",
			db:      DatabaseConfig{ID: "valid_pg_id", Driver: "postgres", Service: "pg_inst"},
			wantErr: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := validateDatabaseDriver(&tc.db)
			if (err != nil) != tc.wantErr {
				t.Fatalf("validateDatabaseDriver() error = %v, wantErr = %v", err, tc.wantErr)
			}
		})
	}
}

func TestValidateDBSecrets(t *testing.T) {
	t.Parallel()

	cases := []struct {
		secrets *DBSecretsConfig
		name    string
		wantErr bool
	}{
		{
			name:    "nil secrets config returns nil",
			secrets: nil,
			wantErr: false,
		},
		{
			name: "unsupported secrets source location",
			secrets: &DBSecretsConfig{
				Source:       "external_vault",
				PasswordFile: filepath.Join(string(filepath.Separator), "etc", "auth_token_file"),
			},
			wantErr: true,
		},
		{
			name: "all secret file paths empty rejected",
			secrets: &DBSecretsConfig{
				Source: "container",
			},
			wantErr: true,
		},
		{
			name: "valid container secrets with password file",
			secrets: &DBSecretsConfig{
				Source:       "container",
				PasswordFile: filepath.Join(string(filepath.Separator), "run", "secrets", "auth_token_file"),
			},
			wantErr: false,
		},
		{
			name: "valid host secrets with user and db file",
			secrets: &DBSecretsConfig{
				Source:   "host",
				UserFile: "/etc/secrets/db_user_spec",
				DBFile:   "/etc/secrets/db_name_spec",
			},
			wantErr: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := validateDBSecrets("target_db_id", tc.secrets)
			if (err != nil) != tc.wantErr {
				t.Fatalf("validateDBSecrets() error = %v, wantErr = %v", err, tc.wantErr)
			}
		})
	}
}

func TestValidateHooks(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		hooks   HooksConfig
		wantErr bool
	}{
		{
			name: "invalid pre_dump hook step",
			hooks: HooksConfig{
				PreDump: []HookStep{
					{Action: "bad_action_predump"},
				},
			},
			wantErr: true,
		},
		{
			name: "invalid pre_snapshot hook step",
			hooks: HooksConfig{
				PreSnapshot: []HookStep{
					{Action: "bad_action_presnap"},
				},
			},
			wantErr: true,
		},
		{
			name: "invalid post_snapshot hook step",
			hooks: HooksConfig{
				PostSnapshot: []HookStep{
					{Action: "bad_action_postsnap"},
				},
			},
			wantErr: true,
		},
		{
			name: "valid hooks in all phases",
			hooks: HooksConfig{
				PreDump: []HookStep{
					{Action: "remove_path", Path: "/tmp/pre_dump.sock"},
				},
				PreSnapshot: []HookStep{
					{Action: "wait_for_file", Path: "/tmp/pre_snap.flag", TimeoutSec: 10},
				},
				PostSnapshot: []HookStep{
					{Action: "compose_kill", Service: "worker_backend", Signal: "SIGUSR1"},
				},
			},
			wantErr: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := validateHooks(&tc.hooks)
			if (err != nil) != tc.wantErr {
				t.Fatalf("validateHooks() error = %v, wantErr = %v", err, tc.wantErr)
			}
		})
	}
}

func TestValidateHookStepBasic(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		step    HookStep
		wantErr bool
	}{
		{
			name:    "remove_path with relative path",
			step:    HookStep{Action: "remove_path", Path: "rel/socket.sock"},
			wantErr: true,
		},
		{
			name:    "wait_for_file with zero timeout",
			step:    HookStep{Action: "wait_for_file", Path: "/var/run/file.lock", TimeoutSec: 0},
			wantErr: true,
		},
		{
			name:    "compose_kill missing signal",
			step:    HookStep{Action: "compose_kill", Service: "web_container"},
			wantErr: true,
		},
		{
			name:    "unsupported hook action name",
			step:    HookStep{Action: "unknown_hook_op"},
			wantErr: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := validateHookStep("pre_dump", &tc.step)
			if (err != nil) != tc.wantErr {
				t.Fatalf("validateHookStep() error = %v, wantErr = %v", err, tc.wantErr)
			}
		})
	}
}

func TestValidateHookStepComposeExec(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		service string
		command []string
		wantErr bool
	}{
		{
			name:    "hook compose exec missing command",
			service: "app_worker",
			wantErr: true,
		},
		{
			name:    "hook compose exec missing service",
			command: []string{"ls"},
			wantErr: true,
		},
		{
			name:    "valid step execution",
			service: "valid_worker",
			command: []string{"sync"},
			wantErr: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			step := HookStep{Action: "compose_exec", Service: tc.service, Command: tc.command}
			err := validateHookStep("pre_dump", &step)
			if (err != nil) != tc.wantErr {
				t.Fatalf("validateHookStep() error = %v, wantErr = %v", err, tc.wantErr)
			}
		})
	}
}

func TestValidateHookStepComposeRun(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		service string
		command []string
		wantErr bool
	}{
		{
			name:    "hook compose run missing command",
			service: "batch_runner",
			wantErr: true,
		},
		{
			name:    "hook compose run missing service",
			command: []string{"migrate"},
			wantErr: true,
		},
		{
			name:    "valid step run",
			service: "valid_migrator",
			command: []string{"up"},
			wantErr: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			step := HookStep{Action: "compose_run", Service: tc.service, Command: tc.command}
			err := validateHookStep("pre_dump", &step)
			if (err != nil) != tc.wantErr {
				t.Fatalf("validateHookStep() error = %v, wantErr = %v", err, tc.wantErr)
			}
		})
	}
}

func TestValidateRestoreStep(t *testing.T) {
	t.Parallel()

	knownDBs := map[string]struct{}{
		"orders_db": {},
	}

	cases := []struct {
		dbIDs   map[string]struct{}
		name    string
		step    RestoreStep
		wantErr bool
	}{
		{
			name:    "standard no-op restore action compose_down",
			step:    RestoreStep{Action: "compose_down"},
			dbIDs:   knownDBs,
			wantErr: false,
		},
		{
			name:    "standard no-op restore action extract_files",
			step:    RestoreStep{Action: "extract_files"},
			dbIDs:   knownDBs,
			wantErr: false,
		},
		{
			name:    "standard no-op restore action apply_dac",
			step:    RestoreStep{Action: "apply_dac"},
			dbIDs:   knownDBs,
			wantErr: false,
		},
		{
			name:    "standard no-op restore action extract_host_files",
			step:    RestoreStep{Action: "extract_host_files"},
			dbIDs:   knownDBs,
			wantErr: false,
		},
		{
			name:    "standard no-op restore action deploy_angie",
			step:    RestoreStep{Action: "deploy_angie"},
			dbIDs:   knownDBs,
			wantErr: false,
		},
		{
			name:    "standard no-op restore action compose_up",
			step:    RestoreStep{Action: "compose_up"},
			dbIDs:   knownDBs,
			wantErr: false,
		},
		{
			name:    "standard no-op restore action wait_healthy",
			step:    RestoreStep{Action: "wait_healthy"},
			dbIDs:   knownDBs,
			wantErr: false,
		},
		{
			name:    "standard no-op restore action compose_restart",
			step:    RestoreStep{Action: "compose_restart"},
			dbIDs:   knownDBs,
			wantErr: false,
		},
		{
			name:    "move_contents validation failure",
			step:    RestoreStep{Action: "move_contents", From: "data/source", To: "data/source"},
			dbIDs:   knownDBs,
			wantErr: true,
		},
		{
			name:    "unsupported restore step action",
			step:    RestoreStep{Action: "drop_all_tables"},
			dbIDs:   knownDBs,
			wantErr: true,
		},
	}

	for idx, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := validateRestoreStep(idx, &tc.step, tc.dbIDs)
			if (err != nil) != tc.wantErr {
				t.Fatalf("validateRestoreStep() error = %v, wantErr = %v", err, tc.wantErr)
			}
		})
	}
}

func TestValidateRestoreStepCompose(t *testing.T) {
	t.Parallel()

	emptyDBs := map[string]struct{}{}

	cases := []struct {
		name    string
		service string
		command []string
		args    []string
		wantErr bool
	}{
		{
			name:    "restore compose missing command and args",
			service: "app_service",
			wantErr: true,
		},
		{
			name:    "restore compose missing service",
			command: []string{"sh", "-c", "sync"},
			wantErr: true,
		},
		{
			name:    "valid with args slice",
			service: "batch_svc",
			args:    []string{"arg_val"},
			wantErr: false,
		},
	}

	for idx, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			step := RestoreStep{
				Action:  "compose_exec",
				Service: tc.service,
				Command: tc.command,
				Args:    tc.args,
			}
			err := validateRestoreStep(idx, &step, emptyDBs)
			if (err != nil) != tc.wantErr {
				t.Fatalf("validateRestoreStep() error = %v, wantErr = %v", err, tc.wantErr)
			}
		})
	}
}

func TestValidateRestoreStepDBImport(t *testing.T) {
	t.Parallel()

	knownCatalogDBs := map[string]struct{}{
		"catalog_db": {},
	}

	cases := []struct {
		dbIDs      map[string]struct{}
		name       string
		databaseID string
		wantErr    bool
	}{
		{
			name:       "empty db id and empty dbIDs map",
			dbIDs:      map[string]struct{}{},
			databaseID: "",
			wantErr:    true,
		},
		{
			name:       "empty db id and non-empty dbIDs map",
			dbIDs:      knownCatalogDBs,
			databaseID: "",
			wantErr:    false,
		},
		{
			name:       "known db id reference",
			dbIDs:      knownCatalogDBs,
			databaseID: "catalog_db",
			wantErr:    false,
		},
		{
			name:       "unknown db id reference",
			dbIDs:      knownCatalogDBs,
			databaseID: "missing_db",
			wantErr:    true,
		},
	}

	for idx, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			step := RestoreStep{Action: "db_import", DatabaseID: tc.databaseID}
			err := validateRestoreStep(idx, &step, tc.dbIDs)
			if (err != nil) != tc.wantErr {
				t.Fatalf("validateRestoreStep() error = %v, wantErr = %v", err, tc.wantErr)
			}
		})
	}
}
