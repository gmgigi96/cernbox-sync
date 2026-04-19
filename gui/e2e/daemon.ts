/**
 * Utilities for starting an isolated cernbox-syncd daemon and a small HTTP
 * proxy that bridges browser invoke() calls to the daemon's Unix socket.
 *
 * The GUI uses Tauri's invoke() which talks to Rust command handlers that
 * proxy to the daemon via Unix socket. In Playwright (Vite dev server, no
 * Tauri runtime) we:
 *   1. Start a real cernbox-syncd with an isolated socket + config dir.
 *   2. Start an HTTP server that accepts { cmd, args } JSON and forwards them
 *      to the Unix socket, returning the response.
 *   3. Inject window.__TAURI_INTERNALS__ into every page so that
 *      invoke("ipc_list") etc. hit the HTTP proxy.
 */

import { execSync, spawn, type ChildProcess } from "child_process";
import * as fs from "fs";
import * as http from "http";
import * as net from "net";
import * as os from "os";
import * as path from "path";
import { fileURLToPath } from "url";

const __filename = fileURLToPath(import.meta.url);
const __dirname = path.dirname(__filename);

export const WEBDAV_BASE = "http://localhost/remote.php/webdav/eos/user/e/einstein";
export const WEBDAV_USER = "einstein";
export const WEBDAV_PASS = "relativity";

// ── build helpers ─────────────────────────────────────────────────────────────

/** Returns the absolute path of the repo root (two levels up from gui/e2e/). */
export function repoRoot(): string {
  return path.resolve(__dirname, "../..");
}

let _daemonBin: string | null = null;

/** Builds cernbox-syncd once and caches the path. */
export function buildDaemon(): string {
  if (_daemonBin) return _daemonBin;
  const tmpDir = fs.mkdtempSync(path.join(os.tmpdir(), "cernbox-e2e-bins-"));
  const out = path.join(tmpDir, "cernbox-syncd");
  execSync(`go build -o ${out} ./cmd/cernbox-syncd`, {
    cwd: repoRoot(),
    stdio: "pipe",
  });
  _daemonBin = out;
  return out;
}

// ── Unix socket IPC ────────────────────────────────────────────────────────────

/** Send a single JSON line to the daemon socket and read the response line. */
async function socketSend(sockPath: string, obj: unknown): Promise<unknown> {
  return new Promise((resolve, reject) => {
    const sock = net.createConnection(sockPath, () => {
      sock.write(JSON.stringify(obj) + "\n");
    });
    let buf = "";
    sock.on("data", (chunk) => {
      buf += chunk.toString();
      const nl = buf.indexOf("\n");
      if (nl !== -1) {
        sock.destroy();
        try {
          resolve(JSON.parse(buf.slice(0, nl)));
        } catch (e) {
          reject(e);
        }
      }
    });
    sock.on("error", reject);
  });
}

// ── IPC command → daemon request mapping ──────────────────────────────────────

/**
 * Translate a Tauri invoke call (command name + camelCase args) into the
 * daemon IPC request shape and return the value the Tauri command would return.
 */
async function dispatchInvoke(
  sockPath: string,
  command: string,
  args: Record<string, unknown>,
): Promise<unknown> {
  type DaemonReq = {
    cmd: string;
    name?: string | null;
    folder?: unknown;
    settings?: unknown;
    account?: unknown;
  };
  type DaemonResp = {
    ok: boolean;
    error?: string;
    folders?: unknown[];
    status?: unknown;
    settings?: unknown;
    account?: unknown;
  };

  async function send(req: DaemonReq): Promise<DaemonResp> {
    const resp = (await socketSend(sockPath, req)) as DaemonResp;
    if (!resp.ok) throw new Error(resp.error ?? "daemon error");
    return resp;
  }

  switch (command) {
    case "ipc_list": {
      const r = await send({ cmd: "list" });
      return r.folders ?? [];
    }
    case "ipc_status": {
      const r = await send({ cmd: "status" });
      const s = r.status as { syncing: string[]; last_sync: Record<string, string>; counts: Record<string, unknown> };
      return { syncing: s.syncing, last_sync: s.last_sync, counts: s.counts ?? {} };
    }
    case "ipc_get_account": {
      const r = await send({ cmd: "get-account" });
      return r.account ?? null;
    }
    case "ipc_set_account": {
      await send({ cmd: "set-account", account: { username: args.username, password: args.password } });
      return null;
    }
    case "ipc_get_settings": {
      const r = await send({ cmd: "get-settings" });
      const s = r.settings as Record<string, unknown> | null ?? {};
      return {
        logRotateMaxAge: s.log_rotate_max_age ?? null,
        syncInterval: s.sync_interval ?? null,
        uploadBandwidth: s.upload_bandwidth ?? 0,
        downloadBandwidth: s.download_bandwidth ?? 0,
        transferStreams: s.transfer_streams ?? 1,
        metadataStreams: s.metadata_streams ?? 1,
      };
    }
    case "ipc_set_settings": {
      await send({
        cmd: "set-settings",
        settings: {
          log_rotate_max_age: args.logRotateMaxAge ?? null,
          sync_interval: args.syncInterval ?? null,
          upload_bandwidth: args.uploadBandwidth ?? 0,
          download_bandwidth: args.downloadBandwidth ?? 0,
          transfer_streams: args.transferStreams ?? 1,
          metadata_streams: args.metadataStreams ?? 1,
        },
      });
      return null;
    }
    case "ipc_add": {
      await send({
        cmd: "add",
        folder: {
          Name: args.name,
          LocalRoot: args.localRoot,
          RemoteBase: args.remoteBase,
          Folders: args.folders ?? [],
          Settings: {
            sync_hidden_files: args.syncHiddenFiles ?? false,
            auto_sync_on_change: args.autoSyncOnChange ?? false,
          },
        },
      });
      return null;
    }
    case "ipc_update": {
      await send({
        cmd: "update",
        folder: {
          Name: args.name,
          LocalRoot: args.localRoot,
          RemoteBase: args.remoteBase,
          Folders: args.folders ?? [],
          Settings: {
            sync_hidden_files: args.syncHiddenFiles ?? false,
            auto_sync_on_change: args.autoSyncOnChange ?? false,
          },
        },
      });
      return null;
    }
    case "ipc_remove": {
      await send({ cmd: "remove", name: args.name as string });
      return null;
    }
    case "ipc_sync": {
      await send({ cmd: "sync", name: (args.name as string | null) ?? null });
      return null;
    }
    case "ipc_get_snapshot": {
      const [fResp, sResp] = await Promise.all([
        send({ cmd: "list" }),
        send({ cmd: "status" }),
      ]);
      const s = sResp.status as { syncing: string[]; last_sync: Record<string, string>; counts: Record<string, unknown> };
      return {
        ok: true,
        folders: fResp.folders ?? [],
        status: { syncing: s.syncing, last_sync: s.last_sync, counts: s.counts ?? {} },
      };
    }
    case "list_local_dir": {
      // Return the home dir contents (simplified — sufficient for tests).
      const dir = (args.path as string | null) || os.homedir();
      const entries = fs.readdirSync(dir, { withFileTypes: true })
        .filter((e) => !e.name.startsWith(".") && e.isDirectory())
        .sort((a, b) => a.name.toLowerCase().localeCompare(b.name.toLowerCase()))
        .map((e) => ({ name: e.name, path: path.join(dir, e.name), is_dir: true }));
      const parent = path.dirname(dir);
      if (parent !== dir) entries.unshift({ name: "..", path: parent, is_dir: true });
      return entries;
    }
    case "create_local_dir": {
      const newPath = path.join(args.parent as string, args.name as string);
      fs.mkdirSync(newPath, { recursive: false });
      return newPath;
    }
    case "read_text_file": {
      try {
        return fs.readFileSync(args.path as string, "utf8");
      } catch {
        return null;
      }
    }
    case "open_log_file":
      return null;
    // Tauri's @tauri-apps/api/event listen() calls plugin:event|listen internally.
    // We return a stub handler ID — no actual events are pushed in tests.
    case "plugin:event|listen":
    case "plugin:event|unlisten":
    case "plugin:event|emit":
      return 0;
    default:
      throw new Error(`Unknown invoke command: ${command}`);
  }
}

// ── HTTP proxy server ─────────────────────────────────────────────────────────

export interface ProxyServer {
  url: string;
  close(): void;
}

/**
 * Starts an HTTP server on a random port that accepts POST /invoke with body
 * { command, args } and proxies to the daemon Unix socket at sockPath.
 */
export function startProxyServer(sockPath: string): Promise<ProxyServer> {
  return new Promise((resolve, reject) => {
    const server = http.createServer(async (req, res) => {
      // CORS preflight
      res.setHeader("Access-Control-Allow-Origin", "*");
      res.setHeader("Access-Control-Allow-Methods", "POST, OPTIONS");
      res.setHeader("Access-Control-Allow-Headers", "Content-Type");
      if (req.method === "OPTIONS") {
        res.writeHead(204).end();
        return;
      }
      if (req.method !== "POST" || req.url !== "/invoke") {
        res.writeHead(404).end();
        return;
      }
      let body = "";
      req.on("data", (c) => (body += c));
      req.on("end", async () => {
        res.setHeader("Content-Type", "application/json");
        try {
          const { command, args } = JSON.parse(body) as { command: string; args: Record<string, unknown> };
          const result = await dispatchInvoke(sockPath, command, args);
          res.writeHead(200).end(JSON.stringify({ ok: true, result }));
        } catch (e) {
          res.writeHead(200).end(JSON.stringify({ ok: false, error: String(e) }));
        }
      });
    });
    server.once("error", reject);
    server.listen(0, "127.0.0.1", () => {
      const addr = server.address() as net.AddressInfo;
      resolve({
        url: `http://127.0.0.1:${addr.port}`,
        close: () => server.close(),
      });
    });
  });
}

// ── Daemon lifecycle ───────────────────────────────────────────────────────────

export interface DaemonHandle {
  sockPath: string;
  configDir: string;
  localDir: string;
  tmpDir: string;
  proxy: ProxyServer;
  stop(): void;
}

export interface StartDaemonOptions {
  /** When true the daemon is started without pre-setting credentials (useful for AccountSetup tests). */
  noAccount?: boolean;
}

/**
 * Builds the daemon binary, starts an isolated cernbox-syncd instance, and
 * starts the HTTP proxy. Resolves when the daemon is ready to accept
 * connections on its Unix socket.
 */
export async function startDaemon(opts: StartDaemonOptions = {}): Promise<DaemonHandle> {
  const bin = buildDaemon();
  const tmpDir = fs.mkdtempSync(path.join(os.tmpdir(), "cernbox-e2e-run-"));
  const runDir = path.join(tmpDir, "run");
  const configDir = path.join(tmpDir, "config");
  const localDir = path.join(tmpDir, "local");
  for (const d of [runDir, configDir, localDir]) fs.mkdirSync(d, { recursive: true });

  const sockPath = path.join(runDir, "cernbox-sync.sock");

  const proc: ChildProcess = spawn(bin, ["-interval", "24h", "-socket", sockPath], {
    env: { ...process.env, XDG_RUNTIME_DIR: runDir, XDG_CONFIG_HOME: configDir },
    stdio: "pipe",
  });

  // Wait until the socket is accessible (up to 15 s).
  await waitForSocket(sockPath, 15_000);

  // Set credentials so any IPC that needs them works (unless the test wants a blank state).
  if (!opts.noAccount) {
    await socketSend(sockPath, { cmd: "set-account", account: { username: WEBDAV_USER, password: WEBDAV_PASS } });
  }

  const proxy = await startProxyServer(sockPath);

  const handle: DaemonHandle = {
    sockPath,
    configDir,
    localDir,
    tmpDir,
    proxy,
    stop() {
      proxy.close();
      proc.kill("SIGTERM");
      try {
        fs.rmSync(tmpDir, { recursive: true, force: true });
      } catch { /* best effort */ }
    },
  };

  return handle;
}

async function waitForSocket(sockPath: string, timeoutMs: number): Promise<void> {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    try {
      await socketSend(sockPath, { cmd: "status" });
      return;
    } catch {
      await new Promise((r) => setTimeout(r, 100));
    }
  }
  throw new Error(`Daemon socket ${sockPath} not ready within ${timeoutMs}ms`);
}

// ── JavaScript snippet injected into every page ───────────────────────────────

/**
 * Returns a <script> snippet that installs a window.__TAURI_INTERNALS__ shim.
 * invoke() calls are forwarded to the HTTP proxy server.
 */
export function tauriMockScript(proxyUrl: string): string {
  return `
(function() {
  const PROXY = ${JSON.stringify(proxyUrl)};

  async function invoke(command, args) {
    const resp = await fetch(PROXY + '/invoke', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ command, args: args ?? {} }),
    });
    const data = await resp.json();
    if (!data.ok) throw new Error(data.error);
    return data.result;
  }

  // Tauri v2 exposes invoke via window.__TAURI_INTERNALS__.ipc and
  // the @tauri-apps/api/core module resolves it from there.
  window.__TAURI_INTERNALS__ = {
    invoke,
    // Event listeners are no-ops in tests; App.tsx uses addPluginListener
    // which wraps these. The daemon-snapshot event is fired by the
    // ipc_get_snapshot call in App.tsx's useEffect, so we don't need push events.
    transformCallback: (cb) => { const id = Math.random(); window[id] = cb; return id; },
    convertFileSrc: (p) => p,
  };

  // Also patch the module so existing imports resolve correctly.
  // @tauri-apps/api/core is bundled via Vite; we stub its __invoke__ export.
  window.__TAURI_INVOKE__ = invoke;
})();
`;
}
