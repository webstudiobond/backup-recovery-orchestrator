package storage

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/webstudiobond/backup-recovery-orchestrator/internal/cmdexec"
	"github.com/webstudiobond/backup-recovery-orchestrator/internal/mount"
)

type fakeStorageRunner struct {
	runErr    error
	outputErr error
	failMatch string
	onOutput  func()
	lsfSeq    [][]byte
	calls     [][]string
	mu        sync.Mutex
	lsfIdx    int
}

func (f *fakeStorageRunner) Run(_ context.Context, spec *cmdexec.Spec) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, append([]string{spec.Name}, slices.Clone(spec.Args)...))
	if f.runErr != nil {
		return f.runErr
	}
	if f.failMatch != "" && strings.Contains(strings.Join(spec.Args, " "), f.failMatch) {
		return errors.New("simulated failure for " + f.failMatch)
	}
	return nil
}

func (f *fakeStorageRunner) Output(_ context.Context, spec *cmdexec.Spec) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, append([]string{spec.Name}, slices.Clone(spec.Args)...))
	if f.onOutput != nil {
		f.onOutput()
	}
	if f.outputErr != nil {
		return nil, f.outputErr
	}
	if len(f.lsfSeq) == 0 {
		return nil, nil
	}
	idx := min(f.lsfIdx, len(f.lsfSeq)-1)
	f.lsfIdx++
	return f.lsfSeq[idx], nil
}

func newMockVerifier() *mount.Verifier {
	return &mount.Verifier{
		Runner:       &fakeStorageRunner{},
		StatFSType:   func(_ string) (int64, error) { return 0x65735546, nil },
		PollInterval: time.Millisecond,
	}
}

func createFakeArchive(t *testing.T, dir, name, payload string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir %q: %v", dir, err)
	}
	fullPath := filepath.Join(dir, name)
	if err := os.WriteFile(fullPath, []byte(payload), 0o600); err != nil {
		t.Fatalf("write %q: %v", fullPath, err)
	}
	return fullPath
}

func makeReadOnlyDir(t *testing.T, dir string) {
	t.Helper()
	readOnlyPerm := os.FileMode(0o500)
	writablePerm := os.FileMode(0o700)
	if err := os.Chmod(dir, readOnlyPerm); err != nil {
		t.Fatalf("chmod 0500: %v", err)
	}
	t.Cleanup(func() {
		if cErr := os.Chmod(dir, writablePerm); cErr != nil {
			t.Errorf("cleanup chmod: %v", cErr)
		}
	})
}

func failingRootClose(root *os.Root) error {
	if err := root.Close(); err != nil {
		return errors.Join(errors.New("simulated root close error"), err)
	}
	return errors.New("simulated root close error")
}

func failingOSRemove(_ string) error {
	return errors.New("simulated os remove error")
}
