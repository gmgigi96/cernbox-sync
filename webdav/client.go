package webdav

import (
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const httpTimeFormat = "Mon, 02 Jan 2006 15:04:05 GMT"

// Client is a thin WebDAV HTTP client with basic-auth.
type Client struct {
	base     string // e.g. https://cernbox.cern.ch/remote.php/dav/spaces/eoshome-g$…/Documents
	username string
	password string
	hc       *http.Client
}

// NewClient creates a WebDAV client.
// base must NOT have a trailing slash.
func NewClient(base, username, password string) *Client {
	return &Client{
		base:     strings.TrimRight(base, "/"),
		username: username,
		password: password,
		hc:       &http.Client{Timeout: 60 * time.Second},
	}
}

// do executes a request with basic auth.
func (c *Client) do(req *http.Request) (*http.Response, error) {
	req.SetBasicAuth(c.username, c.password)
	return c.hc.Do(req)
}

// Propfind fetches properties for remotePath (relative to base) at the given depth (0 or 1).
// Pass "" for the base itself.
func (c *Client) Propfind(remotePath string, depth int) ([]Resource, error) {
	u := c.url(remotePath)
	body := `<?xml version="1.0" encoding="utf-8"?>
<d:propfind xmlns:d="DAV:" xmlns:oc="http://owncloud.org/ns">
  <d:prop>
    <oc:name/>
    <oc:fileid/>
    <oc:permissions/>
    <oc:size/>
    <d:getetag/>
    <d:getlastmodified/>
    <d:getcontentlength/>
    <d:getcontenttype/>
    <d:resourcetype/>
  </d:prop>
</d:propfind>`

	req, err := http.NewRequest("PROPFIND", u, strings.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("propfind build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/xml")
	req.Header.Set("Depth", fmt.Sprintf("%d", depth))

	resp, err := c.do(req)
	if err != nil {
		return nil, fmt.Errorf("propfind request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusMultiStatus {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("propfind: unexpected status %d: %s", resp.StatusCode, string(b))
	}

	var ms Multistatus
	if err := xml.NewDecoder(resp.Body).Decode(&ms); err != nil {
		return nil, fmt.Errorf("propfind decode: %w", err)
	}

	resources := make([]Resource, 0, len(ms.Responses))
	for _, r := range ms.Responses {
		res, err := parseResponse(r, c.base)
		if err != nil {
			return nil, err
		}
		resources = append(resources, res)
	}
	return resources, nil
}

// Get downloads a remote file and returns its content.
func (c *Client) Get(remotePath string) (io.ReadCloser, error) {
	u := c.url(remotePath)
	req, err := http.NewRequest("GET", u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("GET %s: status %d", remotePath, resp.StatusCode)
	}
	return resp.Body, nil
}

// Put uploads content to remotePath.
func (c *Client) Put(remotePath string, content io.Reader, size int64) error {
	u := c.url(remotePath)
	req, err := http.NewRequest("PUT", u, content)
	if err != nil {
		return err
	}
	if size >= 0 {
		req.ContentLength = size
	}
	resp, err := c.do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusNoContent {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("PUT %s: status %d: %s", remotePath, resp.StatusCode, string(b))
	}
	return nil
}

// Mkcol creates a remote collection (directory).
func (c *Client) Mkcol(remotePath string) error {
	u := c.url(remotePath)
	req, err := http.NewRequest("MKCOL", u, nil)
	if err != nil {
		return err
	}
	resp, err := c.do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusMethodNotAllowed {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("MKCOL %s: status %d: %s", remotePath, resp.StatusCode, string(b))
	}
	return nil
}

// Delete removes a remote resource.
func (c *Client) Delete(remotePath string) error {
	u := c.url(remotePath)
	req, err := http.NewRequest("DELETE", u, nil)
	if err != nil {
		return err
	}
	resp, err := c.do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNotFound {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("DELETE %s: status %d: %s", remotePath, resp.StatusCode, string(b))
	}
	return nil
}

// url builds the full URL for a remote path relative to base.
func (c *Client) url(remotePath string) string {
	if remotePath == "" || remotePath == "/" {
		return c.base
	}
	// Ensure single slash separator.
	return c.base + "/" + strings.TrimLeft(remotePath, "/")
}

// parseResponse converts a raw PROPFIND response entry into a Resource.
func parseResponse(r Response, base string) (Resource, error) {
	// Decode the href to get a plain path.
	decoded, err := url.PathUnescape(r.Href)
	if err != nil {
		decoded = r.Href
	}

	var res Resource
	res.Href = r.Href

	// Extract the relative path by stripping everything up to and including the base path.
	// The base is a full URL; we need only its path component.
	parsedBase, err := url.Parse(base)
	if err != nil {
		return res, fmt.Errorf("parse base URL: %w", err)
	}
	basePath := strings.TrimRight(parsedBase.Path, "/")

	relPath := decoded
	if idx := strings.Index(relPath, basePath); idx != -1 {
		relPath = relPath[idx+len(basePath):]
	}
	relPath = strings.TrimLeft(relPath, "/")
	relPath = strings.TrimRight(relPath, "/")
	res.Path = relPath

	// Pick up the 200-OK propstat.
	for _, ps := range r.Propstat {
		if !strings.Contains(ps.Status, "200") {
			continue
		}
		p := ps.Props
		res.ETag = strings.Trim(p.ETag, `"`)
		res.Size = p.Size
		if res.Size == 0 {
			res.Size = p.ContentLength
		}
		res.FileID = p.FileID
		res.Permissions = p.Permissions
		res.IsDir = p.ResourceType.Collection != nil

		if p.LastModifiedRaw != "" {
			t, err := time.Parse(httpTimeFormat, p.LastModifiedRaw)
			if err == nil {
				res.LastModified = t
			}
		}
	}
	return res, nil
}
