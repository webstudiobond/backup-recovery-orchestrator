package archive

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

func TestArchiveNames(t *testing.T) {
	t.Parallel()

	const (
		stack = "app"
		stamp = "2026-11-22_10-00-00"
	)

	if got := StackFilesArchiveName(stack, stamp); got != "app_files_2026-11-22_10-00-00.tar.zst" {
		t.Fatalf("StackFilesArchiveName mismatch: got %q", got)
	}

	if got := StackHostFilesArchiveName(stack, stamp); got != "app_host_files_2026-11-22_10-00-00.tar.zst" {
		t.Fatalf("StackHostFilesArchiveName mismatch: got %q", got)
	}

	dbTests := []struct {
		name     string
		dbID     string
		want     string
		totalDBs int
	}{
		{
			name:     "single_db_empty_id",
			dbID:     "",
			totalDBs: 1,
		},
		{
			name:     "single_db_with_id",
			dbID:     "primary",
			totalDBs: 1,
		},
		{
			name:     "multi_db_empty_id",
			dbID:     "",
			totalDBs: 3,
		},
		{
			name:     "multi_db_with_id",
			dbID:     "replica",
			totalDBs: 2,
			want:     "app_db_replica_2026-11-22_10-00-00.sql.zst",
		},
		{
			name:     "zero_total_db_with_id",
			dbID:     "standalone",
			totalDBs: 0,
		},
	}

	for _, tt := range dbTests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			want := tt.want
			if want == "" {
				want = "app_db_2026-11-22_10-00-00.sql.zst"
			}
			got := StackDBArchiveName(stack, tt.dbID, stamp, tt.totalDBs)
			if got != want {
				t.Fatalf("StackDBArchiveName(%q, %q, %q, %d) = %q, want %q", stack, tt.dbID, stamp, tt.totalDBs, got, want)
			}
		})
	}
}

func TestSHA256File(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	validContent := []byte("sha256 calculation test payload")
	validFile := filepath.Join(root, "valid.bin")
	if err := os.WriteFile(validFile, validContent, 0o600); err != nil {
		t.Fatalf("write valid file: %v", err)
	}

	expectedHasher := sha256.New()
	expectedHasher.Write(validContent)
	expectedHash := hex.EncodeToString(expectedHasher.Sum(nil))

	dirPath := filepath.Join(root, "test_dir")
	if err := os.MkdirAll(dirPath, 0o750); err != nil {
		t.Fatalf("mkdir test_dir: %v", err)
	}

	tests := []struct {
		name     string
		path     string
		wantHash string
		wantErr  bool
	}{
		{
			name:     "valid_file_hash",
			path:     validFile,
			wantHash: expectedHash,
			wantErr:  false,
		},
		{
			name:     "nonexistent_file_error",
			path:     filepath.Join(root, "nonexistent.bin"),
			wantHash: "",
			wantErr:  true,
		},
		{
			name:     "directory_read_error",
			path:     dirPath,
			wantHash: "",
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := sha256File(tt.path)
			if (err != nil) != tt.wantErr {
				t.Fatalf("sha256File(%q) error = %v, wantErr = %v", tt.path, err, tt.wantErr)
			}
			if !tt.wantErr && got != tt.wantHash {
				t.Fatalf("sha256File(%q) = %q, want %q", tt.path, got, tt.wantHash)
			}
		})
	}
}
