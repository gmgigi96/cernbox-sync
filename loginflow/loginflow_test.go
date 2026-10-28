package loginflow

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gmgigi96/cernbox-sync/version"
)

// fakeServer is a minimal Login Flow V2 server: one flow, approved on demand,
// consumed once.
type fakeServer struct {
	*httptest.Server

	mu         sync.Mutex
	initUA     string
	approved   bool
	consumed   bool
	pollsSeen  int
	rateLimits int // number of polls answered with 429 before normal handling
}

const (
	testLoginToken = "login-token"
	testPollToken  = "poll-token"
)

func newFakeServer(t *testing.T) *fakeServer {
	t.Helper()
	fs := &fakeServer{}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /index.php/login/v2", func(w http.ResponseWriter, r *http.Request) {
		fs.mu.Lock()
		fs.initUA = r.Header.Get("User-Agent")
		fs.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{
			"poll":  map[string]string{"token": testPollToken, "endpoint": fs.URL + "/index.php/login/v2/poll"},
			"login": fs.URL + "/index.php/login/v2/flow/" + testLoginToken,
		})
	})
	mux.HandleFunc("POST /index.php/login/v2/poll", func(w http.ResponseWriter, r *http.Request) {
		fs.mu.Lock()
		defer fs.mu.Unlock()
		fs.pollsSeen++
		if fs.rateLimits > 0 {
			fs.rateLimits--
			w.Header().Set("Retry-After", "1")
			http.Error(w, "too many requests", http.StatusTooManyRequests)
			return
		}
		if r.FormValue("token") != testPollToken || r.Header.Get("User-Agent") != fs.initUA ||
			!fs.approved || fs.consumed {
			http.NotFound(w, r)
			return
		}
		fs.consumed = true
		_ = json.NewEncoder(w).Encode(Credentials{Server: fs.URL, LoginName: "einstein", AppPassword: "app-secret"})
	})
	fs.Server = httptest.NewServer(mux)
	t.Cleanup(fs.Close)
	return fs
}

func (fs *fakeServer) approve() {
	fs.mu.Lock()
	fs.approved = true
	fs.mu.Unlock()
}

func TestStartReturnsLoginURL(t *testing.T) {
	fs := newFakeServer(t)

	f, err := Start(context.Background(), fs.URL+"/")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if want := fs.URL + "/index.php/login/v2/flow/" + testLoginToken; f.LoginURL != want {
		t.Errorf("LoginURL = %q, want %q", f.LoginURL, want)
	}
	if fs.initUA != version.UserAgent() {
		t.Errorf("init User-Agent = %q, want %q", fs.initUA, version.UserAgent())
	}
}

func TestPollPendingThenApproved(t *testing.T) {
	fs := newFakeServer(t)
	f, err := Start(context.Background(), fs.URL)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	if _, err := f.Poll(context.Background()); !errors.Is(err, ErrPending) {
		t.Fatalf("Poll before approval: err = %v, want ErrPending", err)
	}

	fs.approve()
	c, err := f.Poll(context.Background())
	if err != nil {
		t.Fatalf("Poll after approval: %v", err)
	}
	if c.LoginName != "einstein" || c.AppPassword != "app-secret" || c.Server != fs.URL {
		t.Errorf("credentials = %+v", c)
	}

	// The flow is consumed: polling again must not hand out credentials.
	if _, err := f.Poll(context.Background()); !errors.Is(err, ErrPending) {
		t.Fatalf("Poll after consume: err = %v, want ErrPending", err)
	}
}

func TestWaitReturnsOnApproval(t *testing.T) {
	fs := newFakeServer(t)
	fs.rateLimits = 1
	f, err := Start(context.Background(), fs.URL)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	go func() {
		time.Sleep(100 * time.Millisecond)
		fs.approve()
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, err := f.Wait(ctx, 20*time.Millisecond)
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if c.LoginName != "einstein" {
		t.Errorf("LoginName = %q", c.LoginName)
	}
}

func TestWaitHonoursContext(t *testing.T) {
	fs := newFakeServer(t)
	f, err := Start(context.Background(), fs.URL)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := f.Wait(ctx, 10*time.Millisecond); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Wait: err = %v, want DeadlineExceeded", err)
	}
}

func TestStartRejectsNonHTTPLoginURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"poll":  map[string]string{"token": "t", "endpoint": "http://" + r.Host + "/poll"},
			"login": "file:///etc/passwd",
		})
	}))
	defer srv.Close()

	if _, err := Start(context.Background(), srv.URL); err == nil || !strings.Contains(err.Error(), "invalid login URL") {
		t.Fatalf("Start: err = %v, want invalid login URL", err)
	}
}

func TestStartUnsupportedServer(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()

	if _, err := Start(context.Background(), srv.URL); err == nil || !strings.Contains(err.Error(), "does not support") {
		t.Fatalf("Start: err = %v, want unsupported server error", err)
	}
}

func TestStartInvalidServerURL(t *testing.T) {
	for _, u := range []string{"", "cernbox.cern.ch", "ftp://cernbox.cern.ch"} {
		if _, err := Start(context.Background(), u); err == nil {
			t.Errorf("Start(%q): expected error", u)
		}
	}
}
