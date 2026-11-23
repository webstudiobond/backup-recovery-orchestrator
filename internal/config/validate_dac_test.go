package config

import (
	"testing"
)

func TestValidateDACRules(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		rules   []DACRule
		wantErr bool
	}{
		{
			name: "empty rule path rejected",
			rules: []DACRule{
				{Path: "\t \t", Owner: "app_one:app_one", FileMode: "0644"},
			},
			wantErr: true,
		},
		{
			name: "absolute rule path rejected",
			rules: []DACRule{
				{Path: "/var/www/html", Owner: "app_two:app_two", FileMode: "0644"},
			},
			wantErr: true,
		},
		{
			name: "path traversal escaping stack dir",
			rules: []DACRule{
				{Path: "../outside_tree", Owner: "app_three:app_three", FileMode: "0644"},
			},
			wantErr: true,
		},
		{
			name: "invalid attribute in dac rule",
			rules: []DACRule{
				{Path: "data/logs", Owner: "bad_owner_format"},
			},
			wantErr: true,
		},
		{
			name: "valid dac rules collection",
			rules: []DACRule{
				{Path: "data/storage", Owner: "deploy:deploy", DirMode: "0750", FileMode: "0640"},
			},
			wantErr: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := validateDACRules(tc.rules)
			if (err != nil) != tc.wantErr {
				t.Fatalf("validateDACRules() error = %v, wantErr = %v", err, tc.wantErr)
			}
		})
	}
}

func TestValidateHostFiles(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		items   []HostFileConfig
		wantErr bool
	}{
		{
			name: "relative host file path rejected",
			items: []HostFileConfig{
				{Path: "relative/path/file.txt", Owner: "root:root"},
			},
			wantErr: true,
		},
		{
			name: "protected system location path rejected",
			items: []HostFileConfig{
				{Path: "/etc/passwd", Owner: "sys:sys"},
			},
			wantErr: true,
		},
		{
			name: "duplicate cleaned path rejected",
			items: []HostFileConfig{
				{Path: "/opt/app/config.ini", Owner: "bin:bin"},
				{Path: "/opt/app/./config.ini", Owner: "bin:bin"},
			},
			wantErr: true,
		},
		{
			name: "invalid dac attributes in host file",
			items: []HostFileConfig{
				{Path: "/opt/app/server.crt", Owner: "invalid_no_colon"},
			},
			wantErr: true,
		},
		{
			name: "valid distinct host files",
			items: []HostFileConfig{
				{Path: "/opt/app/conf_a.env", Owner: "infra:infra", FileMode: "0600"},
				{Path: "/opt/app/conf_b.env", DirMode: "0700"},
			},
			wantErr: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := validateHostFiles(tc.items)
			if (err != nil) != tc.wantErr {
				t.Fatalf("validateHostFiles() error = %v, wantErr = %v", err, tc.wantErr)
			}
		})
	}
}

func TestValidateDACAttributes(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		owner      string
		dirMode    string
		fileMode   string
		requireOne bool
		wantErr    bool
	}{
		{
			name:       "require one true with all empty attributes",
			requireOne: true,
			wantErr:    true,
		},
		{
			name:       "invalid owner format",
			owner:      "broken_owner",
			requireOne: true,
			wantErr:    true,
		},
		{
			name:       "invalid dir mode non octal",
			dirMode:    "0888",
			requireOne: true,
			wantErr:    true,
		},
		{
			name:       "invalid file mode non octal",
			fileMode:   "0999",
			requireOne: true,
			wantErr:    true,
		},
		{
			name:       "require one false with all empty attributes",
			requireOne: false,
			wantErr:    false,
		},
		{
			name:       "valid complete attributes set",
			owner:      "daemon:daemon",
			dirMode:    "0755",
			fileMode:   "0644",
			requireOne: true,
			wantErr:    false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := validateDACAttributes("dac_label", tc.owner, tc.dirMode, tc.fileMode, tc.requireOne)
			if (err != nil) != tc.wantErr {
				t.Fatalf("validateDACAttributes() error = %v, wantErr = %v", err, tc.wantErr)
			}
		})
	}
}

func TestValidateOwnerSpec(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		spec    string
		wantErr bool
	}{
		{name: "missing colon single part", spec: "onlyuser", wantErr: true},
		{name: "too many colons three parts", spec: "u:g:extra", wantErr: true},
		{name: "empty user part", spec: ":groupname", wantErr: true},
		{name: "empty group part", spec: "username:", wantErr: true},
		{name: "whitespace only parts", spec: "   :   ", wantErr: true},
		{name: "valid user and group", spec: "adm:sysadmin", wantErr: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := validateOwnerSpec(tc.spec)
			if (err != nil) != tc.wantErr {
				t.Fatalf("validateOwnerSpec(%q) error = %v, wantErr = %v", tc.spec, err, tc.wantErr)
			}
		})
	}
}

func TestValidateOctalMode(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		mode    string
		wantErr bool
	}{
		{name: "unparseable string digits", mode: "xyz", wantErr: true},
		{name: "invalid octal digits", mode: "0789", wantErr: true},
		{name: "exceeds maximum allowed octal bits", mode: "10000", wantErr: true},
		{name: "maximum valid octal mode", mode: "7777", wantErr: false},
		{name: "standard directory octal mode", mode: "0755", wantErr: false},
		{name: "standard file octal mode", mode: "0644", wantErr: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := validateOctalMode(tc.mode)
			if (err != nil) != tc.wantErr {
				t.Fatalf("validateOctalMode(%q) error = %v, wantErr = %v", tc.mode, err, tc.wantErr)
			}
		})
	}
}
