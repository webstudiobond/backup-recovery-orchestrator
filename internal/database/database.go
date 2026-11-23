// Package database implements online containerized database export and restoration drivers.
package database

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/webstudiobond/backup-recovery-orchestrator/internal/cmdexec"
	"github.com/webstudiobond/backup-recovery-orchestrator/internal/config"
	"github.com/webstudiobond/backup-recovery-orchestrator/internal/docker"
)

// Engine orchestrates zstd-compressed database dumps and restores across configured database drivers.
type Engine struct {
	Docker *docker.Client
	Runner cmdexec.Runner
}

type resolvedCreds struct {
	User     string
	Database string
	Password string
}

// NewEngine creates a database engine backed by the given command runner and Docker Compose client.
func NewEngine(runner cmdexec.Runner, dockerClient *docker.Client) *Engine {
	if runner == nil {
		runner = cmdexec.OSRunner{}
	}
	if dockerClient == nil {
		dockerClient = docker.NewClient(runner)
	}
	return &Engine{
		Docker: dockerClient,
		Runner: runner,
	}
}

type pipeReader interface {
	io.Reader
	CloseWithError(error) error
}

type pipeWriter interface {
	io.Writer
	CloseWithError(error) error
}

func defaultPipe() (pipeReader, pipeWriter) {
	return io.Pipe()
}

func defaultCloseFile(f *os.File) error {
	if err := f.Close(); err != nil {
		return fmt.Errorf("close file: %w", err)
	}
	return nil
}

var (
	newPipe   = defaultPipe
	closeFile = defaultCloseFile
)

// DumpToFile exports a database stream and compresses it on the fly with zstd into outPath.
func (e *Engine) DumpToFile(ctx context.Context, composeFile string, db *config.DatabaseConfig, zstdLevel int, outPath string) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("context canceled before dumping database %q: %w", db.ID, err)
	}
	if err := os.MkdirAll(filepath.Dir(outPath), 0o700); err != nil {
		return fmt.Errorf("create database staging directory: %w", err)
	}

	outFile, err := os.OpenFile(filepath.Clean(outPath), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("create database dump file %q: %w", outPath, err)
	}

	pr, pw := newPipe()
	var (
		dumpErr error
		zstdErr error
		wg      sync.WaitGroup
	)
	wg.Add(2)

	go func() {
		defer wg.Done()
		dumpErr = e.dumpStream(ctx, composeFile, db, pw)
		if closeErr := pw.CloseWithError(dumpErr); closeErr != nil && dumpErr == nil {
			dumpErr = closeErr
		}
	}()

	go func() {
		defer wg.Done()
		zstdErr = e.Runner.Run(ctx, &cmdexec.Spec{
			Name:   "zstd",
			Args:   []string{fmt.Sprintf("-%d", zstdLevel), "-q"},
			Stdin:  pr,
			Stdout: outFile,
		})
		if closeErr := pr.CloseWithError(zstdErr); closeErr != nil && zstdErr == nil {
			zstdErr = closeErr
		}
	}()

	wg.Wait()
	closeFileErr := closeFile(outFile)
	if closeFileErr != nil {
		closeFileErr = fmt.Errorf("close database dump file %q: %w", outPath, closeFileErr)
	}

	if combinedErr := errors.Join(dumpErr, zstdErr, closeFileErr); combinedErr != nil {
		if rmErr := os.Remove(outPath); rmErr != nil && !errors.Is(rmErr, os.ErrNotExist) {
			return errors.Join(combinedErr, fmt.Errorf("remove partial dump %q: %w", outPath, rmErr))
		}
		return combinedErr
	}

	info, statErr := os.Stat(outPath)
	if statErr != nil {
		return fmt.Errorf("stat database dump %q: %w", outPath, statErr)
	}
	if info.Size() == 0 {
		return fmt.Errorf("database %q produced an empty dump archive %q", db.ID, outPath)
	}
	return nil
}

// RestoreFromFile decompresses a zstd database archive and streams it directly into the target container.
func (e *Engine) RestoreFromFile(ctx context.Context, composeFile string, db *config.DatabaseConfig, dumpPath string) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("context canceled before restoring database %q: %w", db.ID, err)
	}

	inFile, err := os.Open(filepath.Clean(dumpPath))
	if err != nil {
		return fmt.Errorf("open database dump %q: %w", dumpPath, err)
	}

	pr, pw := newPipe()
	var (
		zstdErr   error
		importErr error
		wg        sync.WaitGroup
	)
	wg.Add(2)

	go func() {
		defer wg.Done()
		zstdErr = e.Runner.Run(ctx, &cmdexec.Spec{
			Name:   "zstd",
			Args:   []string{"-dc"},
			Stdin:  inFile,
			Stdout: pw,
		})
		if closeErr := pw.CloseWithError(zstdErr); closeErr != nil && zstdErr == nil {
			zstdErr = closeErr
		}
	}()

	go func() {
		defer wg.Done()
		importErr = e.importStream(ctx, composeFile, db, pr)
		if closeErr := pr.CloseWithError(importErr); closeErr != nil && importErr == nil {
			importErr = closeErr
		}
	}()

	wg.Wait()
	closeFileErr := closeFile(inFile)
	if closeFileErr != nil {
		closeFileErr = fmt.Errorf("close database dump %q: %w", dumpPath, closeFileErr)
	}
	return errors.Join(zstdErr, importErr, closeFileErr)
}

func (e *Engine) dumpStream(ctx context.Context, composeFile string, db *config.DatabaseConfig, stdout io.Writer) error {
	switch db.Driver {
	case "postgres":
		return e.dumpPostgres(ctx, composeFile, db, stdout)
	case "mariadb":
		return e.dumpMariaDB(ctx, composeFile, db, stdout)
	case "wp-cli":
		return e.dumpWPCLI(ctx, composeFile, db, stdout)
	default:
		return fmt.Errorf("unsupported database driver %q", db.Driver)
	}
}

func (e *Engine) importStream(ctx context.Context, composeFile string, db *config.DatabaseConfig, stdin io.Reader) error {
	switch db.Driver {
	case "postgres":
		return e.importPostgres(ctx, composeFile, db, stdin)
	case "mariadb":
		return e.importMariaDB(ctx, composeFile, db, stdin)
	case "wp-cli":
		return e.importWPCLI(ctx, composeFile, db, stdin)
	default:
		return fmt.Errorf("unsupported database driver %q", db.Driver)
	}
}

func (e *Engine) execWithSecrets(
	ctx context.Context,
	composeFile string,
	db *config.DatabaseConfig,
	containerScript string,
	passEnvKey string,
	directCmdBuilder func(creds resolvedCreds) []string,
	stdin io.Reader,
	stdout io.Writer,
) error {
	if db.Secrets != nil && db.Secrets.Source == "container" {
		args := []string{
			"sh", "-c", containerScript, "_",
			db.Secrets.UserFile,
			db.DBUser,
			db.Secrets.DBFile,
			db.DBName,
			db.Secrets.PasswordFile,
			db.Host,
		}
		if err := e.Docker.Exec(ctx, composeFile, db.Service, nil, args, stdin, stdout); err != nil {
			return fmt.Errorf("docker exec %s (%s): %w", db.Service, db.ID, err)
		}
		return nil
	}

	creds, err := readHostCreds(db)
	if err != nil {
		return err
	}

	var env []string
	if creds.Password != "" {
		env = []string{passEnvKey + "=" + creds.Password}
	}

	cmdArgs := directCmdBuilder(creds)
	if execErr := e.Docker.Exec(ctx, composeFile, db.Service, env, cmdArgs, stdin, stdout); execErr != nil {
		return fmt.Errorf("docker exec %s (%s): %w", db.Service, db.ID, execErr)
	}
	return nil
}

func readHostCreds(db *config.DatabaseConfig) (resolvedCreds, error) {
	creds := resolvedCreds{
		User:     db.DBUser,
		Database: db.DBName,
	}
	if db.Secrets == nil || db.Secrets.Source != "host" {
		return creds, nil
	}

	var err error
	if db.Secrets.UserFile != "" {
		creds.User, err = readSecretValue(db.Secrets.UserFile)
		if err != nil {
			return resolvedCreds{}, err
		}
	}
	if db.Secrets.DBFile != "" {
		creds.Database, err = readSecretValue(db.Secrets.DBFile)
		if err != nil {
			return resolvedCreds{}, err
		}
	}
	if db.Secrets.PasswordFile != "" {
		creds.Password, err = readSecretValue(db.Secrets.PasswordFile)
		if err != nil {
			return resolvedCreds{}, err
		}
	}
	return creds, nil
}

func readSecretValue(path string) (string, error) {
	raw, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return "", fmt.Errorf("read secret file %q: %w", path, err)
	}
	return strings.TrimRight(string(raw), "\r\n"), nil
}
