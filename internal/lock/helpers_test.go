package lock

import (
	"errors"
	"fmt"
	"os"
)

func mockFailingFlock(_, _ int) error {
	return errors.New("simulated flock failure")
}

func mockFailingCloseFile(f *os.File) error {
	if err := f.Close(); err != nil {
		return fmt.Errorf("close fixture: %w", err)
	}
	return errors.New("simulated close failure")
}
