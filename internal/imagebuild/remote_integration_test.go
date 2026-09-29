package imagebuild

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"swarmjet/internal/registryconfig"
	"testing"
	"time"
)

// Opt in with an isolated fixture directory; this builds a deterministic stub,
// never installs or invokes a real Agent CLI or a model API.
func TestDistributionBuildKitSmoke(t *testing.T) {
	root := os.Getenv("SWARMJET_DISTRIBUTION_SMOKE")
	if root == "" {
		t.Skip("isolated BuildKit/registry fixture required")
	}
	registry := os.Getenv("SWARMJET_DISTRIBUTION_SMOKE_REGISTRY")
	if registry == "" {
		t.Fatal("SWARMJET_DISTRIBUTION_SMOKE_REGISTRY must contain the fixture registry host:port")
	}
	raw, e := os.ReadFile(filepath.Join(root, "registry/credentials.json"))
	if e != nil {
		t.Fatal(e)
	}
	var passwords map[string]string
	if e = json.Unmarshal(raw, &passwords); e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	ctx = context.WithValue(ctx, preparedKey{}, Prepared{Registry: registryconfig.Access{Address: registry, Username: "builder", Password: passwords["builder"]}})
	metadata := filepath.Join(root, "smoke-metadata.json")
	log, e := os.Create(filepath.Join(root, "smoke-build.log"))
	if e != nil {
		t.Fatal(e)
	}
	defer log.Close()
	e = RemoteRunner("unix://"+filepath.Join(root, "buildkit/buildkitd.sock"))(ctx, []string{"build", "--progress=plain", "--file", filepath.Join(root, "context/Dockerfile"), "--tag", registry + "/swarmjet/fixture:smoke", "--metadata-file", metadata, filepath.Join(root, "context")}, io.Writer(log))
	if e != nil {
		t.Fatalf("%v; inspect private smoke-build.log", e)
	}
	ref, e := readPublished(metadata, registry+"/swarmjet/fixture")
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(filepath.Join(root, "fixture-reference"), []byte(ref), 0600); e != nil {
		t.Fatal(e)
	}
	t.Log(ref)
}
