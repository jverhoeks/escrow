package main

import (
	"fmt"
	"os"
)

const cliUsage = `escrow-cli — escrow proxy system configuration

Usage:
  escrow-cli setup              [--sudoers]
  escrow-cli pf-enable          [--ecosystems LIST] [--proxy-port PORT] [--proxy-user USER]
  escrow-cli pf-disable
  escrow-cli config write       [--ecosystems LIST] [--proxy-url URL]
  escrow-cli config restore
  escrow-cli status             [--json]
  escrow-cli service            <start|stop|restart|status>

Subcommands that require root: setup, pf-enable, pf-disable, service
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, cliUsage)
		os.Exit(1)
	}

	switch os.Args[1] {
	case "setup":
		runSetup(os.Args[2:])
	case "pf-enable":
		runPfEnable(os.Args[2:])
	case "pf-disable":
		runPfDisable(os.Args[2:])
	case "config":
		if len(os.Args) < 3 {
			fmt.Fprintln(os.Stderr, "error: config requires a subcommand: write, restore")
			os.Exit(1)
		}
		switch os.Args[2] {
		case "write":
			runConfigWrite(os.Args[3:])
		case "restore":
			runConfigRestore(os.Args[3:])
		default:
			fmt.Fprintf(os.Stderr, "error: unknown config subcommand: %s\n", os.Args[2])
			os.Exit(1)
		}
	case "status":
		runStatus(os.Args[2:])
	case "service":
		if len(os.Args) < 3 {
			fmt.Fprintln(os.Stderr, "error: service requires a subcommand: start, stop, restart, status")
			os.Exit(1)
		}
		runService(os.Args[2:])
	default:
		fmt.Fprintf(os.Stderr, "error: unknown subcommand: %s\n", os.Args[1])
		fmt.Fprint(os.Stderr, cliUsage)
		os.Exit(1)
	}
}
