// Package dac enforces declarative ownership and permission policies natively without host shell wrappers.
package dac

import (
	"context"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strconv"

	"github.com/webstudiobond/backup-recovery-orchestrator/internal/config"
)

// Change describes a single resolved ownership and mode transition performed or planned by Apply.
type Change struct {
	Path      string
	SkipCause string
	UID       int
	GID       int
	PrevUID   int
	PrevGID   int
	Mode      os.FileMode
	PrevMode  os.FileMode
	HasOwner  bool
	HasMode   bool
	HasPrev   bool
	Skipped   bool
}

// NeedsUpdate reports whether applying this change modifies the target path ownership or permissions.
func (c *Change) NeedsUpdate() bool {
	if c.Skipped {
		return false
	}
	if !c.HasPrev {
		return c.HasOwner || c.HasMode
	}
	if c.HasOwner && (c.PrevUID != c.UID || c.PrevGID != c.GID) {
		return true
	}
	return c.HasMode && c.PrevMode != c.Mode
}

// CollapseChanges merges repeated rules targeting the same path into a single net transition.
func CollapseChanges(changes []Change) []Change {
	if len(changes) == 0 {
		return nil
	}
	indexByPath := make(map[string]int, len(changes))
	collapsed := make([]Change, 0, len(changes))
	for i := range changes {
		curr := changes[i]
		idx, seen := indexByPath[curr.Path]
		if !seen {
			indexByPath[curr.Path] = len(collapsed)
			collapsed = append(collapsed, curr)
			continue
		}
		if curr.Skipped {
			continue
		}
		prev := &collapsed[idx]
		if prev.Skipped {
			collapsed[idx] = curr
			continue
		}
		if curr.HasOwner {
			prev.UID = curr.UID
			prev.GID = curr.GID
			prev.HasOwner = true
		}
		if curr.HasMode {
			prev.Mode = curr.Mode
			prev.HasMode = true
		}
	}
	return collapsed
}

// Applier resolves owner specs and applies file modes and numeric ownership safely inside target paths.
type Applier struct {
	Lchown      func(path string, uid, gid int) error
	Chmod       func(path string, mode os.FileMode) error
	LookupUser  func(name string) (int, error)
	LookupGroup func(name string) (int, error)
}

func defaultLchown(path string, uid, gid int) error {
	if err := os.Lchown(path, uid, gid); err != nil {
		return fmt.Errorf("lchown %q (%d:%d): %w", path, uid, gid, err)
	}
	return nil
}

func defaultChmod(path string, mode os.FileMode) error {
	if err := os.Chmod(path, mode); err != nil {
		return fmt.Errorf("chmod %q (%04o): %w", path, mode, err)
	}
	return nil
}

var (
	userLookup      = user.Lookup
	userLookupGroup = user.LookupGroup
	filepathAbs     = filepath.Abs
)

func defaultLookupUser(name string) (int, error) {
	u, err := userLookup(name)
	if err != nil {
		return 0, fmt.Errorf("lookup user %q: %w", name, err)
	}
	id, convErr := strconv.Atoi(u.Uid)
	if convErr != nil {
		return 0, fmt.Errorf("parse uid %q for user %q: %w", u.Uid, name, convErr)
	}
	return id, nil
}

func defaultLookupGroup(name string) (int, error) {
	g, err := userLookupGroup(name)
	if err != nil {
		return 0, fmt.Errorf("lookup group %q: %w", name, err)
	}
	id, convErr := strconv.Atoi(g.Gid)
	if convErr != nil {
		return 0, fmt.Errorf("parse gid %q for group %q: %w", g.Gid, name, convErr)
	}
	return id, nil
}

// NewApplier constructs an Applier backed by standard OS ownership and permission system calls.
func NewApplier() *Applier {
	return &Applier{
		Lchown:      defaultLchown,
		Chmod:       defaultChmod,
		LookupUser:  defaultLookupUser,
		LookupGroup: defaultLookupGroup,
	}
}

// ApplyStack enforces DAC rules strictly within stackDir, rejecting symlinks and path escapes.
func (a *Applier) ApplyStack(ctx context.Context, stackDir string, stackUser config.UserConfig, rules []config.DACRule, dryRun bool) ([]Change, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("context canceled before applying DAC rules: %w", err)
	}

	absRoot, err := filepathAbs(filepath.Clean(stackDir))
	if err != nil {
		return nil, fmt.Errorf("resolve stack_dir %q: %w", stackDir, err)
	}

	changes := make([]Change, 0, len(rules))
	for i := range rules {
		rule := &rules[i]
		target, pathErr := resolveWithinRoot(absRoot, rule.Path)
		if pathErr != nil {
			return nil, fmt.Errorf("dac rule %q: %w", rule.Path, pathErr)
		}

		applied, applyErr := a.applySingleRule(ctx, target, stackUser, rule, dryRun)
		if applyErr != nil {
			return nil, fmt.Errorf("apply dac rule %q: %w", rule.Path, applyErr)
		}
		changes = append(changes, applied...)
	}
	return changes, nil
}

// ApplyHostFiles enforces configured DAC attributes on captured host-level paths outside stack_dir.
func (a *Applier) ApplyHostFiles(ctx context.Context, stackUser config.UserConfig, items []config.HostFileConfig, dryRun bool) ([]Change, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("context canceled before applying host DAC rules: %w", err)
	}

	changes := make([]Change, 0, len(items))
	for i := range items {
		item := &items[i]
		if item.Owner == "" && item.DirMode == "" && item.FileMode == "" {
			continue
		}
		rule := config.DACRule{
			Path:      item.Path,
			Owner:     item.Owner,
			DirMode:   item.DirMode,
			FileMode:  item.FileMode,
			Recursive: true,
			Optional:  item.Optional,
		}
		applied, err := a.applySingleRule(ctx, filepath.Clean(item.Path), stackUser, &rule, dryRun)
		if err != nil {
			return nil, fmt.Errorf("apply host_files DAC for %q: %w", item.Path, err)
		}
		changes = append(changes, applied...)
	}
	return changes, nil
}
