package database

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/webstudiobond/backup-recovery-orchestrator/internal/config"
	"github.com/webstudiobond/backup-recovery-orchestrator/internal/docker"
)

func TestWPCLIDumpAndImport(t *testing.T) {
	t.Parallel()

	runner := &fakeDBRunner{dumpBytes: []byte("wp dump payload")}
	engine := NewEngine(runner, docker.NewClient(runner))

	dbCfg := &config.DatabaseConfig{
		ID:      "wp-site",
		Service: "wordpress",
	}

	var out bytes.Buffer
	if err := engine.dumpWPCLI(context.Background(), "/path/compose.yaml", dbCfg, &out); err != nil {
		t.Fatalf("dumpWPCLI failed: %v", err)
	}

	in := bytes.NewReader([]byte("wp import payload"))
	if err := engine.importWPCLI(context.Background(), "/path/compose.yaml", dbCfg, in); err != nil {
		t.Fatalf("importWPCLI failed: %v", err)
	}
}

func TestWPCLIErrorPaths(t *testing.T) {
	t.Parallel()

	failingRunner := &fakeDBRunner{failOnCmd: "docker"}
	engine := NewEngine(failingRunner, docker.NewClient(failingRunner))

	dbCfg := &config.DatabaseConfig{
		ID:      "wp-failing",
		Service: "wordpress",
	}

	var out bytes.Buffer
	if err := engine.dumpWPCLI(context.Background(), "/path/compose.yaml", dbCfg, &out); err == nil || !strings.Contains(err.Error(), "wp-cli export (wp-failing)") {
		t.Fatalf("expected export error, got %v", err)
	}

	in := bytes.NewReader([]byte("sql"))
	if err := engine.importWPCLI(context.Background(), "/path/compose.yaml", dbCfg, in); err == nil || !strings.Contains(err.Error(), "wp-cli import (wp-failing)") {
		t.Fatalf("expected import error, got %v", err)
	}
}
