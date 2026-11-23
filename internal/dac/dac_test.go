package dac

import (
	"context"
	"errors"
	"os"
	"os/user"
	"path/filepath"
	"testing"

	"github.com/webstudiobond/backup-recovery-orchestrator/internal/config"
)

func TestCollapseChangesAndNeedsUpdate(t *testing.T) {
	t.Parallel()

	changes := []Change{
		{
			Path:     "/home/app/configs",
			UID:      1010,
			GID:      1010,
			PrevUID:  0,
			PrevGID:  1010,
			Mode:     0o700,
			PrevMode: 0o750,
			HasOwner: true,
			HasMode:  true,
			HasPrev:  true,
		},
		{
			Path:     "/home/app/configs",
			UID:      0,
			GID:      1010,
			Mode:     0o750,
			HasOwner: true,
			HasMode:  true,
		},
		{
			Path:     "/home/app/data",
			UID:      1010,
			GID:      1010,
			PrevUID:  1010,
			PrevGID:  1010,
			Mode:     0o700,
			PrevMode: 0o755,
			HasOwner: true,
			HasMode:  true,
			HasPrev:  true,
		},
		{
			Path:      "/home/app/missing",
			Skipped:   true,
			SkipCause: "optional path not present",
		},
	}

	collapsed := CollapseChanges(changes)
	if len(collapsed) != 3 {
		t.Fatalf("expected 3 collapsed entries, got %d (%+v)", len(collapsed), collapsed)
	}
	if collapsed[0].NeedsUpdate() {
		t.Fatalf("expected collapsed /home/app/configs to need no update, got %+v", collapsed[0])
	}
	if !collapsed[1].NeedsUpdate() {
		t.Fatalf("expected /home/app/data mode drift to need update, got %+v", collapsed[1])
	}
	if collapsed[2].NeedsUpdate() {
		t.Fatalf("expected skipped entry not to need update, got %+v", collapsed[2])
	}
}

func TestNeedsUpdateVariants(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		change     Change
		wantUpdate bool
	}{
		{
			name:       "skipped change needs no update",
			change:     Change{Skipped: true, HasOwner: true},
			wantUpdate: false,
		},
		{
			name:       "no previous attributes with owner set",
			change:     Change{HasPrev: false, HasOwner: true},
			wantUpdate: true,
		},
		{
			name:       "no previous attributes without owner or mode",
			change:     Change{HasPrev: false, HasOwner: false, HasMode: false},
			wantUpdate: false,
		},
		{
			name:       "owner uid drift needs update",
			change:     Change{HasPrev: true, HasOwner: true, PrevUID: 1001, UID: 1002, PrevGID: 1001, GID: 1001},
			wantUpdate: true,
		},
		{
			name:       "owner gid drift needs update",
			change:     Change{HasPrev: true, HasOwner: true, PrevUID: 1001, UID: 1001, PrevGID: 1001, GID: 1002},
			wantUpdate: true,
		},
		{
			name:       "mode drift needs update",
			change:     Change{HasPrev: true, HasMode: true, PrevMode: 0o644, Mode: 0o600},
			wantUpdate: true,
		},
		{
			name:       "identical mode needs no update",
			change:     Change{HasPrev: true, HasMode: true, PrevMode: 0o644, Mode: 0o644},
			wantUpdate: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if tc.change.NeedsUpdate() != tc.wantUpdate {
				t.Fatalf("NeedsUpdate() = %v, want %v", tc.change.NeedsUpdate(), tc.wantUpdate)
			}
		})
	}
}

func TestCollapseChangesEdgeCases(t *testing.T) {
	t.Parallel()

	if CollapseChanges(nil) != nil {
		t.Fatalf("expected nil for nil slice")
	}

	pathTarget := "/data/app/file_a"
	changes := []Change{
		{Path: pathTarget, Skipped: true, SkipCause: "initial skip"},
		{Path: pathTarget, UID: 1001, GID: 1001, HasOwner: true},
		{Path: pathTarget, Skipped: true, SkipCause: "secondary skip"},
	}

	collapsed := CollapseChanges(changes)
	if len(collapsed) != 1 {
		t.Fatalf("expected 1 collapsed entry, got %d", len(collapsed))
	}
	if collapsed[0].Skipped || !collapsed[0].HasOwner || collapsed[0].UID != 1001 {
		t.Fatalf("expected unskipped override, got %+v", collapsed[0])
	}
}

func TestDefaultSyscalls(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	tmpFile := filepath.Join(tmpDir, "test_file.txt")
	if err := os.WriteFile(tmpFile, []byte("content"), 0o600); err != nil {
		t.Fatalf("write temp file: %v", err)
	}

	if err := defaultLchown(tmpFile, os.Getuid(), os.Getgid()); err != nil {
		t.Fatalf("defaultLchown failed on own file: %v", err)
	}
	if err := defaultLchown("/nonexistent/path/for/dac", 0, 0); err == nil {
		t.Fatalf("expected error from defaultLchown on nonexistent path")
	}

	if err := defaultChmod("/nonexistent/path/for/dac", 0o600); err == nil {
		t.Fatalf("expected error from defaultChmod on nonexistent path")
	}

	if _, err := defaultLookupUser("nonexistent_user_for_dac_9999"); err == nil {
		t.Fatalf("expected error from defaultLookupUser")
	}
	if curr, err := user.Current(); err == nil && curr != nil {
		if uid, uErr := defaultLookupUser(curr.Username); uErr != nil || uid < 0 {
			t.Fatalf("defaultLookupUser failed on current user: %v", uErr)
		}
		if g, gErr := user.LookupGroupId(curr.Gid); gErr == nil && g != nil {
			if gid, lErr := defaultLookupGroup(g.Name); lErr != nil || gid < 0 {
				t.Fatalf("defaultLookupGroup failed on current group: %v", lErr)
			}
		}
	}
	if _, err := defaultLookupGroup("nonexistent_group_for_dac_9999"); err == nil {
		t.Fatalf("expected error from defaultLookupGroup")
	}
}

func TestApplyHostFilesBranches(t *testing.T) {
	t.Parallel()

	applier := NewApplier()
	userCfg := config.UserConfig{Name: "host_app", UID: 1000, GID: 1000}

	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()

	_, ctxErr := applier.ApplyHostFiles(canceledCtx, userCfg, nil, false)
	if ctxErr == nil {
		t.Fatalf("expected error on canceled context in ApplyHostFiles")
	}

	items := []config.HostFileConfig{
		{Path: "/skipped/no_dac"},
	}
	changes, err := applier.ApplyHostFiles(context.Background(), userCfg, items, false)
	if err != nil {
		t.Fatalf("unexpected error on empty dac attributes: %v", err)
	}
	if len(changes) != 0 {
		t.Fatalf("expected 0 changes for skipped item, got %d", len(changes))
	}

	failingItems := []config.HostFileConfig{
		{Path: "/nonexistent/host/file", Owner: "sysadmin:sysadmin", FileMode: "0600"},
	}
	_, failErr := applier.ApplyHostFiles(context.Background(), userCfg, failingItems, false)
	if failErr == nil {
		t.Fatalf("expected error on nonexistent host file")
	}
}

func mockInvalidUserLookup(_ string) (*user.User, error) {
	return &user.User{Uid: "not-a-number"}, nil
}

func mockInvalidGroupLookup(_ string) (*user.Group, error) {
	return &user.Group{Gid: "not-a-number"}, nil
}

func mockFailingAbs(_ string) (string, error) {
	return "", errors.New("simulated abs failure")
}

func TestDefaultLookupUserAndGroupMockErrors(t *testing.T) {
	origUser := userLookup
	origGroup := userLookupGroup
	defer func() {
		userLookup = origUser
		userLookupGroup = origGroup
	}()

	userLookup = mockInvalidUserLookup
	userLookupGroup = mockInvalidGroupLookup

	if _, err := defaultLookupUser("dummy"); err == nil {
		t.Fatalf("expected error from non-numeric uid")
	}

	if _, err := defaultLookupGroup("dummy"); err == nil {
		t.Fatalf("expected error from non-numeric gid")
	}
}

func TestApplyStackAbsError(t *testing.T) {
	origAbs := filepathAbs
	defer func() {
		filepathAbs = origAbs
	}()

	filepathAbs = mockFailingAbs

	applier := NewApplier()
	userCfg := config.UserConfig{Name: "app", UID: 1000, GID: 1000}
	_, err := applier.ApplyStack(context.Background(), "/tmp/dir", userCfg, nil, false)
	if err == nil {
		t.Fatalf("expected error from failing filepathAbs")
	}
}
