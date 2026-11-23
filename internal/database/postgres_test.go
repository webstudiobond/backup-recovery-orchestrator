package database

import (
	"bytes"
	"context"
	"slices"
	"testing"

	"github.com/webstudiobond/backup-recovery-orchestrator/internal/config"
	"github.com/webstudiobond/backup-recovery-orchestrator/internal/docker"
)

func TestBuildPostgresDirectArgs(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		bin      string
		host     string
		dbUser   string
		dbName   string
		extra    []string
		wantArgs []string
	}{
		{
			name:     "all fields populated with extra flags",
			bin:      "mock_pg_dump",
			host:     "pg-host.internal",
			dbUser:   "pg_user",
			dbName:   "pg_database",
			extra:    []string{"--no-owner", "--no-acl"},
			wantArgs: []string{"mock_pg_dump", "-h", "pg-host.internal", "-U", "pg_user", "-d", "pg_database", "--no-owner", "--no-acl"},
		},
		{
			name:     "empty host user and database",
			bin:      "mock_psql",
			host:     "",
			dbUser:   "",
			dbName:   "",
			extra:    []string{"-v", "OPT=1"},
			wantArgs: []string{"mock_psql", "-v", "OPT=1"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := buildPostgresDirectArgs(tc.bin, tc.host, tc.dbUser, tc.dbName, tc.extra...)
			if !slices.Equal(got, tc.wantArgs) {
				t.Fatalf("buildPostgresDirectArgs() = %v, want %v", got, tc.wantArgs)
			}
		})
	}
}

func TestPostgresDirectDumpAndImport(t *testing.T) {
	t.Parallel()

	runner := &fakeDBRunner{dumpBytes: []byte("CREATE TABLE pg (id int);")}
	engine := NewEngine(runner, docker.NewClient(runner))

	dbCfg := &config.DatabaseConfig{
		ID:      "pg-direct",
		Service: "pg-svc",
		Host:    "localhost",
		DBUser:  "pg_admin",
		DBName:  "prod_db",
	}

	var out bytes.Buffer
	if err := engine.dumpPostgres(context.Background(), "/path/compose.yaml", dbCfg, &out); err != nil {
		t.Fatalf("dumpPostgres failed: %v", err)
	}

	in := bytes.NewReader([]byte("DROP TABLE pg;"))
	if err := engine.importPostgres(context.Background(), "/path/compose.yaml", dbCfg, in); err != nil {
		t.Fatalf("importPostgres failed: %v", err)
	}
}
