import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, waitFor, fireEvent } from "@testing-library/react";
import { invoke } from "@tauri-apps/api/core";
import { SetupWizard, durationSeconds, needsSetup, wizardSteps } from "../pages/SetupWizard";
import { useUiStore } from "../store/uiStore";
import type { DaemonState } from "../hooks/useDaemon";
import type { Account, LegacyClient } from "../types";

const SERVER = "https://cernbox.cern.ch";
const ACCOUNT: Account = { username: "gdelmont", password: "secret" };

const noop = async () => {};
function daemonState(overrides: Partial<DaemonState> = {}): DaemonState {
  return {
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
    ...overrides,
  };
}

const legacyClient: LegacyClient = {
  app_name: "CERNBox",
  config_path: "/home/u/.config/cernbox/cernbox.cfg",
  running: false,
  accounts: [
    {
      id: "0",
      server_url: `${SERVER}/cernbox/desktop/`,
      username: "legacyuser",
      display_name: "legacyuser",
      folders: [
        {
          id: "1",
          display_name: "Home",
          local_path: "/home/u/cernbox/",
          dav_url: `${SERVER}/cernbox/desktop/remote.php/dav/files/legacyuser/`,
          target_path: "/",
          paused: false,
          ignore_hidden_files: true,
          virtual_files: false,
          excluded: [],
          baseline_entries: 10,
          in_use: false,
          local_is_empty: false,
        },
      ],
    },
  ],
};

const SETTINGS = {
  logRotateMaxAge: "168h",
  syncInterval: "5m0s",
  uploadBandwidth: 0,
  downloadBandwidth: 2 * 1024 * 1024,
  transferStreams: 4,
  metadataStreams: 2,
};

/** Mocks the daemon commands the wizard uses. */
function mockDaemon({ legacy = [] as LegacyClient[], account = ACCOUNT as Account | null } = {}) {
  vi.mocked(invoke).mockImplementation(async (cmd) => {
    switch (cmd) {
      case "ipc_legacy_detect":
        return legacy;
      case "ipc_set_account":
        return undefined;
      case "ipc_get_account":
        return account;
      case "ipc_get_settings":
        return SETTINGS;
      case "ipc_set_settings":
        return undefined;
      default:
        throw new Error(`unexpected command ${cmd}`);
    }
  });
}

function renderWizard(props: { account?: Account | false; daemon?: DaemonState } = {}) {
  const onFinish = vi.fn();
  const onAccountChanged = vi.fn();
  render(
    <SetupWizard
      serverUrl={SERVER}
      daemon={props.daemon ?? daemonState()}
      account={props.account ?? false}
      onAccountChanged={onAccountChanged}
      onFinish={onFinish}
    />,
  );
  return { onFinish, onAccountChanged };
}

const currentStep = () => screen.getByRole("listitem", { current: "step" });

beforeEach(() => {
  vi.mocked(invoke).mockReset();
  localStorage.clear();
  useUiStore.setState({ viewMode: "simple" });
});

describe("needsSetup", () => {
  it("is needed without an account, even after a previous setup", () => {
    expect(needsSetup(false, true, 3)).toBe(true);
  });

  it("is needed on first start with nothing synced", () => {
    expect(needsSetup(ACCOUNT, false, 0)).toBe(true);
  });

  it("is skipped once done, or when folders are already synced", () => {
    expect(needsSetup(ACCOUNT, true, 0)).toBe(false);
    expect(needsSetup(ACCOUNT, false, 2)).toBe(false);
  });
});

describe("durationSeconds", () => {
  it("reads the durations of the daemon and of the choices alike", () => {
    expect(durationSeconds("5m")).toBe(300);
    expect(durationSeconds("5m0s")).toBe(300);
    expect(durationSeconds("1h0m0s")).toBe(durationSeconds("1h"));
    expect(durationSeconds("1m30s")).toBe(90);
    expect(durationSeconds("soon")).toBeNaN();
    expect(durationSeconds("5mx")).toBeNaN();
  });
});

describe("wizardSteps", () => {
  it("offers the import only when a desktop client syncs folders", () => {
    expect(wizardSteps(0)).toEqual(["welcome", "account", "interface", "preferences", "done"]);
    expect(wizardSteps(2)).toEqual(["welcome", "account", "import", "interface", "preferences", "done"]);
  });
});

describe("SetupWizard", () => {
  it("walks a new user through sign-in, interface and preferences", async () => {
    mockDaemon();
    const { onFinish, onAccountChanged } = renderWizard();

    // Welcome: the button is enabled once the desktop-client detection answered.
    const start = await screen.findByRole("button", { name: /get started/i });
    await waitFor(() => expect(start).toBeEnabled());
    expect(screen.queryByText(/desktop client on this computer/)).not.toBeInTheDocument();
    fireEvent.click(start);

    // Account: no import step without a desktop client.
    expect(currentStep()).toHaveTextContent("Account");
    expect(screen.queryByText("Import")).not.toBeInTheDocument();
    fireEvent.change(screen.getByPlaceholderText("your-cern-username"), { target: { value: "gdelmont" } });
    fireEvent.change(screen.getByPlaceholderText("••••••••"), { target: { value: "secret" } });
    fireEvent.click(screen.getByRole("button", { name: /get started/i }));

    await waitFor(() => expect(currentStep()).toHaveTextContent("Interface"));
    expect(invoke).toHaveBeenCalledWith("ipc_set_account", { username: "gdelmont", password: "secret" });
    expect(onAccountChanged).toHaveBeenCalledWith(ACCOUNT);

    // Interface: simple is preselected; the choice is persisted.
    expect(screen.getByRole("radio", { name: /simple/i })).toHaveAttribute("aria-checked", "true");
    fireEvent.click(screen.getByRole("radio", { name: /advanced/i }));
    expect(useUiStore.getState().viewMode).toBe("advanced");
    expect(localStorage.getItem("cernbox-sync-ui")).toContain('"advanced"');
    fireEvent.click(screen.getByRole("button", { name: /continue/i }));

    // Preferences: shows the daemon's values and saves the edits, keeping the rest.
    expect(currentStep()).toHaveTextContent("Preferences");
    const interval = screen.getByRole("combobox", { name: "Sync interval" });
    await waitFor(() => expect(interval).toHaveValue("5m"));
    expect(await screen.findByDisplayValue("2")).toBeInTheDocument(); // 2 MB/s download limit
    fireEvent.change(interval, { target: { value: "15m" } });
    fireEvent.click(screen.getByRole("button", { name: /continue/i }));

    await waitFor(() => expect(currentStep()).toHaveTextContent("Finish"));
    expect(invoke).toHaveBeenCalledWith("ipc_set_settings", {
      logRotateMaxAge: "168h",
      syncInterval: "15m",
      uploadBandwidth: 0,
      downloadBandwidth: 2 * 1024 * 1024,
      transferStreams: 4,
      metadataStreams: 2,
    });

    // Done: nothing synced yet, so adding a folder is the main action.
    expect(screen.getByText(/advanced interface/i)).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: /add a folder/i }));
    expect(onFinish).toHaveBeenCalledWith(true);
  });

  it("offers to import the folders of a desktop client, signing in with its username", async () => {
    mockDaemon({ legacy: [legacyClient] });
    renderWizard();

    expect(await screen.findByText(/CERNBox desktop client on this computer syncs 1 folder/)).toBeInTheDocument();
    expect(screen.getByText("Import")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: /get started/i }));

    expect(screen.getByPlaceholderText("your-cern-username")).toHaveValue("legacyuser");
    expect(screen.getByText(/Sign in to import it next/)).toBeInTheDocument();
    fireEvent.change(screen.getByPlaceholderText("••••••••"), { target: { value: "secret" } });
    fireEvent.click(screen.getByRole("button", { name: /get started/i }));

    // The import page of the desktop client is the next step; skipping moves on.
    await waitFor(() => expect(currentStep()).toHaveTextContent("Import"));
    expect(screen.getByText("Import from the CERNBox desktop client")).toBeInTheDocument();
    await waitFor(() => expect(invoke).toHaveBeenCalledWith("ipc_legacy_detect", { serverUrl: SERVER, plan: true }));
    fireEvent.click(screen.getByRole("button", { name: "Skip" }));
    expect(currentStep()).toHaveTextContent("Interface");
  });

  it("keeps an account already signed in and lets the user go back", async () => {
    mockDaemon();
    renderWizard({ account: ACCOUNT });

    const start = await screen.findByRole("button", { name: /get started/i });
    await waitFor(() => expect(start).toBeEnabled());
    fireEvent.click(start);

    expect(screen.getByText("Signed in")).toBeInTheDocument();
    expect(screen.getByText("gdelmont")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: /continue/i }));
    expect(currentStep()).toHaveTextContent("Interface");

    fireEvent.click(screen.getByRole("button", { name: /back/i }));
    expect(currentStep()).toHaveTextContent("Account");
    fireEvent.click(screen.getByRole("button", { name: /use a different account/i }));
    expect(screen.getByPlaceholderText("your-cern-username")).toBeInTheDocument();
  });

  it("skips the preferences without changing them", async () => {
    mockDaemon();
    const { onFinish } = renderWizard({
      account: ACCOUNT,
      daemon: daemonState({ folders: [{ Name: "home" } as DaemonState["folders"][number]] }),
    });

    const start = await screen.findByRole("button", { name: /get started/i });
    await waitFor(() => expect(start).toBeEnabled());
    fireEvent.click(start);
    fireEvent.click(screen.getByRole("button", { name: /continue/i })); // account
    fireEvent.click(screen.getByRole("button", { name: /continue/i })); // interface
    await waitFor(() => expect(invoke).toHaveBeenCalledWith("ipc_get_settings"));
    fireEvent.click(screen.getByRole("button", { name: "Skip" }));

    expect(currentStep()).toHaveTextContent("Finish");
    expect(invoke).not.toHaveBeenCalledWith("ipc_set_settings", expect.anything());
    expect(screen.getByText(/1 folder is kept in sync/)).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: /open cernbox sync/i }));
    expect(onFinish).toHaveBeenCalledWith(false);
  });

  it("does not wait for the desktop-client detection while the daemon is offline", async () => {
    mockDaemon();
    renderWizard({ daemon: daemonState({ daemonOnline: false }) });
    expect(screen.getByRole("button", { name: /get started/i })).toBeEnabled();
    expect(invoke).not.toHaveBeenCalledWith("ipc_legacy_detect", expect.anything());
  });
});
