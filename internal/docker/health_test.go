package docker

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestWaitHealthySuccessAndTimeout(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	composePath := "/home/app-alpha/docker-compose.yaml"

	t.Run("ndjson becomes healthy on second poll", func(t *testing.T) {
		t.Parallel()

		runner := &fakeRunner{
			outputSeq: [][]byte{
				[]byte("{\"Service\":\"postgres\",\"State\":\"running\",\"Health\":\"starting\"}\n{\"Service\":\"app\",\"State\":\"running\",\"Health\":\"\"}\n"),
				[]byte("{\"Service\":\"postgres\",\"State\":\"running\",\"Health\":\"healthy\"}\n{\"Service\":\"app\",\"State\":\"running\",\"Health\":\"\"}\n"),
			},
		}
		client := &Client{Runner: runner, PollInterval: time.Millisecond}
		if err := client.WaitHealthy(ctx, composePath, nil, time.Second); err != nil {
			t.Fatalf("expected WaitHealthy to succeed, got %v", err)
		}
		if len(runner.calls) != 2 {
			t.Fatalf("expected 2 poll attempts, got %d", len(runner.calls))
		}
	})

	t.Run("json array scoped to target service", func(t *testing.T) {
		t.Parallel()

		runner := &fakeRunner{
			outputSeq: [][]byte{
				[]byte(`[{"Service":"database","State":"running","Health":"healthy"},{"Service":"app","State":"exited","Health":"unhealthy"}]`),
			},
		}
		client := &Client{Runner: runner, PollInterval: time.Millisecond}
		if err := client.WaitHealthy(ctx, composePath, []string{"database"}, time.Second); err != nil {
			t.Fatalf("expected scoped WaitHealthy to succeed, got %v", err)
		}
	})

	t.Run("timeout when service missing or unhealthy", func(t *testing.T) {
		t.Parallel()

		cases := []struct {
			err    error
			name   string
			want   string
			output []byte
		}{
			{
				name:   "unhealthy container",
				output: []byte(`{"Service":"postgres","State":"running","Health":"starting"}`),
				want:   "starting",
			},
			{
				name:   "missing target service",
				output: []byte(`{"Service":"redis","State":"running","Health":"healthy"}`),
				want:   "missing from docker compose ps",
			},
			{
				name: "command failure",
				err:  errors.New("daemon unavailable"),
				want: "daemon unavailable",
			},
		}

		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()

				runner := &fakeRunner{
					outputSeq:  [][]byte{tc.output},
					outputErrs: []error{tc.err},
				}
				client := &Client{Runner: runner, PollInterval: time.Millisecond}
				err := client.WaitHealthy(ctx, composePath, []string{"postgres"}, 15*time.Millisecond)
				if err == nil || !strings.Contains(err.Error(), tc.want) {
					t.Fatalf("expected timeout containing %q, got %v", tc.want, err)
				}
			})
		}
	})
}

func TestWaitHealthyValidationErrors(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client := NewClient(&fakeRunner{})

	t.Run("empty wait compose path", func(t *testing.T) {
		t.Parallel()
		if err := client.WaitHealthy(ctx, "", nil, time.Millisecond); err == nil {
			t.Fatal("expected validation error for empty wait compose path")
		}
	})
}

func TestWaitHealthyDefaultTimeouts(t *testing.T) {
	t.Parallel()

	runner := &fakeRunner{
		outputSeq: [][]byte{
			[]byte(`[{"Service":"web","State":"running","Health":"healthy"}]`),
		},
	}
	client := &Client{Runner: runner, PollInterval: 0}
	if err := client.WaitHealthy(context.Background(), "/home/app-default/docker-compose.yaml", []string{"web"}, 0); err != nil {
		t.Fatalf("expected WaitHealthy to succeed with zero timeout/interval defaults, got %v", err)
	}
}

func TestCheckHealthOnceErrors(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client := &Client{Runner: &fakeRunner{}}

	if _, _, err := client.checkHealthOnce(ctx, "", nil); err == nil {
		t.Fatal("expected error on empty compose file")
	}

	parseFailRunner := &fakeRunner{
		outputSeq: [][]byte{[]byte("not a valid json")},
	}
	parseFailClient := &Client{Runner: parseFailRunner}
	if _, _, err := parseFailClient.checkHealthOnce(ctx, "/home/app-default/docker-compose.yaml", nil); err == nil {
		t.Fatal("expected error on invalid json output")
	}
}

func TestParseComposePSJSONBranches(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name        string
		errContains string
		input       []byte
		wantCount   int
	}{
		{
			name:      "whitespace only returns nil",
			input:     []byte("  \t\n  "),
			wantCount: 0,
		},
		{
			name:        "invalid json array",
			input:       []byte("[{invalid json"),
			errContains: "parse compose ps JSON array",
		},
		{
			name:      "ndjson with blank lines",
			input:     []byte("  \n{\"Service\":\"s1\",\"State\":\"running\"}\n\n  \n{\"Service\":\"s2\",\"State\":\"running\"}\n"),
			wantCount: 2,
		},
		{
			name:        "ndjson invalid line",
			input:       []byte("{\"Service\":\"s1\",\"State\":\"running\"}\nnot-json\n"),
			errContains: "parse compose ps NDJSON line",
		},
		{
			name:        "scanner error token too long",
			input:       bytes.Repeat([]byte("x"), 70*1024),
			errContains: "scan compose ps output",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			res, err := parseComposePSJSON(tc.input)
			if tc.errContains != "" {
				if err == nil || !strings.Contains(err.Error(), tc.errContains) {
					t.Fatalf("expected error containing %q, got %v", tc.errContains, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(res) != tc.wantCount {
				t.Fatalf("expected %d items, got %d", tc.wantCount, len(res))
			}
		})
	}
}

func TestEvaluateContainersHealthBranches(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		wantReason string
		containers []composePSContainer
		targets    []string
		wantReady  bool
	}{
		{
			name:       "empty containers list",
			containers: nil,
			wantReady:  false,
			wantReason: "no containers reported",
		},
		{
			name: "non-running container state",
			containers: []composePSContainer{
				{Service: "worker", State: "stopped", Health: ""},
			},
			targets:    []string{"worker"},
			wantReady:  false,
			wantReason: "state is \"stopped\"",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			ready, reason, err := evaluateContainersHealth(tc.containers, tc.targets)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if ready != tc.wantReady {
				t.Fatalf("expected ready=%v, got %v", tc.wantReady, ready)
			}
			if !strings.Contains(reason, tc.wantReason) {
				t.Fatalf("expected reason containing %q, got %q", tc.wantReason, reason)
			}
		})
	}
}
