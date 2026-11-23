package database

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/webstudiobond/backup-recovery-orchestrator/internal/cmdexec"
	"github.com/webstudiobond/backup-recovery-orchestrator/internal/config"
	"github.com/webstudiobond/backup-recovery-orchestrator/internal/docker"
)

func TestNewEngineDefaults(t *testing.T) {
	t.Parallel()

	eng := NewEngine(nil, nil)
	if eng.Runner == nil || eng.Docker == nil {
		t.Fatalf("expected defaults initialized, got runner=%v, docker=%v", eng.Runner, eng.Docker)
	}
}

func TestDumpAndRestoreDrivers(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	composeFile := "/home/app/docker-compose.yaml"
	secretDir := t.TempDir()
	hostUserSecret, hostDBSecret, hostAuthSecret := writeHostSecretFixtures(t, secretDir)

	cases := []struct {
		name           string
		id             string
		driver         string
		service        string
		host           string
		wantDumpMatch  string
		wantImportTerm string
		useContainer   bool
		useHostSecrets bool
	}{
		{name: "postgres container secrets", id: "pg-main", driver: "postgres", service: "pg", host: "/var/run/postgresql", useContainer: true, wantDumpMatch: "pg_dump", wantImportTerm: "psql"},
		{name: "mariadb host secrets", id: "host-maria", driver: "mariadb", service: "db", useHostSecrets: true, wantDumpMatch: "MYSQL_PWD=s3cr3t", wantImportTerm: "appuser"},
		{name: "wp-cli driver", id: "wp", driver: "wp-cli", service: "wp", wantDumpMatch: "export", wantImportTerm: "import"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			dbCfg := config.DatabaseConfig{
				ID:      tc.id,
				Driver:  tc.driver,
				Service: tc.service,
				Host:    tc.host,
			}
			if tc.useContainer {
				secretsRoot := "/run/secrets"
				dbCfg.Secrets = &config.DBSecretsConfig{
					Source:       "container",
					UserFile:     filepath.Join(secretsRoot, "db_user"),
					DBFile:       filepath.Join(secretsRoot, "db_name"),
					PasswordFile: filepath.Join(secretsRoot, "db_auth"),
				}
			}
			if tc.useHostSecrets {
				dbCfg.Secrets = &config.DBSecretsConfig{
					Source:       "host",
					UserFile:     hostUserSecret,
					DBFile:       hostDBSecret,
					PasswordFile: hostAuthSecret,
				}
			}

			runner := &fakeDBRunner{dumpBytes: []byte("CREATE TABLE t(id int);")}
			engine := NewEngine(runner, docker.NewClient(runner))
			outDir := t.TempDir()
			fileName := dbCfg.ID + ".sql.zst"
			outPath := filepath.Join(outDir, fileName)

			if err := engine.DumpToFile(ctx, composeFile, &dbCfg, 6, outPath); err != nil {
				t.Fatalf("DumpToFile error: %v", err)
			}

			dirRoot, openErr := os.OpenRoot(outDir)
			if openErr != nil {
				t.Fatalf("open root %q: %v", outDir, openErr)
			}
			defer func() {
				if closeErr := dirRoot.Close(); closeErr != nil {
					t.Errorf("close root: %v", closeErr)
				}
			}()

			rawDump, readErr := dirRoot.ReadFile(fileName)
			if readErr != nil {
				t.Fatalf("read dump file: %v", readErr)
			}
			if string(rawDump) != "CREATE TABLE t(id int);" {
				t.Fatalf("unexpected dump file payload: %q", string(rawDump))
			}

			if !containsArgFragment(runner.calls, tc.wantDumpMatch) {
				t.Fatalf("expected dump call containing %q, calls=%+v", tc.wantDumpMatch, runner.calls)
			}

			runner.calls = nil
			if err := engine.RestoreFromFile(ctx, composeFile, &dbCfg, outPath); err != nil {
				t.Fatalf("RestoreFromFile error: %v", err)
			}
			if !containsArgFragment(runner.calls, tc.wantImportTerm) {
				t.Fatalf("expected restore call containing %q, calls=%+v", tc.wantImportTerm, runner.calls)
			}
		})
	}
}

func TestDumpAndRestoreErrors(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	composeFile := "/home/app/docker-compose.yaml"
	canceledCtx, cancel := context.WithCancel(ctx)
	cancel()

	cases := []struct {
		ctx         context.Context
		name        string
		failOnCmd   string
		driver      string
		errContains string
		dumpBytes   []byte
		isRestore   bool
		checkClean  bool
	}{
		{ctx: ctx, name: "empty dump removes output", dumpBytes: nil, errContains: "empty dump"},
		{ctx: ctx, name: "zstd failure cleans partial dump", dumpBytes: []byte("sql"), failOnCmd: "zstd", checkClean: true, errContains: "simulated failure"},
		{ctx: canceledCtx, name: "canceled dump", errContains: "context canceled"},
		{ctx: canceledCtx, name: "canceled restore", isRestore: true, errContains: "context canceled"},
		{ctx: ctx, name: "missing restore dump file", isRestore: true, errContains: "open database dump"},
		{ctx: ctx, name: "restore import failure", driver: "oracle", isRestore: true, errContains: "unsupported database driver"},
		{ctx: ctx, name: "unsupported driver", driver: "oracle", errContains: "unsupported database driver"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			runner := &fakeDBRunner{
				dumpBytes: tc.dumpBytes,
				failOnCmd: tc.failOnCmd,
			}
			engine := NewEngine(runner, docker.NewClient(runner))
			target := filepath.Join(t.TempDir(), "test.sql.zst")

			driver := tc.driver
			if driver == "" {
				driver = "postgres"
			}
			db := config.DatabaseConfig{ID: "db", Driver: driver, Service: "svc"}

			var err error
			if tc.isRestore {
				if tc.name == "restore import failure" {
					if writeErr := os.WriteFile(target, []byte("zstd_dump_data"), 0o600); writeErr != nil {
						t.Fatalf("write restore fixture failed: %v", writeErr)
					}
				}
				err = engine.RestoreFromFile(tc.ctx, composeFile, &db, target)
			} else {
				err = engine.DumpToFile(tc.ctx, composeFile, &db, 6, target)
			}

			if err == nil || !strings.Contains(err.Error(), tc.errContains) {
				t.Fatalf("expected error containing %q, got %v", tc.errContains, err)
			}
			if tc.checkClean {
				if _, statErr := os.Stat(target); !os.IsNotExist(statErr) {
					t.Fatalf("expected partial dump %q to be removed", target)
				}
			}
		})
	}
}

func TestDumpToFileOSFailures(t *testing.T) {
	t.Parallel()

	runner := &fakeDBRunner{dumpBytes: []byte("CREATE TABLE dummy(id int);")}
	engine := NewEngine(runner, docker.NewClient(runner))
	dbCfg := &config.DatabaseConfig{ID: "dummy", Driver: "wp-cli", Service: "wp"}

	tmpFile := filepath.Join(t.TempDir(), "regular_file")
	if err := os.WriteFile(tmpFile, []byte("data"), 0o600); err != nil {
		t.Fatalf("write temp file: %v", err)
	}
	invalidPath := filepath.Join(tmpFile, "sub", "dump.sql.zst")
	if err := engine.DumpToFile(context.Background(), "/compose.yaml", dbCfg, 6, invalidPath); err == nil || !strings.Contains(err.Error(), "create database staging directory") {
		t.Fatalf("expected staging directory creation error, got %v", err)
	}

	existingDir := t.TempDir()
	if err := engine.DumpToFile(context.Background(), "/compose.yaml", dbCfg, 6, existingDir); err == nil || !strings.Contains(err.Error(), "create database dump file") {
		t.Fatalf("expected file open error, got %v", err)
	}

	lockedParent := t.TempDir()
	partialTarget := filepath.Join(lockedParent, "partial.sql.zst")

	removeFailRunner := &fakeDBRunner{
		dumpBytes: []byte("sql data"),
		onRun: func(_ *cmdexec.Spec) {
			if rmErr := os.Remove(partialTarget); rmErr != nil {
				t.Errorf("remove: %v", rmErr)
			}
			if mkdirErr := os.Mkdir(partialTarget, 0o700); mkdirErr != nil {
				t.Errorf("mkdir target: %v", mkdirErr)
			}
			if writeErr := os.WriteFile(filepath.Join(partialTarget, "child_file"), []byte("data"), 0o600); writeErr != nil {
				t.Errorf("write child: %v", writeErr)
			}
		},
		failAll: true,
	}
	err := NewEngine(removeFailRunner, docker.NewClient(removeFailRunner)).DumpToFile(context.Background(), "/compose.yaml", dbCfg, 6, partialTarget)
	if err == nil || !strings.Contains(err.Error(), "remove partial dump") {
		t.Fatalf("expected remove partial dump error, got %v", err)
	}

	statDir := t.TempDir()
	statTarget := filepath.Join(statDir, "vanished.sql.zst")
	statFailRunner := &fakeDBRunner{
		dumpBytes: []byte("valid table"),
		onRun: func(_ *cmdexec.Spec) {
			if rmErr := os.Remove(statTarget); rmErr != nil && !errors.Is(rmErr, os.ErrNotExist) {
				t.Errorf("remove: %v", rmErr)
			}
		},
	}
	err = NewEngine(statFailRunner, docker.NewClient(statFailRunner)).DumpToFile(context.Background(), "/compose.yaml", dbCfg, 6, statTarget)
	if err == nil || !strings.Contains(err.Error(), "stat database dump") {
		t.Fatalf("expected stat database dump error, got %v", err)
	}
}

func TestReadSecretValueAndHostCreds(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()
	crlfSecret := filepath.Join(tmp, "crlf_pass")
	if err := os.WriteFile(crlfSecret, []byte("my_secret_token\r\n"), 0o600); err != nil {
		t.Fatalf("write secret file: %v", err)
	}

	val, err := readSecretValue(crlfSecret)
	if err != nil || val != "my_secret_token" {
		t.Fatalf("expected trimmed secret value, got %q, err=%v", val, err)
	}

	if _, missErr := readSecretValue(filepath.Join(tmp, "missing_secret")); missErr == nil {
		t.Fatalf("expected error on missing secret file")
	}

	noSecretsDB := &config.DatabaseConfig{DBUser: "alice", DBName: "alicedb"}
	creds, err := readHostCreds(noSecretsDB)
	if err != nil || creds.User != "alice" || creds.Database != "alicedb" {
		t.Fatalf("expected default creds, got %+v, err=%v", creds, err)
	}

	containerSecretsDB := &config.DatabaseConfig{
		DBUser:  "bob",
		DBName:  "bobdb",
		Secrets: &config.DBSecretsConfig{Source: "container_type"},
	}
	creds, err = readHostCreds(containerSecretsDB)
	if err != nil || creds.User != "bob" || creds.Database != "bobdb" {
		t.Fatalf("expected default creds for container source, got %+v, err=%v", creds, err)
	}

	missingPath := filepath.Join(tmp, "nonexistent")
	cases := []struct {
		name       string
		userFile   string
		dbFile     string
		passFile   string
		wantErrMsg string
	}{
		{name: "missing UserFile", userFile: missingPath, wantErrMsg: "UserFile"},
		{name: "missing DBFile", dbFile: missingPath, wantErrMsg: "DBFile"},
		{name: "missing PasswordFile", passFile: missingPath, wantErrMsg: "PasswordFile"},
	}
	engine := NewEngine(&fakeDBRunner{}, nil)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := &config.DatabaseConfig{
				Service: "db",
				Secrets: &config.DBSecretsConfig{
					Source:       "host",
					UserFile:     tc.userFile,
					DBFile:       tc.dbFile,
					PasswordFile: tc.passFile,
				},
			}
			if _, credErr := readHostCreds(db); credErr == nil {
				t.Fatalf("expected error on %s", tc.wantErrMsg)
			}
			if execErr := engine.execWithSecrets(context.Background(), "/compose.yaml", db, "echo test", "PASS", func(_ resolvedCreds) []string {
				return []string{"cmd"}
			}, nil, nil); execErr == nil {
				t.Fatalf("expected execWithSecrets to fail on %s", tc.wantErrMsg)
			}
		})
	}
}

func TestExecWithSecretsErrors(t *testing.T) {
	t.Parallel()

	failingRunner := &fakeDBRunner{failAll: true}
	engine := NewEngine(failingRunner, docker.NewClient(failingRunner))

	cases := []struct {
		name       string
		user       string
		wantErrMsg string
		hasSecrets bool
	}{
		{name: "container source exec failure", wantErrMsg: "docker exec db", hasSecrets: true},
		{name: "host source exec failure", user: "user", wantErrMsg: "docker exec db", hasSecrets: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := config.DatabaseConfig{
				ID:      tc.name,
				Service: "db",
				DBUser:  tc.user,
			}
			if tc.hasSecrets {
				db.Secrets = &config.DBSecretsConfig{
					Source: "container",
				}
			}
			err := engine.execWithSecrets(context.Background(), "/compose.yaml", &db, "echo test", "PASS", func(_ resolvedCreds) []string {
				return []string{"cmd"}
			}, nil, nil)
			if err == nil || !strings.Contains(err.Error(), tc.wantErrMsg) {
				t.Fatalf("expected error containing %q, got %v", tc.wantErrMsg, err)
			}
		})
	}
}

func TestImportStreamUnsupportedDriver(t *testing.T) {
	t.Parallel()

	engine := NewEngine(&fakeDBRunner{}, nil)
	db := &config.DatabaseConfig{Driver: "unsupported_drv"}
	if err := engine.importStream(context.Background(), "/compose.yaml", db, nil); err == nil {
		t.Fatalf("expected unsupported driver error")
	}
}

func TestPipeAndFileCloseErrors(t *testing.T) {
	origPipe := newPipe
	origClose := closeFile
	t.Cleanup(func() {
		newPipe = origPipe
		closeFile = origClose
	})

	cases := []struct {
		name        string
		pipeSetup   func() (pipeReader, pipeWriter)
		closeSetup  func(*os.File) error
		errContains string
		isRestore   bool
	}{
		{
			name:        "dump pipe writer close error",
			pipeSetup:   mockWriterFailingPipe,
			errContains: "pipe writer close failure",
		},
		{
			name:        "dump pipe reader close error",
			pipeSetup:   mockReaderFailingPipe,
			errContains: "pipe reader close failure",
		},
		{
			name:        "dump file close error",
			closeSetup:  mockFailingCloseFile,
			errContains: "close database dump file",
		},
		{
			name:        "restore pipe writer close error",
			pipeSetup:   mockWriterFailingPipe,
			errContains: "pipe writer close failure",
			isRestore:   true,
		},
		{
			name:        "restore pipe reader close error",
			pipeSetup:   mockReaderFailingPipe,
			errContains: "pipe reader close failure",
			isRestore:   true,
		},
		{
			name:        "restore file close error",
			closeSetup:  mockFailingCloseFile,
			errContains: "close database dump",
			isRestore:   true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			newPipe = origPipe
			closeFile = origClose
			if tc.pipeSetup != nil {
				newPipe = tc.pipeSetup
			}
			if tc.closeSetup != nil {
				closeFile = tc.closeSetup
			}

			runner := &fakeDBRunner{dumpBytes: []byte("CREATE TABLE t(id int);")}
			engine := NewEngine(runner, docker.NewClient(runner))
			dbCfg := &config.DatabaseConfig{ID: "db-pipe", Driver: "mariadb", Service: "db"}
			target := filepath.Join(t.TempDir(), "target.sql.zst")

			var err error
			if tc.isRestore {
				if writeErr := os.WriteFile(target, []byte("zstd_data"), 0o600); writeErr != nil {
					t.Fatalf("write restore fixture failed: %v", writeErr)
				}
				err = engine.RestoreFromFile(context.Background(), "/compose.yaml", dbCfg, target)
			} else {
				err = engine.DumpToFile(context.Background(), "/compose.yaml", dbCfg, 6, target)
			}

			if err == nil || !strings.Contains(err.Error(), tc.errContains) {
				t.Fatalf("expected error containing %q, got %v", tc.errContains, err)
			}
		})
	}

	closedFile, openErr := os.CreateTemp(t.TempDir(), "already-closed")
	if openErr != nil {
		t.Fatalf("create temp: %v", openErr)
	}
	if closeErr := closedFile.Close(); closeErr != nil {
		t.Fatalf("close temp: %v", closeErr)
	}
	if err := defaultCloseFile(closedFile); err == nil {
		t.Fatalf("expected error closing already closed file")
	}
}
