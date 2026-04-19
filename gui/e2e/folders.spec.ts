/**
 * Folders page tests.
 *
 * Tests use a real daemon and the dev WebDAV server (einstein / relativity).
 */

import { test, expect } from "./fixture";
import { login, goToFolders, addFolderViaProxy, mkRemoteDir, rmRemoteDir } from "./helpers";

test.describe("Folders page", () => {
  test("shows empty state when no folders are registered", async ({ appPage }) => {
    await login(appPage);
    await goToFolders(appPage);

    await expect(appPage.getByText("No sync folders yet")).toBeVisible();
    await expect(appPage.getByRole("button", { name: "Add Sync Folder" }).first()).toBeVisible();
  });

  test("shows a folder card after a folder is registered", async ({ appPage, daemon }) => {
    const remoteUrl = await mkRemoteDir();
    try {
      await addFolderViaProxy(daemon, "my-docs", daemon.localDir, remoteUrl, appPage);
      await login(appPage);
      await goToFolders(appPage);

      await expect(appPage.getByText("my-docs")).toBeVisible({ timeout: 8_000 });
      await expect(appPage.getByText("Never synced")).toBeVisible();
    } finally {
      await rmRemoteDir(remoteUrl);
    }
  });

  test("remove folder dialog appears and cancels correctly", async ({ appPage, daemon }) => {
    const remoteUrl = await mkRemoteDir();
    try {
      await addFolderViaProxy(daemon, "removeme", daemon.localDir, remoteUrl, appPage);
      await login(appPage);
      await goToFolders(appPage);
      await appPage.getByText("removeme").waitFor({ timeout: 8_000 });

      // Open the context menu (MoreVertical icon-only button on the folder card).
      await appPage.locator(".btn-icon").first().click();
      // Use the visible context menu item.
      await appPage.getByRole("button", { name: "Remove Folder" }).click();

      // Confirm dialog should appear.
      await expect(appPage.getByText(/Remove "removeme"\?/)).toBeVisible();

      // Cancel — folder should remain.
      await appPage.getByRole("button", { name: "Cancel" }).click();
      await expect(appPage.getByText(/Remove "removeme"\?/)).not.toBeVisible();
      await expect(appPage.getByText("removeme")).toBeVisible();
    } finally {
      await rmRemoteDir(remoteUrl);
    }
  });

  test("removing a folder via confirmation removes it from the daemon", async ({ appPage, daemon }) => {
    const remoteUrl = await mkRemoteDir();
    try {
      await addFolderViaProxy(daemon, "bye-folder", daemon.localDir, remoteUrl, appPage);
      await login(appPage);
      await goToFolders(appPage);
      await appPage.getByText("bye-folder").waitFor({ timeout: 8_000 });

      await appPage.locator(".btn-icon").first().click();
      await appPage.getByRole("button", { name: "Remove Folder" }).click();
      await appPage.getByRole("button", { name: "Remove" }).click();

      // Reload to get a fresh snapshot — push events are not available in tests.
      await appPage.reload();
      await appPage.waitForSelector("#root > *", { timeout: 10_000 });
      await login(appPage);
      await goToFolders(appPage);
      await expect(appPage.getByText("No sync folders yet")).toBeVisible({ timeout: 8_000 });
    } finally {
      await rmRemoteDir(remoteUrl);
    }
  });

  test("View Details button opens FolderDetail", async ({ appPage, daemon }) => {
    const remoteUrl = await mkRemoteDir();
    try {
      await addFolderViaProxy(daemon, "detail-test", daemon.localDir, remoteUrl, appPage);
      await login(appPage);
      await goToFolders(appPage);
      await appPage.getByText("detail-test").waitFor({ timeout: 8_000 });

      await appPage.getByRole("button", { name: "View Details" }).first().click();

      await expect(appPage.getByText("Last Synced")).toBeVisible({ timeout: 5_000 });
    } finally {
      await rmRemoteDir(remoteUrl);
    }
  });
});
