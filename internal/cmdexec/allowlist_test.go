package cmdexec

import (
	"context"
	"os/exec"
	"testing"
)

func TestAllowedCommands(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	cases := []struct {
		builder func(context.Context) *exec.Cmd
		name    string
	}{
		{name: "docker", builder: cmdDocker},
		{name: "zstd", builder: cmdZstd},
		{name: "lvcreate", builder: cmdLvcreate},
		{name: "lvremove", builder: cmdLvremove},
		{name: "lvs", builder: cmdLvs},
		{name: "mount", builder: cmdMount},
		{name: "umount", builder: cmdUmount},
		{name: "mountpoint", builder: cmdMountpoint},
		{name: "systemctl", builder: cmdSystemctl},
		{name: "rclone", builder: cmdRclone},
		{name: "groupadd", builder: cmdGroupadd},
		{name: "useradd", builder: cmdUseradd},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			registered, exists := allowedCommands[tc.name]
			if !exists {
				t.Fatalf("command %q not found in allowedCommands map", tc.name)
			}

			cmdFromMap := registered(ctx)
			if cmdFromMap == nil || len(cmdFromMap.Args) == 0 || cmdFromMap.Args[0] != tc.name {
				t.Fatalf("expected command from map to have arg %q, got %v", tc.name, cmdFromMap)
			}

			cmdFromBuilder := tc.builder(ctx)
			if cmdFromBuilder == nil || len(cmdFromBuilder.Args) == 0 || cmdFromBuilder.Args[0] != tc.name {
				t.Fatalf("expected direct builder command to have arg %q, got %v", tc.name, cmdFromBuilder)
			}
		})
	}
}
