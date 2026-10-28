import { describe, it, expect, beforeEach } from "vitest";
import { useSyncStore } from "../store/syncStore";

// Plan 1.3: when the daemon reports an authentication failure, the GUI must
// ask the user to log in again. Assumed API (to be added by the implementer):
//   - useSyncStore state field `authRequired: boolean` (default false)
//   - set to true by a `sync_failed` event whose `error` contains "unauthorized"
//   - action `clearAuthRequired()` resets it (and `_applySnapshot` does not touch it)
//   - useDaemon() exposes `authRequired`

beforeEach(() => {
  useSyncStore.setState({
    folders: [],
    status: { syncing: [], last_sync: {}, counts: {} },
    progress: {},
    error: null,
    authRequired: false,
  } as never);
});

const state = () => useSyncStore.getState() as unknown as {
  authRequired: boolean;
  clearAuthRequired: () => void;
  _handleEvent: (e: object) => void;
};

describe("auth failure (plan 1.3)", () => {
  it("sets authRequired on a sync_failed event carrying an unauthorized error", () => {
    state()._handleEvent({
      type: "sync_failed",
      folder: "docs",
      error: "remote scan: webdav: unauthorized",
    });
    expect(state().authRequired).toBe(true);
  });

  it("does not set authRequired for an unrelated sync failure", () => {
    state()._handleEvent({ type: "sync_failed", folder: "docs", error: "disk full" });
    expect(state().authRequired).toBe(false);
  });

  it("clearAuthRequired resets the flag", () => {
    state()._handleEvent({ type: "sync_failed", folder: "docs", error: "webdav: unauthorized" });
    state().clearAuthRequired();
    expect(state().authRequired).toBe(false);
  });
});
