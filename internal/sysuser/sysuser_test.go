package sysuser

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/webstudiobond/backup-recovery-orchestrator/internal/cmdexec"
	"github.com/webstudiobond/backup-recovery-orchestrator/internal/config"
)

type fakeSysRunner struct {
	runErr    error
	failMatch string
	calls     [][]string
}

func (f *fakeSysRunner) Run(_ context.Context, spec *cmdexec.Spec) error {
	f.calls = append(f.calls, append([]string{spec.Name}, slices.Clone(spec.Args)...))
	if f.runErr != nil {
		return f.runErr
	}
	if f.failMatch != "" && (spec.Name == f.failMatch || strings.Contains(strings.Join(spec.Args, " "), f.failMatch)) {
		return errors.New("simulated failure for " + f.failMatch)
	}
	return nil
}

func (f *fakeSysRunner) Output(_ context.Context, _ *cmdexec.Spec) ([]byte, error) {
	return nil, nil
}

func writeAccountFiles(t *testing.T) (pPath, gPath string) {
	t.Helper()

	dir := t.TempDir()
	pPath = filepath.Join(dir, "accounts")
	gPath = filepath.Join(dir, "groups")
	usersData := "root:x:0:0:root:/root:/bin/bash\napp-alpha:x:1010:1010::/home/app-alpha:/usr/sbin/nologin\nother:x:1020:1020::/home/other:/usr/sbin/nologin\n"
	groupsData := "root:x:0:\napp-alpha:x:1010:\nother:x:1020:\n"

	if err := os.WriteFile(pPath, []byte(usersData), 0o600); err != nil {
		t.Fatalf("write accounts: %v", err)
	}
	if err := os.WriteFile(gPath, []byte(groupsData), 0o600); err != nil {
		t.Fatalf("write groups: %v", err)
	}
	return pPath, gPath
}

func TestNewManagerAndPaths(t *testing.T) {
	t.Parallel()

	mgrDefault := NewManager(nil)
	if mgrDefault == nil || mgrDefault.Runner == nil {
		t.Fatal("expected default Manager and Runner")
	}
	if mgrDefault.passwdPath() != defaultPasswdPath || mgrDefault.groupPath() != defaultGroupPath {
		t.Fatalf("expected default paths, got %q and %q", mgrDefault.passwdPath(), mgrDefault.groupPath())
	}

	emptyMgr := &Manager{}
	if emptyMgr.passwdPath() != defaultPasswdPath || emptyMgr.groupPath() != defaultGroupPath {
		t.Fatalf("expected default fallback paths, got %q and %q", emptyMgr.passwdPath(), emptyMgr.groupPath())
	}

	custom := &Manager{PasswdPath: "/custom/passwd", GroupPath: "/custom/group"}
	if custom.passwdPath() != "/custom/passwd" || custom.groupPath() != "/custom/group" {
		t.Fatalf("expected custom paths, got %q and %q", custom.passwdPath(), custom.groupPath())
	}

	mgrWithRunner := NewManager(&fakeSysRunner{})
	if mgrWithRunner == nil {
		t.Fatal("expected non-nil Manager")
	}
}

func TestEnsureUserExistingAndCreate(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	t.Run("exact existing match succeeds idempotently", func(t *testing.T) {
		t.Parallel()
		pPath, gPath := writeAccountFiles(t)
		runner := &fakeSysRunner{}
		mgr := &Manager{Runner: runner, PasswdPath: pPath, GroupPath: gPath}

		status, err := mgr.EnsureUser(ctx, config.UserConfig{Name: "app-alpha", UID: 1010, GID: 1010}, "/home/app-alpha", false)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if status != StatusExisting || len(runner.calls) != 0 {
			t.Fatalf("expected existing status with zero commands, got %q calls=%+v", status, runner.calls)
		}
	})

	t.Run("creates absent user and group with exact IDs", func(t *testing.T) {
		t.Parallel()
		pPath, gPath := writeAccountFiles(t)
		runner := &fakeSysRunner{}
		mgr := &Manager{Runner: runner, PasswdPath: pPath, GroupPath: gPath}

		target := config.UserConfig{Name: "app-beta", UID: 1015, GID: 1015}
		homePath := "/home/" + target.Name

		dryStatus, dryErr := mgr.EnsureUser(ctx, target, homePath, true)
		if dryErr != nil || dryStatus != StatusCreated || len(runner.calls) != 0 {
			t.Fatalf("unexpected dry-run result: status=%q err=%v calls=%+v", dryStatus, dryErr, runner.calls)
		}

		status, err := mgr.EnsureUser(ctx, target, homePath, false)
		if err != nil || status != StatusCreated {
			t.Fatalf("expected created status, got %q err=%v", status, err)
		}
		if len(runner.calls) != 2 {
			t.Fatalf("expected 2 commands (groupadd, useradd), got %+v", runner.calls)
		}
		if !slices.Equal(runner.calls[0], []string{"groupadd", "-g", "1015", target.Name}) {
			t.Fatalf("unexpected groupadd call: %+v", runner.calls[0])
		}
		if !slices.Equal(runner.calls[1], []string{"useradd", "-u", "1015", "-g", "1015", "-M", "-d", homePath, "-s", "/usr/sbin/nologin", target.Name}) {
			t.Fatalf("unexpected useradd call: %+v", runner.calls[1])
		}
	})
}

func TestEnsureUserConflicts(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	pPath, gPath := writeAccountFiles(t)
	mgr := &Manager{Runner: &fakeSysRunner{}, PasswdPath: pPath, GroupPath: gPath}

	cases := []struct {
		name        string
		errContains string
		user        config.UserConfig
	}{
		{
			name:        "name exists with wrong uid",
			user:        config.UserConfig{Name: "app-alpha", UID: 1999, GID: 1010},
			errContains: "uid/gid conflict",
		},
		{
			name:        "uid already owned by another user",
			user:        config.UserConfig{Name: "newapp", UID: 1020, GID: 1099},
			errContains: "already assigned to user",
		},
		{
			name:        "gid already owned by another group",
			user:        config.UserConfig{Name: "newapp", UID: 1099, GID: 1020},
			errContains: "already assigned to group",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := mgr.EnsureUser(ctx, tc.user, "/home/"+tc.user.Name, false)
			if err == nil || !strings.Contains(err.Error(), tc.errContains) {
				t.Fatalf("expected error containing %q, got %v", tc.errContains, err)
			}
		})
	}
}

func TestLoadAndValidateAccountErrors(t *testing.T) {
	t.Parallel()

	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()

	pPath, gPath := writeAccountFiles(t)

	cases := []struct {
		ctx           context.Context
		name          string
		passwdPath    string
		groupPath     string
		errContains   string
		user          config.UserConfig
		keepEmptyName bool
	}{
		{
			name:        "canceled_context",
			ctx:         canceledCtx,
			passwdPath:  pPath,
			groupPath:   gPath,
			errContains: "context canceled",
		},
		{
			name:          "empty_user_name",
			ctx:           context.Background(),
			user:          config.UserConfig{Name: "  ", UID: 1050, GID: 1050},
			passwdPath:    pPath,
			groupPath:     gPath,
			errContains:   "target user name must not be empty",
			keepEmptyName: true,
		},
		{
			name:        "passwd_parse_error",
			ctx:         context.Background(),
			passwdPath:  "/nonexistent/passwd",
			groupPath:   gPath,
			errContains: "open passwd file",
		},
		{
			name:        "group_parse_error",
			ctx:         context.Background(),
			passwdPath:  pPath,
			groupPath:   "/nonexistent/group",
			errContains: "open group file",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			u := tc.user
			if !tc.keepEmptyName {
				u = config.UserConfig{Name: "app-test", UID: 1050, GID: 1050}
			}
			mgr := &Manager{Runner: &fakeSysRunner{}, PasswdPath: tc.passwdPath, GroupPath: tc.groupPath}
			_, err := mgr.EnsureUser(tc.ctx, u, "/home/"+u.Name, false)
			if err == nil || !strings.Contains(err.Error(), tc.errContains) {
				t.Fatalf("expected error containing %q, got %v", tc.errContains, err)
			}
		})
	}
}

func TestValidateAccountPartialConflict(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	pOnly := filepath.Join(dir, "passwd_only")
	gOnly := filepath.Join(dir, "group_only")
	if err := os.WriteFile(pOnly, []byte("onlyuser:x:1070:1070::/home:/bin/sh\n"), 0o600); err != nil {
		t.Fatalf("write pOnly: %v", err)
	}
	if err := os.WriteFile(gOnly, []byte("onlygroup:x:1080:\n"), 0o600); err != nil {
		t.Fatalf("write gOnly: %v", err)
	}

	emptyP := filepath.Join(dir, "empty_p")
	emptyG := filepath.Join(dir, "empty_g")
	if err := os.WriteFile(emptyP, []byte(""), 0o600); err != nil {
		t.Fatalf("write emptyP: %v", err)
	}
	if err := os.WriteFile(emptyG, []byte(""), 0o600); err != nil {
		t.Fatalf("write emptyG: %v", err)
	}

	ctx := context.Background()

	t.Run("user_exists_without_group", func(t *testing.T) {
		t.Parallel()
		mgr := &Manager{Runner: &fakeSysRunner{}, PasswdPath: pOnly, GroupPath: emptyG}
		_, err := mgr.EnsureUser(ctx, config.UserConfig{Name: "onlyuser", UID: 1070, GID: 1070}, "/home", false)
		if err == nil || !strings.Contains(err.Error(), "partial account conflict") {
			t.Fatalf("expected partial conflict error, got %v", err)
		}
	})

	t.Run("group_exists_without_user", func(t *testing.T) {
		t.Parallel()
		mgr := &Manager{Runner: &fakeSysRunner{}, PasswdPath: emptyP, GroupPath: gOnly}
		_, err := mgr.EnsureUser(ctx, config.UserConfig{Name: "onlygroup", UID: 1080, GID: 1080}, "/home", false)
		if err == nil || !strings.Contains(err.Error(), "partial account conflict") {
			t.Fatalf("expected partial conflict error, got %v", err)
		}
	})
}

func TestProvisionAccountErrors(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	pPath, gPath := writeAccountFiles(t)
	target := config.UserConfig{Name: "app-fail", UID: 1090, GID: 1090}

	t.Run("groupadd_failure", func(t *testing.T) {
		t.Parallel()
		runner := &fakeSysRunner{failMatch: "groupadd"}
		mgr := &Manager{Runner: runner, PasswdPath: pPath, GroupPath: gPath}
		_, err := mgr.EnsureUser(ctx, target, "/home/app-fail", false)
		if err == nil || !strings.Contains(err.Error(), "create group") {
			t.Fatalf("expected create group error, got %v", err)
		}
	})

	t.Run("useradd_failure", func(t *testing.T) {
		t.Parallel()
		runner := &fakeSysRunner{failMatch: "useradd"}
		mgr := &Manager{Runner: runner, PasswdPath: pPath, GroupPath: gPath}
		_, err := mgr.EnsureUser(ctx, target, "/home/app-fail", false)
		if err == nil || !strings.Contains(err.Error(), "create user") {
			t.Fatalf("expected create user error, got %v", err)
		}
	})
}

func TestVerifyExistingUser(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	pPath, gPath := writeAccountFiles(t)
	mgr := &Manager{Runner: &fakeSysRunner{}, PasswdPath: pPath, GroupPath: gPath}

	cases := []struct {
		name        string
		errContains string
		user        config.UserConfig
	}{
		{
			name: "matching existing user succeeds",
			user: config.UserConfig{Name: "other", UID: 1020, GID: 1020},
		},
		{
			name:        "missing user rejected",
			user:        config.UserConfig{Name: "missing", UID: 1099, GID: 1099},
			errContains: "does not exist on host",
		},
		{
			name:        "id conflict rejected",
			user:        config.UserConfig{Name: "other", UID: 1999, GID: 1020},
			errContains: "found uid=1020 gid=1020",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := mgr.VerifyExistingUser(ctx, tc.user)
			if tc.errContains != "" {
				if err == nil || !strings.Contains(err.Error(), tc.errContains) {
					t.Fatalf("expected error containing %q, got %v", tc.errContains, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected VerifyExistingUser error: %v", err)
			}
		})
	}
}
