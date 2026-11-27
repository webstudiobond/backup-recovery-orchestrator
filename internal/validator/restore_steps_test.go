package validator

import (
	"testing"

	"github.com/webstudiobond/backup-recovery-orchestrator/internal/config"
)

func TestValidateRestoreStep(t *testing.T) {
	t.Parallel()

	knownDBs := map[string]struct{}{
		"orders_db": {},
	}

	cases := []struct {
		dbIDs   map[string]struct{}
		name    string
		step    config.RestoreStep
		wantErr bool
	}{
		{
			name:    "standard no-op restore action compose_down",
			step:    config.RestoreStep{Action: config.RestoreActionComposeDown},
			dbIDs:   knownDBs,
			wantErr: false,
		},
		{
			name:    "standard no-op restore action extract_files",
			step:    config.RestoreStep{Action: config.RestoreActionExtractFiles},
			dbIDs:   knownDBs,
			wantErr: false,
		},
		{
			name:    "standard no-op restore action apply_dac",
			step:    config.RestoreStep{Action: config.RestoreActionApplyDAC},
			dbIDs:   knownDBs,
			wantErr: false,
		},
		{
			name:    "standard no-op restore action extract_host_files",
			step:    config.RestoreStep{Action: config.RestoreActionExtractHostFiles},
			dbIDs:   knownDBs,
			wantErr: false,
		},
		{
			name:    "standard no-op restore action deploy_reverse_proxy",
			step:    config.RestoreStep{Action: config.RestoreActionDeployReverseProxy},
			dbIDs:   knownDBs,
			wantErr: false,
		},
		{
			name:    "standard no-op restore action compose_up",
			step:    config.RestoreStep{Action: config.RestoreActionComposeUp},
			dbIDs:   knownDBs,
			wantErr: false,
		},
		{
			name:    "standard no-op restore action wait_healthy",
			step:    config.RestoreStep{Action: config.RestoreActionWaitHealthy},
			dbIDs:   knownDBs,
			wantErr: false,
		},
		{
			name:    "standard no-op restore action compose_restart",
			step:    config.RestoreStep{Action: config.RestoreActionComposeRestart},
			dbIDs:   knownDBs,
			wantErr: false,
		},
		{
			name:    "move_contents missing from",
			step:    config.RestoreStep{Action: config.RestoreActionMoveContents, To: "service/data"},
			dbIDs:   knownDBs,
			wantErr: true,
		},
		{
			name:    "move_contents escaping path",
			step:    config.RestoreStep{Action: config.RestoreActionMoveContents, From: "../outside", To: "service/data"},
			dbIDs:   knownDBs,
			wantErr: true,
		},
		{
			name:    "move_contents absolute target",
			step:    config.RestoreStep{Action: config.RestoreActionMoveContents, From: "daemon/ckpt", To: "/etc/evil"},
			dbIDs:   knownDBs,
			wantErr: true,
		},
		{
			name:    "move_contents target inside source",
			step:    config.RestoreStep{Action: config.RestoreActionMoveContents, From: "daemon/data", To: "daemon/data/sub"},
			dbIDs:   knownDBs,
			wantErr: true,
		},
		{
			name:    "move_contents identical paths",
			step:    config.RestoreStep{Action: config.RestoreActionMoveContents, From: "data/source", To: "data/source"},
			dbIDs:   knownDBs,
			wantErr: true,
		},
		{
			name:    "move_contents valid paths",
			step:    config.RestoreStep{Action: config.RestoreActionMoveContents, From: "data/stage", To: "data/live"},
			dbIDs:   knownDBs,
			wantErr: false,
		},
		{
			name:    "unsupported restore step action",
			step:    config.RestoreStep{Action: "drop_all_tables"},
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
			step := config.RestoreStep{
				Action:  config.RestoreActionComposeExec,
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
			step := config.RestoreStep{Action: config.RestoreActionDBImport, DatabaseID: tc.databaseID}
			err := validateRestoreStep(idx, &step, tc.dbIDs)
			if (err != nil) != tc.wantErr {
				t.Fatalf("validateRestoreStep() error = %v, wantErr = %v", err, tc.wantErr)
			}
		})
	}
}

func TestValidateRestoreSteps(t *testing.T) {
	t.Parallel()

	dbs := []config.DatabaseConfig{
		{ID: "billing_db", Driver: "postgres", Service: "pg"},
	}
	validSteps := []config.RestoreStep{
		{Action: config.RestoreActionComposeDown},
		{Action: config.RestoreActionDBImport, DatabaseID: "billing_db"},
	}
	if err := validateRestoreSteps(validSteps, dbs); err != nil {
		t.Fatalf("expected valid steps, got: %v", err)
	}
}
