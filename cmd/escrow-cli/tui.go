package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/jverhoeks/escrow/cmd/escrow-cli/tui"
)

func runTUI(args []string) {
	fs := flag.NewFlagSet("tui", flag.ExitOnError)
	url := fs.String("url", "", "dashboard base URL (default: discover the local proxy)")
	user := fs.String("user", "", "dashboard username (default: from escrow.toml)")
	pass := fs.String("password", "", "dashboard password (default: from escrow.toml)")
	path := fs.String("path", "", "event-log path for offline mode (default: discover)")
	fs.Parse(args) //nolint:errcheck
	if err := tui.Run(tui.Options{URL: *url, User: *user, Password: *pass, Path: *path}); err != nil {
		fmt.Fprintln(os.Stderr, "escrow-cli tui:", err)
		os.Exit(1)
	}
}
