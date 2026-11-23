// Package sysuser inspects /etc/passwd and /etc/group to enforce exact UID/GID preservation during restore.
package sysuser

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/webstudiobond/backup-recovery-orchestrator/internal/cmdexec"
	"github.com/webstudiobond/backup-recovery-orchestrator/internal/config"
)

const (
	defaultPasswdPath = "/etc/passwd"
	defaultGroupPath  = "/etc/group"
)

// Status describes whether the target system user and group already matched or had to be created.
type Status string

// Status constants classify system account resolution states during user and group reconciliation.
const (
	StatusExisting Status = "existing"
	StatusCreated  Status = "created"
)

// Manager validates and provisions host system accounts without risking UID/GID collisions.
type Manager struct {
	Runner     cmdexec.Runner
	PasswdPath string
	GroupPath  string
}

// NewManager builds a sysuser Manager reading /etc/passwd and /etc/group and running OS groupadd/useradd.
func NewManager(runner cmdexec.Runner) *Manager {
	if runner == nil {
		runner = cmdexec.OSRunner{}
	}
	return &Manager{
		Runner:     runner,
		PasswdPath: defaultPasswdPath,
		GroupPath:  defaultGroupPath,
	}
}

// VerifyExistingUser asserts that the configured system user and group already exist on the host with exact IDs.
func (m *Manager) VerifyExistingUser(ctx context.Context, target config.UserConfig) error {
	matched, err := m.loadAndValidateAccount(ctx, target)
	if err != nil {
		return err
	}
	if !matched {
		return fmt.Errorf("system user %q (uid=%d, gid=%d) does not exist on host", target.Name, target.UID, target.GID)
	}
	return nil
}

// EnsureUser enforces exact name, UID, and GID consistency, aborting on any partial conflict.
func (m *Manager) EnsureUser(ctx context.Context, target config.UserConfig, homeDir string, dryRun bool) (Status, error) {
	matched, err := m.loadAndValidateAccount(ctx, target)
	if err != nil {
		return "", err
	}
	if matched {
		return StatusExisting, nil
	}

	if dryRun {
		return StatusCreated, nil
	}
	return m.provisionAccount(ctx, target, homeDir)
}

func (m *Manager) loadAndValidateAccount(ctx context.Context, target config.UserConfig) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, fmt.Errorf("context canceled before checking user %q: %w", target.Name, err)
	}
	if strings.TrimSpace(target.Name) == "" {
		return false, errors.New("target user name must not be empty")
	}

	users, err := parsePasswd(m.passwdPath())
	if err != nil {
		return false, err
	}
	groups, grpErr := parseGroup(m.groupPath())
	if grpErr != nil {
		return false, grpErr
	}

	return validateAccountState(target, users, groups)
}

func validateAccountState(target config.UserConfig, users []passwdEntry, groups []groupEntry) (bool, error) {
	existingUser, userExists := findUserByName(users, target.Name)
	existingGroup, groupExists := findGroupByName(groups, target.Name)

	if userExists || groupExists {
		if !userExists || !groupExists {
			return false, fmt.Errorf("partial account conflict for %q (user_exists=%t, group_exists=%t)", target.Name, userExists, groupExists)
		}
		if existingUser.uid != target.UID || existingUser.gid != target.GID || existingGroup.gid != target.GID {
			return false, fmt.Errorf(
				"uid/gid conflict for %q: found uid=%d gid=%d (group gid=%d), manifest requires uid=%d gid=%d",
				target.Name, existingUser.uid, existingUser.gid, existingGroup.gid, target.UID, target.GID,
			)
		}
		return true, nil
	}

	if uidOwner, taken := findUserByUID(users, target.UID); taken {
		return false, fmt.Errorf("uid %d required by %q is already assigned to user %q", target.UID, target.Name, uidOwner.name)
	}
	if gidOwner, taken := findGroupByGID(groups, target.GID); taken {
		return false, fmt.Errorf("gid %d required by %q is already assigned to group %q", target.GID, target.Name, gidOwner.name)
	}
	return false, nil
}

func (m *Manager) provisionAccount(ctx context.Context, target config.UserConfig, homeDir string) (Status, error) {
	gidStr := strconv.FormatUint(uint64(target.GID), 10)
	uidStr := strconv.FormatUint(uint64(target.UID), 10)

	if err := m.Runner.Run(ctx, &cmdexec.Spec{
		Name: "groupadd",
		Args: []string{"-g", gidStr, target.Name},
	}); err != nil {
		return "", fmt.Errorf("create group %q (%s): %w", target.Name, gidStr, err)
	}

	if err := m.Runner.Run(ctx, &cmdexec.Spec{
		Name: "useradd",
		Args: []string{"-u", uidStr, "-g", gidStr, "-M", "-d", homeDir, "-s", "/usr/sbin/nologin", target.Name},
	}); err != nil {
		return "", fmt.Errorf("create user %q (%s:%s): %w", target.Name, uidStr, gidStr, err)
	}

	return StatusCreated, nil
}

func (m *Manager) passwdPath() string {
	if m.PasswdPath != "" {
		return m.PasswdPath
	}
	return defaultPasswdPath
}

func (m *Manager) groupPath() string {
	if m.GroupPath != "" {
		return m.GroupPath
	}
	return defaultGroupPath
}
