package lvm

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"slices"
	"sync"
	"syscall"

	"github.com/webstudiobond/backup-recovery-orchestrator/internal/cmdexec"
)

type lvmCall struct {
	name string
	args []string
}

type fakeLVMRunner struct {
	failCommands map[string]error
	calls        []lvmCall
	mu           sync.Mutex
}

func (f *fakeLVMRunner) Run(_ context.Context, spec *cmdexec.Spec) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, lvmCall{name: spec.Name, args: slices.Clone(spec.Args)})
	if err, ok := f.failCommands[spec.Name]; ok {
		return err
	}
	return nil
}

func (f *fakeLVMRunner) Output(_ context.Context, _ *cmdexec.Spec) ([]byte, error) {
	return nil, nil
}

func mockFailingStatfs(_ string, _ *syscall.Statfs_t) error {
	return errors.New("simulated statfs error")
}

func mockZeroBlockStatfs(_ string, stat *syscall.Statfs_t) error {
	stat.Bsize = 0
	return nil
}

func mockAlwaysMissingStat(_ string) (os.FileInfo, error) {
	return nil, fs.ErrNotExist
}
