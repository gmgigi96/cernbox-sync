import { create } from "zustand";
import { persist } from "zustand/middleware";

/**
 * "simple"   — Nextcloud-style list of sync connections; add/modify only.
 * "advanced" — sidebar layout with dashboard, statistics and folder details.
 */
export type ViewMode = "simple" | "advanced";

export interface UiState {
  viewMode: ViewMode;
  setViewMode: (mode: ViewMode) => void;
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
    }),
    { name: UI_STORAGE_KEY },
  ),
);
