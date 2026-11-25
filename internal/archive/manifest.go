package archive

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/webstudiobond/backup-recovery-orchestrator/internal/config"
)

// Manifest constants define TAR container layout standards, schema versioning, and archive classifications for verification and restore orchestration.
const (
	ManifestFilename      = "manifest.json"
	ManifestSchemaVersion = 1
	SelfBackupKind        = "self_backup"
)

// ManifestDB records metadata needed to restore a compressed database dump back to its service.
type ManifestDB struct {
	Secrets           *config.DBSecretsConfig `json:"secrets,omitempty"`
	ID                string                  `json:"id"`
	Driver            string                  `json:"driver"`
	Service           string                  `json:"service"`
	Host              string                  `json:"host,omitempty"`
	DBUser            string                  `json:"db_user,omitempty"`
	DBName            string                  `json:"db_name,omitempty"`
	ArtifactPath      string                  `json:"artifact_path"`
	UncompressedBytes int64                   `json:"uncompressed_bytes,omitempty"`
}

// ManifestHostFile records host-level files outside stack_dir and whether they were captured in the host-files archive.
type ManifestHostFile struct {
	Path     string `json:"path"`
	TarPath  string `json:"tar_path"`
	Owner    string `json:"owner,omitempty"`
	DirMode  string `json:"dir_mode,omitempty"`
	FileMode string `json:"file_mode,omitempty"`
	Optional bool   `json:"optional,omitempty"`
	Present  bool   `json:"present"`
}

// Manifest captures full restoration metadata and SHA-256 digests inside each backup container.
type Manifest struct {
	Artifacts                  map[string]string       `json:"artifacts"`
	Kind                       string                  `json:"kind,omitempty"`
	CreatedAt                  string                  `json:"created_at"`
	StackName                  string                  `json:"stack_name"`
	StackDir                   string                  `json:"stack_dir"`
	ComposeFile                string                  `json:"compose_file"`
	FilesArchive               string                  `json:"files_archive"`
	HostFilesArchive           string                  `json:"host_files_archive,omitempty"`
	Angie                      config.StackAngieConfig `json:"angie"`
	User                       config.UserConfig       `json:"user"`
	Databases                  []ManifestDB            `json:"databases,omitempty"`
	HostFiles                  []ManifestHostFile      `json:"host_files,omitempty"`
	DAC                        []config.DACRule        `json:"dac,omitempty"`
	RestoreSteps               []config.RestoreStep    `json:"restore_steps,omitempty"`
	SchemaVersion              int                     `json:"schema_version"`
	FilesUncompressedBytes     int64                   `json:"files_uncompressed_bytes,omitempty"`
	HostFilesUncompressedBytes int64                   `json:"host_files_uncompressed_bytes,omitempty"`
}

// StackFilesArchiveName returns the container member filename for compressed stack files.
func StackFilesArchiveName(stackName, stamp string) string {
	return fmt.Sprintf("%s_files_%s.tar.zst", stackName, stamp)
}

// StackHostFilesArchiveName returns the container member filename for compressed host_files.
func StackHostFilesArchiveName(stackName, stamp string) string {
	return fmt.Sprintf("%s_host_files_%s.tar.zst", stackName, stamp)
}

// StackDBArchiveName returns the database dump filename for the container, disambiguating multi-DB stacks.
func StackDBArchiveName(stackName, dbID, stamp string, totalDBs int) string {
	if totalDBs <= 1 || dbID == "" {
		return fmt.Sprintf("%s_db_%s.sql.zst", stackName, stamp)
	}
	return fmt.Sprintf("%s_db_%s_%s.sql.zst", stackName, dbID, stamp)
}

func sha256File(path string) (string, error) {
	file, err := os.Open(filepath.Clean(path))
	if err != nil {
		return "", fmt.Errorf("open %q for sha256: %w", path, err)
	}

	hasher := sha256.New()
	_, copyErr := io.Copy(hasher, file)
	closeErr := file.Close()
	if err := errors.Join(copyErr, closeErr); err != nil {
		return "", fmt.Errorf("hash %q: %w", path, err)
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}
