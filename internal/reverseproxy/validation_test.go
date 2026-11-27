package reverseproxy

import (
	"strings"
	"testing"

	"github.com/webstudiobond/backup-recovery-orchestrator/internal/config"
)

func TestValidateGlobalStackName(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		stackName string
		wantErr   bool
	}{
		{name: "empty disables reverse proxy", stackName: "", wantErr: false},
		{name: "valid stack name", stackName: "edge-proxy", wantErr: false},
		{name: "invalid identifier", stackName: "-bad", wantErr: true},
		{name: "reserved selfbackup rejected", stackName: config.ReservedSelfBackupName, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := ValidateGlobalStackName(tc.stackName)
			if (err != nil) != tc.wantErr {
				t.Fatalf("ValidateGlobalStackName(%q) error = %v, wantErr = %v", tc.stackName, err, tc.wantErr)
			}
		})
	}
}

func TestValidateStackAssets(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name         string
		reverseProxy config.StackReverseProxyConfig
		wantErr      bool
	}{
		{
			name: "invalid acme names identifier",
			reverseProxy: config.StackReverseProxyConfig{
				ACMENames: []string{".illegal_acme"},
			},
			wantErr: true,
		},
		{
			name: "invalid domain confs identifier",
			reverseProxy: config.StackReverseProxyConfig{
				DomainConfs: []string{"nested/domain.conf"},
			},
			wantErr: true,
		},
		{
			name: "invalid stream confs identifier",
			reverseProxy: config.StackReverseProxyConfig{
				StreamConfs: []string{"bad stream conf"},
			},
			wantErr: true,
		},
		{
			name: "valid stack reverse_proxy entries",
			reverseProxy: config.StackReverseProxyConfig{
				ACMENames:   []string{"app.example.com"},
				DomainConfs: []string{"app.example.com.conf"},
				StreamConfs: []string{"stream.example.org.conf"},
			},
			wantErr: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := ValidateStackAssets(&tc.reverseProxy)
			if (err != nil) != tc.wantErr {
				t.Fatalf("ValidateStackAssets() error = %v, wantErr = %v", err, tc.wantErr)
			}
		})
	}
}

func TestCleanRelativeDir(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		raw      string
		want     string
		wantErr  string
		required bool
	}{
		{name: "empty required rejected", raw: "  ", required: true, wantErr: "must not be empty"},
		{name: "empty optional allowed", raw: "  ", required: false, want: ""},
		{name: "nul byte rejected", raw: "data/\x00bad", required: true, wantErr: "must be a relative path inside stack_dir"},
		{name: "absolute path rejected", raw: "/etc/proxy", required: true, wantErr: "must be a relative path inside stack_dir"},
		{name: "dot rejected", raw: ".", required: true, wantErr: "must stay within stack_dir"},
		{name: "parent traversal rejected", raw: "../outside", required: true, wantErr: "relative path inside stack_dir"},
		{name: "valid nested relative dir cleaned", raw: " data/reverse//vhosts/ ", required: true, want: "data/reverse/vhosts"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := CleanRelativeDir(tc.raw, "reverse_proxy_provider.domains_dir", tc.required)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("expected error containing %q, got %v", tc.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("CleanRelativeDir() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestValidateProviderConfig(t *testing.T) {
	t.Parallel()

	valid := func() config.ReverseProxyProviderConfig {
		return config.ReverseProxyProviderConfig{
			Type:        typeAngie,
			ServiceName: "proxy-svc",
			CertsDir:    "data/certs",
			DomainsDir:  "data/conf.d/domains",
			StreamDir:   "data/conf.d/stream",
		}
	}

	cases := []struct {
		mutate  func(*config.ReverseProxyProviderConfig)
		name    string
		wantErr string
	}{
		{
			name:   "valid full provider",
			mutate: func(*config.ReverseProxyProviderConfig) {},
		},
		{
			name:   "valid without optional stream_dir",
			mutate: func(c *config.ReverseProxyProviderConfig) { c.StreamDir = "" },
		},
		{
			name:    "unsupported type",
			mutate:  func(c *config.ReverseProxyProviderConfig) { c.Type = "caddy" },
			wantErr: "unsupported reverse_proxy_provider.type",
		},
		{
			name:    "missing service_name",
			mutate:  func(c *config.ReverseProxyProviderConfig) { c.ServiceName = "" },
			wantErr: "service_name",
		},
		{
			name:    "missing domains_dir",
			mutate:  func(c *config.ReverseProxyProviderConfig) { c.DomainsDir = "" },
			wantErr: "domains_dir must not be empty",
		},
		{
			name:    "missing certs_dir",
			mutate:  func(c *config.ReverseProxyProviderConfig) { c.CertsDir = "" },
			wantErr: "certs_dir must not be empty",
		},
		{
			name:    "absolute domains_dir rejected",
			mutate:  func(c *config.ReverseProxyProviderConfig) { c.DomainsDir = "/etc/proxy/domains" },
			wantErr: "must be a relative path",
		},
		{
			name:    "dot certs_dir rejected",
			mutate:  func(c *config.ReverseProxyProviderConfig) { c.CertsDir = "." },
			wantErr: "must stay within stack_dir",
		},
		{
			name:    "escaping stream_dir rejected",
			mutate:  func(c *config.ReverseProxyProviderConfig) { c.StreamDir = "../outside" },
			wantErr: "must be a relative path",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := valid()
			tc.mutate(&cfg)
			err := ValidateProviderConfig(&cfg)
			if tc.wantErr == "" && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)) {
				t.Fatalf("expected error containing %q, got %v", tc.wantErr, err)
			}
		})
	}
}

func TestValidateBundle(t *testing.T) {
	t.Parallel()

	makeProvider := func() *config.ReverseProxyProviderConfig {
		return &config.ReverseProxyProviderConfig{
			Type:        typeAngie,
			ServiceName: "proxy-gw",
			CertsDir:    "data/certs",
			DomainsDir:  "data/conf.d/domains",
		}
	}

	cases := []struct {
		global  *config.GlobalConfig
		name    string
		wantErr string
		stacks  []config.StackConfig
	}{
		{
			name:    "disabled global rejects stack provider",
			global:  &config.GlobalConfig{},
			stacks:  []config.StackConfig{{Name: "web-portal", ReverseProxyProvider: makeProvider()}},
			wantErr: "global reverse_proxy_stack is not set",
		},
		{
			name:    "disabled global rejects stack proxy assets",
			global:  &config.GlobalConfig{},
			stacks:  []config.StackConfig{{Name: "api-service", ReverseProxy: config.StackReverseProxyConfig{DomainConfs: []string{"a.example.com.conf"}}}},
			wantErr: "global reverse_proxy_stack is not set",
		},
		{
			name:   "disabled global allows deploy_reverse_proxy no-op without assets",
			global: &config.GlobalConfig{},
			stacks: []config.StackConfig{{Name: "worker-node", RestoreSteps: []config.RestoreStep{{Action: config.RestoreActionDeployReverseProxy}}}},
		},
		{
			name:    "enabled global rejects missing proxy stack",
			global:  &config.GlobalConfig{ReverseProxyStack: "proxy-missing"},
			stacks:  []config.StackConfig{{Name: "blog-site"}},
			wantErr: "not found in conf_dir",
		},
		{
			name:    "enabled global rejects proxy stack without provider",
			global:  &config.GlobalConfig{ReverseProxyStack: "proxy-no-prov"},
			stacks:  []config.StackConfig{{Name: "proxy-no-prov"}},
			wantErr: "must define reverse_proxy_provider",
		},
		{
			name:    "enabled global rejects proxy stack with reverse_proxy assets",
			global:  &config.GlobalConfig{ReverseProxyStack: "proxy-assets"},
			stacks:  []config.StackConfig{{Name: "proxy-assets", ReverseProxyProvider: makeProvider(), ReverseProxy: config.StackReverseProxyConfig{DomainConfs: []string{"x.example.com.conf"}}}},
			wantErr: "must not define reverse_proxy",
		},
		{
			name:   "enabled global rejects provider on non-proxy stack",
			global: &config.GlobalConfig{ReverseProxyStack: "proxy-main"},
			stacks: []config.StackConfig{
				{Name: "proxy-main", ReverseProxyProvider: makeProvider()},
				{Name: "mail-stack", ReverseProxyProvider: makeProvider()},
			},
			wantErr: "must not define reverse_proxy_provider",
		},
		{
			name:   "enabled global rejects streams without stream_dir",
			global: &config.GlobalConfig{ReverseProxyStack: "proxy-streams"},
			stacks: []config.StackConfig{
				{Name: "proxy-streams", ReverseProxyProvider: makeProvider()},
				{Name: "turn-stack", ReverseProxy: config.StackReverseProxyConfig{StreamConfs: []string{"turn.inc"}}},
			},
			wantErr: "stream_dir is required",
		},
		{
			name:   "enabled global accepts valid proxy stack and domain stack",
			global: &config.GlobalConfig{ReverseProxyStack: "proxy-ok"},
			stacks: []config.StackConfig{
				{Name: "proxy-ok", ReverseProxyProvider: makeProvider()},
				{Name: "web-ok", ReverseProxy: config.StackReverseProxyConfig{DomainConfs: []string{"web.example.com.conf"}}},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := ValidateBundle(tc.global, tc.stacks)
			if tc.wantErr == "" && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)) {
				t.Fatalf("expected error containing %q, got %v", tc.wantErr, err)
			}
		})
	}
}
