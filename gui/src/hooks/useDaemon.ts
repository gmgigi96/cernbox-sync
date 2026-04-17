import { useState, useEffect, useCallback } from "react";
import { ipc } from "../ipc";
import type { Folder, SyncStatus } from "../types";

export interface DaemonState {
  folders: Folder[];
  status: SyncStatus | null;
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
  const [folders, setFolders] = useState<Folder[]>([]);
  const [status, setStatus] = useState<SyncStatus | null>(null);
  const [daemonOnline, setDaemonOnline] = useState(false);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  const refresh = useCallback(async () => {
    try {
      const [f, s] = await Promise.all([ipc.list(), ipc.status()]);
      setFolders(f);
      setStatus(s);
      setDaemonOnline(true);
      setError(null);
    } catch (e) {
      setDaemonOnline(false);
      setError(String(e));
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    refresh();
    const id = setInterval(refresh, 5000);
    return () => clearInterval(id);
  }, [refresh]);

  const addFolder = useCallback(
    async (f: Pick<Folder, "Name" | "LocalRoot" | "RemoteBase" | "Folders">) => {
      await ipc.add(f.Name, f.LocalRoot, f.RemoteBase, f.Folders.length > 0 ? f.Folders : undefined);
      await refresh();
    },
    [refresh],
  );

  const updateFolder = useCallback(
    async (f: Folder) => {
      await ipc.update(f.Name, f.LocalRoot, f.RemoteBase, f.Folders.length > 0 ? f.Folders : undefined, f.Settings.sync_hidden_files);
      await refresh();
    },
    [refresh],
  );

  const removeFolder = useCallback(
    async (name: string) => {
      await ipc.remove(name);
      await refresh();
    },
    [refresh],
  );

  const syncFolder = useCallback(
    async (name?: string) => {
      await ipc.sync(name);
      await refresh();
    },
    [refresh],
  );

  return { folders, status, daemonOnline, loading, error, refresh, addFolder, updateFolder, removeFolder, syncFolder };
}
