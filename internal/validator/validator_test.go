package validator

import (
	"context"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/webstudiobond/backup-recovery-orchestrator/internal/config"
)

func TestLoadGlobal(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	canceledCtx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := LoadGlobal(canceledCtx, "/nonexistent/config.json"); err == nil {
		t.Fatal("expected canceled context error from LoadGlobal")
	}

	dir := t.TempDir()
	validPath := filepath.Join(dir, "valid.json")
	writeValidatorTestFile(t, validPath, `{
		"max_archive_gb": 300,
		"lvm": {
			"vg_name": "vg_dr",
			"lv_name": "lv_dr",
			"snapshot_name": "snap_dr",
			"snapshot_size": "5G",
			"mount_point": "/mnt/dr"
		}
	}`)
	global, err := LoadGlobal(ctx, validPath)
	if err != nil {
		t.Fatalf("expected LoadGlobal success, got: %v", err)
	}
	if global.MaxArchiveGB != 300 {
		t.Fatalf("expected max_archive_gb 300, got %d", global.MaxArchiveGB)
	}

	invalidPath := filepath.Join(dir, "invalid.json")
	writeValidatorTestFile(t, invalidPath, `{
		"max_archive_gb": 0,
		"lvm": {
			"vg_name": "vg_dr",
			"lv_name": "lv_dr",
			"snapshot_name": "snap_dr",
			"snapshot_size": "5G",
			"mount_point": "/mnt/dr"
		}
	}`)
	if _, err := LoadGlobal(ctx, invalidPath); err == nil || !strings.Contains(err.Error(), "invalid global config") {
		t.Fatalf("expected invalid global config error, got %v", err)
	}
}

func TestLoadBundle(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	examplePath := filepath.Join("..", "..", "examples", "etc", "bro", "config.json")
	bundle, err := LoadBundle(ctx, examplePath)
	if err != nil {
		t.Fatalf("expected example bundle to validate cleanly, got: %v", err)
	}
	if bundle.Config().ReverseProxyStack() == nil {
		t.Fatal("expected example bundle to define a reverse_proxy_stack")
	}

	dir := t.TempDir()
	globalPath := filepath.Join(dir, "config.json")
	writeValidatorTestFile(t, globalPath, `{
		"backup_root": "/backups",
		"reverse_proxy_stack": "proxy-stack",
		"lvm": {"vg_name":"vg","lv_name":"lv","snapshot_name":"snap","snapshot_size":"35G","mount_point":"/mnt/snap"}
	}`)
	writeValidatorTestFile(t, filepath.Join(dir, "conf.d", "proxy-stack.json"), `{
		"user": {"name":"proxy","uid":1001,"gid":1001},
		"reverse_proxy_provider": {
			"type": "angie",
			"service_name": "edge",
			"certs_dir": "data/certs",
			"domains_dir": "data/conf.d/domains"
		},
		"reverse_proxy": {}
	}`)
	if _, err := LoadBundle(ctx, globalPath); err == nil || !strings.Contains(err.Error(), "must not define reverse_proxy") {
		t.Fatalf("expected empty reverse_proxy block rejection on proxy stack, got %v", err)
	}

	badGlobalPath := filepath.Join(dir, "bad-global.json")
	writeValidatorTestFile(t, badGlobalPath, `{"timezone":"mars"}`)
	if _, err := LoadBundle(ctx, badGlobalPath); err == nil || !strings.Contains(err.Error(), "invalid global config") {
		t.Fatalf("expected invalid global config error, got %v", err)
	}

	missingConfDirPath := filepath.Join(dir, "missing-confd.json")
	writeValidatorTestFile(t, missingConfDirPath, `{
		"conf_dir": "/nonexistent-conf-dir-for-validator-test",
		"lvm": {"vg_name":"vg","lv_name":"lv","snapshot_name":"snap","snapshot_size":"35G","mount_point":"/mnt/snap"}
	}`)
	if _, err := LoadBundle(ctx, missingConfDirPath); err == nil || !strings.Contains(err.Error(), "load bundle") {
		t.Fatalf("expected load bundle error, got %v", err)
	}

	badStackGlobal := filepath.Join(dir, "bad-stack-global.json")
	writeValidatorTestFile(t, badStackGlobal, `{
		"conf_dir": "`+filepath.Join(dir, "bad-confd")+`",
		"lvm": {"vg_name":"vg","lv_name":"lv","snapshot_name":"snap","snapshot_size":"35G","mount_point":"/mnt/snap"}
	}`)
	writeValidatorTestFile(t, filepath.Join(dir, "bad-confd", "app.json"), `{"zstd_level":99}`)
	if _, err := LoadBundle(ctx, badStackGlobal); err == nil || !strings.Contains(err.Error(), `validate stack "app"`) {
		t.Fatalf("expected stack validation error, got %v", err)
	}
}

func TestValidateBundle(t *testing.T) {
	t.Parallel()

	if _, err := ValidateBundle(nil); err == nil {
		t.Fatal("expected nil bundle rejection")
	}

	badGlobal := validTestGlobalConfig()
	badGlobal.MaxArchiveGB = 0
	if _, err := ValidateBundle(&config.Bundle{Global: badGlobal}); err == nil || !strings.Contains(err.Error(), "invalid global config:") {
		t.Fatalf("expected invalid global config error, got %v", err)
	}

	badStackGlobal := validTestGlobalConfig()
	badStack := validTestStackConfig()
	badStack.Destinations = []string{"missing_dest"}
	if _, err := ValidateBundle(&config.Bundle{Global: badStackGlobal, Stacks: []config.StackConfig{badStack}}); err == nil || !strings.Contains(err.Error(), "validate stack") {
		t.Fatalf("expected stack validation error, got %v", err)
	}

	crossGlobal := validTestGlobalConfig()
	crossGlobal.ReverseProxyStack = "missing-proxy"
	if _, err := ValidateBundle(&config.Bundle{Global: crossGlobal}); err == nil || !strings.Contains(err.Error(), "validate reverse proxy bundle") {
		t.Fatalf("expected cross-stack reverse proxy error, got %v", err)
	}

	okGlobal := validTestGlobalConfig()
	okGlobal.Destinations = []config.DestinationConfig{
		{Name: "primary_store", Type: config.DestTypeDir, BackupDir: "/var/backups_dir"},
	}
	okStack := validTestStackConfig()
	okStack.Destinations = []string{"primary_store"}
	validated, err := ValidateBundle(&config.Bundle{
		Global: okGlobal,
		Stacks: []config.StackConfig{okStack},
	})
	if err != nil {
		t.Fatalf("expected valid bundle, got: %v", err)
	}
	if len(validated.Config().Stacks) != 1 || validated.Config().Stacks[0].Name != okStack.Name {
		t.Fatalf("unexpected validated bundle contents: %+v", validated.Config())
	}
}

func TestBundleImmutabilityAndDuplicateStacks(t *testing.T) {
	t.Parallel()

	var nilBundle *Bundle
	if nilBundle.Config() != nil {
		t.Fatal("expected nil Bundle.Config() to return nil")
	}

	dupGlobal := validTestGlobalConfig()
	dupGlobal.Destinations = []config.DestinationConfig{
		{Name: "dup_store", Type: config.DestTypeDir, BackupDir: "/var/dup_backups"},
	}
	dupStack := validTestStackConfig()
	dupStack.Name = "dup_service"
	dupStack.Destinations = []string{"dup_store"}
	if _, err := ValidateBundle(&config.Bundle{
		Global: dupGlobal,
		Stacks: []config.StackConfig{dupStack, dupStack},
	}); err == nil || !strings.Contains(err.Error(), "duplicate stack name") {
		t.Fatalf("expected duplicate stack name error, got %v", err)
	}

	remoteCount := 8
	rawGlobal := validTestGlobalConfig()
	rawGlobal.BackupRoot = "/var/isolated-backups"
	rawGlobal.ReverseProxyStack = ""
	rawGlobal.Retention.RemoteOverrides = map[string]int{"isolated_override": 3}
	rawGlobal.Destinations = []config.DestinationConfig{
		{Name: "isolated_override", Type: config.DestTypeDir, BackupDir: "/var/iso_a", RemoteCount: &remoteCount},
		{Name: "isolated_dest", Type: config.DestTypeDir, BackupDir: "/var/iso_b"},
	}

	rawStack := validTestStackConfig()
	rawStack.Name = "isolated_app"
	rawStack.Destinations = []string{"isolated_dest"}
	rawStack.Databases = []config.DatabaseConfig{
		{
			ID:      "app_db",
			Driver:  "postgres",
			Service: "db",
			Secrets: &config.DBSecretsConfig{
				Source:   "host",
				UserFile: "/var/validator/db_user",
			},
		},
	}

	raw := config.Bundle{
		Global: rawGlobal,
		Stacks: []config.StackConfig{rawStack},
	}
	expected := raw.Clone()

	validated, err := ValidateBundle(&raw)
	if err != nil {
		t.Fatalf("expected valid bundle, got: %v", err)
	}

	raw.Global.BackupRoot = "/mutated/backup_root"
	raw.Global.Destinations[0].Name = "mutated_edge_store"
	*raw.Global.Destinations[0].RemoteCount = 99
	clear(raw.Global.Retention.RemoteOverrides)
	raw.Stacks[0].Name = "mutated_isolated_app"
	raw.Stacks[0].Destinations[0] = "mutated_dest"
	raw.Stacks[0].Databases[0].Secrets.UserFile = "/mutated/db_user"

	snap1 := validated.Config()
	if !reflect.DeepEqual(*snap1, expected) {
		t.Fatalf("mutating caller input altered validated Bundle:\ngot:  %+v\nwant: %+v", snap1, expected)
	}

	snap1.Global.BackupRoot = "/caller/mutated"
	*snap1.Global.Destinations[0].RemoteCount = 123
	snap1.Stacks[0].Databases[0].Secrets.UserFile = "/caller/mutated_user"

	snap2 := validated.Config()
	if !reflect.DeepEqual(*snap2, expected) {
		t.Fatalf("mutating returned Config() altered validated Bundle:\ngot:  %+v\nwant: %+v", snap2, expected)
	}
}
