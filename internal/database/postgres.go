package database

import (
	"context"
	"io"

	"github.com/webstudiobond/backup-recovery-orchestrator/internal/config"
)

const driverPostgres = "postgres"

const postgresDumpContainerScript = `if [ -n "$1" ]; then u="$(cat "$1")"; else u="$2"; fi
if [ -n "$3" ]; then d="$(cat "$3")"; else d="$4"; fi
if [ -n "$5" ]; then p="$(cat "$5")"; export PGPASSWORD="$p"; fi
set -- --no-owner --no-acl
if [ -n "$6" ]; then set -- -h "$6" "$@"; fi
if [ -n "$u" ]; then set -- -U "$u" "$@"; fi
if [ -n "$d" ]; then set -- -d "$d" "$@"; fi
exec pg_dump "$@"`

const postgresImportContainerScript = `if [ -n "$1" ]; then u="$(cat "$1")"; else u="$2"; fi
if [ -n "$3" ]; then d="$(cat "$3")"; else d="$4"; fi
if [ -n "$5" ]; then p="$(cat "$5")"; export PGPASSWORD="$p"; fi
set -- -v ON_ERROR_STOP=1
if [ -n "$6" ]; then set -- -h "$6" "$@"; fi
if [ -n "$u" ]; then set -- -U "$u" "$@"; fi
if [ -n "$d" ]; then set -- -d "$d" "$@"; fi
exec psql "$@"`

func (e *Engine) dumpPostgres(ctx context.Context, composeFile string, db *config.DatabaseConfig, stdout io.Writer) error {
	return e.execWithSecrets(ctx, composeFile, db, postgresDumpContainerScript, "PGPASSWORD", func(creds resolvedCreds) []string {
		return buildPostgresDirectArgs("pg_dump", db.Host, creds.User, creds.Database, "--no-owner", "--no-acl")
	}, nil, stdout)
}

func (e *Engine) importPostgres(ctx context.Context, composeFile string, db *config.DatabaseConfig, stdin io.Reader) error {
	return e.execWithSecrets(ctx, composeFile, db, postgresImportContainerScript, "PGPASSWORD", func(creds resolvedCreds) []string {
		return buildPostgresDirectArgs("psql", db.Host, creds.User, creds.Database, "-v", "ON_ERROR_STOP=1")
	}, stdin, nil)
}

func buildPostgresDirectArgs(bin, host, dbUser, dbName string, extra ...string) []string {
	args := make([]string, 0, 7+len(extra))
	args = append(args, bin)
	if host != "" {
		args = append(args, "-h", host)
	}
	if dbUser != "" {
		args = append(args, "-U", dbUser)
	}
	if dbName != "" {
		args = append(args, "-d", dbName)
	}
	args = append(args, extra...)
	return args
}
