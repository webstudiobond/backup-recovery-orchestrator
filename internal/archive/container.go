package archive

import (
	"archive/tar"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

var (
	jsonMarshalIndent = json.MarshalIndent
	sha256FileFn      = sha256File
	walkDir           = filepath.WalkDir
	osRemove          = os.Remove
)

// AssembleContainer hashes all staged artifacts, writes manifest.json, and packages an uncompressed TAR container.
func (e *Engine) AssembleContainer(ctx context.Context, manifest *Manifest, stagingDir, targetTarPath string) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("context canceled before assembling container: %w", err)
	}
	if manifest == nil {
		return errors.New("manifest must not be nil")
	}

	nowFn := e.Now
	if nowFn == nil {
		nowFn = time.Now
	}
	now := nowFn()
	if manifest.SchemaVersion == 0 {
		manifest.SchemaVersion = ManifestSchemaVersion
	}
	if manifest.CreatedAt == "" {
		manifest.CreatedAt = now.Format(time.RFC3339)
	}

	stagedFiles, err := collectStagedFiles(stagingDir)
	if err != nil {
		return err
	}

	manifest.Artifacts = make(map[string]string, len(stagedFiles))
	for _, item := range stagedFiles {
		digest, hashErr := sha256FileFn(item.absPath)
		if hashErr != nil {
			return hashErr
		}
		manifest.Artifacts[item.relPath] = digest
	}

	manifestBytes, marshalErr := jsonMarshalIndent(manifest, "", "  ")
	if marshalErr != nil {
		return fmt.Errorf("marshal manifest.json: %w", marshalErr)
	}
	manifestBytes = append(manifestBytes, '\n')

	if mkErr := os.MkdirAll(filepath.Dir(targetTarPath), 0o700); mkErr != nil {
		return fmt.Errorf("create target backup directory: %w", mkErr)
	}

	return writeUncompressedTar(targetTarPath, manifestBytes, stagedFiles, now)
}

type stagedFile struct {
	absPath string
	relPath string
	size    int64
	mode    int64
}

func collectStagedFiles(stagingDir string) ([]stagedFile, error) {
	cleanRoot := filepath.Clean(stagingDir)
	var files []stagedFile

	err := walkDir(cleanRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("unexpected symlink %q in staging directory", path)
		}

		info, infoErr := entry.Info()
		if infoErr != nil {
			return fmt.Errorf("stat staged file %q: %w", path, infoErr)
		}
		rel, relErr := filepath.Rel(cleanRoot, path)
		if relErr != nil {
			return fmt.Errorf("compute relative path for %q: %w", path, relErr)
		}
		slashRel := filepath.ToSlash(rel)
		if slashRel == ManifestFilename {
			return nil
		}

		files = append(files, stagedFile{
			absPath: path,
			relPath: slashRel,
			size:    info.Size(),
			mode:    int64(info.Mode().Perm()),
		})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk staging directory %q: %w", stagingDir, err)
	}

	slices.SortFunc(files, func(a, b stagedFile) int {
		return strings.Compare(a.relPath, b.relPath)
	})
	return files, nil
}

func writeUncompressedTar(targetTarPath string, manifestBytes []byte, files []stagedFile, modTime time.Time) error {
	cleanTarget := filepath.Clean(targetTarPath)
	outFile, err := os.OpenFile(cleanTarget, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("create container archive %q: %w", cleanTarget, err)
	}

	tw := tar.NewWriter(outFile)
	writeErr := writeManifestEntry(tw, manifestBytes, modTime)
	if writeErr == nil {
		for _, item := range files {
			if err := writeStagedFileEntry(tw, item, modTime); err != nil {
				writeErr = err
				break
			}
		}
	}

	tarCloseErr := tw.Close()
	syncErr := outFile.Sync()
	chmodErr := outFile.Chmod(0o600)
	fileCloseErr := outFile.Close()

	if combined := errors.Join(writeErr, tarCloseErr, syncErr, chmodErr, fileCloseErr); combined != nil {
		if rmErr := osRemove(cleanTarget); rmErr != nil && !errors.Is(rmErr, os.ErrNotExist) {
			return errors.Join(combined, rmErr)
		}
		return fmt.Errorf("write container %q: %w", cleanTarget, combined)
	}

	return ensureNonEmptyFile(cleanTarget)
}

func writeManifestEntry(tw *tar.Writer, manifestBytes []byte, modTime time.Time) error {
	hdr := &tar.Header{
		Name:     ManifestFilename,
		Mode:     0o600,
		Size:     int64(len(manifestBytes)),
		Typeflag: tar.TypeReg,
		ModTime:  modTime,
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return fmt.Errorf("write manifest header: %w", err)
	}
	if _, err := tw.Write(manifestBytes); err != nil {
		return fmt.Errorf("write manifest body: %w", err)
	}
	return nil
}

func writeStagedFileEntry(tw *tar.Writer, item stagedFile, modTime time.Time) error {
	hdr := &tar.Header{
		Name:     item.relPath,
		Mode:     item.mode,
		Size:     item.size,
		Typeflag: tar.TypeReg,
		ModTime:  modTime,
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return fmt.Errorf("write tar header for %q: %w", item.relPath, err)
	}

	src, openErr := os.Open(filepath.Clean(item.absPath))
	if openErr != nil {
		return fmt.Errorf("open staged file %q: %w", item.absPath, openErr)
	}

	_, copyErr := io.Copy(tw, src)
	closeErr := src.Close()
	if err := errors.Join(copyErr, closeErr); err != nil {
		return fmt.Errorf("copy staged file %q to container: %w", item.relPath, err)
	}
	return nil
}
