// Package docker wraps Docker Compose lifecycle, execution, and health-polling commands.
package docker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/webstudiobond/backup-recovery-orchestrator/internal/cmdexec"
)

const defaultPollInterval = 2 * time.Second

// Client executes Docker Compose commands for a stack using an injected command runner.
type Client struct {
	Runner       cmdexec.Runner
	PollInterval time.Duration
}

// NewClient constructs a Docker Compose client using OSRunner when runner is nil.
func NewClient(runner cmdexec.Runner) *Client {
	if runner == nil {
		runner = cmdexec.OSRunner{}
	}
	return &Client{
		Runner:       runner,
		PollInterval: defaultPollInterval,
	}
}

// Up starts all services or the selected subset in detached mode.
func (c *Client) Up(ctx context.Context, composeFile string, services ...string) error {
	args := make([]string, 0, 2+len(services))
	args = append(args, "up", "-d")
	args = append(args, services...)
	return c.runCompose(ctx, composeFile, args, nil, nil)
}

// Down stops and removes containers for the given compose file.
func (c *Client) Down(ctx context.Context, composeFile string) error {
	return c.runCompose(ctx, composeFile, []string{"down"}, nil, nil)
}

// Restart restarts all services or the selected subset for the given compose file.
func (c *Client) Restart(ctx context.Context, composeFile string, services ...string) error {
	args := make([]string, 0, 1+len(services))
	args = append(args, "restart")
	args = append(args, services...)
	return c.runCompose(ctx, composeFile, args, nil, nil)
}

// Kill delivers a Unix signal (such as USR2) to a running compose service container.
func (c *Client) Kill(ctx context.Context, composeFile, service, signal string) error {
	if strings.TrimSpace(service) == "" || strings.TrimSpace(signal) == "" {
		return errors.New("service and signal must not be empty")
	}
	return c.runCompose(ctx, composeFile, []string{"kill", "-s", signal, service}, nil, nil)
}

// Exec runs a non-interactive command inside a running compose service container.
func (c *Client) Exec(ctx context.Context, composeFile, service string, envVars, cmdArgs []string, stdin io.Reader, stdout io.Writer) error {
	if strings.TrimSpace(service) == "" || len(cmdArgs) == 0 {
		return errors.New("service and command arguments must not be empty")
	}
	args := make([]string, 0, 3+len(envVars)*2+len(cmdArgs))
	args = append(args, "exec", "-T")
	for _, entry := range envVars {
		args = append(args, "-e", entry)
	}
	args = append(args, service)
	args = append(args, cmdArgs...)
	return c.runCompose(ctx, composeFile, args, stdin, stdout)
}

// Run executes a one-off container command without starting linked dependency services.
func (c *Client) Run(ctx context.Context, composeFile, service string, envVars, cmdArgs []string, stdin io.Reader, stdout io.Writer) error {
	if strings.TrimSpace(service) == "" || len(cmdArgs) == 0 {
		return errors.New("service and command arguments must not be empty")
	}
	args := make([]string, 0, 5+len(envVars)*2+len(cmdArgs))
	args = append(args, "run", "--rm", "-T", "--no-deps")
	for _, entry := range envVars {
		args = append(args, "-e", entry)
	}
	args = append(args, service)
	args = append(args, cmdArgs...)
	return c.runCompose(ctx, composeFile, args, stdin, stdout)
}

// CheckAngieConfig validates Angie configuration syntax without reloading or interrupting active proxy traffic.
func (c *Client) CheckAngieConfig(ctx context.Context, composeFile, serviceName string) error {
	if strings.TrimSpace(serviceName) == "" {
		serviceName = "angie"
	}
	return c.Exec(ctx, composeFile, serviceName, nil, []string{"angie", "-t"}, nil, nil)
}

func composeArgs(composeFile string, subArgs []string) ([]string, error) {
	if strings.TrimSpace(composeFile) == "" {
		return nil, errors.New("compose file path must not be empty")
	}
	args := make([]string, 0, 3+len(subArgs))
	args = append(args, "compose", "-f", composeFile)
	args = append(args, subArgs...)
	return args, nil
}

func (c *Client) runCompose(ctx context.Context, composeFile string, subArgs []string, stdin io.Reader, stdout io.Writer) error {
	args, err := composeArgs(composeFile, subArgs)
	if err != nil {
		return err
	}
	if runErr := c.Runner.Run(ctx, &cmdexec.Spec{
		Name:   "docker",
		Args:   args,
		Stdin:  stdin,
		Stdout: stdout,
	}); runErr != nil {
		return fmt.Errorf("docker %s: %w", strings.Join(args, " "), runErr)
	}
	return nil
}
