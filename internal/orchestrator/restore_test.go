package orchestrator

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/webstudiobond/backup-recovery-orchestrator/internal/archive"
	"github.com/webstudiobond/backup-recovery-orchestrator/internal/config"
	"github.com/webstudiobond/backup-recovery-orchestrator/internal/dac"
)

func TestRestoreValidationAndErrors(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := t.TempDir()
	stackName := "app-restore-val"
	bundle := buildTestBundle(t, root, stackName)
	pPath, gPath := writeFakeAccounts(t, root, stackName)
	runner := &fakeOrchRunner{snapMount: bundle.Global.LVM.MountPoint, stackName: stackName}
	orch := newTestOrchestrator(runner, pPath, gPath, nil)

	if err := orch.Backup(ctx, &bundle, &BackupOptions{NoSync: true, Out: io.Discard}); err != nil {
		t.Fatalf("Backup failed: %v", err)
	}
	archivePath := filepath.Join(bundle.Global.BackupRoot, stackName, stackName+"_2026-11-16_15-00-00.tar")

	canceledCtx, cancel := context.WithCancel(ctx)
	cancel()

	cases := []struct {
		ctx     context.Context
		global  *config.GlobalConfig
		opts    *RestoreOptions
		name    string
		wantErr string
	}{
		{
			ctx:     canceledCtx,
			global:  &bundle.Global,
			opts:    &RestoreOptions{ArchivePath: archivePath},
			name:    "canceled context",
			wantErr: "context canceled before restore",
		},
		{
			ctx:     ctx,
			global:  nil,
			opts:    &RestoreOptions{ArchivePath: archivePath},
			name:    "nil global config",
			wantErr: "global config must not be nil",
		},
		{
			ctx:     ctx,
			global:  &bundle.Global,
			opts:    nil,
			name:    "nil options",
			wantErr: "archive path is required for restore",
		},
		{
			ctx:     ctx,
			global:  &bundle.Global,
			opts:    &RestoreOptions{ArchivePath: "   "},
			name:    "empty archive path",
			wantErr: "archive path is required for restore",
		},
		{
			ctx:     ctx,
			global:  &bundle.Global,
			opts:    &RestoreOptions{ArchivePath: filepath.Join(root, "missing.tar")},
			name:    "archive verification error",
			wantErr: "verify archive",
		},
		{
			ctx:     ctx,
			global:  &bundle.Global,
			opts:    &RestoreOptions{ArchivePath: archivePath, AngieMode: "invalid-angie-mode"},
			name:    "angie selection error",
			wantErr: "unsupported angie restore mode",
		},
		{
			ctx:     ctx,
			global:  &bundle.Global,
			opts:    &RestoreOptions{ArchivePath: archivePath, HostFilesMode: "invalid-host-mode"},
			name:    "host files selection error",
			wantErr: "unsupported host-files mode",
		},
		{
			ctx:     ctx,
			global:  &bundle.Global,
			opts:    &RestoreOptions{ArchivePath: archivePath, OnlySteps: []string{"unknown_step"}},
			name:    "no steps remaining after filtering",
			wantErr: "no restore steps selected after filtering",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := orch.Restore(tc.ctx, tc.global, tc.opts)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("expected error containing %q, got %v", tc.wantErr, err)
			}
		})
	}
}

func TestRestoreUserAndMkdirTempErrors(t *testing.T) {
	root := t.TempDir()
	stackName := "app-restore-env"
	bundle := buildTestBundle(t, root, stackName)
	pPath, gPath := writeFakeAccounts(t, root, stackName)
	runner := &fakeOrchRunner{snapMount: bundle.Global.LVM.MountPoint, stackName: stackName}
	orch := newTestOrchestrator(runner, pPath, gPath, nil)

	if err := orch.Backup(context.Background(), &bundle, &BackupOptions{NoSync: true, Out: io.Discard}); err != nil {
		t.Fatalf("Backup failed: %v", err)
	}
	archivePath := filepath.Join(bundle.Global.BackupRoot, stackName, stackName+"_2026-11-16_15-00-00.tar")

	origRootMkdir := rootMkdir
	rootMkdir = mockFailingRootMkdir
	err := orch.Restore(context.Background(), &bundle.Global, &RestoreOptions{
		ArchivePath: archivePath,
	})
	rootMkdir = origRootMkdir
	if err == nil || !strings.Contains(err.Error(), "create restore unpack directory") {
		t.Fatalf("expected create restore unpack directory error, got %v", err)
	}

	failOrch := newTestOrchestrator(&fakeOrchRunner{failMatch: "useradd"}, pPath, gPath, nil)
	failOrch.SysUser.PasswdPath = filepath.Join(root, "nonexistent-passwd")
	errUser := failOrch.Restore(context.Background(), &bundle.Global, &RestoreOptions{
		ArchivePath: archivePath,
		DryRun:      true,
	})
	if errUser == nil || !strings.Contains(errUser.Error(), "system user validation failed") {
		t.Fatalf("expected system user validation failed error, got %v", errUser)
	}
}

func TestRestoreDryRunAndSuccess(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := t.TempDir()
	stackName := "app-restore-run"
	bundle := buildTestBundle(t, root, stackName)
	pPath, gPath := writeFakeAccounts(t, root, stackName)
	runner := &fakeOrchRunner{
		snapMount: bundle.Global.LVM.MountPoint,
		stackName: stackName,
		psOutput:  []byte(`{"Service":"pg","State":"running","Health":"healthy"}`),
	}
	orch := newTestOrchestrator(runner, pPath, gPath, nil)

	if err := orch.Backup(ctx, &bundle, &BackupOptions{NoSync: true, Out: io.Discard}); err != nil {
		t.Fatalf("Backup failed: %v", err)
	}
	archivePath := filepath.Join(bundle.Global.BackupRoot, stackName, stackName+"_2026-11-16_15-00-00.tar")

	var dryBuf bytes.Buffer
	dryErr := orch.Restore(ctx, &bundle.Global, &RestoreOptions{
		ArchivePath: archivePath,
		DryRun:      true,
		Out:         &dryBuf,
	})
	if dryErr != nil {
		t.Fatalf("expected dry-run restore success, got %v", dryErr)
	}
	if !strings.Contains(dryBuf.String(), "Dry-run restore plan for stack") {
		t.Fatalf("expected dry-run output, got:\n%s", dryBuf.String())
	}

	var runBuf bytes.Buffer
	runErr := orch.Restore(ctx, &bundle.Global, &RestoreOptions{
		ArchivePath: archivePath,
		AngieMode:   RestoreModeAll,
		Out:         &runBuf,
	})
	if runErr != nil {
		t.Fatalf("expected restore success, got %v", runErr)
	}
}

func TestRunRestoreSequenceAndErrors(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := t.TempDir()
	stackName := "app-restore-seq"
	bundle := buildTestBundle(t, root, stackName)
	pPath, gPath := writeFakeAccounts(t, root, stackName)
	runner := &fakeOrchRunner{snapMount: bundle.Global.LVM.MountPoint, stackName: stackName}
	orch := newTestOrchestrator(runner, pPath, gPath, nil)

	manifest := &archive.Manifest{
		StackName: stackName,
		StackDir:  bundle.Stacks[0].StackDir,
	}

	unpackErr := orch.runRestoreSequence(
		ctx,
		&bundle.Global,
		filepath.Join(root, "missing.tar"),
		t.TempDir(),
		io.Discard,
		manifest,
		angieSelection{},
		nil,
		nil,
	)
	if unpackErr == nil || !strings.Contains(unpackErr.Error(), "unpack container") {
		t.Fatalf("expected unpack container error, got %v", unpackErr)
	}

	if err := orch.Backup(ctx, &bundle, &BackupOptions{NoSync: true, Out: io.Discard}); err != nil {
		t.Fatalf("Backup failed: %v", err)
	}
	archivePath := filepath.Join(bundle.Global.BackupRoot, stackName, stackName+"_2026-11-16_15-00-00.tar")

	stepErr := orch.runRestoreSequence(
		ctx,
		&bundle.Global,
		archivePath,
		t.TempDir(),
		io.Discard,
		manifest,
		angieSelection{},
		nil,
		[]config.RestoreStep{{Action: "unknown_step_action"}},
	)
	if stepErr == nil || !strings.Contains(stepErr.Error(), "restore step[0]") {
		t.Fatalf("expected restore step[0] error, got %v", stepErr)
	}
}

func TestExecuteComposeLifecycleDown(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := t.TempDir()
	composeFile := filepath.Join(root, "docker-compose.yaml")
	if err := os.WriteFile(composeFile, []byte("services: {}\n"), 0o600); err != nil {
		t.Fatalf("write compose file: %v", err)
	}

	pPath, gPath := writeFakeAccounts(t, root, "app-comp-down")
	runner := &fakeOrchRunner{failMatch: "down"}
	failOrch := newTestOrchestrator(runner, pPath, gPath, nil)

	for idx, path := range []string{filepath.Join(root, "absent.yaml"), composeFile} {
		step := config.RestoreStep{Action: "compose_down"}
		handled, err := failOrch.executeComposeLifecycleStep(ctx, path, &step)
		if !handled {
			t.Fatal("expected handled=true")
		}
		if idx == 0 && err != nil {
			t.Fatalf("expected nil err for absent compose file, got %v", err)
		}
		if idx == 1 && (err == nil || !strings.Contains(err.Error(), "compose down")) {
			t.Fatalf("expected compose down error, got %v", err)
		}
	}
}

func TestExecuteComposeLifecycleRestart(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := t.TempDir()
	composeFile := filepath.Join(root, "docker-compose.yaml")
	if err := os.WriteFile(composeFile, []byte("services: {}\n"), 0o600); err != nil {
		t.Fatalf("write compose file: %v", err)
	}

	pPath, gPath := writeFakeAccounts(t, root, "app-comp-restart")
	failOrch := newTestOrchestrator(&fakeOrchRunner{failMatch: "compose "}, pPath, gPath, nil)
	okOrch := newTestOrchestrator(&fakeOrchRunner{}, pPath, gPath, nil)

	for idx, orch := range []*Orchestrator{failOrch, okOrch} {
		step := config.RestoreStep{Action: "compose_restart", Services: []string{"web_gamma"}}
		handled, err := orch.executeComposeLifecycleStep(ctx, composeFile, &step)
		if !handled {
			t.Fatal("expected handled=true")
		}
		if idx == 0 && (err == nil || !strings.Contains(err.Error(), "compose restart")) {
			t.Fatalf("expected restart error, got %v", err)
		}
		if idx == 1 && err != nil {
			t.Fatalf("expected restart success, got %v", err)
		}
	}
}

func TestExecuteComposeLifecycleOther(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := t.TempDir()
	composeFile := filepath.Join(root, "docker-compose.yaml")
	if err := os.WriteFile(composeFile, []byte("services: {}\n"), 0o600); err != nil {
		t.Fatalf("write compose file: %v", err)
	}

	pPath, gPath := writeFakeAccounts(t, root, "app-comp-other")
	failOrch := newTestOrchestrator(&fakeOrchRunner{failMatch: "compose "}, pPath, gPath, nil)

	cases := []struct {
		composePath string
		wantErr     string
		name        string
		step        config.RestoreStep
		wantHandled bool
	}{
		{
			step:        config.RestoreStep{Action: "compose_up", Services: []string{"web_alpha"}},
			composePath: composeFile,
			wantErr:     "compose up",
			name:        "compose up error",
			wantHandled: true,
		},
		{
			step:        config.RestoreStep{Action: "wait_healthy", Services: []string{"web_beta"}, TimeoutSec: 1},
			composePath: composeFile,
			wantErr:     "wait healthy",
			name:        "wait healthy error",
			wantHandled: true,
		},
		{
			step:        config.RestoreStep{Action: "compose_run", Service: "web_delta", Command: []string{"true"}},
			composePath: composeFile,
			wantErr:     "compose run",
			name:        "compose run delegate",
			wantHandled: true,
		},
		{
			step:        config.RestoreStep{Action: "unsupported_lifecycle"},
			composePath: composeFile,
			name:        "unsupported lifecycle action",
			wantHandled: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			gotHandled, gotErr := failOrch.executeComposeLifecycleStep(ctx, tc.composePath, &tc.step)
			if gotHandled != tc.wantHandled {
				t.Fatalf("expected handled=%v, got %v", tc.wantHandled, gotHandled)
			}
			if tc.wantErr != "" && (gotErr == nil || !strings.Contains(gotErr.Error(), tc.wantErr)) {
				t.Fatalf("expected error containing %q, got %v", tc.wantErr, gotErr)
			}
			if tc.wantErr == "" && gotErr != nil {
				t.Fatalf("unexpected error: %v", gotErr)
			}
		})
	}
}

func TestExecuteComposeRestoreCommand(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := t.TempDir()
	pPath, gPath := writeFakeAccounts(t, root, "app-cmd")

	okOrch := newTestOrchestrator(&fakeOrchRunner{}, pPath, gPath, nil)
	failOrch := newTestOrchestrator(&fakeOrchRunner{failMatch: "-T"}, pPath, gPath, nil)
	composeFile := filepath.Join(root, "docker-compose.yaml")

	for _, tc := range []struct {
		action  string
		service string
	}{
		{action: "compose_exec", service: "app"},
		{action: "compose_run", service: "worker"},
	} {
		step := config.RestoreStep{Action: tc.action, Service: tc.service, Command: []string{"ls"}}
		if err := okOrch.executeComposeRestoreCommand(ctx, composeFile, &step); err != nil {
			t.Fatalf("expected %s success, got %v", tc.action, err)
		}

		errStep := config.RestoreStep{Action: tc.action, Service: tc.service, Args: []string{"ls"}}
		if err := failOrch.executeComposeRestoreCommand(ctx, composeFile, &errStep); err == nil {
			t.Fatalf("expected %s error, got nil", tc.action)
		}
	}
}

func TestExecuteDataExtractFiles(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := t.TempDir()
	stackName := "app-data-extract"
	bundle := buildTestBundle(t, root, stackName)
	pPath, gPath := writeFakeAccounts(t, root, stackName)

	runner := &fakeOrchRunner{snapMount: bundle.Global.LVM.MountPoint, stackName: stackName}
	okOrch := newTestOrchestrator(runner, pPath, gPath, nil)
	failOrch := newTestOrchestrator(&fakeOrchRunner{failMatch: "tar"}, pPath, gPath, nil)

	rctx := &restoreExecutionContext{
		manifest: &archive.Manifest{
			StackName: stackName,
			StackDir:  bundle.Stacks[0].StackDir,
			User:      bundle.Stacks[0].User,
		},
		global:    &bundle.Global,
		out:       io.Discard,
		unpackDir: t.TempDir(),
	}

	extractCases := []struct {
		orch        *Orchestrator
		archiveName string
		wantErr     string
		name        string
	}{
		{
			orch:    okOrch,
			wantErr: "manifest missing files_archive",
			name:    "missing archive",
		},
		{
			orch:        failOrch,
			archiveName: "files.tar.zst",
			wantErr:     "extract stack files",
			name:        "tar error",
		},
		{
			orch:        okOrch,
			archiveName: "files.tar.zst",
			name:        "extract success",
		},
	}
	for _, tc := range extractCases {
		rctx.manifest.FilesArchive = tc.archiveName
		handled, err := tc.orch.executeDataOrInfraStep(ctx, rctx, &config.RestoreStep{Action: "extract_files"})
		if !handled {
			t.Fatalf("expected handled=true for %s", tc.name)
		}
		if tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)) {
			t.Fatalf("expected error containing %q, got %v", tc.wantErr, err)
		}
		if tc.wantErr == "" && err != nil {
			t.Fatalf("unexpected error for %s: %v", tc.name, err)
		}
	}

	handled, errMove := okOrch.executeDataOrInfraStep(ctx, rctx, &config.RestoreStep{Action: "move_contents", From: "nonexistent", To: "dst"})
	if !handled || errMove == nil || !strings.Contains(errMove.Error(), "stat source directory") {
		t.Fatalf("expected move_contents error, got handled=%v err=%v", handled, errMove)
	}

	handled, errDefault := okOrch.executeDataOrInfraStep(ctx, rctx, &config.RestoreStep{Action: "unhandled_data_action"})
	if handled || errDefault != nil {
		t.Fatalf("expected unhandled action returning false and nil err, got handled=%v err=%v", handled, errDefault)
	}
}

func TestExecuteDataApplyDAC(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := t.TempDir()
	stackName := "app-data-dac"
	bundle := buildTestBundle(t, root, stackName)
	pPath, gPath := writeFakeAccounts(t, root, stackName)

	runner := &fakeOrchRunner{snapMount: bundle.Global.LVM.MountPoint, stackName: stackName}
	okOrch := newTestOrchestrator(runner, pPath, gPath, nil)
	dacErrOrch := newTestOrchestrator(runner, pPath, gPath, nil)
	dacErrOrch.DAC = &dac.Applier{
		LookupUser: func(_ string) (int, error) { return 0, os.ErrNotExist },
	}

	rctx := &restoreExecutionContext{
		manifest: &archive.Manifest{
			StackName: stackName,
			StackDir:  bundle.Stacks[0].StackDir,
			User:      bundle.Stacks[0].User,
		},
		global:    &bundle.Global,
		out:       io.Discard,
		unpackDir: t.TempDir(),
	}

	dacCases := []struct {
		orch    *Orchestrator
		wantErr string
		name    string
		rules   []config.DACRule
	}{
		{
			orch: okOrch,
			name: "empty rules",
		},
		{
			orch:    dacErrOrch,
			rules:   []config.DACRule{{Path: "absent-dac-file", Owner: bundle.Stacks[0].DAC[0].Owner, FileMode: "0600"}},
			wantErr: "apply stack DAC",
			name:    "apply error",
		},
		{
			orch:  okOrch,
			rules: bundle.Stacks[0].DAC,
			name:  "apply success",
		},
	}
	for _, tc := range dacCases {
		rctx.manifest.DAC = tc.rules
		handled, err := tc.orch.executeDataOrInfraStep(ctx, rctx, &config.RestoreStep{Action: "apply_dac"})
		if !handled {
			t.Fatalf("expected handled=true for %s", tc.name)
		}
		if tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)) {
			t.Fatalf("expected error containing %q, got %v", tc.wantErr, err)
		}
		if tc.wantErr == "" && err != nil {
			t.Fatalf("unexpected error for %s: %v", tc.name, err)
		}
	}
}

func TestExecuteRestoreStepUnsupported(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	pPath, gPath := writeFakeAccounts(t, root, "app-step")
	orch := newTestOrchestrator(&fakeOrchRunner{}, pPath, gPath, nil)

	rctx := &restoreExecutionContext{
		manifest: &archive.Manifest{
			ComposeFile: filepath.Join(root, "docker-compose.yaml"),
		},
	}
	err := orch.executeRestoreStep(context.Background(), rctx, &config.RestoreStep{Action: "not_a_real_action"})
	if err == nil || !strings.Contains(err.Error(), "unsupported restore step action") {
		t.Fatalf("expected unsupported restore step action error, got %v", err)
	}
}

func TestEffectiveAndFilterRestoreSteps(t *testing.T) {
	t.Parallel()

	customManifest := &archive.Manifest{
		RestoreSteps: []config.RestoreStep{{Action: "custom_step"}},
	}
	steps := effectiveRestoreSteps(customManifest)
	if len(steps) != 1 || steps[0].Action != "custom_step" {
		t.Fatalf("expected 1 custom step, got %+v", steps)
	}

	defaultSteps := effectiveRestoreSteps(&archive.Manifest{})
	if len(defaultSteps) != 7 {
		t.Fatalf("expected 7 default steps, got %d", len(defaultSteps))
	}

	inputSteps := []config.RestoreStep{
		{Action: "filter_s1"},
		{Action: "filter_s2"},
		{Action: "filter_s3"},
		{Action: "filter_s4"},
		{Action: "filter_s5"},
		{Action: "filter_s6"},
		{Action: "filter_s7"},
	}

	filterCases := []struct {
		name      string
		only      []string
		skip      []string
		wantCount int
	}{
		{
			only:      []string{"filter_s1", "filter_s2"},
			name:      "only filter",
			wantCount: 2,
		},
		{
			skip:      []string{"filter_s3"},
			name:      "skip filter",
			wantCount: 6,
		},
		{
			only:      []string{"filter_s5", "filter_s6"},
			skip:      []string{"filter_s7"},
			name:      "both filters",
			wantCount: 2,
		},
	}

	for _, tc := range filterCases {
		filtered := filterRestoreSteps(inputSteps, tc.only, tc.skip)
		if len(filtered) != tc.wantCount {
			t.Fatalf("%s: expected %d steps, got %d", tc.name, tc.wantCount, len(filtered))
		}
	}
}
