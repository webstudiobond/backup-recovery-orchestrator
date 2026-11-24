package dac

import (
	"errors"
	"os"
	"testing"
)

type ownerRecord struct {
	path string
	uid  int
	gid  int
}

func mockDifferentSameFile(_, _ os.FileInfo) bool {
	return false
}

func mockFailingOpenDot(_ *os.Root, _ string) (*os.File, error) {
	return nil, errors.New("simulated open dot error")
}

func mockFailingFileStat(_ *os.File) (os.FileInfo, error) {
	return nil, errors.New("simulated fstat error")
}

func mockFailingFileChown(_ *os.File, _, _ int) error {
	return errors.New("simulated fchown error")
}

func mockFailingFileChmod(_ *os.File, _ os.FileMode) error {
	return errors.New("simulated fchmod error")
}

func mockFailingFileClose(_ *os.File) error {
	return errors.New("simulated file close error")
}

func mockFailingRootClose(_ *os.Root) error {
	return errors.New("simulated root close error")
}

func mockFailingRootLstat(_ *os.Root, _ string) (os.FileInfo, error) {
	return nil, errors.New("simulated root lstat error")
}

func mockFailingRootStat(_ *os.Root, _ string) (os.FileInfo, error) {
	return nil, errors.New("simulated root stat error")
}

func mockFailingRootLchown(_ *os.Root, _ string, _, _ int) error {
	return errors.New("simulated root lchown error")
}

func mockFailingRootChmod(_ *os.Root, _ string, _ os.FileMode) error {
	return errors.New("simulated root chmod error")
}

func mockFailingRootOpenFile(_ *os.Root, _ string, _ int, _ os.FileMode) (*os.File, error) {
	return nil, errors.New("simulated root openfile error")
}

func mockFailingRootOpenRoot(_ *os.Root, _ string) (*os.Root, error) {
	return nil, errors.New("simulated root openroot error")
}

func mockFailingFileReadDir(_ *os.File, _ int) ([]os.DirEntry, error) {
	return nil, errors.New("simulated file readdir error")
}

func closeRoot(t *testing.T, r *os.Root) {
	t.Helper()
	if err := r.Close(); err != nil {
		t.Fatalf("close root: %v", err)
	}
}

func assertPerm(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %q: %v", path, err)
	}
	if info.Mode().Perm() != want {
		t.Fatalf("expected mode %04o on %q, got %04o", want, path, info.Mode().Perm())
	}
}

func assertOwnerRecorded(t *testing.T, records []ownerRecord, path string, uid, gid int) {
	t.Helper()
	for _, rec := range records {
		if rec.path == path {
			if rec.uid != uid || rec.gid != gid {
				t.Fatalf("expected owner %d:%d on %q, got %d:%d", uid, gid, path, rec.uid, rec.gid)
			}
			return
		}
	}
	t.Fatalf("expected owner call recorded for %q", path)
}

func assertDryChangesHavePrev(t *testing.T, changes []Change) {
	t.Helper()
	for i := range changes {
		if !changes[i].Skipped && !changes[i].HasPrev {
			t.Fatalf("expected previous attributes recorded on %+v", changes[i])
		}
	}
}
