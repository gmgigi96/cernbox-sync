import { invoke } from "@tauri-apps/api/core";
import type { Account, Folder, SyncStatus } from "./types";

export const ipc = {
  list(): Promise<Folder[]> {
    return invoke<Folder[]>("ipc_list");
  },
  add(name: string, localRoot: string, remoteBase: string, folders?: string[], syncHiddenFiles?: boolean): Promise<void> {
    return invoke("ipc_add", { name, localRoot, remoteBase, folders: folders ?? null, syncHiddenFiles: syncHiddenFiles ?? null });
  },
  getAccount(): Promise<Account | null> {
    return invoke<Account | null>("ipc_get_account");
  },
  setAccount(username: string, password: string): Promise<void> {
    return invoke("ipc_set_account", { username, password });
  },
  update(name: string, localRoot: string, remoteBase: string, folders?: string[], syncHiddenFiles?: boolean): Promise<void> {
    return invoke("ipc_update", { name, localRoot, remoteBase, folders: folders ?? null, syncHiddenFiles: syncHiddenFiles ?? null });
  },
  remove(name: string): Promise<void> {
    return invoke("ipc_remove", { name });
  },
  sync(name?: string): Promise<void> {
    return invoke("ipc_sync", { name: name ?? null });
  },
  status(): Promise<SyncStatus> {
    return invoke<SyncStatus>("ipc_status");
  },
  stop(): Promise<void> {
    return invoke("ipc_stop");
  },
  getSettings(): Promise<string | null> {
    return invoke<string | null>("ipc_get_settings");
  },
  setSettings(logRotateMaxAge: string | null): Promise<void> {
    return invoke("ipc_set_settings", { logRotateMaxAge });
  },
  listLocalDir(path?: string): Promise<LocalEntry[]> {
    return invoke<LocalEntry[]>("list_local_dir", { path: path ?? null });
  },
  createLocalDir(parent: string, name: string): Promise<string> {
    return invoke<string>("create_local_dir", { parent, name });
  },
  openLogFile(path: string): Promise<void> {
    return invoke("open_log_file", { path });
  },

};

export interface LocalEntry {
  name: string;
  path: string;
  is_dir: boolean;
}
