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
	"os"
	"path/filepath"
)

const maxManifestSize = 8 << 20

// VerifyContainer reads manifest.json and validates SHA-256 digests of every artifact inside tarPath.
func VerifyContainer(ctx context.Context, tarPath string) (Manifest, error) {
	if err := ctx.Err(); err != nil {
		return Manifest{}, fmt.Errorf("context canceled before verifying container %q: %w", tarPath, err)
	}

	cleanTar := filepath.Clean(tarPath)
	file, err := os.Open(cleanTar)
	if err != nil {
		return Manifest{}, fmt.Errorf("open container %q: %w", cleanTar, err)
	}

	manifest, verifyErr := readAndVerifyTar(ctx, file)
	closeErr := file.Close()
	if combined := errors.Join(verifyErr, closeErr); combined != nil {
		return Manifest{}, combined
	}
	return manifest, nil
}

func readAndVerifyTar(ctx context.Context, r io.Reader) (Manifest, error) {
	tr := tar.NewReader(r)
	var (
		manifest   Manifest
		hasMan     bool
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

		if hdr.Typeflag == tar.TypeDir {
			continue
		}
		if hdr.Typeflag != tar.TypeReg {
			return Manifest{}, fmt.Errorf("unsupported non-regular tar entry %q (type %c)", hdr.Name, hdr.Typeflag)
		}

		cleanMember, pathErr := sanitizeTarMemberPath(hdr.Name)
		if pathErr != nil {
			return Manifest{}, pathErr
		}

		if cleanMember == ManifestFilename {
			if hasMan {
				return Manifest{}, errors.New("duplicate manifest.json in container")
			}
			parsed, parseErr := decodeManifestEntry(tr, hdr.Size)
			if parseErr != nil {
				return Manifest{}, parseErr
			}
			manifest = parsed
			hasMan = true
			continue
		}

		digest, hashErr := hashReader(tr)
		if hashErr != nil {
			return Manifest{}, fmt.Errorf("hash container entry %q: %w", cleanMember, hashErr)
		}
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

func decodeManifestEntry(r io.Reader, size int64) (Manifest, error) {
	if size <= 0 || size > maxManifestSize {
		return Manifest{}, fmt.Errorf("manifest.json size %d exceeds bounds", size)
	}

	data, err := io.ReadAll(io.LimitReader(r, maxManifestSize))
	if err != nil {
		return Manifest{}, fmt.Errorf("read manifest.json: %w", err)
	}

	var manifest Manifest
	if decErr := json.Unmarshal(data, &manifest); decErr != nil {
		return Manifest{}, fmt.Errorf("parse manifest.json: %w", decErr)
	}
	if manifest.SchemaVersion != ManifestSchemaVersion {
		return Manifest{}, fmt.Errorf("unsupported manifest schema version %d", manifest.SchemaVersion)
	}
	return manifest, nil
}

func hashReader(r io.Reader) (string, error) {
	hasher := sha256.New()
	if _, err := io.Copy(hasher, r); err != nil {
		return "", fmt.Errorf("stream tar payload: %w", err)
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
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
