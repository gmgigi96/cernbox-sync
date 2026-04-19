/**
 * FolderDetail page tests.
 *
 * Folders are pre-registered via the IPC proxy so we can focus on what the
 * detail page renders rather than the add-folder flow.
 */

import { test, expect } from "./fixture";
import { login, addFolderViaProxy, mkRemoteDir, rmRemoteDir } from "./helpers";

async function openFolderDetail(appPage: import("@playwright/test").Page, folderName: string) {
  // Navigate to Folders page; the daemon snapshot is fetched on mount so no
  // reload needed — just wait for the card to appear.
  await appPage.getByRole("button", { name: /Syncing Folders/i }).click();
  await appPage.getByText(folderName).first().waitFor({ timeout: 8_000 });
  // Click the card to open the detail view.
  await appPage.getByText(folderName).first().click();
  await expect(appPage.getByText("Last Synced")).toBeVisible({ timeout: 5_000 });
}

test.describe("FolderDetail page", () => {
  test("shows the four stat cards", async ({ appPage, daemon }) => {
    const remoteUrl = await mkRemoteDir();
    try {
      await addFolderViaProxy(daemon, "detail-cards", daemon.localDir, remoteUrl, appPage);
      await login(appPage);
      await openFolderDetail(appPage, "detail-cards");

      await expect(appPage.getByText("Last Synced")).toBeVisible();
      await expect(appPage.getByText("Synced Items")).toBeVisible();
      await expect(appPage.getByText("Conflicts")).toBeVisible();
      await expect(appPage.getByText("Status")).toBeVisible();
    } finally {
      await rmRemoteDir(remoteUrl);
    }
  });

  test("shows Never synced status on a fresh folder", async ({ appPage, daemon }) => {
    const remoteUrl = await mkRemoteDir();
    try {
      await addFolderViaProxy(daemon, "fresh-folder", daemon.localDir, remoteUrl, appPage);
      await login(appPage);
      await openFolderDetail(appPage, "fresh-folder");

      // Status card should show "Never" for a folder that has never synced.
      await expect(appPage.getByText("Never")).toBeVisible();
    } finally {
      await rmRemoteDir(remoteUrl);
    }
  });

  test("shows the Selective Sync section with a remote folder tree", async ({ appPage, daemon }) => {
    const remoteUrl = await mkRemoteDir();
    try {
      await addFolderViaProxy(daemon, "selective-test", daemon.localDir, remoteUrl, appPage);
      await login(appPage);
      await openFolderDetail(appPage, "selective-test");

      await expect(appPage.getByText("Selective Sync")).toBeVisible();
    } finally {
      await rmRemoteDir(remoteUrl);
    }
  });

  test("shows the Settings section with toggle controls", async ({ appPage, daemon }) => {
    const remoteUrl = await mkRemoteDir();
    try {
      await addFolderViaProxy(daemon, "settings-test", daemon.localDir, remoteUrl, appPage);
      await login(appPage);
      await openFolderDetail(appPage, "settings-test");

      await expect(appPage.getByText("Sync hidden files")).toBeVisible();
      await expect(appPage.getByText("Auto-sync on change")).toBeVisible();
    } finally {
      await rmRemoteDir(remoteUrl);
    }
  });

  test("toggling Sync hidden files changes the switch state", async ({ appPage, daemon }) => {
    const remoteUrl = await mkRemoteDir();
    try {
      await addFolderViaProxy(daemon, "toggle-test", daemon.localDir, remoteUrl, appPage);
      await login(appPage);
      await openFolderDetail(appPage, "toggle-test");

      // The "Sync hidden files" toggle uses role="switch".
      const toggle = appPage.getByRole("switch").first();
      const before = await toggle.getAttribute("aria-checked");
      await toggle.click();
      const after = await toggle.getAttribute("aria-checked");
      expect(after).not.toBe(before);
    } finally {
      await rmRemoteDir(remoteUrl);
    }
  });

  test("back button returns to the Folders list", async ({ appPage, daemon }) => {
    const remoteUrl = await mkRemoteDir();
    try {
      await addFolderViaProxy(daemon, "back-test", daemon.localDir, remoteUrl, appPage);
      await login(appPage);
      await openFolderDetail(appPage, "back-test");

      // Back button is an icon-only btn-icon at position 0 in the detail header.
      await appPage.locator(".btn-icon").first().click();
      await expect(appPage.getByRole("heading", { name: "Syncing Folders" })).toBeVisible({ timeout: 3_000 });
    } finally {
      await rmRemoteDir(remoteUrl);
    }
  });

  test("Sync Now button is present in the detail context menu", async ({ appPage, daemon }) => {
    const remoteUrl = await mkRemoteDir();
    try {
      await addFolderViaProxy(daemon, "syncnow-test", daemon.localDir, remoteUrl, appPage);
      await login(appPage);
      await openFolderDetail(appPage, "syncnow-test");

      // Open the MoreVertical context menu (second btn-icon, after the back button).
      await appPage.locator(".btn-icon").nth(1).click();
      await expect(appPage.getByRole("button", { name: /Sync Now/i })).toBeVisible({ timeout: 3_000 });
    } finally {
      await rmRemoteDir(remoteUrl);
    }
  });

  test("Recent Activity section is visible", async ({ appPage, daemon }) => {
    const remoteUrl = await mkRemoteDir();
    try {
      await addFolderViaProxy(daemon, "activity-test", daemon.localDir, remoteUrl, appPage);
      await login(appPage);
      await openFolderDetail(appPage, "activity-test");

      await expect(appPage.getByText("Recent Activity")).toBeVisible();
    } finally {
      await rmRemoteDir(remoteUrl);
    }
  });
});
