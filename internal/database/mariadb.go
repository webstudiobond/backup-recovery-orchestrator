package database

import (
	"context"
	"io"

	"github.com/webstudiobond/backup-recovery-orchestrator/internal/config"
)

const driverMariaDB = "mariadb"

const mariaDumpContainerScript = `if [ -n "$1" ]; then u="$(cat "$1")"; else u="$2"; fi
if [ -n "$3" ]; then d="$(cat "$3")"; else d="$4"; fi
if [ -n "$5" ]; then p="$(cat "$5")"; export MYSQL_PWD="$p"; fi
set -- --single-transaction --quick --skip-dump-date
if [ -n "$6" ]; then set -- -h "$6" "$@"; fi
if [ -n "$u" ]; then set -- -u "$u" "$@"; fi
if [ -n "$d" ]; then set -- "$@" "$d"; fi
exec mariadb-dump "$@"`

const mariaImportContainerScript = `if [ -n "$1" ]; then u="$(cat "$1")"; else u="$2"; fi
if [ -n "$3" ]; then d="$(cat "$3")"; else d="$4"; fi
if [ -n "$5" ]; then p="$(cat "$5")"; export MYSQL_PWD="$p"; fi
set --
if [ -n "$6" ]; then set -- -h "$6" "$@"; fi
if [ -n "$u" ]; then set -- -u "$u" "$@"; fi
if [ -n "$d" ]; then set -- "$@" "$d"; fi
exec mariadb "$@"`

func (e *Engine) dumpMariaDB(ctx context.Context, composeFile string, db *config.DatabaseConfig, stdout io.Writer) error {
	return e.execWithSecrets(ctx, composeFile, db, mariaDumpContainerScript, "MYSQL_PWD", func(creds resolvedCreds) []string {
		return buildMariaDirectArgs("mariadb-dump", db.Host, creds.User, creds.Database, "--single-transaction", "--quick", "--skip-dump-date")
	}, nil, stdout)
}

func (e *Engine) importMariaDB(ctx context.Context, composeFile string, db *config.DatabaseConfig, stdin io.Reader) error {
	return e.execWithSecrets(ctx, composeFile, db, mariaImportContainerScript, "MYSQL_PWD", func(creds resolvedCreds) []string {
		return buildMariaDirectArgs("mariadb", db.Host, creds.User, creds.Database)
	}, stdin, nil)
}

func buildMariaDirectArgs(bin, host, dbUser, dbName string, flags ...string) []string {
	args := make([]string, 0, 6+len(flags))
	args = append(args, bin)
	args = append(args, flags...)
	if host != "" {
		args = append(args, "-h", host)
	}
	if dbUser != "" {
		args = append(args, "-u", dbUser)
	}
	if dbName != "" {
		args = append(args, dbName)
	}
	return args
}
