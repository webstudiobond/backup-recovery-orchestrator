package dac

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/webstudiobond/backup-recovery-orchestrator/internal/config"
)

func TestWalkAndApplyErrors(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	subDir := filepath.Join(root, "walk_sub")
	if err := os.MkdirAll(subDir, 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	secondFile := filepath.Join(subDir, "2_file.txt")
	for _, f := range []string{filepath.Join(subDir, "1_file.txt"), secondFile} {
		if err := os.WriteFile(f, []byte("data"), 0o600); err != nil {
			t.Fatalf("write file: %v", err)
		}
	}

	applier := NewApplier()

	subRoot, openErr := os.OpenRoot(subDir)
	if openErr != nil {
		t.Fatalf("open subRoot: %v", openErr)
	}
	defer closeRoot(t, subRoot)
	subInfo, statErr := subRoot.Stat(".")
	if statErr != nil {
		t.Fatalf("stat subRoot: %v", statErr)
	}

	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()

	_, ctxErr := applier.walkRootAndApply(canceledCtx, subRoot, subDir, subInfo, &dacPolicy{})
	if ctxErr == nil {
		t.Fatalf("expected context canceled error during walk")
	}

	commitFailApplier := &Applier{Chmod: func(_ string, _ os.FileMode) error { return errors.New("walk chmod failed") }}
	failPolicy := dacPolicy{dirMode: 0o755, hasDirMode: true, fileMode: 0o644, hasFileMode: true}
	if _, commitErr := commitFailApplier.walkRootAndApply(context.Background(), subRoot, subDir, subInfo, &failPolicy); commitErr == nil {
		t.Fatalf("expected commit error during walk")
	}

	removedApplier := &Applier{
		Chmod: func(p string, _ os.FileMode) error {
			if filepath.Base(p) == "1_file.txt" {
				if rmErr := os.Remove(secondFile); rmErr != nil {
					return rmErr
				}
			}
			return nil
		},
	}
	fileOnlyPolicy := dacPolicy{fileMode: 0o600, hasFileMode: true}
	_, walkStatErr := removedApplier.walkRootAndApply(context.Background(), subRoot, subDir, subInfo, &fileOnlyPolicy)
	if walkStatErr == nil {
		t.Fatalf("expected lstat error after file deletion")
	}
}

func TestApplyStackRejectsIntermediateSymlinks(t *testing.T) {
	t.Parallel()

	baseDir := t.TempDir()
	stackDir := filepath.Join(baseDir, "stack")
	outsideDir := filepath.Join(baseDir, "outside")
	outsideFile := filepath.Join(outsideDir, "secret.txt")
	internalSecret := filepath.Join(stackDir, "secrets", "app.key")

	for _, dir := range []string{stackDir, outsideDir, filepath.Dir(internalSecret)} {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			t.Fatalf("mkdir %q: %v", dir, err)
		}
	}
	for _, f := range []string{outsideFile, internalSecret} {
		if err := os.WriteFile(f, []byte("secret"), 0o600); err != nil {
			t.Fatalf("write %q: %v", f, err)
		}
	}

	if err := os.Symlink(outsideDir, filepath.Join(stackDir, "ext_link")); err != nil {
		t.Fatalf("symlink ext: %v", err)
	}
	if err := os.Symlink(filepath.Dir(internalSecret), filepath.Join(stackDir, "int_link")); err != nil {
		t.Fatalf("symlink int: %v", err)
	}
	if err := os.WriteFile(filepath.Join(stackDir, "plain_file"), []byte("x"), 0o600); err != nil {
		t.Fatalf("write plain_file: %v", err)
	}

	applier := NewApplier()
	userCfg := config.UserConfig{Name: "symlink_app", UID: 1000, GID: 1000}

	cases := []struct {
		name        string
		errContains string
		rule        config.DACRule
	}{
		{name: "ext", rule: config.DACRule{Path: "ext_link/secret.txt", FileMode: "0644"}},
		{name: "int", rule: config.DACRule{Path: "int_link/app.key", FileMode: "0644"}},
		{name: "opt", rule: config.DACRule{Path: "ext_link/secret.txt", FileMode: "0644", Optional: true}},
		{name: "nondir", rule: config.DACRule{Path: "plain_file/child.txt", FileMode: "0644"}, errContains: "non-directory segment"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			wantErr := tc.errContains
			if wantErr == "" {
				wantErr = "refusing symlink"
			}
			_, err := applier.ApplyStack(context.Background(), stackDir, userCfg, []config.DACRule{tc.rule}, false)
			if err == nil || !strings.Contains(err.Error(), wantErr) {
				t.Fatalf("expected error containing %q, got %v", wantErr, err)
			}
		})
	}
	assertPerm(t, outsideFile, 0o600)
	assertPerm(t, internalSecret, 0o600)
}

func TestWalkBranchesWithMocks(t *testing.T) {
	origSameFile := rootSameFile
	origOpenRoot := openRoot
	origOpenDot := rootOpenDot
	origReadDir := fileReadDir
	origOpenFile := rootOpenFile
	origFileStat := fileStat
	origRootStat := rootStat
	origRootLstat := rootLstat
	origRootClose := rootClose
	origRootOpenRoot := rootOpenRoot
	t.Cleanup(func() {
		rootSameFile = origSameFile
		openRoot = origOpenRoot
		rootOpenDot = origOpenDot
		fileReadDir = origReadDir
		rootOpenFile = origOpenFile
		fileStat = origFileStat
		rootStat = origRootStat
		rootLstat = origRootLstat
		rootClose = origRootClose
		rootOpenRoot = origRootOpenRoot
	})

	stackDir := t.TempDir()
	nestedFile := filepath.Join(stackDir, "sub", "data.txt")
	if err := os.MkdirAll(filepath.Dir(nestedFile), 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(nestedFile, []byte("ok"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.Mkdir(filepath.Join(stackDir, "sub", "sub2"), 0o750); err != nil {
		t.Fatalf("mkdir sub2: %v", err)
	}

	applier := NewApplier()
	userCfg := config.UserConfig{Name: "toctou_app", UID: 1000, GID: 1000}

	r, err := os.OpenRoot(stackDir)
	if err != nil {
		t.Fatalf("open root: %v", err)
	}
	defer closeRoot(t, r)
	info, err := r.Lstat("sub")
	if err != nil {
		t.Fatalf("lstat: %v", err)
	}

	cases := []struct {
		setup   func()
		fn      func() error
		wantErr bool
	}{
		{setup: func() { rootSameFile = mockDifferentSameFile }, fn: func() error {
			defer func() { rootSameFile = origSameFile }()
			_, aErr := applier.ApplyStack(context.Background(), stackDir, userCfg, []config.DACRule{{Path: "sub/data.txt", FileMode: "0644"}}, false)
			return aErr
		}, wantErr: true},
		{setup: func() { rootOpenDot = mockFailingOpenDot }, fn: func() error { _, rErr := readSortedDirEntries(r, stackDir); return rErr }, wantErr: true},
		{setup: func() { rootOpenDot = origOpenDot; fileReadDir = mockFailingFileReadDir }, fn: func() error { _, rErr := readSortedDirEntries(r, stackDir); return rErr }, wantErr: true},
		{setup: func() { fileReadDir = origReadDir; rootOpenFile = mockFailingRootOpenFile }, fn: func() error { _, oErr := openExistingChildDir(r, "sub", info, stackDir); return oErr }, wantErr: true},
		{setup: func() { rootOpenFile = origOpenFile; fileStat = mockFailingFileStat }, fn: func() error { _, oErr := openExistingChildDir(r, "sub", info, stackDir); return oErr }, wantErr: true},
		{setup: func() { fileStat = origFileStat; rootOpenRoot = mockFailingRootOpenRoot }, fn: func() error {
			_, oErr := openExistingChildDirFallback(r, "sub", info, stackDir)
			return oErr
		}, wantErr: true},
		{setup: func() { rootOpenRoot = origRootOpenRoot; rootStat = mockFailingRootStat }, fn: func() error {
			_, oErr := openExistingChildDirFallback(r, "sub", info, stackDir)
			return oErr
		}, wantErr: true},
		{setup: func() { rootStat = origRootStat; rootSameFile = mockDifferentSameFile }, fn: func() error {
			defer func() { rootSameFile = origSameFile }()
			_, oErr := openExistingChildDirFallback(r, "sub", info, stackDir)
			return oErr
		}, wantErr: true},
		{setup: func() { rootLstat = mockFailingRootLstat }, fn: func() error { _, _, sErr := stepIntoChildDir(r, "sub", stackDir); return sErr }, wantErr: true},
		{setup: func() { rootLstat = origRootLstat; rootClose = mockFailingRootClose }, fn: func() error { _, _, _, nErr := navigateParentRoot(r, []string{"sub", "sub2"}, stackDir); return nErr }, wantErr: true},
	}
	for i, tc := range cases {
		if tc.setup != nil {
			tc.setup()
		}
		cErr := tc.fn()
		if (cErr != nil) != tc.wantErr {
			t.Fatalf("case %d: wantErr=%v, got=%v", i, tc.wantErr, cErr)
		}
	}
}

func TestWalkEntryBranchesWithMocks(t *testing.T) {
	origOpenFile := rootOpenFile
	origFileChmod := fileChmod
	origFileReadDir := fileReadDir
	origRootLstat := rootLstat
	origRootClose := rootClose
	t.Cleanup(func() {
		rootOpenFile = origOpenFile
		fileChmod = origFileChmod
		fileReadDir = origFileReadDir
		rootLstat = origRootLstat
		rootClose = origRootClose
	})

	stackDir := t.TempDir()
	nestedFile := filepath.Join(stackDir, "sub", "data.txt")
	if err := os.MkdirAll(filepath.Dir(nestedFile), 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(nestedFile, []byte("ok"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	directFile := filepath.Join(stackDir, "direct.txt")
	if err := os.WriteFile(directFile, []byte("ok"), 0o600); err != nil {
		t.Fatalf("write direct: %v", err)
	}

	applier := NewApplier()
	policy := dacPolicy{uid: 1000, gid: 1000, hasOwner: true, fileMode: 0o644, dirMode: 0o750}

	r, err := os.OpenRoot(stackDir)
	if err != nil {
		t.Fatalf("open root: %v", err)
	}
	defer closeRoot(t, r)
	info, err := r.Lstat("sub")
	if err != nil {
		t.Fatalf("lstat: %v", err)
	}

	cases := []struct {
		fn      func() error
		wantErr bool
	}{
		{fn: func() error {
			rootClose = mockFailingRootClose
			defer func() { rootClose = origRootClose }()
			_, aErr := applier.applyWalkEntry(context.Background(), r, "sub", stackDir, &policy)
			return aErr
		}, wantErr: true},
		{fn: func() error {
			rootOpenFile = mockFailingRootOpenFile
			defer func() { rootOpenFile = origOpenFile }()
			_, aErr := applier.applyWalkEntry(context.Background(), r, "sub", stackDir, &policy)
			return aErr
		}, wantErr: true},
		{fn: func() error {
			fileChmod = mockFailingFileChmod
			defer func() { fileChmod = origFileChmod }()
			_, aErr := applier.applyWalkEntry(context.Background(), r, "direct.txt", stackDir, &policy)
			return aErr
		}, wantErr: true},
		{fn: func() error {
			fileReadDir = mockFailingFileReadDir
			defer func() { fileReadDir = origFileReadDir }()
			noOwnerPol := dacPolicy{fileMode: 0o644, dirMode: 0o750}
			_, wErr := applier.walkRootAndApply(context.Background(), r, stackDir, info, &noOwnerPol)
			return wErr
		}, wantErr: true},
		{fn: func() error {
			cCtx, cancel := context.WithCancel(context.Background())
			rootLstat = func(root *os.Root, name string) (fs.FileInfo, error) {
				cancel()
				return origRootLstat(root, name)
			}
			defer func() { rootLstat = origRootLstat }()
			noOwnerPol := dacPolicy{fileMode: 0o644, dirMode: 0o750}
			_, wErr := applier.walkRootAndApply(cCtx, r, stackDir, info, &noOwnerPol)
			return wErr
		}, wantErr: true},
	}
	for i, tc := range cases {
		cErr := tc.fn()
		if (cErr != nil) != tc.wantErr {
			t.Fatalf("case %d: wantErr=%v, got=%v", i, tc.wantErr, cErr)
		}
	}
}

func TestWalkEntrySkippedNodes(t *testing.T) {
	stackDir := t.TempDir()
	nestedFile := filepath.Join(stackDir, "sub", "data.txt")
	if err := os.MkdirAll(filepath.Dir(nestedFile), 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(nestedFile, []byte("ok"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	pipePath := filepath.Join(stackDir, "pipe.fifo")
	if err := syscall.Mkfifo(pipePath, 0o600); err != nil {
		t.Fatalf("mkfifo: %v", err)
	}
	if err := os.Symlink(nestedFile, filepath.Join(stackDir, "sym")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	applier := NewApplier()
	policy := dacPolicy{uid: 1000, gid: 1000, hasOwner: true, fileMode: 0o644, dirMode: 0o750}

	r, err := os.OpenRoot(stackDir)
	if err != nil {
		t.Fatalf("open root: %v", err)
	}
	defer closeRoot(t, r)

	symCh, symErr := applier.applyWalkEntry(context.Background(), r, "sym", stackDir, &policy)
	if symErr != nil || len(symCh) != 1 || !symCh[0].Skipped {
		t.Fatalf("expected symlink skipped: err=%v, ch=%v", symErr, symCh)
	}
	fifoCh, fifoErr := applier.applyWalkEntry(context.Background(), r, "pipe.fifo", stackDir, &policy)
	if fifoErr != nil || len(fifoCh) != 1 || !fifoCh[0].Skipped {
		t.Fatalf("expected fifo skipped: err=%v, ch=%v", fifoErr, fifoCh)
	}
}

func TestApplyStackSkipsFIFOAndSpecialFiles(t *testing.T) {
	t.Parallel()

	stackDir := t.TempDir()
	dataDir := filepath.Join(stackDir, "data")
	if err := os.MkdirAll(dataDir, 0o750); err != nil {
		t.Fatalf("mkdir data: %v", err)
	}
	fifoPath := filepath.Join(dataDir, "pipe.fifo")
	if err := syscall.Mkfifo(fifoPath, 0o644); err != nil {
		t.Fatalf("mkfifo: %v", err)
	}
	regPath := filepath.Join(dataDir, "normal.txt")
	if err := os.WriteFile(regPath, []byte("ok"), 0o600); err != nil {
		t.Fatalf("write normal: %v", err)
	}

	applier := NewApplier()
	userCfg := config.UserConfig{Name: "fifo_app", UID: 1000, GID: 1000}

	walkChanges, walkErr := applier.ApplyStack(context.Background(), stackDir, userCfg, []config.DACRule{{Path: "data", DirMode: "0750", FileMode: "0640", Recursive: true}}, false)
	if walkErr != nil {
		t.Fatalf("unexpected error on recursive walk with FIFO: %v", walkErr)
	}
	var sawFifoSkip bool
	for i := range walkChanges {
		if walkChanges[i].Path == fifoPath && walkChanges[i].Skipped {
			sawFifoSkip = true
		}
	}
	if !sawFifoSkip {
		t.Fatalf("expected FIFO skipped during recursive walk, got %+v", walkChanges)
	}
	assertPerm(t, fifoPath, 0o644)
	assertPerm(t, regPath, 0o640)
}

func TestOpenExistingChildDirRejectsFIFOWithoutBlocking(t *testing.T) {
	t.Parallel()

	stackDir := t.TempDir()
	fifoPath := filepath.Join(stackDir, "pipe.fifo")
	if err := syscall.Mkfifo(fifoPath, 0o600); err != nil {
		t.Fatalf("mkfifo: %v", err)
	}

	stackRoot, openErr := os.OpenRoot(stackDir)
	if openErr != nil {
		t.Fatalf("open stackRoot: %v", openErr)
	}
	defer closeRoot(t, stackRoot)

	if _, err := openExistingChildDir(stackRoot, "pipe.fifo", nil, fifoPath); err == nil {
		t.Fatalf("expected openExistingChildDir on FIFO to fail without blocking")
	}
}
