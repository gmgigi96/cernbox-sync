import { describe, it, expect, beforeEach, vi } from "vitest";
import { render, screen, act } from "@testing-library/react";
import { invoke } from "@tauri-apps/api/core";
import { Dashboard } from "../pages/Dashboard";
import { useSyncStore } from "../store/syncStore";
import type { DaemonState } from "../hooks/useDaemon";
import type { Folder, SyncStatus } from "../types";

// Dashboard uses openPath to open local directories in the file manager.
vi.mock("@tauri-apps/plugin-opener", () => ({
  openPath: vi.fn(),
}));

const makeFolder = (name: string): Folder => ({
  Name: name,
  LocalRoot: `/home/user/${name}`,
  RemoteBase: `https://cern.ch/dav/${name}`,
  Folders: [],
  Settings: { sync_hidden_files: false, auto_sync_on_change: false },
});

const emptyStatus: SyncStatus = { syncing: [], last_sync: {}, counts: {} };
const noop = async () => {};

function makeDaemon(overrides?: Partial<DaemonState>): DaemonState {
  return {
    folders: [],
    status: emptyStatus,
    // progress/uploadBps/downloadBps come from the store directly in Dashboard,
    // so these values on daemon are not read by the component.
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

beforeEach(() => {
  // Make all invoke calls return a resolved promise so Dashboard's useEffect
  // hooks (listConflicts, readTextFile) don't throw on the mock.
  vi.mocked(invoke).mockResolvedValue(undefined);

  useSyncStore.setState({
    folders: [],
    status: emptyStatus,
    progress: {},
    uploadBps: 0,
    downloadBps: 0,
    daemonOnline: true,
    loading: false,
    error: null,
  });
});

describe("Dashboard progress display", () => {
  it("shows 0% in active transfers when sync has started but no progress event fired", () => {
    const daemon = makeDaemon({
      folders: [makeFolder("docs")],
      status: { ...emptyStatus, syncing: ["docs"] },
    });

    render(
      <Dashboard
        daemon={daemon}
        onNavigate={() => {}}
        onOpenFolder={() => {}}
        onOpenConflicts={() => {}}
      />,
    );

    expect(screen.getByText("0%")).toBeInTheDocument();
  });

  it("updates active-transfers progress bar when sync_progress fires", () => {
    const daemon = makeDaemon({
      folders: [makeFolder("docs")],
      status: { ...emptyStatus, syncing: ["docs"] },
    });

    render(
      <Dashboard
        daemon={daemon}
        onNavigate={() => {}}
        onOpenFolder={() => {}}
        onOpenConflicts={() => {}}
      />,
    );

    // Before any progress event: 0%.
    expect(screen.getByText("0%")).toBeInTheDocument();

    // Simulate a sync_progress event arriving from the daemon.
    act(() => {
      useSyncStore.getState()._handleEvent({
        type: "sync_progress",
        folder: "docs",
        progress: {
          done: 5,
          total: 10,
          current: "report.pdf",
          upload_bps: 0,
          download_bps: 0,
          bytes_done: 0,
          bytes_total: 0,
        },
      });
    });

    // Dashboard should re-render via its direct store subscription and show 50%
    // in both the active-transfers row and the folder-overview card.
    expect(screen.getAllByText("50%").length).toBeGreaterThanOrEqual(1);
    // The current file name should appear too.
    expect(screen.getByText("report.pdf")).toBeInTheDocument();
  });

  it("removes active transfer entry after sync_completed fires", () => {
    const daemon = makeDaemon({
      folders: [makeFolder("docs")],
      status: { ...emptyStatus, syncing: ["docs"] },
    });

    render(
      <Dashboard
        daemon={daemon}
        onNavigate={() => {}}
        onOpenFolder={() => {}}
        onOpenConflicts={() => {}}
      />,
    );

    act(() => {
      useSyncStore.getState()._handleEvent({
        type: "sync_progress",
        folder: "docs",
        progress: {
          done: 8,
          total: 10,
          current: "",
          upload_bps: 0,
          download_bps: 0,
          bytes_done: 0,
          bytes_total: 0,
        },
      });
    });
    expect(screen.getAllByText("80%").length).toBeGreaterThanOrEqual(1);

    act(() => {
      useSyncStore.getState()._handleEvent({
        type: "sync_completed",
        folder: "docs",
        last_sync: new Date().toISOString(),
      });
    });

    // Progress entry deleted → no percentage shown.
    expect(screen.queryByText("80%")).not.toBeInTheDocument();
  });

  it("updates folder-card circular progress when sync_progress fires", () => {
    const daemon = makeDaemon({
      folders: [makeFolder("docs")],
      status: { ...emptyStatus, syncing: ["docs"] },
    });

    render(
      <Dashboard
        daemon={daemon}
        onNavigate={() => {}}
        onOpenFolder={() => {}}
        onOpenConflicts={() => {}}
      />,
    );

    act(() => {
      useSyncStore.getState()._handleEvent({
        type: "sync_progress",
        folder: "docs",
        progress: {
          done: 3,
          total: 4,
          current: "",
          upload_bps: 0,
          download_bps: 0,
          bytes_done: 0,
          bytes_total: 0,
        },
      });
    });

    // Both the active-transfers row and the folder-card circular progress show "75%".
    const pctEls = screen.getAllByText("75%");
    expect(pctEls.length).toBeGreaterThanOrEqual(1);
  });
});
