package archive

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"

	"github.com/webstudiobond/backup-recovery-orchestrator/internal/config"
)

const (
	maxManifestSize     = 8 << 20
	maxContainerEntries = 1024
	bytesPerGiB         = int64(1 << 30)
)

// ContainerLimits caps the entry count and cumulative payload bytes permitted when reading an outer TAR container.
type ContainerLimits struct {
	MaxEntries int
	MaxBytes   int64
}

// ContainerLimitsFromGB converts a configured maximum archive size in GiB into enforceable outer container limits.
func ContainerLimitsFromGB(maxArchiveGB uint64) ContainerLimits {
	if maxArchiveGB == 0 {
		maxArchiveGB = config.DefaultMaxArchiveGB
	}
	maxGB := uint64(math.MaxInt64 / bytesPerGiB)
	if maxArchiveGB > maxGB {
		return ContainerLimits{
			MaxEntries: maxContainerEntries,
			MaxBytes:   math.MaxInt64,
		}
	}
	return ContainerLimits{
		MaxEntries: maxContainerEntries,
		MaxBytes:   int64(maxArchiveGB) * bytesPerGiB,
	}
}

func normalizeContainerLimits(limits ContainerLimits) ContainerLimits {
	if limits.MaxEntries <= 0 {
		limits.MaxEntries = maxContainerEntries
	}
	if limits.MaxBytes <= 0 {
		limits.MaxBytes = int64(config.DefaultMaxArchiveGB) * bytesPerGiB
	}
	return limits
}

func cappedReadLimit(remaining int64) int64 {
	if remaining == math.MaxInt64 {
		return math.MaxInt64
	}
	return remaining + 1
}

// VerifyContainer reads manifest.json and validates SHA-256 digests of every artifact inside tarPath using default bounds.
func VerifyContainer(ctx context.Context, tarPath string) (Manifest, error) {
	return VerifyContainerWithLimits(ctx, tarPath, ContainerLimits{})
}

// VerifyContainerWithLimits reads manifest.json and validates SHA-256 digests of every artifact inside tarPath under limits.
func VerifyContainerWithLimits(ctx context.Context, tarPath string, limits ContainerLimits) (Manifest, error) {
	if err := ctx.Err(); err != nil {
		return Manifest{}, fmt.Errorf("context canceled before verifying container %q: %w", tarPath, err)
	}

	cleanTar := filepath.Clean(tarPath)
	file, err := os.Open(cleanTar)
	if err != nil {
		return Manifest{}, fmt.Errorf("open container %q: %w", cleanTar, err)
	}

	manifest, verifyErr := readAndVerifyTar(ctx, file, normalizeContainerLimits(limits))
	closeErr := file.Close()
	if combined := errors.Join(verifyErr, closeErr); combined != nil {
		return Manifest{}, combined
	}
	return manifest, nil
}

func validateContainerEntryHeader(hdr *tar.Header, remaining int64, limits ContainerLimits) error {
	if hdr.Typeflag != tar.TypeReg {
		return fmt.Errorf("unsupported non-regular tar entry %q (type %c)", hdr.Name, hdr.Typeflag)
	}
	if hdr.Size < 0 {
		return fmt.Errorf("negative size %d for tar entry %q", hdr.Size, hdr.Name)
	}
	if hdr.Size > remaining {
		return fmt.Errorf("container payload exceeds maximum allowed size of %d bytes", limits.MaxBytes)
	}
	return nil
}

func readAndVerifyTar(ctx context.Context, r io.Reader, limits ContainerLimits) (Manifest, error) {
	tr := tar.NewReader(r)
	var (
		manifest   Manifest
		hasMan     bool
		entries    int
		totalBytes int64
		actualSums = make(map[string]string)
	)

	for {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return Manifest{}, fmt.Errorf("context canceled during container verification: %w", ctxErr)
		}

		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return Manifest{}, fmt.Errorf("read container tar header: %w", err)
		}

		entries++
		if entries > limits.MaxEntries {
			return Manifest{}, fmt.Errorf("container entry count exceeds limit %d", limits.MaxEntries)
		}

		if hdr.Typeflag == tar.TypeDir {
			continue
		}
		remaining := limits.MaxBytes - totalBytes
		if valErr := validateContainerEntryHeader(hdr, remaining, limits); valErr != nil {
			return Manifest{}, valErr
		}

		cleanMember, pathErr := sanitizeTarMemberPath(hdr.Name)
		if pathErr != nil {
			return Manifest{}, pathErr
		}

		if cleanMember == ManifestFilename {
			if hasMan {
				return Manifest{}, errors.New("duplicate manifest.json in container")
			}
			parsed, readBytes, parseErr := decodeManifestEntry(tr, hdr.Size)
			if parseErr != nil {
				return Manifest{}, parseErr
			}
			totalBytes += readBytes
			manifest = parsed
			hasMan = true
			continue
		}

		digest, written, hashErr := hashContainerEntry(tr, cleanMember, remaining, limits)
		if hashErr != nil {
			return Manifest{}, hashErr
		}
		totalBytes += written
		actualSums[cleanMember] = digest
	}

	if !hasMan {
		return Manifest{}, errors.New("container missing manifest.json")
	}
	if err := compareChecksums(&manifest, actualSums); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

var hashReaderFn = hashReader

func hashContainerEntry(r io.Reader, name string, remaining int64, limits ContainerLimits) (digest string, written int64, err error) {
	digest, written, err = hashReaderFn(r, remaining)
	if err != nil {
		return "", written, fmt.Errorf("hash container entry %q: %w", name, err)
	}
	if written > remaining {
		return "", written, fmt.Errorf("container payload exceeds maximum allowed size of %d bytes", limits.MaxBytes)
	}
	return digest, written, nil
}

func decodeManifestEntry(r io.Reader, size int64) (Manifest, int64, error) {
	if size <= 0 || size > maxManifestSize {
		return Manifest{}, 0, fmt.Errorf("manifest.json size %d exceeds bounds", size)
	}

	data, err := io.ReadAll(io.LimitReader(r, maxManifestSize))
	if err != nil {
		return Manifest{}, 0, fmt.Errorf("read manifest.json: %w", err)
	}

	var manifest Manifest
	if decErr := json.Unmarshal(data, &manifest); decErr != nil {
		return Manifest{}, 0, fmt.Errorf("parse manifest.json: %w", decErr)
	}
	if manifest.SchemaVersion != ManifestSchemaVersion {
		return Manifest{}, 0, fmt.Errorf("unsupported manifest schema version %d", manifest.SchemaVersion)
	}
	return manifest, int64(len(data)), nil
}

func hashReader(r io.Reader, remaining int64) (digest string, written int64, err error) {
	hasher := sha256.New()
	written, err = io.Copy(hasher, io.LimitReader(r, cappedReadLimit(remaining)))
	if err != nil {
		return "", written, fmt.Errorf("stream tar payload: %w", err)
	}
	return hex.EncodeToString(hasher.Sum(nil)), written, nil
}

func compareChecksums(manifest *Manifest, actualSums map[string]string) error {
	for expectedPath, expectedHash := range manifest.Artifacts {
		actualHash, ok := actualSums[expectedPath]
		if !ok {
			return fmt.Errorf("manifest artifact %q is missing from container", expectedPath)
		}
		if actualHash != expectedHash {
			return fmt.Errorf("checksum mismatch for %q: expected %s, got %s", expectedPath, expectedHash, actualHash)
		}
	}

	for actualPath := range actualSums {
		if _, listed := manifest.Artifacts[actualPath]; !listed {
			return fmt.Errorf("unexpected unverified entry %q in container", actualPath)
		}
	}
	return nil
}
