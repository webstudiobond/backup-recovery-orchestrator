package docker

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/webstudiobond/backup-recovery-orchestrator/internal/cmdexec"
)

const defaultHealthTimeout = 180 * time.Second

type composePSContainer struct {
	Service string `json:"Service"`
	Name    string `json:"Name"`
	State   string `json:"State"`
	Health  string `json:"Health"`
}

// WaitHealthy polls docker compose ps JSON output until target services are running and healthy or timeout expires.
func (c *Client) WaitHealthy(ctx context.Context, composeFile string, services []string, timeout time.Duration) error {
	if strings.TrimSpace(composeFile) == "" {
		return errors.New("compose file path must not be empty")
	}
	if timeout <= 0 {
		timeout = defaultHealthTimeout
	}

	interval := c.PollInterval
	if interval <= 0 {
		interval = defaultPollInterval
	}

	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var lastReason string
	for {
		ready, reason, err := c.checkHealthOnce(waitCtx, composeFile, services)
		if err == nil && ready {
			return nil
		}
		if err != nil {
			lastReason = err.Error()
		} else {
			lastReason = reason
		}

		timer := time.NewTimer(interval)
		select {
		case <-waitCtx.Done():
			timer.Stop()
			return fmt.Errorf("health check timed out after %s (%s): %w", timeout, lastReason, waitCtx.Err())
		case <-timer.C:
		}
	}
}

func (c *Client) checkHealthOnce(ctx context.Context, composeFile string, services []string) (ready bool, reason string, err error) {
	args, argErr := composeArgs(composeFile, []string{"ps", "--format", "json"})
	if argErr != nil {
		return false, "", argErr
	}

	out, outErr := c.Runner.Output(ctx, &cmdexec.Spec{
		Name: "docker",
		Args: args,
	})
	if outErr != nil {
		return false, "", fmt.Errorf("docker compose ps: %w", outErr)
	}

	containers, parseErr := parseComposePSJSON(out)
	if parseErr != nil {
		return false, "", parseErr
	}

	return evaluateContainersHealth(containers, services)
}

func parseComposePSJSON(raw []byte) ([]composePSContainer, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil, nil
	}

	if trimmed[0] == '[' {
		var list []composePSContainer
		if err := json.Unmarshal(trimmed, &list); err != nil {
			return nil, fmt.Errorf("parse compose ps JSON array: %w", err)
		}
		return list, nil
	}

	containers := make([]composePSContainer, 0, 4)
	scanner := bufio.NewScanner(bytes.NewReader(trimmed))
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var item composePSContainer
		if err := json.Unmarshal(line, &item); err != nil {
			return nil, fmt.Errorf("parse compose ps NDJSON line: %w", err)
		}
		containers = append(containers, item)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan compose ps output: %w", err)
	}
	return containers, nil
}

func evaluateContainersHealth(containers []composePSContainer, targetServices []string) (ready bool, reason string, err error) {
	if len(containers) == 0 {
		return false, "no containers reported by docker compose ps", nil
	}

	targetSet := make(map[string]bool, len(targetServices))
	for _, svc := range targetServices {
		targetSet[svc] = false
	}

	for i := range containers {
		item := &containers[i]
		if len(targetSet) > 0 {
			if _, wanted := targetSet[item.Service]; !wanted {
				continue
			}
			targetSet[item.Service] = true
		}

		if item.State != "running" {
			return false, fmt.Sprintf("service %q state is %q", item.Service, item.State), nil
		}
		if item.Health != "" && item.Health != "healthy" {
			return false, fmt.Sprintf("service %q health is %q", item.Service, item.Health), nil
		}
	}

	for svc, seen := range targetSet {
		if !seen {
			return false, fmt.Sprintf("service %q missing from docker compose ps", svc), nil
		}
	}

	return true, "", nil
}
