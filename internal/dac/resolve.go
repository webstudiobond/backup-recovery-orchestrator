package dac

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/webstudiobond/backup-recovery-orchestrator/internal/config"
)

func defaultMaxInt() uint64 {
	return math.MaxInt
}

var (
	maxIntLimit = defaultMaxInt
	filepathRel = filepath.Rel
)

func (a *Applier) resolveOwner(ownerSpec string, stackUser config.UserConfig) (uid, gid int, ok bool, err error) {
	if strings.TrimSpace(ownerSpec) == "" {
		return 0, 0, false, nil
	}

	parts := strings.Split(ownerSpec, ":")
	if len(parts) != 2 {
		return 0, 0, false, fmt.Errorf("invalid owner specification %q", ownerSpec)
	}

	resolvedUID, uidErr := a.resolvePrincipal(strings.TrimSpace(parts[0]), stackUser.Name, stackUser.UID, true)
	if uidErr != nil {
		return 0, 0, false, uidErr
	}
	resolvedGID, gidErr := a.resolvePrincipal(strings.TrimSpace(parts[1]), stackUser.Name, stackUser.GID, false)
	if gidErr != nil {
		return 0, 0, false, gidErr
	}

	return resolvedUID, resolvedGID, true, nil
}

func (a *Applier) resolvePrincipal(token, stackName string, stackID uint32, isUser bool) (int, error) {
	switch token {
	case "root":
		return 0, nil
	case "stack":
		if uint64(stackID) > math.MaxInt || uint64(stackID) > maxIntLimit() {
			return 0, fmt.Errorf("stack id %d exceeds int range", stackID)
		}
		return int(stackID), nil
	case stackName:
		if stackID > 0 && uint64(stackID) <= math.MaxInt && uint64(stackID) <= maxIntLimit() {
			return int(stackID), nil
		}
	}

	if numeric, err := strconv.ParseUint(token, 10, 32); err == nil {
		if numeric > math.MaxInt || numeric > maxIntLimit() {
			return 0, fmt.Errorf("numeric id %d exceeds int range", numeric)
		}
		return int(numeric), nil
	}

	if isUser {
		lookup := a.LookupUser
		if lookup == nil {
			lookup = defaultLookupUser
		}
		return lookup(token)
	}
	lookup := a.LookupGroup
	if lookup == nil {
		lookup = defaultLookupGroup
	}
	return lookup(token)
}

func parseOptionalMode(raw string) (mode os.FileMode, set bool, err error) {
	if strings.TrimSpace(raw) == "" {
		return 0, false, nil
	}
	parsed, parseErr := strconv.ParseUint(raw, 8, 32)
	if parseErr != nil {
		return 0, false, fmt.Errorf("parse mode %q: %w", raw, parseErr)
	}
	if parsed > 0o7777 {
		return 0, false, fmt.Errorf("mode %q exceeds 07777", raw)
	}
	return os.FileMode(parsed), true, nil
}

func resolveWithinRoot(absRoot, relPath string) (string, error) {
	if strings.TrimSpace(relPath) == "" {
		return "", fmt.Errorf("path %q must not be empty", relPath)
	}
	if filepath.IsAbs(relPath) {
		return "", fmt.Errorf("path %q must be relative to stack_dir", relPath)
	}
	joined := filepath.Join(absRoot, relPath)
	rel, err := filepathRel(absRoot, joined)
	if err != nil {
		return "", fmt.Errorf("resolve relative path %q: %w", relPath, err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q escapes stack_dir", relPath)
	}
	return rel, nil
}
