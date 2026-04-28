package webdav_test

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gmgigi96/cernbox-sync/webdav"
)

// ── helpers ───────────────────────────────────────────────────────────────────

const httpTimeFormat = "Mon, 02 Jan 2006 15:04:05 GMT"

type testResource struct {
	href    string
	etag    string
	isDir   bool
	size    int64
	modTime time.Time
	fileID  string
}

// propfindXML builds a 207 Multi-Status XML body from the given resources.
func propfindXML(resources []testResource) string {
	var sb strings.Builder
	sb.WriteString(`<?xml version="1.0"?>`)
	sb.WriteString(`<d:multistatus xmlns:d="DAV:" xmlns:oc="http://owncloud.org/ns">`)
	for _, r := range resources {
		fmt.Fprintf(&sb, `<d:response><d:href>%s</d:href><d:propstat><d:prop>`, r.href)
		fmt.Fprintf(&sb, `<d:getetag>"%s"</d:getetag>`, r.etag)
		fmt.Fprintf(&sb, `<d:getlastmodified>%s</d:getlastmodified>`, r.modTime.UTC().Format(httpTimeFormat))
		if r.isDir {
			sb.WriteString(`<d:resourcetype><d:collection/></d:resourcetype>`)
		} else {
			sb.WriteString(`<d:resourcetype/>`)
			fmt.Fprintf(&sb, `<d:getcontentlength>%d</d:getcontentlength>`, r.size)
		}
		fmt.Fprintf(&sb, `<oc:fileid>%s</oc:fileid>`, r.fileID)
		sb.WriteString(`</d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response>`)
	}
	sb.WriteString(`</d:multistatus>`)
	return sb.String()
}

// newTestServer starts an httptest server and returns (server, client).
// subPath is appended to srv.URL to form the WebDAV base (e.g. "/dav/spaces/root").
func newTestServer(t *testing.T, h http.Handler, subPath string) (*httptest.Server, *webdav.Client) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	base := srv.URL + subPath
	c := webdav.NewClient(base, "user", "secret")
	return srv, c
}

// ── NewClient ─────────────────────────────────────────────────────────────────

func TestNewClient_TrimsTrailingSlash(t *testing.T) {
	// A trailing slash in the base must not produce double slashes in requests.
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusMethodNotAllowed)
	}))
	t.Cleanup(srv.Close)

	c := webdav.NewClient(srv.URL+"/dav/spaces/root/", "u", "p")
	_, _ = c.Propfind("", 0) // ignore error – we just want to capture the path

	if strings.HasPrefix(gotPath, "//") {
		t.Errorf("double slash in request path: %q", gotPath)
	}
}

// ── Propfind ──────────────────────────────────────────────────────────────────

func TestPropfind_File(t *testing.T) {
	modTime := time.Date(2024, 3, 15, 10, 0, 0, 0, time.UTC)
	_, c := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusMultiStatus)
		_, _ = fmt.Fprint(w, propfindXML([]testResource{
			{href: "/report.txt", etag: "abc123", isDir: false, size: 42, modTime: modTime, fileID: "fid-1"},
		}))
	}), "")

	resources, err := c.Propfind("report.txt", 0)
	if err != nil {
		t.Fatalf("Propfind: %v", err)
	}
	if len(resources) != 1 {
		t.Fatalf("expected 1 resource, got %d", len(resources))
	}
	r := resources[0]
	if r.Path != "report.txt" {
		t.Errorf("Path: got %q, want %q", r.Path, "report.txt")
	}
	if r.ETag != "abc123" {
		t.Errorf("ETag: got %q (quotes should be stripped)", r.ETag)
	}
	if r.Size != 42 {
		t.Errorf("Size: got %d", r.Size)
	}
	if r.IsDir {
		t.Error("IsDir should be false")
	}
	if r.FileID != "fid-1" {
		t.Errorf("FileID: got %q", r.FileID)
	}
	if !r.LastModified.Equal(modTime) {
		t.Errorf("LastModified: got %v, want %v", r.LastModified, modTime)
	}
}

func TestPropfind_Directory(t *testing.T) {
	_, c := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusMultiStatus)
		_, _ = fmt.Fprint(w, propfindXML([]testResource{
			{href: "/docs", etag: "dir-etag", isDir: true, fileID: "fid-dir"},
		}))
	}), "")

	resources, err := c.Propfind("docs", 0)
	if err != nil {
		t.Fatalf("Propfind: %v", err)
	}
	if len(resources) != 1 {
		t.Fatalf("expected 1 resource, got %d", len(resources))
	}
	if !resources[0].IsDir {
		t.Error("IsDir should be true for collection")
	}
	if resources[0].Path != "docs" {
		t.Errorf("Path: got %q", resources[0].Path)
	}
}

func TestPropfind_MultipleResources(t *testing.T) {
	_, c := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusMultiStatus)
		_, _ = fmt.Fprint(w, propfindXML([]testResource{
			{href: "/docs", isDir: true, etag: "e0"},
			{href: "/docs/a.txt", isDir: false, size: 10, etag: "e1"},
			{href: "/docs/b.txt", isDir: false, size: 20, etag: "e2"},
		}))
	}), "")

	resources, err := c.Propfind("docs", 1)
	if err != nil {
		t.Fatalf("Propfind: %v", err)
	}
	if len(resources) != 3 {
		t.Fatalf("expected 3 resources, got %d", len(resources))
	}
}

func TestPropfind_ETagQuotesStripped(t *testing.T) {
	_, c := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusMultiStatus)
		// Intentionally use outer quotes (the propfindXML helper adds them).
		_, _ = fmt.Fprint(w, propfindXML([]testResource{
			{href: "/f.txt", etag: "quoted-etag"},
		}))
	}), "")

	resources, err := c.Propfind("f.txt", 0)
	if err != nil {
		t.Fatalf("Propfind: %v", err)
	}
	if resources[0].ETag != "quoted-etag" {
		t.Errorf("ETag should have quotes stripped, got %q", resources[0].ETag)
	}
}

func TestPropfind_URLEncodedPath(t *testing.T) {
	// Hrefs with percent-encoded characters must decode to the plain path.
	_, c := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusMultiStatus)
		// href with space encoded as %20
		_, _ = fmt.Fprint(w, propfindXML([]testResource{
			{href: "/my%20file.txt", etag: "e1"},
		}))
	}), "")

	resources, err := c.Propfind("my%20file.txt", 0)
	if err != nil {
		t.Fatalf("Propfind: %v", err)
	}
	if resources[0].Path != "my file.txt" {
		t.Errorf("Path: got %q, want %q", resources[0].Path, "my file.txt")
	}
}

func TestPropfind_BasePath_Stripped(t *testing.T) {
	// When the client base includes a sub-path, the sub-path must be stripped
	// from resource paths in the response.
	_, c := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusMultiStatus)
		_, _ = fmt.Fprint(w, propfindXML([]testResource{
			{href: "/dav/spaces/root/notes/todo.txt", etag: "e1", size: 5},
		}))
	}), "/dav/spaces/root")

	resources, err := c.Propfind("notes/todo.txt", 0)
	if err != nil {
		t.Fatalf("Propfind: %v", err)
	}
	if resources[0].Path != "notes/todo.txt" {
		t.Errorf("Path: got %q (base path not stripped correctly)", resources[0].Path)
	}
}

func TestPropfind_RootHref_EmptyPath(t *testing.T) {
	// The root resource itself should produce Path == "".
	_, c := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusMultiStatus)
		_, _ = fmt.Fprint(w, propfindXML([]testResource{
			{href: "/", isDir: true, etag: "root-etag"},
		}))
	}), "")

	resources, err := c.Propfind("", 0)
	if err != nil {
		t.Fatalf("Propfind: %v", err)
	}
	if resources[0].Path != "" {
		t.Errorf("root path should be empty string, got %q", resources[0].Path)
	}
}

func TestPropfind_SetsBasicAuth(t *testing.T) {
	var user, pass string
	_, c := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, _ = r.BasicAuth()
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusMultiStatus)
		_, _ = fmt.Fprint(w, propfindXML(nil))
	}), "")

	_, _ = c.Propfind("", 0)
	if user != "user" || pass != "secret" {
		t.Errorf("basic auth: got user=%q pass=%q", user, pass)
	}
}

func TestPropfind_SetsDepthHeader(t *testing.T) {
	var depth string
	_, c := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		depth = r.Header.Get("Depth")
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusMultiStatus)
		_, _ = fmt.Fprint(w, propfindXML(nil))
	}), "")

	_, _ = c.Propfind("", 1)
	if depth != "1" {
		t.Errorf("Depth header: got %q, want %q", depth, "1")
	}
}

func TestPropfind_UnexpectedStatus_ReturnsError(t *testing.T) {
	_, c := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	}), "")

	_, err := c.Propfind("missing", 0)
	if err == nil {
		t.Fatal("expected error for non-207 status")
	}
}

// ── Get ───────────────────────────────────────────────────────────────────────

func TestGet_Success(t *testing.T) {
	_, c := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			http.Error(w, "wrong method", http.StatusMethodNotAllowed)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, "hello world")
	}), "")

	rc, err := c.Get("hello.txt")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer func() { _ = rc.Close() }()
	body, _ := io.ReadAll(rc)
	if string(body) != "hello world" {
		t.Errorf("body: got %q", string(body))
	}
}

func TestGet_SetsBasicAuth(t *testing.T) {
	var user, pass string
	_, c := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, _ = r.BasicAuth()
		w.WriteHeader(http.StatusOK)
	}), "")

	rc, _ := c.Get("f.txt")
	if rc != nil {
		_ = rc.Close()
	}
	if user != "user" || pass != "secret" {
		t.Errorf("basic auth: got user=%q pass=%q", user, pass)
	}
}

func TestGet_NotFound_ReturnsError(t *testing.T) {
	_, c := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}), "")

	_, err := c.Get("missing.txt")
	if err == nil {
		t.Fatal("expected error for 404 response")
	}
}

// ── Put ───────────────────────────────────────────────────────────────────────

func TestPut_Success_Created(t *testing.T) {
	var gotBody []byte
	_, c := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "PUT" {
			http.Error(w, "wrong method", http.StatusMethodNotAllowed)
			return
		}
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusCreated)
	}), "")

	content := strings.NewReader("file content")
	if err := c.Put("upload.txt", content, int64(len("file content"))); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if string(gotBody) != "file content" {
		t.Errorf("server received %q", string(gotBody))
	}
}

func TestPut_Success_NoContent(t *testing.T) {
	_, c := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusNoContent)
	}), "")

	if err := c.Put("file.txt", strings.NewReader("data"), 4); err != nil {
		t.Fatalf("Put with 204: %v", err)
	}
}

func TestPut_Success_OK(t *testing.T) {
	_, c := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusOK)
	}), "")

	if err := c.Put("file.txt", strings.NewReader("data"), 4); err != nil {
		t.Fatalf("Put with 200: %v", err)
	}
}

func TestPut_SetsContentTypeAndLength(t *testing.T) {
	var ct string
	var cl int64
	_, c := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ct = r.Header.Get("Content-Type")
		cl = r.ContentLength
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusCreated)
	}), "")

	_ = c.Put("f.txt", strings.NewReader("hello"), 5)
	if ct != "application/octet-stream" {
		t.Errorf("Content-Type: got %q", ct)
	}
	if cl != 5 {
		t.Errorf("ContentLength: got %d", cl)
	}
}

func TestPut_ErrorStatus_ReturnsError(t *testing.T) {
	_, c := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		http.Error(w, "server error", http.StatusInternalServerError)
	}), "")

	err := c.Put("fail.txt", strings.NewReader("data"), 4)
	if err == nil {
		t.Fatal("expected error for 500 response")
	}
}

// ── Mkcol ─────────────────────────────────────────────────────────────────────

func TestMkcol_Success(t *testing.T) {
	var gotMethod string
	_, c := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		w.WriteHeader(http.StatusCreated)
	}), "")

	if err := c.Mkcol("newdir"); err != nil {
		t.Fatalf("Mkcol: %v", err)
	}
	if gotMethod != "MKCOL" {
		t.Errorf("expected MKCOL method, got %q", gotMethod)
	}
}

func TestMkcol_AlreadyExists_NoError(t *testing.T) {
	// 405 Method Not Allowed is returned when the collection already exists.
	_, c := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusMethodNotAllowed)
	}), "")

	if err := c.Mkcol("existingdir"); err != nil {
		t.Fatalf("Mkcol 405 should not error: %v", err)
	}
}

func TestMkcol_ErrorStatus_ReturnsError(t *testing.T) {
	_, c := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "forbidden", http.StatusForbidden)
	}), "")

	if err := c.Mkcol("forbidden"); err == nil {
		t.Fatal("expected error for 403 response")
	}
}

// ── Delete ────────────────────────────────────────────────────────────────────

func TestDelete_Success(t *testing.T) {
	var gotMethod string
	_, c := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		w.WriteHeader(http.StatusNoContent)
	}), "")

	if err := c.Delete("old.txt"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if gotMethod != "DELETE" {
		t.Errorf("expected DELETE method, got %q", gotMethod)
	}
}

func TestDelete_OK_NoError(t *testing.T) {
	_, c := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}), "")

	if err := c.Delete("file.txt"); err != nil {
		t.Fatalf("Delete 200 should not error: %v", err)
	}
}

func TestDelete_NotFound_NoError(t *testing.T) {
	// 404 is treated as success — resource is already gone.
	_, c := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}), "")

	if err := c.Delete("ghost.txt"); err != nil {
		t.Fatalf("Delete 404 should not error: %v", err)
	}
}

func TestDelete_ErrorStatus_ReturnsError(t *testing.T) {
	_, c := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "server error", http.StatusInternalServerError)
	}), "")

	if err := c.Delete("fail.txt"); err == nil {
		t.Fatal("expected error for 500 response")
	}
}
