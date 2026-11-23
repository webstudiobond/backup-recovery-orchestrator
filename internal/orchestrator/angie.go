package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/webstudiobond/backup-recovery-orchestrator/internal/config"
	"github.com/webstudiobond/backup-recovery-orchestrator/internal/dac"
	"github.com/webstudiobond/backup-recovery-orchestrator/internal/docker"
)

type angieSelection struct {
	Domains []string
	Streams []string
	Certs   []string
}

type angieRollbackEntry struct {
	targetPath string
	backupPath string
	existed    bool
	isDir      bool
}

func checkAngieSelectionList(requested, available []string, kind string) error {
	for _, item := range requested {
		if !slices.Contains(available, item) {
			return fmt.Errorf("requested angie %s %q is not present in archive manifest", kind, item)
		}
	}
	return nil
}

func resolveAngieSelection(avail *config.StackAngieConfig, mode string, reqDomains, reqStreams, reqCerts []string) (angieSelection, error) {
	if len(reqDomains) > 0 || len(reqStreams) > 0 || len(reqCerts) > 0 {
		if err := checkAngieSelectionList(reqDomains, avail.DomainConfs, "domain"); err != nil {
			return angieSelection{}, err
		}
		if err := checkAngieSelectionList(reqStreams, avail.StreamConfs, "stream"); err != nil {
			return angieSelection{}, err
		}
		if err := checkAngieSelectionList(reqCerts, avail.ACMENames, "cert"); err != nil {
			return angieSelection{}, err
		}
		return angieSelection{
			Domains: slices.Clone(reqDomains),
			Streams: slices.Clone(reqStreams),
			Certs:   slices.Clone(reqCerts),
		}, nil
	}

	switch mode {
	case "", RestoreModeNone:
		return angieSelection{}, nil
	case RestoreModeAll:
		return angieSelection{
			Domains: slices.Clone(avail.DomainConfs),
			Streams: slices.Clone(avail.StreamConfs),
			Certs:   slices.Clone(avail.ACMENames),
		}, nil
	default:
		return angieSelection{}, fmt.Errorf("unsupported angie restore mode %q (expected 'none' or 'all')", mode)
	}
}

func deployAngieWithRollback(
	ctx context.Context,
	dockerClient *docker.Client,
	dacApplier *dac.Applier,
	globalAngie *config.GlobalAngieConfig,
	unpackDir string,
	sel angieSelection,
) error {
	if len(sel.Domains) == 0 && len(sel.Streams) == 0 && len(sel.Certs) == 0 {
		return nil
	}
	if strings.TrimSpace(globalAngie.ComposeFile) == "" {
		return errors.New("global angie.compose_file is required to deploy angie assets")
	}
	if _, statErr := os.Stat(globalAngie.ComposeFile); statErr != nil {
		return fmt.Errorf("angie compose_file %q not found: %w", globalAngie.ComposeFile, statErr)
	}

	rollbackDir, mkErr := os.MkdirTemp("", "bro-angie-rollback-*")
	if mkErr != nil {
		return fmt.Errorf("create angie rollback directory: %w", mkErr)
	}

	var rollbackEntries []angieRollbackEntry
	deployErr := applySelectedAngieFiles(ctx, dacApplier, globalAngie, unpackDir, rollbackDir, sel, &rollbackEntries)
	if deployErr == nil {
		deployErr = dockerClient.CheckAngieConfig(ctx, globalAngie.ComposeFile, globalAngie.ServiceName)
	}

	var rbErr error
	if deployErr != nil {
		rbErr = rollbackAngieFiles(rollbackEntries)
	}
	rmErr := os.RemoveAll(rollbackDir)
	return errors.Join(wrapDeployErr(deployErr), rbErr, wrapRemoveErr(rmErr, rollbackDir))
}

func wrapDeployErr(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("deploy angie assets: %w", err)
}

func applySelectedAngieFiles(
	ctx context.Context,
	dacApplier *dac.Applier,
	globalAngie *config.GlobalAngieConfig,
	unpackDir, rollbackDir string,
	sel angieSelection,
	rollbackEntries *[]angieRollbackEntry,
) error {
	angieSvc := globalAngie.ServiceName
	if angieSvc == "" {
		angieSvc = config.DefaultAngieService
	}

	if len(sel.Domains) > 0 {
		if err := deployAngieConfFiles(ctx, dacApplier, globalAngie.DomainsDir, angieSvc, filepath.Join(unpackDir, "domains"), rollbackDir, "domains_dir", "domain", sel.Domains, rollbackEntries); err != nil {
			return err
		}
	}
	if len(sel.Streams) > 0 {
		if err := deployAngieConfFiles(ctx, dacApplier, globalAngie.ResolveStreamDir(), angieSvc, filepath.Join(unpackDir, "stream"), rollbackDir, "stream_dir", "stream", sel.Streams, rollbackEntries); err != nil {
			return err
		}
	}
	if len(sel.Certs) > 0 {
		if err := deployAngieCerts(ctx, dacApplier, globalAngie.CertsDir, angieSvc, unpackDir, rollbackDir, sel.Certs, rollbackEntries); err != nil {
			return err
		}
	}
	return nil
}

func deployAngieConfFiles(
	ctx context.Context,
	dacApplier *dac.Applier,
	targetDir, angieSvc, stagedDir, rollbackDir, dirField, kind string,
	confs []string,
	rollbackEntries *[]angieRollbackEntry,
) error {
	if strings.TrimSpace(targetDir) == "" {
		return fmt.Errorf("global angie.%s is required to deploy %s configs", dirField, kind)
	}
	if err := os.MkdirAll(targetDir, 0o750); err != nil {
		return fmt.Errorf("ensure angie %s dir %q: %w", kind, targetDir, err)
	}

	for idx, conf := range confs {
		src := filepath.Join(stagedDir, conf)
		dst := filepath.Join(targetDir, conf)
		bak := filepath.Join(rollbackDir, fmt.Sprintf("%s_%d_%s", kind, idx, conf))

		entry, snapErr := snapshotExistingPath(dst, bak)
		if snapErr != nil {
			return snapErr
		}
		*rollbackEntries = append(*rollbackEntries, entry)

		if copyErr := copyPlainFile(src, dst, 0o640); copyErr != nil {
			return copyErr
		}
		if _, dacErr := dacApplier.ApplyStack(ctx, targetDir, config.UserConfig{Name: angieSvc}, []config.DACRule{
			{Path: conf, Owner: "root:" + angieSvc, FileMode: "0440"},
		}, false); dacErr != nil {
			return fmt.Errorf("apply DAC to angie %s %q: %w", kind, conf, dacErr)
		}
	}
	return nil
}

func deployAngieCerts(
	ctx context.Context,
	dacApplier *dac.Applier,
	certsDir, angieSvc, unpackDir, rollbackDir string,
	certs []string,
	rollbackEntries *[]angieRollbackEntry,
) error {
	if strings.TrimSpace(certsDir) == "" {
		return errors.New("global angie.certs_dir is required to deploy certificates")
	}
	if err := os.MkdirAll(certsDir, 0o750); err != nil {
		return fmt.Errorf("ensure angie certs dir %q: %w", certsDir, err)
	}

	for idx, acme := range certs {
		srcDir := filepath.Join(unpackDir, "certs", acme)
		dstDir := filepath.Join(certsDir, acme)
		bakDir := filepath.Join(rollbackDir, fmt.Sprintf("cert_%d_%s", idx, acme))

		entry, snapErr := snapshotExistingPath(dstDir, bakDir)
		if snapErr != nil {
			return snapErr
		}
		*rollbackEntries = append(*rollbackEntries, entry)

		if rmErr := os.RemoveAll(dstDir); rmErr != nil {
			return fmt.Errorf("clear target cert dir %q: %w", dstDir, rmErr)
		}
		if copyErr := copyDirectoryContents(srcDir, dstDir); copyErr != nil {
			return copyErr
		}
		if _, dacErr := dacApplier.ApplyStack(ctx, certsDir, config.UserConfig{Name: angieSvc}, []config.DACRule{
			{Path: acme, Owner: angieSvc + ":" + angieSvc, DirMode: "0700", FileMode: "0600", Recursive: true},
		}, false); dacErr != nil {
			return fmt.Errorf("apply DAC to angie certs %q: %w", acme, dacErr)
		}
	}
	return nil
}

func snapshotExistingPath(targetPath, backupPath string) (angieRollbackEntry, error) {
	info, err := os.Lstat(targetPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return angieRollbackEntry{targetPath: targetPath, existed: false}, nil
		}
		return angieRollbackEntry{}, fmt.Errorf("stat existing angie asset %q: %w", targetPath, err)
	}

	if info.IsDir() {
		if copyErr := copyDirectoryContents(targetPath, backupPath); copyErr != nil {
			return angieRollbackEntry{}, fmt.Errorf("snapshot existing angie dir %q: %w", targetPath, copyErr)
		}
		return angieRollbackEntry{targetPath: targetPath, backupPath: backupPath, existed: true, isDir: true}, nil
	}

	if copyErr := copyPlainFile(targetPath, backupPath, info.Mode().Perm()); copyErr != nil {
		return angieRollbackEntry{}, fmt.Errorf("snapshot existing angie file %q: %w", targetPath, copyErr)
	}
	return angieRollbackEntry{targetPath: targetPath, backupPath: backupPath, existed: true, isDir: false}, nil
}

func rollbackAngieFiles(entries []angieRollbackEntry) error {
	var errs []error
	for _, entry := range slices.Backward(entries) {
		if rmErr := os.RemoveAll(entry.targetPath); rmErr != nil {
			errs = append(errs, fmt.Errorf("rollback remove %q: %w", entry.targetPath, rmErr))
			continue
		}
		if !entry.existed {
			continue
		}
		if entry.isDir {
			if cpErr := copyDirectoryContents(entry.backupPath, entry.targetPath); cpErr != nil {
				errs = append(errs, fmt.Errorf("rollback restore dir %q: %w", entry.targetPath, cpErr))
			}
			continue
		}
		if cpErr := copyPlainFile(entry.backupPath, entry.targetPath, 0o600); cpErr != nil {
			errs = append(errs, fmt.Errorf("rollback restore file %q: %w", entry.targetPath, cpErr))
		}
	}
	return errors.Join(errs...)
}
