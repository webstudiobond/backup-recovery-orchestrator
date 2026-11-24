package hooks

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

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

func mockFailingOpenRoot(_ string) (*os.Root, error) {
	return nil, errors.New("simulated open root failure")
}

func mockDifferentSameFile(_, _ os.FileInfo) bool {
	return false
}

func mockFailingRootLstat(_ *os.Root, _ string) (os.FileInfo, error) {
	return nil, errors.New("simulated root lstat error")
}

func assertFilesExist(t *testing.T, paths ...string) {
	t.Helper()
	for _, p := range paths {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("expected %q to remain intact: %v", p, err)
		}
	}
}

func mockFailingTargetPathAbs(p string) (string, error) {
	if strings.Contains(p, "fail_target") {
		return "", errors.New("simulated target abs error")
	}
	return filepath.Abs(p)
}

func mockFailingRootClose(_ *os.Root) error {
	return errors.New("simulated root close error")
}

func mockFailingRootStat(_ *os.Root, _ string) (os.FileInfo, error) {
	return nil, errors.New("simulated root stat error")
}

func mockFailingRootOpenRoot(_ *os.Root, _ string) (*os.Root, error) {
	return nil, errors.New("simulated root openroot error")
}

func mockFailingRootOpenFile(_ *os.Root, _ string, _ int, _ os.FileMode) (*os.File, error) {
	return nil, errors.New("simulated root openfile error")
}

func mockFailingRootRemoveAll(_ *os.Root, _ string) error {
	return errors.New("simulated root removeall error")
}

func mockFailingFileStat(_ *os.File) (os.FileInfo, error) {
	return nil, errors.New("simulated file stat error")
}
