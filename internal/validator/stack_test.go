package validator

import (
	"strings"
	"testing"

	"github.com/webstudiobond/backup-recovery-orchestrator/internal/config"
)

func TestValidateStackConfig(t *testing.T) {
	t.Parallel()

	known := map[string]struct{}{"main_target": {}}

	cases := []struct {
		modify  func(*config.StackConfig)
		name    string
		wantErr string
	}{
		{
			name:    "invalid stack name format",
			modify:  func(c *config.StackConfig) { c.Name = "bad name" },
			wantErr: "validate stack name",
		},
		{
			name:    "reserved self backup name identifier",
			modify:  func(c *config.StackConfig) { c.Name = config.ReservedSelfBackupName },
			wantErr: "reserved for selfbackup",
		},
		{
			name:    "invalid user name format",
			modify:  func(c *config.StackConfig) { c.User.Name = "" },
			wantErr: "validate user name",
		},
		{
			name:    "relative stack_dir path",
			modify:  func(c *config.StackConfig) { c.StackDir = "opt/service" },
			wantErr: "stack_dir",
		},
		{
			name:    "relative compose_file path",
			modify:  func(c *config.StackConfig) { c.ComposeFile = "docker-compose.yml" },
			wantErr: "compose_file",
		},
		{
			name:    "invalid zstd level in stack",
			modify:  func(c *config.StackConfig) { c.ZstdLevel = 25 },
			wantErr: "zstd_level",
		},
		{
			name:    "delegated lvm error",
			modify:  func(c *config.StackConfig) { c.LVM.VGName = "" },
			wantErr: "validate stack lvm",
		},
		{
			name:    "delegated retention error",
			modify:  func(c *config.StackConfig) { c.Retention.LocalCount = 0 },
			wantErr: "validate stack retention",
		},
		{
			name:    "delegated stack destinations error",
			modify:  func(c *config.StackConfig) { c.Destinations = []string{"ghost"} },
			wantErr: "validate stack destinations",
		},
		{
			name:    "delegated reverse_proxy assets error",
			modify:  func(c *config.StackConfig) { c.ReverseProxy.DomainConfs = []string{"../escape.conf"} },
			wantErr: "validate reverse_proxy assets",
		},
		{
			name: "delegated reverse_proxy_provider error",
			modify: func(c *config.StackConfig) {
				c.ReverseProxyProvider = &config.ReverseProxyProviderConfig{Type: "unsupported"}
			},
			wantErr: "validate reverse_proxy_provider",
		},
		{
			name:    "delegated dac error",
			modify:  func(c *config.StackConfig) { c.DAC = []config.DACRule{{Path: "../escape", Owner: "root:root"}} },
			wantErr: "validate dac",
		},
		{
			name:    "delegated host_files error",
			modify:  func(c *config.StackConfig) { c.HostFiles = []config.HostFileConfig{{Path: "/etc/shadow"}} },
			wantErr: "validate host_files",
		},
		{
			name:    "delegated databases error",
			modify:  func(c *config.StackConfig) { c.Databases = []config.DatabaseConfig{{ID: "invalid id"}} },
			wantErr: "validate databases",
		},
		{
			name:    "delegated hooks error",
			modify:  func(c *config.StackConfig) { c.Hooks.PreSnapshot = []config.HookStep{{Action: "unknown"}} },
			wantErr: "validate hooks",
		},
		{
			name:    "restore steps error",
			modify:  func(c *config.StackConfig) { c.RestoreSteps = []config.RestoreStep{{Action: "drop_all"}} },
			wantErr: "unsupported action",
		},
		{
			name: "valid stack with valid reverse_proxy_provider",
			modify: func(c *config.StackConfig) {
				c.ReverseProxyProvider = &config.ReverseProxyProviderConfig{
					Type:        "angie",
					ServiceName: "proxy-svc",
					CertsDir:    "data/certs",
					DomainsDir:  "data/conf.d/domains",
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := validTestStackConfig()
			tc.modify(&cfg)
			err := validateStackConfig(&cfg, known)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("expected error containing %q, got %v", tc.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}
