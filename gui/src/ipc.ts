import { invoke } from "@tauri-apps/api/core";
import type { Folder, SyncStatus } from "./types";

export const ipc = {
  list(): Promise<Folder[]> {
    return invoke<Folder[]>("ipc_list");
  },
  add(
    name: string,
    local_root: string,
    remote_base: string,
    username: string,
    password: string,
  ): Promise<void> {
    return invoke("ipc_add", { name, local_root, remote_base, username, password });
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
  setSettings(log_rotate_max_age: string | null): Promise<void> {
    return invoke("ipc_set_settings", { log_rotate_max_age });
  },
};
