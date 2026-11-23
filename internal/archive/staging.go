package archive

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/webstudiobond/backup-recovery-orchestrator/internal/config"
)

var ioCopy = io.Copy

// StageAngieAssets copies configured Angie domain configs, stream configs, and certificate directories into stagingDir.
func StageAngieAssets(
	ctx context.Context,
	globalAngie *config.GlobalAngieConfig,
	stackAngie *config.StackAngieConfig,
	stagingDir string,
) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("context canceled before staging Angie assets: %w", err)
	}
	if len(stackAngie.DomainConfs) == 0 && len(stackAngie.StreamConfs) == 0 && len(stackAngie.ACMENames) == 0 {
		return nil
	}

	if err := stageAngieConfFiles(globalAngie.DomainsDir, stackAngie.DomainConfs, filepath.Join(stagingDir, "domains"), "domains_dir", "domain"); err != nil {
		return err
	}
	if err := stageAngieConfFiles(globalAngie.ResolveStreamDir(), stackAngie.StreamConfs, filepath.Join(stagingDir, "stream"), "stream_dir", "stream"); err != nil {
		return err
	}
	return stageAngieCerts(globalAngie.CertsDir, stackAngie.ACMENames, filepath.Join(stagingDir, "certs"))
}

func stageAngieConfFiles(srcDir string, confNames []string, dstDir, dirField, kind string) error {
	if len(confNames) == 0 {
		return nil
	}
	if strings.TrimSpace(srcDir) == "" {
		return fmt.Errorf("global angie.%s is required when stack defines %s configs", dirField, kind)
	}
	if err := os.MkdirAll(dstDir, 0o750); err != nil {
		return fmt.Errorf("create staged %s dir %q: %w", kind, dstDir, err)
	}

	for _, confName := range confNames {
		src := filepath.Join(srcDir, confName)
		dst := filepath.Join(dstDir, confName)
		if err := copyRegularFile(src, dst); err != nil {
			return fmt.Errorf("stage angie %s config %q: %w", kind, src, err)
		}
	}
	return nil
}

func stageAngieCerts(certsDir string, acmeNames []string, dstBaseDir string) error {
	if len(acmeNames) == 0 {
		return nil
	}
	if strings.TrimSpace(certsDir) == "" {
		return errors.New("global angie.certs_dir is required when stack defines acme_names")
	}

	for _, acme := range acmeNames {
		srcDir := filepath.Join(certsDir, acme)
		info, err := os.Stat(srcDir)
		if err != nil || !info.IsDir() {
			return fmt.Errorf("angie certificate directory %q not found: %w", srcDir, err)
		}
		dstDir := filepath.Join(dstBaseDir, acme)
		if copyErr := copyDirTree(srcDir, dstDir); copyErr != nil {
			return fmt.Errorf("stage angie certificates %q: %w", srcDir, copyErr)
		}
	}
	return nil
}

func copyDirTree(srcDir, dstDir string) error {
	cleanSrc := filepath.Clean(srcDir)
	if err := walkDir(cleanSrc, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("refusing symlink %q while staging", path)
		}

		rel, relErr := filepath.Rel(cleanSrc, path)
		if relErr != nil {
			return fmt.Errorf("relative path for %q: %w", path, relErr)
		}
		target := filepath.Join(dstDir, rel)

		if entry.IsDir() {
			info, infoErr := entry.Info()
			if infoErr != nil {
				return fmt.Errorf("stat dir %q: %w", path, infoErr)
			}
			if err := os.MkdirAll(target, info.Mode().Perm()); err != nil {
				return fmt.Errorf("mkdir %q: %w", target, err)
			}
			return nil
		}

		return copyRegularFile(path, target)
	}); err != nil {
		return fmt.Errorf("copy directory tree %q: %w", cleanSrc, err)
	}
	return nil
}

func copyRegularFile(src, dst string) error {
	cleanSrc := filepath.Clean(src)
	info, statErr := os.Lstat(cleanSrc)
	if statErr != nil {
		return fmt.Errorf("stat %q: %w", cleanSrc, statErr)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("refusing symlink %q", cleanSrc)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("expected regular file at %q", cleanSrc)
	}
	if mkErr := os.MkdirAll(filepath.Dir(dst), 0o750); mkErr != nil {
		return fmt.Errorf("mkdir parent for %q: %w", dst, mkErr)
	}

	in, openErr := os.Open(cleanSrc)
	if openErr != nil {
		return fmt.Errorf("open %q: %w", cleanSrc, openErr)
	}

	out, createErr := os.OpenFile(filepath.Clean(dst), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode().Perm())
	if createErr != nil {
		closeErr := in.Close()
		return errors.Join(fmt.Errorf("create %q: %w", dst, createErr), closeErr)
	}

	_, copyErr := ioCopy(out, in)
	chmodErr := out.Chmod(info.Mode().Perm())
	closeOutErr := out.Close()
	closeInErr := in.Close()
	if err := errors.Join(copyErr, chmodErr, closeOutErr, closeInErr); err != nil {
		return fmt.Errorf("copy %q to %q: %w", cleanSrc, dst, err)
	}
	return nil
}
