package main

import (
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"sort"
	"time"
)

// dockerProxyArgs is the escrow wiring injected into a docker build.
type dockerProxyArgs struct {
	AddHost   string            // host.docker.internal:host-gateway
	BuildArgs map[string]string // proxy env + registry env
}

// deriveDockerArgs assembles the proxy/registry build-args for the given
// ecosystems. proxyHost is the build-reachable address of escrow (default
// host.docker.internal); egressPort is escrow's egress-proxy port.
func deriveDockerArgs(ecosystems []string, proxyHost string, egressPort int) dockerProxyArgs {
	mirrorBase := fmt.Sprintf("http://%s:7888", proxyHost)
	proxyURL := fmt.Sprintf("http://%s:%d", proxyHost, egressPort)
	noProxy := proxyHost + ",localhost,127.0.0.1"

	ba := map[string]string{
		"HTTP_PROXY":  proxyURL,
		"HTTPS_PROXY": proxyURL,
		"http_proxy":  proxyURL,
		"https_proxy": proxyURL,
		"NO_PROXY":    noProxy,
		"no_proxy":    noProxy,
	}
	for _, e := range buildEnvVars(ecosystems, mirrorBase) {
		ba[e.key] = e.value
	}
	return dockerProxyArgs{AddHost: proxyHost + ":host-gateway", BuildArgs: ba}
}

func sortedKeys(m map[string]string) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

// parseDockerFlags extracts escrow flags and returns the remaining (user) args.
func parseDockerFlags(args []string) (ecosystems []string, proxyHost string, egressPort int, rest []string) {
	fs := flag.NewFlagSet("docker", flag.ExitOnError)
	ecoStr := fs.String("ecosystems", "npm,pypi,go", "comma-separated ecosystems")
	host := fs.String("proxy-host", "host.docker.internal", "build-reachable escrow host")
	port := fs.Int("egress-port", 7889, "escrow egress-proxy port")
	_ = fs.Parse(args)
	return parseEcosystems(*ecoStr), *host, *port, fs.Args()
}

func runDocker(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: escrow-cli docker <check|build|compose> ...")
		os.Exit(2)
	}
	switch args[0] {
	case "check":
		runDockerCheck(args[1:])
	case "build":
		runDockerBuild(args[1:])
	case "compose":
		runDockerCompose(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "unknown docker subcommand %q\n", args[0])
		os.Exit(2)
	}
}

func runDockerCheck(args []string) {
	ecos, proxyHost, egressPort, _ := parseDockerFlags(args)
	da := deriveDockerArgs(ecos, proxyHost, egressPort)
	fmt.Printf("--add-host %s\n", da.AddHost)
	for _, k := range sortedKeys(da.BuildArgs) {
		fmt.Printf("--build-arg %s=%s\n", k, da.BuildArgs[k])
	}
	client := &http.Client{Timeout: 2 * time.Second}
	if resp, err := client.Get("http://127.0.0.1:7888/healthz"); err == nil {
		_ = resp.Body.Close()
		fmt.Println("escrow mirror: reachable on 127.0.0.1:7888")
	} else {
		fmt.Println("escrow mirror: NOT reachable on 127.0.0.1:7888 — start escrow first")
	}
}

// dockerBuildArgv assembles the full `docker build ...` argument vector
// (escrow wiring first, then the user's args). Build-args are emitted in a
// stable (sorted) order for determinism.
func dockerBuildArgv(da dockerProxyArgs, userArgs []string) []string {
	argv := []string{"build", "--add-host", da.AddHost}
	for _, k := range sortedKeys(da.BuildArgs) {
		argv = append(argv, "--build-arg", k+"="+da.BuildArgs[k])
	}
	return append(argv, userArgs...)
}

func runDockerBuild(args []string) {
	ecos, proxyHost, egressPort, userArgs := parseDockerFlags(args)
	da := deriveDockerArgs(ecos, proxyHost, egressPort)
	argv := dockerBuildArgv(da, userArgs)

	cmd := exec.Command("docker", argv...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	if err := cmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "docker build failed: %v\n", err)
		os.Exit(1)
	}
}

func runDockerCompose(args []string) {
	// implemented in Task 7
	_ = args
	fmt.Fprintln(os.Stderr, "docker compose: not yet implemented")
	os.Exit(2)
}
