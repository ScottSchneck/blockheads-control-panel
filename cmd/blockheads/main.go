// Command blockheads is the Blockheads Control Panel.
//
// For now it runs the console server list (built-in DNS plus the menu that
// lets Xbox, PlayStation and Switch players join your Bedrock servers). The
// web panel, server manager and the rest of phase 1 are added to this same
// program as they are built.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/ScottSchneck/blockheads-control-panel/internal/serverlist"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	showVersion := flag.Bool("version", false, "print the version and exit")
	flag.Parse()
	if *showVersion {
		fmt.Println("blockheads", version)
		return
	}

	cfg, err := serverlist.LoadConfig()
	if err != nil {
		fmt.Fprintln(os.Stderr, "setup problem:", err)
		os.Exit(2)
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel})))
	slog.Info("Blockheads Control Panel", "version", version)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := serverlist.Run(ctx, cfg); err != nil {
		slog.Error("stopped", "error", err)
		os.Exit(1)
	}
}
