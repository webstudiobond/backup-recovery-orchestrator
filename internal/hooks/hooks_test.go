package hooks

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/webstudiobond/backup-recovery-orchestrator/internal/config"
	"github.com/webstudiobond/backup-recovery-orchestrator/internal/docker"
)

func TestRunPhaseSuccess(t *testing.T) {
	t.Parallel()

	stackDir := t.TempDir()
	composeFile := filepath.Join(stackDir, "docker-compose.yaml")
	checkpointDir := filepath.Join(stackDir, "daemon", "data", "backup_checkpoint")
	markerFile := filepath.Join(checkpointDir, "CURRENT")

	if err := os.MkdirAll(checkpointDir, 0o750); err != nil {
		t.Fatalf("mkdir checkpoint: %v", err)
	}

	go func() {
		time.Sleep(10 * time.Millisecond)
		if writeErr := os.WriteFile(markerFile, []byte("ok"), 0o600); writeErr != nil {
			t.Errorf("write marker: %v", writeErr)
		}
	}()

	fake := &recordedDocker{}
	runner := &Runner{
		Docker:       docker.NewClient(fake),
		PollInterval: 2 * time.Millisecond,
	}

	preSteps := []config.HookStep{
		{Action: "compose_kill", Service: "daemon", Signal: "USR2"},
		{Action: "wait_for_file", Path: markerFile, TimeoutSec: 2},
		{Action: "compose_exec", Service: "daemon", Command: []string{"echo", "ready"}},
		{Action: "compose_run", Service: "helper", Command: []string{"check"}},
	}

	if err := runner.RunPhase(context.Background(), "pre_snapshot", stackDir, composeFile, preSteps); err != nil {
		t.Fatalf("unexpected pre_snapshot hook error: %v", err)
	}

	if len(fake.args) != 3 {
		t.Fatalf("expected 3 docker calls, got %d", len(fake.args))
	}

	postSteps := []config.HookStep{
		{Action: "remove_path", Path: checkpointDir},
	}
	if err := runner.RunPhase(context.Background(), "post_snapshot", stackDir, composeFile, postSteps); err != nil {
		t.Fatalf("unexpected post_snapshot hook error: %v", err)
	}
	if _, statErr := os.Stat(checkpointDir); !os.IsNotExist(statErr) {
		t.Fatalf("expected checkpoint dir %q to be removed, statErr=%v", checkpointDir, statErr)
	}
}

func TestRunPhaseErrors(t *testing.T) {
	t.Parallel()

	stackDir := t.TempDir()
	composeFile := filepath.Join(stackDir, "docker-compose.yaml")

	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()

	cases := []struct {
		ctx         context.Context
		name        string
		removePath  string
		errContains string
		steps       []config.HookStep
	}{
		{
			ctx:         canceledCtx,
			name:        "canceled context",
			steps:       []config.HookStep{{Action: "cancel_probe"}},
			errContains: "canceled",
		},
		{
			ctx:         context.Background(),
			name:        "remove_path refuses stack root",
			removePath:  stackDir,
			errContains: "outside stack_dir",
		},
		{
			ctx:         context.Background(),
			name:        "remove_path refuses escaping path",
			removePath:  filepath.Dir(stackDir),
			errContains: "outside stack_dir",
		},
		{
			ctx:         context.Background(),
			name:        "wait_for_file times out",
			steps:       []config.HookStep{{Action: "wait_for_file", Path: filepath.Join(stackDir, "missing"), TimeoutSec: 1}},
			errContains: "timed out",
		},
		{
			ctx:         context.Background(),
			name:        "unsupported action",
			steps:       []config.HookStep{{Action: "unknown_hook"}},
			errContains: "unsupported hook action",
		},
	}

	runner := &Runner{
		Docker:       docker.NewClient(&recordedDocker{}),
		PollInterval: 5 * time.Millisecond,
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			steps := tc.steps
			if tc.removePath != "" {
				steps = []config.HookStep{{Action: "remove_path", Path: tc.removePath}}
			}
			err := runner.RunPhase(tc.ctx, "pre_snapshot", stackDir, composeFile, steps)
			if err == nil || !strings.Contains(err.Error(), tc.errContains) {
				t.Fatalf("expected error containing %q, got %v", tc.errContains, err)
			}
		})
	}
}

func TestNewRunnerDefaults(t *testing.T) {
	t.Parallel()

	r1 := NewRunner(nil)
	if r1.Docker == nil || r1.PollInterval != defaultWaitPollInterval {
		t.Fatalf("expected default runner and poll interval, got docker=%v interval=%v", r1.Docker, r1.PollInterval)
	}

	customClient := docker.NewClient(nil)
	r2 := NewRunner(customClient)
	if r2.Docker != customClient {
		t.Fatalf("expected injected docker client")
	}
}

func TestRunStepDockerErrors(t *testing.T) {
	t.Parallel()

	failingDocker := &recordedDocker{runErr: errors.New("simulated docker error")}
	runner := &Runner{
		Docker:       docker.NewClient(failingDocker),
		PollInterval: time.Millisecond,
	}

	cases := []struct {
		name        string
		action      string
		service     string
		signal      string
		errContains string
		command     []string
	}{
		{
			name:        "compose kill error",
			action:      "compose_kill",
			service:     "kill-svc",
			signal:      "USR1",
			errContains: "compose kill",
		},
		{
			name:        "compose exec error",
			action:      "compose_exec",
			service:     "exec-svc",
			command:     []string{"true"},
			errContains: "compose exec",
		},
		{
			name:        "compose run error",
			action:      "compose_run",
			service:     "run-svc",
			command:     []string{"true"},
			errContains: "compose run",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			step := &config.HookStep{
				Action:  tc.action,
				Service: tc.service,
				Signal:  tc.signal,
				Command: tc.command,
			}
			err := runner.runStep(context.Background(), "/home/app", "/home/app/docker-compose.yaml", step)
			if err == nil || !strings.Contains(err.Error(), tc.errContains) {
				t.Fatalf("expected error containing %q, got %v", tc.errContains, err)
			}
		})
	}
}

func TestWaitForFileDefaultsAndErrors(t *testing.T) {
	t.Parallel()

	runner := &Runner{
		Docker:       docker.NewClient(&recordedDocker{}),
		PollInterval: 0,
	}

	tmpFile := filepath.Join(t.TempDir(), "ready")
	if err := os.WriteFile(tmpFile, []byte("ok"), 0o600); err != nil {
		t.Fatalf("write temp: %v", err)
	}

	if err := runner.waitForFile(context.Background(), tmpFile, time.Second); err != nil {
		t.Fatalf("expected waitForFile to succeed with default poll interval, got %v", err)
	}

	if err := runner.waitForFile(context.Background(), tmpFile, 0); err == nil {
		t.Fatal("expected error on non-positive timeout")
	}
}

func TestRemovePathMockErrors(t *testing.T) {
	origAbs := filepathAbs
	origRemove := removeAll
	t.Cleanup(func() {
		filepathAbs = origAbs
		removeAll = origRemove
	})

	stackDir := t.TempDir()
	validTarget := filepath.Join(stackDir, "child")
	failTarget := filepath.Join(stackDir, "fail_target_child")

	cases := []struct {
		absMock     func(string) (string, error)
		removeMock  func(string) error
		name        string
		targetPath  string
		errContains string
	}{
		{
			name:        "stack dir abs failure",
			targetPath:  validTarget,
			absMock:     mockFailingAbs,
			errContains: "resolve stack_dir",
		},
		{
			name:        "remove path abs failure",
			targetPath:  failTarget,
			absMock:     mockFailingTargetPathAbs,
			errContains: "resolve remove_path",
		},
		{
			name:        "remove all failure",
			targetPath:  validTarget,
			removeMock:  mockFailingRemoveAll,
			errContains: "remove",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			filepathAbs = origAbs
			removeAll = origRemove
			if tc.absMock != nil {
				filepathAbs = tc.absMock
			}
			if tc.removeMock != nil {
				removeAll = tc.removeMock
			}

			err := removePathWithinStack(stackDir, tc.targetPath)
			if err == nil || !strings.Contains(err.Error(), tc.errContains) {
				t.Fatalf("expected error containing %q, got %v", tc.errContains, err)
			}
		})
	}
}
