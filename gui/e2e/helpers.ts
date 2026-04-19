/**
 * Shared test helpers for e2e tests.
 */

import * as crypto from "crypto";
import * as http from "http";
import * as https from "https";
import type { Page } from "@playwright/test";
import { WEBDAV_BASE, WEBDAV_USER, WEBDAV_PASS, type DaemonHandle } from "./daemon";

/** Create a unique remote subdirectory on the WebDAV server and return its URL. */
export async function mkRemoteDir(): Promise<string> {
  const id = "e2e-" + crypto.randomBytes(6).toString("hex");
  const url = `${WEBDAV_BASE}/${id}`;
  await webdavMkcol(url);
  return url;
}

/** Delete a remote directory recursively (best-effort). */
export async function rmRemoteDir(url: string): Promise<void> {
  await webdavRequest("DELETE", url).catch(() => {});
}

function webdavRequest(
  method: string,
  url: string,
  headers: Record<string, string> = {},
  body?: Buffer,
): Promise<{ status: number; body: string }> {
  return new Promise((resolve, reject) => {
    const u = new URL(url);
    const opts: http.RequestOptions = {
      method,
      hostname: u.hostname,
      port: u.port || (u.protocol === "https:" ? 443 : 80),
      path: u.pathname + u.search,
      headers: {
        Authorization: "Basic " + Buffer.from(`${WEBDAV_USER}:${WEBDAV_PASS}`).toString("base64"),
        ...headers,
      },
    };
    const lib = u.protocol === "https:" ? https : http;
    const req = lib.request(opts, (res) => {
      let buf = "";
      res.on("data", (c: Buffer) => (buf += c.toString()));
      res.on("end", () => resolve({ status: res.statusCode ?? 0, body: buf }));
    });
    req.on("error", reject);
    if (body) req.write(body);
    req.end();
  });
}

async function webdavMkcol(url: string): Promise<void> {
  const { status } = await webdavRequest("MKCOL", url);
  if (status !== 201 && status !== 405 /* already exists */) {
    throw new Error(`MKCOL ${url} returned ${status}`);
  }
}

/**
 * Log in through the AccountSetup form so subsequent interactions see
 * the main app shell (Sidebar + pages).
 */
export async function login(page: Page, username = WEBDAV_USER, password = WEBDAV_PASS): Promise<void> {
  // If already past account setup, do nothing.
  const heading = page.getByText("Connect your CERN account");
  if (!(await heading.isVisible())) return;

  await page.getByPlaceholder("your-cern-username").fill(username);
  await page.getByPlaceholder("••••••••").fill(password);
  await page.getByRole("button", { name: "Get Started" }).click();
  await heading.waitFor({ state: "hidden", timeout: 5_000 });
}

/**
 * Navigate to the Folders page by clicking the sidebar button.
 */
export async function goToFolders(page: Page): Promise<void> {
  await page.getByRole("button", { name: /Syncing Folders/i }).click();
  await page.waitForSelector("text=Syncing Folders", { timeout: 5_000 });
}

/**
 * Navigate to the Settings page.
 */
export async function goToSettings(page: Page): Promise<void> {
  await page.getByRole("button", { name: /^Settings$/i }).click();
  await page.waitForSelector("text=Configure daemon behaviour", { timeout: 5_000 });
}

/**
 * Register a sync folder through the IPC proxy directly (faster than going
 * through the full add-folder UI flow), then reload the app page so the
 * React store picks up the new folder via a fresh ipc_get_snapshot call.
 */
export async function addFolderViaProxy(
  daemon: DaemonHandle,
  name: string,
  localRoot: string,
  remoteBase: string,
  page?: Page,
): Promise<void> {
  const body = JSON.stringify({
    command: "ipc_add",
    args: { name, localRoot, remoteBase, folders: null, syncHiddenFiles: null, autoSyncOnChange: null },
  });
  const resp = await fetch(`${daemon.proxy.url}/invoke`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body,
  });
  const data = await resp.json() as { ok: boolean; error?: string };
  if (!data.ok) throw new Error(`ipc_add failed: ${data.error}`);

  if (page) {
    // Reload so the React app fetches a fresh snapshot that includes the new folder.
    await page.reload();
    await page.waitForSelector("#root > *", { timeout: 10_000 });
  }
}
