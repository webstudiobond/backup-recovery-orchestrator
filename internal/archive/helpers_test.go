package archive

import (
	"archive/tar"
	"bytes"
	"context"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"

	"github.com/webstudiobond/backup-recovery-orchestrator/internal/cmdexec"
	"github.com/webstudiobond/backup-recovery-orchestrator/internal/config"
)

type recordedTarCall struct {
	name string
	args []string
	env  []string
}

type fakeArchiveRunner struct {
	runErr     error
	zstdErr    error
	calls      []recordedTarCall
	tarPayload []byte
	mu         sync.Mutex
	skipWrite  bool
	writeEmpty bool
}

func (f *fakeArchiveRunner) Run(_ context.Context, spec *cmdexec.Spec) error {
	f.mu.Lock()
	f.calls = append(f.calls, recordedTarCall{
		name: spec.Name,
		args: slices.Clone(spec.Args),
		env:  slices.Clone(spec.Env),
	})
	f.mu.Unlock()

	if spec.Name == "tar" {
		if f.runErr != nil {
			return f.runErr
		}
		if spec.Stdout != nil && !f.skipWrite && !f.writeEmpty {
			payload := f.tarPayload
			if payload == nil {
				payload = []byte("raw-tar-payload")
			}
			_, err := spec.Stdout.Write(payload)
			return err
		}
		return nil
	}

	if spec.Name == "zstd" {
		if f.zstdErr != nil {
			return f.zstdErr
		}
		if f.skipWrite || f.writeEmpty {
			return nil
		}
		if spec.Stdin != nil && spec.Stdout != nil {
			_, err := io.Copy(spec.Stdout, spec.Stdin)
			return err
		}
		return nil
	}

	if f.runErr != nil {
		return f.runErr
	}
	return nil
}

func (f *fakeArchiveRunner) Output(_ context.Context, _ *cmdexec.Spec) ([]byte, error) {
	return nil, nil
}

func prepareReverseProxyFixtures(t *testing.T, root string) (config.StackConfig, config.StackReverseProxyConfig) {
	t.Helper()

	reverseProxyStackDir := filepath.Join(root, "reverse-proxy-stack")
	domainsDir := filepath.Join(reverseProxyStackDir, "data", "conf.d", "domains")
	streamDir := filepath.Join(reverseProxyStackDir, "data", "conf.d", "stream")
	certsDir := filepath.Join(reverseProxyStackDir, "data", "certs", "svc-chat")

	if err := os.MkdirAll(domainsDir, 0o750); err != nil {
		t.Fatalf("mkdir domains: %v", err)
	}
	if err := os.MkdirAll(streamDir, 0o750); err != nil {
		t.Fatalf("mkdir stream: %v", err)
	}
	if err := os.MkdirAll(certsDir, 0o700); err != nil {
		t.Fatalf("mkdir certs: %v", err)
	}
	domainFile := filepath.Join(domainsDir, "chat.example.com.conf")
	if err := os.WriteFile(domainFile, []byte("server {}"), 0o600); err != nil {
		t.Fatalf("write domain conf: %v", err)
	}
	if err := os.Chmod(domainFile, 0o400); err != nil {
		t.Fatalf("chmod domain conf: %v", err)
	}
	streamFile := filepath.Join(streamDir, "rtc-turn.inc")
	if err := os.WriteFile(streamFile, []byte("server { listen 5349; }"), 0o600); err != nil {
		t.Fatalf("write stream conf: %v", err)
	}
	if err := os.WriteFile(filepath.Join(certsDir, "certificate.pem"), []byte("CERT"), 0o600); err != nil {
		t.Fatalf("write cert: %v", err)
	}

	return config.StackConfig{
		Name:     "reverse-proxy-host",
		StackDir: reverseProxyStackDir,
		ReverseProxyProvider: &config.ReverseProxyProviderConfig{
			Type:        "angie",
			ServiceName: "edge-proxy",
			DomainsDir:  "data/conf.d/domains",
			StreamDir:   "data/conf.d/stream",
			CertsDir:    "data/certs",
		},
	}, config.StackReverseProxyConfig{
		ACMENames:   []string{"svc-chat"},
		DomainConfs: []string{"chat.example.com.conf"},
		StreamConfs: []string{"rtc-turn.inc"},
	}
}

func writeRawTar(t *testing.T, tarPath string, entries map[string]string) {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for name, body := range entries {
		hdr := &tar.Header{
			Name:     name,
			Mode:     0o600,
			Size:     int64(len(body)),
			Typeflag: tar.TypeReg,
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("write header %q: %v", name, err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatalf("write body %q: %v", name, err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
	if err := os.WriteFile(tarPath, buf.Bytes(), 0o600); err != nil {
		t.Fatalf("write tar %q: %v", tarPath, err)
	}
}

type failAfterBytesWriter struct {
	err      error
	maxBytes int
	written  int
}

func (f *failAfterBytesWriter) Write(p []byte) (n int, err error) {
	if f.written+len(p) > f.maxBytes {
		return 0, f.err
	}
	f.written += len(p)
	return len(p), nil
}

type fakeDirEntry struct {
	infoErr error
	info    fs.FileInfo
	name    string
	mode    fs.FileMode
	isDir   bool
}

func (f fakeDirEntry) Name() string               { return f.name }
func (f fakeDirEntry) IsDir() bool                { return f.isDir }
func (f fakeDirEntry) Type() fs.FileMode          { return f.mode }
func (f fakeDirEntry) Info() (fs.FileInfo, error) { return f.info, f.infoErr }

type failingReader struct {
	err error
}

func (f *failingReader) Read(_ []byte) (n int, err error) {
	return 0, f.err
}

type customTarEntry struct {
	header *tar.Header
	body   []byte
}

func makeCustomTarBytes(t *testing.T, entries []customTarEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, entry := range entries {
		if err := tw.WriteHeader(entry.header); err != nil {
			t.Fatalf("write header %q: %v", entry.header.Name, err)
		}
		if len(entry.body) > 0 {
			if _, err := tw.Write(entry.body); err != nil {
				t.Fatalf("write body %q: %v", entry.header.Name, err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
	return buf.Bytes()
}

type cancelAfterBytesReader struct {
	r      io.Reader
	cancel context.CancelFunc
	remain int
}

func (c *cancelAfterBytesReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.remain -= n
	if c.remain <= 0 && c.cancel != nil {
		c.cancel()
		c.cancel = nil
	}
	return n, err
}
