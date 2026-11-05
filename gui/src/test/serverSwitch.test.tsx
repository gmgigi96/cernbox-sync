import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, fireEvent, waitFor, within } from "@testing-library/react";
import { invoke } from "@tauri-apps/api/core";
import { App } from "../App";
import { markSetupDone } from "../pages/SetupWizard";
import { DEFAULT_SERVER_URL } from "../servers";
import { useUiStore } from "../store/uiStore";

vi.mock("@tauri-apps/api/event", () => ({
  listen: vi.fn(async () => () => {}),
}));
vi.mock("@tauri-apps/plugin-opener", () => ({
  openUrl: vi.fn(async () => undefined),
}));

const QA = "https://qa.cernbox.cern.ch";
const FOLDER = {
  Name: "Documents",
  LocalRoot: "/home/user/Documents",
  RemoteBase: "https://cernbox.cern.ch/remote.php/dav/spaces/1",
  Folders: [],
  Settings: {},
};

beforeEach(() => {
  localStorage.clear();
  markSetupDone();
  useUiStore.setState({ serverUrl: DEFAULT_SERVER_URL });
  vi.mocked(invoke).mockReset();
  vi.mocked(invoke).mockImplementation(async (cmd) => {
    switch (cmd) {
      case "ipc_get_account":
        return { username: "einstein", password: "app-password" };
      case "ipc_get_snapshot":
        return { folders: [FOLDER], status: { syncing: [], last_sync: {}, counts: {} } };
      case "ipc_list":
        return [FOLDER];
      case "login_flow_start":
        return `${QA}/index.php/login/v2/flow/token`;
      case "login_flow_poll":
        return "einstein";
      default:
        return undefined;
    }
  });
});

async function chooseQa() {
  render(<App />);
  fireEvent.click(await screen.findByRole("button", { name: "Settings" }));
  fireEvent.click(await screen.findByRole("button", { name: "Change server" }));
  fireEvent.change(screen.getByLabelText("Server"), { target: { value: QA } });
  fireEvent.click(screen.getByRole("button", { name: "Continue to sign in" }));
  await screen.findByRole("button", { name: /Sign in with browser/i });
}

describe("switching server from the settings", () => {
  it("switches once the sign-in succeeded and shows the server in the sidebar", async () => {
    await chooseQa();
    expect(useUiStore.getState().serverUrl).toBe(DEFAULT_SERVER_URL);

    fireEvent.click(screen.getByRole("button", { name: /Sign in with browser/i }));

    await waitFor(() => expect(useUiStore.getState().serverUrl).toBe(QA));
    expect(invoke).toHaveBeenCalledWith("login_flow_start", { serverUrl: QA });
    expect(await within(screen.getByRole("complementary")).findByText("CERNBox QA")).toBeInTheDocument();
  });

  it("keeps the current server when the sign-in is cancelled", async () => {
    await chooseQa();

    fireEvent.click(screen.getByRole("button", { name: "Cancel" }));

    expect(await screen.findByRole("button", { name: "Change server" })).toBeInTheDocument();
    expect(useUiStore.getState().serverUrl).toBe(DEFAULT_SERVER_URL);
    expect(screen.queryByTitle(QA)).not.toBeInTheDocument();
  });
});
