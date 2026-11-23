package cmdexec

import (
	"context"
	"os/exec"
)

var allowedCommands = map[string]func(context.Context) *exec.Cmd{
	"docker":     cmdDocker,
	"tar":        cmdTar,
	"zstd":       cmdZstd,
	"lvcreate":   cmdLvcreate,
	"lvremove":   cmdLvremove,
	"lvs":        cmdLvs,
	"mount":      cmdMount,
	"umount":     cmdUmount,
	"mountpoint": cmdMountpoint,
	"systemctl":  cmdSystemctl,
	"rclone":     cmdRclone,
	"groupadd":   cmdGroupadd,
	"useradd":    cmdUseradd,
}

func cmdDocker(ctx context.Context) *exec.Cmd     { return exec.CommandContext(ctx, "docker") }
func cmdTar(ctx context.Context) *exec.Cmd        { return exec.CommandContext(ctx, "tar") }
func cmdZstd(ctx context.Context) *exec.Cmd       { return exec.CommandContext(ctx, "zstd") }
func cmdLvcreate(ctx context.Context) *exec.Cmd   { return exec.CommandContext(ctx, "lvcreate") }
func cmdLvremove(ctx context.Context) *exec.Cmd   { return exec.CommandContext(ctx, "lvremove") }
func cmdLvs(ctx context.Context) *exec.Cmd        { return exec.CommandContext(ctx, "lvs") }
func cmdMount(ctx context.Context) *exec.Cmd      { return exec.CommandContext(ctx, "mount") }
func cmdUmount(ctx context.Context) *exec.Cmd     { return exec.CommandContext(ctx, "umount") }
func cmdMountpoint(ctx context.Context) *exec.Cmd { return exec.CommandContext(ctx, "mountpoint") }
func cmdSystemctl(ctx context.Context) *exec.Cmd  { return exec.CommandContext(ctx, "systemctl") }
func cmdRclone(ctx context.Context) *exec.Cmd     { return exec.CommandContext(ctx, "rclone") }
func cmdGroupadd(ctx context.Context) *exec.Cmd   { return exec.CommandContext(ctx, "groupadd") }
func cmdUseradd(ctx context.Context) *exec.Cmd    { return exec.CommandContext(ctx, "useradd") }
