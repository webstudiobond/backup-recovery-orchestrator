package config

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/webstudiobond/backup-recovery-orchestrator/internal/reverseproxy"
)

func TestValidateGlobalReverseProxyStack(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		stackName string
		wantErr   bool
	}{
		{name: "empty disables reverse proxy", stackName: "", wantErr: false},
		{name: "valid stack name", stackName: "edge-proxy", wantErr: false},
		{name: "invalid identifier", stackName: "-bad", wantErr: true},
		{name: "reserved selfbackup rejected", stackName: ReservedSelfBackupName, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := validateGlobalReverseProxyStack(tc.stackName)
			if (err != nil) != tc.wantErr {
				t.Fatalf("validateGlobalReverseProxyStack(%q) error = %v, wantErr = %v", tc.stackName, err, tc.wantErr)
			}
		})
	}
}

func TestValidateReverseProxyProvider(t *testing.T) {
	t.Parallel()

	valid := func() ReverseProxyProviderConfig {
		return ReverseProxyProviderConfig{
			Type:        reverseproxy.TypeAngie,
			ServiceName: "proxy-svc",
			CertsDir:    "data/certs",
			DomainsDir:  "data/conf.d/domains",
			StreamDir:   "data/conf.d/stream",
		}
	}

	cases := []struct {
		mutate  func(*ReverseProxyProviderConfig)
		name    string
		wantErr string
	}{
		{
			name:   "valid full provider",
			mutate: func(*ReverseProxyProviderConfig) {},
		},
		{
			name:   "valid without optional stream_dir",
			mutate: func(c *ReverseProxyProviderConfig) { c.StreamDir = "" },
		},
		{
			name:    "unsupported type",
			mutate:  func(c *ReverseProxyProviderConfig) { c.Type = "caddy" },
			wantErr: "unsupported reverse_proxy_provider.type",
		},
		{
			name:    "missing service_name",
			mutate:  func(c *ReverseProxyProviderConfig) { c.ServiceName = "" },
			wantErr: "service_name",
		},
		{
			name:    "missing domains_dir",
			mutate:  func(c *ReverseProxyProviderConfig) { c.DomainsDir = "" },
			wantErr: "domains_dir must not be empty",
		},
		{
			name:    "missing certs_dir",
			mutate:  func(c *ReverseProxyProviderConfig) { c.CertsDir = "" },
			wantErr: "certs_dir must not be empty",
		},
		{
			name:    "absolute domains_dir rejected",
			mutate:  func(c *ReverseProxyProviderConfig) { c.DomainsDir = "/etc/proxy/domains" },
			wantErr: "must be a relative path",
		},
		{
			name:    "dot certs_dir rejected",
			mutate:  func(c *ReverseProxyProviderConfig) { c.CertsDir = "." },
			wantErr: "must stay within stack_dir",
		},
		{
			name:    "escaping stream_dir rejected",
			mutate:  func(c *ReverseProxyProviderConfig) { c.StreamDir = "../outside" },
			wantErr: "must be a relative path",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := valid()
			tc.mutate(&cfg)
			err := validateReverseProxyProvider(&cfg)
			if tc.wantErr == "" && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)) {
				t.Fatalf("expected error containing %q, got %v", tc.wantErr, err)
			}
		})
	}
}

func TestValidateBundleReverseProxy(t *testing.T) {
	t.Parallel()

	makeProvider := func() *ReverseProxyProviderConfig {
		return &ReverseProxyProviderConfig{
			Type:        reverseproxy.TypeAngie,
			ServiceName: "proxy-gw",
			CertsDir:    "data/certs",
			DomainsDir:  "data/conf.d/domains",
		}
	}

	cases := []struct {
		global  *GlobalConfig
		name    string
		wantErr string
		stacks  []StackConfig
	}{
		{
			name:    "disabled global rejects stack provider",
			global:  &GlobalConfig{},
			stacks:  []StackConfig{{Name: "web-portal", ReverseProxyProvider: makeProvider()}},
			wantErr: "global reverse_proxy_stack is not set",
		},
		{
			name:    "disabled global rejects stack proxy assets",
			global:  &GlobalConfig{},
			stacks:  []StackConfig{{Name: "api-service", ReverseProxy: StackReverseProxyConfig{DomainConfs: []string{"a.example.com.conf"}}}},
			wantErr: "global reverse_proxy_stack is not set",
		},
		{
			name:   "disabled global allows deploy_reverse_proxy no-op without assets",
			global: &GlobalConfig{},
			stacks: []StackConfig{{Name: "worker-node", RestoreSteps: []RestoreStep{{Action: "deploy_reverse_proxy"}}}},
		},
		{
			name:    "enabled global rejects missing proxy stack",
			global:  &GlobalConfig{ReverseProxyStack: "proxy-missing"},
			stacks:  []StackConfig{{Name: "blog-site"}},
			wantErr: "not found in conf_dir",
		},
		{
			name:    "enabled global rejects proxy stack without provider",
			global:  &GlobalConfig{ReverseProxyStack: "proxy-no-prov"},
			stacks:  []StackConfig{{Name: "proxy-no-prov"}},
			wantErr: "must define reverse_proxy_provider",
		},
		{
			name:   "enabled global rejects provider on non-proxy stack",
			global: &GlobalConfig{ReverseProxyStack: "proxy-main"},
			stacks: []StackConfig{
				{Name: "proxy-main", ReverseProxyProvider: makeProvider()},
				{Name: "mail-stack", ReverseProxyProvider: makeProvider()},
			},
			wantErr: "must not define reverse_proxy_provider",
		},
		{
			name:   "enabled global rejects streams without stream_dir",
			global: &GlobalConfig{ReverseProxyStack: "proxy-streams"},
			stacks: []StackConfig{
				{Name: "proxy-streams", ReverseProxyProvider: makeProvider()},
				{Name: "turn-stack", ReverseProxy: StackReverseProxyConfig{StreamConfs: []string{"turn.inc"}}},
			},
			wantErr: "stream_dir is required",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := validateBundleReverseProxy(tc.global, tc.stacks)
			if tc.wantErr == "" && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)) {
				t.Fatalf("expected error containing %q, got %v", tc.wantErr, err)
			}
		})
	}
}

func TestLoadBundleReverseProxyErrors(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	globalPath := filepath.Join(root, "config.json")
	writeTestFile(t, globalPath, `{
  "backup_root": "/backups",
  "reverse_proxy_stack": "proxy-stack",
  "lvm": {"vg_name":"vg","lv_name":"lv","snapshot_name":"snap","snapshot_size":"35G","mount_point":"/mnt/snap"}
}`)
	writeTestFile(t, filepath.Join(root, "conf.d", "proxy-stack.json"), `{
  "user": {"name":"proxy","uid":1001,"gid":1001},
  "reverse_proxy_provider": {
    "type": "angie",
    "service_name": "edge",
    "certs_dir": "data/certs",
    "domains_dir": "data/conf.d/domains"
  },
  "reverse_proxy": {}
}`)
	if _, err := LoadBundle(context.Background(), globalPath); err == nil || !strings.Contains(err.Error(), "must not define reverse_proxy") {
		t.Fatalf("expected empty reverse_proxy block rejection on proxy stack, got %v", err)
	}

	invalidProviderStack := &StackConfig{
		Name:                 "px-invalid",
		User:                 UserConfig{Name: "px-invalid"},
		StackDir:             "/home/px-invalid",
		ComposeFile:          "/home/px-invalid/docker-compose.yaml",
		ZstdLevel:            6,
		Retention:            RetentionConfig{LocalCount: 1, RemoteCount: 1},
		LVM:                  LVMConfig{VGName: "vg", LVName: "lv", SnapshotName: "snap", SnapshotSize: "1G", MountPoint: "/mnt/snap"},
		ReverseProxyProvider: &ReverseProxyProviderConfig{Type: "unsupported"},
	}
	if err := validateStackConfig(invalidProviderStack, nil); err == nil || !strings.Contains(err.Error(), "unsupported reverse_proxy_provider.type") {
		t.Fatalf("expected validateStackConfig provider error, got %v", err)
	}
}
