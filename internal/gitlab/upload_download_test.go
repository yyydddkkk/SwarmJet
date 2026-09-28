package gitlab

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDownloadUploadImageUsesPersonalTokenAndRejectsNonImages(t *testing.T) {
	const secret = "66dbcd21ec5d24ed6ea225176098d52b"
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.EscapedPath() != "/api/v4/projects/42/uploads/"+secret+"/sample.png" || r.Header.Get("PRIVATE-TOKEN") != "personal-secret" {
			t.Errorf("unexpected upload request %s", r.URL.EscapedPath())
			w.WriteHeader(http.StatusForbidden)
			return
		}
		if requests == 1 {
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write([]byte("\x89PNG\r\n\x1a\n"))
			return
		}
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte("<html>not an image</html>"))
	}))
	defer server.Close()
	client := New(server.URL, "personal-secret")
	data, kind, err := client.DownloadUploadImage(context.Background(), 42, secret, "sample.png")
	if err != nil || kind != "image/png" || len(data) != 8 {
		t.Fatalf("image download failed: kind=%q bytes=%d error=%v", kind, len(data), err)
	}
	_, _, err = client.DownloadUploadImage(context.Background(), 42, secret, "sample.png")
	if !errors.Is(err, ErrUnsupportedUploadImage) || strings.Contains(err.Error(), "personal-secret") {
		t.Fatalf("unsafe media type accepted: %v", err)
	}
}
