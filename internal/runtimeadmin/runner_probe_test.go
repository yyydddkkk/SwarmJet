package runtimeadmin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

func TestRunnerTestStartsRestrictedContainerAndReportsAgents(t *testing.T) {
	image := "registry.test/swarmjet/agent@sha256:" + strings.Repeat("a", 64)
	var runArgs []string
	h := New(Config{DataDir: t.TempDir(), Image: image, Secret: "private",
		DockerCommand: func(_ context.Context, args, _ []string) ([]byte, error) {
			if slices.Contains(args, "inspect") {
				return []byte("sha256:image-id"), nil
			}
			runArgs = slices.Clone(args)
			return []byte("dst\nopencode\n"), nil
		},
	})
	r := httptest.NewRequest(http.MethodPost, "/runner-test", nil)
	r.Header.Set("X-SwarmJet-Runtime-Secret", "private")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatal(w.Code, w.Body.String())
	}
	var result struct {
		OK     bool     `json:"ok"`
		Image  string   `json:"image"`
		Agents []string `json:"agents"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if !result.OK || result.Image != image || !slices.Equal(result.Agents, []string{"opencode", "dst"}) {
		t.Fatalf("unexpected result: %+v", result)
	}
	for _, arg := range []string{"--rm", "--network=none", "--read-only", "--cap-drop=ALL", "--security-opt=no-new-privileges", image} {
		if !slices.Contains(runArgs, arg) {
			t.Fatalf("missing restricted Runner argument %q: %v", arg, runArgs)
		}
	}
}
