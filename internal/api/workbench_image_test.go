package api

import "testing"

func TestWorkbenchImageFilenameIsOneSafePathSegment(t *testing.T) {
	for _, name := range []string{"sample.png", "截图 2026.png", "chart-v2.webp"} {
		if !validUploadFilename(name) {
			t.Fatalf("valid upload name rejected: %q", name)
		}
	}
	for _, name := range []string{"", ".", "..", "../secret", "nested/image.png", `nested\image.png`, "image\n.png"} {
		if validUploadFilename(name) {
			t.Fatalf("unsafe upload name accepted: %q", name)
		}
	}
}
