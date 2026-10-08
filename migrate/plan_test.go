package migrate

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gmgigi96/cernbox-sync/config"
)

const spacesPath = "/remote.php/dav/spaces/"

// fakeServer serves the Graph API and WebDAV like CERNBox: the drive alias
// of a space is its storage path, and the server puts it at the end of the
// space's WebDAV URL.
type fakeServer struct {
	*httptest.Server
	tree     map[string]bool // decoded path → is a collection
	requests atomic.Int32
}

func newFakeServer(t *testing.T) *fakeServer {
	t.Helper()
	s := &fakeServer{tree: map[string]bool{}}
	home, project := spacesPath+"eoshome-e$1", spacesPath+"eosproject-c$2"
	for _, d := range []string{home, home + "/docs", home + "/big", home + "/My Stuff", home + "/My Stuff/keep",
		home + "/My Stuff/skip me", home + "/.config", project, project + "/Projects", project + "/Projects/2026 Heterogeneous Spaces"} {
		s.tree[d] = true
	}
	s.tree[home+"/top.txt"] = false
	s.tree[home+"/My Stuff/z.txt"] = false
	s.Server = httptest.NewServer(s)
	t.Cleanup(s.Close)
	return s
}

func (s *fakeServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.requests.Add(1)
	if user, pass, _ := r.BasicAuth(); user != "einstein" || pass != "relativity" {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	if r.URL.Path == "/graph/v1beta1/me/drives" {
		drive := func(id, name, typ, alias string) map[string]any {
			return map[string]any{"id": id, "name": name, "driveType": typ, "driveAlias": alias,
				"root": map[string]any{"webDavUrl": s.URL + "/remote.php/dav/spaces/" + alias}}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"value": []any{
			drive("eoshome-e$1", "einstein", "personal", "eos/user/e/einstein"),
			drive("eosproject-c$2", "cernbox", "project", "eos/project/c/cernbox"),
		}})
		return
	}
	p := strings.TrimSuffix(r.URL.Path, "/")
	isDir, ok := s.tree[p]
	if r.Method != "PROPFIND" || !ok {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	paths := []string{p}
	if isDir && r.Header.Get("Depth") == "1" {
		for c := range s.tree {
			if strings.HasPrefix(c, p+"/") && !strings.Contains(c[len(p)+1:], "/") {
				paths = append(paths, c)
			}
		}
		slices.Sort(paths[1:])
	}
	w.WriteHeader(http.StatusMultiStatus)
	_, _ = fmt.Fprint(w, `<?xml version="1.0"?><d:multistatus xmlns:d="DAV:">`)
	for _, c := range paths {
		var href, resType string
		for seg := range strings.SplitSeq(strings.TrimPrefix(c, "/"), "/") {
			href += "/" + url.PathEscape(seg)
		}
		if s.tree[c] {
			href, resType = href+"/", "<d:collection/>"
		}
		_, _ = fmt.Fprintf(w, `<d:response><d:href>%s</d:href><d:propstat><d:prop><d:getetag>"e"</d:getetag><d:resourcetype>%s</d:resourcetype></d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response>`, href, resType)
	}
	_, _ = fmt.Fprint(w, `</d:multistatus>`)
}

func (s *fakeServer) planner(t *testing.T, existing ...config.Folder) *Planner {
	t.Helper()
	p, err := NewPlanner(s.URL, "einstein", "relativity", existing)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func (s *fakeServer) account() Account {
	return Account{ID: "0", ServerURL: s.URL + "/cernbox/desktop/", Username: "einstein"}
}

func legacyFolder(s *fakeServer, target string) Folder {
	return Folder{
		ID:                "f1",
		DisplayName:       "home",
		LocalPath:         "/home/u/cernbox/",
		DavURL:            s.URL + "/cernbox/desktop/remote.php/dav/files/einstein/",
		TargetPath:        target,
		IgnoreHiddenFiles: true,
		Excluded:          []string{},
		JournalPath:       "/home/u/cernbox/.sync_journal.db",
	}
}

func TestPlan_StoragePathsMapToSpaces(t *testing.T) {
	s := newFakeServer(t)
	plan := s.planner(t).Plan(s.account(), legacyFolder(s, "/eos/project/c/cernbox/Projects/2026 Heterogeneous Spaces"))
	want := Plan{
		Ready:      true,
		RemoteBase: s.URL + spacesPath + "eosproject-c$2/Projects/2026%20Heterogeneous%20Spaces",
		Location:   "cernbox / Projects/2026 Heterogeneous Spaces",
	}
	if fmt.Sprint(plan) != fmt.Sprint(want) {
		t.Errorf("plan = %+v, want %+v", plan, want)
	}

	plan = s.planner(t).Plan(s.account(), legacyFolder(s, "/eos/user/e/einstein/docs"))
	if plan.RemoteBase != s.URL+spacesPath+"eoshome-e$1/docs" {
		t.Errorf("plan = %+v", plan)
	}
}

func TestPlan_HomeWithExclusions(t *testing.T) {
	s := newFakeServer(t)
	f := legacyFolder(s, "/home")
	f.Excluded = []string{"big", "My Stuff/skip me"}

	plan := s.planner(t).Plan(s.account(), f)
	if !plan.Ready || plan.RemoteBase != s.URL+spacesPath+"eoshome-e$1" || plan.Location != "einstein" {
		t.Fatalf("plan = %+v", plan)
	}
	// Selections are stored URL-encoded, like the GUI's folder picker does.
	if want := []string{"My%20Stuff/keep", "docs"}; !slices.Equal(plan.Folders, want) {
		t.Errorf("folders = %q, want %q", plan.Folders, want)
	}
	if want := []string{"My Stuff/z.txt", "top.txt"}; !slices.Equal(plan.UnsyncedFiles, want) {
		t.Errorf("unsynced = %q, want %q", plan.UnsyncedFiles, want)
	}

	f.IgnoreHiddenFiles = false
	if plan := s.planner(t).Plan(s.account(), f); !slices.Contains(plan.Folders, ".config") {
		t.Errorf("hidden folders are selected when hidden files are synced: %q", plan.Folders)
	}
}

func TestPlan_SpacesAwareClient(t *testing.T) {
	s := newFakeServer(t)
	f := legacyFolder(s, "/")
	f.DavURL = s.URL + "/dav/spaces/eosproject-c%242"
	if plan := s.planner(t).Plan(s.account(), f); plan.RemoteBase != s.URL+spacesPath+"eosproject-c$2" || plan.Location != "cernbox" {
		t.Errorf("plan = %+v", plan)
	}
}

func TestPlan_ProbesCandidatesInOrder(t *testing.T) {
	s := newFakeServer(t)
	p := s.planner(t)
	var urls []string
	for _, c := range p.candidates(s.account(), legacyFolder(s, "/Documents")) {
		urls = append(urls, c.url)
	}
	// ownCloud servers: the files root is the personal space.
	want := []string{s.URL + spacesPath + "eoshome-e$1/Documents", s.URL + "/remote.php/dav/files/einstein/Documents"}
	if !slices.Equal(urls, want) {
		t.Errorf("candidates = %q, want %q", urls, want)
	}
}

func TestPlan_BlockedFolders(t *testing.T) {
	s := newFakeServer(t)
	p := s.planner(t, config.Folder{Name: "Home", LocalRoot: "/home/u/other", RemoteBase: s.URL + spacesPath + "eoshome-e$1"})

	for _, tc := range []struct {
		name    string
		account func(*Account)
		folder  func(*Folder)
		reason  string
		offline bool // decided without asking the server
	}{
		{"another server", func(a *Account) { a.ServerURL = "https://other.example/" }, nil, "Synced with other.example", true},
		{"virtual files", nil, func(f *Folder) { f.VirtualFiles = true }, "virtual files", true},
		{"a running client", nil, func(f *Folder) { f.InUse = true }, "Quit it", true},
		{"a missing journal", nil, func(f *Folder) { f.JournalPath = "" }, "every local file would be uploaded again", true},
		{"an unreadable journal", nil, func(f *Folder) { f.JournalError = "boom" }, "cannot be read (boom)", true},
		{"the same local folder", nil, func(f *Folder) { f.LocalPath = "/home/u/other/" }, `Already synced as "Home"`, true},
		{"the same remote folder", nil, nil, `Already synced as "Home"`, false},
		{"a missing remote folder", nil, func(f *Folder) { f.TargetPath = "/home/gone" }, "/home/gone was not found", false},
		{"everything excluded", nil, func(f *Folder) {
			f.TargetPath = "/eos/project/c/cernbox"
			f.Excluded = []string{"Projects"}
		}, "All of its sub-folders are excluded", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, f := s.account(), legacyFolder(s, "/home")
			if tc.account != nil {
				tc.account(&a)
			}
			if tc.folder != nil {
				tc.folder(&f)
			}
			before := s.requests.Load()
			plan := p.Plan(a, f)
			if plan.Ready || !strings.Contains(plan.Reason, tc.reason) {
				t.Errorf("plan = %+v, want reason %q", plan, tc.reason)
			}
			if tc.offline && s.requests.Load() != before {
				t.Error("the server should not be contacted")
			}
		})
	}

	// A folder without journal is fine when nothing is stored locally.
	f := legacyFolder(s, "/eos/project/c/cernbox/Projects")
	f.JournalPath, f.LocalIsEmpty = "", true
	if plan := p.Plan(s.account(), f); !plan.Ready {
		t.Errorf("plan = %+v", plan)
	}
}

func TestNewPlanner_RejectedCredentials(t *testing.T) {
	s := newFakeServer(t)
	if _, err := NewPlanner(s.URL, "einstein", "wrong", nil); err == nil || !strings.Contains(err.Error(), "401") {
		t.Errorf("err = %v", err)
	}
}

func TestSpaceURL(t *testing.T) {
	for _, tc := range []struct{ raw, alias, id, want string }{
		{"https://h/remote.php/dav/spaces/eos/user/g/gdelmont", "eos/user/g/gdelmont", "eoshome-g$X", "https://h/remote.php/dav/spaces/eoshome-g$X"},
		{"https://h/remote.php/dav/spaces/abc-123", "", "abc-123", "https://h/remote.php/dav/spaces/abc-123"},
		{"https://h/dav/spaces/other/", "personal/einstein", "abc", "https://h/dav/spaces/other/abc"},
	} {
		if got := spaceURL(tc.raw, tc.alias, tc.id); got != tc.want {
			t.Errorf("spaceURL(%q) = %q, want %q", tc.raw, got, tc.want)
		}
	}
}

func TestUniqueName(t *testing.T) {
	if got := UniqueName("home", map[string]bool{}); got != "home" {
		t.Errorf("got %q", got)
	}
	if got := UniqueName("home", map[string]bool{"home": true, "home (2)": true}); got != "home (3)" {
		t.Errorf("got %q", got)
	}
}
