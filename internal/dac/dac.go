// Package dac enforces declarative ownership and permission policies natively without host shell wrappers.
package dac

import (
	"context"
	"errors"
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

var (
	userLookup      = user.Lookup
	userLookupGroup = user.LookupGroup
	filepathAbs     = filepath.Abs
	openRoot        = os.OpenRoot
	rootSameFile    = os.SameFile
	rootClose       = (*os.Root).Close
	rootLstat       = (*os.Root).Lstat
	rootStat        = (*os.Root).Stat
	rootOpenDot     = (*os.Root).Open
	rootOpenFile    = (*os.Root).OpenFile
	rootOpenRoot    = (*os.Root).OpenRoot
	rootLchown      = (*os.Root).Lchown
	rootChmod       = (*os.Root).Chmod
	fileClose       = (*os.File).Close
	fileStat        = (*os.File).Stat
	fileChown       = (*os.File).Chown
	fileChmod       = (*os.File).Chmod
	fileReadDir     = (*os.File).ReadDir
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

// NewApplier constructs an Applier backed by descriptor-safe OS ownership and permission system calls.
func NewApplier() *Applier {
	return &Applier{
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
	if len(rules) == 0 {
		return nil, nil
	}

	stackRoot, openErr := openRoot(absRoot)
	if openErr != nil {
		return nil, fmt.Errorf("open stack_dir %q: %w", absRoot, openErr)
	}

	changes, applyErr := a.applyStackRules(ctx, stackRoot, absRoot, stackUser, rules, dryRun)
	closeErr := rootClose(stackRoot)
	if closeErr != nil {
		return nil, errors.Join(applyErr, fmt.Errorf("close stack_dir %q: %w", absRoot, closeErr))
	}
	return changes, applyErr
}

func (a *Applier) applyStackRules(ctx context.Context, stackRoot *os.Root, absRoot string, stackUser config.UserConfig, rules []config.DACRule, dryRun bool) ([]Change, error) {
	changes := make([]Change, 0, len(rules))
	for i := range rules {
		rule := &rules[i]
		cleanRel, pathErr := resolveWithinRoot(absRoot, rule.Path)
		if pathErr != nil {
			return nil, fmt.Errorf("dac rule %q: %w", rule.Path, pathErr)
		}

		applied, applyErr := a.applyRuleInRoot(ctx, stackRoot, absRoot, cleanRel, stackUser, rule, dryRun)
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
		applied, err := a.applyHostFileItem(ctx, stackUser, item, dryRun)
		if err != nil {
			return nil, fmt.Errorf("apply host_files DAC for %q: %w", item.Path, err)
		}
		changes = append(changes, applied...)
	}
	return changes, nil
}

func (a *Applier) applyHostFileItem(ctx context.Context, stackUser config.UserConfig, item *config.HostFileConfig, dryRun bool) ([]Change, error) {
	cleanPath := filepath.Clean(item.Path)
	parentDir := filepath.Dir(cleanPath)
	baseName := filepath.Base(cleanPath)
	if cleanPath == string(filepath.Separator) || baseName == "." || baseName == string(filepath.Separator) {
		return nil, fmt.Errorf("invalid host_file path %q", item.Path)
	}

	parentRoot, openErr := openRoot(parentDir)
	if openErr != nil {
		if errors.Is(openErr, os.ErrNotExist) && item.Optional {
			return []Change{{
				Path:      cleanPath,
				Skipped:   true,
				SkipCause: "optional path not present",
			}}, nil
		}
		return nil, fmt.Errorf("open parent directory %q: %w", parentDir, openErr)
	}

	rule := config.DACRule{
		Path:      baseName,
		Owner:     item.Owner,
		DirMode:   item.DirMode,
		FileMode:  item.FileMode,
		Recursive: true,
		Optional:  item.Optional,
	}
	applied, applyErr := a.applyRuleInRoot(ctx, parentRoot, parentDir, baseName, stackUser, &rule, dryRun)
	closeErr := rootClose(parentRoot)
	if closeErr != nil {
		return nil, errors.Join(applyErr, fmt.Errorf("close parent directory %q: %w", parentDir, closeErr))
	}
	return applied, applyErr
}
