package dac

import (
	"os"
	"testing"
)

type ownerRecord struct {
	path string
	uid  int
	gid  int
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
