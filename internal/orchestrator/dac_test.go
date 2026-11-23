package orchestrator

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/webstudiobond/backup-recovery-orchestrator/internal/config"
	"github.com/webstudiobond/backup-recovery-orchestrator/internal/dac"
)

func TestApplyDACLiveDryRunAndApply(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := t.TempDir()
	stackName := "app-eps"
	bundle := buildTestBundle(t, root, stackName)
	pPath, gPath := writeFakeAccounts(t, root, stackName)
	orch := newTestOrchestrator(&fakeOrchRunner{}, pPath, gPath, nil)

	composePath := bundle.Stacks[0].ComposeFile
	if err := os.Chmod(composePath, 0o400); err != nil {
		t.Fatalf("chmod compose file: %v", err)
	}
	hostPath := bundle.Stacks[0].HostFiles[0].Path
	if err := os.Chmod(hostPath, 0o600); err != nil {
		t.Fatalf("chmod host file: %v", err)
	}
	bundle.Stacks[0].HostFiles = append(bundle.Stacks[0].HostFiles, config.HostFileConfig{
		Path:     filepath.Join(root, "etc", "optional-absent"),
		Owner:    "0:0",
		FileMode: "0400",
		Optional: true,
	})

	var dryBuf bytes.Buffer
	if dryErr := orch.ApplyDAC(ctx, &bundle, &DACOptions{
		Out:    &dryBuf,
		Stack:  stackName,
		DryRun: true,
	}); dryErr != nil {
		t.Fatalf("ApplyDAC dry-run failed: %v", dryErr)
	}
	assertContainsAll(t, dryBuf.String(), []string{"Dry-run DAC plan", "CHANGE " + composePath, "mode 0400 -> 0600", "CHANGE " + hostPath, "mode 0600 -> 0400", "SKIP ", "changed=2", "skipped=1"})

	composeInfo, statErr := os.Stat(composePath)
	if statErr != nil || composeInfo.Mode().Perm() != 0o400 {
		t.Fatalf("expected dry-run not to modify %q mode, got %v err=%v", composePath, composeInfo.Mode().Perm(), statErr)
	}

	var applyBuf bytes.Buffer
	if applyErr := orch.ApplyDAC(ctx, &bundle, &DACOptions{
		Out:   &applyBuf,
		Stack: stackName,
	}); applyErr != nil {
		t.Fatalf("ApplyDAC apply failed: %v", applyErr)
	}
	assertContainsAll(t, applyBuf.String(), []string{"UPDATED " + composePath, "UPDATED " + hostPath})
	assertFileMode(t, composePath, 0o600)
	assertFileMode(t, hostPath, 0o400)

	curOwner := fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid())
	bundle.Stacks[0].DAC[0].Owner = curOwner
	bundle.Stacks[0].HostFiles[0].Owner = curOwner

	var idempotentBuf bytes.Buffer
	if idemErr := orch.ApplyDAC(ctx, &bundle, &DACOptions{
		Out:    &idempotentBuf,
		Stack:  stackName,
		DryRun: true,
	}); idemErr != nil {
		t.Fatalf("ApplyDAC idempotent dry-run failed: %v", idemErr)
	}
	assertContainsAll(t, idempotentBuf.String(), []string{"changed=0 unchanged=2 skipped=1"})
}

func TestApplyDACErrors(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := t.TempDir()
	stackName := "app-zeta"
	pPath, gPath := writeFakeAccounts(t, root, stackName)
	orch := newTestOrchestrator(&fakeOrchRunner{}, pPath, gPath, nil)

	canceledCtx, cancel := context.WithCancel(ctx)
	cancel()
	bInit := buildTestBundle(t, root, stackName)
	if err := orch.ApplyDAC(canceledCtx, &bInit, &DACOptions{Stack: stackName}); err == nil {
		t.Fatal("expected error on canceled context")
	}

	if err := orch.ApplyDAC(ctx, nil, &DACOptions{Stack: stackName}); err == nil {
		t.Fatal("expected error on nil bundle")
	}

	if err := orch.ApplyDAC(ctx, &bInit, nil); err == nil {
		t.Fatal("expected error on nil options")
	}

	errCases := []struct {
		setup   func(t *testing.T, b *config.Bundle)
		name    string
		stack   string
		wantErr string
	}{
		{
			name:    "empty stack rejected",
			stack:   "",
			wantErr: "stack name is required",
		},
		{
			name:    "whitespace stack rejected",
			stack:   "   ",
			wantErr: "stack name is required",
		},
		{
			name:    "unknown stack rejected",
			stack:   "unknown",
			wantErr: "not found in configuration",
		},
		{
			name: "missing host user rejected",
			setup: func(_ *testing.T, b *config.Bundle) {
				b.Stacks[0].User = config.UserConfig{Name: "ghost", UID: 1099, GID: 1099}
			},
			stack:   stackName,
			wantErr: "does not exist on host",
		},
		{
			name: "uid mismatch rejected",
			setup: func(_ *testing.T, b *config.Bundle) {
				b.Stacks[0].User.UID = 1999
			},
			stack:   stackName,
			wantErr: "uid/gid conflict",
		},
		{
			name: "missing stack dir rejected",
			setup: func(_ *testing.T, b *config.Bundle) {
				b.Stacks[0].StackDir = filepath.Join(b.ConfigDir, "missing_stack_dir")
			},
			stack:   stackName,
			wantErr: "directory",
		},
		{
			name: "file stack dir rejected",
			setup: func(t *testing.T, b *config.Bundle) {
				filePath := filepath.Join(b.ConfigDir, "stack_as_file")
				if err := os.WriteFile(filePath, []byte("x"), 0o600); err != nil {
					t.Fatalf("write file: %v", err)
				}
				b.Stacks[0].StackDir = filePath
			},
			stack:   stackName,
			wantErr: "is not a directory",
		},
		{
			name: "stack DAC escape rejected",
			setup: func(_ *testing.T, b *config.Bundle) {
				b.Stacks[0].DAC = []config.DACRule{
					{Path: "../escape", Owner: "1001:1001", FileMode: "0600"},
				}
			},
			stack:   stackName,
			wantErr: "DAC failed",
		},
		{
			name: "host_files DAC non-existent path rejected",
			setup: func(_ *testing.T, b *config.Bundle) {
				b.Stacks[0].HostFiles = []config.HostFileConfig{
					{Path: filepath.Join(b.ConfigDir, "missing_host_file"), Owner: "1002:1002", FileMode: "0600", Optional: false},
				}
			},
			stack:   stackName,
			wantErr: "host_files DAC failed",
		},
	}

	for _, tc := range errCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			b := buildTestBundle(t, t.TempDir(), stackName)
			if tc.setup != nil {
				tc.setup(t, &b)
			}
			applyErr := orch.ApplyDAC(ctx, &b, &DACOptions{Stack: tc.stack, DryRun: true})
			if applyErr == nil || !strings.Contains(applyErr.Error(), tc.wantErr) {
				t.Fatalf("expected error containing %q, got %v", tc.wantErr, applyErr)
			}
		})
	}
}

func TestFormatDACDiff(t *testing.T) {
	t.Parallel()

	change := &dac.Change{
		HasOwner: true,
		HasMode:  true,
		UID:      1001,
		GID:      1001,
		Mode:     0o640,
	}
	diff := formatDACDiff(change)
	if !strings.Contains(diff, "owner -> 1001:1001") || !strings.Contains(diff, "mode -> 0640") {
		t.Fatalf("unexpected diff for change without prev: %q", diff)
	}
}
