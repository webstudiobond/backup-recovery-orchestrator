package orchestrator

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/webstudiobond/backup-recovery-orchestrator/internal/config"
)

func TestSyncValidationAndContextErrors(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := t.TempDir()
	pPath, gPath := writeFakeAccounts(t, root, "app-sync-val")
	orch := newTestOrchestrator(&fakeOrchRunner{}, pPath, gPath, nil)
	bundle := buildTestBundle(t, root, "app-sync-val")
	validated := mustValidateBundle(t, &bundle)

	canceledCtx, cancel := context.WithCancel(ctx)
	cancel()
	if err := orch.Sync(canceledCtx, validated, nil); err == nil || !strings.Contains(err.Error(), "context canceled before sync") {
		t.Fatalf("expected canceled context error, got %v", err)
	}

	if err := orch.Sync(ctx, nil, nil); err == nil || !strings.Contains(err.Error(), "bundle must not be nil") {
		t.Fatalf("expected nil bundle error, got %v", err)
	}

	if err := orch.Sync(ctx, validated, &SyncOptions{Stacks: []string{"unknown-stack"}}); err == nil || !strings.Contains(err.Error(), "not found in configuration") {
		t.Fatalf("expected unknown stack error, got %v", err)
	}

	if err := orch.Sync(ctx, validated, &SyncOptions{Destination: "unknown-dest"}); err == nil || !strings.Contains(err.Error(), "destination \"unknown-dest\" not found") {
		t.Fatalf("expected unknown destination error, got %v", err)
	}

	emptyDestsBundle := bundle
	emptyDestsBundle.Global.Destinations = nil
	if err := orch.Sync(ctx, mustValidateBundle(t, &emptyDestsBundle), nil); err == nil || !strings.Contains(err.Error(), "no offsite destinations configured") {
		t.Fatalf("expected empty destination list error, got %v", err)
	}
}

func TestSyncLoopCancellationAndSuccess(t *testing.T) {
	root := t.TempDir()
	stackName := "app-sync-succ"
	pPath, gPath := writeFakeAccounts(t, root, stackName)
	runner := &fakeOrchRunner{}
	orch := newTestOrchestrator(runner, pPath, gPath, nil)
	bundle := buildTestBundle(t, root, stackName)

	localStackDir := filepath.Join(bundle.Global.BackupRoot, stackName)
	if err := os.MkdirAll(localStackDir, 0o750); err != nil {
		t.Fatalf("mkdir localStackDir: %v", err)
	}
	tarFile := filepath.Join(localStackDir, stackName+"_2026-11-16_15-00-00.tar")
	if err := os.WriteFile(tarFile, []byte("tar-content"), 0o600); err != nil {
		t.Fatalf("write tar: %v", err)
	}

	localSelfDir := filepath.Join(bundle.Global.BackupRoot, config.ReservedSelfBackupName)
	if err := os.MkdirAll(localSelfDir, 0o750); err != nil {
		t.Fatalf("mkdir localSelfDir: %v", err)
	}
	selfTar := filepath.Join(localSelfDir, config.ReservedSelfBackupName+"_2026-11-16_15-00-00.tar")
	if err := os.WriteFile(selfTar, []byte("self-tar-content"), 0o600); err != nil {
		t.Fatalf("write self tar: %v", err)
	}

	validated := mustValidateBundle(t, &bundle)
	var buf bytes.Buffer
	if err := orch.Sync(context.Background(), validated, &SyncOptions{Out: &buf}); err != nil {
		t.Fatalf("expected sync success with nil stacks, got %v", err)
	}
	outStr := buf.String()
	if !strings.Contains(outStr, "[sync]") || !strings.Contains(outStr, config.ReservedSelfBackupName) {
		t.Fatalf("expected sync log in output, got:\n%s", outStr)
	}

	if err := orch.Sync(context.Background(), validated, nil); err != nil {
		t.Fatalf("expected sync success with nil opts, got %v", err)
	}

	loopCtx, cancelLoop := context.WithCancel(context.Background())
	twoDestsBundle := bundle
	twoDestsBundle.Global.Destinations = []config.DestinationConfig{
		bundle.Global.Destinations[0],
		{
			Name:        "cloud-second",
			Type:        config.DestTypeFUSE,
			MountPoint:  bundle.Global.Destinations[0].MountPoint,
			BackupDir:   bundle.Global.Destinations[0].BackupDir,
			SystemdUnit: "mnt-second.mount",
		},
	}
	cancelStatFn := func(_ string) (int64, error) {
		cancelLoop()
		return 0x65735546, nil
	}
	cancelOrch := newTestOrchestrator(&fakeOrchRunner{}, pPath, gPath, cancelStatFn)
	if err := cancelOrch.Sync(loopCtx, mustValidateBundle(t, &twoDestsBundle), &SyncOptions{
		Stacks: []string{stackName},
	}); err != nil {
		t.Logf("cancelOrch.Sync result: %v", err)
	}
}

func TestSyncDestinationStacksBranching(t *testing.T) {
	root := t.TempDir()
	stackName := "app-sync-branch"
	pPath, gPath := writeFakeAccounts(t, root, stackName)
	orch := newTestOrchestrator(&fakeOrchRunner{}, pPath, gPath, nil)
	bundle := buildTestBundle(t, root, stackName)

	dest := &bundle.Global.Destinations[0]
	skippedStack := bundle.Stacks[0]
	skippedStack.Destinations = []string{"other-destination"}
	errsSkip := orch.syncDestinationStacks(context.Background(), io.Discard, bundle.Global.BackupRoot, dest, []config.StackConfig{skippedStack}, false, false)
	if len(errsSkip) != 0 {
		t.Fatalf("expected no errors for skipped stack, got %v", errsSkip)
	}

	errsMissingArchive := orch.syncDestinationStacks(context.Background(), io.Discard, bundle.Global.BackupRoot, dest, []config.StackConfig{skippedStack}, true, false)
	if len(errsMissingArchive) == 0 || !strings.Contains(errsMissingArchive[0].Error(), "no local archives found") {
		t.Fatalf("expected no local archives found error, got %v", errsMissingArchive)
	}

	selfMissingDir := filepath.Join(bundle.Global.BackupRoot, config.ReservedSelfBackupName)
	if err := os.MkdirAll(selfMissingDir, 0o750); err != nil {
		t.Fatalf("mkdir selfMissingDir: %v", err)
	}
	errsSelfMissingArchive := orch.syncDestinationStacks(context.Background(), io.Discard, bundle.Global.BackupRoot, dest, nil, false, true)
	if len(errsSelfMissingArchive) == 0 || !strings.Contains(errsSelfMissingArchive[0].Error(), "no local archives found") {
		t.Fatalf("expected self missing archive error, got %v", errsSelfMissingArchive)
	}

	localStackDir := filepath.Join(bundle.Global.BackupRoot, stackName)
	if err := os.MkdirAll(localStackDir, 0o750); err != nil {
		t.Fatalf("mkdir localStackDir: %v", err)
	}
	tarFile := filepath.Join(localStackDir, stackName+"_2026-11-16_15-00-00.tar")
	if err := os.WriteFile(tarFile, []byte("tar"), 0o600); err != nil {
		t.Fatalf("write tar: %v", err)
	}

	badCleanupDest := *dest
	badCleanupDest.RcloneCleanupRemote = "invalid:remote"
	errsCleanup := orch.syncDestinationStacks(context.Background(), io.Discard, bundle.Global.BackupRoot, &badCleanupDest, bundle.Stacks[:1], true, false)
	if len(errsCleanup) == 0 || !strings.Contains(errsCleanup[0].Error(), "cleanup destination") {
		t.Fatalf("expected cleanup destination error, got %v", errsCleanup)
	}
}

func TestSyncArchiveAcrossDestinations(t *testing.T) {
	root := t.TempDir()
	stackName := "app-sync-across"
	pPath, gPath := writeFakeAccounts(t, root, stackName)
	orch := newTestOrchestrator(&fakeOrchRunner{}, pPath, gPath, nil)
	bundle := buildTestBundle(t, root, stackName)

	localStackDir := filepath.Join(bundle.Global.BackupRoot, stackName)
	if err := os.MkdirAll(localStackDir, 0o750); err != nil {
		t.Fatalf("mkdir localStackDir: %v", err)
	}
	validTar := filepath.Join(localStackDir, stackName+"_2026-11-16_15-00-00.tar")
	if err := os.WriteFile(validTar, []byte("tar-payload"), 0o600); err != nil {
		t.Fatalf("write validTar: %v", err)
	}

	dests := []config.DestinationConfig{
		bundle.Global.Destinations[0],
		{
			Name: "cloud-ignored",
			Type: config.DestTypeFUSE,
		},
	}
	stack := bundle.Stacks[0]
	stack.Destinations = []string{bundle.Global.Destinations[0].Name}

	errsSuccess := orch.syncArchiveAcrossDestinations(context.Background(), io.Discard, dests, &stack, validTar)
	if len(errsSuccess) != 0 {
		t.Fatalf("expected success, got %v", errsSuccess)
	}

	missingTar := filepath.Join(localStackDir, "missing.tar")
	errsFailure := orch.syncArchiveAcrossDestinations(context.Background(), io.Discard, dests, &stack, missingTar)
	if len(errsFailure) == 0 || !strings.Contains(errsFailure[0].Error(), "offsite sync to") {
		t.Fatalf("expected offsite sync failure, got %v", errsFailure)
	}
}

func TestSyncSingleArchiveToDest(t *testing.T) {
	root := t.TempDir()
	stackName := "app-sync-single"
	pPath, gPath := writeFakeAccounts(t, root, stackName)
	orch := newTestOrchestrator(&fakeOrchRunner{}, pPath, gPath, nil)
	bundle := buildTestBundle(t, root, stackName)

	localStackDir := filepath.Join(bundle.Global.BackupRoot, stackName)
	if err := os.MkdirAll(localStackDir, 0o750); err != nil {
		t.Fatalf("mkdir localStackDir: %v", err)
	}
	validTar := filepath.Join(localStackDir, stackName+"_2026-11-16_15-00-00.tar")
	if err := os.WriteFile(validTar, []byte("tar-payload"), 0o600); err != nil {
		t.Fatalf("write validTar: %v", err)
	}

	dest := bundle.Global.Destinations[0]
	dest.RcloneCleanupRemote = ""
	var outBuf bytes.Buffer
	if err := orch.syncSingleArchiveToDest(context.Background(), &outBuf, &dest, stackName, validTar, 5); err != nil {
		t.Fatalf("expected syncSingleArchiveToDest success, got %v", err)
	}
	if !strings.Contains(outBuf.String(), "[sync]") {
		t.Fatalf("expected sync log in output, got:\n%s", outBuf.String())
	}

	badCleanupDest := dest
	badCleanupDest.RcloneCleanupRemote = "invalid:remote"
	var cleanupErrBuf bytes.Buffer
	if err := orch.syncSingleArchiveToDest(context.Background(), &cleanupErrBuf, &badCleanupDest, stackName, validTar, 5); err == nil || !strings.Contains(err.Error(), "rclone cleanup on") {
		t.Fatalf("expected rclone cleanup error, got %v", err)
	}
	if !strings.Contains(cleanupErrBuf.String(), "[sync-error]") {
		t.Fatalf("expected sync-error log in output, got:\n%s", cleanupErrBuf.String())
	}

	var syncErrBuf bytes.Buffer
	missingTar := filepath.Join(localStackDir, "nonexistent.tar")
	if err := orch.syncSingleArchiveToDest(context.Background(), &syncErrBuf, &dest, stackName, missingTar, 5); err == nil || !strings.Contains(err.Error(), "offsite sync to") {
		t.Fatalf("expected offsite sync error, got %v", err)
	}
	if !strings.Contains(syncErrBuf.String(), "[sync-error]") {
		t.Fatalf("expected sync-error log in output, got:\n%s", syncErrBuf.String())
	}
}
