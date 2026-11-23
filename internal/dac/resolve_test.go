package dac

import (
	"errors"
	"os"
	"testing"

	"github.com/webstudiobond/backup-recovery-orchestrator/internal/config"
)

func TestResolveOwner(t *testing.T) {
	t.Parallel()

	userCfg := config.UserConfig{Name: "app_deployer", UID: 1005, GID: 1005}

	applier := &Applier{
		LookupUser: func(name string) (int, error) {
			if name == "known_user" {
				return 1010, nil
			}
			return 0, errors.New("lookup user failure")
		},
		LookupGroup: func(name string) (int, error) {
			if name == "known_group" {
				return 1020, nil
			}
			return 0, errors.New("lookup group failure")
		},
	}

	cases := []struct {
		name      string
		ownerSpec string
		wantUID   int
		wantGID   int
		wantOk    bool
		wantErr   bool
	}{
		{name: "empty owner specification string", ownerSpec: "   ", wantUID: 0, wantGID: 0, wantOk: false, wantErr: false},
		{name: "missing colon in specification", ownerSpec: "single_user_only", wantUID: 0, wantGID: 0, wantOk: false, wantErr: true},
		{name: "invalid uid principal resolution", ownerSpec: "missing_user:root", wantUID: 0, wantGID: 0, wantOk: false, wantErr: true},
		{name: "invalid gid principal resolution", ownerSpec: "root:missing_group", wantUID: 0, wantGID: 0, wantOk: false, wantErr: true},
		{name: "valid root user and group", ownerSpec: "root:root", wantUID: 0, wantGID: 0, wantOk: true, wantErr: false},
		{name: "valid custom looked up user and group", ownerSpec: "known_user:known_group", wantUID: 1010, wantGID: 1020, wantOk: true, wantErr: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			uid, gid, ok, err := applier.resolveOwner(tc.ownerSpec, userCfg)
			if (err != nil) != tc.wantErr {
				t.Fatalf("resolveOwner(%q) err = %v, wantErr = %v", tc.ownerSpec, err, tc.wantErr)
			}
			if ok != tc.wantOk || uid != tc.wantUID || gid != tc.wantGID {
				t.Fatalf("resolveOwner(%q) = (%d, %d, %v), want (%d, %d, %v)", tc.ownerSpec, uid, gid, ok, tc.wantUID, tc.wantGID, tc.wantOk)
			}
		})
	}
}

func TestResolvePrincipal(t *testing.T) {
	t.Parallel()

	applier := &Applier{
		LookupUser: func(name string) (int, error) {
			if name == "mock_daemon" {
				return 1050, nil
			}
			return 0, errors.New("user not found")
		},
		LookupGroup: func(name string) (int, error) {
			if name == "mock_grp" {
				return 1060, nil
			}
			return 0, errors.New("group not found")
		},
	}

	cases := []struct {
		name      string
		token     string
		stackName string
		wantID    int
		stackID   uint32
		isUser    bool
		wantErr   bool
	}{
		{name: "root principal token", token: "root", stackName: "", wantID: 0, stackID: 0, isUser: true, wantErr: false},
		{name: "stack alias principal token", token: "stack", stackName: "", wantID: 2001, stackID: 2001, isUser: true, wantErr: false},
		{name: "stack username exact match token", token: "deploy_user", stackName: "deploy_user", wantID: 2002, stackID: 2002, isUser: true, wantErr: false},
		{name: "numeric integer literal token", token: "3003", stackName: "", wantID: 3003, stackID: 0, isUser: true, wantErr: false},
		{name: "lookup user success branch", token: "mock_daemon", stackName: "", wantID: 1050, stackID: 0, isUser: true, wantErr: false},
		{name: "lookup user error branch", token: "unknown_user", stackName: "", wantID: 0, stackID: 0, isUser: true, wantErr: true},
		{name: "lookup group success branch", token: "mock_grp", stackName: "", wantID: 1060, stackID: 0, isUser: false, wantErr: false},
		{name: "lookup group error branch", token: "unknown_group", stackName: "", wantID: 0, stackID: 0, isUser: false, wantErr: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			id, err := applier.resolvePrincipal(tc.token, tc.stackName, tc.stackID, tc.isUser)
			if (err != nil) != tc.wantErr {
				t.Fatalf("resolvePrincipal(%q) err = %v, wantErr = %v", tc.token, err, tc.wantErr)
			}
			if err == nil && id != tc.wantID {
				t.Fatalf("resolvePrincipal(%q) = %d, want %d", tc.token, id, tc.wantID)
			}
		})
	}
}

func TestResolvePrincipalDefaultFallbacks(t *testing.T) {
	t.Parallel()

	emptyApplier := &Applier{}
	_, errUser := emptyApplier.resolvePrincipal("nonexistent_user_token_xyz", "", 0, true)
	if errUser == nil {
		t.Fatalf("expected error from default lookup user fallback")
	}
	_, errGroup := emptyApplier.resolvePrincipal("nonexistent_group_token_xyz", "", 0, false)
	if errGroup == nil {
		t.Fatalf("expected error from default lookup group fallback")
	}
}

func TestParseOptionalMode(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		raw      string
		wantMode os.FileMode
		wantSet  bool
		wantErr  bool
	}{
		{name: "empty whitespace mode", raw: "   ", wantMode: 0, wantSet: false, wantErr: false},
		{name: "invalid non octal digits", raw: "0899", wantMode: 0, wantSet: false, wantErr: true},
		{name: "exceeds maximum permission bits", raw: "10000", wantMode: 0, wantSet: false, wantErr: true},
		{name: "valid standard directory mode", raw: "0755", wantMode: os.FileMode(0o755), wantSet: true, wantErr: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			mode, set, err := parseOptionalMode(tc.raw)
			if (err != nil) != tc.wantErr {
				t.Fatalf("parseOptionalMode(%q) err = %v, wantErr = %v", tc.raw, err, tc.wantErr)
			}
			if set != tc.wantSet || mode != tc.wantMode {
				t.Fatalf("parseOptionalMode(%q) = (%04o, %v), want (%04o, %v)", tc.raw, mode, set, tc.wantMode, tc.wantSet)
			}
		})
	}
}

func TestResolveWithinRoot(t *testing.T) {
	t.Parallel()

	root := t.TempDir()

	cases := []struct {
		name    string
		relPath string
		wantErr bool
	}{
		{name: "absolute path rejected", relPath: "/etc/shadow", wantErr: true},
		{name: "parent traversal rejected", relPath: "../outside_root", wantErr: true},
		{name: "valid relative nested path", relPath: "sub/dir_path", wantErr: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			target, err := resolveWithinRoot(root, tc.relPath)
			if (err != nil) != tc.wantErr {
				t.Fatalf("resolveWithinRoot(%q) err = %v, wantErr = %v", tc.relPath, err, tc.wantErr)
			}
			if err == nil && target == "" {
				t.Fatalf("expected non-empty resolved path")
			}
		})
	}
}

func mockLowMaxInt() uint64 {
	return 10
}

func mockFailingRel(_, _ string) (string, error) {
	return "", errors.New("simulated rel failure")
}

func TestResolvePrincipalExceedsMaxInt(t *testing.T) {
	origMax := maxIntLimit
	defer func() {
		maxIntLimit = origMax
	}()

	maxIntLimit = mockLowMaxInt

	applier := &Applier{}
	if _, err := applier.resolvePrincipal("stack", "demo", 100, true); err == nil {
		t.Fatalf("expected error from stack id exceeding maxInt")
	}

	if _, err := applier.resolvePrincipal("100", "demo", 0, true); err == nil {
		t.Fatalf("expected error from numeric id exceeding maxInt")
	}
}

func TestResolveWithinRootRelError(t *testing.T) {
	origRel := filepathRel
	defer func() {
		filepathRel = origRel
	}()

	filepathRel = mockFailingRel

	if _, err := resolveWithinRoot("/some/root", "relative/path"); err == nil {
		t.Fatalf("expected error from failing filepathRel")
	}
}
