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

func TestBundleReverseProxyStack(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name              string
		reverseProxyStack string
		stackName         string
		wantMatch         bool
	}{
		{
			name:              "empty reverse_proxy_stack returns nil",
			reverseProxyStack: "",
			stackName:         "angie",
			wantMatch:         false,
		},
		{
			name:              "custom stack name matches configured reverse proxy stack",
			reverseProxyStack: "gateway-proxy",
			stackName:         "gateway-proxy",
			wantMatch:         true,
		},
		{
			name:              "missing reverse proxy stack returns nil",
			reverseProxyStack: "edge-ingress",
			stackName:         "other-app",
			wantMatch:         false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			bundle := Bundle{
				Global: GlobalConfig{ReverseProxyStack: tc.reverseProxyStack},
				Stacks: []StackConfig{{Name: tc.stackName, Enabled: true}},
			}
			got := bundle.ReverseProxyStack()
			if !tc.wantMatch {
				if got != nil {
					t.Fatalf("expected nil ReverseProxyStack, got %+v", got)
				}
				return
			}
			if got == nil || got.Name != tc.stackName {
				t.Fatalf("expected ReverseProxyStack %q, got %+v", tc.stackName, got)
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

func TestStackReverseProxyHasAssets(t *testing.T) {
	t.Parallel()

	var nilCfg *StackReverseProxyConfig
	if nilCfg.HasAssets() {
		t.Fatal("expected nil config HasAssets() = false")
	}
	if (&StackReverseProxyConfig{}).HasAssets() {
		t.Fatal("expected empty config HasAssets() = false")
	}
	if !(&StackReverseProxyConfig{DomainConfs: []string{"app.example.com.conf"}}).HasAssets() {
		t.Fatal("expected DomainConfs HasAssets() = true")
	}
	if !(&StackReverseProxyConfig{StreamConfs: []string{"app.example.com.inc"}}).HasAssets() {
		t.Fatal("expected StreamConfs HasAssets() = true")
	}
	if !(&StackReverseProxyConfig{ACMENames: []string{"portal"}}).HasAssets() {
		t.Fatal("expected ACMENames HasAssets() = true")
	}
}
