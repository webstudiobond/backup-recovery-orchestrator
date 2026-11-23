package hooks

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/webstudiobond/backup-recovery-orchestrator/internal/cmdexec"
)

type recordedDocker struct {
	runErr error
	args   [][]string
	mu     sync.Mutex
}

func (f *recordedDocker) Run(_ context.Context, spec *cmdexec.Spec) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.args = append(f.args, slices.Clone(spec.Args))
	return f.runErr
}

func (f *recordedDocker) Output(_ context.Context, _ *cmdexec.Spec) ([]byte, error) {
	return nil, nil
}

func mockFailingAbs(_ string) (string, error) {
	return "", errors.New("simulated abs failure")
}

func mockFailingRemoveAll(_ string) error {
	return errors.New("simulated remove failure")
}

func mockFailingTargetPathAbs(p string) (string, error) {
	if strings.Contains(p, "fail_target") {
		return "", errors.New("simulated target abs error")
	}
	return filepath.Abs(p)
}
