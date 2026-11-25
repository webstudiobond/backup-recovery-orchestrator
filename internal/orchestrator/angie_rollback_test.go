package orchestrator

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/webstudiobond/backup-recovery-orchestrator/internal/config"
	"github.com/webstudiobond/backup-recovery-orchestrator/internal/dac"
)

func TestResolveAngieDACUserAndRelativeStackChecks(t *testing.T) {
	t.Parallel()

	fallbackUser := resolveAngieDACUser(nil, "edge-angie")
	if fallbackUser.Name != "edge-angie" || fallbackUser.UID != 0 {
		t.Fatalf("unexpected fallback user: %+v", fallbackUser)
	}
	configured := &config.StackConfig{
		StackDir: "/home/proxy-rel",
		User:     config.UserConfig{Name: "proxy-svc", UID: 1001, GID: 1001},
	}
	if got := resolveAngieDACUser(configured, "edge-angie"); got != configured.User {
		t.Fatalf("expected configured stack user, got %+v", got)
	}

	if _, ok := angieTargetStackRel(&config.StackConfig{StackDir: "relative/stack"}, "/home/proxy-rel/data"); ok {
		t.Fatal("expected relative stack_dir vs absolute target to reject")
	}
	if _, ok := angieTargetStackRel(configured, "/etc/angie/conf.d/app.conf"); ok {
		t.Fatal("expected external target outside stack_dir to return false")
	}
	if !angieDACRuleMatches(".", "data/conf.d/site.conf", true) {
		t.Fatal("expected recursive root rule '.' to match nested path")
	}
	if angieDACRuleMatches("data", "data/conf.d/site.conf", false) {
		t.Fatal("expected non-recursive ancestor rule not to match nested path")
	}
}

func TestResolveAngieDACRulesFromConfigAndFallback(t *testing.T) {
	t.Parallel()

	svc := config.DefaultAngieService
	confRulesFallback := resolveAngieConfDACRules(nil, "/etc/angie/domains", "site.example.com.conf", svc)
	if len(confRulesFallback) != 1 || confRulesFallback[0].Owner != "root:"+svc || confRulesFallback[0].FileMode != "0440" {
		t.Fatalf("unexpected conf fallback rules: %+v", confRulesFallback)
	}
	certRulesFallback := resolveAngieCertDACRules(nil, "/etc/angie/certs", "site.example.com", svc)
	if len(certRulesFallback) != 1 || certRulesFallback[0].Owner != svc+":"+svc || certRulesFallback[0].DirMode != "0700" || certRulesFallback[0].FileMode != "0600" {
		t.Fatalf("unexpected cert fallback rules: %+v", certRulesFallback)
	}

	angieStack := &config.StackConfig{
		Name:     svc,
		StackDir: "/home/proxy-main",
		User:     config.UserConfig{Name: svc, UID: 1001, GID: 1001},
		DAC: []config.DACRule{
			{Path: ".", Owner: "root:wheel", DirMode: "0750", Recursive: true},
			{Path: "data/conf.d", Owner: "root:stack", DirMode: "0750", FileMode: "0640", Recursive: true},
			{Path: "data/conf.d/domains/override.example.com.conf", Owner: "alt:alt", FileMode: "0600"},
			{Path: "data/certs", Owner: "stack:proxy-grp", DirMode: "0750", FileMode: "0640", Recursive: true},
			{Path: "data/certs/site1/privkey.pem", Owner: "root:certs", FileMode: "0440"},
		},
	}

	confRules := resolveAngieConfDACRules(angieStack, "/home/proxy-main/data/conf.d/domains", "override.example.com.conf", svc)
	wantConf := []config.DACRule{{Path: "override.example.com.conf", Owner: "alt:alt", FileMode: "0600"}}
	if !slices.Equal(confRules, wantConf) {
		t.Fatalf("expected conf rules %+v, got %+v", wantConf, confRules)
	}

	certRules := resolveAngieCertDACRules(angieStack, "/home/proxy-main/data/certs", "site1", svc)
	wantCert := []config.DACRule{
		{Path: "site1", Owner: "stack:proxy-grp", DirMode: "0750", FileMode: "0640", Recursive: true},
		{Path: "site1/privkey.pem", Owner: "root:certs", FileMode: "0440"},
	}
	if !slices.Equal(certRules, wantCert) {
		t.Fatalf("expected cert rules %+v, got %+v", wantCert, certRules)
	}

	partialStack := &config.StackConfig{
		StackDir: "/home/proxy-partial",
		DAC: []config.DACRule{
			{Path: "data/certs/site1", Owner: "custom:custom"},
		},
	}
	partialCert := resolveAngieCertDACRules(partialStack, "/home/proxy-partial/data/certs", "site1", svc)
	if partialCert[0].Owner != "custom:custom" || partialCert[0].DirMode != "0700" || partialCert[0].FileMode != "0600" {
		t.Fatalf("expected partial cert override to preserve fallback modes, got %+v", partialCert[0])
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
	symTarget := filepath.Join(root, "sym_target")
	if err := os.Symlink(srcFile, symTarget); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	tests := []struct {
		target  string
		backup  string
		wantErr bool
	}{
		{target: filepath.Join(root, "missing"), backup: filepath.Join(backupDir, "b1"), wantErr: false},
		{target: filepath.Join(noPerm, "target"), backup: filepath.Join(backupDir, "b2"), wantErr: true},
		{target: symTarget, backup: filepath.Join(backupDir, "b3"), wantErr: true},
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

func TestRollbackAngieFilesPreservesDAC(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	domainsBase := filepath.Join(root, "domains")
	confName := "rb.example.com.conf"
	confTarget := filepath.Join(domainsBase, confName)
	confBackup := filepath.Join(root, "bak_conf")
	if err := os.WriteFile(confBackup, []byte("good-conf"), 0o600); err != nil {
		t.Fatalf("write conf backup: %v", err)
	}
	if err := os.MkdirAll(domainsBase, 0o750); err != nil {
		t.Fatalf("mkdir domainsBase: %v", err)
	}

	certsBase := filepath.Join(root, "certs")
	acmeName := "app.example.com"
	certTarget := filepath.Join(certsBase, acmeName)
	certBackup := filepath.Join(root, "bak_cert")
	if err := os.MkdirAll(certBackup, 0o750); err != nil {
		t.Fatalf("mkdir certBackup: %v", err)
	}
	if err := os.WriteFile(filepath.Join(certBackup, "key.pem"), []byte("secret-key"), 0o600); err != nil {
		t.Fatalf("write key backup: %v", err)
	}
	if err := os.MkdirAll(certsBase, 0o750); err != nil {
		t.Fatalf("mkdir certsBase: %v", err)
	}

	owners := make(map[string][2]int)
	dacApplier := recordingAngieDACApplier(owners)
	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()

	svc := config.DefaultAngieService
	entries := []angieRollbackEntry{
		{baseDir: domainsBase, targetPath: confTarget, backupPath: confBackup, dacRules: resolveAngieConfDACRules(nil, domainsBase, confName, svc), existed: true, isDir: false},
		{baseDir: certsBase, targetPath: certTarget, backupPath: certBackup, dacRules: resolveAngieCertDACRules(nil, certsBase, acmeName, svc), existed: true, isDir: true},
	}
	angieUser := config.UserConfig{Name: svc, UID: 1002, GID: 1002}
	if err := rollbackAngieFiles(canceledCtx, dacApplier, angieUser, entries); err != nil {
		t.Fatalf("unexpected rollback error: %v", err)
	}

	assertPathPermAndOwner(t, owners, confTarget, 0o440, [2]int{0, 1002})
	assertPathPermAndOwner(t, owners, certTarget, 0o700, [2]int{1002, 1002})
	assertPathPermAndOwner(t, owners, filepath.Join(certTarget, "key.pem"), 0o600, [2]int{1002, 1002})

	failingDAC := &dac.Applier{
		LookupUser: func(_ string) (int, error) { return 0, errors.New("dac lookup fail") },
	}
	if err := rollbackAngieFiles(context.Background(), failingDAC, angieUser, entries[:1]); err == nil || !strings.Contains(err.Error(), "rollback restore DAC") {
		t.Fatalf("expected rollback restore DAC error, got %v", err)
	}
}

func TestRollbackAngieFilesErrors(t *testing.T) {
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
		wantErr string
		entries []angieRollbackEntry
	}{
		{entries: []angieRollbackEntry{{targetPath: targetFile, existed: false}}},
		{wantErr: "rollback remove", entries: []angieRollbackEntry{{targetPath: roTarget}}},
		{
			wantErr: "rollback restore",
			entries: []angieRollbackEntry{
				{targetPath: filepath.Join(root, "t_dir"), backupPath: missingBackup, existed: true, isDir: true},
				{targetPath: filepath.Join(root, "t_file"), backupPath: missingBackup, existed: true, isDir: false},
			},
		},
	}
	for i, tc := range tests {
		err := rollbackAngieFiles(context.Background(), nil, config.UserConfig{Name: config.DefaultAngieService}, tc.entries)
		if tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)) {
			t.Fatalf("case %d: expected error containing %q, got %v", i, tc.wantErr, err)
		}
		if tc.wantErr == "" && err != nil {
			t.Fatalf("case %d: unexpected rollback error: %v", i, err)
		}
	}
}
