package imagebuild

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPublishedDigestIsRegistryReferenceNotLocalID(t *testing.T) {
	p := filepath.Join(t.TempDir(), "meta.json")
	for _, digest := range []string{"sha256:" + strings.Repeat("a", 64), "sha256:bad", "tag"} {
		if e := os.WriteFile(p, []byte(`{"containerimage.digest":"`+digest+`"}`), 0600); e != nil {
			t.Fatal(e)
		}
		ref, e := readPublished(p, "registry.test/agent")
		if strings.HasSuffix(digest, strings.Repeat("a", 64)) {
			if e != nil || ref != "registry.test/agent@"+digest {
				t.Fatal(ref, e)
			}
		} else if e == nil {
			t.Fatal("accepted invalid digest")
		}
	}
}

func TestBuildLogSecretsAcrossWrites(t *testing.T) {
	var out strings.Builder
	w := newSecretWriter(&out, Prepared{Env: map[string]string{"HTTP_PROXY": "http://user:very-secret@proxy:8080"}})
	for _, part := range []string{"before very-", "secret and http://user:very-", "secret@proxy:8080 after"} {
		_, _ = w.Write([]byte(part))
	}
	if e := w.Close(); e != nil {
		t.Fatal(e)
	}
	if strings.Contains(out.String(), "very-secret") || out.String() != "before [REDACTED] and [REDACTED] after" {
		t.Fatal(out.String())
	}
}
