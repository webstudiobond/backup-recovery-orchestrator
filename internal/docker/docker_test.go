package docker

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
)

func TestClientComposeCommands(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	composePath := "/home/site1/docker-compose.yaml"

	cases := []struct {
		invoke      func(c *Client) error
		name        string
		wantSubArgs []string
	}{
		{
			name:        "up selected services",
			invoke:      func(c *Client) error { return c.Up(ctx, composePath, "db", "redis") },
			wantSubArgs: []string{"up", "-d", "db", "redis"},
		},
		{
			name:        "down",
			invoke:      func(c *Client) error { return c.Down(ctx, composePath) },
			wantSubArgs: []string{"down"},
		},
		{
			name:        "restart all",
			invoke:      func(c *Client) error { return c.Restart(ctx, composePath) },
			wantSubArgs: []string{"restart"},
		},
		{
			name:        "kill with signal",
			invoke:      func(c *Client) error { return c.Kill(ctx, composePath, "daemon", "USR2") },
			wantSubArgs: []string{"kill", "-s", "USR2", "daemon"},
		},
		{
			name: "exec with env",
			invoke: func(c *Client) error {
				return c.Exec(ctx, composePath, "mariadb", []string{"MYSQL_PWD=secret"}, []string{"mariadb-dump", "-u", "app"}, nil, nil)
			},
			wantSubArgs: []string{"exec", "-T", "-e", "MYSQL_PWD=secret", "mariadb", "mariadb-dump", "-u", "app"},
		},
		{
			name: "run wp-cli",
			invoke: func(c *Client) error {
				return c.Run(ctx, composePath, "wp-cli", nil, []string{"cache", "flush"}, nil, nil)
			},
			wantSubArgs: []string{"run", "--rm", "-T", "--no-deps", "wp-cli", "cache", "flush"},
		},
		{
			name: "run with env",
			invoke: func(c *Client) error {
				return c.Run(ctx, composePath, "node", []string{"NODE_ENV=production"}, []string{"npm", "start"}, nil, nil)
			},
			wantSubArgs: []string{"run", "--rm", "-T", "--no-deps", "-e", "NODE_ENV=production", "node", "npm", "start"},
		},
		{
			name:        "check angie config",
			invoke:      func(c *Client) error { return c.CheckAngieConfig(ctx, composePath, "") },
			wantSubArgs: []string{"exec", "-T", "angie", "angie", "-t"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			runner := &fakeRunner{}
			client := NewClient(runner)
			if err := tc.invoke(client); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(runner.calls) != 1 {
				t.Fatalf("expected 1 call, got %d", len(runner.calls))
			}
			got := runner.calls[0]
			wantArgs := append([]string{"compose", "-f", composePath}, tc.wantSubArgs...)
			if got.name != "docker" || !slices.Equal(got.args, wantArgs) {
				t.Fatalf("expected docker %v, got %s %v", wantArgs, got.name, got.args)
			}
		})
	}
}

func TestClientValidationErrors(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client := NewClient(&fakeRunner{})

	cases := []struct {
		fn   func() error
		name string
	}{
		{name: "empty compose path", fn: func() error { return client.Down(ctx, "") }},
		{name: "empty kill service", fn: func() error { return client.Kill(ctx, "/home/a/docker-compose.yaml", "", "USR2") }},
		{name: "empty exec args", fn: func() error { return client.Exec(ctx, "/home/a/docker-compose.yaml", "app", nil, nil, nil, nil) }},
		{name: "empty run service", fn: func() error {
			return client.Run(ctx, "/home/a/docker-compose.yaml", "", nil, []string{"echo"}, nil, nil)
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if err := tc.fn(); err == nil {
				t.Fatalf("expected validation error for %s", tc.name)
			}
		})
	}
}

func TestNewClientDefaults(t *testing.T) {
	t.Parallel()

	c := NewClient(nil)
	if c.Runner == nil {
		t.Fatal("expected default runner to be initialized")
	}
	if c.PollInterval != defaultPollInterval {
		t.Fatalf("expected poll interval %v, got %v", defaultPollInterval, c.PollInterval)
	}
}

func TestClientComposeRunError(t *testing.T) {
	t.Parallel()

	runner := &fakeRunner{runErr: errors.New("simulated exec failure")}
	client := NewClient(runner)

	err := client.Down(context.Background(), "/home/site1/docker-compose.yaml")
	if err == nil || !strings.Contains(err.Error(), "simulated exec failure") {
		t.Fatalf("expected simulated failure error, got %v", err)
	}
}
