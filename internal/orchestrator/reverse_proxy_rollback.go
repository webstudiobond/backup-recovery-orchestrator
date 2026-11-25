package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/webstudiobond/backup-recovery-orchestrator/internal/config"
	"github.com/webstudiobond/backup-recovery-orchestrator/internal/dac"
	"github.com/webstudiobond/backup-recovery-orchestrator/internal/reverseproxy"
)

type reverseProxyRollbackEntry struct {
	dirRel     string
	baseDir    string
	entryName  string
	backupPath string
	dacRules   []config.DACRule
	existed    bool
	isDir      bool
}

var walkReverseProxyRootFS = fs.WalkDir

func resolveReverseProxyDACUser(reverseProxyStack *config.StackConfig, serviceName string) config.UserConfig {
	if reverseProxyStack != nil && strings.TrimSpace(reverseProxyStack.User.Name) != "" {
		return reverseProxyStack.User
	}
	return config.UserConfig{Name: serviceName}
}

func dacRuleFromPolicy(assetPath string, policy reverseproxy.DACPolicy) config.DACRule {
	return config.DACRule{
		Path:      assetPath,
		Owner:     policy.Owner,
		DirMode:   policy.DirMode,
		FileMode:  policy.FileMode,
		Recursive: policy.Recursive,
	}
}

func resolveReverseProxyConfDACRules(
	provider reverseproxy.Provider,
	reverseProxyStack *config.StackConfig,
	dirRel string,
	conf string,
	ownerName string,
) []config.DACRule {
	rule := dacRuleFromPolicy(conf, provider.DefaultConfDACPolicy(ownerName))
	if reverseProxyStack == nil {
		return []config.DACRule{rule}
	}
	stackRel := path.Join(dirRel, conf)
	for i := range reverseProxyStack.DAC {
		r := &reverseProxyStack.DAC[i]
		cleanRule := filepath.ToSlash(filepath.Clean(r.Path))
		if !reverseProxyDACRuleMatches(cleanRule, stackRel, r.Recursive) {
			continue
		}
		if r.Owner != "" {
			rule.Owner = r.Owner
		}
		if r.FileMode != "" {
			rule.FileMode = r.FileMode
		}
	}
	return []config.DACRule{rule}
}

func resolveReverseProxyCertDACRules(
	provider reverseproxy.Provider,
	reverseProxyStack *config.StackConfig,
	certsRel string,
	acme string,
	ownerName string,
) []config.DACRule {
	baseRule := dacRuleFromPolicy(acme, provider.DefaultCertDACPolicy(ownerName))
	if reverseProxyStack == nil {
		return []config.DACRule{baseRule}
	}
	stackRel := path.Join(certsRel, acme)
	var extra []config.DACRule
	childPrefix := stackRel + "/"
	for i := range reverseProxyStack.DAC {
		r := &reverseProxyStack.DAC[i]
		cleanRule := filepath.ToSlash(filepath.Clean(r.Path))
		if reverseProxyDACRuleMatches(cleanRule, stackRel, r.Recursive) {
			applyReverseProxyDirRule(&baseRule, r)
			continue
		}
		if subRel, hasChild := strings.CutPrefix(cleanRule, childPrefix); hasChild {
			childRule := *r
			childRule.Path = path.Join(acme, subRel)
			extra = append(extra, childRule)
		}
	}
	return append([]config.DACRule{baseRule}, extra...)
}

func resolveReverseProxyParentDirDACRules(reverseProxyStack *config.StackConfig, cleanRelDir string) []config.DACRule {
	if reverseProxyStack == nil || len(reverseProxyStack.DAC) == 0 {
		return nil
	}
	segments := strings.Split(cleanRelDir, "/")
	rules := make([]config.DACRule, 0, len(segments))
	for i := range segments {
		segPath := path.Join(segments[:i+1]...)
		if rule, ok := resolveReverseProxyDirRule(reverseProxyStack.DAC, segPath); ok {
			rules = append(rules, rule)
		}
	}
	return rules
}

func resolveReverseProxyDirRule(dacRules []config.DACRule, segPath string) (config.DACRule, bool) {
	rule := config.DACRule{Path: segPath}
	for i := range dacRules {
		r := &dacRules[i]
		cleanRule := filepath.ToSlash(filepath.Clean(r.Path))
		if !reverseProxyDACRuleMatches(cleanRule, segPath, r.Recursive) {
			continue
		}
		if r.Owner != "" {
			rule.Owner = r.Owner
		}
		if r.DirMode != "" {
			rule.DirMode = r.DirMode
		}
	}
	return rule, rule.Owner != "" || rule.DirMode != ""
}

func applyReverseProxyDirRule(dst, src *config.DACRule) {
	if src.Owner != "" {
		dst.Owner = src.Owner
	}
	if src.DirMode != "" {
		dst.DirMode = src.DirMode
	}
	if src.FileMode != "" {
		dst.FileMode = src.FileMode
	}
}

func reverseProxyDACRuleMatches(cleanRule, targetRel string, recursive bool) bool {
	if cleanRule == targetRel {
		return true
	}
	if !recursive {
		return false
	}
	if cleanRule == "." {
		return true
	}
	return strings.HasPrefix(targetRel, cleanRule+"/")
}

func snapshotExistingReverseProxyPath(parentRoot *os.Root, dirRel, baseDir, entryName, backupPath string) (reverseProxyRollbackEntry, error) {
	info, err := rootLstat(parentRoot, entryName)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return reverseProxyRollbackEntry{dirRel: dirRel, baseDir: baseDir, entryName: entryName, existed: false}, nil
		}
		return reverseProxyRollbackEntry{}, fmt.Errorf("stat existing reverse-proxy asset %q: %w", entryName, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return reverseProxyRollbackEntry{}, fmt.Errorf("refusing symlink reverse-proxy asset %q", entryName)
	}

	if info.IsDir() {
		if copyErr := copyRootDirToOS(parentRoot, entryName, backupPath); copyErr != nil {
			return reverseProxyRollbackEntry{}, fmt.Errorf("snapshot existing reverse-proxy dir %q: %w", entryName, copyErr)
		}
		return reverseProxyRollbackEntry{dirRel: dirRel, baseDir: baseDir, entryName: entryName, backupPath: backupPath, existed: true, isDir: true}, nil
	}

	if copyErr := copyRootFileToOS(parentRoot, entryName, backupPath, info.Mode().Perm()); copyErr != nil {
		return reverseProxyRollbackEntry{}, fmt.Errorf("snapshot existing reverse-proxy file %q: %w", entryName, copyErr)
	}
	return reverseProxyRollbackEntry{dirRel: dirRel, baseDir: baseDir, entryName: entryName, backupPath: backupPath, existed: true, isDir: false}, nil
}

func rollbackReverseProxyFiles(
	ctx context.Context,
	dacApplier *dac.Applier,
	reverseProxyRoot *os.Root,
	reverseProxyUser config.UserConfig,
	entries []reverseProxyRollbackEntry,
) error {
	rollbackCtx := context.WithoutCancel(ctx)
	var errs []error
	for i := len(entries) - 1; i >= 0; i-- {
		if err := rollbackSingleReverseProxyEntry(rollbackCtx, dacApplier, reverseProxyRoot, reverseProxyUser, &entries[i]); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func rollbackSingleReverseProxyEntry(
	ctx context.Context,
	dacApplier *dac.Applier,
	reverseProxyRoot *os.Root,
	reverseProxyUser config.UserConfig,
	entry *reverseProxyRollbackEntry,
) error {
	targetRoot, rootErr := openStackDirNoSymlink(reverseProxyRoot, entry.dirRel, true)
	if rootErr != nil {
		return fmt.Errorf("rollback open target dir %q: %w", entry.dirRel, rootErr)
	}
	if rmErr := rootRemoveAll(targetRoot, entry.entryName); rmErr != nil {
		return errors.Join(fmt.Errorf("rollback remove %q: %w", entry.entryName, rmErr), rootClose(targetRoot))
	}
	if !entry.existed {
		return rootClose(targetRoot)
	}
	if entry.isDir {
		if cpErr := copyDirectoryIntoRoot(entry.backupPath, targetRoot, entry.entryName); cpErr != nil {
			return errors.Join(fmt.Errorf("rollback restore dir %q: %w", entry.entryName, cpErr), rootClose(targetRoot))
		}
	} else if cpErr := copyPlainFileToRoot(entry.backupPath, targetRoot, entry.entryName, 0o600); cpErr != nil {
		return errors.Join(fmt.Errorf("rollback restore file %q: %w", entry.entryName, cpErr), rootClose(targetRoot))
	}
	if _, dacErr := dacApplier.ApplyStack(ctx, entry.baseDir, reverseProxyUser, entry.dacRules, false); dacErr != nil {
		return errors.Join(fmt.Errorf("rollback restore DAC for %q: %w", entry.entryName, dacErr), rootClose(targetRoot))
	}
	return rootClose(targetRoot)
}

func copyPlainFileToRoot(srcAbs string, dstRoot *os.Root, relName string, mode os.FileMode) error {
	data, err := os.ReadFile(filepath.Clean(srcAbs))
	if err != nil {
		return fmt.Errorf("read %q: %w", srcAbs, err)
	}
	file, openErr := rootOpenFile(dstRoot, relName, os.O_CREATE|os.O_WRONLY|os.O_TRUNC|syscall.O_NOFOLLOW, mode)
	if openErr != nil {
		return fmt.Errorf("open root target %q: %w", relName, openErr)
	}
	return writeFilePayload(file, data, mode)
}

func copyRootFileToOS(srcRoot *os.Root, relName, dstAbs string, mode os.FileMode) error {
	data, err := srcRoot.ReadFile(relName)
	if err != nil {
		return fmt.Errorf("read root file %q: %w", relName, err)
	}
	return writeBytesToOSFile(dstAbs, data, mode)
}

func copyRootDirToOS(srcRoot *os.Root, dirName, dstBackupAbs string) error {
	subRoot, err := rootOpenRoot(srcRoot, dirName)
	if err != nil {
		return fmt.Errorf("open root dir %q: %w", dirName, err)
	}
	copyErr := walkReverseProxyRootFS(subRoot.FS(), ".", func(rel string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("refusing symlink %q in reverse-proxy asset tree", rel)
		}
		relOS := filepath.FromSlash(rel)
		target := filepath.Join(dstBackupAbs, relOS)
		info, infoErr := entry.Info()
		if infoErr != nil {
			return fmt.Errorf("stat %q: %w", rel, infoErr)
		}
		if entry.IsDir() {
			if mkErr := osMkdirAll(target, info.Mode().Perm()); mkErr != nil {
				return fmt.Errorf("mkdir %q: %w", target, mkErr)
			}
			return nil
		}
		return copyRootFileToOS(subRoot, relOS, target, info.Mode().Perm())
	})
	return errors.Join(copyErr, rootClose(subRoot))
}

func copyDirectoryIntoRoot(srcAbs string, dstParentRoot *os.Root, dstName string) error {
	cleanSrc := filepath.Clean(srcAbs)
	dstRoot, rootErr := openStackDirNoSymlink(dstParentRoot, dstName, true)
	if rootErr != nil {
		return fmt.Errorf("open root dir %q: %w", dstName, rootErr)
	}
	walkErr := filepathWalkDir(cleanSrc, func(srcPath string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("refusing symlink %q in reverse-proxy asset tree", srcPath)
		}
		rel, relErr := filepathRel(cleanSrc, srcPath)
		if relErr != nil {
			return fmt.Errorf("relative path for %q: %w", srcPath, relErr)
		}
		if rel == "." {
			return nil
		}
		slashRel := filepath.ToSlash(rel)
		if entry.IsDir() {
			sub, subErr := openStackDirNoSymlink(dstRoot, slashRel, true)
			if subErr != nil {
				return subErr
			}
			return rootClose(sub)
		}
		info, infoErr := entry.Info()
		if infoErr != nil {
			return fmt.Errorf("stat file %q: %w", srcPath, infoErr)
		}
		return copyPlainFileToRoot(srcPath, dstRoot, rel, info.Mode().Perm())
	})
	return errors.Join(walkErr, rootClose(dstRoot))
}
