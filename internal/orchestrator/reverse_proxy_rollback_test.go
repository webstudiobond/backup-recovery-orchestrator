package orchestrator

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/webstudiobond/backup-recovery-orchestrator/internal/config"
	"github.com/webstudiobond/backup-recovery-orchestrator/internal/dac"
)

func TestResolveReverseProxyDACUser(t *testing.T) {
	t.Parallel()

	fallbackUser := resolveReverseProxyDACUser(nil, "edge-fallback")
	if fallbackUser.Name != "edge-fallback" || fallbackUser.UID != 0 {
		t.Fatalf("unexpected fallback user: %+v", fallbackUser)
	}
	configured := &config.StackConfig{
		StackDir: "/home/proxy-rel",
		User:     config.UserConfig{Name: "proxy-cfg", UID: 1001, GID: 1001},
	}
	if got := resolveReverseProxyDACUser(configured, "edge-fallback"); got != configured.User {
		t.Fatalf("expected configured stack user, got %+v", got)
	}
}

func TestResolveReverseProxyDACRules(t *testing.T) {
	t.Parallel()

	if !reverseProxyDACRuleMatches(".", "data/conf.d/site.conf", true) {
		t.Fatal("expected recursive root rule '.' to match nested path")
	}
	if reverseProxyDACRuleMatches("data", "data/conf.d/site.conf", false) {
		t.Fatal("expected non-recursive ancestor rule not to match nested path")
	}

	provider := mustLookupReverseProxyProvider(t)
	svc := "proxy-svc"
	confFallback := resolveReverseProxyConfDACRules(provider, nil, "data/domains", "site.example.com.conf", svc)
	if len(confFallback) != 1 || confFallback[0].Owner != "root:"+svc || confFallback[0].FileMode != "0440" {
		t.Fatalf("unexpected conf fallback rules: %+v", confFallback)
	}
	certFallback := resolveReverseProxyCertDACRules(provider, nil, "tls/certs", "site.example.com", svc)
	if len(certFallback) != 1 || certFallback[0].Owner != svc+":"+svc || certFallback[0].DirMode != "0700" || certFallback[0].FileMode != "0600" {
		t.Fatalf("unexpected cert fallback rules: %+v", certFallback)
	}

	reverseProxyStack := &config.StackConfig{
		Name:     svc,
		StackDir: "/home/reverse-proxy-main",
		User:     config.UserConfig{Name: svc, UID: 1001, GID: 1001},
		DAC: []config.DACRule{
			{Path: ".", Owner: "root:wheel", DirMode: "0750", Recursive: true},
			{Path: "data/conf.d", Owner: "root:stack", DirMode: "0750", FileMode: "0640", Recursive: true},
			{Path: "data/conf.d/domains/override.example.com.conf", Owner: "alt:alt", FileMode: "0600"},
			{Path: "tls/certs", Owner: "stack:proxy-grp", DirMode: "0750", FileMode: "0640", Recursive: true},
			{Path: "tls/certs/site1/privkey.pem", Owner: "root:certs", FileMode: "0440"},
		},
	}

	confRules := resolveReverseProxyConfDACRules(provider, reverseProxyStack, "data/conf.d/domains", "override.example.com.conf", svc)
	wantConf := []config.DACRule{{Path: "override.example.com.conf", Owner: "alt:alt", FileMode: "0600"}}
	if !slices.Equal(confRules, wantConf) {
		t.Fatalf("expected conf rules %+v, got %+v", wantConf, confRules)
	}

	certRules := resolveReverseProxyCertDACRules(provider, reverseProxyStack, "tls/certs", "site1", svc)
	wantCert := []config.DACRule{
		{Path: "site1", Owner: "stack:proxy-grp", DirMode: "0750", FileMode: "0640", Recursive: true},
		{Path: "site1/privkey.pem", Owner: "root:certs", FileMode: "0440"},
	}
	if !slices.Equal(certRules, wantCert) {
		t.Fatalf("expected cert rules %+v, got %+v", wantCert, certRules)
	}

	partialStack := &config.StackConfig{
		StackDir: "/home/reverse-proxy-partial",
		DAC:      []config.DACRule{{Path: "var/certs/site1", Owner: "custom:custom"}},
	}
	partialCert := resolveReverseProxyCertDACRules(provider, partialStack, "var/certs", "site1", svc)
	if partialCert[0].Owner != "custom:custom" || partialCert[0].DirMode != "0700" || partialCert[0].FileMode != "0600" {
		t.Fatalf("expected partial cert override to preserve fallback modes, got %+v", partialCert[0])
	}
}

func TestResolveReverseProxyParentDirDACRules(t *testing.T) {
	t.Parallel()

	if got := resolveReverseProxyParentDirDACRules(nil, "a/b"); got != nil {
		t.Fatalf("expected nil parent rules for nil stack, got %+v", got)
	}
	if got := resolveReverseProxyParentDirDACRules(&config.StackConfig{}, "a/b"); got != nil {
		t.Fatalf("expected nil parent rules for empty DAC, got %+v", got)
	}

	stackWithDAC := &config.StackConfig{
		DAC: []config.DACRule{
			{Path: ".", Owner: "root:basegrp", DirMode: "0750", Recursive: true},
			{Path: "tier1", DirMode: "0751"},
			{Path: "tier1/tier2", Owner: "root:midgrp", DirMode: "0755", Recursive: true},
			{Path: "tier1/tier2/leaf", Owner: "root:leafgrp", DirMode: "0750"},
		},
	}
	got := resolveReverseProxyParentDirDACRules(stackWithDAC, "tier1/tier2/leaf")
	want := []config.DACRule{
		{Path: "tier1", Owner: "root:basegrp", DirMode: "0751"},
		{Path: "tier1/tier2", Owner: "root:midgrp", DirMode: "0755"},
		{Path: "tier1/tier2/leaf", Owner: "root:leafgrp", DirMode: "0750"},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("expected parent dir rules %+v, got %+v", want, got)
	}
}

func TestSnapshotExistingReverseProxyPath(t *testing.T) {
	root := t.TempDir()
	reverseProxyDir := filepath.Join(root, "reverse_proxy")
	backupDir := filepath.Join(root, "backup")
	for _, d := range []string{reverseProxyDir, backupDir, filepath.Join(reverseProxyDir, "src_dir")} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			t.Fatalf("mkdir %q: %v", d, err)
		}
	}
	if err := os.WriteFile(filepath.Join(reverseProxyDir, "src_dir", "nested.txt"), []byte("nested"), 0o600); err != nil {
		t.Fatalf("write nested: %v", err)
	}
	if err := os.WriteFile(filepath.Join(reverseProxyDir, "src_file"), []byte("x"), 0o600); err != nil {
		t.Fatalf("write src_file: %v", err)
	}
	if err := os.Symlink("src_file", filepath.Join(reverseProxyDir, "sym_target")); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	fileParent := filepath.Join(root, "file_parent")
	if err := os.WriteFile(fileParent, []byte("x"), 0o600); err != nil {
		t.Fatalf("write file parent: %v", err)
	}

	reverseProxyRoot, openErr := os.OpenRoot(reverseProxyDir)
	if openErr != nil {
		t.Fatalf("open reverseProxyRoot: %v", openErr)
	}
	t.Cleanup(func() { closeOrchRoot(t, reverseProxyRoot) })

	origLstat := rootLstat
	t.Cleanup(func() { rootLstat = origLstat })

	tests := []struct {
		setup   func()
		name    string
		entry   string
		backup  string
		wantErr bool
	}{
		{name: "missing_entry", entry: "missing", backup: filepath.Join(backupDir, "b1"), wantErr: false},
		{
			name:    "lstat_error",
			entry:   "src_file",
			backup:  filepath.Join(backupDir, "b2"),
			setup:   func() { rootLstat = mockFailingRootLstat },
			wantErr: true,
		},
		{name: "symlink_rejected", entry: "sym_target", backup: filepath.Join(backupDir, "b3"), wantErr: true},
		{name: "dir_backup_error", entry: "src_dir", backup: filepath.Join(fileParent, "sub"), wantErr: true},
		{name: "file_backup_error", entry: "src_file", backup: filepath.Join(fileParent, "sub"), wantErr: true},
		{name: "valid_dir_backup", entry: "src_dir", backup: filepath.Join(backupDir, "b_dir"), wantErr: false},
	}

	for _, tc := range tests {
		rootLstat = origLstat
		if tc.setup != nil {
			tc.setup()
		}
		_, err := snapshotExistingReverseProxyPath(reverseProxyRoot, ".", reverseProxyDir, tc.entry, tc.backup)
		if tc.wantErr && err == nil {
			t.Fatalf("expected error for %s", tc.name)
		}
		if !tc.wantErr && err != nil {
			t.Fatalf("unexpected error for %s: %v", tc.name, err)
		}
	}
}

func TestRollbackReverseProxyFilesPreservesDAC(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	reverseProxyDir := filepath.Join(root, "reverse_proxy")
	domainsBase := filepath.Join(reverseProxyDir, "rb-domains")
	certsBase := filepath.Join(reverseProxyDir, "rb-certs")
	for _, d := range []string{domainsBase, certsBase} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			t.Fatalf("mkdir %q: %v", d, err)
		}
	}
	confName := "rb.example.com.conf"
	confTarget := filepath.Join(domainsBase, confName)
	confBackup := filepath.Join(root, "bak_conf")
	if err := os.WriteFile(confBackup, []byte("good-conf"), 0o600); err != nil {
		t.Fatalf("write conf backup: %v", err)
	}

	acmeName := "app.example.com"
	certTarget := filepath.Join(certsBase, acmeName)
	certBackup := filepath.Join(root, "bak_cert")
	if err := os.MkdirAll(filepath.Join(certBackup, "sub"), 0o750); err != nil {
		t.Fatalf("mkdir certBackup: %v", err)
	}
	if err := os.WriteFile(filepath.Join(certBackup, "key.pem"), []byte("secret-key"), 0o600); err != nil {
		t.Fatalf("write key backup: %v", err)
	}

	reverseProxyRoot, openErr := os.OpenRoot(reverseProxyDir)
	if openErr != nil {
		t.Fatalf("open reverseProxyRoot: %v", openErr)
	}
	t.Cleanup(func() { closeOrchRoot(t, reverseProxyRoot) })

	owners := make(map[string][2]int)
	dacApplier := recordingReverseProxyDACApplier(owners)
	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()

	provider := mustLookupReverseProxyProvider(t)
	svc := "reverse-proxy-rb"
	entries := []reverseProxyRollbackEntry{
		{dirRel: "rb-domains", baseDir: domainsBase, entryName: confName, backupPath: confBackup, dacRules: resolveReverseProxyConfDACRules(provider, nil, "rb-domains", confName, svc), existed: true, isDir: false},
		{dirRel: "rb-certs", baseDir: certsBase, entryName: acmeName, backupPath: certBackup, dacRules: resolveReverseProxyCertDACRules(provider, nil, "rb-certs", acmeName, svc), existed: true, isDir: true},
	}
	reverseProxyUser := config.UserConfig{Name: svc, UID: 1002, GID: 1002}
	if err := rollbackReverseProxyFiles(canceledCtx, dacApplier, reverseProxyRoot, reverseProxyUser, entries); err != nil {
		t.Fatalf("unexpected rollback error: %v", err)
	}

	assertReverseProxyPathPermAndOwner(t, owners, confTarget, 0o440, [2]int{0, 1002})
	assertReverseProxyPathPermAndOwner(t, owners, certTarget, 0o700, [2]int{1002, 1002})
	assertReverseProxyPathPermAndOwner(t, owners, filepath.Join(certTarget, "key.pem"), 0o600, [2]int{1002, 1002})

	failingDAC := &dac.Applier{
		LookupUser: func(_ string) (int, error) { return 0, errors.New("dac lookup fail") },
	}
	if err := rollbackReverseProxyFiles(context.Background(), failingDAC, reverseProxyRoot, reverseProxyUser, entries[:1]); err == nil || !strings.Contains(err.Error(), "rollback restore DAC") {
		t.Fatalf("expected rollback restore DAC error, got %v", err)
	}
}

func TestRollbackReverseProxyErrorsAndCopyHelpers(t *testing.T) {
	root := t.TempDir()
	reverseProxyDir := filepath.Join(root, "reverse_proxy")
	if err := os.MkdirAll(filepath.Join(reverseProxyDir, "rb-remove-dir"), 0o750); err != nil {
		t.Fatalf("mkdir reverse_proxy rb-remove-dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(reverseProxyDir, "rb-remove-dir", "target_not_existed"), []byte("data"), 0o600); err != nil {
		t.Fatalf("write target: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(reverseProxyDir, "err-domains"), 0o750); err != nil {
		t.Fatalf("mkdir reverse_proxy err-domains: %v", err)
	}
	reverseProxyRoot, openErr := os.OpenRoot(reverseProxyDir)
	if openErr != nil {
		t.Fatalf("open reverseProxyRoot: %v", openErr)
	}
	t.Cleanup(func() { closeOrchRoot(t, reverseProxyRoot) })

	missingBackup := filepath.Join(root, "missing_backup")
	rbUser := config.UserConfig{Name: "rb-err-user"}
	if err := rollbackReverseProxyFiles(context.Background(), nil, reverseProxyRoot, rbUser, []reverseProxyRollbackEntry{
		{dirRel: "rb-remove-dir", entryName: "target_not_existed", existed: false},
	}); err != nil {
		t.Fatalf("expected clean removal of non-existed entry, got %v", err)
	}

	if err := rollbackReverseProxyFiles(context.Background(), nil, reverseProxyRoot, rbUser, []reverseProxyRollbackEntry{
		{dirRel: "rb-restore-dir", entryName: "t_dir", backupPath: missingBackup, existed: true, isDir: true},
		{dirRel: "rb-restore-file", entryName: "t_file", backupPath: missingBackup, existed: true, isDir: false},
	}); err == nil || !strings.Contains(err.Error(), "rollback restore") {
		t.Fatalf("expected rollback restore errors, got %v", err)
	}

	if err := rollbackSingleReverseProxyEntry(context.Background(), nil, reverseProxyRoot, rbUser, &reverseProxyRollbackEntry{dirRel: "../outside_dir"}); err == nil || !strings.Contains(err.Error(), "rollback open target dir") {
		t.Fatalf("expected invalid dirRel error, got %v", err)
	}

	origRmAll := rootRemoveAll
	t.Cleanup(func() { rootRemoveAll = origRmAll })
	rootRemoveAll = mockFailingRootRemoveAll
	if err := rollbackSingleReverseProxyEntry(context.Background(), nil, reverseProxyRoot, rbUser, &reverseProxyRollbackEntry{dirRel: "err-domains", entryName: "x"}); err == nil || !strings.Contains(err.Error(), "rollback remove") {
		t.Fatalf("expected rollback remove error, got %v", err)
	}
	rootRemoveAll = origRmAll

	testCopyPlainAndRootFileErrors(t, root, reverseProxyRoot)
	testCopyRootDirToOSErrors(t, root, reverseProxyDir, reverseProxyRoot)
	testCopyDirectoryIntoRootErrors(t, root, reverseProxyDir, reverseProxyRoot)
}

func testCopyPlainAndRootFileErrors(t *testing.T, root string, reverseProxyRoot *os.Root) {
	t.Helper()

	srcFile := filepath.Join(root, "plain.txt")
	if err := os.WriteFile(srcFile, []byte("hello"), 0o600); err != nil {
		t.Fatalf("write srcFile: %v", err)
	}
	origOpenFile := rootOpenFile
	t.Cleanup(func() { rootOpenFile = origOpenFile })

	rootOpenFile = mockFailingRootOpenFile
	if err := copyPlainFileToRoot(srcFile, reverseProxyRoot, "out.txt", 0o600); err == nil || !strings.Contains(err.Error(), "open root target") {
		t.Fatalf("expected root open target error, got %v", err)
	}
	rootOpenFile = origOpenFile

	if err := copyRootFileToOS(reverseProxyRoot, "missing_root_file", filepath.Join(root, "out.txt"), 0o600); err == nil || !strings.Contains(err.Error(), "read root file") {
		t.Fatalf("expected read root file error, got %v", err)
	}
}

func testCopyRootDirToOSErrors(t *testing.T, root, reverseProxyDir string, reverseProxyRoot *os.Root) {
	t.Helper()

	origOpenRoot := rootOpenRoot
	origWalkReverseProxy := walkReverseProxyRootFS
	origMkdir := osMkdirAll
	t.Cleanup(func() {
		rootOpenRoot = origOpenRoot
		walkReverseProxyRootFS = origWalkReverseProxy
		osMkdirAll = origMkdir
	})

	for _, dirName := range []string{"rb-restore-dir", "rb-restore-file", "copy-walk-a", "copy-walk-b", "copy-mkdir"} {
		if err := os.MkdirAll(filepath.Join(reverseProxyDir, dirName), 0o750); err != nil {
			t.Fatalf("mkdir %s: %v", dirName, err)
		}
	}
	rootOpenRoot = mockFailingRootOpenRoot
	if err := copyRootDirToOS(reverseProxyRoot, "copy-open-fail", filepath.Join(root, "bak")); err == nil || !strings.Contains(err.Error(), "open root dir") {
		t.Fatalf("expected open root dir error, got %v", err)
	}
	rootOpenRoot = origOpenRoot

	symDir := filepath.Join(reverseProxyDir, "sym_dir")
	if err := os.MkdirAll(symDir, 0o750); err != nil {
		t.Fatalf("mkdir sym_dir: %v", err)
	}
	if err := os.Symlink("../plain.txt", filepath.Join(symDir, "bad_link")); err != nil {
		t.Fatalf("symlink bad_link: %v", err)
	}
	if err := copyRootDirToOS(reverseProxyRoot, "sym_dir", filepath.Join(root, "bak_sym")); err == nil || !strings.Contains(err.Error(), "refusing symlink") {
		t.Fatalf("expected symlink error in copyRootDirToOS, got %v", err)
	}

	walkReverseProxyRootFS = func(_ fs.FS, _ string, fn fs.WalkDirFunc) error {
		return fn("sub_info", fakeDirEntry{name: "sub_info", isDir: true, infoErr: errors.New("stat err")}, nil)
	}
	if err := copyRootDirToOS(reverseProxyRoot, "copy-walk-a", filepath.Join(root, "bak_walk")); err == nil || !strings.Contains(err.Error(), "stat") {
		t.Fatalf("expected entry info error, got %v", err)
	}
	walkReverseProxyRootFS = func(_ fs.FS, _ string, fn fs.WalkDirFunc) error {
		return fn("sub_walk", fakeDirEntry{name: "sub_walk"}, errors.New("walk err"))
	}
	if err := copyRootDirToOS(reverseProxyRoot, "copy-walk-b", filepath.Join(root, "bak_walk2")); err == nil {
		t.Fatal("expected walk err")
	}
	dirInfo, statErr := os.Stat(reverseProxyDir)
	if statErr != nil {
		t.Fatalf("stat reverseProxyDir: %v", statErr)
	}
	walkReverseProxyRootFS = func(_ fs.FS, _ string, fn fs.WalkDirFunc) error {
		return fn("sub_mkdir", fs.FileInfoToDirEntry(dirInfo), nil)
	}
	osMkdirAll = func(string, os.FileMode) error { return errors.New("mkdir err") }
	if err := copyRootDirToOS(reverseProxyRoot, "copy-mkdir", filepath.Join(root, "bak_mkdir")); err == nil || !strings.Contains(err.Error(), "mkdir") {
		t.Fatalf("expected mkdir error in copyRootDirToOS, got %v", err)
	}
	walkReverseProxyRootFS = origWalkReverseProxy
	osMkdirAll = origMkdir
}

func testCopyDirectoryIntoRootErrors(t *testing.T, root, reverseProxyDir string, reverseProxyRoot *os.Root) {
	t.Helper()

	srcFile := filepath.Join(root, "plain_copy.txt")
	if err := os.WriteFile(srcFile, []byte("hello"), 0o600); err != nil {
		t.Fatalf("write plain_copy: %v", err)
	}
	origWalk := filepathWalkDir
	origRel := filepathRel
	t.Cleanup(func() {
		filepathWalkDir = origWalk
		filepathRel = origRel
	})

	if err := copyDirectoryIntoRoot(filepath.Join(root, "missing_src_dir"), reverseProxyRoot, "bad_dst"); err == nil {
		t.Fatal("expected error copying missing dir into root")
	}
	if err := copyDirectoryIntoRoot(filepath.Join(root, "src"), reverseProxyRoot, "../../escape"); err == nil {
		t.Fatal("expected error for escaping dstName")
	}

	srcSubDir := filepath.Join(root, "src_sub")
	if err := os.MkdirAll(filepath.Join(srcSubDir, "nested"), 0o750); err != nil {
		t.Fatalf("mkdir src_sub: %v", err)
	}
	if err := os.WriteFile(filepath.Join(srcSubDir, "nested", "a.txt"), []byte("a"), 0o600); err != nil {
		t.Fatalf("write a.txt: %v", err)
	}
	if err := copyDirectoryIntoRoot(srcSubDir, reverseProxyRoot, "copy_ok"); err != nil {
		t.Fatalf("expected copyDirectoryIntoRoot success, got %v", err)
	}

	symSrcDir := filepath.Join(root, "sym_src")
	if err := os.MkdirAll(symSrcDir, 0o750); err != nil {
		t.Fatalf("mkdir sym_src: %v", err)
	}
	if err := os.Symlink(srcFile, filepath.Join(symSrcDir, "sym")); err != nil {
		t.Fatalf("symlink sym: %v", err)
	}
	if err := copyDirectoryIntoRoot(symSrcDir, reverseProxyRoot, "copy_sym"); err == nil || !strings.Contains(err.Error(), "refusing symlink") {
		t.Fatalf("expected symlink error in copyDirectoryIntoRoot, got %v", err)
	}

	filepathRel = func(_, _ string) (string, error) { return "", errors.New("rel err") }
	if err := copyDirectoryIntoRoot(srcSubDir, reverseProxyRoot, "copy_rel"); err == nil {
		t.Fatal("expected rel err in copyDirectoryIntoRoot")
	}
	filepathRel = origRel

	filepathWalkDir = func(_ string, fn fs.WalkDirFunc) error {
		return fn(filepath.Join(srcSubDir, "nested"), fakeDirEntry{name: "nested", isDir: false, infoErr: errors.New("file info err")}, nil)
	}
	if err := copyDirectoryIntoRoot(srcSubDir, reverseProxyRoot, "copy_info"); err == nil {
		t.Fatal("expected file info error in copyDirectoryIntoRoot")
	}
	filepathWalkDir = origWalk

	if err := os.WriteFile(filepath.Join(reverseProxyDir, "copy_ok", "file_blocker"), []byte("x"), 0o600); err != nil {
		t.Fatalf("write blocker: %v", err)
	}
	filepathWalkDir = func(_ string, fn fs.WalkDirFunc) error {
		return fn(filepath.Join(srcSubDir, "file_blocker"), fakeDirEntry{name: "file_blocker", isDir: true}, nil)
	}
	if err := copyDirectoryIntoRoot(srcSubDir, reverseProxyRoot, "copy_ok"); err == nil {
		t.Fatal("expected subdir open error in copyDirectoryIntoRoot")
	}
}
