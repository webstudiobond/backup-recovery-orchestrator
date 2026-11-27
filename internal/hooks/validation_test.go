package hooks

import (
	"testing"

	"github.com/webstudiobond/backup-recovery-orchestrator/internal/config"
)

func TestValidateHooks(t *testing.T) {
	t.Parallel()

	cases := []struct {
		hooks   *config.HooksConfig
		name    string
		wantErr bool
	}{
		{
			name:    "nil hooks config returns nil",
			hooks:   nil,
			wantErr: false,
		},
		{
			name: "invalid pre_dump hook step",
			hooks: &config.HooksConfig{
				PreDump: []config.HookStep{
					{Action: "bad_action_predump"},
				},
			},
			wantErr: true,
		},
		{
			name: "invalid pre_snapshot hook step",
			hooks: &config.HooksConfig{
				PreSnapshot: []config.HookStep{
					{Action: "bad_action_presnap"},
				},
			},
			wantErr: true,
		},
		{
			name: "invalid post_snapshot hook step",
			hooks: &config.HooksConfig{
				PostSnapshot: []config.HookStep{
					{Action: "bad_action_postsnap"},
				},
			},
			wantErr: true,
		},
		{
			name: "valid hooks in all phases",
			hooks: &config.HooksConfig{
				PreDump: []config.HookStep{
					{Action: hookActionRemovePath, Path: "/tmp/pre_dump.sock"},
				},
				PreSnapshot: []config.HookStep{
					{Action: hookActionWaitForFile, Path: "/tmp/pre_snap.flag", TimeoutSec: 10},
				},
				PostSnapshot: []config.HookStep{
					{Action: hookActionComposeKill, Service: "worker_backend", Signal: "SIGUSR1"},
				},
			},
			wantErr: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := ValidateHooks(tc.hooks)
			if (err != nil) != tc.wantErr {
				t.Fatalf("ValidateHooks() error = %v, wantErr = %v", err, tc.wantErr)
			}
		})
	}
}

func TestValidateHookStep(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		step    config.HookStep
		wantErr bool
	}{
		{
			name:    "remove_path with relative path",
			step:    config.HookStep{Action: hookActionRemovePath, Path: "rel/socket.sock"},
			wantErr: true,
		},
		{
			name:    "wait_for_file with zero timeout",
			step:    config.HookStep{Action: hookActionWaitForFile, Path: "/var/run/file.lock", TimeoutSec: 0},
			wantErr: true,
		},
		{
			name:    "compose_kill missing signal",
			step:    config.HookStep{Action: hookActionComposeKill, Service: "web_container"},
			wantErr: true,
		},
		{
			name:    "hook compose exec missing command",
			step:    config.HookStep{Action: hookActionComposeExec, Service: "app_worker"},
			wantErr: true,
		},
		{
			name:    "hook compose exec missing service",
			step:    config.HookStep{Action: hookActionComposeExec, Command: []string{"ls"}},
			wantErr: true,
		},
		{
			name:    "valid compose exec step",
			step:    config.HookStep{Action: hookActionComposeExec, Service: "valid_worker", Command: []string{"sync"}},
			wantErr: false,
		},
		{
			name:    "hook compose run missing command",
			step:    config.HookStep{Action: hookActionComposeRun, Service: "batch_runner"},
			wantErr: true,
		},
		{
			name:    "hook compose run missing service",
			step:    config.HookStep{Action: hookActionComposeRun, Command: []string{"migrate"}},
			wantErr: true,
		},
		{
			name:    "valid compose run step",
			step:    config.HookStep{Action: hookActionComposeRun, Service: "valid_migrator", Command: []string{"up"}},
			wantErr: false,
		},
		{
			name:    "unsupported hook action name",
			step:    config.HookStep{Action: "unknown_hook_op"},
			wantErr: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := validateHookStep("pre_dump", &tc.step)
			if (err != nil) != tc.wantErr {
				t.Fatalf("validateHookStep() error = %v, wantErr = %v", err, tc.wantErr)
			}
		})
	}
}
