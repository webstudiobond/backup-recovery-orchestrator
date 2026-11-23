package dac

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"syscall"

	"github.com/webstudiobond/backup-recovery-orchestrator/internal/config"
)

func (a *Applier) applySingleRule(ctx context.Context, target string, stackUser config.UserConfig, rule *config.DACRule, dryRun bool) ([]Change, error) {
	info, err := os.Lstat(target)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) && rule.Optional {
			return []Change{{
				Path:      target,
				Skipped:   true,
				SkipCause: "optional path not present",
			}}, nil
		}
		return nil, fmt.Errorf("stat %q: %w", target, err)
	}

	if info.Mode()&os.ModeSymlink != 0 {
		return []Change{{
			Path:      target,
			Skipped:   true,
			SkipCause: "refusing to modify symlink target",
		}}, nil
	}

	uid, gid, hasOwner, ownerErr := a.resolveOwner(rule.Owner, stackUser)
	if ownerErr != nil {
		return nil, ownerErr
	}

	dirMode, hasDirMode, dirErr := parseOptionalMode(rule.DirMode)
	if dirErr != nil {
		return nil, dirErr
	}
	fileMode, hasFileMode, fileErr := parseOptionalMode(rule.FileMode)
	if fileErr != nil {
		return nil, fileErr
	}

	if !rule.Recursive || !info.IsDir() {
		change := Change{
			Path:     target,
			UID:      uid,
			GID:      gid,
			HasOwner: hasOwner,
		}
		populatePrevAttrs(&change, info)
		if info.IsDir() && hasDirMode {
			change.Mode = dirMode
			change.HasMode = true
		} else if !info.IsDir() && hasFileMode {
			change.Mode = fileMode
			change.HasMode = true
		}
		if err := a.commitChange(&change, dryRun); err != nil {
			return nil, err
		}
		return []Change{change}, nil
	}

	return a.walkAndApply(ctx, target, uid, gid, hasOwner, dirMode, hasDirMode, fileMode, hasFileMode, dryRun)
}

func (a *Applier) walkAndApply(
	ctx context.Context,
	root string,
	uid, gid int,
	hasOwner bool,
	dirMode os.FileMode,
	hasDirMode bool,
	fileMode os.FileMode,
	hasFileMode bool,
	dryRun bool,
) ([]Change, error) {
	var changes []Change
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return fmt.Errorf("context canceled during DAC walk: %w", ctxErr)
		}
		if entry.Type()&os.ModeSymlink != 0 {
			changes = append(changes, Change{
				Path:      path,
				Skipped:   true,
				SkipCause: "symlink skipped",
			})
			return nil
		}

		info, infoErr := entry.Info()
		if infoErr != nil {
			return fmt.Errorf("stat %q: %w", path, infoErr)
		}

		change := Change{
			Path:     path,
			UID:      uid,
			GID:      gid,
			HasOwner: hasOwner,
		}
		populatePrevAttrs(&change, info)
		if entry.IsDir() && hasDirMode {
			change.Mode = dirMode
			change.HasMode = true
		} else if !entry.IsDir() && hasFileMode {
			change.Mode = fileMode
			change.HasMode = true
		}

		if err := a.commitChange(&change, dryRun); err != nil {
			return err
		}
		changes = append(changes, change)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk %q: %w", root, err)
	}
	return changes, nil
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

func (a *Applier) commitChange(change *Change, dryRun bool) error {
	if dryRun || change.Skipped {
		return nil
	}
	if change.HasOwner {
		lchown := a.Lchown
		if lchown == nil {
			lchown = defaultLchown
		}
		if err := lchown(change.Path, change.UID, change.GID); err != nil {
			return err
		}
	}
	if change.HasMode {
		chmod := a.Chmod
		if chmod == nil {
			chmod = defaultChmod
		}
		if err := chmod(change.Path, change.Mode); err != nil {
			return err
		}
	}
	return nil
}
