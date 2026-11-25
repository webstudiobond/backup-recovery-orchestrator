package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/webstudiobond/backup-recovery-orchestrator/internal/config"
	"github.com/webstudiobond/backup-recovery-orchestrator/internal/dac"
)

type angieRollbackEntry struct {
	baseDir    string
	targetPath string
	backupPath string
	dacRules   []config.DACRule
	existed    bool
	isDir      bool
}

func resolveAngieDACUser(angieStack *config.StackConfig, angieSvc string) config.UserConfig {
	if angieStack != nil && strings.TrimSpace(angieStack.User.Name) != "" {
		return angieStack.User
	}
	return config.UserConfig{Name: angieSvc}
}

func resolveAngieConfDACRules(angieStack *config.StackConfig, targetDir, conf, angieSvc string) []config.DACRule {
	rule := config.DACRule{
		Path:     conf,
		Owner:    "root:" + angieSvc,
		FileMode: "0440",
	}
	stackRel, ok := angieTargetStackRel(angieStack, filepath.Join(targetDir, conf))
	if !ok {
		return []config.DACRule{rule}
	}
	for i := range angieStack.DAC {
		r := &angieStack.DAC[i]
		cleanRule := filepath.ToSlash(filepath.Clean(r.Path))
		if !angieDACRuleMatches(cleanRule, stackRel, r.Recursive) {
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

func resolveAngieCertDACRules(angieStack *config.StackConfig, certsDir, acme, angieSvc string) []config.DACRule {
	baseRule := config.DACRule{
		Path:      acme,
		Owner:     angieSvc + ":" + angieSvc,
		DirMode:   "0700",
		FileMode:  "0600",
		Recursive: true,
	}
	stackRel, ok := angieTargetStackRel(angieStack, filepath.Join(certsDir, acme))
	if !ok {
		return []config.DACRule{baseRule}
	}

	var extra []config.DACRule
	childPrefix := stackRel + "/"
	for i := range angieStack.DAC {
		r := &angieStack.DAC[i]
		cleanRule := filepath.ToSlash(filepath.Clean(r.Path))
		if angieDACRuleMatches(cleanRule, stackRel, r.Recursive) {
			applyAngieDirRule(&baseRule, r)
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

func applyAngieDirRule(dst, src *config.DACRule) {
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

func angieTargetStackRel(angieStack *config.StackConfig, targetPath string) (string, bool) {
	if angieStack == nil || strings.TrimSpace(angieStack.StackDir) == "" {
		return "", false
	}
	cleanStack := filepath.Clean(angieStack.StackDir)
	cleanTarget := filepath.Clean(targetPath)
	rel, err := filepathRel(cleanStack, cleanTarget)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return filepath.ToSlash(rel), true
}

func angieDACRuleMatches(cleanRule, targetRel string, recursive bool) bool {
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

func snapshotExistingPath(targetPath, backupPath string) (angieRollbackEntry, error) {
	info, err := os.Lstat(targetPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return angieRollbackEntry{targetPath: targetPath, existed: false}, nil
		}
		return angieRollbackEntry{}, fmt.Errorf("stat existing angie asset %q: %w", targetPath, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return angieRollbackEntry{}, fmt.Errorf("refusing symlink angie asset %q", targetPath)
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

func rollbackAngieFiles(ctx context.Context, dacApplier *dac.Applier, angieUser config.UserConfig, entries []angieRollbackEntry) error {
	rollbackCtx := context.WithoutCancel(ctx)
	var errs []error
	for i := len(entries) - 1; i >= 0; i-- {
		if err := rollbackSingleAngieEntry(rollbackCtx, dacApplier, angieUser, &entries[i]); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func rollbackSingleAngieEntry(ctx context.Context, dacApplier *dac.Applier, angieUser config.UserConfig, entry *angieRollbackEntry) error {
	if rmErr := os.RemoveAll(entry.targetPath); rmErr != nil {
		return fmt.Errorf("rollback remove %q: %w", entry.targetPath, rmErr)
	}
	if !entry.existed {
		return nil
	}
	if entry.isDir {
		if cpErr := copyDirectoryContents(entry.backupPath, entry.targetPath); cpErr != nil {
			return fmt.Errorf("rollback restore dir %q: %w", entry.targetPath, cpErr)
		}
	} else if cpErr := copyPlainFile(entry.backupPath, entry.targetPath, 0o600); cpErr != nil {
		return fmt.Errorf("rollback restore file %q: %w", entry.targetPath, cpErr)
	}
	if _, dacErr := dacApplier.ApplyStack(ctx, entry.baseDir, angieUser, entry.dacRules, false); dacErr != nil {
		return fmt.Errorf("rollback restore DAC for %q: %w", entry.targetPath, dacErr)
	}
	return nil
}
