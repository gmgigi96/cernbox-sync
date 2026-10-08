// Package migrate takes over the sync folders configured in the ownCloud
// desktop client (including its CERNBox branding), so that users moving to
// cernbox-sync do not have to set everything up again.
//
// The old client is never modified: its configuration and journal databases
// are only read (no files are created next to them either), and its stored
// credentials are never read.
package migrate

import (
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
)

// Client is a configured ownCloud-based desktop client.
type Client struct {
	// AppName is the product name, e.g. "CERNBox".
	AppName    string `json:"app_name"`
	ConfigPath string `json:"config_path"`
	// Running reports whether the client appears to be running.
	Running bool `json:"running"`
	// Fixed upload/download limits in KB/s; 0 when none is set.
	UploadLimitKBps   int64     `json:"upload_limit_kbps,omitempty"`
	DownloadLimitKBps int64     `json:"download_limit_kbps,omitempty"`
	Accounts          []Account `json:"accounts"`
}

// Account is an account of a desktop client.
type Account struct {
	ID          string   `json:"id"`
	ServerURL   string   `json:"server_url"`
	Username    string   `json:"username"`
	DisplayName string   `json:"display_name"`
	Folders     []Folder `json:"folders"`
}

// Folder is a sync folder of a desktop-client account.
type Folder struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
	LocalPath   string `json:"local_path"`
	// DavURL is the WebDAV root TargetPath is relative to: either the user's
	// files (…/remote.php/dav/files/<user>/) or a space (…/dav/spaces/<id>).
	DavURL string `json:"dav_url"`
	// TargetPath is the plain (not URL-encoded) remote folder.
	TargetPath        string `json:"target_path"`
	SpaceID           string `json:"space_id,omitempty"`
	Paused            bool   `json:"paused"`
	IgnoreHiddenFiles bool   `json:"ignore_hidden_files"`
	// VirtualFiles is set when the folder uses on-demand (placeholder) files.
	VirtualFiles bool `json:"virtual_files"`
	// Excluded lists the plain sub-folder paths that are not synced.
	Excluded []string `json:"excluded"`
	// BaselineEntries is the number of synced items in the client's journal.
	BaselineEntries int    `json:"baseline_entries"`
	JournalPath     string `json:"journal_path,omitempty"`
	JournalError    string `json:"journal_error,omitempty"`
	// InUse is set when a running desktop client holds the journal.
	InUse bool `json:"in_use"`
	// LocalIsEmpty is set when the local folder is missing or holds no user files.
	LocalIsEmpty bool `json:"local_is_empty"`
	// Plan is filled in by Planner.Plan.
	Plan *Plan `json:"plan,omitempty"`
}

// FolderRef identifies a folder among detected clients.
type FolderRef struct {
	ConfigPath string `json:"config_path"`
	AccountID  string `json:"account_id"`
	FolderID   string `json:"folder_id"`
}

// knownClients are the builds of the ownCloud desktop client looked for:
// configuration directory names (APPLICATION_SHORTNAME since 2.9,
// APPLICATION_NAME before), configuration file and executable names.
var knownClients = []struct {
	name, file, exe string
	dirs            []string
}{
	{name: "CERNBox", dirs: []string{"cernbox", "CERNBox"}, file: "cernbox.cfg", exe: "cernbox"},
	{name: "ownCloud", dirs: []string{"ownCloud"}, file: "owncloud.cfg", exe: "owncloud"},
}

// folderGroups hold folder definitions under an account. Older releases
// wrote virtual-files and multi-folder setups to their own groups so that
// even older releases would not load them.
var folderGroups = []string{"Folders", "FoldersWithPlaceholders", "Multifolders"}

// Detect looks for the configured desktop clients of the current user that
// have sync folders.
func Detect() []Client {
	return detect(configBases(), runningProcesses())
}

func detect(bases, running []string) []Client {
	var found []Client
	seen := map[string]bool{}
	for _, base := range bases {
		for _, kc := range knownClients {
			for _, dir := range kc.dirs {
				path := filepath.Join(base, dir, kc.file)
				// Releases 2.9-4.2 left a symlink at the location they
				// migrated from, and case-insensitive file systems match
				// several spellings: read each file once.
				real, err := filepath.EvalSymlinks(path)
				if err != nil || seen[strings.ToLower(real)] {
					continue
				}
				text, err := os.ReadFile(real)
				if err != nil {
					continue
				}
				seen[strings.ToLower(real)] = true
				c := parseConfig(kc.name, path, parseSettings(string(text)))
				c.Running = slices.ContainsFunc(running, func(p string) bool { return isClientProcess(p, kc.exe) })
				for _, a := range c.Accounts {
					c.Running = c.Running || slices.ContainsFunc(a.Folders, func(f Folder) bool { return f.InUse })
				}
				if slices.ContainsFunc(c.Accounts, func(a Account) bool { return len(a.Folders) > 0 }) {
					found = append(found, c)
				}
			}
		}
	}
	return found
}

// configBases lists the directories that may hold the client's configuration
// directory: the current location (since 2.5) first, then the one of
// releases up to 2.4.
func configBases() []string {
	home, _ := os.UserHomeDir()
	config, _ := os.UserConfigDir()
	var bases []string
	switch runtime.GOOS {
	case "darwin":
		// config is ~/Library/Application Support.
		bases = []string{filepath.Join(home, "Library", "Preferences"), config}
	case "windows":
		// config is %APPDATA%.
		bases = []string{config, os.Getenv("LOCALAPPDATA")}
	default:
		// config is $XDG_CONFIG_HOME (~/.config).
		data := os.Getenv("XDG_DATA_HOME")
		if data == "" {
			data = filepath.Join(home, ".local", "share")
		}
		bases = []string{config, filepath.Join(data, "data")}
	}
	// Without a home directory the paths above are relative: skip them.
	return slices.DeleteFunc(bases, func(b string) bool { return !filepath.IsAbs(b) })
}

func parseConfig(appName, path string, s settings) Client {
	var ids []string
	for key := range s {
		rest, ok := strings.CutPrefix(key, "Accounts/")
		if !ok {
			continue
		}
		id, _, ok := strings.Cut(rest, "/")
		if ok && id != "" && strings.Trim(id, "0123456789") == "" && !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	c := Client{
		AppName:           appName,
		ConfigPath:        path,
		UploadLimitKBps:   bandwidthLimit(s, "useUploadLimit", "uploadLimit"),
		DownloadLimitKBps: bandwidthLimit(s, "useDownloadLimit", "downloadLimit"),
	}
	for _, id := range ids {
		c.Accounts = append(c.Accounts, parseAccount(s, id))
	}
	return c
}

// bandwidthLimit returns the limit in KB/s when a fixed one is set: the mode
// is positive for a fixed limit, 0 for none and negative for "automatic",
// which has no equivalent here.
func bandwidthLimit(s settings, modeKey, limitKey string) int64 {
	mode, err := strconv.ParseInt(strings.TrimSpace(s.str("BWLimit/"+modeKey)), 10, 64)
	if err != nil || mode < 1 {
		return 0
	}
	limit, err := strconv.ParseInt(strings.TrimSpace(s.str("BWLimit/"+limitKey)), 10, 64)
	if err != nil || limit < 1 {
		return 0
	}
	return limit
}

func parseAccount(s settings, id string) Account {
	prefix := "Accounts/" + id + "/"
	a := Account{ID: id, ServerURL: s[prefix+"url"].url()}
	// dav_user is the WebDAV user id; the others are login names.
	for _, k := range []string{"dav_user", "http_user", "user", "webflow_user"} {
		if a.Username = s.str(prefix + k); a.Username != "" {
			break
		}
	}
	a.DisplayName = s.str(prefix + "display-name")
	if a.DisplayName == "" {
		a.DisplayName = a.Username
	}

	for _, group := range folderGroups {
		groupPrefix := prefix + group + "/"
		var ids []string
		for key := range s {
			if rest, ok := strings.CutPrefix(key, groupPrefix); ok {
				if fid, _, ok := strings.Cut(rest, "/"); ok && !slices.Contains(ids, fid) {
					ids = append(ids, fid)
				}
			}
		}
		slices.Sort(ids)
		for _, fid := range ids {
			f := parseFolder(s, groupPrefix+fid+"/", fid, group == "FoldersWithPlaceholders", a)
			if f.LocalPath != "" {
				a.Folders = append(a.Folders, f)
			}
		}
	}
	return a
}

func parseFolder(s settings, prefix, id string, placeholders bool, a Account) Folder {
	f := Folder{
		ID:                id,
		DisplayName:       s.str(prefix + "displayString"),
		LocalPath:         s.str(prefix + "localPath"),
		DavURL:            s[prefix+"davUrl"].url(),
		TargetPath:        s.str(prefix + "targetPath"),
		SpaceID:           s.str(prefix + "spaceId"),
		Paused:            s.boolean(prefix+"paused", false),
		IgnoreHiddenFiles: s.boolean(prefix+"ignoreHiddenFiles", true),
		Excluded:          []string{},
	}
	if f.DavURL == "" {
		// Releases before davUrl was stored always used the user's files root.
		f.DavURL = strings.TrimRight(a.ServerURL, "/") + "/remote.php/dav/files/" + url.PathEscape(a.Username) + "/"
	}
	mode := s.str(prefix + "virtualFilesMode")
	f.VirtualFiles = placeholders || s.boolean(prefix+"usePlaceholders", false) || (mode != "" && mode != "off")
	if f.DisplayName == "" {
		f.DisplayName = filepath.Base(strings.TrimRight(f.TargetPath, "/"))
		if f.DisplayName == "." || f.DisplayName == "/" {
			f.DisplayName = id
		}
	}

	f.LocalIsEmpty = localIsEmpty(f.LocalPath)
	f.JournalPath = journalFile(f.LocalPath, s.str(prefix+"journalPath"))
	if f.JournalPath != "" {
		switch j, err := readJournal(f.JournalPath); {
		case errors.Is(err, errJournalLocked):
			f.InUse = true
		case err != nil:
			f.JournalError = err.Error()
		default:
			f.Excluded = j.excluded
			f.BaselineEntries = len(j.entries)
		}
	}
	return f
}

// ForServer keeps only the accounts with folders configured for serverURL's
// host, and the clients that have any.
func ForServer(clients []Client, serverURL string) []Client {
	host := hostOf(serverURL)
	var out []Client
	for _, c := range clients {
		c.Accounts = slices.DeleteFunc(slices.Clone(c.Accounts), func(a Account) bool {
			return len(a.Folders) == 0 || hostOf(a.ServerURL) != host
		})
		if len(c.Accounts) > 0 {
			out = append(out, c)
		}
	}
	return out
}

// Find returns the account and folder ref points to.
func Find(clients []Client, ref FolderRef) (Account, Folder, bool) {
	for _, c := range clients {
		if c.ConfigPath != ref.ConfigPath {
			continue
		}
		for _, a := range c.Accounts {
			if a.ID != ref.AccountID {
				continue
			}
			for _, f := range a.Folders {
				if f.ID == ref.FolderID {
					return a, f, true
				}
			}
		}
	}
	return Account{}, Folder{}, false
}

func hostOf(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Host)
}

func isClientProcess(process, exe string) bool {
	name := process[strings.LastIndexAny(process, `/\`)+1:]
	if strings.HasSuffix(strings.ToLower(name), ".exe") {
		name = name[:len(name)-len(".exe")]
	}
	return strings.EqualFold(name, exe)
}
