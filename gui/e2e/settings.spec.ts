/**
 * Settings page tests.
 *
 * These tests interact with the real daemon's settings over IPC, so they use
 * the dev environment (make dev-up) but do not need the WebDAV server.
 */

import { test, expect } from "./fixture";
import { login, goToSettings } from "./helpers";

test.describe("Settings page", () => {
  test("shows the Settings page with all six sections", async ({ appPage }) => {
    await login(appPage);
    await goToSettings(appPage);

    await expect(appPage.getByText("Sync Interval")).toBeVisible();
    await expect(appPage.getByText("Log Rotation Time")).toBeVisible();
    await expect(appPage.getByText("Upload Bandwidth")).toBeVisible();
    await expect(appPage.getByText("Download Bandwidth")).toBeVisible();
    await expect(appPage.getByText("Transfer Streams")).toBeVisible();
    await expect(appPage.getByText("Metadata Streams")).toBeVisible();
  });

  test("loads the current sync interval from the daemon", async ({ appPage }) => {
    await login(appPage);
    await goToSettings(appPage);

    // The daemon is started with -interval 24h, which matches the daemon
    // setting set at startup. The default shown in the UI input is "5m" when
    // the daemon hasn't stored a value yet.
    const input = appPage.locator("input[placeholder='e.g. 5m']");
    await expect(input).toBeVisible();
    const value = await input.inputValue();
    // Any valid Go duration string (e.g. "5m", "1h", "24h0m0s").
    expect(value).toMatch(/^\d/);
  });

  test("Save button appears only after a field is changed", async ({ appPage }) => {
    await login(appPage);
    await goToSettings(appPage);

    // No dirty state initially.
    await expect(appPage.getByRole("button", { name: "Save" })).not.toBeVisible();

    // Modify the sync interval.
    const input = appPage.locator("input[placeholder='e.g. 5m']");
    await input.click({ clickCount: 3 });
    await input.fill("10m");

    await expect(appPage.getByRole("button", { name: "Save" })).toBeVisible();
  });

  test("Cancel reverts unsaved changes", async ({ appPage }) => {
    await login(appPage);
    await goToSettings(appPage);

    const input = appPage.locator("input[placeholder='e.g. 5m']");
    const original = await input.inputValue();

    await input.click({ clickCount: 3 });
    await input.fill("2h");
    await expect(appPage.getByRole("button", { name: "Cancel" })).toBeVisible();
    await appPage.getByRole("button", { name: "Cancel" }).click();

    expect(await input.inputValue()).toBe(original);
    await expect(appPage.getByRole("button", { name: "Save" })).not.toBeVisible();
  });

  test("Save persists the new sync interval to the daemon", async ({ appPage }) => {
    await login(appPage);
    await goToSettings(appPage);

    const input = appPage.locator("input[placeholder='e.g. 5m']");
    await input.click({ clickCount: 3 });
    await input.fill("15m");
    await appPage.getByRole("button", { name: "Save" }).click();

    // "Settings saved." toast appears.
    await expect(appPage.getByText("Settings saved.")).toBeVisible({ timeout: 5_000 });
    // Save button disappears (no longer dirty).
    await expect(appPage.getByRole("button", { name: "Save" })).not.toBeVisible({ timeout: 3_000 });

    // Reload and navigate back to Settings to verify the persisted value.
    await appPage.reload();
    await login(appPage);
    await goToSettings(appPage);
    const reloaded = appPage.locator("input[placeholder='e.g. 5m']");
    // Go formats the duration back as "15m0s" when round-tripping through the daemon.
    await expect(reloaded).toHaveValue(/^15m/, { timeout: 5_000 });
  });

  test("Reset to defaults restores all fields and shows saved toast", async ({ appPage }) => {
    await login(appPage);
    await goToSettings(appPage);

    // First dirty the state.
    const input = appPage.locator("input[placeholder='e.g. 5m']");
    await input.click({ clickCount: 3 });
    await input.fill("99m");
    await appPage.getByRole("button", { name: "Save" }).click();
    await appPage.getByText("Settings saved.").waitFor({ state: "visible" });

    // Now reset.
    await appPage.getByRole("button", { name: /Reset to defaults/i }).click();
    await expect(appPage.getByText("Settings saved.")).toBeVisible({ timeout: 5_000 });

    await expect(appPage.locator("input[placeholder='e.g. 5m']")).toHaveValue(/^5m/);
  });

  test("changing transfer streams updates input correctly", async ({ appPage }) => {
    await login(appPage);
    await goToSettings(appPage);

    const input = appPage.locator("input[placeholder='1']").first();
    await input.fill("4");

    await expect(appPage.getByRole("button", { name: "Save" })).toBeVisible();
    await expect(input).toHaveValue("4");
  });
});
