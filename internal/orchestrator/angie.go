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

func resolveAngieServiceName(globalAngie *config.GlobalAngieConfig) string {
	if globalAngie.ServiceName != "" {
		return globalAngie.ServiceName
	}
	return config.DefaultAngieService
}

func deployAngieWithRollback(
	ctx context.Context,
	dockerClient *docker.Client,
	dacApplier *dac.Applier,
	globalAngie *config.GlobalAngieConfig,
	angieStack *config.StackConfig,
	workDir, unpackDir string,
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

	rollbackDir, mkErr := createWorkSubdir(workDir, angieRollbackPrefix+"*")
	if mkErr != nil {
		return fmt.Errorf("create angie rollback directory: %w", mkErr)
	}

	angieSvc := resolveAngieServiceName(globalAngie)
	angieUser := resolveAngieDACUser(angieStack, angieSvc)
	var rollbackEntries []angieRollbackEntry
	deployErr := applySelectedAngieFiles(ctx, dacApplier, globalAngie, angieStack, angieSvc, angieUser, unpackDir, rollbackDir, sel, &rollbackEntries)
	if deployErr == nil {
		deployErr = dockerClient.CheckAngieConfig(ctx, globalAngie.ComposeFile, globalAngie.ServiceName)
	}

	var rbErr error
	if deployErr != nil {
		rbErr = rollbackAngieFiles(ctx, dacApplier, angieUser, rollbackEntries)
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
	angieStack *config.StackConfig,
	angieSvc string,
	angieUser config.UserConfig,
	unpackDir, rollbackDir string,
	sel angieSelection,
	rollbackEntries *[]angieRollbackEntry,
) error {
	if len(sel.Domains) > 0 {
		if err := deployAngieConfFiles(ctx, dacApplier, angieStack, globalAngie.DomainsDir, angieSvc, angieUser, filepath.Join(unpackDir, "domains"), rollbackDir, "domains_dir", "domain", sel.Domains, rollbackEntries); err != nil {
			return err
		}
	}
	if len(sel.Streams) > 0 {
		if err := deployAngieConfFiles(ctx, dacApplier, angieStack, globalAngie.ResolveStreamDir(), angieSvc, angieUser, filepath.Join(unpackDir, "stream"), rollbackDir, "stream_dir", "stream", sel.Streams, rollbackEntries); err != nil {
			return err
		}
	}
	if len(sel.Certs) > 0 {
		if err := deployAngieCerts(ctx, dacApplier, angieStack, globalAngie.CertsDir, angieSvc, angieUser, unpackDir, rollbackDir, sel.Certs, rollbackEntries); err != nil {
			return err
		}
	}
	return nil
}

func deployAngieConfFiles(
	ctx context.Context,
	dacApplier *dac.Applier,
	angieStack *config.StackConfig,
	targetDir, angieSvc string,
	angieUser config.UserConfig,
	stagedDir, rollbackDir, dirField, kind string,
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
		rules := resolveAngieConfDACRules(angieStack, targetDir, conf, angieSvc)

		entry, snapErr := snapshotExistingPath(dst, bak)
		if snapErr != nil {
			return snapErr
		}
		entry.baseDir = targetDir
		entry.dacRules = rules
		*rollbackEntries = append(*rollbackEntries, entry)

		if copyErr := copyPlainFile(src, dst, 0o640); copyErr != nil {
			return copyErr
		}
		if _, dacErr := dacApplier.ApplyStack(ctx, targetDir, angieUser, rules, false); dacErr != nil {
			return fmt.Errorf("apply DAC to angie %s %q: %w", kind, conf, dacErr)
		}
	}
	return nil
}

func deployAngieCerts(
	ctx context.Context,
	dacApplier *dac.Applier,
	angieStack *config.StackConfig,
	certsDir, angieSvc string,
	angieUser config.UserConfig,
	unpackDir, rollbackDir string,
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
		rules := resolveAngieCertDACRules(angieStack, certsDir, acme, angieSvc)

		entry, snapErr := snapshotExistingPath(dstDir, bakDir)
		if snapErr != nil {
			return snapErr
		}
		entry.baseDir = certsDir
		entry.dacRules = rules
		*rollbackEntries = append(*rollbackEntries, entry)

		if rmErr := os.RemoveAll(dstDir); rmErr != nil {
			return fmt.Errorf("clear target cert dir %q: %w", dstDir, rmErr)
		}
		if copyErr := copyDirectoryContents(srcDir, dstDir); copyErr != nil {
			return copyErr
		}
		if _, dacErr := dacApplier.ApplyStack(ctx, certsDir, angieUser, rules, false); dacErr != nil {
			return fmt.Errorf("apply DAC to angie certs %q: %w", acme, dacErr)
		}
	}
	return nil
}
