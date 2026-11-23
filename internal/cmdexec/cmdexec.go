// Package cmdexec provides a testable abstraction over external process execution with context propagation.
package cmdexec

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

// Spec describes an external command invocation and its optional working directory, environment overrides, and I/O streams.
type Spec struct {
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
	Name   string
	Dir    string
	Args   []string
	Env    []string
}

// Runner executes external commands so higher-level packages remain decoupled from os/exec in unit tests.
type Runner interface {
	Run(ctx context.Context, spec *Spec) error
	Output(ctx context.Context, spec *Spec) ([]byte, error)
}

// OSRunner executes allowlisted commands against the host operating system using os/exec.
type OSRunner struct{}

// Run executes the command described by spec and includes captured stderr when no custom Stderr writer is provided.
func (OSRunner) Run(ctx context.Context, spec *Spec) error {
	if err := validateSpec(ctx, spec); err != nil {
		return err
	}

	cmd, err := buildCommand(ctx, spec)
	if err != nil {
		return err
	}

	var stderrBuf bytes.Buffer
	if spec.Stderr != nil {
		cmd.Stderr = spec.Stderr
	} else {
		cmd.Stderr = &stderrBuf
	}

	if runErr := cmd.Run(); runErr != nil {
		errMsg := strings.TrimSpace(stderrBuf.String())
		if errMsg != "" {
			return fmt.Errorf("command %s %s failed: %w: %s", spec.Name, strings.Join(spec.Args, " "), runErr, errMsg)
		}
		return fmt.Errorf("command %s %s failed: %w", spec.Name, strings.Join(spec.Args, " "), runErr)
	}
	return nil
}

// Output executes the command described by spec and returns its standard output bytes.
func (OSRunner) Output(ctx context.Context, spec *Spec) ([]byte, error) {
	if err := validateSpec(ctx, spec); err != nil {
		return nil, err
	}

	cmd, err := buildCommand(ctx, spec)
	if err != nil {
		return nil, err
	}

	var stdoutBuf bytes.Buffer
	var stderrBuf bytes.Buffer
	cmd.Stdout = &stdoutBuf
	if spec.Stderr != nil {
		cmd.Stderr = spec.Stderr
	} else {
		cmd.Stderr = &stderrBuf
	}

	if runErr := cmd.Run(); runErr != nil {
		errMsg := strings.TrimSpace(stderrBuf.String())
		if errMsg != "" {
			return nil, fmt.Errorf("command %s %s failed: %w: %s", spec.Name, strings.Join(spec.Args, " "), runErr, errMsg)
		}
		return nil, fmt.Errorf("command %s %s failed: %w", spec.Name, strings.Join(spec.Args, " "), runErr)
	}
	return stdoutBuf.Bytes(), nil
}

func validateSpec(ctx context.Context, spec *Spec) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("context canceled before executing command: %w", err)
	}
	if spec == nil || strings.TrimSpace(spec.Name) == "" {
		return errors.New("command name must not be empty")
	}
	return nil
}

func buildCommand(ctx context.Context, spec *Spec) (*exec.Cmd, error) {
	builder, allowed := allowedCommands[spec.Name]
	if !allowed {
		return nil, fmt.Errorf("executable %q is not in allowlist", spec.Name)
	}

	cmd := builder(ctx)
	cmd.Args = append(cmd.Args, spec.Args...)
	cmd.Dir = spec.Dir
	cmd.Stdin = spec.Stdin
	cmd.Stdout = spec.Stdout
	if len(spec.Env) > 0 {
		cmd.Env = append(os.Environ(), spec.Env...)
	}
	return cmd, nil
}
