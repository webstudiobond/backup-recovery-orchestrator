package dac

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"syscall"
)

func (a *Applier) commitDirChange(dirRoot *os.Root, change *Change) error {
	if a.Lchown != nil && a.Chmod != nil {
		return a.commitHookedChange(change)
	}

	dirFile, openErr := rootOpenDot(dirRoot, ".")
	if openErr != nil {
		return fmt.Errorf("open directory %q for DAC: %w", change.Path, openErr)
	}
	return a.applyToFileHandle(dirFile, change)
}

func (a *Applier) commitFileChange(parentRoot *os.Root, name string, lstatInfo fs.FileInfo, change *Change) error {
	if a.Lchown != nil && a.Chmod != nil {
		return a.commitHookedChange(change)
	}
	if lstatInfo == nil || !lstatInfo.Mode().IsRegular() {
		return fmt.Errorf("refusing DAC mutation on non-regular file %q", change.Path)
	}

	opened, openErr := rootOpenFile(parentRoot, name, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if openErr != nil {
		if errors.Is(openErr, os.ErrPermission) {
			return a.commitPermissionRestrictedFile(parentRoot, name, lstatInfo, change)
		}
		return fmt.Errorf("open %q for DAC: %w", change.Path, openErr)
	}
	return a.commitOpenedRegularFile(opened, lstatInfo, change)
}

func (a *Applier) commitPermissionRestrictedFile(parentRoot *os.Root, name string, lstatInfo fs.FileInfo, change *Change) error {
	return a.commitParentRootEntry(parentRoot, name, lstatInfo, change)
}

func (a *Applier) commitOpenedRegularFile(opened *os.File, lstatInfo fs.FileInfo, change *Change) error {
	fInfo, statErr := fileStat(opened)
	if statErr != nil {
		return errors.Join(fmt.Errorf("fstat %q: %w", change.Path, statErr), fileClose(opened))
	}
	if !fInfo.Mode().IsRegular() || !rootSameFile(lstatInfo, fInfo) {
		return errors.Join(fmt.Errorf("concurrent path replacement detected at %q", change.Path), fileClose(opened))
	}
	return a.applyToFileHandle(opened, change)
}

func (a *Applier) applyToFileHandle(f *os.File, change *Change) error {
	var ownerErr, modeErr error
	if change.HasOwner {
		if a.Lchown != nil {
			ownerErr = a.Lchown(change.Path, change.UID, change.GID)
		} else if err := fileChown(f, change.UID, change.GID); err != nil {
			ownerErr = fmt.Errorf("fchown %q (%d:%d): %w", change.Path, change.UID, change.GID, err)
		}
	}
	if ownerErr == nil && change.HasMode {
		if a.Chmod != nil {
			modeErr = a.Chmod(change.Path, change.Mode)
		} else if err := fileChmod(f, change.Mode); err != nil {
			modeErr = fmt.Errorf("fchmod %q (%04o): %w", change.Path, change.Mode, err)
		}
	}
	return errors.Join(ownerErr, modeErr, wrapCloseErr(change.Path, fileClose(f)))
}

func (a *Applier) commitParentRootEntry(parentRoot *os.Root, name string, lstatInfo fs.FileInfo, change *Change) error {
	recheck, statErr := rootLstat(parentRoot, name)
	if statErr != nil {
		return fmt.Errorf("lstat %q: %w", change.Path, statErr)
	}
	if !recheck.Mode().IsRegular() || (lstatInfo != nil && !rootSameFile(lstatInfo, recheck)) {
		return fmt.Errorf("concurrent path replacement detected at %q", change.Path)
	}

	if change.HasOwner {
		if a.Lchown != nil {
			if err := a.Lchown(change.Path, change.UID, change.GID); err != nil {
				return err
			}
		} else if err := rootLchown(parentRoot, name, change.UID, change.GID); err != nil {
			return fmt.Errorf("lchown %q (%d:%d): %w", change.Path, change.UID, change.GID, err)
		}
	}
	if change.HasMode {
		if a.Chmod != nil {
			return a.Chmod(change.Path, change.Mode)
		}
		if err := rootChmod(parentRoot, name, change.Mode); err != nil {
			return fmt.Errorf("chmod %q (%04o): %w", change.Path, change.Mode, err)
		}
	}
	return nil
}

func (a *Applier) commitHookedChange(change *Change) error {
	if change.HasOwner {
		if err := a.Lchown(change.Path, change.UID, change.GID); err != nil {
			return err
		}
	}
	if change.HasMode {
		if err := a.Chmod(change.Path, change.Mode); err != nil {
			return err
		}
	}
	return nil
}

func wrapCloseErr(path string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("close %q: %w", path, err)
}
