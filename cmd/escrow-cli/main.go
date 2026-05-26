package main

import (
	"fmt"
	"os"
)

const cliUsage = `escrow-cli — escrow proxy system configuration

Usage:
  escrow-cli setup                   [--sudoers] [--dry-run]
  escrow-cli fw-enable               [--ecosystems LIST] [--proxy-port PORT] [--proxy-user USER]
  escrow-cli fw-disable
  escrow-cli fw-test                 [--ecosystems LIST]
  escrow-cli config write            [--ecosystems LIST] [--proxy-url URL] [--git]
  escrow-cli config write-local      [--ecosystems LIST] [--proxy-url URL]
  escrow-cli config write-env        [--ecosystems LIST] [--proxy-url URL]
  escrow-cli config write-shell      [--ecosystems LIST] [--proxy-url URL] [--profiles LIST]
  escrow-cli config check            [--ecosystems LIST]
  escrow-cli config check-local      [--ecosystems LIST]
  escrow-cli config check-env
  escrow-cli config check-shell
  escrow-cli config restore          [--ecosystems LIST]
  escrow-cli config restore-local    [--ecosystems LIST]
  escrow-cli config restore-env
  escrow-cli config restore-shell    [--profiles LIST]
  escrow-cli status                  [--json]
  escrow-cli service                 <start|stop|restart|status>

Aliases (backward-compatible):
  pf-enable  →  fw-enable
  pf-disable →  fw-disable

Subcommands that require root: setup, fw-enable, fw-disable, service
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, cliUsage)
		os.Exit(1)
	}

	switch os.Args[1] {
	case "setup":
		runSetup(os.Args[2:])
	case "fw-enable", "pf-enable":
		runFwEnable(os.Args[2:])
	case "fw-disable", "pf-disable":
		runFwDisable(os.Args[2:])
	case "fw-test":
		runFwTest(os.Args[2:])
	case "config":
		if len(os.Args) < 3 {
			fmt.Fprintln(os.Stderr, "error: config requires a subcommand: write, write-local, restore, restore-local")
			os.Exit(1)
		}
		switch os.Args[2] {
		case "write":
			runConfigWrite(os.Args[3:])
		case "write-local":
			runConfigWriteLocal(os.Args[3:])
		case "write-env":
			runConfigWriteEnv(os.Args[3:])
		case "write-shell":
			runConfigWriteShell(os.Args[3:])
		case "check":
			runConfigCheck(os.Args[3:])
		case "check-local":
			runConfigCheckLocal(os.Args[3:])
		case "check-env":
			runConfigCheckEnv(os.Args[3:])
		case "check-shell":
			runConfigCheckShell(os.Args[3:])
		case "restore":
			runConfigRestore(os.Args[3:])
		case "restore-local":
			runConfigRestoreLocal(os.Args[3:])
		case "restore-env":
			runConfigRestoreEnv(os.Args[3:])
		case "restore-shell":
			runConfigRestoreShell(os.Args[3:])
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
