package orchestrator

import (
	"bytes"
	"errors"
	"testing"

	"github.com/webstudiobond/backup-recovery-orchestrator/internal/cmdexec"
	"github.com/webstudiobond/backup-recovery-orchestrator/internal/config"
)

func TestOrchestratorNew(t *testing.T) {
	t.Parallel()

	oDefault := New(nil)
	if oDefault == nil || oDefault.Docker == nil || oDefault.LVM == nil || oDefault.Archive == nil {
		t.Fatalf("expected fully initialized Orchestrator with nil runner")
	}

	oCustom := New(cmdexec.OSRunner{})
	if oCustom == nil || oCustom.Docker == nil {
		t.Fatalf("expected fully initialized Orchestrator with custom runner")
	}
}

func TestSelectStacks(t *testing.T) {
	t.Parallel()

	cases := []struct {
		bundle       *config.Bundle
		name         string
		reqStack     string
		wantNames    []string
		duplicateReq bool
		wantErr      bool
	}{
		{
			name:    "nil bundle error",
			bundle:  nil,
			wantErr: true,
		},
		{
			name: "explicit requested with duplicates",
			bundle: &config.Bundle{
				Stacks: []config.StackConfig{
					{Name: "stack-a", Enabled: true},
					{Name: "stack-b", Enabled: false},
				},
			},
			duplicateReq: true,
			wantNames:    []string{"stack-a", "stack-b"},
		},
		{
			name: "unknown stack requested",
			bundle: &config.Bundle{
				Stacks: []config.StackConfig{
					{Name: "stack-c", Enabled: true},
				},
			},
			reqStack: "stack-missing",
			wantErr:  true,
		},
		{
			name: "enabled fallback",
			bundle: &config.Bundle{
				Stacks: []config.StackConfig{
					{Name: "stack-d", Enabled: true},
					{Name: "stack-e", Enabled: false},
					{Name: "stack-f", Enabled: true},
				},
			},
			wantNames: []string{"stack-d", "stack-f"},
		},
		{
			name: "no enabled stacks error",
			bundle: &config.Bundle{
				Stacks: []config.StackConfig{
					{Name: "stack-g", Enabled: false},
				},
			},
			wantErr: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var requested []string
			if tc.duplicateReq && len(tc.wantNames) >= 2 {
				requested = []string{tc.wantNames[0], tc.wantNames[0], tc.wantNames[1]}
			} else if tc.reqStack != "" {
				requested = []string{tc.reqStack}
			}

			selected, err := selectStacks(tc.bundle, requested)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			assertStackNames(t, selected, tc.wantNames)
		})
	}
}

func assertStackNames(t *testing.T, selected []config.StackConfig, want []string) {
	t.Helper()
	if len(selected) != len(want) {
		t.Fatalf("expected %d stacks, got %d", len(want), len(selected))
	}
	for i, w := range want {
		if selected[i].Name != w {
			t.Fatalf("stack[%d] expected %q, got %q", i, w, selected[i].Name)
		}
	}
}

func TestSelectDestinations(t *testing.T) {
	t.Parallel()

	dests := []config.DestinationConfig{
		{Name: "nas-primary", Type: "mount"},
		{Name: "cloud-secondary", Type: "rclone"},
	}

	cases := []struct {
		name      string
		filter    string
		matchDest string
		dests     []config.DestinationConfig
		wantCount int
		wantErr   bool
	}{
		{
			name:    "empty slice error",
			dests:   nil,
			filter:  "",
			wantErr: true,
		},
		{
			name:      "empty filter returns all",
			dests:     dests,
			filter:    "  ",
			wantCount: 2,
		},
		{
			name:      "matching filter",
			dests:     dests,
			matchDest: "nas-primary",
			wantCount: 1,
		},
		{
			name:    "unknown destination filter",
			dests:   dests,
			filter:  "unknown-dest",
			wantErr: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			filter := tc.filter
			if tc.matchDest != "" {
				filter = tc.matchDest
			}

			res, err := selectDestinations(tc.dests, filter)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(res) != tc.wantCount {
				t.Fatalf("expected %d destinations, got %d", tc.wantCount, len(res))
			}
			if tc.matchDest != "" && res[0].Name != tc.matchDest {
				t.Fatalf("expected %q, got %q", tc.matchDest, res[0].Name)
			}
		})
	}
}

func TestWrapRemoveErr(t *testing.T) {
	t.Parallel()

	if err := wrapRemoveErr(nil, "/tmp/dir"); err != nil {
		t.Fatalf("expected nil for nil err, got %v", err)
	}

	raw := errors.New("permission denied")
	wrapped := wrapRemoveErr(raw, "/tmp/test-dir")
	if wrapped == nil || !errors.Is(wrapped, raw) {
		t.Fatalf("expected wrapped error to contain raw err, got %v", wrapped)
	}
}

type testFailingWriter struct{}

func (f *testFailingWriter) Write(_ []byte) (int, error) {
	return 0, errors.New("write failure")
}

func TestWriteLine(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	writeLine(&buf, "hello")
	if buf.String() != "hello\n" {
		t.Fatalf("expected hello\\n, got %q", buf.String())
	}

	writeLine(&testFailingWriter{}, "fail line")
}
