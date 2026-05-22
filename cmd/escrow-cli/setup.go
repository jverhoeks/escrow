package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

const (
	pfConf       = "/etc/pf.conf"
	pfAnchorFile = "/etc/pf.anchors/escrow-npm"
	sudoersPath  = "/etc/sudoers.d/escrow"
)

var escrowPfLines = []string{
	`rdr-anchor "escrow"`,
	`anchor "escrow"`,
	`load anchor "escrow" from "/etc/pf.anchors/escrow-npm"`,
}

func runSetup(args []string) {
	fs := flag.NewFlagSet("setup", flag.ExitOnError)
	withSudoers := fs.Bool("sudoers", false, "install sudoers file at /etc/sudoers.d/escrow")
	fs.Parse(args) //nolint:errcheck

	requireRoot("setup")

	var done, already []string

	// 1. Create _escrow system account.
	if exec.Command("dscl", ".", "-read", "/Users/_escrow").Run() != nil {
		out, err := exec.Command("/usr/bin/sysadminctl",
			"-addUser", "_escrow",
			"-fullName", "Escrow Proxy",
			"-roleAccount",
		).CombinedOutput()
		if err != nil {
			die("creating _escrow account: %v\n%s", err, strings.TrimSpace(string(out)))
		}
		done = append(done, "created system account _escrow")
	} else {
		already = append(already, "_escrow account already exists")
	}

	// 2. Patch /etc/pf.conf and reload.
	patched, err := patchPfConf()
	if err != nil {
		die("patching %s: %v", pfConf, err)
	}
	if patched {
		out, err := exec.Command("pfctl", "-f", pfConf).CombinedOutput()
		if err != nil {
			die("reloading pf.conf: %v\n%s", err, strings.TrimSpace(string(out)))
		}
		done = append(done, "updated "+pfConf+" and reloaded pf")
	} else {
		already = append(already, pfConf+" already contains escrow anchors")
	}

	// 3. Create empty anchor file.
	if _, err := os.Stat(pfAnchorFile); os.IsNotExist(err) {
		if err := os.MkdirAll("/etc/pf.anchors", 0755); err != nil {
			die("creating /etc/pf.anchors: %v", err)
		}
		comment := "# Escrow pf anchor — managed by escrow-cli\n"
		if err := writeAtomic(pfAnchorFile, []byte(comment), 0644); err != nil {
			die("creating %s: %v", pfAnchorFile, err)
		}
		done = append(done, "created "+pfAnchorFile)
	} else {
		already = append(already, pfAnchorFile+" already exists")
	}

	// 4. Install sudoers file (opt-in via --sudoers).
	if *withSudoers {
		if err := installSudoers(); err != nil {
			die("installing sudoers: %v", err)
		}
		done = append(done, "installed sudoers file at "+sudoersPath)
	}

	// 5. Summary.
	for _, s := range done {
		fmt.Println("✓", s)
	}
	for _, s := range already {
		fmt.Println("–", s)
	}
}

// patchPfConf ensures the three escrow anchor lines are present in /etc/pf.conf.
// It removes any stale partial escrow lines first, then inserts all three
// after the com.apple rdr-anchor line (or at the end if not found).
// Returns true if the file was modified.
func patchPfConf() (bool, error) {
	data, err := os.ReadFile(pfConf)
	if err != nil {
		return false, err
	}
	lines := strings.Split(string(data), "\n")

	// Count how many escrow lines are already present.
	escrowSet := make(map[string]bool, len(escrowPfLines))
	for _, l := range escrowPfLines {
		escrowSet[l] = true
	}
	present := 0
	for _, line := range lines {
		if escrowSet[strings.TrimSpace(line)] {
			present++
		}
	}
	if present == len(escrowPfLines) {
		return false, nil // already fully configured
	}

	// Remove stale partial escrow lines.
	filtered := lines[:0:len(lines)]
	for _, line := range lines {
		if !escrowSet[strings.TrimSpace(line)] {
			filtered = append(filtered, line)
		}
	}

	// Find insertion point: after `rdr-anchor "com.apple/*"`.
	insertIdx := len(filtered)
	for i, line := range filtered {
		if strings.Contains(line, `rdr-anchor "com.apple`) {
			insertIdx = i + 1
			break
		}
	}

	out := make([]string, 0, len(filtered)+len(escrowPfLines))
	out = append(out, filtered[:insertIdx]...)
	out = append(out, escrowPfLines...)
	out = append(out, filtered[insertIdx:]...)

	return true, writeAtomic(pfConf, []byte(strings.Join(out, "\n")), 0644)
}

// installSudoers writes a sudoers snippet granting the admin group passwordless
// access to the privileged escrow-cli subcommands. Validated by visudo before install.
func installSudoers() error {
	exe, err := os.Executable()
	if err != nil {
		exe = "/usr/local/bin/escrow-cli"
	}
	content := fmt.Sprintf(
		"# Escrow proxy — passwordless sudo for admin group\n"+
			"%%admin ALL=(root) NOPASSWD: %s setup\n"+
			"%%admin ALL=(root) NOPASSWD: %s pf-enable *\n"+
			"%%admin ALL=(root) NOPASSWD: %s pf-disable\n"+
			"%%admin ALL=(root) NOPASSWD: %s service *\n",
		exe, exe, exe, exe,
	)

	tmp, err := os.CreateTemp("", "escrow-sudoers-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())

	if _, err := tmp.WriteString(content); err != nil {
		tmp.Close()
		return err
	}
	tmp.Close()

	if out, err := exec.Command("visudo", "-c", "-f", tmp.Name()).CombinedOutput(); err != nil {
		return fmt.Errorf("visudo validation failed: %v\n%s", err, strings.TrimSpace(string(out)))
	}

	return writeAtomic(sudoersPath, []byte(content), 0440)
}
