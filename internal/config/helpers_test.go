package config

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatalf("mkdir %q: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %q: %v", path, err)
	}
}

func assertAlphaStack(t *testing.T, bundle *Bundle, stack *StackConfig) {
	t.Helper()

	if stack.Name != "app-alpha" || !stack.Enabled {
		t.Fatalf("unexpected app-alpha identity/enabled state: %+v", *stack)
	}
	if stack.StackDir != "/home/app-alpha" || stack.ComposeFile != "/home/app-alpha/docker-compose.yaml" {
		t.Fatalf("unexpected default paths: dir=%q compose=%q", stack.StackDir, stack.ComposeFile)
	}
	if stack.ZstdLevel != 1 || stack.Retention.LocalCount != 1 || stack.Retention.RemoteCount != 5 {
		t.Fatalf("unexpected app-alpha overrides: zstd=%d retention=%+v", stack.ZstdLevel, stack.Retention)
	}
	if !slices.Equal(stack.Destinations, []string{"cloud-primary", "s3-archive"}) {
		t.Fatalf("unexpected app-alpha destinations: %v", stack.Destinations)
	}
	if got := stack.RemoteCountFor(&bundle.Global.Destinations[0]); got != 14 {
		t.Fatalf("expected cloud-primary remote_count 14 (destination override), got %d", got)
	}
	if got := stack.RemoteCountFor(&bundle.Global.Destinations[1]); got != 2 {
		t.Fatalf("expected s3-archive remote_count 2 (stack remote_overrides), got %d", got)
	}
	assertAlphaAssets(t, stack)
}

func assertAlphaAssets(t *testing.T, stack *StackConfig) {
	t.Helper()

	expectedExcludes := []string{"*.tmp", "postgres/*", "logs/*"}
	if !slices.Equal(stack.Files.Excludes, expectedExcludes) {
		t.Fatalf("expected additive excludes %v, got %v", expectedExcludes, stack.Files.Excludes)
	}
	if len(stack.DAC) != 3 || stack.DAC[1].Owner != "root:stack" || stack.DAC[1].DirMode != "0750" || stack.DAC[1].FileMode != "0440" {
		t.Fatalf("unexpected additive DAC rules: %+v", stack.DAC)
	}
	if len(stack.HostFiles) != 2 ||
		stack.HostFiles[0].Path != "/etc/cron.d/svc-app-alpha" ||
		stack.HostFiles[1].Path != "/etc/logrotate.d/app-alpha" {
		t.Fatalf("unexpected host_files: %+v", stack.HostFiles)
	}
	if len(stack.Databases) != 1 || stack.Databases[0].Secrets == nil {
		t.Fatalf("expected inherited database with secrets, got %+v", stack.Databases)
	}
	if len(stack.RestoreSteps) != 9 {
		t.Fatalf("expected 9 inherited restore steps, got %d", len(stack.RestoreSteps))
	}
}

func assertBetaStack(t *testing.T, stack *StackConfig) {
	t.Helper()

	if stack.Enabled {
		t.Fatalf("expected app-beta stack disabled")
	}
	if len(stack.ReverseProxy.ACMENames) != 2 || len(stack.ReverseProxy.DomainConfs) != 2 || len(stack.ReverseProxy.StreamConfs) != 1 {
		t.Fatalf("expected multi-domain and stream reverse_proxy assets, got %+v", stack.ReverseProxy)
	}
	if len(stack.Hooks.PreSnapshot) != 3 || len(stack.Hooks.PostSnapshot) != 1 {
		t.Fatalf("unexpected app-beta hooks: %+v", stack.Hooks)
	}
	if !stack.Files.NoWildcardsMatchSlash {
		t.Fatalf("expected no_wildcards_match_slash enabled on app-beta")
	}
}
