package main

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/webstudiobond/backup-recovery-orchestrator/internal/cmdexec"
	"github.com/webstudiobond/backup-recovery-orchestrator/internal/orchestrator"
)

type fakeCLIRunner struct{}

func (fakeCLIRunner) Run(_ context.Context, spec *cmdexec.Spec) error {
	if spec.Name == "tar" {
		if spec.Stdout != nil {
			_, err := spec.Stdout.Write([]byte("archive-content"))
			return err
		}
		for i := range spec.Args {
			if strings.HasSuffix(spec.Args[i], ".tar") || strings.HasSuffix(spec.Args[i], ".tar.zst") {
				if err := os.WriteFile(spec.Args[i], []byte("archive-content"), 0o600); err != nil {
					return err
				}
			}
		}
	}
	if spec.Name == "zstd" && spec.Stdin != nil && spec.Stdout != nil {
		_, err := io.Copy(spec.Stdout, spec.Stdin)
		return err
	}
	return nil
}

func (fakeCLIRunner) Output(_ context.Context, _ *cmdexec.Spec) ([]byte, error) {
	return nil, nil
}

type failingWriter struct{}

func (failingWriter) Write(_ []byte) (int, error) {
	return 0, errors.New("write failure")
}

type fakeLockHandle struct {
	releaseErr error
}

func (f fakeLockHandle) Release() error {
	return f.releaseErr
}

func mockJSONMarshalIndentFail(_ any, _, _ string) ([]byte, error) {
	return nil, errors.New("marshal failure")
}

var (
	testPasswdPath string
	testGroupPath  string
)

func mockNewOrchestrator(runner cmdexec.Runner) *orchestrator.Orchestrator {
	orch := orchestrator.New(runner)
	if testPasswdPath != "" {
		orch.SysUser.PasswdPath = testPasswdPath
	}
	if testGroupPath != "" {
		orch.SysUser.GroupPath = testGroupPath
	}
	return orch
}

func writeAccountFixtures(t *testing.T, root string) (accountPath, groupPath string) {
	t.Helper()

	accountPath = filepath.Join(root, "accounts")
	groupPath = filepath.Join(root, "groups")
	usersLines := "root:x:0:0:root:/root:/bin/bash\napp-user:x:1011:1011::/home/app-user:/usr/sbin/nologin\n"
	groupsLines := "root:x:0:\napp-user:x:1011:\n"
	if err := os.WriteFile(accountPath, []byte(usersLines), 0o600); err != nil {
		t.Fatalf("write test accounts: %v", err)
	}
	if err := os.WriteFile(groupPath, []byte(groupsLines), 0o600); err != nil {
		t.Fatalf("write test group: %v", err)
	}
	return accountPath, groupPath
}

func writeFixtureConfig(t *testing.T, root string) string {
	t.Helper()

	homeDir := filepath.Join(root, "home")
	alphaHome := filepath.Join(homeDir, "app-alpha")
	rootHome := filepath.Join(homeDir, "app-root")
	for _, dir := range []string{alphaHome, rootHome} {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			t.Fatalf("mkdir home: %v", err)
		}
		bDir := filepath.Join(root, "backups", filepath.Base(dir))
		if err := os.MkdirAll(bDir, 0o750); err != nil {
			t.Fatalf("mkdir backup: %v", err)
		}
		if err := os.WriteFile(filepath.Join(bDir, filepath.Base(dir)+"_2026-11-22_10-00-00.tar"), []byte("tar"), 0o600); err != nil {
			t.Fatalf("write tar: %v", err)
		}
	}

	cfgPath := filepath.Join(root, "config.json")
	globalJSON := `{
  "backup_root": "` + filepath.Join(root, "backups") + `",
  "lock_file": "` + filepath.Join(root, "bro.lock") + `",
  "destinations": [
    {
      "name": "remote-drive",
      "type": "rclone",
      "remote": "remote:bucket/backups"
    }
  ],
  "lvm": {
    "vg_name": "ubuntu-vg",
    "lv_name": "lv_home",
    "snapshot_name": "home_snap",
    "snapshot_size": "35G",
    "mount_point": "` + filepath.Join(root, "mnt", "snap") + `"
  }
}`
	if err := os.WriteFile(cfgPath, []byte(globalJSON), 0o600); err != nil {
		t.Fatalf("write config.json: %v", err)
	}

	profilesDir := filepath.Join(root, "profiles")
	confDir := filepath.Join(root, "conf.d")
	for _, dir := range []string{profilesDir, confDir} {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			t.Fatalf("mkdir %q: %v", dir, err)
		}
	}

	profileJSON := `{"files":{"excludes":["*.tmp"]}}`
	if err := os.WriteFile(filepath.Join(profilesDir, "base.json"), []byte(profileJSON), 0o600); err != nil {
		t.Fatalf("write profile: %v", err)
	}

	stackJSON := `{"extends":"base","user":{"name":"app-alpha","uid":1012,"gid":1012},"stack_dir":"` + alphaHome + `","angie":{"domain_confs":["status.example.com.conf"]}}`
	if err := os.WriteFile(filepath.Join(confDir, "app-alpha.json"), []byte(stackJSON), 0o600); err != nil {
		t.Fatalf("write stack: %v", err)
	}

	rootStackJSON := `{"extends":"base","user":{"name":"root","uid":0,"gid":0},"stack_dir":"` + rootHome + `"}`
	if err := os.WriteFile(filepath.Join(confDir, "app-root.json"), []byte(rootStackJSON), 0o600); err != nil {
		t.Fatalf("write root stack: %v", err)
	}

	return cfgPath
}
