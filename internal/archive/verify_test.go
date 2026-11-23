package archive

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"testing"
)

func TestVerifyContainer(t *testing.T) {
	t.Parallel()

	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()

	validPayload := []byte("sample container content")
	validDigest := sha256.Sum256(validPayload)
	validManifest := fmt.Sprintf(`{"schema_version":1,"artifacts":{"sample.txt":%q}}`, hex.EncodeToString(validDigest[:]))

	cases := []struct {
		entries     map[string]string
		ctx         context.Context
		name        string
		errContains string
		skipWrite   bool
	}{
		{
			ctx:         canceledCtx,
			name:        "canceled context",
			errContains: "context canceled before verifying container",
			skipWrite:   true,
		},
		{
			ctx:         context.Background(),
			name:        "nonexistent tar file",
			errContains: "open container",
			skipWrite:   true,
		},
		{
			entries:     map[string]string{"site1_files_2026-11-16_10-00-00.tar.zst": "abc"},
			ctx:         context.Background(),
			name:        "missing manifest",
			errContains: "missing manifest.json",
		},
		{
			entries: map[string]string{
				ManifestFilename:                        `{"schema_version":1,"artifacts":{"app_files_2026-11-16_11-00-00.tar.zst":"0000"}}`,
				"app_files_2026-11-16_11-00-00.tar.zst": "corrupted",
			},
			ctx:         context.Background(),
			name:        "checksum mismatch container entry",
			errContains: "checksum mismatch",
		},
		{
			entries: map[string]string{
				ManifestFilename: `{"schema_version":1,"artifacts":{}}`,
				"rogue.txt":      "payload",
			},
			ctx:         context.Background(),
			name:        "unexpected unverified file",
			errContains: "unverified entry",
		},
		{
			entries: map[string]string{
				"../escape.txt": "bad",
			},
			ctx:         context.Background(),
			name:        "path traversal member",
			errContains: "unsafe archive member path",
		},
		{
			entries: map[string]string{
				ManifestFilename: `{"schema_version":1,"artifacts":{"expected_absent.tar.zst":"abcd"}}`,
			},
			ctx:         context.Background(),
			name:        "missing artifact from container",
			errContains: "is missing from container",
		},
		{
			entries: map[string]string{
				ManifestFilename: validManifest,
				"sample.txt":     string(validPayload),
			},
			ctx:  context.Background(),
			name: "valid container success",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			tarPath := filepath.Join(t.TempDir(), "target.tar")
			if !tc.skipWrite {
				writeRawTar(t, tarPath, tc.entries)
			}

			manifest, err := VerifyContainer(tc.ctx, tarPath)
			if tc.errContains != "" {
				if err == nil || !strings.Contains(err.Error(), tc.errContains) {
					t.Fatalf("expected error containing %q, got %v", tc.errContains, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if manifest.SchemaVersion != ManifestSchemaVersion {
				t.Fatalf("unexpected schema version: %d", manifest.SchemaVersion)
			}
		})
	}
}

func TestReadAndVerifyTarStreamErrors(t *testing.T) {
	t.Parallel()

	t.Run("canceled context during verification", func(t *testing.T) {
		t.Parallel()

		var buf bytes.Buffer
		tw := tar.NewWriter(&buf)
		manBytes := []byte(`{"schema_version":1,"artifacts":{}}`)
		hdrMan := &tar.Header{Name: ManifestFilename, Mode: 0o600, Size: int64(len(manBytes)), Typeflag: tar.TypeReg}
		if err := tw.WriteHeader(hdrMan); err != nil {
			t.Fatalf("write man header: %v", err)
		}
		if _, err := tw.Write(manBytes); err != nil {
			t.Fatalf("write man: %v", err)
		}
		nextBytes := []byte("next")
		hdrFile := &tar.Header{Name: "next.txt", Mode: 0o600, Size: int64(len(nextBytes)), Typeflag: tar.TypeReg}
		if err := tw.WriteHeader(hdrFile); err != nil {
			t.Fatalf("write next header: %v", err)
		}
		if _, err := tw.Write(nextBytes); err != nil {
			t.Fatalf("write next: %v", err)
		}
		if err := tw.Close(); err != nil {
			t.Fatalf("close tar: %v", err)
		}

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		r := &cancelAfterBytesReader{
			r:      bytes.NewReader(buf.Bytes()),
			cancel: cancel,
			remain: len(buf.Bytes()) / 2,
		}

		_, err := readAndVerifyTar(ctx, r)
		if err == nil || !strings.Contains(err.Error(), "context canceled during container verification") {
			t.Fatalf("expected context canceled error, got %v", err)
		}
	})

	t.Run("read container tar header error", func(t *testing.T) {
		t.Parallel()

		_, err := readAndVerifyTar(context.Background(), &failingReader{err: errors.New("read header fail")})
		if err == nil || !strings.Contains(err.Error(), "read container tar header") {
			t.Fatalf("expected header error, got %v", err)
		}
	})

	t.Run("hash entry error on stream failure", func(t *testing.T) {
		t.Parallel()

		var buf bytes.Buffer
		tw := tar.NewWriter(&buf)
		if err := tw.WriteHeader(&tar.Header{Name: "data.bin", Mode: 0o600, Size: 1024, Typeflag: tar.TypeReg}); err != nil {
			t.Fatalf("write header: %v", err)
		}

		r := io.MultiReader(bytes.NewReader(buf.Bytes()), &failingReader{err: errors.New("stream fault")})
		_, err := readAndVerifyTar(context.Background(), r)
		if err == nil || !strings.Contains(err.Error(), "hash container entry") {
			t.Fatalf("expected hash error, got %v", err)
		}
	})
}

func TestReadAndVerifyTarEntries(t *testing.T) {
	t.Parallel()

	t.Run("directory entry is skipped and non regular entry rejected", func(t *testing.T) {
		t.Parallel()

		data := []byte("payload")
		digest := sha256.Sum256(data)
		manBody := fmt.Sprintf(`{"schema_version":1,"artifacts":{"sub/item.txt":%q}}`, hex.EncodeToString(digest[:]))

		rawTar := makeCustomTarBytes(t, []customTarEntry{
			{header: &tar.Header{Name: "sub/", Mode: 0o755, Typeflag: tar.TypeDir}},
			{header: &tar.Header{Name: ManifestFilename, Mode: 0o600, Size: int64(len(manBody)), Typeflag: tar.TypeReg}, body: []byte(manBody)},
			{header: &tar.Header{Name: "sub/item.txt", Mode: 0o600, Size: int64(len(data)), Typeflag: tar.TypeReg}, body: data},
		})

		manifest, err := readAndVerifyTar(context.Background(), bytes.NewReader(rawTar))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if manifest.SchemaVersion != ManifestSchemaVersion {
			t.Fatalf("expected schema version 1, got %d", manifest.SchemaVersion)
		}
	})

	t.Run("unsupported non regular entry rejected", func(t *testing.T) {
		t.Parallel()

		rawTar := makeCustomTarBytes(t, []customTarEntry{
			{header: &tar.Header{Name: "symlink.txt", Mode: 0o600, Typeflag: tar.TypeSymlink, Linkname: "target"}},
		})

		_, err := readAndVerifyTar(context.Background(), bytes.NewReader(rawTar))
		if err == nil || !strings.Contains(err.Error(), "unsupported non-regular tar entry") {
			t.Fatalf("expected unsupported non-regular error, got %v", err)
		}
	})

	t.Run("duplicate manifest rejected", func(t *testing.T) {
		t.Parallel()

		manBody := []byte(`{"schema_version":1,"artifacts":{}}`)
		rawTar := makeCustomTarBytes(t, []customTarEntry{
			{header: &tar.Header{Name: ManifestFilename, Mode: 0o600, Size: int64(len(manBody)), Typeflag: tar.TypeReg}, body: manBody},
			{header: &tar.Header{Name: ManifestFilename, Mode: 0o600, Size: int64(len(manBody)), Typeflag: tar.TypeReg}, body: manBody},
		})

		_, err := readAndVerifyTar(context.Background(), bytes.NewReader(rawTar))
		if err == nil || !strings.Contains(err.Error(), "duplicate manifest.json in container") {
			t.Fatalf("expected duplicate manifest error, got %v", err)
		}
	})

	t.Run("decode manifest error", func(t *testing.T) {
		t.Parallel()

		badJSON := []byte("{bad-json")
		rawTar := makeCustomTarBytes(t, []customTarEntry{
			{header: &tar.Header{Name: ManifestFilename, Mode: 0o600, Size: int64(len(badJSON)), Typeflag: tar.TypeReg}, body: badJSON},
		})

		_, err := readAndVerifyTar(context.Background(), bytes.NewReader(rawTar))
		if err == nil || !strings.Contains(err.Error(), "parse manifest.json") {
			t.Fatalf("expected parse manifest error, got %v", err)
		}
	})
}

func TestDecodeManifestEntry(t *testing.T) {
	t.Parallel()

	cases := []struct {
		r           io.Reader
		name        string
		errContains string
		size        int64
		expectErr   bool
	}{
		{
			r:         strings.NewReader(""),
			name:      "size zero exceeds bounds",
			size:      0,
			expectErr: true,
		},
		{
			r:         strings.NewReader(""),
			name:      "negative size exceeds bounds",
			size:      -1,
			expectErr: true,
		},
		{
			r:         strings.NewReader(""),
			name:      "size larger than maximum limit",
			size:      maxManifestSize + 1,
			expectErr: true,
		},
		{
			r:           &failingReader{err: errors.New("read failed")},
			name:        "failing reader returns read error",
			errContains: "read manifest.json",
			size:        100,
			expectErr:   true,
		},
		{
			r:           strings.NewReader("{invalid-json"),
			name:        "invalid json returns parse error",
			errContains: "parse manifest.json",
			size:        13,
			expectErr:   true,
		},
		{
			r:           strings.NewReader(`{"schema_version":999}`),
			name:        "unsupported schema version",
			errContains: "unsupported manifest schema version 999",
			size:        22,
			expectErr:   true,
		},
		{
			r:         strings.NewReader(`{"schema_version":1,"created_at":"2026-11-16T10:00:00Z"}`),
			name:      "valid manifest decoded successfully",
			size:      54,
			expectErr: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			expectedErr := tc.errContains
			if expectedErr == "" && tc.expectErr {
				expectedErr = "exceeds bounds"
			}

			manifest, err := decodeManifestEntry(tc.r, tc.size)
			if tc.expectErr {
				if err == nil || !strings.Contains(err.Error(), expectedErr) {
					t.Fatalf("expected error containing %q, got %v", expectedErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if manifest.SchemaVersion != ManifestSchemaVersion {
				t.Fatalf("expected schema version %d, got %d", ManifestSchemaVersion, manifest.SchemaVersion)
			}
		})
	}
}

func TestHashReader(t *testing.T) {
	t.Parallel()

	cases := []struct {
		r           io.Reader
		name        string
		expected    string
		errContains string
		expectErr   bool
	}{
		{
			r:           &failingReader{err: errors.New("stream failure")},
			name:        "failing reader returns stream error",
			errContains: "stream tar payload",
			expectErr:   true,
		},
		{
			r:         strings.NewReader("hello-world"),
			name:      "valid data returns correct hex digest",
			expected:  "afa27b44d43b02a9fea41d13cedc2e4016cfcf87c5dbf990e593669aa8ce286d",
			expectErr: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			digest, err := hashReader(tc.r)
			if tc.expectErr {
				if err == nil || !strings.Contains(err.Error(), tc.errContains) {
					t.Fatalf("expected error containing %q, got %v", tc.errContains, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if digest != tc.expected {
				t.Fatalf("expected digest %s, got %s", tc.expected, digest)
			}
		})
	}
}

func TestCompareChecksums(t *testing.T) {
	t.Parallel()

	cases := []struct {
		manifest    *Manifest
		actualSums  map[string]string
		name        string
		errContains string
		expectErr   bool
	}{
		{
			manifest: &Manifest{
				Artifacts: map[string]string{"db_backup_alpha.sql.zst": "hash1"},
			},
			actualSums:  map[string]string{},
			name:        "missing artifact from actual sums",
			errContains: "is missing from container",
			expectErr:   true,
		},
		{
			manifest: &Manifest{
				Artifacts: map[string]string{"db_backup_beta.sql.zst": "hash1"},
			},
			actualSums:  map[string]string{"db_backup_beta.sql.zst": "hash2"},
			name:        "checksum mismatch between actual and manifest",
			errContains: "checksum mismatch for",
			expectErr:   true,
		},
		{
			manifest: &Manifest{
				Artifacts: map[string]string{},
			},
			actualSums:  map[string]string{"extra_file.txt": "hash3"},
			name:        "unexpected unverified entry",
			errContains: "unexpected unverified entry",
			expectErr:   true,
		},
		{
			manifest: &Manifest{
				Artifacts: map[string]string{"file.txt": "hash4"},
			},
			actualSums: map[string]string{"file.txt": "hash4"},
			name:       "all checksums match exactly",
			expectErr:  false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := compareChecksums(tc.manifest, tc.actualSums)
			if tc.expectErr {
				if err == nil || !strings.Contains(err.Error(), tc.errContains) {
					t.Fatalf("expected error containing %q, got %v", tc.errContains, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}
