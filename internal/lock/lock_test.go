package lock

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAcquireAndRelease(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	lockPath := filepath.Join(tempDir, "nested", "bro.lock")
	ctx := context.Background()

	first, err := Acquire(ctx, lockPath)
	if err != nil {
		t.Fatalf("unexpected error acquiring first lock: %v", err)
	}
	if first.Path() != lockPath {
		t.Fatalf("expected path %q, got %q", lockPath, first.Path())
	}

	second, lockErr := Acquire(ctx, lockPath)
	if !errors.Is(lockErr, ErrLocked) {
		if second != nil {
			if relErr := second.Release(); relErr != nil {
				t.Errorf("failed to release unexpected second lock: %v", relErr)
			}
		}
		t.Fatalf("expected ErrLocked on concurrent acquire, got %v", lockErr)
	}

	if relErr := first.Release(); relErr != nil {
		t.Fatalf("unexpected error releasing first lock: %v", relErr)
	}
	if relErr := first.Release(); relErr != nil {
		t.Fatalf("expected idempotent Release, got %v", relErr)
	}

	reacquired, reErr := Acquire(ctx, lockPath)
	if reErr != nil {
		t.Fatalf("unexpected error reacquiring lock after release: %v", reErr)
	}
	if relErr := reacquired.Release(); relErr != nil {
		t.Fatalf("unexpected error releasing reacquired lock: %v", relErr)
	}
}

func TestAcquireValidationAndContext(t *testing.T) {
	t.Parallel()

	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()

	cases := []struct {
		ctx  context.Context
		name string
		path string
	}{
		{
			ctx:  context.Background(),
			name: "empty path",
			path: "",
		},
		{
			ctx:  context.Background(),
			name: "dot path",
			path: ".",
		},
		{
			ctx:  canceledCtx,
			name: "canceled context",
			path: filepath.Join(t.TempDir(), "bro.lock"),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			held, err := Acquire(tc.ctx, tc.path)
			if err == nil {
				if held != nil {
					if relErr := held.Release(); relErr != nil {
						t.Errorf("failed to release unexpected lock: %v", relErr)
					}
				}
				t.Fatalf("expected error for %s, got nil", tc.name)
			}
		})
	}
}

func TestAcquireFilesystemFailures(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "file_blocking_dir")
	if err := os.WriteFile(filePath, []byte("data"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}

	cases := []struct {
		name        string
		path        string
		errContains string
	}{
		{
			name:        "mkdir failure on file parent",
			path:        filepath.Join(filePath, "nested", "fail.lock"),
			errContains: "create lock directory",
		},
		{
			name:        "open failure on existing directory",
			path:        tmpDir,
			errContains: "open lock file",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			held, err := Acquire(context.Background(), tc.path)
			if err == nil {
				if held != nil {
					if relErr := held.Release(); relErr != nil {
						t.Errorf("cleanup release: %v", relErr)
					}
				}
				t.Fatalf("expected error containing %q, got nil", tc.errContains)
			}
			if !strings.Contains(err.Error(), tc.errContains) {
				t.Fatalf("expected error containing %q, got %v", tc.errContains, err)
			}
		})
	}
}

func TestAcquireMockErrors(t *testing.T) {
	origFlock := flockFn
	origClose := closeFile
	origMaxInt := maxIntLimit
	t.Cleanup(func() {
		flockFn = origFlock
		closeFile = origClose
		maxIntLimit = origMaxInt
	})

	cases := []struct {
		flockSetup  func(int, int) error
		name        string
		errContains string
		maxInt      uintptr
	}{
		{
			name:        "acquire fd exceeds max int",
			maxInt:      0,
			errContains: "file descriptor exceeds int range",
		},
		{
			name:        "acquire flock failure",
			maxInt:      origMaxInt,
			flockSetup:  mockFailingFlock,
			errContains: "acquire flock on",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			flockFn = origFlock
			closeFile = origClose
			maxIntLimit = tc.maxInt
			if tc.flockSetup != nil {
				flockFn = tc.flockSetup
			}

			lockPath := filepath.Join(t.TempDir(), "test.lock")
			held, err := Acquire(context.Background(), lockPath)
			if err == nil {
				if held != nil {
					if relErr := held.Release(); relErr != nil {
						t.Errorf("cleanup release: %v", relErr)
					}
				}
				t.Fatalf("expected acquire error containing %q, got nil", tc.errContains)
			}
			if !strings.Contains(err.Error(), tc.errContains) {
				t.Fatalf("expected error containing %q, got %v", tc.errContains, err)
			}
		})
	}
}

func TestReleaseMockErrors(t *testing.T) {
	origFlock := flockFn
	origClose := closeFile
	origMaxInt := maxIntLimit
	t.Cleanup(func() {
		flockFn = origFlock
		closeFile = origClose
		maxIntLimit = origMaxInt
	})

	cases := []struct {
		flockSetup  func(int, int) error
		closeSetup  func(*os.File) error
		name        string
		errContains string
	}{
		{
			name:        "release flock failure",
			flockSetup:  mockFailingFlock,
			errContains: "release flock on",
		},
		{
			name:        "release close failure",
			closeSetup:  mockFailingCloseFile,
			errContains: "close lock file",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			flockFn = origFlock
			closeFile = origClose
			maxIntLimit = origMaxInt

			lockPath := filepath.Join(t.TempDir(), "test.lock")
			held, err := Acquire(context.Background(), lockPath)
			if err != nil {
				t.Fatalf("acquire failed: %v", err)
			}

			if tc.flockSetup != nil {
				flockFn = tc.flockSetup
			}
			if tc.closeSetup != nil {
				closeFile = tc.closeSetup
			}

			relErr := held.Release()
			if relErr == nil || !strings.Contains(relErr.Error(), tc.errContains) {
				t.Fatalf("expected release error containing %q, got %v", tc.errContains, relErr)
			}
		})
	}

	lockPath := filepath.Join(t.TempDir(), "maxint_release.lock")
	flockFn = origFlock
	closeFile = origClose
	maxIntLimit = origMaxInt
	held, err := Acquire(context.Background(), lockPath)
	if err != nil {
		t.Fatalf("acquire failed: %v", err)
	}
	maxIntLimit = 0
	if err := held.Release(); err != nil {
		t.Fatalf("expected successful release when maxInt is 0, got %v", err)
	}
}

func TestDefaultHelpersDirect(t *testing.T) {
	t.Parallel()

	if err := defaultFlock(-1, 0); err == nil {
		t.Fatal("expected error from defaultFlock with invalid fd")
	}

	closedFile, err := os.CreateTemp(t.TempDir(), "closed")
	if err != nil {
		t.Fatalf("create temp: %v", err)
	}
	if err := closedFile.Close(); err != nil {
		t.Fatalf("close temp: %v", err)
	}
	if err := defaultCloseFile(closedFile); err == nil {
		t.Fatal("expected error from defaultCloseFile on closed file")
	}
}
