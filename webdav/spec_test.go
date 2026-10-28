package webdav_test

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unsafe"

	"github.com/gmgigi96/cernbox-sync/webdav"
)

// ── helpers ───────────────────────────────────────────────────────────────────

const specBase = "/dav/root"

var specNames = []string{
	"plain.txt",
	"hash#tag.txt",
	"100%.txt",
	"%41literal.txt",
	"what?.txt",
	"a&b.txt",
	"plus+sign.txt",
	"semi;colon.txt",
	"it's.txt",
	`say "hi".txt`,
	"with space.txt",
	" leading.txt",
	"trailing.txt ",
	"trailingdot.",
	"emoji-\U0001F600.txt",
	"日本語のファイル.txt",
	"all # % ? & + ; ' \" mixed.txt",
	strings.Repeat("a", 255),
}

func specClient(t *testing.T, h http.HandlerFunc) *webdav.Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return webdav.NewClient(srv.URL+specBase, "user", "secret")
}

func specMultistatus(href, perms string) string {
	href = strings.ReplaceAll(href, "&", "&amp;")
	return fmt.Sprintf(`<?xml version="1.0"?>
<d:multistatus xmlns:d="DAV:" xmlns:oc="http://owncloud.org/ns"><d:response><d:href>%s</d:href><d:propstat><d:prop>
<d:getetag>"e1"</d:getetag><d:getlastmodified>Mon, 02 Jan 2006 15:04:05 GMT</d:getlastmodified>
<d:resourcetype/><d:getcontentlength>3</d:getcontentlength><oc:fileid>f1</oc:fileid><oc:permissions>%s</oc:permissions>
</d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response></d:multistatus>`, href, perms)
}

// specCall invokes one client method on path p.
type specCall struct {
	method string
	call   func(c *webdav.Client, p string) error
	ok     int // status that means success
}

func specCalls() []specCall {
	return []specCall{
		{"PROPFIND", func(c *webdav.Client, p string) error { _, err := c.Propfind(p, 0); return err }, 207},
		{"GET", func(c *webdav.Client, p string) error {
			rc, err := c.Get(p)
			if err == nil {
				_ = rc.Close()
			}
			return err
		}, 200},
		{"PUT", func(c *webdav.Client, p string) error { return c.Put(p, strings.NewReader("abc"), 3) }, 201},
		{"MKCOL", func(c *webdav.Client, p string) error { return c.Mkcol(p) }, 201},
		{"DELETE", func(c *webdav.Client, p string) error { return c.Delete(p) }, 204},
		{"MOVE", func(c *webdav.Client, p string) error { return c.Move(p, p+"-dst", false) }, 201},
	}
}

func specReply(w http.ResponseWriter, r *http.Request, code int) {
	if code == 207 {
		w.WriteHeader(207)
		_, _ = io.WriteString(w, specMultistatus(r.URL.EscapedPath(), "RDNVW"))
		return
	}
	w.WriteHeader(code)
	if code == 200 {
		_, _ = io.WriteString(w, "abc")
	}
}

// ── URL escaping ──────────────────────────────────────────────────────────────

// Plan 4.1, 4.2: every method sends the exact decoded path to the server.
func TestSpec_URLEscaping_AllMethods(t *testing.T) {
	for _, sc := range specCalls() {
		for _, name := range specNames {
			t.Run(fmt.Sprintf("%s/%.20q", sc.method, name), func(t *testing.T) {
				var gotPath, gotQuery, gotFrag string
				c := specClient(t, func(w http.ResponseWriter, r *http.Request) {
					gotPath, gotQuery, gotFrag = r.URL.Path, r.URL.RawQuery, r.URL.Fragment
					specReply(w, r, sc.ok)
				})
				p := "dir name/" + name
				if err := sc.call(c, p); err != nil {
					t.Fatalf("%s: %v", sc.method, err)
				}
				want := specBase + "/" + p
				if sc.method == "MOVE" {
					want = specBase + "/" + p // source path in request line
				}
				if gotPath != want {
					t.Errorf("server path = %q, want %q", gotPath, want)
				}
				if gotQuery != "" || gotFrag != "" {
					t.Errorf("leaked into query/fragment: %q %q", gotQuery, gotFrag)
				}
			})
		}
	}
}

// Plan 4.1, 4.2: PROPFIND hrefs with special names parse back to the decoded path.
func TestSpec_Propfind_HrefDecoding(t *testing.T) {
	for _, name := range specNames {
		t.Run(fmt.Sprintf("%.20q", name), func(t *testing.T) {
			href := specBase + "/" + url.PathEscape("dir name") + "/" + url.PathEscape(name)
			c := specClient(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(207)
				_, _ = io.WriteString(w, specMultistatus(href, "RDNVW"))
			})
			res, err := c.Propfind("dir name", 1)
			if err != nil {
				t.Fatal(err)
			}
			if len(res) != 1 {
				t.Fatalf("got %d resources", len(res))
			}
			if want := "dir name/" + name; res[0].Path != want {
				t.Errorf("Path = %q, want %q", res[0].Path, want)
			}
		})
	}
}

// Plan 4.1, 4.2: MOVE sends an absolute, escaped Destination header.
func TestSpec_Move_DestinationHeader(t *testing.T) {
	for _, name := range specNames {
		t.Run(fmt.Sprintf("%.20q", name), func(t *testing.T) {
			var dest string
			var srvURL string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				dest = r.Header.Get("Destination")
				w.WriteHeader(http.StatusCreated)
			}))
			t.Cleanup(srv.Close)
			srvURL = srv.URL
			c := webdav.NewClient(srvURL+specBase, "u", "p")
			if err := c.Move("old/x", "new dir/"+name, false); err != nil {
				t.Fatal(err)
			}
			u, err := url.Parse(dest)
			if err != nil {
				t.Fatalf("Destination %q: %v", dest, err)
			}
			if u.Scheme == "" || u.Host == "" {
				t.Errorf("Destination not absolute: %q", dest)
			}
			if u.RawQuery != "" || u.Fragment != "" {
				t.Errorf("Destination leaks query/fragment: %q", dest)
			}
			if want := specBase + "/new dir/" + name; u.Path != want {
				t.Errorf("Destination path = %q, want %q", u.Path, want)
			}
		})
	}
}

// ── Timeout ───────────────────────────────────────────────────────────────────

// Plan 2.6: the client must not have an overall http.Client.Timeout.
func TestSpec_NoOverallClientTimeout(t *testing.T) {
	c := webdav.NewClient("http://example.invalid/dav", "u", "p")
	f := reflect.ValueOf(c).Elem().FieldByName("hc")
	if !f.IsValid() {
		t.Skip("no hc field")
	}
	hc := *(**http.Client)(unsafe.Pointer(f.UnsafeAddr()))
	if hc.Timeout != 0 {
		t.Errorf("http.Client.Timeout = %v, want 0", hc.Timeout)
	}
}

// Plan 2.6: a slowly streaming body completes (short version of the >60 s case).
func TestSpec_SlowStreamingGet(t *testing.T) {
	c := specClient(t, func(w http.ResponseWriter, r *http.Request) {
		for range 5 {
			_, _ = io.WriteString(w, "chunk")
			w.(http.Flusher).Flush()
			time.Sleep(60 * time.Millisecond)
		}
	})
	rc, err := c.Get("slow")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rc.Close() }()
	b, err := io.ReadAll(rc)
	if err != nil || string(b) != strings.Repeat("chunk", 5) {
		t.Fatalf("read = %q, %v", b, err)
	}
}

// ── StatusError ───────────────────────────────────────────────────────────────

// Contract: unexpected status yields *StatusError{Method, Path, Code}; text format.
func TestSpec_StatusError_Fields(t *testing.T) {
	for _, sc := range specCalls() {
		t.Run(sc.method, func(t *testing.T) {
			c := specClient(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusForbidden)
				_, _ = io.WriteString(w, "nope")
			})
			err := sc.call(c, "some/file")
			var se *webdav.StatusError
			if !errors.As(err, &se) {
				t.Fatalf("err = %v (%T), want *StatusError", err, err)
			}
			if se.Method != sc.method || se.Code != 403 || !strings.Contains(se.Path, "some/file") {
				t.Errorf("got %+v", se)
			}
			if sc.method != "GET" && !strings.Contains(se.Error(), "status 403") {
				t.Errorf("Error() = %q", se.Error())
			}
			if !strings.HasPrefix(se.Error(), sc.method+" ") {
				t.Errorf("Error() = %q, want %s prefix", se.Error(), sc.method)
			}
		})
	}
}

// Plan 1.3: 401 on every method matches ErrUnauthorized and is not retried.
func TestSpec_StatusError_Unauthorized(t *testing.T) {
	for _, sc := range specCalls() {
		t.Run(sc.method, func(t *testing.T) {
			var n atomic.Int32
			c := specClient(t, func(w http.ResponseWriter, r *http.Request) {
				n.Add(1)
				w.WriteHeader(http.StatusUnauthorized)
			})
			c.SetRetryPolicy(3, 10*time.Millisecond)
			err := sc.call(c, "f")
			if !errors.Is(err, webdav.ErrUnauthorized) {
				t.Fatalf("err = %v, want ErrUnauthorized", err)
			}
			if n.Load() != 1 {
				t.Errorf("requests = %d, want 1", n.Load())
			}
		})
	}
}

// Plan 11.1 (423), 12.1 (507), 403: StatusError with that code, not retried.
func TestSpec_StatusError_NotRetried(t *testing.T) {
	for _, code := range []int{403, 423, 507} {
		for _, sc := range specCalls() {
			t.Run(fmt.Sprintf("%d/%s", code, sc.method), func(t *testing.T) {
				var n atomic.Int32
				c := specClient(t, func(w http.ResponseWriter, r *http.Request) {
					n.Add(1)
					w.WriteHeader(code)
				})
				c.SetRetryPolicy(3, 10*time.Millisecond)
				err := sc.call(c, "f")
				var se *webdav.StatusError
				if !errors.As(err, &se) || se.Code != code {
					t.Fatalf("err = %v, want StatusError %d", err, code)
				}
				if n.Load() != 1 {
					t.Errorf("requests = %d, want 1", n.Load())
				}
			})
		}
	}
}

// ── Move ──────────────────────────────────────────────────────────────────────

// Plan 3.x: MOVE method, Overwrite header, success on 201/204.
func TestSpec_Move_Success(t *testing.T) {
	for _, overwrite := range []bool{false, true} {
		for _, code := range []int{201, 204} {
			t.Run(fmt.Sprintf("overwrite=%v/%d", overwrite, code), func(t *testing.T) {
				var method, ow, path string
				c := specClient(t, func(w http.ResponseWriter, r *http.Request) {
					method, ow, path = r.Method, r.Header.Get("Overwrite"), r.URL.Path
					w.WriteHeader(code)
				})
				if err := c.Move("a/b.txt", "c/d.txt", overwrite); err != nil {
					t.Fatal(err)
				}
				want := "F"
				if overwrite {
					want = "T"
				}
				if method != "MOVE" || ow != want || path != specBase+"/a/b.txt" {
					t.Errorf("method=%q overwrite=%q path=%q", method, ow, path)
				}
			})
		}
	}
}

// Plan 3.x: MOVE 412 / 409 map to StatusError.
func TestSpec_Move_Errors(t *testing.T) {
	for _, code := range []int{412, 409} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			c := specClient(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(code) })
			err := c.Move("a", "b", false)
			var se *webdav.StatusError
			if !errors.As(err, &se) || se.Code != code || se.Method != "MOVE" {
				t.Fatalf("err = %v", err)
			}
		})
	}
}

// ── Retries ───────────────────────────────────────────────────────────────────

// Plan 12.3: 429 with Retry-After: 1 is retried after ~1 s.
func TestSpec_Retry_429RetryAfter(t *testing.T) {
	var n atomic.Int32
	c := specClient(t, func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1) == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		specReply(w, r, 207)
	})
	c.SetRetryPolicy(3, 10*time.Millisecond)
	start := time.Now()
	if _, err := c.Propfind("", 0); err != nil {
		t.Fatal(err)
	}
	if el := time.Since(start); el < 900*time.Millisecond {
		t.Errorf("elapsed %v, want >= ~1s", el)
	}
	if n.Load() != 2 {
		t.Errorf("requests = %d, want 2", n.Load())
	}
}

// Plan 12.3: Retry-After as an HTTP date is accepted.
func TestSpec_Retry_RetryAfterHTTPDate(t *testing.T) {
	var n atomic.Int32
	c := specClient(t, func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1) == 1 {
			w.Header().Set("Retry-After", time.Now().Add(time.Second).UTC().Format(http.TimeFormat))
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		specReply(w, r, 200)
	})
	c.SetRetryPolicy(3, 10*time.Millisecond)
	rc, err := c.Get("f")
	if err != nil {
		t.Fatal(err)
	}
	_ = rc.Close()
	if n.Load() != 2 {
		t.Errorf("requests = %d, want 2", n.Load())
	}
}

// Plan 12.4: 502/503/504 are retried with backoff and succeed.
func TestSpec_Retry_5xxThenSuccess(t *testing.T) {
	for _, code := range []int{502, 503, 504} {
		for _, sc := range []specCall{specCalls()[0], specCalls()[1], specCalls()[3], specCalls()[4]} {
			t.Run(fmt.Sprintf("%d/%s", code, sc.method), func(t *testing.T) {
				var n atomic.Int32
				c := specClient(t, func(w http.ResponseWriter, r *http.Request) {
					if n.Add(1) <= 2 {
						w.WriteHeader(code)
						return
					}
					specReply(w, r, sc.ok)
				})
				c.SetRetryPolicy(3, 10*time.Millisecond)
				if err := sc.call(c, "f"); err != nil {
					t.Fatal(err)
				}
				if n.Load() != 3 {
					t.Errorf("requests = %d, want 3", n.Load())
				}
			})
		}
	}
}

// Plan 12.4: retries stop after maxRetries; last StatusError is returned.
func TestSpec_Retry_GivesUp(t *testing.T) {
	for _, sc := range []specCall{specCalls()[0], specCalls()[1], specCalls()[3], specCalls()[4]} {
		t.Run(sc.method, func(t *testing.T) {
			var n atomic.Int32
			c := specClient(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(500 + 2 + int(n.Add(1))%2) // alternate 503 / 502; last is checked below
			})
			c.SetRetryPolicy(2, 10*time.Millisecond)
			err := sc.call(c, "f")
			var se *webdav.StatusError
			if !errors.As(err, &se) {
				t.Fatalf("err = %v, want StatusError", err)
			}
			if n.Load() != 3 {
				t.Errorf("requests = %d, want 3 (1 + 2 retries)", n.Load())
			}
			if want := 502 + int(n.Load())%2; se.Code != want {
				t.Errorf("Code = %d, want last status %d", se.Code, want)
			}
		})
	}
}

// Plan 12.5: a dropped connection (transport error) is retried for PROPFIND and GET.
func TestSpec_Retry_TransportError(t *testing.T) {
	for _, sc := range specCalls()[:2] {
		t.Run(sc.method, func(t *testing.T) {
			var n atomic.Int32
			c := specClient(t, func(w http.ResponseWriter, r *http.Request) {
				if n.Add(1) == 1 {
					conn, _, err := w.(http.Hijacker).Hijack()
					if err == nil {
						_ = conn.Close()
					}
					return
				}
				specReply(w, r, sc.ok)
			})
			c.SetRetryPolicy(3, 10*time.Millisecond)
			if err := sc.call(c, "f"); err != nil {
				t.Fatal(err)
			}
			if n.Load() != 2 {
				t.Errorf("requests = %d, want 2", n.Load())
			}
		})
	}
}

// opaqueReader hides Seek/Len so the body cannot be re-opened.
type opaqueReader struct{ r io.Reader }

func (o opaqueReader) Read(p []byte) (int, error) { return o.r.Read(p) }

// Plan 12.5: a PUT with a non-rewindable body is not resent after a partial read.
func TestSpec_Retry_PutNotResentAfterPartialRead(t *testing.T) {
	var n atomic.Int32
	c := specClient(t, func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		buf := make([]byte, 4)
		_, _ = io.ReadFull(r.Body, buf)
		conn, _, err := w.(http.Hijacker).Hijack()
		if err == nil {
			_ = conn.Close()
		}
	})
	c.SetRetryPolicy(3, 10*time.Millisecond)
	body := opaqueReader{strings.NewReader(strings.Repeat("x", 1<<20))}
	if err := c.Put("big", body, 1<<20); err == nil {
		t.Fatal("expected error")
	}
	if n.Load() != 1 {
		t.Errorf("requests = %d, want 1 (no resend of half-consumed stream)", n.Load())
	}
}

// Plan 12.3: a huge Retry-After does not block once retries are exhausted
// (and, by contract, is capped at 5 min otherwise).
func TestSpec_Retry_RetryAfterCapped(t *testing.T) {
	c := specClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "86400")
		w.WriteHeader(http.StatusTooManyRequests)
	})
	c.SetRetryPolicy(0, 10*time.Millisecond)
	start := time.Now()
	_, err := c.Propfind("", 0)
	var se *webdav.StatusError
	if !errors.As(err, &se) || se.Code != 429 {
		t.Fatalf("err = %v, want StatusError 429", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Errorf("waited %v with no retries left", time.Since(start))
	}
}

// ── Permissions ───────────────────────────────────────────────────────────────

// Contract: oc:permissions is exposed in Resource.Permissions.
func TestSpec_Propfind_Permissions(t *testing.T) {
	c := specClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(207)
		_, _ = io.WriteString(w, specMultistatus(specBase+"/f.txt", "RDNVW"))
	})
	res, err := c.Propfind("f.txt", 0)
	if err != nil || len(res) != 1 {
		t.Fatalf("res=%v err=%v", res, err)
	}
	if res[0].Permissions != "RDNVW" {
		t.Errorf("Permissions = %q", res[0].Permissions)
	}
}
