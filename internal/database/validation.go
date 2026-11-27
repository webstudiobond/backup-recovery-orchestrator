package database

import (
	"fmt"
	"strings"

	"github.com/webstudiobond/backup-recovery-orchestrator/internal/config"
	"github.com/webstudiobond/backup-recovery-orchestrator/internal/identifier"
)

// ValidateDatabases verifies unique identifiers, registered drivers, service names, and secret source configurations.
func ValidateDatabases(databases []config.DatabaseConfig) error {
	seen := make(map[string]struct{}, len(databases))
	for i := range databases {
		db := &databases[i]
		if err := identifier.Validate(db.ID, "database id"); err != nil {
			return fmt.Errorf("validate database id %q: %w", db.ID, err)
		}
		if _, dup := seen[db.ID]; dup {
			return fmt.Errorf("duplicate database id %q", db.ID)
		}
		seen[db.ID] = struct{}{}

		if err := validateDatabaseDriver(db); err != nil {
			return err
		}
		if err := validateDBSecrets(db.ID, db.Secrets); err != nil {
			return err
		}
	}
	return nil
}

func validateDatabaseDriver(db *config.DatabaseConfig) error {
	if _, supported := databaseDrivers[db.Driver]; !supported {
		return fmt.Errorf("database %q has unsupported driver %q", db.ID, db.Driver)
	}
	if strings.TrimSpace(db.Service) == "" {
		return fmt.Errorf("database %q requires service for driver %q", db.ID, db.Driver)
	}
	return nil
}

func validateDBSecrets(dbID string, secrets *config.DBSecretsConfig) error {
	if secrets == nil {
		return nil
	}
	switch secrets.Source {
	case secretsSourceContainer, secretsSourceHost:
	default:
		return fmt.Errorf("database %q secrets.source %q must be 'container' or 'host'", dbID, secrets.Source)
	}
	if secrets.PasswordFile == "" && secrets.UserFile == "" && secrets.DBFile == "" {
		return fmt.Errorf("database %q secrets block must define at least one secret file path", dbID)
	}
	return nil
}
