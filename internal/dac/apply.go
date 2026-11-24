package dac

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/webstudiobond/backup-recovery-orchestrator/internal/config"
)

type dacPolicy struct {
	uid         int
	gid         int
	dirMode     os.FileMode
	fileMode    os.FileMode
	hasOwner    bool
	hasDirMode  bool
	hasFileMode bool
	dryRun      bool
}

func (a *Applier) applyRuleInRoot(
	ctx context.Context,
	baseRoot *os.Root,
	baseAbs string,
	cleanRel string,
	stackUser config.UserConfig,
	rule *config.DACRule,
	dryRun bool,
) ([]Change, error) {
	if cleanRel == "." {
		return a.applyRootDirRule(ctx, baseRoot, baseAbs, stackUser, rule, dryRun)
	}
	return a.applySubpathRule(ctx, baseRoot, baseAbs, cleanRel, stackUser, rule, dryRun)
}

func (a *Applier) applyRootDirRule(
	ctx context.Context,
	baseRoot *os.Root,
	baseAbs string,
	stackUser config.UserConfig,
	rule *config.DACRule,
	dryRun bool,
) ([]Change, error) {
	info, statErr := rootStat(baseRoot, ".")
	if statErr != nil {
		if errors.Is(statErr, os.ErrNotExist) && rule.Optional {
			return []Change{optionalSkippedChange(baseAbs)}, nil
		}
		return nil, fmt.Errorf("stat %q: %w", baseAbs, statErr)
	}
	policy, polErr := a.buildDACPolicy(stackUser, rule, dryRun)
	if polErr != nil {
		return nil, polErr
	}
	if !rule.Recursive {
		change, applyErr := a.applyDirNode(baseRoot, baseAbs, info, &policy)
		if applyErr != nil {
			return nil, applyErr
		}
		return []Change{change}, nil
	}
	return a.walkRootAndApply(ctx, baseRoot, baseAbs, info, &policy)
}

func (a *Applier) buildDACPolicy(stackUser config.UserConfig, rule *config.DACRule, dryRun bool) (dacPolicy, error) {
	uid, gid, hasOwner, ownerErr := a.resolveOwner(rule.Owner, stackUser)
	if ownerErr != nil {
		return dacPolicy{}, ownerErr
	}
	dirMode, hasDirMode, dirErr := parseOptionalMode(rule.DirMode)
	if dirErr != nil {
		return dacPolicy{}, dirErr
	}
	fileMode, hasFileMode, fileErr := parseOptionalMode(rule.FileMode)
	if fileErr != nil {
		return dacPolicy{}, fileErr
	}
	return dacPolicy{
		uid:         uid,
		gid:         gid,
		dirMode:     dirMode,
		fileMode:    fileMode,
		hasOwner:    hasOwner,
		hasDirMode:  hasDirMode,
		hasFileMode: hasFileMode,
		dryRun:      dryRun,
	}, nil
}

func (a *Applier) applySubpathRule(
	ctx context.Context,
	baseRoot *os.Root,
	baseAbs string,
	cleanRel string,
	stackUser config.UserConfig,
	rule *config.DACRule,
	dryRun bool,
) (changes []Change, err error) {
	parts := strings.Split(filepath.ToSlash(cleanRel), "/")
	targetFullPath := filepath.Join(baseAbs, cleanRel)

	parentRoot, closeParent, found, navErr := navigateParentRoot(baseRoot, parts[:len(parts)-1], targetFullPath)
	if navErr != nil {
		return nil, navErr
	}
	if !found {
		if rule.Optional {
			return []Change{optionalSkippedChange(targetFullPath)}, nil
		}
		return nil, fmt.Errorf("stat %q: %w", targetFullPath, os.ErrNotExist)
	}
	if closeParent {
		defer func() {
			if closeErr := rootClose(parentRoot); closeErr != nil {
				err = errors.Join(err, fmt.Errorf("close parent of %q: %w", targetFullPath, closeErr))
			}
		}()
	}

	lastSeg := parts[len(parts)-1]
	return a.applyRuleLeaf(ctx, parentRoot, lastSeg, targetFullPath, stackUser, rule, dryRun)
}

func (a *Applier) applyRuleLeaf(
	ctx context.Context,
	parentRoot *os.Root,
	lastSeg string,
	targetFullPath string,
	stackUser config.UserConfig,
	rule *config.DACRule,
	dryRun bool,
) ([]Change, error) {
	info, statErr := rootLstat(parentRoot, lastSeg)
	if statErr != nil {
		if errors.Is(statErr, os.ErrNotExist) && rule.Optional {
			return []Change{optionalSkippedChange(targetFullPath)}, nil
		}
		return nil, fmt.Errorf("stat %q: %w", targetFullPath, statErr)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return []Change{{
			Path:      targetFullPath,
			Skipped:   true,
			SkipCause: "refusing to modify symlink target",
		}}, nil
	}
	if !info.IsDir() && !info.Mode().IsRegular() {
		return []Change{{
			Path:      targetFullPath,
			Skipped:   true,
			SkipCause: "refusing to modify non-regular target",
		}}, nil
	}

	policy, polErr := a.buildDACPolicy(stackUser, rule, dryRun)
	if polErr != nil {
		return nil, polErr
	}

	if !info.IsDir() {
		change, err := a.applyFileNode(parentRoot, lastSeg, targetFullPath, info, &policy)
		if err != nil {
			return nil, err
		}
		return []Change{change}, nil
	}

	dirRoot, openErr := openExistingChildDir(parentRoot, lastSeg, info, targetFullPath)
	if openErr != nil {
		return nil, openErr
	}

	var (
		changes []Change
		err     error
	)
	if !rule.Recursive {
		var change Change
		change, err = a.applyDirNode(dirRoot, targetFullPath, info, &policy)
		if err == nil {
			changes = []Change{change}
		}
	} else {
		changes, err = a.walkRootAndApply(ctx, dirRoot, targetFullPath, info, &policy)
	}
	if closeErr := rootClose(dirRoot); closeErr != nil {
		return nil, errors.Join(err, fmt.Errorf("close directory %q: %w", targetFullPath, closeErr))
	}
	return changes, err
}

func (a *Applier) applyDirNode(dirRoot *os.Root, fullPath string, info fs.FileInfo, policy *dacPolicy) (Change, error) {
	change := Change{
		Path:     fullPath,
		UID:      policy.uid,
		GID:      policy.gid,
		HasOwner: policy.hasOwner,
	}
	populatePrevAttrs(&change, info)
	if policy.hasDirMode {
		change.Mode = policy.dirMode
		change.HasMode = true
	}
	if policy.dryRun || (!change.HasOwner && !change.HasMode) {
		return change, nil
	}

	if err := a.commitDirChange(dirRoot, &change); err != nil {
		return Change{}, err
	}
	return change, nil
}

func (a *Applier) applyFileNode(parentRoot *os.Root, name, fullPath string, info fs.FileInfo, policy *dacPolicy) (Change, error) {
	change := Change{
		Path:     fullPath,
		UID:      policy.uid,
		GID:      policy.gid,
		HasOwner: policy.hasOwner,
	}
	populatePrevAttrs(&change, info)
	if policy.hasFileMode {
		change.Mode = policy.fileMode
		change.HasMode = true
	}
	if policy.dryRun || (!change.HasOwner && !change.HasMode) {
		return change, nil
	}

	if err := a.commitFileChange(parentRoot, name, info, &change); err != nil {
		return Change{}, err
	}
	return change, nil
}

func populatePrevAttrs(change *Change, info fs.FileInfo) {
	if info == nil {
		return
	}
	change.PrevMode = info.Mode().Perm()
	if stat, ok := info.Sys().(*syscall.Stat_t); ok && stat != nil {
		if uint64(stat.Uid) <= uint64(math.MaxInt) && uint64(stat.Gid) <= uint64(math.MaxInt) {
			change.PrevUID = int(stat.Uid)
			change.PrevGID = int(stat.Gid)
			change.HasPrev = true
		}
	}
}

func optionalSkippedChange(path string) Change {
	return Change{
		Path:      path,
		Skipped:   true,
		SkipCause: "optional path not present",
	}
}

func symlinkSkippedChange(path string) Change {
	return Change{
		Path:      path,
		Skipped:   true,
		SkipCause: "symlink skipped",
	}
}

func nonRegularSkippedChange(path string) Change {
	return Change{
		Path:      path,
		Skipped:   true,
		SkipCause: "non-regular file skipped",
	}
}
