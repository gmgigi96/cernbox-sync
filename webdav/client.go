package webdav

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
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
	ctx      context.Context // nil means context.Background()

	maxRetries int
	baseDelay  time.Duration
}

const (
	defaultMaxRetries = 3
	defaultBaseDelay  = time.Second
	maxRetryDelay     = 5 * time.Minute
)

// NewClient creates a WebDAV client.
// base must NOT have a trailing slash.
//
// There is no overall request timeout (transfers of large files may take
// arbitrarily long); only connection establishment and the wait for response
// headers are bounded.
func NewClient(base, username, password string) *Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.DialContext = (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).DialContext
	tr.TLSHandshakeTimeout = 30 * time.Second
	tr.ResponseHeaderTimeout = 120 * time.Second
	tr.IdleConnTimeout = 90 * time.Second
	return &Client{
		base:       strings.TrimRight(base, "/"),
		username:   username,
		password:   password,
		hc:         &http.Client{Transport: tr},
		maxRetries: defaultMaxRetries,
		baseDelay:  defaultBaseDelay,
	}
}

// WithContext returns a shallow copy of the client whose requests (and retry
// waits) are aborted when ctx is cancelled.
func (c *Client) WithContext(ctx context.Context) *Client {
	cp := *c
	cp.ctx = ctx
	return &cp
}

func (c *Client) newRequest(method, u string, body io.Reader) (*http.Request, error) {
	ctx := c.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	return http.NewRequestWithContext(ctx, method, u, body)
}

// SetRetryPolicy sets how many times retryable requests are retried (a request
// is attempted once plus maxRetries more times) and the base delay of the
// exponential backoff.
func (c *Client) SetRetryPolicy(maxRetries int, baseDelay time.Duration) {
	c.maxRetries = max(maxRetries, 0)
	c.baseDelay = baseDelay
}

// retryableStatus reports whether a response status is worth retrying.
func retryableStatus(code int) bool {
	switch code {
	case http.StatusTooManyRequests, http.StatusBadGateway,
		http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	}
	return false
}

// retryAfter parses a Retry-After header (seconds or HTTP-date).
func retryAfter(h string) (time.Duration, bool) {
	h = strings.TrimSpace(h)
	if h == "" {
		return 0, false
	}
	if secs, err := strconv.Atoi(h); err == nil {
		return time.Duration(max(secs, 0)) * time.Second, true
	}
	if t, err := http.ParseTime(h); err == nil {
		return max(time.Until(t), 0), true
	}
	return 0, false
}

// do executes a request with basic auth, retrying transient failures
// (transport errors and 429/502/503/504) with exponential backoff. A request
// whose body cannot be rewound is never resent.
func (c *Client) do(req *http.Request) (*http.Response, error) {
	req.SetBasicAuth(c.username, c.password)
	rewindable := req.Body == nil || req.Body == http.NoBody || req.GetBody != nil

	for attempt := 0; ; attempt++ {
		r := req
		if attempt > 0 && req.GetBody != nil {
			r = req.Clone(req.Context())
			body, err := req.GetBody()
			if err != nil {
				return nil, err
			}
			r.Body = body
		}
		resp, err := c.hc.Do(r)

		canRetry := rewindable && attempt < c.maxRetries && req.Context().Err() == nil
		var wait time.Duration
		switch {
		case err != nil:
			if !canRetry {
				return nil, err
			}
		case retryableStatus(resp.StatusCode):
			if !canRetry {
				return resp, nil
			}
			if d, ok := retryAfter(resp.Header.Get("Retry-After")); ok {
				wait = d
			}
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
			_ = resp.Body.Close()
		default:
			return resp, nil
		}

		if wait == 0 {
			wait = c.baseDelay << min(attempt, 20)
		}
		wait = min(wait, maxRetryDelay)
		select {
		case <-time.After(wait):
		case <-req.Context().Done():
			return nil, req.Context().Err()
		}
	}
}

// statusErr builds a StatusError from an unexpected response.
func statusErr(method, path string, resp *http.Response) *StatusError {
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	return &StatusError{Method: method, Path: path, Code: resp.StatusCode, Body: string(b)}
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

	req, err := c.newRequest("PROPFIND", u, strings.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("propfind build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/xml")
	req.Header.Set("Depth", fmt.Sprintf("%d", depth))

	resp, err := c.do(req)
	if err != nil {
		return nil, fmt.Errorf("propfind request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusMultiStatus {
		return nil, statusErr("PROPFIND", remotePath, resp)
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

// ErrUnauthorized matches (via errors.Is) a StatusError with code 401.
var ErrUnauthorized = errors.New("webdav: unauthorized")

// StatusError is returned when the server answers with an unexpected status.
type StatusError struct {
	Method string
	Path   string
	Code   int
	Body   string
}

// Is reports whether e matches target; a 401 matches ErrUnauthorized.
func (e *StatusError) Is(target error) bool {
	return target == ErrUnauthorized && e.Code == http.StatusUnauthorized
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("%s %s: status %d: %s", e.Method, e.Path, e.Code, e.Body)
}

// GetInfo carries the response headers of a GET that matter for sync.
type GetInfo struct {
	// ETag is the entity tag of the content actually served (quotes stripped),
	// empty when the server did not send one.
	ETag string
	// ContentLength is the announced body length, -1 when unknown.
	ContentLength int64
	// LastModified is the Last-Modified header, zero when absent.
	LastModified time.Time
}

// Get downloads a remote file and returns its content.
func (c *Client) Get(remotePath string) (io.ReadCloser, error) {
	rc, _, err := c.GetWithInfo(remotePath)
	return rc, err
}

// GetWithInfo downloads a remote file and also returns the validators of the
// content that is being served, which may be newer than a previous PROPFIND.
func (c *Client) GetWithInfo(remotePath string) (io.ReadCloser, GetInfo, error) {
	u := c.url(remotePath)
	req, err := c.newRequest("GET", u, nil)
	if err != nil {
		return nil, GetInfo{}, err
	}
	resp, err := c.do(req)
	if err != nil {
		return nil, GetInfo{}, err
	}
	if resp.StatusCode != http.StatusOK {
		defer func() { _ = resp.Body.Close() }()
		return nil, GetInfo{}, statusErr("GET", remotePath, resp)
	}
	info := GetInfo{
		ETag:          strings.Trim(strings.TrimPrefix(resp.Header.Get("ETag"), "W/"), `"`),
		ContentLength: resp.ContentLength,
	}
	if t, err := http.ParseTime(resp.Header.Get("Last-Modified")); err == nil {
		info.LastModified = t
	}
	return resp.Body, info, nil
}

// Put uploads content to remotePath.
func (c *Client) Put(remotePath string, content io.Reader, size int64) error {
	u := c.url(remotePath)
	req, err := c.newRequest("PUT", u, content)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	if size >= 0 {
		req.ContentLength = size
	}
	resp, err := c.do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusNoContent {
		return statusErr("PUT", remotePath, resp)
	}
	return nil
}

// PutFile uploads the local file to remotePath. The file is reopened for each
// attempt so the upload can be retried; wrap (optional) decorates the reader
// on every attempt, e.g. for rate limiting or progress reporting.
func (c *Client) PutFile(remotePath, localPath string, wrap func(io.Reader) io.Reader) error {
	fi, err := os.Stat(localPath)
	if err != nil {
		return err
	}
	open := func() (io.ReadCloser, error) {
		f, err := os.Open(localPath)
		if err != nil {
			return nil, err
		}
		var r io.Reader = f
		if wrap != nil {
			r = wrap(f)
		}
		return struct {
			io.Reader
			io.Closer
		}{r, f}, nil
	}
	body, err := open()
	if err != nil {
		return err
	}
	req, err := c.newRequest("PUT", c.url(remotePath), body)
	if err != nil {
		_ = body.Close()
		return err
	}
	req.GetBody = open
	req.ContentLength = fi.Size()
	if fi.Size() == 0 {
		req.Body = http.NoBody
		req.GetBody = func() (io.ReadCloser, error) { return http.NoBody, nil }
		_ = body.Close()
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	resp, err := c.do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusNoContent {
		return statusErr("PUT", remotePath, resp)
	}
	return nil
}

// Mkcol creates a remote collection (directory).
func (c *Client) Mkcol(remotePath string) error {
	u := c.url(remotePath)
	req, err := c.newRequest("MKCOL", u, nil)
	if err != nil {
		return err
	}
	resp, err := c.do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusMethodNotAllowed {
		return statusErr("MKCOL", remotePath, resp)
	}
	return nil
}

// Delete removes a remote resource.
func (c *Client) Delete(remotePath string) error {
	u := c.url(remotePath)
	req, err := c.newRequest("DELETE", u, nil)
	if err != nil {
		return err
	}
	resp, err := c.do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNotFound {
		return statusErr("DELETE", remotePath, resp)
	}
	return nil
}

// Move renames src to dst on the server. With overwrite false an existing dst
// makes the server answer 412.
func (c *Client) Move(src, dst string, overwrite bool) error {
	req, err := c.newRequest("MOVE", c.url(src), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Destination", c.url(dst))
	if overwrite {
		req.Header.Set("Overwrite", "T")
	} else {
		req.Header.Set("Overwrite", "F")
	}
	resp, err := c.do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusNoContent {
		return statusErr("MOVE", src, resp)
	}
	return nil
}

// url builds the full URL for a remote path relative to base, escaping each
// path segment.
func (c *Client) url(remotePath string) string {
	remotePath = strings.Trim(remotePath, "/")
	if remotePath == "" {
		return c.base
	}
	segs := strings.Split(remotePath, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return c.base + "/" + strings.Join(segs, "/")
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
