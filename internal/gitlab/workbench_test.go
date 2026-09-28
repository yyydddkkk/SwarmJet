package gitlab

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestWorkbenchPageAndWritesUsePersonalToken(t *testing.T) {
	var paths []string
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("PRIVATE-TOKEN") != "personal-secret" {
			t.Errorf("missing personal token")
		}
		paths = append(paths, r.Method+" "+r.URL.RequestURI())
		switch r.URL.Path {
		case "/api/v4/projects/7/issues":
			if r.Method == http.MethodGet {
				if r.URL.Query().Get("page") != "2" || r.URL.Query().Get("state") != "closed" {
					t.Errorf("filters missing: %v", r.URL.Query())
				}
				w.Header().Set("X-Next-Page", "3")
				io.WriteString(w, `[{"iid":12,"title":"Fix"}]`)
				return
			}
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body["title"] != "New" {
				t.Errorf("create body: %v, %v", body, err)
			}
			io.WriteString(w, `{"iid":13,"title":"New"}`)
		case "/api/v4/projects/7/merge_requests/3/approve":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["sha"] != "head-sha" {
				t.Errorf("approval did not bind HEAD: %v", body)
			}
			io.WriteString(w, `{}`)
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	defer remote.Close()
	c := New(remote.URL, "personal-secret")
	rows, more, err := c.WorkbenchPage(context.Background(), "issues", 7, url.Values{"state": {"closed"}}, 2, 50)
	if err != nil || !more || len(rows) != 1 {
		t.Fatalf("page: %v, %v, %d", err, more, len(rows))
	}
	if _, err := c.WorkbenchWrite(context.Background(), http.MethodPost, "issues", 7, 0, map[string]any{"title": "New"}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.WorkbenchSubresource(context.Background(), http.MethodPost, "merge_requests", 7, 3, "/approve", map[string]any{"sha": "head-sha"}); err != nil {
		t.Fatal(err)
	}
	if len(paths) != 3 {
		t.Fatalf("calls: %v", paths)
	}
}

func TestWorkbenchOptionsPaginatesLabelsAndSearchesActiveMembers(t *testing.T) {
	var calls []string
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("PRIVATE-TOKEN") != "personal-secret" {
			t.Errorf("options did not use personal identity")
		}
		calls = append(calls, r.URL.RequestURI())
		switch r.URL.Path {
		case "/api/v4/projects/7/labels":
			if r.URL.Query().Get("per_page") != "100" {
				t.Errorf("labels missing page size: %s", r.URL.RawQuery)
			}
			if r.URL.Query().Get("page") == "1" {
				w.Header().Set("X-Next-Page", "2")
				io.WriteString(w, `[{"name":"bug","color":"#ff0000"}]`)
			} else {
				io.WriteString(w, `[{"name":"priority::high","color":"#0000ff"}]`)
			}
		case "/api/v4/projects/7/members/all":
			if r.URL.Query().Get("query") != "ali" || r.URL.Query().Get("state") != "active" {
				t.Errorf("member search missing: %s", r.URL.RawQuery)
			}
			io.WriteString(w, `[{"id":42,"name":"Alice","username":"alice","avatar_url":"https://example/avatar","state":"active"},{"id":43,"name":"Old","username":"old","state":"blocked"}]`)
		default:
			t.Errorf("wrong metadata path: %s", r.URL.Path)
		}
	}))
	defer remote.Close()
	labels, assignees, labelsTruncated, assigneesTruncated, err := New(remote.URL, "personal-secret").WorkbenchOptions(context.Background(), 7, "ali")
	if err != nil || labelsTruncated || assigneesTruncated || len(labels) != 2 || labels[1].Name != "priority::high" || len(assignees) != 1 || assignees[0].ID != 42 || assignees[0].AvatarURL != "https://example/avatar" {
		t.Fatalf("options: labels=%+v assignees=%+v truncated=%v/%v err=%v", labels, assignees, labelsTruncated, assigneesTruncated, err)
	}
	if len(calls) != 3 {
		t.Fatalf("expected 3 metadata calls, got %v", calls)
	}
}

func TestWorkbenchOptionsBoundsMemberResults(t *testing.T) {
	memberCalls := 0
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v4/projects/7/labels":
			io.WriteString(w, `[]`)
		case "/api/v4/projects/7/members/all":
			memberCalls++
			w.Header().Set("X-Next-Page", "2")
			io.WriteString(w, `[{"id":1,"name":"Alice","username":"alice"}]`)
		default:
			t.Errorf("unexpected options path: %s", r.URL.Path)
		}
	}))
	defer remote.Close()
	_, assignees, _, truncated, err := New(remote.URL, "personal-secret").WorkbenchOptions(context.Background(), 7, "")
	if err != nil || memberCalls != 1 || !truncated || len(assignees) != 1 {
		t.Fatalf("bounded members: calls=%d truncated=%v assignees=%+v err=%v", memberCalls, truncated, assignees, err)
	}
}

func TestWorkbenchUploadAndPermissionFailure(t *testing.T) {
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v4/projects/5/uploads" {
			if r.Header.Get("PRIVATE-TOKEN") != "personal-secret" || !strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data;") {
				t.Errorf("bad upload headers")
			}
			file, header, err := r.FormFile("file")
			if err != nil || header.Filename != "image.png" {
				t.Errorf("bad upload file: %v", err)
			}
			defer file.Close()
			data, _ := io.ReadAll(file)
			if !bytes.Equal(data, []byte("png")) {
				t.Errorf("bad upload content")
			}
			io.WriteString(w, `{"markdown":"![image](/uploads/x)"}`)
			return
		}
		w.WriteHeader(http.StatusForbidden)
		io.WriteString(w, `{"message":"secret-in-response"}`)
	}))
	defer remote.Close()
	c := New(remote.URL, "personal-secret")
	data, err := c.WorkbenchUpload(context.Background(), 5, "image.png", strings.NewReader("png"))
	if err != nil || !bytes.Contains(data, []byte("markdown")) {
		t.Fatalf("upload: %s, %v", data, err)
	}
	_, err = c.WorkbenchGet(context.Background(), "issues", 5, 1)
	if code, ok := StatusCode(err); !ok || code != 403 || strings.Contains(err.Error(), "secret-in-response") {
		t.Fatalf("permission error leaks response: %v", err)
	}
}

func TestGitLabErrorReasonRedactsToken(t *testing.T) {
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(422)
		io.WriteString(w, `{"message":"invalid target; token personal-secret"}`)
	}))
	defer remote.Close()
	_, err := New(remote.URL, "personal-secret").WorkbenchGet(context.Background(), "issues", 1, 1)
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Reason != "invalid target; token [redacted]" || strings.Contains(err.Error(), "personal-secret") {
		t.Fatalf("error leaked token: %#v", err)
	}
}

func TestMergePinnedOptionalCIStillUsesFixedSHA(t *testing.T) {
	mergeCalls := 0
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v4/projects/5/merge_requests/2":
			io.WriteString(w, `{"iid":2,"sha":"head","target_branch":"main","state":"opened","detailed_merge_status":"mergeable"}`)
		case "/api/v4/projects/5/merge_requests/2/merge":
			mergeCalls++
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["sha"] != "head" {
				t.Errorf("merge was not SHA pinned: %v", body)
			}
			io.WriteString(w, `{}`)
		default:
			t.Errorf("unexpected %s", r.URL.Path)
		}
	}))
	defer remote.Close()
	c := New(remote.URL, "secret")
	if err := c.MergePinned(context.Background(), 5, 2, "head", "main", false); err != nil {
		t.Fatal(err)
	}
	if mergeCalls != 1 {
		t.Fatalf("merge calls: %d", mergeCalls)
	}
	if err := c.MergePinned(context.Background(), 5, 2, "stale", "main", false); err == nil {
		t.Fatal("stale SHA accepted")
	}
	if mergeCalls != 1 {
		t.Fatal("stale SHA was submitted")
	}
}

func TestWorkbenchDiffUsesGitLabPaginationAndVersionSHA(t *testing.T) {
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v4/projects/5/merge_requests/2/versions":
			io.WriteString(w, `[{"id":9,"base_commit_sha":"base","start_commit_sha":"start","head_commit_sha":"head","state":"overflow"}]`)
		case "/api/v4/projects/5/merge_requests/2/diffs":
			if r.URL.Query().Get("page") != "2" || r.URL.Query().Get("per_page") != "20" {
				t.Errorf("missing diff pagination: %v", r.URL.Query())
			}
			w.Header().Set("X-Next-Page", "3")
			io.WriteString(w, `[{"new_path":"a.go","diff":"@@","too_large":true}]`)
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	defer remote.Close()
	diffs, more, version, err := New(remote.URL, "token").WorkbenchDiffPage(context.Background(), 5, 2, 2)
	if err != nil || !more || len(diffs) != 1 || !diffs[0].TooLarge || version.HeadCommitSHA != "head" || version.State != "overflow" {
		t.Fatalf("diff page: %+v %v %+v %v", diffs, more, version, err)
	}
}
