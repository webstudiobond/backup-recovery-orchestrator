package orchestrator

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/webstudiobond/backup-recovery-orchestrator/internal/config"
	"github.com/webstudiobond/backup-recovery-orchestrator/internal/dac"
	"github.com/webstudiobond/backup-recovery-orchestrator/internal/docker"
)

func TestRestoreAngieRollbackOnInvalidConfig(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := t.TempDir()
	stackName := "app-delta"
	bundle := buildTestBundle(t, root, stackName)
	pPath, gPath := writeFakeAccounts(t, root, stackName)

	runner := &fakeOrchRunner{
		snapMount: bundle.Global.LVM.MountPoint,
		stackName: stackName,
		psOutput:  []byte(`{"Service":"pg","State":"running","Health":"healthy"}`),
	}
	orch := newTestOrchestrator(runner, pPath, gPath, nil)

	if err := orch.Backup(ctx, &bundle, &BackupOptions{NoSync: true, Out: io.Discard}); err != nil {
		t.Fatalf("Backup failed: %v", err)
	}

	domainPath := filepath.Join(bundle.Global.Angie.DomainsDir, "alpha.example.com.conf")
	if err := os.WriteFile(domainPath, []byte("live-good-config"), 0o600); err != nil {
		t.Fatalf("update live angie domain: %v", err)
	}
	streamPath := filepath.Join(bundle.Global.Angie.StreamDir, bundle.Stacks[0].Angie.StreamConfs[0])
	if err := os.WriteFile(streamPath, []byte("live-good-stream"), 0o600); err != nil {
		t.Fatalf("update live angie stream: %v", err)
	}

	runner.failMatch = "angie -t"
	archivePath := filepath.Join(bundle.Global.BackupRoot, stackName, stackName+"_2026-11-16_15-00-00.tar")
	err := orch.Restore(ctx, &bundle.Global, &RestoreOptions{
		ArchivePath: archivePath,
		AngieMode:   RestoreModeAll,
		OnlySteps:   []string{"deploy_angie"},
		Out:         io.Discard,
	})
	if err == nil || !strings.Contains(err.Error(), "angie -t") {
		t.Fatalf("expected angie -t error, got %v", err)
	}

	restoredBytes, readErr := os.ReadFile(filepath.Clean(domainPath))
	if readErr != nil {
		t.Fatalf("read domain config after rollback: %v", readErr)
	}
	if string(restoredBytes) != "live-good-config" {
		t.Fatalf("expected angie domain rolled back to %q, got %q", "live-good-config", string(restoredBytes))
	}
	restoredStreamBytes, readStreamErr := os.ReadFile(filepath.Clean(streamPath))
	if readStreamErr != nil {
		t.Fatalf("read stream config after rollback: %v", readStreamErr)
	}
	if string(restoredStreamBytes) != "live-good-stream" {
		t.Fatalf("expected angie stream rolled back to %q, got %q", "live-good-stream", string(restoredStreamBytes))
	}
}

func TestCheckAngieSelectionList(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		kind      string
		wantErr   string
		requested []string
		available []string
	}{
		{name: "all items present", kind: "domain", requested: []string{"a.example.com"}, available: []string{"a.example.com", "b.example.com"}},
		{name: "empty requested items", kind: "stream", requested: nil, available: []string{"s.example.com"}},
		{name: "missing item returns error", kind: "cert", requested: []string{"missing.example.com"}, available: []string{"exists.example.com"}, wantErr: "is not present in archive manifest"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := checkAngieSelectionList(tc.requested, tc.available, tc.kind)
			if tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)) {
				t.Fatalf("expected error containing %q, got %v", tc.wantErr, err)
			}
			if tc.wantErr == "" && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestResolveAngieSelection(t *testing.T) {
	t.Parallel()

	domainConf := "res-site.example.com.conf"
	avail := &config.StackAngieConfig{
		DomainConfs: []string{domainConf},
		StreamConfs: []string{"res-stream.example.com.inc"},
		ACMENames:   []string{"app-cert.example.com"},
	}

	tests := []struct {
		name        string
		mode        string
		wantErr     string
		reqDomains  []string
		reqStreams  []string
		reqCerts    []string
		wantDomains []string
	}{
		{name: "explicit valid", reqDomains: []string{domainConf}, reqStreams: []string{"res-stream.example.com.inc"}, reqCerts: []string{"app-cert.example.com"}, wantDomains: []string{domainConf}},
		{name: "missing domain", reqDomains: []string{"notfound-domain.example.com.conf"}, wantErr: "requested angie domain"},
		{name: "missing stream", reqStreams: []string{"notfound-stream.example.com.inc"}, wantErr: "requested angie stream"},
		{name: "missing cert", reqCerts: []string{"notfound-cert.example.com"}, wantErr: "requested angie cert"},
		{name: "mode empty", mode: ""},
		{name: "mode none", mode: RestoreModeNone},
		{name: "mode all", mode: RestoreModeAll, wantDomains: []string{domainConf}},
		{name: "unknown mode", mode: "unknown-mode", wantErr: "unsupported angie restore mode"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			sel, err := resolveAngieSelection(avail, tc.mode, tc.reqDomains, tc.reqStreams, tc.reqCerts)
			if tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)) {
				t.Fatalf("expected error %q, got %v", tc.wantErr, err)
			}
			if tc.wantErr == "" && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(tc.wantDomains) > 0 && !slices.Equal(sel.Domains, tc.wantDomains) {
				t.Fatalf("expected domains %v, got %v", tc.wantDomains, sel.Domains)
			}
		})
	}
}

func TestDeployAngieWithRollback(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := t.TempDir()
	composeFile := filepath.Join(root, "docker-compose.yaml")
	if err := os.WriteFile(composeFile, []byte("services: {}\n"), 0o600); err != nil {
		t.Fatalf("write compose: %v", err)
	}
	domainsDir := filepath.Join(root, "domains")
	unpackDir := filepath.Join(root, "unpack")
	if err := os.MkdirAll(filepath.Join(unpackDir, "domains"), 0o750); err != nil {
		t.Fatalf("mkdir unpack domains: %v", err)
	}
	confName := "deploy.example.com.conf"
	if err := os.WriteFile(filepath.Join(unpackDir, "domains", confName), []byte("server {}"), 0o600); err != nil {
		t.Fatalf("write conf: %v", err)
	}

	runner := &fakeOrchRunner{}
	dockerClient := &docker.Client{Runner: runner}
	dacApplier := &dac.Applier{
		Lchown:      func(_ string, _, _ int) error { return nil },
		Chmod:       os.Chmod,
		LookupUser:  func(_ string) (int, error) { return 1000, nil },
		LookupGroup: func(_ string) (int, error) { return 1000, nil },
	}

	tests := []struct {
		global  *config.GlobalAngieConfig
		name    string
		wantErr string
		sel     angieSelection
	}{
		{name: "empty selection", global: &config.GlobalAngieConfig{}, sel: angieSelection{}},
		{name: "empty compose file", global: &config.GlobalAngieConfig{}, sel: angieSelection{Domains: []string{"d.example.com"}}, wantErr: "compose_file is required"},
		{name: "missing compose file", global: &config.GlobalAngieConfig{ComposeFile: filepath.Join(root, "missing.yaml")}, sel: angieSelection{Domains: []string{"d.example.com"}}, wantErr: "not found"},
		{name: "success deployment", global: &config.GlobalAngieConfig{ComposeFile: composeFile, DomainsDir: domainsDir}, sel: angieSelection{Domains: []string{confName}}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := deployAngieWithRollback(ctx, dockerClient, dacApplier, tc.global, unpackDir, tc.sel)
			if tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)) {
				t.Fatalf("expected error containing %q, got %v", tc.wantErr, err)
			}
			if tc.wantErr == "" && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestDeployAngieWithRollbackTempDirError(t *testing.T) {
	fakeDir := filepath.Join(t.TempDir(), "not_a_dir")
	if err := os.WriteFile(fakeDir, []byte("x"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}
	t.Setenv("TMPDIR", fakeDir)

	composeFile := filepath.Join(t.TempDir(), "docker-compose.yaml")
	if err := os.WriteFile(composeFile, []byte("services: {}\n"), 0o600); err != nil {
		t.Fatalf("write compose: %v", err)
	}

	globalAngie := &config.GlobalAngieConfig{ComposeFile: composeFile}
	sel := angieSelection{Domains: []string{"test.example.com.conf"}}
	if err := deployAngieWithRollback(context.Background(), nil, nil, globalAngie, t.TempDir(), sel); err == nil || !strings.Contains(err.Error(), "create angie rollback directory") {
		t.Fatalf("expected create angie rollback directory error, got %v", err)
	}
}

func TestApplySelectedAngieFilesValidation(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	dacApplier := &dac.Applier{
		LookupUser: func(_ string) (int, error) { return 0, errors.New("lookup err") },
	}
	root := t.TempDir()
	var entries []angieRollbackEntry

	fileParent := filepath.Join(root, "file_as_parent")
	if err := os.WriteFile(fileParent, []byte("x"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}

	tests := []struct {
		global  *config.GlobalAngieConfig
		name    string
		wantErr string
		sel     angieSelection
	}{
		{name: "missing domains_dir", global: &config.GlobalAngieConfig{}, sel: angieSelection{Domains: []string{"app.example.com.conf"}}, wantErr: "global angie.domains_dir is required"},
		{name: "mkdir domain dir error", global: &config.GlobalAngieConfig{DomainsDir: filepath.Join(fileParent, "sub")}, sel: angieSelection{Domains: []string{"app.example.com.conf"}}, wantErr: "ensure angie domain dir"},
		{name: "missing stream_dir", global: &config.GlobalAngieConfig{}, sel: angieSelection{Streams: []string{"app.example.com.inc"}}, wantErr: "global angie.stream_dir is required"},
		{name: "missing certs_dir", global: &config.GlobalAngieConfig{}, sel: angieSelection{Certs: []string{"cert1"}}, wantErr: "global angie.certs_dir is required"},
		{name: "mkdir certs dir error", global: &config.GlobalAngieConfig{CertsDir: filepath.Join(fileParent, "sub")}, sel: angieSelection{Certs: []string{"cert1"}}, wantErr: "ensure angie certs dir"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := applySelectedAngieFiles(ctx, dacApplier, tc.global, root, root, tc.sel, &entries)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("expected error containing %q, got %v", tc.wantErr, err)
			}
		})
	}
}

func TestDeployAngieConfErrors(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := t.TempDir()
	stagedDir := filepath.Join(root, "staged")
	targetDir := filepath.Join(root, "target")
	if err := os.MkdirAll(stagedDir, 0o750); err != nil {
		t.Fatalf("mkdir staged: %v", err)
	}
	if err := os.MkdirAll(targetDir, 0o750); err != nil {
		t.Fatalf("mkdir target: %v", err)
	}

	var entries []angieRollbackEntry
	failingDAC := &dac.Applier{
		LookupUser: func(_ string) (int, error) { return 0, errors.New("user lookup failed") },
	}

	confName := "conf-err.example.com.conf"
	if err := deployAngieConfFiles(ctx, nil, targetDir, "angie", stagedDir, root, "domains_dir", "domain", []string{confName}, &entries); err == nil {
		t.Fatal("expected copyPlainFile error when staged conf is missing")
	}
	if err := os.WriteFile(filepath.Join(stagedDir, confName), []byte("server {}"), 0o600); err != nil {
		t.Fatalf("write staged conf: %v", err)
	}
	if err := deployAngieConfFiles(ctx, failingDAC, targetDir, "angie", stagedDir, root, "domains_dir", "domain", []string{confName}, &entries); err == nil {
		t.Fatal("expected dac error on conf deploy")
	}

	fileRollbackDir := filepath.Join(root, "file_as_rollback_dir")
	if err := os.WriteFile(fileRollbackDir, []byte("x"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}
	existingConf := filepath.Join(targetDir, "existing-conf.example.com.conf")
	if err := os.WriteFile(existingConf, []byte("existing"), 0o600); err != nil {
		t.Fatalf("write existing conf: %v", err)
	}
	if err := deployAngieConfFiles(ctx, nil, targetDir, "angie", stagedDir, fileRollbackDir, "domains_dir", "domain", []string{"existing-conf.example.com.conf"}, &entries); err == nil {
		t.Fatal("expected snapshotExistingPath error on conf deploy")
	}
}

func TestDeployAngieCertErrors(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := t.TempDir()
	certsTarget := filepath.Join(root, "certs_target")
	if err := os.MkdirAll(certsTarget, 0o750); err != nil {
		t.Fatalf("mkdir certsTarget: %v", err)
	}

	var entries []angieRollbackEntry
	failingDAC := &dac.Applier{
		LookupUser: func(_ string) (int, error) { return 0, errors.New("user lookup failed") },
	}

	certName := "app-acme.example.com"
	if err := deployAngieCerts(ctx, nil, certsTarget, "angie", root, root, []string{certName}, &entries); err == nil {
		t.Fatal("expected copy error when cert src is missing")
	}
	certSrc := filepath.Join(root, "certs", certName)
	if err := os.MkdirAll(certSrc, 0o750); err != nil {
		t.Fatalf("mkdir certSrc: %v", err)
	}
	if err := deployAngieCerts(ctx, failingDAC, certsTarget, "angie", root, root, []string{certName}, &entries); err == nil {
		t.Fatal("expected dac error on cert deploy")
	}

	fileRollbackDir := filepath.Join(root, "file_as_rollback_dir")
	if err := os.WriteFile(fileRollbackDir, []byte("x"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}
	existingCert := filepath.Join(certsTarget, "existing-cert.example.com")
	if err := os.Mkdir(existingCert, 0o750); err != nil {
		t.Fatalf("mkdir existing cert: %v", err)
	}
	if err := deployAngieCerts(ctx, nil, certsTarget, "angie", root, fileRollbackDir, []string{"existing-cert.example.com"}, &entries); err == nil {
		t.Fatal("expected snapshotExistingPath error on cert deploy")
	}

	roCerts := filepath.Join(root, "ro_certs")
	if err := os.MkdirAll(filepath.Join(roCerts, certName), 0o750); err != nil {
		t.Fatalf("mkdir ro_certs: %v", err)
	}
	roDirPerm := os.FileMode(0o500)
	if err := os.Chmod(roCerts, roDirPerm); err != nil {
		t.Fatalf("chmod ro_certs: %v", err)
	}
	resetCertsPerm := os.FileMode(0o700)
	t.Cleanup(func() {
		if err := os.Chmod(roCerts, resetCertsPerm); err != nil {
			t.Errorf("cleanup chmod ro_certs: %v", err)
		}
	})
	if err := deployAngieCerts(ctx, nil, roCerts, "angie", root, root, []string{certName}, &entries); err == nil {
		t.Fatal("expected remove error in read-only certs dir")
	}
}

func TestSnapshotExistingPath(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	backupDir := filepath.Join(root, "backup")
	if err := os.Mkdir(backupDir, 0o750); err != nil {
		t.Fatalf("mkdir backup: %v", err)
	}

	noPerm := filepath.Join(root, "noperm")
	if err := os.Mkdir(noPerm, 0o000); err != nil {
		t.Fatalf("mkdir noperm: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(noPerm, 0o600); err != nil {
			t.Errorf("cleanup chmod noperm: %v", err)
		}
	})

	fileParent := filepath.Join(root, "file_parent")
	if err := os.WriteFile(fileParent, []byte("x"), 0o600); err != nil {
		t.Fatalf("write file parent: %v", err)
	}
	srcDir := filepath.Join(root, "src_dir")
	if err := os.Mkdir(srcDir, 0o750); err != nil {
		t.Fatalf("mkdir src dir: %v", err)
	}
	srcFile := filepath.Join(root, "src_file")
	if err := os.WriteFile(srcFile, []byte("x"), 0o600); err != nil {
		t.Fatalf("write src file: %v", err)
	}

	tests := []struct {
		target  string
		backup  string
		wantErr bool
	}{
		{target: filepath.Join(root, "missing"), backup: filepath.Join(backupDir, "b1"), wantErr: false},
		{target: filepath.Join(noPerm, "target"), backup: filepath.Join(backupDir, "b2"), wantErr: true},
		{target: srcDir, backup: filepath.Join(fileParent, "sub"), wantErr: true},
		{target: srcFile, backup: filepath.Join(fileParent, "sub"), wantErr: true},
	}
	for _, tc := range tests {
		_, err := snapshotExistingPath(tc.target, tc.backup)
		if tc.wantErr && err == nil {
			t.Fatalf("expected error for target %s", tc.target)
		}
		if !tc.wantErr && err != nil {
			t.Fatalf("unexpected error for target %s: %v", tc.target, err)
		}
	}
}

func TestRollbackAngieFiles(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	targetFile := filepath.Join(root, "target_not_existed")
	if err := os.WriteFile(targetFile, []byte("data"), 0o600); err != nil {
		t.Fatalf("write target: %v", err)
	}

	roParent := filepath.Join(root, "ro_parent")
	if err := os.Mkdir(roParent, 0o750); err != nil {
		t.Fatalf("mkdir ro parent: %v", err)
	}
	roTarget := filepath.Join(roParent, "target")
	if err := os.WriteFile(roTarget, []byte("data"), 0o600); err != nil {
		t.Fatalf("write ro target: %v", err)
	}
	if err := os.Chmod(roParent, 0o400); err != nil {
		t.Fatalf("chmod ro parent: %v", err)
	}
	resetParentPerm := os.FileMode(0o700)
	t.Cleanup(func() {
		if err := os.Chmod(roParent, resetParentPerm); err != nil {
			t.Errorf("cleanup chmod roParent: %v", err)
		}
	})

	missingBackup := filepath.Join(root, "missing_backup")
	tests := []struct {
		entries []angieRollbackEntry
		wantErr bool
	}{
		{entries: []angieRollbackEntry{{targetPath: targetFile, existed: false}}, wantErr: false},
		{entries: []angieRollbackEntry{{targetPath: roTarget}}, wantErr: true},
		{entries: []angieRollbackEntry{
			{targetPath: filepath.Join(root, "t_dir"), backupPath: missingBackup, existed: true, isDir: true},
			{targetPath: filepath.Join(root, "t_file"), backupPath: missingBackup, existed: true, isDir: false},
		}, wantErr: true},
	}
	for _, tc := range tests {
		err := rollbackAngieFiles(tc.entries)
		if tc.wantErr && err == nil {
			t.Fatal("expected rollback error")
		}
		if !tc.wantErr && err != nil {
			t.Fatalf("unexpected rollback error: %v", err)
		}
	}
}
