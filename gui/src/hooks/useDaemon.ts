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
  addFolder: (f: Omit<Folder, "Password"> & { Password: string }) => Promise<void>;
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
    async (f: Folder) => {
      await ipc.add(f.Name, f.LocalRoot, f.RemoteBase, f.Username, f.Password);
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

  return { folders, status, daemonOnline, loading, error, refresh, addFolder, removeFolder, syncFolder };
}
