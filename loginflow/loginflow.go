// Package loginflow implements the client side of the Nextcloud Login Flow V2
// (https://docs.nextcloud.com/server/latest/developer_manual/client_apis/LoginFlow/).
//
// The client starts a flow anonymously, the user opens the returned login URL
// in a browser and grants access, and the client polls until the server hands
// out a login name and an app password. The app password is then used as the
// Basic-Auth password for WebDAV.
//
// This is client logic: the CLI and the GUI run the flow and pass the
// resulting credentials to the daemon via the set-account IPC command.
package loginflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gmgigi96/cernbox-sync/version"
)

// initPath is the path the server exposes the login flow under. It is
// hard-coded by the Nextcloud desktop client, so servers must serve it.
const initPath = "/index.php/login/v2"

// DefaultPollInterval is the delay between two polls. The server already
// waits a few seconds on each poll while the flow is pending.
const DefaultPollInterval = 3 * time.Second

// DefaultTimeout bounds how long a client waits for the user to grant access.
// It matches the server's default flow lifetime (20 minutes).
const DefaultTimeout = 20 * time.Minute

// ErrPending is returned by Poll while the user has not granted access yet.
// The server answers the same way for unknown, denied and expired flows, so a
// client cannot tell them apart and simply polls until its timeout.
var ErrPending = errors.New("login flow: waiting for the user to grant access")

// Credentials are returned by the server once the user has granted access.
type Credentials struct {
	Server      string `json:"server"`
	LoginName   string `json:"loginName"`
	AppPassword string `json:"appPassword"`
}

// Flow is a started login flow.
type Flow struct {
	// LoginURL must be opened by the user in a browser to grant access.
	LoginURL string

	pollToken    string
	pollEndpoint string
	hc           *http.Client
}

type initResponse struct {
	Poll struct {
		Token    string `json:"token"`
		Endpoint string `json:"endpoint"`
	} `json:"poll"`
	Login string `json:"login"`
}

func newHTTPClient() *http.Client {
	// The server holds a poll open for a few seconds while the flow is pending.
	return &http.Client{Timeout: 30 * time.Second}
}

// Start begins a login flow against serverURL (e.g. https://cernbox.cern.ch).
func Start(ctx context.Context, serverURL string) (*Flow, error) {
	base, err := parseHTTPURL(serverURL)
	if err != nil {
		return nil, fmt.Errorf("login flow: invalid server URL: %w", err)
	}
	u := strings.TrimRight(base.String(), "/") + initPath

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", version.UserAgent())
	req.Header.Set("Accept", "application/json")

	hc := newHTTPClient()
	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("login flow: init: %w", err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return nil, fmt.Errorf("login flow: server %s does not support the login flow", serverURL)
	case http.StatusTooManyRequests:
		return nil, errors.New("login flow: too many login attempts, try again in a minute")
	default:
		return nil, fmt.Errorf("login flow: init: unexpected status %s", resp.Status)
	}

	var ir initResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&ir); err != nil {
		return nil, fmt.Errorf("login flow: init: decoding response: %w", err)
	}
	if ir.Poll.Token == "" {
		return nil, errors.New("login flow: init: missing poll token")
	}
	// Both URLs come from the server: refuse anything that is not http(s) so
	// that the client never hands e.g. a file:// URL to the OS "open" call.
	if _, err := parseHTTPURL(ir.Login); err != nil {
		return nil, fmt.Errorf("login flow: init: invalid login URL: %w", err)
	}
	if _, err := parseHTTPURL(ir.Poll.Endpoint); err != nil {
		return nil, fmt.Errorf("login flow: init: invalid poll endpoint: %w", err)
	}

	return &Flow{
		LoginURL:     ir.Login,
		pollToken:    ir.Poll.Token,
		pollEndpoint: ir.Poll.Endpoint,
		hc:           hc,
	}, nil
}

// Poll checks once whether the user has granted access. It returns ErrPending
// while the flow is still pending. On success the flow is consumed on the
// server: the credentials are handed out only once.
func (f *Flow) Poll(ctx context.Context) (*Credentials, error) {
	form := url.Values{"token": {f.pollToken}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, f.pollEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	// The server binds the poll to the User-Agent sent at init.
	req.Header.Set("User-Agent", version.UserAgent())
	req.Header.Set("Accept", "application/json")

	resp, err := f.hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("login flow: poll: %w", err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return nil, ErrPending
	case http.StatusTooManyRequests:
		return nil, &retryAfterError{wait: parseRetryAfter(resp.Header.Get("Retry-After"))}
	default:
		return nil, fmt.Errorf("login flow: poll: unexpected status %s", resp.Status)
	}

	var c Credentials
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&c); err != nil {
		return nil, fmt.Errorf("login flow: poll: decoding response: %w", err)
	}
	if c.LoginName == "" || c.AppPassword == "" {
		return nil, errors.New("login flow: poll: incomplete credentials in response")
	}
	return &c, nil
}

// Wait polls every interval until the user grants access, ctx is done, or an
// unexpected error occurs. Callers should bound ctx (see DefaultTimeout):
// denied and expired flows look pending forever.
func (f *Flow) Wait(ctx context.Context, interval time.Duration) (*Credentials, error) {
	for {
		c, err := f.Poll(ctx)
		if err == nil {
			return c, nil
		}

		wait := interval
		var ra *retryAfterError
		switch {
		case errors.Is(err, ErrPending):
		case errors.As(err, &ra):
			wait = max(wait, ra.wait)
		default:
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, err
		}

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(wait):
		}
	}
}

// retryAfterError reports a rate-limited poll; it is retried by Wait.
type retryAfterError struct{ wait time.Duration }

func (e *retryAfterError) Error() string {
	return fmt.Sprintf("login flow: poll rate-limited, retry after %s", e.wait)
}

func parseRetryAfter(v string) time.Duration {
	if s, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && s > 0 {
		return time.Duration(s) * time.Second
	}
	return time.Second
}

func parseHTTPURL(raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return nil, err
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("%q is not an http(s) URL", raw)
	}
	return u, nil
}
