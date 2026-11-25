package orchestrator

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/webstudiobond/backup-recovery-orchestrator/internal/config"
	"github.com/webstudiobond/backup-recovery-orchestrator/internal/dac"
	"github.com/webstudiobond/backup-recovery-orchestrator/internal/docker"
	"github.com/webstudiobond/backup-recovery-orchestrator/internal/reverseproxy"
)

func recordingReverseProxyDACApplier(owners map[string][2]int) *dac.Applier {
	return &dac.Applier{
		Lchown: func(path string, uid, gid int) error {
			owners[path] = [2]int{uid, gid}
			return nil
		},
		Chmod: os.Chmod,
		LookupUser: func(name string) (int, error) {
			if name == "root" {
				return 0, nil
			}
			return 1002, nil
		},
		LookupGroup: func(_ string) (int, error) { return 1002, nil },
	}
}

func assertReverseProxyPathPermAndOwner(t *testing.T, owners map[string][2]int, path string, wantMode os.FileMode, wantOwner [2]int) {
	t.Helper()

	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("lstat %q: %v", path, err)
	}
	if info.Mode().Perm() != wantMode {
		t.Fatalf("expected mode %04o on %q, got %04o", wantMode, path, info.Mode().Perm())
	}
	gotOwner, ok := owners[path]
	if !ok || gotOwner != wantOwner {
		t.Fatalf("expected lchown %v on %q, got %v (present=%t)", wantOwner, path, gotOwner, ok)
	}
}

func TestRestoreReverseProxyRollbackOnInvalidConfig(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := t.TempDir()
	stackName := "app-delta"
	bundle := buildTestBundle(t, root, stackName)
	pPath, gPath := writeFakeAccounts(t, root, stackName)

	runner := &fakeOrchRunner{
		snapMount: bundle.Global.LVM.MountPoint,
		stackName: stackName,
		psOutput:  []byte(`{"Service":"pg","State":"running","Health":"healthy"}`),
	}
	orch := newTestOrchestrator(runner, pPath, gPath, nil)
	owners := make(map[string][2]int)
	orch.DAC = recordingReverseProxyDACApplier(owners)

	if err := orch.Backup(ctx, &bundle, &BackupOptions{NoSync: true, Out: io.Discard}); err != nil {
		t.Fatalf("Backup failed: %v", err)
	}

	reverseProxyStack := bundle.ReverseProxyStack()
	reverseProxyStack.User = config.UserConfig{Name: "reverseproxy", UID: 1005, GID: 1005}
	reverseProxyStack.DAC = []config.DACRule{
		{Path: "data/conf.d", Owner: "root:reverseproxy", DirMode: "0750", FileMode: "0640", Recursive: true},
		{Path: "data/tls-certs", Owner: "stack:stack", DirMode: "0750", FileMode: "0640", Recursive: true},
	}

	domainPath := filepath.Join(reverseProxyStack.StackDir, reverseProxyStack.ReverseProxyProvider.DomainsDir, "alpha.example.com.conf")
	if err := os.WriteFile(domainPath, []byte("live-good-config"), 0o600); err != nil {
		t.Fatalf("update live domain: %v", err)
	}
	streamPath := filepath.Join(reverseProxyStack.StackDir, reverseProxyStack.ReverseProxyProvider.StreamDir, bundle.Stacks[0].ReverseProxy.StreamConfs[0])
	if err := os.WriteFile(streamPath, []byte("live-good-stream"), 0o600); err != nil {
		t.Fatalf("update live stream: %v", err)
	}
	certDir := filepath.Join(reverseProxyStack.StackDir, reverseProxyStack.ReverseProxyProvider.CertsDir, stackName)
	certFile := filepath.Join(certDir, "certificate.pem")
	if err := os.WriteFile(certFile, []byte("live-good-cert"), 0o600); err != nil {
		t.Fatalf("update live cert: %v", err)
	}

	clear(owners)
	runner.failMatch = "angie -t"
	archivePath := filepath.Join(bundle.Global.BackupRoot, stackName, stackName+"_2026-11-16_15-00-00.tar")
	err := orch.Restore(ctx, &bundle.Global, &RestoreOptions{
		ArchivePath:       archivePath,
		ReverseProxyStack: reverseProxyStack,
		ReverseProxyMode:  RestoreModeAll,
		OnlySteps:         []string{"deploy_reverse_proxy"},
		Out:               io.Discard,
	})
	if err == nil || !strings.Contains(err.Error(), "angie -t") {
		t.Fatalf("expected angie -t error, got %v", err)
	}

	for path, wantBody := range map[string]string{
		domainPath: "live-good-config",
		streamPath: "live-good-stream",
		certFile:   "live-good-cert",
	} {
		data, readErr := os.ReadFile(filepath.Clean(path))
		if readErr != nil || string(data) != wantBody {
			t.Fatalf("expected %q rolled back to %q, got %q (err=%v)", path, wantBody, string(data), readErr)
		}
	}

	assertReverseProxyPathPermAndOwner(t, owners, domainPath, 0o640, [2]int{0, 1005})
	assertReverseProxyPathPermAndOwner(t, owners, streamPath, 0o640, [2]int{0, 1005})
	assertReverseProxyPathPermAndOwner(t, owners, certDir, 0o750, [2]int{1005, 1005})
	assertReverseProxyPathPermAndOwner(t, owners, certFile, 0o640, [2]int{1005, 1005})
}

func TestCheckReverseProxySelectionList(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		kind      string
		wantErr   string
		requested []string
		available []string
	}{
		{name: "all items present", kind: "domain", requested: []string{"a.example.com"}, available: []string{"a.example.com", "b.example.com"}},
		{name: "empty requested items", kind: "stream", requested: nil, available: []string{"s.example.com"}},
		{name: "missing item returns error", kind: "cert", requested: []string{"missing.example.com"}, available: []string{"exists.example.com"}, wantErr: "is not present in archive manifest"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := checkReverseProxySelectionList(tc.requested, tc.available, tc.kind)
			if tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)) {
				t.Fatalf("expected error containing %q, got %v", tc.wantErr, err)
			}
			if tc.wantErr == "" && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestResolveReverseProxySelection(t *testing.T) {
	t.Parallel()

	domainConf := "res-site.example.com.conf"
	avail := &config.StackReverseProxyConfig{
		DomainConfs: []string{domainConf},
		StreamConfs: []string{"res-stream.example.com.inc"},
		ACMENames:   []string{"app-cert.example.com"},
	}

	tests := []struct {
		name        string
		mode        string
		wantErr     string
		reqDomains  []string
		reqStreams  []string
		reqCerts    []string
		wantDomains []string
	}{
		{name: "explicit valid", reqDomains: []string{domainConf}, reqStreams: []string{"res-stream.example.com.inc"}, reqCerts: []string{"app-cert.example.com"}, wantDomains: []string{domainConf}},
		{name: "missing domain", reqDomains: []string{"notfound-domain.example.com.conf"}, wantErr: "requested reverse-proxy domain"},
		{name: "missing stream", reqStreams: []string{"notfound-stream.example.com.inc"}, wantErr: "requested reverse-proxy stream"},
		{name: "missing cert", reqCerts: []string{"notfound-cert.example.com"}, wantErr: "requested reverse-proxy cert"},
		{name: "mode empty", mode: ""},
		{name: "mode none", mode: RestoreModeNone},
		{name: "mode all", mode: RestoreModeAll, wantDomains: []string{domainConf}},
		{name: "unknown mode", mode: "unknown-mode", wantErr: "unsupported reverse-proxy restore mode"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			sel, err := resolveReverseProxySelection(avail, tc.mode, tc.reqDomains, tc.reqStreams, tc.reqCerts)
			if tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)) {
				t.Fatalf("expected error %q, got %v", tc.wantErr, err)
			}
			if tc.wantErr == "" && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(tc.wantDomains) > 0 && !slices.Equal(sel.Domains, tc.wantDomains) {
				t.Fatalf("expected domains %v, got %v", tc.wantDomains, sel.Domains)
			}
		})
	}
}

func assertReverseProxyDirMode(t *testing.T, dir string, wantMode os.FileMode) {
	t.Helper()

	info, err := os.Lstat(dir)
	if err != nil {
		t.Fatalf("lstat %q: %v", dir, err)
	}
	if info.Mode().Perm() != wantMode {
		t.Fatalf("expected mode %04o on %q, got %04o", wantMode, dir, info.Mode().Perm())
	}
}

func TestDeployReverseProxyWithRollback(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := t.TempDir()
	workDir := filepath.Join(root, workDirName)
	reverseProxyDir := filepath.Join(root, "reverse_proxy_stack")
	if err := os.MkdirAll(reverseProxyDir, 0o750); err != nil {
		t.Fatalf("mkdir reverse_proxy_stack: %v", err)
	}
	composeFile := filepath.Join(reverseProxyDir, "docker-compose.yaml")
	if err := os.WriteFile(composeFile, []byte("services: {}\n"), 0o600); err != nil {
		t.Fatalf("write compose: %v", err)
	}
	unpackDir := filepath.Join(root, "unpack")
	if err := os.MkdirAll(filepath.Join(unpackDir, "domains"), 0o750); err != nil {
		t.Fatalf("mkdir unpack domains: %v", err)
	}
	confName := "deploy.example.com.conf"
	if err := os.WriteFile(filepath.Join(unpackDir, "domains", confName), []byte("server {}"), 0o600); err != nil {
		t.Fatalf("write conf: %v", err)
	}

	validProvider := &config.ReverseProxyProviderConfig{
		Type:        reverseproxy.TypeAngie,
		ServiceName: "reverse-proxy-edge",
		DomainsDir:  "vhosts",
		CertsDir:    "tls",
	}

	runner := &fakeOrchRunner{}
	dockerClient := &docker.Client{Runner: runner}
	dacApplier := &dac.Applier{
		Lchown: func(_ string, _, _ int) error { return nil },
		Chmod:  os.Chmod,
		LookupUser: func(name string) (int, error) {
			if name == "root" {
				return 0, nil
			}
			return 1000, nil
		},
		LookupGroup: func(name string) (int, error) {
			if name != "reverse-proxy-linux-user" && name != "vhosts-grp" {
				return 0, errors.New("unexpected fallback DAC group " + name)
			}
			return 1000, nil
		},
	}

	tests := []struct {
		reverseProxyStack *config.StackConfig
		name              string
		wantErr           string
		checkDir          string
		sel               reverseProxySelection
		wantDirMode       os.FileMode
	}{
		{name: "empty selection", reverseProxyStack: nil, sel: reverseProxySelection{}},
		{name: "missing provider", reverseProxyStack: &config.StackConfig{}, sel: reverseProxySelection{Domains: []string{"d1.example.com"}}, wantErr: "provider is required"},
		{name: "unsupported provider", reverseProxyStack: &config.StackConfig{ReverseProxyProvider: &config.ReverseProxyProviderConfig{Type: "caddy"}}, sel: reverseProxySelection{Domains: []string{"d2.example.com"}}, wantErr: "unsupported reverse-proxy provider"},
		{name: "empty compose file", reverseProxyStack: &config.StackConfig{ReverseProxyProvider: validProvider}, sel: reverseProxySelection{Domains: []string{"d3.example.com"}}, wantErr: "compose_file is required"},
		{name: "missing compose file", reverseProxyStack: &config.StackConfig{ComposeFile: filepath.Join(root, "missing.yaml"), ReverseProxyProvider: validProvider}, sel: reverseProxySelection{Domains: []string{"d4.example.com"}}, wantErr: "not found"},
		{name: "missing stack_dir", reverseProxyStack: &config.StackConfig{StackDir: filepath.Join(root, "missing_stack"), ComposeFile: composeFile, ReverseProxyProvider: validProvider}, sel: reverseProxySelection{Domains: []string{"d5.example.com"}}, wantErr: "open reverse_proxy_stack stack_dir"},
		{name: "success deployment uses stack user for fallback DAC", reverseProxyStack: &config.StackConfig{StackDir: reverseProxyDir, ComposeFile: composeFile, User: config.UserConfig{Name: "reverse-proxy-linux-user"}, DAC: []config.DACRule{{Path: "vhosts", Owner: "root:vhosts-grp", DirMode: "0751"}}, ReverseProxyProvider: validProvider}, sel: reverseProxySelection{Domains: []string{confName}}, checkDir: filepath.Join(reverseProxyDir, "vhosts"), wantDirMode: 0o751},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := deployReverseProxyWithRollback(ctx, dockerClient, dacApplier, tc.reverseProxyStack, workDir, unpackDir, tc.sel)
			if tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)) {
				t.Fatalf("expected error containing %q, got %v", tc.wantErr, err)
			}
			if tc.wantErr == "" && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tc.checkDir != "" {
				assertReverseProxyDirMode(t, tc.checkDir, tc.wantDirMode)
			}
		})
	}
}

func TestDeployReverseProxyWithRollbackTempDirError(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	fakeDir := filepath.Join(root, "not_a_dir")
	if err := os.WriteFile(fakeDir, []byte("x"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}
	reverseProxyDir := filepath.Join(root, "reverse_proxy")
	if err := os.MkdirAll(reverseProxyDir, 0o750); err != nil {
		t.Fatalf("mkdir reverse_proxy: %v", err)
	}
	composeFile := filepath.Join(reverseProxyDir, "docker-compose.yaml")
	if err := os.WriteFile(composeFile, []byte("services: {}\n"), 0o600); err != nil {
		t.Fatalf("write compose: %v", err)
	}

	reverseProxyStack := &config.StackConfig{
		StackDir:    reverseProxyDir,
		ComposeFile: composeFile,
		ReverseProxyProvider: &config.ReverseProxyProviderConfig{
			Type:        reverseproxy.TypeAngie,
			ServiceName: "reverse-proxy-rb-err",
			DomainsDir:  "reverse-proxy-domains",
			CertsDir:    "reverse-proxy-certs",
		},
	}
	sel := reverseProxySelection{Domains: []string{"test.example.com.conf"}}
	if err := deployReverseProxyWithRollback(context.Background(), nil, nil, reverseProxyStack, fakeDir, t.TempDir(), sel); err == nil || !strings.Contains(err.Error(), "create reverse-proxy rollback directory") {
		t.Fatalf("expected create reverse-proxy rollback directory error, got %v", err)
	}
}

func TestApplySelectedReverseProxyFilesValidation(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	dacApplier := &dac.Applier{
		LookupUser: func(_ string) (int, error) { return 0, errors.New("lookup err") },
	}
	root := t.TempDir()
	reverseProxyDir := filepath.Join(root, "reverse_proxy")
	if err := os.MkdirAll(reverseProxyDir, 0o750); err != nil {
		t.Fatalf("mkdir reverse_proxy: %v", err)
	}
	if err := os.WriteFile(filepath.Join(reverseProxyDir, "file_blocker"), []byte("x"), 0o600); err != nil {
		t.Fatalf("write file_blocker: %v", err)
	}
	reverseProxyRoot, openErr := os.OpenRoot(reverseProxyDir)
	if openErr != nil {
		t.Fatalf("open reverseProxyRoot: %v", openErr)
	}
	t.Cleanup(func() { closeOrchRoot(t, reverseProxyRoot) })

	var entries []reverseProxyRollbackEntry
	reverseProxyUser := config.UserConfig{Name: "reverse-proxy-app"}
	provider := mustLookupReverseProxyProvider(t)

	tests := []struct {
		provCfg *config.ReverseProxyProviderConfig
		name    string
		wantErr string
		sel     reverseProxySelection
	}{
		{name: "missing domains_dir", provCfg: &config.ReverseProxyProviderConfig{ServiceName: "svc-a"}, sel: reverseProxySelection{Domains: []string{"app1.example.com.conf"}}, wantErr: "reverse_proxy_provider.domains_dir is required"},
		{name: "absolute domains_dir rejected", provCfg: &config.ReverseProxyProviderConfig{ServiceName: "svc-b", DomainsDir: "/etc/domains"}, sel: reverseProxySelection{Domains: []string{"app2.example.com.conf"}}, wantErr: "must stay inside stack_dir"},
		{name: "dot domains_dir rejected", provCfg: &config.ReverseProxyProviderConfig{ServiceName: "svc-c", DomainsDir: "."}, sel: reverseProxySelection{Domains: []string{"app3.example.com.conf"}}, wantErr: "must stay inside stack_dir"},
		{name: "ensure domain dir error", provCfg: &config.ReverseProxyProviderConfig{ServiceName: "svc-d", DomainsDir: "file_blocker/sub"}, sel: reverseProxySelection{Domains: []string{"app4.example.com.conf"}}, wantErr: "ensure reverse-proxy domain dir"},
		{name: "missing stream_dir", provCfg: &config.ReverseProxyProviderConfig{ServiceName: "svc-e"}, sel: reverseProxySelection{Streams: []string{"app5.example.com.inc"}}, wantErr: "reverse_proxy_provider.stream_dir is required"},
		{name: "missing certs_dir", provCfg: &config.ReverseProxyProviderConfig{ServiceName: "svc-f"}, sel: reverseProxySelection{Certs: []string{"cert1"}}, wantErr: "reverse_proxy_provider.certs_dir is required"},
		{name: "ensure certs dir error", provCfg: &config.ReverseProxyProviderConfig{ServiceName: "svc-g", CertsDir: "file_blocker/sub"}, sel: reverseProxySelection{Certs: []string{"cert2"}}, wantErr: "ensure reverse-proxy certs dir"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			reverseProxyStack := &config.StackConfig{StackDir: reverseProxyDir, ReverseProxyProvider: tc.provCfg}
			err := applySelectedReverseProxyFiles(ctx, dacApplier, provider, reverseProxyStack, reverseProxyRoot, reverseProxyUser, root, root, tc.sel, &entries)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("expected error containing %q, got %v", tc.wantErr, err)
			}
		})
	}
}

func TestDeployReverseProxyConfFilesErrors(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	stagedDir := filepath.Join(root, "staged")
	reverseProxyDir := filepath.Join(root, "reverse_proxy")
	if err := os.MkdirAll(stagedDir, 0o750); err != nil {
		t.Fatalf("mkdir staged: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(reverseProxyDir, "sites"), 0o750); err != nil {
		t.Fatalf("mkdir reverse_proxy sites: %v", err)
	}
	reverseProxyRoot, openErr := os.OpenRoot(reverseProxyDir)
	if openErr != nil {
		t.Fatalf("open reverseProxyRoot: %v", openErr)
	}
	t.Cleanup(func() { closeOrchRoot(t, reverseProxyRoot) })

	provider := mustLookupReverseProxyProvider(t)
	reverseProxyStack := &config.StackConfig{
		StackDir: reverseProxyDir,
		ReverseProxyProvider: &config.ReverseProxyProviderConfig{
			Type:        reverseproxy.TypeAngie,
			ServiceName: "gw-conf",
			DomainsDir:  "sites",
			CertsDir:    "pki",
		},
	}
	var entries []reverseProxyRollbackEntry
	reverseProxyUser := config.UserConfig{Name: "gw-conf"}
	failingDAC := &dac.Applier{
		LookupUser: func(_ string) (int, error) { return 0, errors.New("user lookup failed") },
	}

	confName := "conf-err.example.com.conf"
	if err := deployReverseProxyConfFiles(ctx, nil, provider, reverseProxyStack, reverseProxyRoot, reverseProxyUser, "sites", stagedDir, root, "domains_dir", "domain", []string{confName}, &entries); err == nil {
		t.Fatal("expected copy error when staged conf is missing")
	}
	if err := os.WriteFile(filepath.Join(stagedDir, confName), []byte("server {}"), 0o600); err != nil {
		t.Fatalf("write staged conf: %v", err)
	}
	if err := deployReverseProxyConfFiles(ctx, failingDAC, provider, reverseProxyStack, reverseProxyRoot, reverseProxyUser, "sites", stagedDir, root, "domains_dir", "domain", []string{confName}, &entries); err == nil {
		t.Fatal("expected dac error on conf deploy")
	}
	fileRollbackDir := filepath.Join(root, "file_as_rollback_dir")
	if err := os.WriteFile(fileRollbackDir, []byte("x"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(reverseProxyDir, "sites", "existing-conf.example.com.conf"), []byte("existing"), 0o600); err != nil {
		t.Fatalf("write existing conf: %v", err)
	}
	if err := deployReverseProxyConfFiles(ctx, nil, provider, reverseProxyStack, reverseProxyRoot, reverseProxyUser, "sites", stagedDir, fileRollbackDir, "domains_dir", "domain", []string{"existing-conf.example.com.conf"}, &entries); err == nil {
		t.Fatal("expected snapshot error on conf deploy")
	}

	reverseProxyStackWithParentDAC := *reverseProxyStack
	reverseProxyStackWithParentDAC.DAC = []config.DACRule{{Path: ".", Owner: "root:missing-parent-grp", DirMode: "0750", Recursive: true}}
	if err := deployReverseProxyConfFiles(ctx, failingDAC, provider, &reverseProxyStackWithParentDAC, reverseProxyRoot, reverseProxyUser, "sites", stagedDir, root, "domains_dir", "domain", []string{confName}, &entries); err == nil {
		t.Fatal("expected parent dir dac error on conf deploy")
	}
}

func TestDeployReverseProxyCertsErrors(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	reverseProxyDir := filepath.Join(root, "reverse_proxy")
	if err := os.MkdirAll(filepath.Join(reverseProxyDir, "pki"), 0o750); err != nil {
		t.Fatalf("mkdir reverse_proxy pki: %v", err)
	}
	reverseProxyRoot, openErr := os.OpenRoot(reverseProxyDir)
	if openErr != nil {
		t.Fatalf("open reverseProxyRoot: %v", openErr)
	}
	t.Cleanup(func() { closeOrchRoot(t, reverseProxyRoot) })

	provider := mustLookupReverseProxyProvider(t)
	reverseProxyStack := &config.StackConfig{
		StackDir: reverseProxyDir,
		ReverseProxyProvider: &config.ReverseProxyProviderConfig{
			Type:        reverseproxy.TypeAngie,
			ServiceName: "gw-cert",
			DomainsDir:  "sites",
			CertsDir:    "pki",
		},
	}
	var entries []reverseProxyRollbackEntry
	reverseProxyUser := config.UserConfig{Name: "gw-cert"}
	failingDAC := &dac.Applier{
		LookupUser: func(_ string) (int, error) { return 0, errors.New("user lookup failed") },
	}
	fileRollbackDir := filepath.Join(root, "file_as_rollback_dir")
	if err := os.WriteFile(fileRollbackDir, []byte("x"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}

	certName := "app-acme.example.com"
	if err := deployReverseProxyCerts(ctx, nil, provider, reverseProxyStack, reverseProxyRoot, reverseProxyUser, "pki", root, root, []string{certName}, &entries); err == nil {
		t.Fatal("expected copy error when cert src is missing")
	}
	certSrc := filepath.Join(root, "certs", certName)
	if err := os.MkdirAll(certSrc, 0o750); err != nil {
		t.Fatalf("mkdir certSrc: %v", err)
	}
	if err := deployReverseProxyCerts(ctx, failingDAC, provider, reverseProxyStack, reverseProxyRoot, reverseProxyUser, "pki", root, root, []string{certName}, &entries); err == nil {
		t.Fatal("expected dac error on cert deploy")
	}
	if err := os.Mkdir(filepath.Join(reverseProxyDir, "pki", "existing-cert.example.com"), 0o750); err != nil {
		t.Fatalf("mkdir existing cert: %v", err)
	}
	if err := deployReverseProxyCerts(ctx, nil, provider, reverseProxyStack, reverseProxyRoot, reverseProxyUser, "pki", root, fileRollbackDir, []string{"existing-cert.example.com"}, &entries); err == nil {
		t.Fatal("expected snapshot error on cert deploy")
	}

	origRmAll := rootRemoveAll
	t.Cleanup(func() { rootRemoveAll = origRmAll })
	rootRemoveAll = mockFailingRootRemoveAll
	if err := deployReverseProxyCerts(ctx, nil, provider, reverseProxyStack, reverseProxyRoot, reverseProxyUser, "pki", root, root, []string{certName}, &entries); err == nil {
		t.Fatal("expected rootRemoveAll error during cert deploy")
	}

	reverseProxyStackWithCertParentDAC := *reverseProxyStack
	reverseProxyStackWithCertParentDAC.DAC = []config.DACRule{{Path: ".", Owner: "root:missing-cert-parent-grp", DirMode: "0750", Recursive: true}}
	if err := deployReverseProxyCerts(ctx, failingDAC, provider, &reverseProxyStackWithCertParentDAC, reverseProxyRoot, reverseProxyUser, "pki", root, root, []string{certName}, &entries); err == nil {
		t.Fatal("expected parent dir dac error on cert deploy")
	}
}
