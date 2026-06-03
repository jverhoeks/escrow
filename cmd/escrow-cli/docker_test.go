package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestDockerBuildArgv(t *testing.T) {
	da := deriveDockerArgs([]string{"npm"}, "host.docker.internal", 7889)
	argv := dockerBuildArgv(da, []string{"-t", "myimg", "."})

	assert.Equal(t, "build", argv[0])
	assertHasPair(t, argv, "--add-host", "host.docker.internal:host-gateway")
	assertHasPair(t, argv, "--build-arg", "NPM_CONFIG_REGISTRY=http://host.docker.internal:7888/")
	assertHasPair(t, argv, "--build-arg", "HTTP_PROXY=http://host.docker.internal:7889")
	assert.Equal(t, []string{"-t", "myimg", "."}, argv[len(argv)-3:])
}

func assertHasPair(t *testing.T, argv []string, flag, val string) {
	t.Helper()
	for i := 0; i+1 < len(argv); i++ {
		if argv[i] == flag && argv[i+1] == val {
			return
		}
	}
	t.Fatalf("expected %s %q in argv: %v", flag, val, argv)
}

func TestComposeOverride(t *testing.T) {
	da := deriveDockerArgs([]string{"npm"}, "host.docker.internal", 7889)
	out := composeOverride([]string{"web", "worker"}, da)

	assert.Contains(t, out, "services:\n")
	assert.Contains(t, out, "  web:\n")
	assert.Contains(t, out, "  worker:\n")
	assert.Contains(t, out, `HTTP_PROXY: "http://host.docker.internal:7889"`)
	assert.Contains(t, out, `NPM_CONFIG_REGISTRY: "http://host.docker.internal:7888/"`)
	assert.Contains(t, out, `- "host.docker.internal:host-gateway"`)
}

func TestDeriveDockerArgs(t *testing.T) {
	got := deriveDockerArgs([]string{"npm", "pypi", "go"}, "host.docker.internal", 7889)

	assert.Equal(t, "host.docker.internal:host-gateway", got.AddHost)
	assert.Equal(t, "http://host.docker.internal:7889", got.BuildArgs["HTTP_PROXY"])
	assert.Equal(t, "http://host.docker.internal:7889", got.BuildArgs["HTTPS_PROXY"])
	assert.Equal(t, "http://host.docker.internal:7889", got.BuildArgs["http_proxy"])
	assert.Equal(t, "host.docker.internal,localhost,127.0.0.1", got.BuildArgs["NO_PROXY"])
	assert.Equal(t, "http://host.docker.internal:7888/", got.BuildArgs["NPM_CONFIG_REGISTRY"])
	assert.Equal(t, "http://host.docker.internal:7888/pypi/simple/", got.BuildArgs["PIP_INDEX_URL"])
	assert.Contains(t, got.BuildArgs["GOPROXY"], "/go,off")
}
