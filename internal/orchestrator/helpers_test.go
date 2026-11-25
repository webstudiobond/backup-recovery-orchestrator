package orchestrator

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/webstudiobond/backup-recovery-orchestrator/internal/archive"
	"github.com/webstudiobond/backup-recovery-orchestrator/internal/cmdexec"
	"github.com/webstudiobond/backup-recovery-orchestrator/internal/config"
	"github.com/webstudiobond/backup-recovery-orchestrator/internal/dac"
	"github.com/webstudiobond/backup-recovery-orchestrator/internal/database"
	"github.com/webstudiobond/backup-recovery-orchestrator/internal/docker"
	"github.com/webstudiobond/backup-recovery-orchestrator/internal/hooks"
	"github.com/webstudiobond/backup-recovery-orchestrator/internal/lvm"
	"github.com/webstudiobond/backup-recovery-orchestrator/internal/mount"
	"github.com/webstudiobond/backup-recovery-orchestrator/internal/storage"
	"github.com/webstudiobond/backup-recovery-orchestrator/internal/sysuser"
)

type orchCall struct {
	name string
	args []string
}

type fakeOrchRunner struct {
	onRun     func(spec *cmdexec.Spec) error
	failMatch string
	snapMount string
	stackName string
	psOutput  []byte
	calls     []orchCall
	mu        sync.Mutex
}

func (f *fakeOrchRunner) Run(_ context.Context, spec *cmdexec.Spec) error {
	var stdinData []byte
	if spec.Stdin != nil {
		data, err := io.ReadAll(spec.Stdin)
		if err != nil {
			return err
		}
		stdinData = data
	}

	f.mu.Lock()
	f.calls = append(f.calls, orchCall{name: spec.Name, args: slices.Clone(spec.Args)})
	failMatch := f.failMatch
	snapMount := f.snapMount
	stackName := f.stackName
	onRun := f.onRun
	f.mu.Unlock()

	if onRun != nil {
		if err := onRun(spec); err != nil {
			return err
		}
	}

	joined := spec.Name + " " + strings.Join(spec.Args, " ")
	if failMatch != "" && strings.Contains(joined, failMatch) {
		return errors.New("simulated command failure: " + joined)
	}

	if spec.Name == "lvcreate" && snapMount != "" && stackName != "" {
		if err := os.MkdirAll(filepath.Join(snapMount, stackName), 0o750); err != nil {
			return err
		}
	}
	return simulateOrchStdout(spec, stdinData)
}

func simulateOrchStdout(spec *cmdexec.Spec, stdinData []byte) error {
	if spec.Stdout == nil {
		return nil
	}
	switch spec.Name {
	case "tar":
		tarBytes, err := makeFakeOrchTarBytes()
		if err != nil {
			return err
		}
		_, err = spec.Stdout.Write(tarBytes)
		return err
	case "zstd":
		_, err := spec.Stdout.Write(stdinData)
		return err
	default:
		_, err := spec.Stdout.Write([]byte("SQL DUMP"))
		return err
	}
}

func makeFakeOrchTarBytes() ([]byte, error) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	content := []byte("services: {}\n")
	if err := tw.WriteHeader(&tar.Header{
		Name:     "docker-compose.yaml",
		Mode:     0o600,
		Typeflag: tar.TypeReg,
		Size:     int64(len(content)),
	}); err != nil {
		return nil, err
	}
	if _, err := tw.Write(content); err != nil {
		return nil, err
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func (f *fakeOrchRunner) Output(_ context.Context, spec *cmdexec.Spec) ([]byte, error) {
	f.mu.Lock()
	f.calls = append(f.calls, orchCall{name: spec.Name, args: slices.Clone(spec.Args)})
	psOut := f.psOutput
	f.mu.Unlock()
	return psOut, nil
}

func newTestOrchestrator(runner *fakeOrchRunner, pPath, gPath string, statFSTypeFn func(string) (int64, error)) *Orchestrator {
	dockerClient := &docker.Client{Runner: runner, PollInterval: time.Millisecond}
	if statFSTypeFn == nil {
		statFSTypeFn = func(_ string) (int64, error) { return 0x65735546, nil }
	}
	verifier := &mount.Verifier{
		Runner:       runner,
		StatFSType:   statFSTypeFn,
		PollInterval: time.Millisecond,
	}
	lvmMgr := &lvm.Manager{
		Runner:       runner,
		AvailBytesFn: func(_ string) (uint64, error) { return 100 << 30, nil },
	}
	dacApplier := &dac.Applier{
		Lchown:      func(_ string, _, _ int) error { return nil },
		Chmod:       os.Chmod,
		LookupUser:  func(_ string) (int, error) { return 1002, nil },
		LookupGroup: func(_ string) (int, error) { return 1002, nil },
	}
	return &Orchestrator{
		Docker:     dockerClient,
		Hooks:      &hooks.Runner{Docker: dockerClient, PollInterval: time.Millisecond},
		Database:   database.NewEngine(runner, dockerClient),
		LVM:        lvmMgr,
		Archive:    &archive.Engine{Runner: runner, Now: func() time.Time { return time.Date(2026, 11, 16, 15, 0, 0, 0, time.UTC) }},
		Storage:    storage.NewManager(runner, verifier),
		SysUser:    &sysuser.Manager{Runner: runner, PasswdPath: pPath, GroupPath: gPath},
		DAC:        dacApplier,
		Now:        func() time.Time { return time.Date(2026, 11, 16, 15, 0, 0, 0, time.UTC) },
		SystemdDir: filepath.Join(filepath.Dir(pPath), "etc", "systemd", "system"),
	}
}

func buildTestBundle(t *testing.T, root, stackName string) config.Bundle {
	t.Helper()

	stackDir := filepath.Join(root, "home", stackName)
	composeFile := filepath.Join(stackDir, "docker-compose.yaml")
	angieCompose := filepath.Join(root, "home", "angie", "docker-compose.yaml")
	domainsDir := filepath.Join(root, "home", "angie", "data", "conf.d", "domains")
	streamDir := filepath.Join(root, "home", "angie", "data", "conf.d", "stream")
	certsDir := filepath.Join(root, "home", "angie", "data", "certs", stackName)
	hostLogrotate := filepath.Join(root, "etc", "logrotate.d", stackName)
	cfgDir := filepath.Join(root, "etc", "bro")
	systemdDir := filepath.Join(root, "etc", "systemd", "system")
	fuseMount := filepath.Join(root, "mnt", "cloud-primary")
	fuseBackups := filepath.Join(fuseMount, "backups")

	for _, d := range []string{stackDir, filepath.Dir(angieCompose), domainsDir, streamDir, certsDir, filepath.Dir(hostLogrotate), cfgDir, systemdDir, fuseBackups} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			t.Fatalf("mkdir %q: %v", d, err)
		}
	}
	for _, f := range []string{
		composeFile,
		angieCompose,
		filepath.Join(certsDir, "certificate.pem"),
		hostLogrotate,
		filepath.Join(cfgDir, "config.json"),
		filepath.Join(systemdDir, "bro.service"),
		filepath.Join(systemdDir, "bro.timer"),
		filepath.Join(systemdDir, "mnt-cloud.mount"),
		filepath.Join(systemdDir, "mnt-cloud.automount"),
		filepath.Join(systemdDir, "unrelated.service"),
	} {
		if err := os.WriteFile(f, []byte("data"), 0o600); err != nil {
			t.Fatalf("write %q: %v", f, err)
		}
	}
	if err := os.Symlink(filepath.Join(systemdDir, "bro.service"), filepath.Join(systemdDir, "bro-link.service")); err != nil {
		t.Fatalf("symlink bro-link.service: %v", err)
	}
	domainFile := filepath.Join(domainsDir, "alpha.example.com.conf")
	if err := os.WriteFile(domainFile, []byte("server {}"), 0o600); err != nil {
		t.Fatalf("write domain conf: %v", err)
	}
	streamFile := filepath.Join(streamDir, "turn.example.com.inc")
	if err := os.WriteFile(streamFile, []byte("server { listen 5349; }"), 0o600); err != nil {
		t.Fatalf("write stream conf: %v", err)
	}

	return config.Bundle{
		ConfigDir: cfgDir,
		Global: config.GlobalConfig{
			BackupRoot: filepath.Join(root, "backups"),
			MinFreeGB:  10,
			ZstdLevel:  6,
			LVM: config.LVMConfig{
				VGName:       "ubuntu-vg",
				LVName:       "lv_home",
				SnapshotName: "home_snap",
				SnapshotSize: "35G",
				MountPoint:   filepath.Join(root, "mnt", "home_snap"),
			},
			Angie: config.GlobalAngieConfig{
				ComposeFile: angieCompose,
				ServiceName: "angie",
				CertsDir:    filepath.Dir(certsDir),
				DomainsDir:  domainsDir,
				StreamDir:   streamDir,
			},
			Retention: config.RetentionConfig{LocalCount: 1, RemoteCount: 5},
			Destinations: []config.DestinationConfig{
				{
					Name:                "cloud-primary",
					Type:                "fuse",
					MountPoint:          fuseMount,
					BackupDir:           fuseBackups,
					SystemdUnit:         "mnt-cloud.mount",
					RcloneCleanupRemote: "cloud-primary-enc",
				},
			},
		},
		Stacks: []config.StackConfig{
			{
				Name:        stackName,
				Enabled:     true,
				StackDir:    stackDir,
				ComposeFile: composeFile,
				User:        config.UserConfig{Name: stackName, UID: 1010, GID: 1010},
				LVM: config.LVMConfig{
					VGName:       "ubuntu-vg",
					LVName:       "lv_home",
					SnapshotName: "home_snap",
					SnapshotSize: "35G",
					MountPoint:   filepath.Join(root, "mnt", "home_snap"),
				},
				MinFreeGB: 10,
				ZstdLevel: 1,
				Retention: config.RetentionConfig{LocalCount: 1, RemoteCount: 5},
				DAC: []config.DACRule{
					{Path: "docker-compose.yaml", Owner: "root:root", FileMode: "0600"},
				},
				HostFiles: []config.HostFileConfig{
					{Path: hostLogrotate, Owner: "root:root", FileMode: "0400"},
				},
				Databases: []config.DatabaseConfig{
					{ID: "primary", Driver: "postgres", Service: "pg", DBUser: stackName, DBName: stackName},
				},
				Angie: config.StackAngieConfig{
					ACMENames:   []string{stackName},
					DomainConfs: []string{"alpha.example.com.conf"},
					StreamConfs: []string{"turn.example.com.inc"},
				},
				RestoreSteps: []config.RestoreStep{
					{Action: "compose_down"},
					{Action: "extract_files"},
					{Action: "apply_dac"},
					{Action: "extract_host_files"},
					{Action: "deploy_angie"},
					{Action: "compose_up", Services: []string{"pg"}},
					{Action: "wait_healthy", Services: []string{"pg"}},
					{Action: "db_import"},
				},
			},
		},
	}
}

func writeFakeAccounts(t *testing.T, root, stackName string) (pPath, gPath string) {
	t.Helper()
	pPath = filepath.Join(root, "accounts")
	gPath = filepath.Join(root, "groups")
	if err := os.WriteFile(pPath, []byte(stackName+":x:1010:1010::/home/"+stackName+":/usr/sbin/nologin\n"), 0o600); err != nil {
		t.Fatalf("write accounts: %v", err)
	}
	if err := os.WriteFile(gPath, []byte(stackName+":x:1010:\n"), 0o600); err != nil {
		t.Fatalf("write groups: %v", err)
	}
	return pPath, gPath
}

func assertContainsAll(t *testing.T, haystack string, needles []string) {
	t.Helper()
	for _, needle := range needles {
		if !strings.Contains(haystack, needle) {
			t.Fatalf("expected %q in output, got:\n%s", needle, haystack)
		}
	}
}

func assertFileMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %q: %v", path, err)
	}
	if info.Mode().Perm() != want {
		t.Fatalf("expected %q mode %04o, got %04o", path, want, info.Mode().Perm())
	}
}

func assertBashContainerLayout(ctx context.Context, t *testing.T, archivePath, stackName string) {
	t.Helper()

	verified, err := archive.VerifyContainer(ctx, archivePath)
	if err != nil {
		t.Fatalf("VerifyContainer failed: %v", err)
	}
	wantFiles := stackName + "_files_2026-11-16_15-00-00.tar.zst"
	if verified.FilesArchive != wantFiles {
		t.Fatalf("expected files_archive %q, got %q", wantFiles, verified.FilesArchive)
	}
	if verified.FilesUncompressedBytes <= 0 || verified.HostFilesUncompressedBytes <= 0 {
		t.Fatalf("expected positive uncompressed archive sizes, got files=%d host=%d", verified.FilesUncompressedBytes, verified.HostFilesUncompressedBytes)
	}
	wantDB := stackName + "_db_2026-11-16_15-00-00.sql.zst"
	if len(verified.Databases) != 1 || verified.Databases[0].ArtifactPath != wantDB || verified.Databases[0].UncompressedBytes <= 0 {
		t.Fatalf("expected single db artifact %q with uncompressed bytes, got %+v", wantDB, verified.Databases)
	}
	if _, ok := verified.Artifacts["domains/alpha.example.com.conf"]; !ok {
		t.Fatalf("expected domains/alpha.example.com.conf in artifacts, got %+v", verified.Artifacts)
	}
	if _, ok := verified.Artifacts["stream/turn.example.com.inc"]; !ok {
		t.Fatalf("expected stream/turn.example.com.inc in artifacts, got %+v", verified.Artifacts)
	}
	if _, ok := verified.Artifacts["certs/"+stackName+"/certificate.pem"]; !ok {
		t.Fatalf("expected certs/%s/certificate.pem in artifacts, got %+v", stackName, verified.Artifacts)
	}
}

func closeOrchRoot(t *testing.T, r *os.Root) {
	t.Helper()
	if err := r.Close(); err != nil {
		t.Fatalf("close root: %v", err)
	}
}

func mockDifferentSameFile(_, _ fs.FileInfo) bool {
	return false
}

func mockFailingRootClose(_ *os.Root) error {
	return errors.New("simulated root close error")
}

func mockFailingRootLstat(_ *os.Root, _ string) (fs.FileInfo, error) {
	return nil, errors.New("simulated root lstat error")
}

func mockFailingRootStat(_ *os.Root, _ string) (fs.FileInfo, error) {
	return nil, errors.New("simulated root stat error")
}

func mockFailingRootOpenDot(_ *os.Root, _ string) (*os.File, error) {
	return nil, errors.New("simulated root open dot error")
}

func mockFailingRootOpenRoot(_ *os.Root, _ string) (*os.Root, error) {
	return nil, errors.New("simulated root open root error")
}

func mockFailingOpenRoot(_ string) (*os.Root, error) {
	return nil, errors.New("simulated open root failure")
}

func mockFailingRootOpenFile(_ *os.Root, _ string, _ int, _ fs.FileMode) (*os.File, error) {
	return nil, errors.New("simulated root open file error")
}

func mockFailingRootMkdir(_ *os.Root, _ string, _ fs.FileMode) error {
	return errors.New("simulated root mkdir error")
}

func mockFailingFileReadDir(_ *os.File, _ int) ([]fs.DirEntry, error) {
	return nil, errors.New("simulated file readdir error")
}

func mockFailingFileStat(_ *os.File) (fs.FileInfo, error) {
	return nil, errors.New("simulated file stat error")
}

func mockFailingRootRemoveAll(_ *os.Root, _ string) error {
	return errors.New("simulated root removeall error")
}

func mockFailingOSStat(_ string) (fs.FileInfo, error) {
	return nil, errors.New("simulated os stat error")
}

type fakeDirEntry struct {
	infoErr error
	name    string
	isDir   bool
}

func (f fakeDirEntry) Name() string               { return f.name }
func (f fakeDirEntry) IsDir() bool                { return f.isDir }
func (f fakeDirEntry) Type() fs.FileMode          { return 0 }
func (f fakeDirEntry) Info() (fs.FileInfo, error) { return nil, f.infoErr }

var _ = fs.ModePerm
