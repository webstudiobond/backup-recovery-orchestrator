package database

import (
	"context"
	"fmt"
	"io"

	"github.com/webstudiobond/backup-recovery-orchestrator/internal/config"
)

func (e *Engine) dumpWPCLI(ctx context.Context, composeFile string, db *config.DatabaseConfig, stdout io.Writer) error {
	if err := e.Docker.Run(ctx, composeFile, db.Service, nil, []string{"db", "export", "--single-transaction", "--quick", "-"}, nil, stdout); err != nil {
		return fmt.Errorf("wp-cli export (%s): %w", db.ID, err)
	}
	return nil
}

func (e *Engine) importWPCLI(ctx context.Context, composeFile string, db *config.DatabaseConfig, stdin io.Reader) error {
	if err := e.Docker.Run(ctx, composeFile, db.Service, nil, []string{"db", "import", "-"}, stdin, nil); err != nil {
		return fmt.Errorf("wp-cli import (%s): %w", db.ID, err)
	}
	return nil
}
