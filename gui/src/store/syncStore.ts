import { create } from "zustand";
import type { Folder, SyncStatus, FileCounts } from "../types";

// ── Types ──────────────────────────────────────────────────────────────────────

export interface SyncProgress {
  done: number;
  total: number;
  current: string;
  upload_bps: number;
  download_bps: number;
  bytes_done: number;
  bytes_total: number;
}

export interface DaemonSnapshot {
  ok: boolean;
  folders: Folder[];
  status?: SyncStatus;
}

export interface DaemonEvent {
  type: string;
  folder?: string;
  folder_data?: Folder;
  progress?: SyncProgress;
  last_sync?: string;
  counts?: FileCounts;
  error?: string;
}

// ── Store ──────────────────────────────────────────────────────────────────────

export interface SyncState {
  folders: Folder[];
  status: SyncStatus;
  /** Per-folder real-time sync progress (only present while syncing). */
  progress: Record<string, SyncProgress>;
  /** Aggregate upload bytes/sec across all currently-syncing folders. */
  uploadBps: number;
  /** Aggregate download bytes/sec across all currently-syncing folders. */
  downloadBps: number;
  daemonOnline: boolean;
  loading: boolean;
  error: string | null;

  // Internal updaters — prefixed with _ to signal they are not for direct use.
  _applySnapshot: (folders: Folder[], status: SyncStatus) => void;
  _setDaemonOffline: (error: string) => void;
  _handleEvent: (event: DaemonEvent) => void;
}

const emptyStatus: SyncStatus = { syncing: [], last_sync: {}, counts: {} };

export const useSyncStore = create<SyncState>((set) => ({
  folders: [],
  status: emptyStatus,
  progress: {},
  uploadBps: 0,
  downloadBps: 0,
  daemonOnline: false,
  loading: true,
  error: null,

  _applySnapshot: (folders, status) =>
    set({ folders, status, daemonOnline: true, loading: false, error: null }),

  _setDaemonOffline: (error) =>
    set({ daemonOnline: false, loading: false, error }),

  _handleEvent: (event) =>
    set((state) => {
      switch (event.type) {
        case "sync_started": {
          const name = event.folder!;
          return {
            status: {
              ...state.status,
              syncing: state.status.syncing.includes(name)
                ? state.status.syncing
                : [...state.status.syncing, name],
            },
          };
        }

        case "sync_progress": {
          const name = event.folder!;
          const newProgress = { ...state.progress, [name]: event.progress! };
          let up = 0, down = 0;
          for (const p of Object.values(newProgress)) {
            up += p.upload_bps ?? 0;
            down += p.download_bps ?? 0;
          }
          return { progress: newProgress, uploadBps: up, downloadBps: down };
        }

        case "sync_completed": {
          const name = event.folder!;
          const newProgress = { ...state.progress };
          delete newProgress[name];
          let up = 0, down = 0;
          for (const p of Object.values(newProgress)) { up += p.upload_bps ?? 0; down += p.download_bps ?? 0; }
          return {
            status: {
              ...state.status,
              syncing: state.status.syncing.filter((n) => n !== name),
              last_sync: { ...state.status.last_sync, [name]: event.last_sync! },
              counts: event.counts
                ? { ...state.status.counts, [name]: event.counts }
                : state.status.counts,
            },
            progress: newProgress,
            uploadBps: up,
            downloadBps: down,
          };
        }

        case "sync_failed": {
          const name = event.folder!;
          const newProgress = { ...state.progress };
          delete newProgress[name];
          let up = 0, down = 0;
          for (const p of Object.values(newProgress)) { up += p.upload_bps ?? 0; down += p.download_bps ?? 0; }
          return {
            status: {
              ...state.status,
              syncing: state.status.syncing.filter((n) => n !== name),
            },
            progress: newProgress,
            uploadBps: up,
            downloadBps: down,
          };
        }

        case "folder_added": {
          const f = event.folder_data!;
          return {
            folders: [
              ...state.folders.filter((x) => x.Name !== f.Name),
              f,
            ],
          };
        }

        case "folder_removed": {
          const name = event.folder!;
          return {
            folders: state.folders.filter((f) => f.Name !== name),
          };
        }

        case "folder_updated": {
          const f = event.folder_data!;
          return {
            folders: state.folders.map((x) => (x.Name === f.Name ? f : x)),
          };
        }

        case "sync_paused": {
          if (!event.folder) {
            // Global pause
            return {
              status: { ...state.status, global_paused: true },
            };
          }
          // Per-folder pause: update the folder's settings in the list
          const f = event.folder_data;
          return {
            folders: f
              ? state.folders.map((x) => (x.Name === f.Name ? f : x))
              : state.folders,
            status: {
              ...state.status,
              paused_folders: [
                ...(state.status.paused_folders ?? []).filter((n) => n !== event.folder),
                event.folder,
              ],
            },
          };
        }

        case "sync_resumed": {
          if (!event.folder) {
            // Global resume
            return {
              status: { ...state.status, global_paused: false },
            };
          }
          const f = event.folder_data;
          return {
            folders: f
              ? state.folders.map((x) => (x.Name === f.Name ? f : x))
              : state.folders,
            status: {
              ...state.status,
              paused_folders: (state.status.paused_folders ?? []).filter((n) => n !== event.folder),
            },
          };
        }

        default:
          return {};
      }
    }),
}));
