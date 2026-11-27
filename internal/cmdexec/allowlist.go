package cmdexec

import (
	"context"
	"os/exec"
)

// Allowlisted external executable names permitted for host command execution.
const (
	BinDocker     = "docker"
	BinTar        = "tar"
	BinZstd       = "zstd"
	BinLVCreate   = "lvcreate"
	BinLVRemove   = "lvremove"
	BinLVS        = "lvs"
	BinMount      = "mount"
	BinUmount     = "umount"
	BinMountpoint = "mountpoint"
	BinSystemctl  = "systemctl"
	BinRclone     = "rclone"
	BinGroupadd   = "groupadd"
	BinUseradd    = "useradd"
)

var allowedCommands = map[string]func(context.Context) *exec.Cmd{
	BinDocker:     cmdDocker,
	BinTar:        cmdTar,
	BinZstd:       cmdZstd,
	BinLVCreate:   cmdLvcreate,
	BinLVRemove:   cmdLvremove,
	BinLVS:        cmdLvs,
	BinMount:      cmdMount,
	BinUmount:     cmdUmount,
	BinMountpoint: cmdMountpoint,
	BinSystemctl:  cmdSystemctl,
	BinRclone:     cmdRclone,
	BinGroupadd:   cmdGroupadd,
	BinUseradd:    cmdUseradd,
}

func cmdDocker(ctx context.Context) *exec.Cmd     { return exec.CommandContext(ctx, BinDocker) }
func cmdTar(ctx context.Context) *exec.Cmd        { return exec.CommandContext(ctx, BinTar) }
func cmdZstd(ctx context.Context) *exec.Cmd       { return exec.CommandContext(ctx, BinZstd) }
func cmdLvcreate(ctx context.Context) *exec.Cmd   { return exec.CommandContext(ctx, BinLVCreate) }
func cmdLvremove(ctx context.Context) *exec.Cmd   { return exec.CommandContext(ctx, BinLVRemove) }
func cmdLvs(ctx context.Context) *exec.Cmd        { return exec.CommandContext(ctx, BinLVS) }
func cmdMount(ctx context.Context) *exec.Cmd      { return exec.CommandContext(ctx, BinMount) }
func cmdUmount(ctx context.Context) *exec.Cmd     { return exec.CommandContext(ctx, BinUmount) }
func cmdMountpoint(ctx context.Context) *exec.Cmd { return exec.CommandContext(ctx, BinMountpoint) }
func cmdSystemctl(ctx context.Context) *exec.Cmd  { return exec.CommandContext(ctx, BinSystemctl) }
func cmdRclone(ctx context.Context) *exec.Cmd     { return exec.CommandContext(ctx, BinRclone) }
func cmdGroupadd(ctx context.Context) *exec.Cmd   { return exec.CommandContext(ctx, BinGroupadd) }
func cmdUseradd(ctx context.Context) *exec.Cmd    { return exec.CommandContext(ctx, BinUseradd) }
