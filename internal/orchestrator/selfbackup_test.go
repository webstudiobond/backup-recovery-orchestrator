package orchestrator

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/webstudiobond/backup-recovery-orchestrator/internal/archive"
	"github.com/webstudiobond/backup-recovery-orchestrator/internal/cmdexec"
	"github.com/webstudiobond/backup-recovery-orchestrator/internal/config"
)

func TestSelfBackupAndRestoreRejection(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := t.TempDir()
	stackName := "app-theta"
	bundle := buildTestBundle(t, root, stackName)
	pPath, gPath := writeFakeAccounts(t, root, stackName)
	runner := &fakeOrchRunner{snapMount: bundle.Global.LVM.MountPoint, stackName: stackName}
	orch := newTestOrchestrator(runner, pPath, gPath, nil)

	if err := orch.Backup(ctx, &bundle, &BackupOptions{
		Stacks: []string{stackName},
		NoSync: true,
		Out:    io.Discard,
	}); err != nil {
		t.Fatalf("targeted stack Backup failed: %v", err)
	}
	selfDir := filepath.Join(bundle.Global.BackupRoot, config.ReservedSelfBackupName)
	if _, statErr := os.Stat(selfDir); !os.IsNotExist(statErr) {
		t.Fatalf("expected targeted stack backup not to create %q, got err=%v", selfDir, statErr)
	}

	var dryBuf bytes.Buffer
	if err := orch.SelfBackup(ctx, &bundle, &SelfBackupOptions{
		Out:    &dryBuf,
		DryRun: true,
	}); err != nil {
		t.Fatalf("SelfBackup dry-run failed: %v", err)
	}
	dryOut := dryBuf.String()
	assertContainsAll(t, dryOut, []string{config.ReservedSelfBackupName, archive.SelfBackupKind, bundle.ConfigDir, "bro.service", "bro.timer", "mnt-cloud.mount", "mnt-cloud.automount"})
	for _, excluded := range []string{"unrelated.service", "bro-link.service"} {
		if strings.Contains(dryOut, excluded) {
			t.Fatalf("expected %q excluded from selfbackup, got:\n%s", excluded, dryOut)
		}
	}

	if err := orch.SelfBackup(ctx, &bundle, &SelfBackupOptions{Out: io.Discard}); err != nil {
		t.Fatalf("SelfBackup failed: %v", err)
	}

	localSelfTar := filepath.Join(selfDir, config.ReservedSelfBackupName+"_2026-11-16_15-00-00.tar")
	verified, verifyErr := archive.VerifyContainer(ctx, localSelfTar)
	if verifyErr != nil {
		t.Fatalf("VerifyContainer on selfbackup failed: %v", verifyErr)
	}
	if verified.Kind != archive.SelfBackupKind || verified.StackName != config.ReservedSelfBackupName {
		t.Fatalf("unexpected selfbackup manifest metadata: kind=%q stack=%q", verified.Kind, verified.StackName)
	}

	remoteSelfTar := filepath.Join(bundle.Global.Destinations[0].BackupDir, config.ReservedSelfBackupName, filepath.Base(localSelfTar))
	if _, statErr := os.Stat(remoteSelfTar); statErr != nil {
		t.Fatalf("expected synced selfbackup at %q: %v", remoteSelfTar, statErr)
	}

	restoreErr := orch.Restore(ctx, &bundle.Global, &RestoreOptions{
		ArchivePath: localSelfTar,
		DryRun:      true,
		Out:         io.Discard,
	})
	if restoreErr == nil || !strings.Contains(restoreErr.Error(), "cannot be restored") {
		t.Fatalf("expected selfbackup restore rejection, got %v", restoreErr)
	}
}

func TestDestinationUnitCandidates(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		unit     string
		expected []string
	}{
		{
			name:     "mount unit",
			unit:     "mnt-alpha.mount",
			expected: []string{"mnt-alpha.mount", "mnt-alpha.automount"},
		},
		{
			name:     "automount unit",
			unit:     "mnt-beta.automount",
			expected: []string{"mnt-beta.automount", "mnt-beta.mount"},
		},
		{
			name:     "service unit",
			unit:     "/etc/systemd/system/bro-custom.service",
			expected: []string{"bro-custom.service"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := destinationUnitCandidates(tc.unit)
			if !slices.Equal(got, tc.expected) {
				t.Fatalf("destinationUnitCandidates(%q) = %v, want %v", tc.unit, got, tc.expected)
			}
		})
	}
}

func TestCollectSelfBackupItems(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	pPath, gPath := writeFakeAccounts(t, root, "app-self-items")
	orch := newTestOrchestrator(&fakeOrchRunner{}, pPath, gPath, nil)
	bundle := buildTestBundle(t, root, "app-self-items")

	orch.SystemdDir = filepath.Join(root, "file_as_systemd")
	if err := os.WriteFile(orch.SystemdDir, []byte("x"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}
	if _, err := orch.collectSelfBackupItems(&bundle); err == nil || !strings.Contains(err.Error(), "read systemd unit directory") {
		t.Fatalf("expected read systemd unit directory error, got %v", err)
	}

	sysDir := filepath.Join(root, "systemd")
	if err := os.MkdirAll(sysDir, 0o700); err != nil {
		t.Fatalf("mkdir systemd: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sysDir, "bro-one.service"), []byte("unit"), 0o600); err != nil {
		t.Fatalf("write bro-one: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sysDir, "other.service"), []byte("unit"), 0o600); err != nil {
		t.Fatalf("write other: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(sysDir, "bro-sub.d"), 0o700); err != nil {
		t.Fatalf("mkdir bro-sub.d: %v", err)
	}

	orch.SystemdDir = sysDir
	bundle.ConfigDir = ""
	bundle.Global.Destinations = []config.DestinationConfig{
		{SystemdUnit: ""},
		{SystemdUnit: "mnt-target.automount"},
		{SystemdUnit: "mnt-target.mount"},
	}

	items, err := orch.collectSelfBackupItems(&bundle)
	if err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	if len(items) == 0 {
		t.Fatal("expected non-empty items")
	}

	orch.SystemdDir = ""
	emptySysItems, errEmpty := orch.collectSelfBackupItems(&bundle)
	if errEmpty != nil {
		t.Fatalf("expected success for empty systemdDir, got %v", errEmpty)
	}
	if len(emptySysItems) == 0 {
		t.Fatal("expected non-empty items for empty systemdDir")
	}
}

func TestBuildSelfBackupContainerErrors(t *testing.T) {
	root := t.TempDir()
	pPath, gPath := writeFakeAccounts(t, root, "app-self-build")
	orch := newTestOrchestrator(&fakeOrchRunner{}, pPath, gPath, nil)
	global := config.GlobalConfig{
		BackupRoot: root,
		ZstdLevel:  3,
	}
	now := time.Now()
	stamp := "2026-11-16_15-00-00"

	fileAsRoot := filepath.Join(root, "file_root")
	if err := os.WriteFile(fileAsRoot, []byte("x"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}
	badGlobal := global
	badGlobal.BackupRoot = fileAsRoot
	if _, _, err := orch.buildSelfBackupContainer(context.Background(), &badGlobal, nil, now, stamp); err == nil || !strings.Contains(err.Error(), "create local selfbackup directory") {
		t.Fatalf("expected create local directory error, got %v", err)
	}

	badTmp := filepath.Join(root, "bad_tmp")
	if err := os.WriteFile(badTmp, []byte("x"), 0o600); err != nil {
		t.Fatalf("write bad_tmp: %v", err)
	}
	t.Setenv("TMPDIR", badTmp)
	if _, _, err := orch.buildSelfBackupContainer(context.Background(), &global, nil, now, stamp); err == nil || !strings.Contains(err.Error(), "create selfbackup staging directory") {
		t.Fatalf("expected create staging directory error, got %v", err)
	}
	t.Setenv("TMPDIR", "")

	badItems := []config.HostFileConfig{
		{Path: filepath.Join(root, "nonexistent_file"), Optional: false},
	}
	if _, _, err := orch.buildSelfBackupContainer(context.Background(), &global, badItems, now, stamp); err == nil || !strings.Contains(err.Error(), "archive selfbackup paths") {
		t.Fatalf("expected archive selfbackup paths error, got %v", err)
	}

	selfDir := filepath.Join(global.BackupRoot, config.ReservedSelfBackupName)
	if err := os.MkdirAll(selfDir, 0o700); err != nil {
		t.Fatalf("mkdir selfDir: %v", err)
	}
	conflictArchive := filepath.Join(selfDir, config.ReservedSelfBackupName+"_"+stamp+".tar")
	if err := os.MkdirAll(conflictArchive, 0o700); err != nil {
		t.Fatalf("mkdir conflictArchive: %v", err)
	}
	if _, _, err := orch.buildSelfBackupContainer(context.Background(), &global, nil, now, stamp); err == nil {
		t.Fatal("expected AssembleContainer error")
	}
}

func TestSelfBackupValidation(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := t.TempDir()
	pPath, gPath := writeFakeAccounts(t, root, "app-self-val")
	orch := newTestOrchestrator(&fakeOrchRunner{}, pPath, gPath, nil)
	bundle := buildTestBundle(t, root, "app-self-val")

	canceledCtx, cancel := context.WithCancel(ctx)
	cancel()
	if err := orch.SelfBackup(canceledCtx, &bundle, nil); err == nil || !strings.Contains(err.Error(), "context canceled before selfbackup") {
		t.Fatalf("expected canceled context error, got %v", err)
	}

	if err := orch.SelfBackup(ctx, nil, nil); err == nil || !strings.Contains(err.Error(), "configuration bundle must not be nil") {
		t.Fatalf("expected nil bundle error, got %v", err)
	}

	orch.SystemdDir = filepath.Join(root, "file_sys")
	if err := os.WriteFile(orch.SystemdDir, []byte("x"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}
	if err := orch.SelfBackup(ctx, &bundle, nil); err == nil || !strings.Contains(err.Error(), "read systemd unit directory") {
		t.Fatalf("expected collect items error, got %v", err)
	}
	orch.SystemdDir = ""

	orch.LVM.AvailBytesFn = func(_ string) (uint64, error) {
		return 0, errors.New("simulated space check failure")
	}
	if err := orch.SelfBackup(ctx, &bundle, nil); err == nil || !strings.Contains(err.Error(), "check free space") {
		t.Fatalf("expected check free space error, got %v", err)
	}
	orch.LVM.AvailBytesFn = nil

	fileAsRoot := filepath.Join(root, "file_as_backup_root")
	if err := os.WriteFile(fileAsRoot, []byte("x"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}
	badBundle := bundle
	badBundle.Global.BackupRoot = fileAsRoot
	if err := orch.SelfBackup(ctx, &badBundle, nil); err == nil || !strings.Contains(err.Error(), "create local selfbackup directory") {
		t.Fatalf("expected build container error, got %v", err)
	}
}

func TestSelfBackupExecutionAndPrune(t *testing.T) {
	root := t.TempDir()
	pPath, gPath := writeFakeAccounts(t, root, "app-self-exec")
	runner := &fakeOrchRunner{}
	orch := newTestOrchestrator(runner, pPath, gPath, nil)
	bundle := buildTestBundle(t, root, "app-self-exec")

	selfDir := filepath.Join(bundle.Global.BackupRoot, config.ReservedSelfBackupName)
	if err := os.MkdirAll(selfDir, 0o700); err != nil {
		t.Fatalf("mkdir selfDir: %v", err)
	}
	for i := 1; i <= 4; i++ {
		dummy := filepath.Join(selfDir, fmt.Sprintf("%s_2026-11-1%d_10-00-00.tar", config.ReservedSelfBackupName, i))
		if err := os.WriteFile(dummy, []byte("tar"), 0o600); err != nil {
			t.Fatalf("write dummy archive: %v", err)
		}
	}

	orch.Now = nil
	var outBuf bytes.Buffer
	if err := orch.SelfBackup(context.Background(), &bundle, &SelfBackupOptions{
		Out:    &outBuf,
		NoSync: true,
	}); err != nil {
		t.Fatalf("expected SelfBackup success with pruning, got %v", err)
	}
	if !strings.Contains(outBuf.String(), "[prune]") {
		t.Fatalf("expected prune log in output, got:\n%s", outBuf.String())
	}

	failBundle := bundle
	failBundle.Global.Destinations = []config.DestinationConfig{
		{
			Name:        "cloud-fail",
			Type:        config.DestTypeFUSE,
			MountPoint:  bundle.Global.Destinations[0].MountPoint,
			BackupDir:   filepath.Join(root, "file_as_backup_dir"),
			SystemdUnit: "mnt-fail.mount",
		},
	}
	if err := os.WriteFile(failBundle.Global.Destinations[0].BackupDir, []byte("x"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}
	if err := orch.SelfBackup(context.Background(), &failBundle, &SelfBackupOptions{
		Out: io.Discard,
	}); err == nil || !strings.Contains(err.Error(), "create remote backup directory") {
		t.Fatalf("expected destination sync error, got %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancelRunner := &fakeOrchRunner{
		onRun: func(spec *cmdexec.Spec) error {
			if spec.Name == "systemctl" {
				cancel()
			}
			return nil
		},
	}
	cancelStatFn := func(_ string) (int64, error) { return 0, errors.New("simulated stat fail") }
	cancelOrch := newTestOrchestrator(cancelRunner, pPath, gPath, cancelStatFn)
	if err := cancelOrch.SelfBackup(ctx, &bundle, &SelfBackupOptions{
		Out: io.Discard,
	}); err == nil || !strings.Contains(err.Error(), "prune local selfbackups") {
		t.Fatalf("expected prune local selfbackups error, got %v", err)
	}
}
