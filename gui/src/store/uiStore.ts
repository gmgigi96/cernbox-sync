import { create } from "zustand";
import { persist } from "zustand/middleware";
import { DEFAULT_SERVER_URL } from "../servers";

/**
 * "simple"   — Nextcloud-style list of sync connections; add/modify only.
 * "advanced" — sidebar layout with dashboard, statistics and folder details.
 */
export type ViewMode = "simple" | "advanced";

export interface UiState {
  viewMode: ViewMode;
  setViewMode: (mode: ViewMode) => void;
  /** CERNBox server the GUI signs in to and browses. Folders keep their own full URL. */
  serverUrl: string;
  setServerUrl: (url: string) => void;
}

/** localStorage key holding the persisted GUI preferences. */
export const UI_STORAGE_KEY = "cernbox-sync-ui";

// GUI-only preferences. These live in the webview's local storage rather than
// the daemon config DB: the daemon only knows about synchronization.
export const useUiStore = create<UiState>()(
  persist(
    (set) => ({
      viewMode: "simple",
      setViewMode: (viewMode) => set({ viewMode }),
      serverUrl: DEFAULT_SERVER_URL,
      setServerUrl: (serverUrl) => set({ serverUrl }),
    }),
    { name: UI_STORAGE_KEY },
  ),
);
