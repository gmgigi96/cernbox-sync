package migrate

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/gmgigi96/cernbox-sync/config"
	"github.com/gmgigi96/cernbox-sync/version"
	"github.com/gmgigi96/cernbox-sync/webdav"
)

// Plan says whether and how a desktop-client folder can be imported.
type Plan struct {
	Ready bool `json:"ready"`
	// Reason explains why the folder cannot be imported.
	Reason string `json:"reason,omitempty"`
	// RemoteBase is the WebDAV URL of the remote folder.
	RemoteBase string `json:"remote_base,omitempty"`
	// Location describes it for people, e.g. "einstein / Projects/x".
	Location string `json:"location,omitempty"`
	// Folders are the sub-folders to sync, relative and URL-encoded like the
	// server's hrefs (as the folder picker of the GUI stores them); empty
	// means everything.
	Folders []string `json:"folders,omitempty"`
	// UnsyncedFiles are the plain paths of the files next to excluded
	// folders, which a selection of sub-folders cannot include.
	UnsyncedFiles []string `json:"unsynced_files,omitempty"`
}

type space struct {
	id, name, driveType, alias, webDAVURL string
}

// Planner decides whether and where the folders of a desktop client can be
// synced on the server this app uses, with this app's account.
type Planner struct {
	serverURL, username, password string
	spaces                        []space
	existing                      []config.Folder
}

var httpClient = &http.Client{Timeout: 30 * time.Second}

// NewPlanner lists the user's spaces on serverURL. existing are the folders
// this app already syncs.
func NewPlanner(serverURL, username, password string, existing []config.Folder) (*Planner, error) {
	spaces, err := listSpaces(serverURL, username, password)
	if err != nil {
		return nil, err
	}
	return &Planner{serverURL: serverURL, username: username, password: password, spaces: spaces, existing: existing}, nil
}

// listSpaces queries the Graph API for the spaces of the user.
func listSpaces(serverURL, username, password string) ([]space, error) {
	req, err := http.NewRequest(http.MethodGet, strings.TrimRight(serverURL, "/")+"/graph/v1beta1/me/drives", nil)
	if err != nil {
		return nil, fmt.Errorf("list spaces: %w", err)
	}
	req.SetBasicAuth(username, password)
	// The account holds an app password, accepted only from the sync client.
	req.Header.Set("User-Agent", version.UserAgent())
	req.Header.Set("Accept", "application/json")
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("list spaces: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("list spaces: status %d: %s", resp.StatusCode, b)
	}
	var drives struct {
		Value []struct {
			ID         string `json:"id"`
			Name       string `json:"name"`
			DriveType  string `json:"driveType"`
			DriveAlias string `json:"driveAlias"`
			Root       struct {
				WebDavURL string `json:"webDavUrl"`
			} `json:"root"`
		} `json:"value"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&drives); err != nil {
		return nil, fmt.Errorf("list spaces: %w", err)
	}
	spaces := make([]space, 0, len(drives.Value))
	for _, d := range drives.Value {
		spaces = append(spaces, space{
			id:        d.ID,
			name:      d.Name,
			driveType: d.DriveType,
			alias:     d.DriveAlias,
			webDAVURL: spaceURL(d.Root.WebDavURL, d.DriveAlias, d.ID),
		})
	}
	return spaces, nil
}

// spaceURL builds the stable WebDAV URL of a space like buildWebDavUrl in
// gui/src/graph.ts does, so that imported folders get the same RemoteBase as
// the ones added from the space picker: when the server puts the alias at the
// end of the URL, it is replaced with the space id.
func spaceURL(raw, alias, id string) string {
	if alias == "" || raw == "" {
		return raw
	}
	base, ok := strings.CutSuffix(raw, "/"+strings.TrimSuffix(alias, "/"))
	if !ok {
		base = strings.TrimSuffix(raw, "/")
	}
	return base + "/" + id
}

// Plan decides whether and how folder f of account a can be imported.
func (p *Planner) Plan(a Account, f Folder) Plan {
	blocked := func(format string, args ...any) Plan { return Plan{Reason: fmt.Sprintf(format, args...)} }
	serverHost := hostOf(p.serverURL)

	switch {
	case hostOf(a.ServerURL) != serverHost:
		return blocked("Synced with %s, not %s.", hostOf(a.ServerURL), serverHost)
	case f.VirtualFiles:
		return blocked("Uses virtual files (downloaded on demand), which CERNBox Sync does not support. Add it from Folders to download it fully.")
	case f.InUse:
		return blocked("In use by the desktop client. Quit it, then check again.")
	case !f.LocalIsEmpty && f.JournalError != "":
		return blocked("Its sync database cannot be read (%s).", f.JournalError)
	case !f.LocalIsEmpty && f.JournalPath == "":
		return blocked("Its sync database is missing, so every local file would be uploaded again.")
	}
	for _, e := range p.existing {
		if samePath(e.LocalRoot, f.LocalPath) {
			return blocked("Already synced as %s.", strconv.Quote(e.Name))
		}
	}

	var found *candidate
	for _, c := range p.candidates(a, f) {
		ok, err := p.isCollection(c.url)
		if err != nil {
			return blocked("%v", err)
		}
		if ok {
			found = &c
			break
		}
	}
	if found == nil {
		return blocked("%s was not found on %s.", "/"+strings.Trim(f.TargetPath, "/"), serverHost)
	}
	for _, e := range p.existing {
		if e.RemoteBase == found.url {
			return blocked("Already synced as %s.", strconv.Quote(e.Name))
		}
	}

	plan := Plan{Ready: true, RemoteBase: found.url, Location: found.label}
	if len(f.Excluded) > 0 {
		folders, unsynced, err := selectionExcluding(webdav.NewClient(found.url, p.username, p.password), f.Excluded, !f.IgnoreHiddenFiles)
		if err != nil {
			return blocked("%v", err)
		}
		// An empty selection would mean "sync everything".
		if len(folders) == 0 {
			return blocked("All of its sub-folders are excluded from sync.")
		}
		plan.Folders, plan.UnsyncedFiles = folders, unsynced
	}
	return plan
}

func samePath(a, b string) bool {
	return filepath.Clean(filepath.FromSlash(a)) == filepath.Clean(filepath.FromSlash(b))
}

type candidate struct {
	url   string
	label string
}

// candidates lists the URLs where the remote folder of f may be on the
// server, most likely first.
//
// Folders of spaces-aware clients point at a space (…/dav/spaces/<id>).
// Older setups, and CERNBox, address paths in the user's files instead: on
// CERNBox spaces appear there under their storage path (the drive alias, e.g.
// /eos/project/c/cernbox) and the personal space also as /home; on ownCloud
// servers the files root is the personal space.
func (p *Planner) candidates(a Account, f Folder) []candidate {
	target := strings.Trim(f.TargetPath, "/")
	var out []candidate
	add := func(base, rel, name string) {
		rel = strings.Trim(rel, "/")
		c := candidate{url: joinDavPath(base, rel), label: name}
		if rel != "" {
			c.label = name + " / " + rel
		}
		if !slices.ContainsFunc(out, func(o candidate) bool { return o.url == c.url }) {
			out = append(out, c)
		}
	}

	davPath := ""
	if u, err := url.Parse(f.DavURL); err == nil {
		davPath = u.EscapedPath()
	}
	if _, rest, ok := strings.Cut(davPath, "/dav/spaces/"); ok {
		id := f.SpaceID
		if id == "" {
			seg, _, _ := strings.Cut(rest, "/")
			if id, _ = url.PathUnescape(seg); id == "" {
				id = seg
			}
		}
		for _, s := range p.spaces {
			if s.id == id {
				add(s.webDAVURL, target, s.name)
			}
		}
		if hostOf(f.DavURL) == hostOf(p.serverURL) {
			add(f.DavURL, target, "/"+target)
		}
		return out
	}

	abs := "/" + target
	type mount struct {
		s    space
		path string
	}
	var mounts []mount
	for _, s := range p.spaces {
		if s.alias != "" {
			mounts = append(mounts, mount{s, "/" + strings.Trim(s.alias, "/")})
		}
		if s.driveType == "personal" {
			mounts = append(mounts, mount{s, "/home"})
		}
	}
	mounts = slices.DeleteFunc(mounts, func(m mount) bool { return abs != m.path && !strings.HasPrefix(abs, m.path+"/") })
	slices.SortStableFunc(mounts, func(x, y mount) int { return len(y.path) - len(x.path) })
	for _, m := range mounts {
		add(m.s.webDAVURL, abs[len(m.path):], m.s.name)
	}
	for _, s := range p.spaces {
		if s.driveType == "personal" {
			add(s.webDAVURL, target, s.name)
		}
	}
	add(strings.TrimRight(p.serverURL, "/")+"/remote.php/dav/files/"+url.PathEscape(a.Username), target, abs)
	return out
}

// joinDavPath appends a plain relative path to a WebDAV URL, encoding each
// segment.
func joinDavPath(base, rel string) string {
	var out strings.Builder
	out.WriteString(strings.TrimRight(base, "/"))
	for seg := range strings.SplitSeq(strings.Trim(rel, "/"), "/") {
		if seg != "" {
			out.WriteString("/" + url.PathEscape(seg))
		}
	}
	return out.String()
}

// isCollection reports whether u is an existing collection. Missing or
// inaccessible resources yield false; other failures are errors.
func (p *Planner) isCollection(u string) (bool, error) {
	res, err := webdav.NewClient(u, p.username, p.password).Propfind("", 0)
	if se, ok := errors.AsType[*webdav.StatusError](err); ok {
		switch se.Code {
		case http.StatusBadRequest, http.StatusForbidden, http.StatusNotFound:
			return false, nil
		case http.StatusUnauthorized:
			return false, errors.New("the server rejected the credentials of this app")
		}
	}
	if err != nil {
		return false, err
	}
	return len(res) > 0 && res[0].IsDir, nil
}

// selectionExcluding turns an exclusion list (sync everything except…) into
// the equivalent selection of sub-folders, by walking the remote tree down to
// each excluded folder. Files next to the walked folders cannot be selected:
// they are returned as unsynced.
func selectionExcluding(c *webdav.Client, excluded []string, includeHidden bool) (folders, unsynced []string, err error) {
	excl := map[string]bool{}
	for _, e := range excluded {
		excl[strings.Trim(e, "/")] = true
	}
	leadsToExcluded := func(dir string) bool {
		for e := range excl {
			if strings.HasPrefix(e, dir+"/") {
				return true
			}
		}
		return false
	}

	var visit func(encodedDir, plainDir string) error
	visit = func(encodedDir, plainDir string) error {
		children, err := c.Propfind(encodedDir, 1)
		if err != nil {
			return err
		}
		for _, child := range children {
			if child.Path == plainDir {
				continue // the listed folder itself
			}
			name := child.Path[strings.LastIndex(child.Path, "/")+1:]
			if name == "" || (!includeHidden && strings.HasPrefix(name, ".")) {
				continue
			}
			href := strings.TrimRight(child.Href, "/")
			encoded := href[strings.LastIndex(href, "/")+1:]
			if encodedDir != "" {
				encoded = encodedDir + "/" + encoded
			}
			switch {
			case !child.IsDir:
				unsynced = append(unsynced, child.Path)
			case excl[child.Path]:
			case leadsToExcluded(child.Path):
				if err := visit(encoded, child.Path); err != nil {
					return err
				}
			default:
				folders = append(folders, encoded)
			}
		}
		return nil
	}
	err = visit("", "")
	return folders, unsynced, err
}

// UniqueName returns base, or "base (2)", "base (3)"… when base is taken.
func UniqueName(base string, taken map[string]bool) string {
	if !taken[base] {
		return base
	}
	i := 2
	for taken[fmt.Sprintf("%s (%d)", base, i)] {
		i++
	}
	return fmt.Sprintf("%s (%d)", base, i)
}
