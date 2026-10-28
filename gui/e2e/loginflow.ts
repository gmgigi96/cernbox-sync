/**
 * Login Flow V2 helpers for e2e tests, against the dev environment.
 *
 * The dev environment has no web UI, so the browser side of the flow (grant /
 * deny) is played by calling the OCS endpoints the web UI would call,
 * authenticated with the demo user's account password.
 */

export const SERVER_URL = "http://localhost";
export const ACCOUNT_USER = "einstein";
export const ACCOUNT_PASS = "relativity";

/**
 * User-Agent of the sync client (mirrors user_agent() in src-tauri/src/lib.rs).
 * The server only accepts app passwords from clients sending it, so the
 * Playwright browser uses it too, like the Tauri webview does.
 */
export const CLIENT_UA = "Mozilla/5.0 (Linux) mirall/0.1.0 (cernbox-sync)";

export interface StartedFlow {
  loginUrl: string;
  pollToken: string;
  pollEndpoint: string;
}

export interface Credentials {
  loginName: string;
  appPassword: string;
}

/** Starts a login flow as the sync client. */
export async function startLoginFlow(serverUrl = SERVER_URL): Promise<StartedFlow> {
  const resp = await fetch(`${serverUrl.replace(/\/$/, "")}/index.php/login/v2`, {
    method: "POST",
    headers: { "User-Agent": CLIENT_UA, Accept: "application/json" },
  });
  if (resp.status === 404) throw new Error(`The server ${serverUrl} does not support browser login.`);
  if (!resp.ok) throw new Error(`Unexpected response from the server (HTTP ${resp.status}).`);
  const body = (await resp.json()) as { poll: { token: string; endpoint: string }; login: string };
  return { loginUrl: body.login, pollToken: body.poll.token, pollEndpoint: body.poll.endpoint };
}

/** Polls once as the sync client; null while access has not been granted. */
export async function pollLoginFlow(flow: StartedFlow): Promise<Credentials | null> {
  const resp = await fetch(flow.pollEndpoint, {
    method: "POST",
    headers: {
      "User-Agent": CLIENT_UA,
      "Content-Type": "application/x-www-form-urlencoded",
      Accept: "application/json",
    },
    body: new URLSearchParams({ token: flow.pollToken }).toString(),
  });
  if (resp.status === 404 || resp.status === 429) return null;
  if (!resp.ok) throw new Error(`Unexpected response from the server (HTTP ${resp.status}).`);
  return (await resp.json()) as Credentials;
}

/** Grants or denies the flow behind loginUrl as the demo user (the web UI's job). */
export async function loginFlowAction(
  loginUrl: string,
  action: "grant" | "deny",
  deviceName = "",
): Promise<void> {
  const lt = loginUrl.slice(loginUrl.lastIndexOf("/") + 1);
  const resp = await fetch(`${SERVER_URL}/ocs/v2.php/cloud/user/login-flow/${lt}/${action}`, {
    method: "POST",
    headers: {
      Authorization: "Basic " + Buffer.from(`${ACCOUNT_USER}:${ACCOUNT_PASS}`).toString("base64"),
      "Content-Type": "application/json",
    },
    body: action === "grant" ? JSON.stringify({ name: deviceName }) : undefined,
  });
  if (!resp.ok) throw new Error(`${action}: HTTP ${resp.status}: ${await resp.text()}`);
}

let appPassword: Promise<string> | null = null;

/** Obtains (once per worker) an app password through a complete login flow. */
export function obtainAppPassword(): Promise<string> {
  appPassword ??= (async () => {
    const flow = await startLoginFlow();
    await loginFlowAction(flow.loginUrl, "grant", "e2e-tests");
    for (let i = 0; i < 20; i++) {
      const creds = await pollLoginFlow(flow);
      if (creds) return creds.appPassword;
    }
    throw new Error("login flow: no credentials after granting access");
  })();
  return appPassword;
}
