package database

import (
	"path/filepath"
	"testing"

	"github.com/webstudiobond/backup-recovery-orchestrator/internal/config"
)

func TestValidateDatabases(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		dbs     []config.DatabaseConfig
		wantErr bool
	}{
		{
			name: "invalid database identifier format",
			dbs: []config.DatabaseConfig{
				{ID: ".hidden_db", Driver: "couchdb", Service: "maria_service"},
			},
			wantErr: true,
		},
		{
			name: "duplicate database identifier detected",
			dbs: []config.DatabaseConfig{
				{ID: "shared_db_entry", Driver: driverMariaDB, Service: "maria_one"},
				{ID: "shared_db_entry", Driver: "sqlite", Service: "sqlite_two"},
			},
			wantErr: true,
		},
		{
			name: "driver validation failure",
			dbs: []config.DatabaseConfig{
				{ID: "db_bad_driver", Driver: "sqlite", Service: "sqlite_svc"},
			},
			wantErr: true,
		},
		{
			name: "secrets validation failure",
			dbs: []config.DatabaseConfig{
				{
					ID:      "db_bad_secrets",
					Driver:  driverMariaDB,
					Service: "maria_svc",
					Secrets: &config.DBSecretsConfig{Source: "invalid_src"},
				},
			},
			wantErr: true,
		},
		{
			name: "valid distinct databases configuration",
			dbs: []config.DatabaseConfig{
				{ID: "main_wp", Driver: driverWPCLI, Service: "wp_primary"},
				{ID: "main_pg", Driver: driverPostgres, Service: "postgres_primary"},
			},
			wantErr: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := ValidateDatabases(tc.dbs)
			if (err != nil) != tc.wantErr {
				t.Fatalf("ValidateDatabases() error = %v, wantErr = %v", err, tc.wantErr)
			}
		})
	}
}

func TestValidateDatabaseDriver(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		db      config.DatabaseConfig
		wantErr bool
	}{
		{
			name:    "wp driver missing service definition",
			db:      config.DatabaseConfig{ID: "db_wp_nosvc", Driver: driverWPCLI, Service: "\t "},
			wantErr: true,
		},
		{
			name:    "unsupported driver type",
			db:      config.DatabaseConfig{ID: "db_unsupported", Driver: "redis_db", Service: "redis_svc"},
			wantErr: true,
		},
		{
			name:    "valid postgres driver with service",
			db:      config.DatabaseConfig{ID: "valid_pg_id", Driver: driverPostgres, Service: "pg_inst"},
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
		secrets *config.DBSecretsConfig
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
			secrets: &config.DBSecretsConfig{
				Source:       "external_vault",
				PasswordFile: filepath.Join(string(filepath.Separator), "etc", "auth_token_file"),
			},
			wantErr: true,
		},
		{
			name: "all secret file paths empty rejected",
			secrets: &config.DBSecretsConfig{
				Source: secretsSourceContainer,
			},
			wantErr: true,
		},
		{
			name: "valid container secrets with password file",
			secrets: &config.DBSecretsConfig{
				Source:       secretsSourceContainer,
				PasswordFile: filepath.Join(string(filepath.Separator), "run", "secrets", "auth_token_file"),
			},
			wantErr: false,
		},
		{
			name: "valid host secrets with user and db file",
			secrets: &config.DBSecretsConfig{
				Source:   secretsSourceHost,
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
