// Package storage synchronizes backup containers to offsite mounts, directories, or rclone remotes and enforces retention.
package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/webstudiobond/backup-recovery-orchestrator/internal/cmdexec"
	"github.com/webstudiobond/backup-recovery-orchestrator/internal/config"
	"github.com/webstudiobond/backup-recovery-orchestrator/internal/mount"
)

const (
	copyBufferSize  = 1 << 20
	timestampLayout = "2006-01-02_15-04-05"
)

// SyncResult records which archives were copied, skipped, or pruned on a destination.
type SyncResult struct {
	Destination string
	Copied      []string
	Skipped     []string
	Pruned      []string
}

// Manager coordinates offsite synchronization, remote/local retention pruning, and rclone cleanup.
type Manager struct {
	Runner   cmdexec.Runner
	Verifier *mount.Verifier
}

// NewManager creates a storage Manager using OSRunner and a default Verifier when arguments are nil.
func NewManager(runner cmdexec.Runner, verifier *mount.Verifier) *Manager {
	if runner == nil {
		runner = cmdexec.OSRunner{}
	}
	if verifier == nil {
		verifier = mount.NewVerifier(runner)
	}
	return &Manager{
		Runner:   runner,
		Verifier: verifier,
	}
}

// SyncArchive copies a newly created local archive to dest and enforces remote retention for stackName.
func (m *Manager) SyncArchive(ctx context.Context, dest *config.DestinationConfig, stackName, srcArchivePath string, remoteKeepCount int) (SyncResult, error) {
	if err := ctx.Err(); err != nil {
		return SyncResult{}, fmt.Errorf("context canceled before syncing %q: %w", stackName, err)
	}
	if dest == nil {
		return SyncResult{}, errors.New("destination config must not be nil")
	}
	if dest.Type == config.DestTypeRclone {
		return m.syncArchiveRclone(ctx, dest, stackName, srcArchivePath, remoteKeepCount)
	}
	if err := m.Verifier.EnsureReady(ctx, dest); err != nil {
		return SyncResult{}, fmt.Errorf("verify destination %q: %w", dest.Name, err)
	}

	remoteStackDir := filepath.Join(dest.BackupDir, stackName)
	if err := os.MkdirAll(remoteStackDir, 0o700); err != nil {
		return SyncResult{}, fmt.Errorf("create remote backup directory %q: %w", remoteStackDir, err)
	}

	res := SyncResult{Destination: dest.Name}
	copied, err := copyArchiveIfChanged(ctx, srcArchivePath, remoteStackDir)
	if err != nil {
		return SyncResult{}, err
	}
	if copied {
		res.Copied = append(res.Copied, filepath.Base(srcArchivePath))
	} else {
		res.Skipped = append(res.Skipped, filepath.Base(srcArchivePath))
	}

	pruned, pruneErr := pruneDirectory(ctx, remoteStackDir, stackName, remoteKeepCount)
	if pruneErr != nil {
		return res, pruneErr
	}
	res.Pruned = pruned
	return res, nil
}

// SyncStack synchronizes up to remoteKeepCount latest local archives of stackName to dest and prunes older remote copies.
func (m *Manager) SyncStack(ctx context.Context, dest *config.DestinationConfig, localStackDir, stackName string, remoteKeepCount int) (SyncResult, error) {
	if err := ctx.Err(); err != nil {
		return SyncResult{}, fmt.Errorf("context canceled before syncing stack %q: %w", stackName, err)
	}
	if dest == nil {
		return SyncResult{}, errors.New("destination config must not be nil")
	}
	if dest.Type == config.DestTypeRclone {
		return m.syncStackRclone(ctx, dest, localStackDir, stackName, remoteKeepCount)
	}
	if err := m.Verifier.EnsureReady(ctx, dest); err != nil {
		return SyncResult{}, fmt.Errorf("verify destination %q: %w", dest.Name, err)
	}

	archives, err := listStackArchives(localStackDir, stackName)
	if err != nil {
		return SyncResult{}, err
	}
	if len(archives) == 0 {
		return SyncResult{}, fmt.Errorf("no local archives found for stack %q in %q", stackName, localStackDir)
	}

	limit := len(archives)
	if remoteKeepCount > 0 && remoteKeepCount < limit {
		limit = remoteKeepCount
	}

	remoteStackDir := filepath.Join(dest.BackupDir, stackName)
	if mkErr := os.MkdirAll(remoteStackDir, 0o700); mkErr != nil {
		return SyncResult{}, fmt.Errorf("create remote backup directory %q: %w", remoteStackDir, mkErr)
	}

	res := SyncResult{Destination: dest.Name}
	for _, srcPath := range archives[:limit] {
		copied, copyErr := copyArchiveIfChanged(ctx, srcPath, remoteStackDir)
		if copyErr != nil {
			return res, copyErr
		}
		if copied {
			res.Copied = append(res.Copied, filepath.Base(srcPath))
		} else {
			res.Skipped = append(res.Skipped, filepath.Base(srcPath))
		}
	}

	pruned, pruneErr := pruneDirectory(ctx, remoteStackDir, stackName, remoteKeepCount)
	if pruneErr != nil {
		return res, pruneErr
	}
	res.Pruned = pruned
	return res, nil
}

// CleanupRemote runs optional rclone trash cleanup for a destination after retention pruning completes.
func (m *Manager) CleanupRemote(ctx context.Context, dest *config.DestinationConfig) error {
	if dest == nil || strings.TrimSpace(dest.RcloneCleanupRemote) == "" {
		return nil
	}
	remote := strings.TrimSpace(dest.RcloneCleanupRemote)
	if strings.ContainsAny(remote, ":/\\ \t\r\n") {
		return fmt.Errorf("invalid rclone cleanup remote name %q", remote)
	}
	if err := m.runRclone(ctx, dest.RcloneConfig, "cleanup", remote+":"); err != nil {
		return fmt.Errorf("rclone cleanup on %q: %w", remote, err)
	}
	return nil
}

func defaultRootClose(root *os.Root) error {
	return errors.Join(root.Close(), nil)
}

var rootClose = defaultRootClose

func defaultOSRemove(name string) error {
	return errors.Join(os.Remove(name), nil)
}

var osRemove = defaultOSRemove

func copyArchiveIfChanged(ctx context.Context, srcPath, dstDir string) (copied bool, err error) {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return false, fmt.Errorf("context canceled before copying %q: %w", srcPath, ctxErr)
	}

	cleanSrc := filepath.Clean(srcPath)
	srcInfo, statErr := os.Stat(cleanSrc)
	if statErr != nil {
		return false, fmt.Errorf("stat source archive %q: %w", cleanSrc, statErr)
	}

	dstPath := filepath.Join(dstDir, filepath.Base(cleanSrc))
	if dstInfo, dstStatErr := os.Stat(dstPath); dstStatErr == nil && dstInfo.Size() == srcInfo.Size() {
		return false, nil
	}

	srcFile, openErr := os.Open(cleanSrc)
	if openErr != nil {
		return false, fmt.Errorf("open source archive %q: %w", cleanSrc, openErr)
	}

	dstRoot, rootErr := os.OpenRoot(filepath.Clean(dstDir))
	if rootErr != nil {
		closeErr := srcFile.Close()
		return false, errors.Join(fmt.Errorf("open destination root %q: %w", dstDir, rootErr), closeErr)
	}
	defer func() {
		if closeRootErr := rootClose(dstRoot); closeRootErr != nil && err == nil {
			err = fmt.Errorf("close destination root %q: %w", dstDir, closeRootErr)
		}
	}()

	baseName := filepath.Base(cleanSrc)
	dstFile, createErr := dstRoot.OpenFile(baseName, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if createErr != nil {
		closeErr := srcFile.Close()
		return false, errors.Join(fmt.Errorf("create destination archive %q: %w", dstPath, createErr), closeErr)
	}

	buf := make([]byte, copyBufferSize)
	_, copyErr := io.CopyBuffer(dstFile, srcFile, buf)
	syncErr := dstFile.Sync()
	chmodErr := dstFile.Chmod(0o600)
	closeDstErr := dstFile.Close()
	closeSrcErr := srcFile.Close()

	if combined := errors.Join(copyErr, syncErr, chmodErr, closeDstErr, closeSrcErr); combined != nil {
		rmErr := osRemove(dstPath)
		if rmErr != nil && !errors.Is(rmErr, os.ErrNotExist) {
			return false, errors.Join(combined, rmErr)
		}
		return false, fmt.Errorf("copy archive to %q: %w", dstPath, combined)
	}

	return true, nil
}
