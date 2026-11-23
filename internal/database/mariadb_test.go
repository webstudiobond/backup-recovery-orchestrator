package database

import (
	"bytes"
	"context"
	"slices"
	"testing"

	"github.com/webstudiobond/backup-recovery-orchestrator/internal/config"
	"github.com/webstudiobond/backup-recovery-orchestrator/internal/docker"
)

func TestBuildMariaDirectArgs(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		bin      string
		host     string
		dbUser   string
		dbName   string
		flags    []string
		wantArgs []string
	}{
		{
			name:     "all options set with flags",
			bin:      "mock-maria-dump",
			host:     "db-host.internal",
			dbUser:   "maria_admin",
			dbName:   "production_db",
			flags:    []string{"--single-transaction", "--quick"},
			wantArgs: []string{"mock-maria-dump", "--single-transaction", "--quick", "-h", "db-host.internal", "-u", "maria_admin", "production_db"},
		},
		{
			name:     "minimal options without flags",
			bin:      "mock-maria",
			host:     "",
			dbUser:   "",
			dbName:   "",
			flags:    nil,
			wantArgs: []string{"mock-maria"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := buildMariaDirectArgs(tc.bin, tc.host, tc.dbUser, tc.dbName, tc.flags...)
			if !slices.Equal(got, tc.wantArgs) {
				t.Fatalf("buildMariaDirectArgs() = %v, want %v", got, tc.wantArgs)
			}
		})
	}
}

func TestMariaDBDirectDumpAndImport(t *testing.T) {
	t.Parallel()

	runner := &fakeDBRunner{dumpBytes: []byte("CREATE TABLE maria (id int);")}
	engine := NewEngine(runner, docker.NewClient(runner))

	dbCfg := &config.DatabaseConfig{
		ID:      "maria-direct",
		Service: "mariadb-svc",
		Host:    "127.0.0.1",
		DBUser:  "root",
		DBName:  "app_db",
	}

	var out bytes.Buffer
	if err := engine.dumpMariaDB(context.Background(), "/path/compose.yaml", dbCfg, &out); err != nil {
		t.Fatalf("dumpMariaDB failed: %v", err)
	}

	in := bytes.NewReader([]byte("DROP TABLE maria;"))
	if err := engine.importMariaDB(context.Background(), "/path/compose.yaml", dbCfg, in); err != nil {
		t.Fatalf("importMariaDB failed: %v", err)
	}
}
