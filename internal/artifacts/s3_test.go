package artifacts

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
)

type fakeS3 struct {
	mu      sync.Mutex
	objects map[string][]byte
	puts    int
	lost    bool
}

func (f *fakeS3) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 ") {
		http.Error(w, "missing signature", http.StatusForbidden)
		return
	}
	key := strings.TrimPrefix(r.URL.Path, "/bucket/")
	if r.URL.Query().Get("list-type") == "2" {
		f.mu.Lock()
		var keys []string
		for k := range f.objects {
			if strings.HasPrefix(k, r.URL.Query().Get("prefix")) {
				keys = append(keys, k)
			}
		}
		f.mu.Unlock()
		sort.Strings(keys)
		type object struct {
			Key string `xml:"Key"`
		}
		type listing struct {
			XMLName     xml.Name `xml:"ListBucketResult"`
			IsTruncated bool     `xml:"IsTruncated"`
			Contents    []object `xml:"Contents"`
		}
		var out listing
		for _, k := range keys {
			out.Contents = append(out.Contents, object{Key: k})
		}
		w.Header().Set("Content-Type", "application/xml")
		_ = xml.NewEncoder(w).Encode(out)
		return
	}
	if key == r.URL.Path {
		http.NotFound(w, r)
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	switch r.Method {
	case http.MethodPut:
		if r.Header.Get("If-None-Match") != "*" {
			http.Error(w, "missing condition", http.StatusBadRequest)
			return
		}
		b, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "read", 500)
			return
		}
		sum := sha256.Sum256(b)
		if r.Header.Get("X-Amz-Checksum-Sha256") != base64.StdEncoding.EncodeToString(sum[:]) {
			http.Error(w, "bad digest", 400)
			return
		}
		if _, ok := f.objects[key]; ok {
			http.Error(w, "exists", http.StatusPreconditionFailed)
			return
		}
		f.objects[key] = b
		f.puts++
		if f.lost {
			f.lost = false
			if h, ok := w.(http.Hijacker); ok {
				conn, _, _ := h.Hijack()
				_ = conn.Close()
				return
			}
		}
		w.Header().Set("ETag", `"fixed"`)
	case http.MethodGet:
		b, ok := f.objects[key]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(b)
	case http.MethodDelete:
		delete(f.objects, key)
	default:
		http.Error(w, fmt.Sprintf("method %s", r.Method), http.StatusMethodNotAllowed)
	}
}

func TestS3StoreConditionalWriteAndRetention(t *testing.T) {
	f := &fakeS3{objects: map[string][]byte{}, lost: true}
	server := httptest.NewServer(f)
	defer server.Close()
	s, err := NewS3Store(context.Background(), S3Config{Bucket: "bucket", Region: "us-east-1", Prefix: "tenant", Endpoint: server.URL, PathStyle: true, AccessKeyID: "test", SecretAccessKey: "test-secret"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	r := Request{TaskID: "task", ExecutionID: "exec", Kind: Result, Name: "result", Role: "dev"}
	data := `{"outcome":"changed","summary":"done"}`
	ref, err := s.Put(ctx, r, strings.NewReader(data))
	if err != nil {
		t.Fatal("lost response recovery", err)
	}
	if _, err = s.Put(ctx, r, strings.NewReader(data)); err != nil {
		t.Fatal("idempotent put", err)
	}
	if _, err = s.Put(ctx, r, strings.NewReader(`{"outcome":"changed","summary":"other"}`)); err != ErrConflict {
		t.Fatalf("conflict: %v", err)
	}
	if b, err := s.Read(ctx, ref); err != nil || string(b) != data {
		t.Fatalf("read %q %v", b, err)
	}
	if f.puts != 1 {
		t.Fatalf("writes=%d", f.puts)
	}
	f.mu.Lock()
	f.objects[s.key(r)] = []byte("corrupted")
	f.mu.Unlock()
	if _, err = s.Read(ctx, ref); err == nil {
		t.Fatal("corrupt object accepted")
	}
	other := Request{TaskID: "task-other", ExecutionID: "exec", Kind: Report, Name: "report"}
	if _, err = s.Put(ctx, other, strings.NewReader("retained")); err != nil {
		t.Fatal(err)
	}
	if err = s.DeleteTask(ctx, "task"); err != nil {
		t.Fatal(err)
	}
	if err = s.DeleteTask(ctx, "task"); err != nil {
		t.Fatal("idempotent delete", err)
	}
	f.mu.Lock()
	_, gone := f.objects[s.key(r)]
	_, kept := f.objects[s.key(other)]
	f.mu.Unlock()
	if gone || !kept {
		t.Fatal("task prefix cleanup crossed boundary")
	}
}
