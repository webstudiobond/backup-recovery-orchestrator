// Command bro provides the Backup & Recovery Orchestrator CLI for Docker Compose stacks.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/webstudiobond/backup-recovery-orchestrator/internal/cmdexec"
)

// AppVersion stores the binary version injected from the release tag via -ldflags.
var AppVersion = "dev"

var osExit = os.Exit

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	err := run(ctx, os.Args[1:], os.Stdout, os.Stderr, cmdexec.OSRunner{})
	stop()
	if err != nil {
		if _, wErr := fmt.Fprintf(os.Stderr, "error: %v\n", err); wErr != nil {
			osExit(1)
			return
		}
		osExit(1)
	}
}
