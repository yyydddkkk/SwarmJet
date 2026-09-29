package main

import (
	"strings"
	"testing"
)

func TestBuildNetworkEnvKeepsInternalDestinationsDirect(t *testing.T) {
	original := map[string]string{"HTTP_PROXY": "http://proxy.example:8080", "NO_PROXY": "private.example"}
	got := buildNetworkEnv(original, "10.0.0.20:5443")
	for _, host := range []string{"localhost", "127.0.0.1", "::1", "registry", "api", "builder", "buildkit", "10.0.0.20", "10.0.0.20:5443", "private.example"} {
		if !strings.Contains(","+got["NO_PROXY"]+",", ","+host+",") {
			t.Errorf("missing %q from NO_PROXY", host)
		}
	}
	if got["NO_PROXY"] != got["no_proxy"] || got["HTTP_PROXY"] != original["HTTP_PROXY"] || original["NO_PROXY"] != "private.example" {
		t.Fatal("build network environment changed the source or lost proxy settings")
	}
}
