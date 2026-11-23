package cmdexec

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestOSRunnerRunAndOutput(t *testing.T) {
	t.Parallel()

	runner := OSRunner{}
	ctx := context.Background()

	cases := []struct {
		name  string
		asOut bool
	}{
		{name: "Run captures stdout", asOut: false},
		{name: "Output returns stdout", asOut: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			spec := Spec{
				Name: "tar",
				Args: []string{"--version"},
			}

			var outStr string
			if tc.asOut {
				out, err := runner.Output(ctx, &spec)
				if err != nil {
					t.Fatalf("unexpected error in Output: %v", err)
				}
				outStr = string(out)
			} else {
				var stdout bytes.Buffer
				spec.Stdout = &stdout
				if err := runner.Run(ctx, &spec); err != nil {
					t.Fatalf("unexpected error in Run: %v", err)
				}
				outStr = stdout.String()
			}

			if !strings.Contains(strings.ToLower(outStr), "tar") {
				t.Fatalf("expected tar version output, got %q", outStr)
			}
		})
	}
}

func TestOSRunnerErrors(t *testing.T) {
	t.Parallel()

	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()

	runner := OSRunner{}

	cases := []struct {
		ctx         context.Context
		spec        *Spec
		name        string
		errContains string
	}{
		{
			ctx:         context.Background(),
			spec:        nil,
			name:        "nil spec",
			errContains: "must not be empty",
		},
		{
			ctx:         context.Background(),
			spec:        &Spec{Name: ""},
			name:        "empty command name",
			errContains: "must not be empty",
		},
		{
			ctx:         context.Background(),
			spec:        &Spec{Name: "sh", Args: []string{"-c", "echo blocked"}},
			name:        "disallowed executable",
			errContains: "not in allowlist",
		},
		{
			ctx:         canceledCtx,
			spec:        &Spec{Name: "docker", Args: []string{"version"}},
			name:        "canceled context",
			errContains: "context canceled",
		},
		{
			ctx:         context.Background(),
			spec:        &Spec{Name: "tar", Args: []string{"-tf", "/nonexistent-bro-test-archive.tar"}},
			name:        "non-zero exit includes stderr",
			errContains: "failed",
		},
		{
			ctx: context.Background(),
			spec: &Spec{
				Name:   "zstd",
				Args:   []string{"-t", "/nonexistent-custom-stderr-archive.zst"},
				Stderr: &bytes.Buffer{},
				Env:    []string{"TEST_ENV_VAR=1"},
			},
			name:        "custom stderr and env with non-zero exit",
			errContains: "failed",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if err := runner.Run(tc.ctx, tc.spec); err == nil || !strings.Contains(err.Error(), tc.errContains) {
				t.Fatalf("expected Run error containing %q for %s, got %v", tc.errContains, tc.name, err)
			}
			if _, err := runner.Output(tc.ctx, tc.spec); err == nil || !strings.Contains(err.Error(), tc.errContains) {
				t.Fatalf("expected Output error containing %q for %s, got %v", tc.errContains, tc.name, err)
			}
		})
	}
}
