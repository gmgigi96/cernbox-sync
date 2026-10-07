//go:build integration

package integration_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gmgigi96/cernbox-sync/ipc"
	"github.com/gmgigi96/cernbox-sync/loginflow"
	"github.com/gmgigi96/cernbox-sync/version"
)

// These tests exercise the Login Flow V2 service of the dev environment. The
// dev environment has no web UI, so the browser side (grant / deny) is played
// by calling the OCS endpoints the web UI would call, authenticated with the
// account password.

// helperUA is the User-Agent used for requests made as the user (not as the
// sync client), so that the account password is accepted.
const helperUA = "cernbox-sync-integration-tests"

var httpClient = &http.Client{Timeout: 30 * time.Second}

// obtainAppPassword runs a complete login flow, granting it as webdavUser with
// the given device name, and returns the app password.
func obtainAppPassword(deviceName string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	flow, err := loginflow.Start(ctx, serverURL)
	if err != nil {
		return "", err
	}
	if err := loginFlowAction(flow.LoginURL, "grant", deviceName); err != nil {
		return "", err
	}
	creds, err := flow.Wait(ctx, 200*time.Millisecond)
	if err != nil {
		return "", err
	}
	if creds.LoginName != webdavUser {
		return "", fmt.Errorf("login name = %q, want %q", creds.LoginName, webdavUser)
	}
	return creds.AppPassword, nil
}

// loginFlowAction plays the web UI: it grants or denies the flow behind
// loginURL as webdavUser.
func loginFlowAction(loginURL, action, deviceName string) error {
	lt := loginURL[strings.LastIndex(loginURL, "/")+1:]
	var body io.Reader
	if action == "grant" {
		b, _ := json.Marshal(map[string]string{"name": deviceName})
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequest(http.MethodPost, serverURL+"/ocs/v2.php/cloud/user/login-flow/"+lt+"/"+action, body)
	if err != nil {
		return err
	}
	req.SetBasicAuth(webdavUser, webdavPass)
	req.Header.Set("User-Agent", helperUA)
	req.Header.Set("Content-Type", "application/json")
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return &httpStatusError{op: action, code: resp.StatusCode, body: string(bytes.TrimSpace(b))}
	}
	return nil
}

type httpStatusError struct {
	op   string
	code int
	body string
}

func (e *httpStatusError) Error() string {
	return fmt.Sprintf("%s: HTTP %d: %s", e.op, e.code, e.body)
}

// propfindStatus returns the status of a Depth:0 PROPFIND on the user's home.
func propfindStatus(t *testing.T, user, pass, userAgent string) int {
	t.Helper()
	req, err := http.NewRequest("PROPFIND", webdavBase+"/", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.SetBasicAuth(user, pass)
	req.Header.Set("Depth", "0")
	req.Header.Set("User-Agent", userAgent)
	resp, err := httpClient.Do(req)
	if err != nil {
		t.Fatalf("PROPFIND: %v", err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

type connectedClient struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

// listClients returns the user's connected clients (OCS management API).
func listClients(t *testing.T) []connectedClient {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, serverURL+"/ocs/v2.php/cloud/user/clients?format=json", nil)
	req.SetBasicAuth(webdavUser, webdavPass)
	req.Header.Set("User-Agent", helperUA)
	req.Header.Set("OCS-APIRequest", "true")
	resp, err := httpClient.Do(req)
	if err != nil {
		t.Fatalf("list clients: %v", err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list clients: HTTP %d: %s", resp.StatusCode, b)
	}
	var out struct {
		OCS struct {
			Data []connectedClient `json:"data"`
		} `json:"ocs"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("list clients: decoding %s: %v", b, err)
	}
	return out.OCS.Data
}

func revokeClient(t *testing.T, id string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodDelete, serverURL+"/ocs/v2.php/cloud/user/clients/"+url.PathEscape(id)+"?format=json", nil)
	req.SetBasicAuth(webdavUser, webdavPass)
	req.Header.Set("User-Agent", helperUA)
	req.Header.Set("OCS-APIRequest", "true")
	resp, err := httpClient.Do(req)
	if err != nil {
		t.Fatalf("revoke client: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("revoke client: HTTP %d: %s", resp.StatusCode, b)
	}
}

// TestIntegration_LoginFlow_AppPasswordAuth checks how the server treats the
// credentials of a sync client: with the sync-client User-Agent only the app
// password is accepted, not the account password.
func TestIntegration_LoginFlow_AppPasswordAuth(t *testing.T) {
	if got := propfindStatus(t, webdavUser, appPassword, version.UserAgent()); got != http.StatusMultiStatus {
		t.Errorf("app password with client User-Agent: status %d, want 207", got)
	}
	if got := propfindStatus(t, webdavUser, webdavPass, version.UserAgent()); got != http.StatusUnauthorized {
		t.Errorf("account password with client User-Agent: status %d, want 401", got)
	}
	if got := propfindStatus(t, "marie", appPassword, version.UserAgent()); got != http.StatusUnauthorized {
		t.Errorf("einstein's app password used as marie: status %d, want 401", got)
	}
}

// TestIntegration_LoginFlow_CLI runs `cernbox-sync login` against a daemon,
// grants the printed login URL, and checks the daemon syncs with the new
// app password.
func TestIntegration_LoginFlow_CLI(t *testing.T) {
	env := setup(t)

	cmd := exec.Command(cliPath, "login", "-server", serverURL, "-no-browser")
	cmd.Env = append(os.Environ(), "XDG_RUNTIME_DIR="+filepath.Dir(env.sockPath))
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start login: %v", err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })

	// Read the login URL from the CLI output and grant it, like a user would
	// in the browser.
	var output strings.Builder
	scanner := bufio.NewScanner(stdout)
	granted := false
	for scanner.Scan() {
		line := scanner.Text()
		output.WriteString(line + "\n")
		if u := strings.TrimSpace(line); !granted && strings.Contains(u, "/index.php/login/v2/flow/") {
			if err := loginFlowAction(u, "grant", "cli-test"); err != nil {
				t.Fatalf("grant: %v", err)
			}
			granted = true
		}
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("login: %v\nstdout:\n%s\nstderr:\n%s", err, output.String(), stderr.String())
	}
	if !granted {
		t.Fatalf("login URL not found in output:\n%s", output.String())
	}
	if !strings.Contains(output.String(), `Logged in as "einstein"`) {
		t.Errorf("unexpected output:\n%s", output.String())
	}

	resp, err := ipc.Send(env.sockPath, ipc.Request{Cmd: ipc.CmdGetAccount})
	if err != nil || !resp.OK || resp.Account == nil {
		t.Fatalf("get-account: %v %+v", err, resp)
	}
	if resp.Account.Username != webdavUser {
		t.Errorf("account username = %q, want %q", resp.Account.Username, webdavUser)
	}
	if p := resp.Account.Password; p == "" || p == appPassword || p == webdavPass {
		t.Errorf("account password was not replaced by a new app password")
	}

	// The daemon now syncs with the app password obtained by the CLI.
	env.writeRemote("from-server.txt", "hello")
	env.triggerSync()
	if got := env.readLocal("from-server.txt"); got != "hello" {
		t.Errorf("synced content = %q, want %q", got, "hello")
	}
}

// TestIntegration_LoginFlow_Deny checks a denied flow never yields credentials.
func TestIntegration_LoginFlow_Deny(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	flow, err := loginflow.Start(ctx, serverURL)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := loginFlowAction(flow.LoginURL, "deny", ""); err != nil {
		t.Fatalf("deny: %v", err)
	}
	if _, err := flow.Poll(ctx); !errors.Is(err, loginflow.ErrPending) {
		t.Fatalf("Poll after deny: err = %v, want ErrPending", err)
	}
	var se *httpStatusError
	if err := loginFlowAction(flow.LoginURL, "grant", ""); !errors.As(err, &se) || se.code != http.StatusNotFound {
		t.Fatalf("grant after deny: err = %v, want HTTP 404", err)
	}
}

// TestIntegration_LoginFlow_SingleUse checks the credentials are handed out
// once, and that the login URL cannot be granted twice.
func TestIntegration_LoginFlow_SingleUse(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	flow, err := loginflow.Start(ctx, serverURL)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := loginFlowAction(flow.LoginURL, "grant", "single-use"); err != nil {
		t.Fatalf("grant: %v", err)
	}
	var se *httpStatusError
	if err := loginFlowAction(flow.LoginURL, "grant", "single-use"); !errors.As(err, &se) || se.code != http.StatusConflict {
		t.Errorf("second grant: err = %v, want HTTP 409", err)
	}
	if _, err := flow.Wait(ctx, 200*time.Millisecond); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if _, err := flow.Poll(ctx); !errors.Is(err, loginflow.ErrPending) {
		t.Errorf("second Poll: err = %v, want ErrPending", err)
	}
}

// TestIntegration_LoginFlow_PollBoundToUserAgent checks the server only hands
// the credentials to a poll with the same User-Agent as the init request.
func TestIntegration_LoginFlow_PollBoundToUserAgent(t *testing.T) {
	const ua = "Mozilla/5.0 (Linux) mirall/0.0.1 (ua-binding-test)"

	req, _ := http.NewRequest(http.MethodPost, serverURL+"/index.php/login/v2", nil)
	req.Header.Set("User-Agent", ua)
	resp, err := httpClient.Do(req)
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	var ir struct {
		Poll struct {
			Token    string `json:"token"`
			Endpoint string `json:"endpoint"`
		} `json:"poll"`
		Login string `json:"login"`
	}
	err = json.NewDecoder(resp.Body).Decode(&ir)
	resp.Body.Close()
	if err != nil {
		t.Fatalf("init: decoding: %v", err)
	}
	if err := loginFlowAction(ir.Login, "grant", "ua-binding"); err != nil {
		t.Fatalf("grant: %v", err)
	}

	poll := func(userAgent string) int {
		req, _ := http.NewRequest(http.MethodPost, ir.Poll.Endpoint, strings.NewReader(url.Values{"token": {ir.Poll.Token}}.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("User-Agent", userAgent)
		resp, err := httpClient.Do(req)
		if err != nil {
			t.Fatalf("poll: %v", err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if got := poll("Mozilla/5.0 (Linux) mirall/0.0.1 (attacker)"); got != http.StatusNotFound {
		t.Errorf("poll with another User-Agent: status %d, want 404", got)
	}
	if got := poll(ua); got != http.StatusOK {
		t.Errorf("poll with the init User-Agent: status %d, want 200", got)
	}
}

// TestIntegration_LoginFlow_Revoke checks a revoked client loses access.
func TestIntegration_LoginFlow_Revoke(t *testing.T) {
	deviceName := "revoke-" + randHex()
	pass, err := obtainAppPassword(deviceName)
	if err != nil {
		t.Fatalf("obtain app password: %v", err)
	}

	var id string
	for _, c := range listClients(t) {
		if c.Name == deviceName {
			id = c.ID
			if !strings.Contains(c.Description, "mirall") && !strings.Contains(c.Description, "Nextcloud") {
				t.Errorf("client description %q does not describe the sync client", c.Description)
			}
		}
	}
	if id == "" {
		t.Fatalf("client %q not listed among connected clients", deviceName)
	}

	// The password is not used before revoking: the server caches accepted
	// credentials for a few minutes, so a revocation is not visible to a client
	// that authenticated recently.
	revokeClient(t, id)

	if got := propfindStatus(t, webdavUser, pass, version.UserAgent()); got != http.StatusUnauthorized {
		t.Errorf("revoked app password: status %d, want 401", got)
	}
	for _, c := range listClients(t) {
		if c.ID == id {
			t.Errorf("revoked client %q still listed", id)
		}
	}
}
