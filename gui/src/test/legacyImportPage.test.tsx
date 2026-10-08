import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, waitFor, fireEvent } from "@testing-library/react";
import { invoke } from "@tauri-apps/api/core";
import { LegacyImport } from "../pages/LegacyImport";
import type { DaemonState } from "../hooks/useDaemon";
import type { LegacyClient, LegacyFolder, LegacyPlan } from "../types";

const SERVER = "https://cernbox.cern.ch";
const CONFIG = "/home/u/.config/cernbox/cernbox.cfg";

function folder(id: string, name: string, plan?: LegacyPlan): LegacyFolder {
  return {
    id,
    display_name: name,
    local_path: `/home/u/${name}/`,
    dav_url: `${SERVER}/cernbox/desktop/remote.php/dav/files/gdelmont/`,
    target_path: `/home/${name}`,
    paused: false,
    ignore_hidden_files: true,
    virtual_files: false,
    excluded: [],
    baseline_entries: 10,
    in_use: false,
    local_is_empty: false,
    plan,
  };
}

function client(folders: LegacyFolder[], overrides: Partial<LegacyClient> = {}): LegacyClient {
  return {
    app_name: "CERNBox",
    config_path: CONFIG,
    running: false,
    accounts: [{ id: "0", server_url: `${SERVER}/cernbox/desktop/`, username: "gdelmont", display_name: "gdelmont", folders }],
    ...overrides,
  };
}

const noop = async () => {};
const daemon = {
  folders: [],
  status: { syncing: [], last_sync: {}, counts: {} },
  progress: {},
  uploadBps: 0,
  downloadBps: 0,
  daemonOnline: true,
  loading: false,
  error: null,
  refresh: noop,
  addFolder: noop,
  updateFolder: noop,
  removeFolder: noop,
  syncFolder: noop,
  pauseFolder: noop,
  resumeFolder: noop,
  pauseAll: noop,
  resumeAll: noop,
} satisfies DaemonState;

/** Mocks the daemon: detection answers `detected` (in turn), import answers `report`. */
function mockDaemon(detected: LegacyClient[][], report: unknown = { results: [] }) {
  const answers = [...detected];
  vi.mocked(invoke).mockImplementation(async (cmd) => {
    if (cmd === "ipc_legacy_detect") return answers.length > 1 ? answers.shift() : answers[0];
    if (cmd === "ipc_legacy_import") return report;
    throw new Error(`unexpected command ${cmd}`);
  });
}

function renderPage(clients: LegacyClient[], onDone = vi.fn()) {
  render(<LegacyImport clients={clients} serverUrl={SERVER} daemon={daemon} onDone={onDone} />);
  return onDone;
}

beforeEach(() => {
  vi.mocked(invoke).mockReset();
});

describe("LegacyImport", () => {
  it("shows the daemon's verdicts and imports the ready folders it selected", async () => {
    const home = folder("f1", "home");
    const lazy = folder("f2", "Lazy");
    const planned = client(
      [
        folder("f1", "home", { ready: true, location: "gdelmont", unsynced_files: ["top.txt"] }),
        folder("f2", "Lazy", { ready: false, reason: "Uses virtual files (downloaded on demand)." }),
      ],
      { upload_limit_kbps: 250 },
    );
    mockDaemon([[planned]], { results: [{ config_path: CONFIG, account_id: "0", folder_id: "f1", name: "home" }] });
    const onDone = renderPage([client([home, lazy])]);

    // Listed right away, then checked by the daemon.
    expect(screen.getAllByText("Checking…")).toHaveLength(2);
    expect(await screen.findByText(/Uses virtual files/)).toBeInTheDocument();
    expect(screen.getByText("gdelmont")).toBeInTheDocument();
    expect(screen.getByText(/1 file next to the excluded folders/)).toBeInTheDocument();
    expect(screen.getByText(/upload 250 KB\/s/)).toBeInTheDocument();
    expect(invoke).toHaveBeenCalledWith("ipc_legacy_detect", { serverUrl: SERVER, plan: true });
    expect(screen.getByRole("checkbox", { name: "Import home" })).toBeChecked();
    expect(screen.getByRole("checkbox", { name: "Import Lazy" })).not.toBeChecked();

    fireEvent.click(screen.getByRole("button", { name: "Import 1 folder" }));
    await screen.findByText("1 folder imported");
    expect(invoke).toHaveBeenCalledWith("ipc_legacy_import", {
      serverUrl: SERVER,
      folders: [{ config_path: CONFIG, account_id: "0", folder_id: "f1" }],
      importLimits: true,
    });

    fireEvent.click(screen.getByRole("button", { name: /Continue/ }));
    expect(onDone).toHaveBeenCalled();
  });

  it("reports the folders the daemon could not import", async () => {
    mockDaemon([[client([folder("f1", "home", { ready: true })])]], {
      results: [{ config_path: CONFIG, account_id: "0", folder_id: "f1", error: "home is still in use" }],
      limits_error: "boom",
    });
    renderPage([client([folder("f1", "home")])]);

    fireEvent.click(await screen.findByRole("button", { name: "Import 1 folder" }));
    expect(await screen.findByText("No folders imported")).toBeInTheDocument();
    expect(screen.getByText(/home is still in use/)).toBeInTheDocument();
  });

  it("does not import while the desktop client is running", async () => {
    const ready = folder("f1", "home", { ready: true });
    mockDaemon([[client([ready], { running: true })], [client([ready])]]);
    renderPage([client([folder("f1", "home")], { running: true })]);

    expect(await screen.findByText(/desktop client is running/)).toBeInTheDocument();
    await waitFor(() => expect(screen.queryByText("Checking…")).not.toBeInTheDocument());
    expect(screen.getByRole("button", { name: "Import 1 folder" })).toBeDisabled();

    fireEvent.click(screen.getByRole("button", { name: /Check again/ }));
    await waitFor(() => expect(screen.getByRole("button", { name: "Import 1 folder" })).toBeEnabled());
    expect(screen.queryByText(/desktop client is running/)).not.toBeInTheDocument();
  });

  it("offers a retry when the daemon cannot check the folders", async () => {
    vi.mocked(invoke).mockRejectedValueOnce("list spaces: status 401");
    renderPage([client([folder("f1", "home")])]);

    expect(await screen.findByText("list spaces: status 401")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Import 0 folders" })).toBeDisabled();

    mockDaemon([[client([folder("f1", "home", { ready: true })])]]);
    fireEvent.click(screen.getByRole("button", { name: "Retry" }));
    await waitFor(() => expect(screen.getByRole("button", { name: "Import 1 folder" })).toBeEnabled());
  });
});
