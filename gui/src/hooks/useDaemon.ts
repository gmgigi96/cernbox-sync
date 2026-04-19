import { useCallback } from "react";
import { ipc } from "../ipc";
import { useSyncStore, type SyncProgress } from "../store/syncStore";
import type { Folder, SyncStatus } from "../types";

export interface DaemonState {
  folders: Folder[];
  status: SyncStatus;
  /** Real-time progress per folder name — only present while a sync is running. */
  progress: Record<string, SyncProgress>;
  /** Aggregate upload bytes/sec across all currently-syncing folders. */
  uploadBps: number;
  /** Aggregate download bytes/sec across all currently-syncing folders. */
  downloadBps: number;
  daemonOnline: boolean;
  loading: boolean;
  error: string | null;
  refresh: () => Promise<void>;
  addFolder: (f: Pick<Folder, "Name" | "LocalRoot" | "RemoteBase" | "Folders">) => Promise<void>;
  updateFolder: (f: Folder) => Promise<void>;
  removeFolder: (name: string) => Promise<void>;
  syncFolder: (name?: string) => Promise<void>;
}

export function useDaemon(): DaemonState {
  const { folders, status, progress, uploadBps, downloadBps, daemonOnline, loading, error } = useSyncStore();

  // Manual refresh — re-fetches full state from the daemon directly.
  // Useful as a fallback when reconnecting after a missed event.
  const refresh = useCallback(async () => {
    try {
      const [f, s] = await Promise.all([ipc.list(), ipc.status()]);
      useSyncStore.getState()._applySnapshot(f, s);
    } catch (e) {
      useSyncStore.getState()._setDaemonOffline(String(e));
    }
  }, []);

  const addFolder = useCallback(
    async (f: Pick<Folder, "Name" | "LocalRoot" | "RemoteBase" | "Folders">) => {
      await ipc.add(f.Name, f.LocalRoot, f.RemoteBase, f.Folders.length > 0 ? f.Folders : undefined);
      // State is updated automatically via the folder_added event from the daemon.
    },
    [],
  );

  const updateFolder = useCallback(async (f: Folder) => {
    await ipc.update(
      f.Name,
      f.LocalRoot,
      f.RemoteBase,
      f.Folders.length > 0 ? f.Folders : undefined,
      f.Settings.sync_hidden_files,
      f.Settings.auto_sync_on_change,
    );
    // State is updated automatically via the folder_updated event from the daemon.
  }, []);

  const removeFolder = useCallback(async (name: string) => {
    await ipc.remove(name);
    // State is updated automatically via the folder_removed event from the daemon.
  }, []);

  const syncFolder = useCallback(async (name?: string) => {
    await ipc.sync(name);
    // State is updated automatically via sync_started / sync_completed events.
  }, []);

  return {
    folders,
    status,
    progress,
    uploadBps,
    downloadBps,
    daemonOnline,
    loading,
    error,
    refresh,
    addFolder,
    updateFolder,
    removeFolder,
    syncFolder,
  };
}
