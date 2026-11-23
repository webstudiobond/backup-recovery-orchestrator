// Package lock provides advisory file locking for single-instance orchestrator execution.
package lock

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sync"
	"syscall"
)

// ErrLocked indicates another orchestrator process already holds the lock file.
var ErrLocked = errors.New("lock already held by another process")

// FileLock guards exclusive single-instance execution across scheduled and manual runs.
type FileLock struct {
	file *os.File
	path string
	mu   sync.Mutex
}

func defaultFlock(fd, how int) error {
	if err := syscall.Flock(fd, how); err != nil {
		return fmt.Errorf("flock: %w", err)
	}
	return nil
}

func defaultCloseFile(f *os.File) error {
	if err := f.Close(); err != nil {
		return fmt.Errorf("close file: %w", err)
	}
	return nil
}

var (
	flockFn     = defaultFlock
	closeFile   = defaultCloseFile
	maxIntLimit = uintptr(math.MaxInt)
)

// Acquire opens path and obtains a non-blocking exclusive advisory lock so overlapping runs fail immediately.
func Acquire(ctx context.Context, path string) (*FileLock, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("context canceled before acquiring lock: %w", err)
	}

	cleanPath := filepath.Clean(path)
	if cleanPath == "" || cleanPath == "." {
		return nil, errors.New("lock file path must not be empty")
	}

	dir := filepath.Dir(cleanPath)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, fmt.Errorf("create lock directory %q: %w", dir, err)
	}

	file, err := os.OpenFile(cleanPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open lock file %q: %w", cleanPath, err)
	}

	fd := file.Fd()
	if fd > maxIntLimit {
		closeErr := closeFile(file)
		return nil, errors.Join(errors.New("file descriptor exceeds int range"), closeErr)
	}

	if flockErr := flockFn(int(fd), syscall.LOCK_EX|syscall.LOCK_NB); flockErr != nil {
		closeErr := closeFile(file)
		if errors.Is(flockErr, syscall.EWOULDBLOCK) || errors.Is(flockErr, syscall.EAGAIN) {
			return nil, errors.Join(fmt.Errorf("%w: %s", ErrLocked, cleanPath), closeErr)
		}
		return nil, errors.Join(fmt.Errorf("acquire flock on %q: %w", cleanPath, flockErr), closeErr)
	}

	return &FileLock{
		file: file,
		path: cleanPath,
	}, nil
}

// Path returns the cleaned filesystem path of the held lock file.
func (l *FileLock) Path() string {
	return l.path
}

// Release unlocks and closes the underlying file descriptor idempotently so deferred cleanup is always safe.
func (l *FileLock) Release() error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.file == nil {
		return nil
	}

	file := l.file
	l.file = nil

	var unlockErr error
	fd := file.Fd()
	if fd <= maxIntLimit {
		if flockErr := flockFn(int(fd), syscall.LOCK_UN); flockErr != nil {
			unlockErr = fmt.Errorf("release flock on %q: %w", l.path, flockErr)
		}
	}

	closeErr := closeFile(file)
	if closeErr != nil {
		closeErr = fmt.Errorf("close lock file %q: %w", l.path, closeErr)
	}

	return errors.Join(unlockErr, closeErr)
}
