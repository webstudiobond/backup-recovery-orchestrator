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
	"github.com/webstudiobond/backup-recovery-orchestrator/internal/reverseproxy"
)

type reverseProxySelection struct {
	Domains []string
	Streams []string
	Certs   []string
}

func (s reverseProxySelection) isEmpty() bool {
	return len(s.Domains) == 0 && len(s.Streams) == 0 && len(s.Certs) == 0
}

func checkReverseProxySelectionList(requested, available []string, kind string) error {
	for _, item := range requested {
		if !slices.Contains(available, item) {
			return fmt.Errorf("requested reverse-proxy %s %q is not present in archive manifest", kind, item)
		}
	}
	return nil
}

func resolveReverseProxySelection(
	avail *config.StackReverseProxyConfig,
	mode string,
	reqDomains []string,
	reqStreams []string,
	reqCerts []string,
) (reverseProxySelection, error) {
	if len(reqDomains) > 0 || len(reqStreams) > 0 || len(reqCerts) > 0 {
		if err := checkReverseProxySelectionList(reqDomains, avail.DomainConfs, "domain"); err != nil {
			return reverseProxySelection{}, err
		}
		if err := checkReverseProxySelectionList(reqStreams, avail.StreamConfs, "stream"); err != nil {
			return reverseProxySelection{}, err
		}
		if err := checkReverseProxySelectionList(reqCerts, avail.ACMENames, "cert"); err != nil {
			return reverseProxySelection{}, err
		}
		return reverseProxySelection{
			Domains: slices.Clone(reqDomains),
			Streams: slices.Clone(reqStreams),
			Certs:   slices.Clone(reqCerts),
		}, nil
	}

	switch mode {
	case "", RestoreModeNone:
		return reverseProxySelection{}, nil
	case RestoreModeAll:
		return reverseProxySelection{
			Domains: slices.Clone(avail.DomainConfs),
			Streams: slices.Clone(avail.StreamConfs),
			Certs:   slices.Clone(avail.ACMENames),
		}, nil
	default:
		return reverseProxySelection{}, fmt.Errorf("unsupported reverse-proxy restore mode %q (expected 'none' or 'all')", mode)
	}
}

func deployReverseProxyWithRollback(
	ctx context.Context,
	dockerClient *docker.Client,
	dacApplier *dac.Applier,
	reverseProxyStack *config.StackConfig,
	workDir string,
	unpackDir string,
	sel reverseProxySelection,
) error {
	if sel.isEmpty() {
		return nil
	}
	if reverseProxyStack == nil || reverseProxyStack.ReverseProxyProvider == nil {
		return errors.New("global reverse_proxy_stack provider is required to deploy reverse-proxy assets")
	}

	providerCfg := reverseProxyStack.ReverseProxyProvider
	provider, provErr := reverseproxy.Lookup(providerCfg.Type)
	if provErr != nil {
		return fmt.Errorf("resolve reverse-proxy provider: %w", provErr)
	}
	if strings.TrimSpace(reverseProxyStack.ComposeFile) == "" {
		return errors.New("reverse_proxy_stack compose_file is required to deploy reverse-proxy assets")
	}
	if _, statErr := os.Stat(reverseProxyStack.ComposeFile); statErr != nil {
		return fmt.Errorf("reverse_proxy_stack compose_file %q not found: %w", reverseProxyStack.ComposeFile, statErr)
	}

	reverseProxyRoot, rootErr := osOpenRoot(filepath.Clean(reverseProxyStack.StackDir))
	if rootErr != nil {
		return fmt.Errorf("open reverse_proxy_stack stack_dir %q: %w", reverseProxyStack.StackDir, rootErr)
	}

	rollbackDir, mkErr := createWorkSubdir(workDir, reverseProxyRollbackPrefix+"*")
	if mkErr != nil {
		return errors.Join(fmt.Errorf("create reverse-proxy rollback directory: %w", mkErr), rootClose(reverseProxyRoot))
	}

	reverseProxyUser := resolveReverseProxyDACUser(reverseProxyStack, providerCfg.ServiceName)
	var rollbackEntries []reverseProxyRollbackEntry
	deployErr := applySelectedReverseProxyFiles(ctx, dacApplier, provider, reverseProxyStack, reverseProxyRoot, reverseProxyUser, unpackDir, rollbackDir, sel, &rollbackEntries)
	if deployErr == nil {
		deployErr = dockerClient.CheckReverseProxyConfig(ctx, reverseProxyStack.ComposeFile, providerCfg.ServiceName, provider.SyntaxCheckArgs())
	}

	var rbErr error
	if deployErr != nil {
		rbErr = rollbackReverseProxyFiles(ctx, dacApplier, reverseProxyRoot, reverseProxyUser, rollbackEntries)
	}
	rmErr := os.RemoveAll(rollbackDir)
	closeErr := rootClose(reverseProxyRoot)
	return errors.Join(wrapDeployErr(deployErr), rbErr, wrapRemoveErr(rmErr, rollbackDir), closeErr)
}

func wrapDeployErr(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("deploy reverse-proxy assets: %w", err)
}

func applySelectedReverseProxyFiles(
	ctx context.Context,
	dacApplier *dac.Applier,
	provider reverseproxy.Provider,
	reverseProxyStack *config.StackConfig,
	reverseProxyRoot *os.Root,
	reverseProxyUser config.UserConfig,
	unpackDir string,
	rollbackDir string,
	sel reverseProxySelection,
	rollbackEntries *[]reverseProxyRollbackEntry,
) error {
	providerCfg := reverseProxyStack.ReverseProxyProvider
	if len(sel.Domains) > 0 {
		if err := deployReverseProxyConfFiles(ctx, dacApplier, provider, reverseProxyStack, reverseProxyRoot, reverseProxyUser, providerCfg.DomainsDir, filepath.Join(unpackDir, "domains"), rollbackDir, "domains_dir", "domain", sel.Domains, rollbackEntries); err != nil {
			return err
		}
	}
	if len(sel.Streams) > 0 {
		if err := deployReverseProxyConfFiles(ctx, dacApplier, provider, reverseProxyStack, reverseProxyRoot, reverseProxyUser, providerCfg.StreamDir, filepath.Join(unpackDir, "stream"), rollbackDir, "stream_dir", "stream", sel.Streams, rollbackEntries); err != nil {
			return err
		}
	}
	if len(sel.Certs) > 0 {
		return deployReverseProxyCerts(ctx, dacApplier, provider, reverseProxyStack, reverseProxyRoot, reverseProxyUser, providerCfg.CertsDir, unpackDir, rollbackDir, sel.Certs, rollbackEntries)
	}
	return nil
}

func applyReverseProxyParentDirDAC(
	ctx context.Context,
	dacApplier *dac.Applier,
	reverseProxyStack *config.StackConfig,
	reverseProxyUser config.UserConfig,
	cleanRelDir string,
	kind string,
) error {
	parentRules := resolveReverseProxyParentDirDACRules(reverseProxyStack, cleanRelDir)
	if len(parentRules) == 0 {
		return nil
	}
	if _, dacErr := dacApplier.ApplyStack(ctx, filepath.Clean(reverseProxyStack.StackDir), reverseProxyUser, parentRules, false); dacErr != nil {
		return fmt.Errorf("apply DAC to reverse-proxy %s dir %q: %w", kind, cleanRelDir, dacErr)
	}
	return nil
}

func deployReverseProxyConfFiles(
	ctx context.Context,
	dacApplier *dac.Applier,
	provider reverseproxy.Provider,
	reverseProxyStack *config.StackConfig,
	reverseProxyRoot *os.Root,
	reverseProxyUser config.UserConfig,
	targetRelDir string,
	stagedDir string,
	rollbackDir string,
	dirField string,
	kind string,
	confs []string,
	rollbackEntries *[]reverseProxyRollbackEntry,
) error {
	cleanRelDir, relErr := reverseproxy.CleanRelativeDir(targetRelDir, "reverse_proxy_provider."+dirField, true)
	if relErr != nil {
		return fmt.Errorf("validate reverse-proxy %s dir: %w", kind, relErr)
	}
	dirRoot, rootErr := openStackDirNoSymlink(reverseProxyRoot, cleanRelDir, true)
	if rootErr != nil {
		return fmt.Errorf("ensure reverse-proxy %s dir %q: %w", kind, cleanRelDir, rootErr)
	}
	if parentErr := applyReverseProxyParentDirDAC(ctx, dacApplier, reverseProxyStack, reverseProxyUser, cleanRelDir, kind); parentErr != nil {
		return errors.Join(parentErr, rootClose(dirRoot))
	}

	targetDirAbs := filepath.Join(filepath.Clean(reverseProxyStack.StackDir), filepath.FromSlash(cleanRelDir))
	for idx, conf := range confs {
		src := filepath.Join(stagedDir, conf)
		bak := filepath.Join(rollbackDir, fmt.Sprintf("%s_%d_%s", kind, idx, conf))
		rules := resolveReverseProxyConfDACRules(provider, reverseProxyStack, cleanRelDir, conf, reverseProxyUser.Name)

		entry, snapErr := snapshotExistingReverseProxyPath(dirRoot, cleanRelDir, targetDirAbs, conf, bak)
		if snapErr != nil {
			return errors.Join(snapErr, rootClose(dirRoot))
		}
		entry.dacRules = rules
		*rollbackEntries = append(*rollbackEntries, entry)

		if copyErr := copyPlainFileToRoot(src, dirRoot, conf, 0o640); copyErr != nil {
			return errors.Join(copyErr, rootClose(dirRoot))
		}
		if _, dacErr := dacApplier.ApplyStack(ctx, targetDirAbs, reverseProxyUser, rules, false); dacErr != nil {
			return errors.Join(fmt.Errorf("apply DAC to reverse-proxy %s %q: %w", kind, conf, dacErr), rootClose(dirRoot))
		}
	}
	return rootClose(dirRoot)
}

func deployReverseProxyCerts(
	ctx context.Context,
	dacApplier *dac.Applier,
	provider reverseproxy.Provider,
	reverseProxyStack *config.StackConfig,
	reverseProxyRoot *os.Root,
	reverseProxyUser config.UserConfig,
	certsRelDir string,
	unpackDir string,
	rollbackDir string,
	certs []string,
	rollbackEntries *[]reverseProxyRollbackEntry,
) error {
	cleanCertsRel, relErr := reverseproxy.CleanRelativeDir(certsRelDir, "reverse_proxy_provider.certs_dir", true)
	if relErr != nil {
		return fmt.Errorf("validate reverse-proxy certs dir: %w", relErr)
	}
	certsRoot, rootErr := openStackDirNoSymlink(reverseProxyRoot, cleanCertsRel, true)
	if rootErr != nil {
		return fmt.Errorf("ensure reverse-proxy certs dir %q: %w", cleanCertsRel, rootErr)
	}
	if parentErr := applyReverseProxyParentDirDAC(ctx, dacApplier, reverseProxyStack, reverseProxyUser, cleanCertsRel, "certs"); parentErr != nil {
		return errors.Join(parentErr, rootClose(certsRoot))
	}

	certsAbsDir := filepath.Join(filepath.Clean(reverseProxyStack.StackDir), filepath.FromSlash(cleanCertsRel))
	for idx, acme := range certs {
		srcDir := filepath.Join(unpackDir, "certs", acme)
		bakDir := filepath.Join(rollbackDir, fmt.Sprintf("cert_%d_%s", idx, acme))
		rules := resolveReverseProxyCertDACRules(provider, reverseProxyStack, cleanCertsRel, acme, reverseProxyUser.Name)

		entry, snapErr := snapshotExistingReverseProxyPath(certsRoot, cleanCertsRel, certsAbsDir, acme, bakDir)
		if snapErr != nil {
			return errors.Join(snapErr, rootClose(certsRoot))
		}
		entry.dacRules = rules
		*rollbackEntries = append(*rollbackEntries, entry)

		if rmErr := rootRemoveAll(certsRoot, acme); rmErr != nil {
			return errors.Join(fmt.Errorf("clear target cert dir %q: %w", acme, rmErr), rootClose(certsRoot))
		}
		if copyErr := copyDirectoryIntoRoot(srcDir, certsRoot, acme); copyErr != nil {
			return errors.Join(copyErr, rootClose(certsRoot))
		}
		if _, dacErr := dacApplier.ApplyStack(ctx, certsAbsDir, reverseProxyUser, rules, false); dacErr != nil {
			return errors.Join(fmt.Errorf("apply DAC to reverse-proxy certs %q: %w", acme, dacErr), rootClose(certsRoot))
		}
	}
	return rootClose(certsRoot)
}
