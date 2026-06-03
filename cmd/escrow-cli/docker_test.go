package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

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
