import { describe, it, expect, beforeEach } from "vitest";
import { useSyncStore } from "../store/syncStore";
import type { Folder, SyncStatus } from "../types";

const makeFolder = (name: string): Folder => ({
  Name: name,
  LocalRoot: `/home/user/${name}`,
  RemoteBase: `https://cern.ch/dav/${name}`,
  Folders: [],
  Settings: { sync_hidden_files: false, auto_sync_on_change: false },
});

const emptyStatus: SyncStatus = { syncing: [], last_sync: {}, counts: {} };

beforeEach(() => {
  useSyncStore.setState({
    folders: [],
    status: emptyStatus,
    progress: {},
    daemonOnline: false,
    loading: true,
    error: null,
  });
});

describe("_applySnapshot", () => {
  it("sets folders, status, marks daemon online and loading false", () => {
    const folders = [makeFolder("docs"), makeFolder("photos")];
    const status: SyncStatus = {
      syncing: ["docs"],
      last_sync: { docs: "2024-01-01T00:00:00Z" },
      counts: { docs: { files: 10, dirs: 2, size: 1024 } },
    };
    useSyncStore.getState()._applySnapshot(folders, status);
    const s = useSyncStore.getState();
    expect(s.folders).toEqual(folders);
    expect(s.status).toEqual(status);
    expect(s.daemonOnline).toBe(true);
    expect(s.loading).toBe(false);
    expect(s.error).toBeNull();
  });
});

describe("_setDaemonOffline", () => {
  it("marks daemon offline and stores the error message", () => {
    useSyncStore.getState()._setDaemonOffline("connection refused");
    const s = useSyncStore.getState();
    expect(s.daemonOnline).toBe(false);
    expect(s.loading).toBe(false);
    expect(s.error).toBe("connection refused");
  });
});

describe("_handleEvent", () => {
  describe("sync_started", () => {
    it("adds the folder name to syncing list", () => {
      useSyncStore.getState()._handleEvent({ type: "sync_started", folder: "docs" });
      expect(useSyncStore.getState().status.syncing).toContain("docs");
    });

    it("does not duplicate if folder already syncing", () => {
      useSyncStore.setState({ status: { ...emptyStatus, syncing: ["docs"] } });
      useSyncStore.getState()._handleEvent({ type: "sync_started", folder: "docs" });
      expect(useSyncStore.getState().status.syncing).toEqual(["docs"]);
    });
  });

  describe("sync_progress", () => {
    it("stores progress for the folder", () => {
      useSyncStore.getState()._handleEvent({
        type: "sync_progress",
        folder: "docs",
        progress: { done: 3, total: 10, current: "file.txt", upload_bps: 0, download_bps: 0, bytes_done: 0, bytes_total: 0 },
      });
      expect(useSyncStore.getState().progress["docs"]).toEqual({ done: 3, total: 10, current: "file.txt", upload_bps: 0, download_bps: 0, bytes_done: 0, bytes_total: 0 });
    });

    it("overwrites previous progress for the same folder", () => {
      useSyncStore.setState({ progress: { docs: { done: 1, total: 10, current: "a.txt", upload_bps: 0, download_bps: 0, bytes_done: 0, bytes_total: 0 } } });
      useSyncStore.getState()._handleEvent({
        type: "sync_progress",
        folder: "docs",
        progress: { done: 5, total: 10, current: "b.txt", upload_bps: 0, download_bps: 0, bytes_done: 0, bytes_total: 0 },
      });
      expect(useSyncStore.getState().progress["docs"].done).toBe(5);
    });
  });

  describe("sync_completed", () => {
    it("removes folder from syncing, clears its progress, records last_sync and counts", () => {
      useSyncStore.setState({
        status: { ...emptyStatus, syncing: ["docs", "photos"] },
        progress: { docs: { done: 5, total: 5, current: "", upload_bps: 0, download_bps: 0, bytes_done: 0, bytes_total: 0 } },
      });
      useSyncStore.getState()._handleEvent({
        type: "sync_completed",
        folder: "docs",
        last_sync: "2024-06-01T12:00:00Z",
        counts: { files: 20, dirs: 3, size: 2048 },
      });
      const s = useSyncStore.getState();
      expect(s.status.syncing).toEqual(["photos"]);
      expect(s.progress["docs"]).toBeUndefined();
      expect(s.status.last_sync["docs"]).toBe("2024-06-01T12:00:00Z");
      expect(s.status.counts["docs"]).toEqual({ files: 20, dirs: 3, size: 2048 });
    });

    it("preserves counts when event has no counts", () => {
      useSyncStore.setState({
        status: {
          ...emptyStatus,
          syncing: ["docs"],
          counts: { docs: { files: 5, dirs: 1, size: 512 } },
        },
      });
      useSyncStore.getState()._handleEvent({
        type: "sync_completed",
        folder: "docs",
        last_sync: "2024-06-01T12:00:00Z",
      });
      expect(useSyncStore.getState().status.counts["docs"]).toEqual({ files: 5, dirs: 1, size: 512 });
    });
  });

  describe("sync_failed", () => {
    it("removes folder from syncing and clears its progress", () => {
      useSyncStore.setState({
        status: { ...emptyStatus, syncing: ["docs"] },
        progress: { docs: { done: 2, total: 10, current: "x.txt", upload_bps: 0, download_bps: 0, bytes_done: 0, bytes_total: 0 } },
      });
      useSyncStore.getState()._handleEvent({ type: "sync_failed", folder: "docs" });
      const s = useSyncStore.getState();
      expect(s.status.syncing).not.toContain("docs");
      expect(s.progress["docs"]).toBeUndefined();
    });
  });

  describe("folder_added", () => {
    it("appends a new folder", () => {
      const f = makeFolder("music");
      useSyncStore.getState()._handleEvent({ type: "folder_added", folder_data: f });
      expect(useSyncStore.getState().folders).toContainEqual(f);
    });

    it("replaces an existing folder with the same name", () => {
      const old = makeFolder("music");
      useSyncStore.setState({ folders: [old] });
      const updated = { ...old, LocalRoot: "/new/path" };
      useSyncStore.getState()._handleEvent({ type: "folder_added", folder_data: updated });
      const folders = useSyncStore.getState().folders;
      expect(folders).toHaveLength(1);
      expect(folders[0].LocalRoot).toBe("/new/path");
    });
  });

  describe("folder_removed", () => {
    it("removes the folder by name", () => {
      useSyncStore.setState({ folders: [makeFolder("docs"), makeFolder("photos")] });
      useSyncStore.getState()._handleEvent({ type: "folder_removed", folder: "docs" });
      const folders = useSyncStore.getState().folders;
      expect(folders).toHaveLength(1);
      expect(folders[0].Name).toBe("photos");
    });
  });

  describe("folder_updated", () => {
    it("replaces the matching folder in-place", () => {
      useSyncStore.setState({ folders: [makeFolder("docs")] });
      const updated = { ...makeFolder("docs"), LocalRoot: "/updated/path" };
      useSyncStore.getState()._handleEvent({ type: "folder_updated", folder_data: updated });
      expect(useSyncStore.getState().folders[0].LocalRoot).toBe("/updated/path");
    });
  });

  describe("unknown event type", () => {
    it("returns empty update (state unchanged)", () => {
      const before = useSyncStore.getState().folders;
      useSyncStore.getState()._handleEvent({ type: "unknown_event" });
      expect(useSyncStore.getState().folders).toBe(before);
    });
  });
});
