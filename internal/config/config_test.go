package config

import (
	"testing"
)

func TestBundleStackByName(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		target    string
		wantFound bool
	}{
		{
			name:      "match existing stack",
			target:    "target-stack",
			wantFound: true,
		},
		{
			name:      "missing stack returns false",
			target:    "missing-stack",
			wantFound: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			bundle := Bundle{
				Stacks: []StackConfig{
					{Name: "target-stack", Enabled: true},
					{Name: "other-stack", Enabled: false},
				},
			}

			found, ok := bundle.StackByName(tc.target)
			if ok != tc.wantFound {
				t.Fatalf("expected found=%v, got %v", tc.wantFound, ok)
			}
			if ok && found.Name != tc.target {
				t.Fatalf("expected name %q, got %q", tc.target, found.Name)
			}
		})
	}
}

func TestBundleAngieStack(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name        string
		serviceName string
		stackName   string
		wantMatch   bool
	}{
		{
			name:      "default service name matches angie stack",
			stackName: DefaultAngieService,
			wantMatch: true,
		},
		{
			name:        "custom service name matches configured stack",
			serviceName: "proxy-edge",
			stackName:   "proxy-edge",
			wantMatch:   true,
		},
		{
			name:        "missing angie stack returns nil",
			serviceName: DefaultAngieService,
			stackName:   "other-app",
			wantMatch:   false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			bundle := Bundle{
				Global: GlobalConfig{Angie: GlobalAngieConfig{ServiceName: tc.serviceName}},
				Stacks: []StackConfig{{Name: tc.stackName, Enabled: true}},
			}
			got := bundle.AngieStack()
			if !tc.wantMatch {
				if got != nil {
					t.Fatalf("expected nil AngieStack, got %+v", got)
				}
				return
			}
			if got == nil || got.Name != tc.stackName {
				t.Fatalf("expected AngieStack %q, got %+v", tc.stackName, got)
			}
		})
	}
}

func TestStackConfigRemoteCountFor(t *testing.T) {
	t.Parallel()

	destCount := 14
	dest := DestinationConfig{
		Name:        "cloud-primary",
		RemoteCount: &destCount,
	}
	destNoCount := DestinationConfig{
		Name: "s3-plain",
	}

	stack := StackConfig{
		Name: "sample-stack",
		Retention: RetentionConfig{
			LocalCount:      1,
			RemoteCount:     5,
			RemoteOverrides: map[string]int{"s3-override": 2},
		},
	}

	cases := []struct {
		dest      *DestinationConfig
		name      string
		wantCount int
	}{
		{
			name:      "nil destination falls back to stack remote count",
			dest:      nil,
			wantCount: 5,
		},
		{
			name:      "stack remote override takes precedence",
			dest:      &DestinationConfig{Name: "s3-override", RemoteCount: &destCount},
			wantCount: 2,
		},
		{
			name:      "destination remote count used when no override",
			dest:      &dest,
			wantCount: 14,
		},
		{
			name:      "fallback to stack count when destination has no count",
			dest:      &destNoCount,
			wantCount: 5,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := stack.RemoteCountFor(tc.dest)
			if got != tc.wantCount {
				t.Fatalf("expected RemoteCountFor %d, got %d", tc.wantCount, got)
			}
		})
	}
}

func TestGlobalAngieConfigResolveStreamDir(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		cfg      GlobalAngieConfig
		expected string
	}{
		{
			name: "explicit stream dir",
			cfg: GlobalAngieConfig{
				StreamDir:  "/etc/angie/stream-custom",
				DomainsDir: "/etc/angie/domains",
			},
			expected: "/etc/angie/stream-custom",
		},
		{
			name: "derived from domains dir",
			cfg: GlobalAngieConfig{
				DomainsDir: "/etc/angie/domains",
			},
			expected: "/etc/angie/stream",
		},
		{
			name:     "empty when both absent",
			cfg:      GlobalAngieConfig{},
			expected: "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := tc.cfg.ResolveStreamDir()
			if got != tc.expected {
				t.Fatalf("expected stream dir %q, got %q", tc.expected, got)
			}
		})
	}
}
