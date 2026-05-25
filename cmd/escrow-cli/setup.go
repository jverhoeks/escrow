package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
)

const (
	pfConf       = "/etc/pf.conf"
	pfAnchorFile = "/etc/pf.anchors/escrow-npm"
	sudoersPath  = "/etc/sudoers.d/escrow"
)

// installBinDir is stamped at build time:
//
//	go build -ldflags "-X main.installBinDir=/opt/homebrew/bin" ./cmd/escrow-cli
//
// The Homebrew formula sets this to #{HOMEBREW_PREFIX}/bin so the sudoers
// file always references the correct installed path regardless of architecture.
var installBinDir = "/usr/local/bin"

var escrowPfLines = []string{
	`rdr-anchor "escrow"`,
	`anchor "escrow"`,
	`load anchor "escrow" from "/etc/pf.anchors/escrow-npm"`,
}

func runSetup(args []string) {
	fs := flag.NewFlagSet("setup", flag.ExitOnError)
	withSudoers := fs.Bool("sudoers", false, "install sudoers file at /etc/sudoers.d/escrow")
	dryRun := fs.Bool("dry-run", false, "print what would be done without making any changes")
	fs.Parse(args) //nolint:errcheck

	if !*dryRun {
		requireRoot("setup")
	}

	if *dryRun {
		fmt.Println("(dry-run — no changes will be made)")
	}

	var would, done, already []string

	// 1. _escrow system account.
	if userExists() {
		already = append(already, "_escrow account already exists")
	} else if *dryRun {
		would = append(would, "create system account _escrow ("+createUserCmd()+")")
	} else {
		if _, err := createSystemUser(); err != nil {
			die("creating _escrow account: %v", err)
		}
		done = append(done, "created system account _escrow")
	}

	if runtime.GOOS == "darwin" {
		// 2. /etc/pf.conf
		if pfConfNeedsUpdate() {
			if *dryRun {
				would = append(would, "patch "+pfConf+" with escrow rdr-anchor lines and reload pf")
			} else {
				patched, err := patchPfConf()
				if err != nil {
					die("patching %s: %v", pfConf, err)
				}
				if patched {
					// Clear the anchor file before reloading pf.conf.
					// setup only wires the anchor hook; fw-enable writes the rules.
					// A stale anchor (e.g. from a prior fw-enable) would fail here
					// because pf validates "user _escrow" at load time even if the
					// account was just created.
					const emptyAnchor = "# Escrow pf anchor — managed by escrow-cli\n"
					writeAtomic(pfAnchorFile, []byte(emptyAnchor), 0644) //nolint:errcheck
					out, err := exec.Command("pfctl", "-f", pfConf).CombinedOutput()
					if err != nil {
						die("reloading pf.conf: %v\n%s", err, strings.TrimSpace(string(out)))
					}
					done = append(done, "updated "+pfConf+" and reloaded pf (run fw-enable to restore rules)")
				}
			}
		} else {
			already = append(already, pfConf+" already contains escrow anchors")
		}

		// 3. Anchor file.
		if _, err := os.Stat(pfAnchorFile); os.IsNotExist(err) {
			if *dryRun {
				would = append(would, "create "+pfAnchorFile)
			} else {
				if err := os.MkdirAll("/etc/pf.anchors", 0755); err != nil {
					die("creating /etc/pf.anchors: %v", err)
				}
				comment := "# Escrow pf anchor — managed by escrow-cli\n"
				if err := writeAtomic(pfAnchorFile, []byte(comment), 0644); err != nil {
					die("creating %s: %v", pfAnchorFile, err)
				}
				done = append(done, "created "+pfAnchorFile)
			}
		} else {
			already = append(already, pfAnchorFile+" already exists")
		}
	}

	if runtime.GOOS == "linux" {
		if *dryRun {
			would = append(would, "create ESCROW iptables/nftables chain (via "+detectLinuxFw()+")")
		} else {
			ok, err := setupLinuxFwChain()
			if err != nil {
				fmt.Fprintf(os.Stderr, "warning: firewall chain setup: %v\n", err)
			} else if ok {
				done = append(done, "created ESCROW iptables/nftables chain")
			}
		}
	}

	// 4. Sudoers (opt-in).
	if *withSudoers {
		if _, err := os.Stat(sudoersPath); err == nil {
			already = append(already, sudoersPath+" already exists")
		} else if *dryRun {
			would = append(would, "install sudoers file at "+sudoersPath)
		} else {
			if err := installSudoers(); err != nil {
				die("installing sudoers: %v", err)
			}
			done = append(done, "installed sudoers file at "+sudoersPath)
		}
	}

	for _, s := range would {
		fmt.Println("~", s)
	}
	for _, s := range done {
		fmt.Println("✓", s)
	}
	for _, s := range already {
		fmt.Println("–", s)
	}
}

// userExists reports whether the _escrow system account already exists.
func userExists() bool {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("dscl", ".", "-read", "/Users/_escrow").Run() == nil
	default:
		return exec.Command("id", "-u", "_escrow").Run() == nil
	}
}

// createUserCmd returns the command string that would create _escrow, for dry-run display.
func createUserCmd() string {
	switch runtime.GOOS {
	case "darwin":
		return "/usr/sbin/sysadminctl -addUser _escrow -fullName \"Escrow Proxy\" -roleAccount"
	default:
		return "useradd --system --no-create-home --home-dir /nonexistent --shell /usr/sbin/nologin _escrow"
	}
}

// pfConfNeedsUpdate reports whether /etc/pf.conf is missing any of the escrow anchor lines.
func pfConfNeedsUpdate() bool {
	data, err := os.ReadFile(pfConf)
	if err != nil {
		return true
	}
	for _, line := range escrowPfLines {
		found := false
		for _, l := range strings.Split(string(data), "\n") {
			if strings.TrimSpace(l) == line {
				found = true
				break
			}
		}
		if !found {
			return true
		}
	}
	return false
}

// patchPfConf ensures the three escrow anchor lines are present in /etc/pf.conf.
// An exclusive advisory lock prevents concurrent modifications. Stale partial
// escrow lines are removed, then all three are re-inserted at the correct
// position (after the last existing rdr-anchor line, or before filter rules).
// Returns true if the file was modified.
func patchPfConf() (bool, error) {
	f, err := os.OpenFile(pfConf, os.O_RDONLY, 0)
	if err != nil {
		return false, err
	}
	defer f.Close()
	// Advisory lock: prevents two concurrent setup calls from corrupting pf.conf.
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return false, fmt.Errorf("locking %s: %v", pfConf, err)
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN) //nolint:errcheck

	data, err := io.ReadAll(f)
	if err != nil {
		return false, err
	}
	lines := strings.Split(string(data), "\n")

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
		return false, nil
	}

	// Remove any stale partial escrow lines.
	filtered := make([]string, 0, len(lines))
	for _, line := range lines {
		if !escrowSet[strings.TrimSpace(line)] {
			filtered = append(filtered, line)
		}
	}

	// Find insertion point: after the last rdr-anchor line (any, not just com.apple).
	// This ensures our rdr-anchor sits in the correct pf.conf section.
	// Fall back to end-of-file only if no rdr-anchor line exists at all, and
	// warn the user that manual placement may be needed.
	insertIdx := -1
	for i, line := range filtered {
		if strings.Contains(line, "rdr-anchor") {
			insertIdx = i + 1
		}
	}
	if insertIdx < 0 {
		fmt.Fprintf(os.Stderr,
			"warning: no rdr-anchor line found in %s; appending escrow anchors at end.\n"+
				"  Verify placement manually if pf redirects do not work.\n", pfConf)
		insertIdx = len(filtered)
	}

	out := make([]string, 0, len(filtered)+len(escrowPfLines))
	out = append(out, filtered[:insertIdx]...)
	out = append(out, escrowPfLines...)
	out = append(out, filtered[insertIdx:]...)

	return true, writeAtomic(pfConf, []byte(strings.Join(out, "\n")), 0644)
}

// installSudoers writes a sudoers snippet that grants admin group members
// passwordless sudo for the privileged escrow-cli subcommands.
// The binary path is taken from the installBinDir build-time variable so it
// reflects the actual Homebrew prefix (Intel vs Apple Silicon).
// The temp file is placed inside /etc/sudoers.d/ (mode 0750, root-owned) to
// prevent unprivileged processes from reading it during the visudo check.
func installSudoers() error {
	bin := filepath.Join(installBinDir, "escrow-cli")
	content := "# Escrow proxy — passwordless sudo for admin group\n" +
		fmt.Sprintf("%%admin ALL=(root) NOPASSWD: %s setup\n", bin) +
		fmt.Sprintf("%%admin ALL=(root) NOPASSWD: %s fw-enable *\n", bin) +
		fmt.Sprintf("%%admin ALL=(root) NOPASSWD: %s fw-disable\n", bin) +
		fmt.Sprintf("%%admin ALL=(root) NOPASSWD: %s service start\n", bin) +
		fmt.Sprintf("%%admin ALL=(root) NOPASSWD: %s service stop\n", bin) +
		fmt.Sprintf("%%admin ALL=(root) NOPASSWD: %s service restart\n", bin) +
		fmt.Sprintf("%%admin ALL=(root) NOPASSWD: %s service status\n", bin)

	// Write temp file inside /etc/sudoers.d/ (root:wheel 0750) so no
	// unprivileged process can read the content before visudo validates it.
	tmp, err := os.CreateTemp(filepath.Dir(sudoersPath), ".escrow-sudoers-tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())

	if err := tmp.Chmod(0440); err != nil {
		tmp.Close()
		return err
	}
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

// createSystemUser creates the _escrow system account if it does not exist.
// Returns (true, nil) if created, (false, nil) if already present.
func createSystemUser() (bool, error) {
	switch runtime.GOOS {
	case "darwin":
		if exec.Command("dscl", ".", "-read", "/Users/_escrow").Run() == nil {
			return false, nil
		}
		// Create the user directly in the local directory node via dscl.
		// sysadminctl -roleAccount writes to Open Directory, which is NOT visible
		// to id(1), pf's user keyword, or /etc/passwd lookups.  dscl "." targets
		// /Local/Default — the same node queried by every standard POSIX lookup.
		uid, err := nextSystemUID()
		if err != nil {
			return false, fmt.Errorf("finding available UID: %v", err)
		}
		cmds := [][]string{
			{"dscl", ".", "-create", "/Users/_escrow"},
			{"dscl", ".", "-create", "/Users/_escrow", "RealName", "Escrow Proxy"},
			{"dscl", ".", "-create", "/Users/_escrow", "UserShell", "/usr/bin/false"},
			{"dscl", ".", "-create", "/Users/_escrow", "NFSHomeDirectory", "/var/empty"},
			{"dscl", ".", "-create", "/Users/_escrow", "UniqueID", uid},
			{"dscl", ".", "-create", "/Users/_escrow", "PrimaryGroupID", "99"},
		}
		for _, args := range cmds {
			if out, err := exec.Command(args[0], args[1:]...).CombinedOutput(); err != nil {
				return false, fmt.Errorf("dscl %v: %v\n%s", args[2:], err, strings.TrimSpace(string(out)))
			}
		}
		return true, nil

	case "linux":
		if exec.Command("id", "-u", "_escrow").Run() == nil {
			return false, nil
		}
		out, err := exec.Command("useradd",
			"--system",
			"--no-create-home",
			"--home-dir", "/nonexistent",
			"--shell", "/usr/sbin/nologin",
			"_escrow",
		).CombinedOutput()
		if err != nil {
			return false, fmt.Errorf("%v\n%s", err, strings.TrimSpace(string(out)))
		}
		return true, nil

	default:
		return false, fmt.Errorf("user creation not supported on %s", runtime.GOOS)
	}
}

// nextSystemUID scans the local dscl directory for the highest free UID in
// the macOS system-user range (200–499) and returns it as a decimal string.
func nextSystemUID() (string, error) {
	out, err := exec.Command("dscl", ".", "-list", "/Users", "UniqueID").Output()
	if err != nil {
		return "499", nil // safe default if dscl fails
	}
	used := make(map[int]bool)
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 {
			if n, err := strconv.Atoi(fields[1]); err == nil {
				used[n] = true
			}
		}
	}
	for uid := 499; uid >= 200; uid-- {
		if !used[uid] {
			return strconv.Itoa(uid), nil
		}
	}
	return "", fmt.Errorf("no free UID in range 200–499")
}

// setupLinuxFwChain creates the empty ESCROW iptables chain (if iptables is in use)
// so that fw-enable can populate it later. No-op for nftables.
func setupLinuxFwChain() (bool, error) {
	switch detectLinuxFw() {
	case "iptables":
		exec.Command("iptables", "-t", "nat", "-N", "ESCROW").Run()  //nolint:errcheck
		// ESCROW6 is in the filter table (blocks IPv6, does not redirect).
		exec.Command("ip6tables", "-N", "ESCROW6").Run()              //nolint:errcheck
		if exec.Command("iptables", "-t", "nat", "-C", "OUTPUT", "-j", "ESCROW").Run() != nil {
			exec.Command("iptables", "-t", "nat", "-A", "OUTPUT", "-j", "ESCROW").Run() //nolint:errcheck
		}
		if exec.Command("ip6tables", "-C", "OUTPUT", "-j", "ESCROW6").Run() != nil {
			exec.Command("ip6tables", "-A", "OUTPUT", "-j", "ESCROW6").Run() //nolint:errcheck
		}
		return true, nil
	case "nftables":
		return true, nil // fw-enable writes the full table on demand
	default:
		return false, fmt.Errorf("neither iptables nor nft found")
	}
}
