package orchestrator

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/webstudiobond/backup-recovery-orchestrator/internal/archive"
	"github.com/webstudiobond/backup-recovery-orchestrator/internal/cmdexec"
	"github.com/webstudiobond/backup-recovery-orchestrator/internal/config"
)

func TestBackupPipelineDryRunAndEndToEnd(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := t.TempDir()
	stackName := "app-alpha"
	bundle := buildTestBundle(t, root, stackName)
	pPath, gPath := writeFakeAccounts(t, root, stackName)

	localDir := filepath.Join(bundle.Global.BackupRoot, stackName)
	if err := os.MkdirAll(localDir, 0o700); err != nil {
		t.Fatalf("mkdir local backup dir: %v", err)
	}
	olderLocal := filepath.Join(localDir, stackName+"_2026-11-15_15-00-00.tar")
	if err := os.WriteFile(olderLocal, []byte("yesterday"), 0o600); err != nil {
		t.Fatalf("write older archive: %v", err)
	}

	runner := &fakeOrchRunner{
		snapMount: bundle.Global.LVM.MountPoint,
		stackName: stackName,
		psOutput:  []byte(`{"Service":"pg","State":"running","Health":"healthy"}`),
	}
	orch := newTestOrchestrator(runner, pPath, gPath, nil)

	var dryBuf bytes.Buffer
	if err := orch.Backup(ctx, &bundle, &BackupOptions{DryRun: true, Out: &dryBuf}); err != nil {
		t.Fatalf("dry-run backup failed: %v", err)
	}
	if !strings.Contains(dryBuf.String(), "cloud-primary(fuse:keep=5)") {
		t.Fatalf("expected dry-run destination summary, got %q", dryBuf.String())
	}

	var runBuf bytes.Buffer
	if err := orch.Backup(ctx, &bundle, &BackupOptions{Out: &runBuf}); err != nil {
		t.Fatalf("Backup failed: %v", err)
	}

	remoteArchive := filepath.Join(bundle.Global.Destinations[0].BackupDir, stackName, stackName+"_2026-11-16_15-00-00.tar")
	if _, statErr := os.Stat(remoteArchive); statErr != nil {
		t.Fatalf("expected remote archive at %q: %v", remoteArchive, statErr)
	}

	localArchive := filepath.Join(localDir, stackName+"_2026-11-16_15-00-00.tar")
	if _, statErr := os.Stat(localArchive); statErr != nil {
		t.Fatalf("expected newest local archive retained (local_count=1): %v", statErr)
	}
	if _, statErr := os.Stat(olderLocal); !os.IsNotExist(statErr) {
		t.Fatalf("expected older local archive pruned beyond local_count=1")
	}

	assertBashContainerLayout(ctx, t, localArchive, stackName)

	var restoreDryBuf bytes.Buffer
	if err := orch.Restore(ctx, &bundle.Global, &RestoreOptions{
		ArchivePath:   remoteArchive,
		AngieMode:     RestoreModeAll,
		HostFilesMode: RestoreModeAll,
		DryRun:        true,
		Out:           &restoreDryBuf,
	}); err != nil {
		t.Fatalf("Restore dry-run failed: %v", err)
	}
	if !strings.Contains(restoreDryBuf.String(), "alpha.example.com.conf") {
		t.Fatalf("expected angie domain in restore dry-run output, got %q", restoreDryBuf.String())
	}
	if !strings.Contains(restoreDryBuf.String(), "turn.example.com.inc") {
		t.Fatalf("expected angie stream in restore dry-run output, got %q", restoreDryBuf.String())
	}

	var restoreBuf bytes.Buffer
	if err := orch.Restore(ctx, &bundle.Global, &RestoreOptions{
		ArchivePath: remoteArchive,
		AngieMode:   RestoreModeAll,
		Out:         &restoreBuf,
	}); err != nil {
		t.Fatalf("Restore failed: %v", err)
	}
}

func TestBackupSkipsFailingDestinationAndContinues(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := t.TempDir()
	stackName := "app-gamma"
	bundle := buildTestBundle(t, root, stackName)
	pPath, gPath := writeFakeAccounts(t, root, stackName)

	brokenMount := filepath.Join(root, "mnt", "broken")
	healthyDir := filepath.Join(root, "usb-backups")
	if err := os.MkdirAll(brokenMount, 0o750); err != nil {
		t.Fatalf("mkdir broken mount: %v", err)
	}

	bundle.Global.Destinations = []config.DestinationConfig{
		{
			Name:           "broken-cloud",
			Type:           "fuse",
			MountPoint:     brokenMount,
			BackupDir:      filepath.Join(brokenMount, "backups"),
			WaitTimeoutSec: 1,
		},
		{
			Name:      "usb-drive",
			Type:      "dir",
			BackupDir: healthyDir,
		},
	}

	runner := &fakeOrchRunner{
		snapMount: bundle.Global.LVM.MountPoint,
		stackName: stackName,
	}
	orch := newTestOrchestrator(runner, pPath, gPath, func(_ string) (int64, error) {
		return 0xef53, nil
	})
	orch.Storage.Verifier.PollInterval = time.Millisecond

	bundle.Global.Destinations[0].Type = "mount"
	runner.failMatch = "mountpoint -q " + brokenMount

	var outBuf bytes.Buffer
	err := orch.Backup(ctx, &bundle, &BackupOptions{Out: &outBuf})
	if err == nil || !strings.Contains(err.Error(), "broken-cloud") {
		t.Fatalf("expected error mentioning broken-cloud, got %v", err)
	}

	usbArchive := filepath.Join(healthyDir, stackName, stackName+"_2026-11-16_15-00-00.tar")
	if _, statErr := os.Stat(usbArchive); statErr != nil {
		t.Fatalf("expected backup to continue to healthy usb-drive destination: %v", statErr)
	}
}

func TestBackupTimezoneModes(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	eet := time.FixedZone("EET", 2*3600)
	fixedNow := time.Date(2026, 11, 16, 17, 0, 0, 0, eet)

	cases := []struct {
		name          string
		timezone      string
		wantStamp     string
		wantCreatedAt string
	}{
		{
			name:          "utc",
			timezone:      config.TimezoneUTC,
			wantStamp:     "2026-11-16_15-00-00_UTC",
			wantCreatedAt: "2026-11-16T15:00:00Z",
		},
		{
			name:          "local",
			timezone:      config.TimezoneLocal,
			wantStamp:     "2026-11-16_17-00-00",
			wantCreatedAt: "2026-11-16T17:00:00+02:00",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			root := t.TempDir()
			stackName := "app-tz-" + tc.name
			bundle := buildTestBundle(t, root, stackName)
			bundle.Global.Timezone = tc.timezone
			pPath, gPath := writeFakeAccounts(t, root, stackName)

			runner := &fakeOrchRunner{
				snapMount: bundle.Global.LVM.MountPoint,
				stackName: stackName,
			}
			orch := newTestOrchestrator(runner, pPath, gPath, nil)
			orch.Now = func() time.Time { return fixedNow }

			if err := orch.Backup(ctx, &bundle, &BackupOptions{Out: io.Discard}); err != nil {
				t.Fatalf("Backup failed: %v", err)
			}

			archivePath := filepath.Join(bundle.Global.BackupRoot, stackName, stackName+"_"+tc.wantStamp+".tar")
			verified, err := archive.VerifyContainer(ctx, archivePath)
			if err != nil {
				t.Fatalf("VerifyContainer(%q) failed: %v", archivePath, err)
			}

			wantFiles := stackName + "_files_" + tc.wantStamp + ".tar.zst"
			if verified.FilesArchive != wantFiles {
				t.Fatalf("expected files_archive %q, got %q", wantFiles, verified.FilesArchive)
			}
			wantHost := stackName + "_host_files_" + tc.wantStamp + ".tar.zst"
			if verified.HostFilesArchive != wantHost {
				t.Fatalf("expected host_files_archive %q, got %q", wantHost, verified.HostFilesArchive)
			}
			wantDB := stackName + "_db_" + tc.wantStamp + ".sql.zst"
			if len(verified.Databases) != 1 || verified.Databases[0].ArtifactPath != wantDB {
				t.Fatalf("expected single db artifact %q, got %+v", wantDB, verified.Databases)
			}
			if verified.CreatedAt != tc.wantCreatedAt {
				t.Fatalf("expected created_at %q, got %q", tc.wantCreatedAt, verified.CreatedAt)
			}
		})
	}
}

func TestBackupOptionsAndValidation(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := t.TempDir()
	stackName := "app-opt"
	bundle := buildTestBundle(t, root, stackName)
	pPath, gPath := writeFakeAccounts(t, root, stackName)
	runner := &fakeOrchRunner{
		snapMount: bundle.Global.LVM.MountPoint,
		stackName: stackName,
	}
	orch := newTestOrchestrator(runner, pPath, gPath, nil)

	canceledCtx, cancel := context.WithCancel(ctx)
	cancel()
	if err := orch.Backup(canceledCtx, &bundle, nil); err == nil || !strings.Contains(err.Error(), "context canceled before backup") {
		t.Fatalf("expected context canceled error, got %v", err)
	}

	if err := orch.Backup(ctx, nil, nil); err == nil {
		t.Fatal("expected error on nil bundle")
	}

	if err := orch.Backup(ctx, &bundle, &BackupOptions{DryRun: true, Stacks: []string{stackName}, Out: io.Discard}); err != nil {
		t.Fatalf("dry-run with explicit stack failed: %v", err)
	}

	origSystemd := orch.SystemdDir
	orch.SystemdDir = filepath.Join(root, "file_not_dir")
	if err := os.WriteFile(orch.SystemdDir, []byte("x"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}
	if err := orch.Backup(ctx, &bundle, &BackupOptions{DryRun: true, Out: io.Discard}); err == nil {
		t.Fatal("expected error in dry run self backup")
	}
	orch.SystemdDir = origSystemd

	callCount := 0
	breakCtx, breakCancel := context.WithCancel(ctx)
	orch.LVM.AvailBytesFn = func(_ string) (uint64, error) {
		callCount++
		breakCancel()
		return 0, errors.New("check space error")
	}
	if err := orch.Backup(breakCtx, &bundle, &BackupOptions{Out: io.Discard}); err == nil {
		t.Fatal("expected error with canceled context")
	}
	if callCount != 1 {
		t.Fatalf("expected loop to break on canceled context, calls=%d", callCount)
	}
}

func TestBackupSingleStackErrors(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := t.TempDir()
	stackName := "app-err"
	bundle := buildTestBundle(t, root, stackName)
	pPath, gPath := writeFakeAccounts(t, root, stackName)

	runner := &fakeOrchRunner{
		snapMount: bundle.Global.LVM.MountPoint,
		stackName: stackName,
	}
	orch := newTestOrchestrator(runner, pPath, gPath, nil)

	orch.LVM.AvailBytesFn = func(_ string) (uint64, error) {
		return 0, errors.New("disk check fail")
	}
	if err := orch.backupSingleStack(ctx, io.Discard, &bundle.Global, &bundle.Stacks[0], true); err == nil || !strings.Contains(err.Error(), "check free space") {
		t.Fatalf("expected check free space error, got %v", err)
	}
	orch.LVM.AvailBytesFn = func(_ string) (uint64, error) { return 100 << 30, nil }

	origNow := orch.Now
	orch.Now = nil
	if err := orch.backupSingleStack(ctx, io.Discard, &bundle.Global, &bundle.Stacks[0], true); err != nil {
		t.Fatalf("backupSingleStack with nil Now failed: %v", err)
	}
	orch.Now = origNow

	origBackupRoot := bundle.Global.BackupRoot
	fileAsDir := filepath.Join(root, "file_as_dir")
	if err := os.WriteFile(fileAsDir, []byte("x"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}
	bundle.Global.BackupRoot = fileAsDir
	if err := orch.backupSingleStack(ctx, io.Discard, &bundle.Global, &bundle.Stacks[0], true); err == nil || !strings.Contains(err.Error(), "create local backup directory") {
		t.Fatalf("expected create local backup directory error, got %v", err)
	}
	bundle.Global.BackupRoot = origBackupRoot

	bundle.Stacks[0].Hooks.PreDump = []config.HookStep{{Command: []string{"fail-stage"}}}
	runner.failMatch = "fail-stage"
	if err := orch.backupSingleStack(ctx, io.Discard, &bundle.Global, &bundle.Stacks[0], true); err == nil || !strings.Contains(err.Error(), "run pre_dump hooks") {
		t.Fatalf("expected stage error, got %v", err)
	}
	runner.failMatch = ""
	bundle.Stacks[0].Hooks.PreDump = nil

	origTZ := bundle.Global.Timezone
	bundle.Global.Timezone = config.TimezoneUTC
	fixedTime := time.Date(2026, 11, 16, 15, 0, 0, 0, time.UTC)
	orch.Now = func() time.Time { return fixedTime }
	_, stamp := resolveBackupTime(fixedTime, bundle.Global.Timezone)
	conflictTar := filepath.Join(bundle.Global.BackupRoot, stackName, fmt.Sprintf("%s_%s.tar", stackName, stamp))
	if err := os.MkdirAll(conflictTar, 0o700); err != nil {
		t.Fatalf("mkdir conflict dir: %v", err)
	}
	if err := orch.backupSingleStack(ctx, io.Discard, &bundle.Global, &bundle.Stacks[0], true); err == nil {
		t.Fatal("expected AssembleContainer error")
	}
	if err := os.RemoveAll(conflictTar); err != nil {
		t.Fatalf("remove conflict dir: %v", err)
	}
	orch.Now = origNow
	bundle.Global.Timezone = origTZ

	bundle.Stacks[0].Retention.LocalCount = 0
	if err := orch.backupSingleStack(ctx, io.Discard, &bundle.Global, &bundle.Stacks[0], true); err == nil || !strings.Contains(err.Error(), "prune local backups") {
		t.Fatalf("expected prune local backups error, got %v", err)
	}
}

func TestBackupSingleStackMkdirTempError(t *testing.T) {
	origRootMkdir := rootMkdir
	rootMkdir = mockFailingRootMkdir
	t.Cleanup(func() {
		rootMkdir = origRootMkdir
	})

	root := t.TempDir()
	stackName := "app-stage-err"
	bundle := buildTestBundle(t, root, stackName)
	pPath, gPath := writeFakeAccounts(t, root, stackName)
	runner := &fakeOrchRunner{snapMount: bundle.Global.LVM.MountPoint, stackName: stackName}
	orch := newTestOrchestrator(runner, pPath, gPath, nil)

	err := orch.backupSingleStack(context.Background(), io.Discard, &bundle.Global, &bundle.Stacks[0], true)
	if err == nil || !strings.Contains(err.Error(), "create staging directory") {
		t.Fatalf("expected create staging directory error, got %v", err)
	}
}

func TestStageStackBackupStepsAndErrors(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := t.TempDir()
	stackName := "app-stage"
	bundle := buildTestBundle(t, root, stackName)
	pPath, gPath := writeFakeAccounts(t, root, stackName)
	runner := &fakeOrchRunner{
		snapMount: bundle.Global.LVM.MountPoint,
		stackName: stackName,
	}
	orch := newTestOrchestrator(runner, pPath, gPath, nil)
	stack := &bundle.Stacks[0]
	stagingDir := filepath.Join(root, "staging")
	if err := os.MkdirAll(stagingDir, 0o750); err != nil {
		t.Fatalf("mkdir staging: %v", err)
	}
	now := time.Now()
	stamp := "2026-11-16_15-00-00"

	cases := []struct {
		setup   func()
		wantErr string
	}{
		{wantErr: "pre_dump hooks", setup: func() {
			stack.Hooks.PreDump = []config.HookStep{{Command: []string{"fail"}}}
			runner.failMatch = "fail"
		}},
		{wantErr: "dump database", setup: func() { stack.Hooks.PreDump = nil; runner.failMatch = "pg_dump" }},
		{wantErr: "pre_snapshot hooks", setup: func() {
			runner.failMatch = "fail-snap"
			stack.Hooks.PreSnapshot = []config.HookStep{{Command: []string{"fail-snap"}}}
		}},
		{wantErr: "create snapshot", setup: func() { stack.Hooks.PreSnapshot = nil; runner.failMatch = "--snapshot" }},
		{wantErr: "archive host_files", setup: func() { runner.failMatch = "tar -cpf -" }},
		{wantErr: "stage angie assets", setup: func() {
			runner.failMatch = ""
			bundle.Global.Angie.DomainsDir = filepath.Join(root, "missing", "domains")
		}},
	}
	for _, tc := range cases {
		tc.setup()
		if _, err := orch.stageStackBackup(ctx, &bundle.Global, stack, stagingDir, now, stamp); err == nil || !strings.Contains(err.Error(), tc.wantErr) {
			t.Fatalf("expected error containing %q, got %v", tc.wantErr, err)
		}
	}

	bundle.Global.Angie.DomainsDir = filepath.Join(root, "home", "angie", "data", "conf.d", "domains")
	stack.HostFiles = []config.HostFileConfig{{Path: filepath.Join(root, "missing_host_file"), Owner: "app:app", FileMode: "0600", Optional: true}}
	manifest, err := orch.stageStackBackup(ctx, &bundle.Global, stack, stagingDir, now, stamp)
	if err != nil {
		t.Fatalf("stageStackBackup with missing host file failed: %v", err)
	}
	if manifest.HostFilesArchive != "" {
		t.Fatalf("expected empty HostFilesArchive when no host files present, got %q", manifest.HostFilesArchive)
	}
}

func TestDumpAllDatabasesAndSnapshotDetails(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := t.TempDir()
	stackName := "app-details"
	bundle := buildTestBundle(t, root, stackName)
	pPath, gPath := writeFakeAccounts(t, root, stackName)
	runner := &fakeOrchRunner{
		snapMount: bundle.Global.LVM.MountPoint,
		stackName: stackName,
	}
	orch := newTestOrchestrator(runner, pPath, gPath, nil)
	stack := &bundle.Stacks[0]

	stack.Databases = nil
	dbs, err := orch.dumpAllDatabases(ctx, stack, root, "stamp")
	if err != nil || dbs != nil {
		t.Fatalf("expected nil dbs on empty databases: %v, %v", dbs, err)
	}

	filesOut := filepath.Join(root, "files.tar.zst")
	cases := []struct {
		setup   func()
		wantErr string
	}{
		{wantErr: "cleanup snapshot", setup: func() {
			created := false
			runner.onRun = func(spec *cmdexec.Spec) error {
				if spec.Name == "lvcreate" {
					created = true
				}
				if created && spec.Name == "lvremove" {
					return errors.New("simulated cleanup fail")
				}
				return nil
			}
		}},
		{wantErr: "post_snapshot hooks", setup: func() {
			runner.onRun = nil
			runner.failMatch = "fail-post"
			stack.Hooks.PostSnapshot = []config.HookStep{{Command: []string{"fail-post"}}}
		}},
		{wantErr: "archive snapshot files", setup: func() {
			runner.onRun = nil
			stack.Hooks.PostSnapshot = nil
			runner.failMatch = "--wildcards"
		}},
	}
	for _, tc := range cases {
		tc.setup()
		if _, err := orch.snapshotAndArchiveFiles(ctx, stack, filesOut); err == nil || !strings.Contains(err.Error(), tc.wantErr) {
			t.Fatalf("expected error containing %q, got %v", tc.wantErr, err)
		}
	}

	allDests := []config.DestinationConfig{
		{Name: "dest-included", Type: "local-store"},
		{Name: "dest-skipped", Type: "remote-store"},
	}
	stack.Destinations = []string{"dest-included"}
	summary := formatStackDestinations(allDests, stack)
	if !strings.Contains(summary, "dest-included") || strings.Contains(summary, "dest-skipped") {
		t.Fatalf("expected formatStackDestinations to skip untargeted destinations, got %q", summary)
	}
}
