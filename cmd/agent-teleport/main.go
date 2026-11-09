// Command agent-teleport moves resumable coding-agent sessions between
// machines.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/Riki1312/agent-teleport/internal/cli"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	app := cli.New()
	if err := app.Run(ctx, os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "agent-teleport:", err)
		var usageError *cli.UsageError
		if errors.As(err, &usageError) {
			os.Exit(2)
		}
		os.Exit(1)
	}
}
