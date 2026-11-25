package archive

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/webstudiobond/backup-recovery-orchestrator/internal/config"
)

var (
	ioCopy                = io.Copy
	walkRootFS            = fs.WalkDir
	rootOpenSubRoot       = (*os.Root).OpenRoot
	closeReverseProxyRoot = (*os.Root).Close
)

// StageReverseProxyAssets copies configured reverse-proxy domain configs, stream configs, and certificate directories into stagingDir.
func StageReverseProxyAssets(
	ctx context.Context,
	reverseProxyStack *config.StackConfig,
	stackReverseProxy *config.StackReverseProxyConfig,
	stagingDir string,
) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("context canceled before staging reverse-proxy assets: %w", err)
	}
	if !stackReverseProxy.HasAssets() {
		return nil
	}
	if reverseProxyStack == nil || reverseProxyStack.ReverseProxyProvider == nil {
		return errors.New("global reverse_proxy_stack provider is required when stack defines reverse-proxy assets")
	}

	reverseProxyRoot, rootErr := openRoot(filepath.Clean(reverseProxyStack.StackDir))
	if rootErr != nil {
		return fmt.Errorf("open reverse-proxy stack_dir %q: %w", reverseProxyStack.StackDir, rootErr)
	}

	provider := reverseProxyStack.ReverseProxyProvider
	stageErr := stageReverseProxyConfFiles(reverseProxyRoot, provider.DomainsDir, stackReverseProxy.DomainConfs, filepath.Join(stagingDir, "domains"), "domains_dir", "domain")
	if stageErr == nil {
		stageErr = stageReverseProxyConfFiles(reverseProxyRoot, provider.StreamDir, stackReverseProxy.StreamConfs, filepath.Join(stagingDir, "stream"), "stream_dir", "stream")
	}
	if stageErr == nil {
		stageErr = stageReverseProxyCerts(reverseProxyRoot, provider.CertsDir, stackReverseProxy.ACMENames, filepath.Join(stagingDir, "certs"))
	}
	return errors.Join(stageErr, reverseProxyRoot.Close())
}

func openReverseProxySubRoot(reverseProxyRoot *os.Root, rawRel, field string) (*os.Root, error) {
	trimmed := strings.TrimSpace(rawRel)
	if trimmed == "" {
		return nil, fmt.Errorf("reverse_proxy_provider.%s is required", field)
	}
	if strings.Contains(trimmed, "\x00") || filepath.IsAbs(trimmed) || !filepath.IsLocal(filepath.FromSlash(trimmed)) {
		return nil, fmt.Errorf("reverse_proxy_provider.%s %q must stay inside stack_dir", field, rawRel)
	}
	cleaned := path.Clean(filepath.ToSlash(trimmed))
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return nil, fmt.Errorf("reverse_proxy_provider.%s %q must stay inside stack_dir", field, rawRel)
	}

	subRoot, err := rootOpenSubRoot(reverseProxyRoot, filepath.FromSlash(cleaned))
	if err != nil {
		return nil, fmt.Errorf("open reverse_proxy_provider.%s %q: %w", field, cleaned, err)
	}
	return subRoot, nil
}

func stageReverseProxyConfFiles(reverseProxyRoot *os.Root, srcRelDir string, confNames []string, dstDir, dirField, kind string) error {
	if len(confNames) == 0 {
		return nil
	}

	srcRoot, rootErr := openReverseProxySubRoot(reverseProxyRoot, srcRelDir, dirField)
	if rootErr != nil {
		return rootErr
	}

	if err := os.MkdirAll(dstDir, 0o750); err != nil {
		return errors.Join(fmt.Errorf("create staged %s dir %q: %w", kind, dstDir, err), srcRoot.Close())
	}

	for _, confName := range confNames {
		if err := copyRootRegularFile(srcRoot, confName, filepath.Join(dstDir, confName)); err != nil {
			return errors.Join(fmt.Errorf("stage reverse-proxy %s config %q: %w", kind, confName, err), srcRoot.Close())
		}
	}
	if err := closeReverseProxyRoot(srcRoot); err != nil {
		return fmt.Errorf("close reverse-proxy %s root: %w", kind, err)
	}
	return nil
}

func stageReverseProxyCerts(reverseProxyRoot *os.Root, certsRelDir string, acmeNames []string, dstBaseDir string) error {
	if len(acmeNames) == 0 {
		return nil
	}

	certsRoot, rootErr := openReverseProxySubRoot(reverseProxyRoot, certsRelDir, "certs_dir")
	if rootErr != nil {
		return rootErr
	}

	for _, acme := range acmeNames {
		info, err := certsRoot.Lstat(acme)
		if err != nil || !info.IsDir() {
			return errors.Join(fmt.Errorf("reverse-proxy certificate directory %q not found: %w", acme, err), certsRoot.Close())
		}
		acmeRoot, openErr := rootOpenSubRoot(certsRoot, acme)
		if openErr != nil {
			return errors.Join(fmt.Errorf("open reverse-proxy certificate directory %q: %w", acme, openErr), certsRoot.Close())
		}
		copyErr := copyRootDirTree(acmeRoot, filepath.Join(dstBaseDir, acme))
		closeErr := acmeRoot.Close()
		if err := errors.Join(copyErr, closeErr); err != nil {
			return errors.Join(fmt.Errorf("stage reverse-proxy certificates %q: %w", acme, err), certsRoot.Close())
		}
	}
	if err := closeReverseProxyRoot(certsRoot); err != nil {
		return fmt.Errorf("close reverse-proxy certs root: %w", err)
	}
	return nil
}

func copyRootDirTree(srcRoot *os.Root, dstDir string) error {
	if err := walkRootFS(srcRoot.FS(), ".", func(rel string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("refusing symlink %q while staging", rel)
		}

		relOS := filepath.FromSlash(rel)
		target := filepath.Join(dstDir, relOS)
		if entry.IsDir() {
			info, infoErr := entry.Info()
			if infoErr != nil {
				return fmt.Errorf("stat dir %q: %w", rel, infoErr)
			}
			if err := os.MkdirAll(target, info.Mode().Perm()); err != nil {
				return fmt.Errorf("mkdir %q: %w", target, err)
			}
			return nil
		}

		return copyRootRegularFile(srcRoot, relOS, target)
	}); err != nil {
		return fmt.Errorf("copy directory tree: %w", err)
	}
	return nil
}

func copyRootRegularFile(srcRoot *os.Root, relSrc, dst string) error {
	info, statErr := srcRoot.Lstat(relSrc)
	if statErr != nil {
		return fmt.Errorf("stat %q: %w", relSrc, statErr)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("refusing symlink %q", relSrc)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("expected regular file at %q", relSrc)
	}
	if mkErr := os.MkdirAll(filepath.Dir(dst), 0o750); mkErr != nil {
		return fmt.Errorf("mkdir parent for %q: %w", dst, mkErr)
	}

	in, openErr := srcRoot.OpenFile(relSrc, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if openErr != nil {
		return fmt.Errorf("open %q: %w", relSrc, openErr)
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
		return fmt.Errorf("copy %q to %q: %w", relSrc, dst, err)
	}
	return nil
}
