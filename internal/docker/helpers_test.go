package docker

import (
	"bytes"
	"context"
	"slices"
	"sync"

	"github.com/webstudiobond/backup-recovery-orchestrator/internal/cmdexec"
)

type recordedCall struct {
	name  string
	stdin string
	args  []string
}

type fakeRunner struct {
	runErr     error
	outputSeq  [][]byte
	outputErrs []error
	calls      []recordedCall
	mu         sync.Mutex
	outIdx     int
}

func (f *fakeRunner) Run(_ context.Context, spec *cmdexec.Spec) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	var stdinStr string
	if spec.Stdin != nil {
		var buf bytes.Buffer
		if _, err := buf.ReadFrom(spec.Stdin); err != nil {
			return err
		}
		stdinStr = buf.String()
	}

	f.calls = append(f.calls, recordedCall{
		name:  spec.Name,
		args:  slices.Clone(spec.Args),
		stdin: stdinStr,
	})

	if spec.Stdout != nil {
		if _, err := spec.Stdout.Write([]byte("sql-stream")); err != nil {
			return err
		}
	}
	return f.runErr
}

func (f *fakeRunner) Output(_ context.Context, spec *cmdexec.Spec) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.calls = append(f.calls, recordedCall{
		name: spec.Name,
		args: slices.Clone(spec.Args),
	})

	idx := f.outIdx
	f.outIdx++
	if len(f.outputErrs) > 0 {
		errIdx := min(idx, len(f.outputErrs)-1)
		if f.outputErrs[errIdx] != nil {
			return nil, f.outputErrs[errIdx]
		}
	}
	if idx < len(f.outputSeq) {
		return f.outputSeq[idx], nil
	}
	if len(f.outputSeq) > 0 {
		return f.outputSeq[len(f.outputSeq)-1], nil
	}
	return nil, nil
}
