package database

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/webstudiobond/backup-recovery-orchestrator/internal/cmdexec"
)

type dbCall struct {
	name  string
	stdin string
	args  []string
	env   []string
}

type fakeDBRunner struct {
	onRun     func(*cmdexec.Spec)
	failOnCmd string
	dumpBytes []byte
	calls     []dbCall
	mu        sync.Mutex
	failAll   bool
}

func (f *fakeDBRunner) Run(_ context.Context, spec *cmdexec.Spec) error {
	var stdinData []byte
	if spec.Stdin != nil {
		var err error
		stdinData, err = readAllWithLimit(spec.Stdin)
		if err != nil {
			return err
		}
	}

	f.mu.Lock()
	if f.onRun != nil {
		f.onRun(spec)
	}
	f.calls = append(f.calls, dbCall{
		name:  spec.Name,
		args:  slices.Clone(spec.Args),
		env:   slices.Clone(spec.Env),
		stdin: string(stdinData),
	})
	failCmd := f.failOnCmd
	failAll := f.failAll
	dumpPayload := f.dumpBytes
	f.mu.Unlock()

	if failAll || (failCmd != "" && spec.Name == failCmd) {
		return errors.New("simulated failure")
	}
	if spec.Stdout == nil {
		return nil
	}

	payload := dumpPayload
	if len(stdinData) > 0 {
		payload = stdinData
	}
	if len(payload) > 0 {
		if _, err := spec.Stdout.Write(payload); err != nil {
			return err
		}
	}
	return nil
}

func (f *fakeDBRunner) Output(_ context.Context, _ *cmdexec.Spec) ([]byte, error) {
	return nil, nil
}

func readAllWithLimit(r interface{ Read([]byte) (int, error) }) ([]byte, error) {
	var buf bytes.Buffer
	_, err := buf.ReadFrom(r)
	return buf.Bytes(), err
}

func containsArgFragment(calls []dbCall, needle string) bool {
	for _, call := range calls {
		for _, arg := range call.args {
			if strings.Contains(arg, needle) {
				return true
			}
		}
		for _, env := range call.env {
			if strings.Contains(env, needle) {
				return true
			}
		}
	}
	return false
}

func writeHostSecretFixtures(t *testing.T, secretDir string) (userFile, dbFile, authFile string) {
	t.Helper()
	userFile = filepath.Join(secretDir, "user")
	dbFile = filepath.Join(secretDir, "db")
	authFile = filepath.Join(secretDir, "auth")
	for path, val := range map[string]string{
		userFile: "appuser\n",
		dbFile:   "appdb\n",
		authFile: "s3cr3t\n",
	} {
		if err := os.WriteFile(path, []byte(val), 0o600); err != nil {
			t.Fatalf("write host secret %q: %v", path, err)
		}
	}
	return userFile, dbFile, authFile
}

type mockPipeReader struct {
	pipeReader
	closeErr error
}

func (m *mockPipeReader) CloseWithError(err error) error {
	if closeErr := m.pipeReader.CloseWithError(err); closeErr != nil {
		return fmt.Errorf("close pipe reader: %w", closeErr)
	}
	return m.closeErr
}

type mockPipeWriter struct {
	pipeWriter
	closeErr error
}

func (m *mockPipeWriter) CloseWithError(err error) error {
	if closeErr := m.pipeWriter.CloseWithError(err); closeErr != nil {
		return fmt.Errorf("close pipe writer: %w", closeErr)
	}
	return m.closeErr
}

func mockWriterFailingPipe() (pipeReader, pipeWriter) {
	r, w := io.Pipe()
	return r, &mockPipeWriter{pipeWriter: w, closeErr: errors.New("simulated pipe writer close failure")}
}

func mockReaderFailingPipe() (pipeReader, pipeWriter) {
	r, w := io.Pipe()
	return &mockPipeReader{pipeReader: r, closeErr: errors.New("simulated pipe reader close failure")}, w
}

func mockFailingCloseFile(f *os.File) error {
	if err := f.Close(); err != nil {
		return fmt.Errorf("close fixture: %w", err)
	}
	return errors.New("simulated close failure")
}
