package storage

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/webstudiobond/backup-recovery-orchestrator/internal/cmdexec"
	"github.com/webstudiobond/backup-recovery-orchestrator/internal/config"
)

func (m *Manager) syncArchiveRclone(ctx context.Context, dest *config.DestinationConfig, stackName, srcArchivePath string, remoteKeepCount int) (SyncResult, error) {
	cleanSrc := filepath.Clean(srcArchivePath)
	srcInfo, statErr := os.Stat(cleanSrc)
	if statErr != nil {
		return SyncResult{}, fmt.Errorf("stat source archive %q: %w", cleanSrc, statErr)
	}

	archiveName := filepath.Base(cleanSrc)
	stackRemoteDir := rcloneStackTarget(dest.Remote, stackName)
	remoteEntries, listErr := m.listRcloneStackFiles(ctx, dest, stackRemoteDir, stackName)
	if listErr != nil {
		return SyncResult{}, listErr
	}

	res := SyncResult{Destination: dest.Name}
	if existingSize, exists := remoteEntries[archiveName]; exists && existingSize == srcInfo.Size() {
		res.Skipped = append(res.Skipped, archiveName)
	} else {
		if copyErr := m.copyFileRclone(ctx, dest, cleanSrc, stackRemoteDir+"/"+archiveName); copyErr != nil {
			return SyncResult{}, copyErr
		}
		res.Copied = append(res.Copied, archiveName)
	}

	pruned, pruneErr := m.pruneRcloneStack(ctx, dest, stackRemoteDir, stackName, remoteKeepCount)
	if pruneErr != nil {
		return res, pruneErr
	}
	res.Pruned = pruned
	return res, nil
}

func (m *Manager) syncStackRclone(ctx context.Context, dest *config.DestinationConfig, localStackDir, stackName string, remoteKeepCount int) (SyncResult, error) {
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

	stackRemoteDir := rcloneStackTarget(dest.Remote, stackName)
	remoteEntries, listErr := m.listRcloneStackFiles(ctx, dest, stackRemoteDir, stackName)
	if listErr != nil {
		return SyncResult{}, listErr
	}

	res := SyncResult{Destination: dest.Name}
	for _, srcPath := range archives[:limit] {
		info, statErr := os.Stat(srcPath)
		if statErr != nil {
			return res, fmt.Errorf("stat local archive %q: %w", srcPath, statErr)
		}
		name := filepath.Base(srcPath)
		if existingSize, exists := remoteEntries[name]; exists && existingSize == info.Size() {
			res.Skipped = append(res.Skipped, name)
			continue
		}
		if copyErr := m.copyFileRclone(ctx, dest, srcPath, stackRemoteDir+"/"+name); copyErr != nil {
			return res, copyErr
		}
		res.Copied = append(res.Copied, name)
	}

	pruned, pruneErr := m.pruneRcloneStack(ctx, dest, stackRemoteDir, stackName, remoteKeepCount)
	if pruneErr != nil {
		return res, pruneErr
	}
	res.Pruned = pruned
	return res, nil
}

func (m *Manager) copyFileRclone(ctx context.Context, dest *config.DestinationConfig, srcPath, remoteTarget string) error {
	if err := m.runRclone(ctx, dest.RcloneConfig, "copyto", srcPath, remoteTarget); err != nil {
		return fmt.Errorf("rclone copyto %q: %w", remoteTarget, err)
	}
	return nil
}

func (m *Manager) pruneRcloneStack(ctx context.Context, dest *config.DestinationConfig, stackRemoteDir, stackName string, keepCount int) ([]string, error) {
	if keepCount < 0 {
		return nil, nil
	}

	entries, err := m.listRcloneStackFiles(ctx, dest, stackRemoteDir, stackName)
	if err != nil {
		return nil, err
	}

	names := make([]string, 0, len(entries))
	for name := range entries {
		names = append(names, name)
	}
	sortArchivesNewestFirst(names, stackName)

	if len(names) <= keepCount {
		return nil, nil
	}

	expired := names[keepCount:]
	pruned := make([]string, 0, len(expired))
	for _, fileName := range expired {
		target := stackRemoteDir + "/" + fileName
		if delErr := m.runRclone(ctx, dest.RcloneConfig, "deletefile", target); delErr != nil {
			return pruned, fmt.Errorf("rclone deletefile %q: %w", target, delErr)
		}
		pruned = append(pruned, fileName)
	}
	return pruned, nil
}

func (m *Manager) listRcloneStackFiles(ctx context.Context, dest *config.DestinationConfig, stackRemoteDir, stackName string) (map[string]int64, error) {
	out, err := m.outputRclone(ctx, dest.RcloneConfig, "lsf", "--files-only", "--format", "sp", "--separator", ";", stackRemoteDir)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "directory not found") {
			return map[string]int64{}, nil
		}
		return nil, fmt.Errorf("rclone lsf %q: %w", stackRemoteDir, err)
	}

	files := make(map[string]int64)
	scanner := bufio.NewScanner(bytes.NewReader(out))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		sizeStr, name, ok := strings.Cut(line, ";")
		if !ok || !isStackArchiveName(name, stackName) {
			continue
		}
		sizeVal, convErr := strconv.ParseInt(sizeStr, 10, 64)
		if convErr != nil {
			return nil, fmt.Errorf("parse rclone size %q for %q: %w", sizeStr, name, convErr)
		}
		files[name] = sizeVal
	}
	if scanErr := scanner.Err(); scanErr != nil {
		return nil, fmt.Errorf("scan rclone listing for %q: %w", stackRemoteDir, scanErr)
	}
	return files, nil
}

func (m *Manager) runRclone(ctx context.Context, rcloneConfig string, args ...string) error {
	if err := m.Runner.Run(ctx, &cmdexec.Spec{
		Name: cmdexec.BinRclone,
		Args: rcloneConfigPrefix(rcloneConfig, args...),
	}); err != nil {
		return fmt.Errorf("run rclone: %w", err)
	}
	return nil
}

func (m *Manager) outputRclone(ctx context.Context, rcloneConfig string, args ...string) ([]byte, error) {
	out, err := m.Runner.Output(ctx, &cmdexec.Spec{
		Name: cmdexec.BinRclone,
		Args: rcloneConfigPrefix(rcloneConfig, args...),
	})
	if err != nil {
		return nil, fmt.Errorf("output rclone: %w", err)
	}
	return out, nil
}

func rcloneStackTarget(baseRemote, stackName string) string {
	trimmed := strings.TrimRight(strings.TrimSpace(baseRemote), "/")
	if strings.HasSuffix(trimmed, ":") {
		return trimmed + stackName
	}
	return trimmed + "/" + stackName
}

func rcloneConfigPrefix(rcloneConfig string, args ...string) []string {
	if strings.TrimSpace(rcloneConfig) == "" {
		return args
	}
	prefixed := make([]string, 0, 2+len(args))
	prefixed = append(prefixed, "--config", rcloneConfig)
	prefixed = append(prefixed, args...)
	return prefixed
}
