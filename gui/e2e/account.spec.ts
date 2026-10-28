/**
 * AccountSetup page tests: connecting the account through the browser login
 * flow (Nextcloud Login Flow V2) against the dev environment.
 *
 * Uses appPageNoAccount — a daemon with no credentials stored — so the app
 * always opens the setup wizard, whose account step shows AccountSetup. The dev
 * environment has no web UI, so the tests grant / deny access through the OCS
 * endpoints, as the web UI would.
 */

import { test, expect } from "./fixture";
import { loginFlowAction } from "./loginflow";
import type { Page } from "@playwright/test";

const LOGIN_URL_RE = /\/index\.php\/login\/v2\/flow\/[\w-]+$/;

/** Clicks "Sign in with browser" and returns the login URL shown by the app. */
async function startSignIn(page: Page): Promise<string> {
  await page.getByRole("button", { name: "Sign in with browser" }).click();
  const link = page.getByText(LOGIN_URL_RE);
  await expect(link).toBeVisible({ timeout: 10_000 });
  return (await link.textContent())!.trim();
}

test.describe("AccountSetup", () => {
  test.beforeEach(async ({ appPageNoAccount }) => {
    await appPageNoAccount.getByRole("button", { name: "Get started" }).click();
  });

  test("shows the browser sign-in when no account is configured", async ({ appPageNoAccount }) => {
    await expect(appPageNoAccount.getByText("Connect your CERN account")).toBeVisible();
    await expect(appPageNoAccount.getByRole("button", { name: "Sign in with browser" })).toBeVisible();
    // The password form is gone: credentials are obtained through the browser.
    await expect(appPageNoAccount.locator("input[type=password]")).toHaveCount(0);
  });

  test("opens the login URL in the browser and waits for access", async ({ appPageNoAccount, daemonNoAccount }) => {
    const loginUrl = await startSignIn(appPageNoAccount);

    expect(daemonNoAccount.proxy.openedUrls).toEqual([loginUrl]);
    await expect(appPageNoAccount.getByText("Waiting for you to grant access in the browser…")).toBeVisible();
  });

  test("moves past setup once access is granted", async ({ appPageNoAccount }) => {
    const loginUrl = await startSignIn(appPageNoAccount);

    await loginFlowAction(loginUrl, "grant", "e2e-account-test");

    // The wizard moves past AccountSetup.
    await expect(appPageNoAccount.getByText("Connect your CERN account")).not.toBeVisible({ timeout: 15_000 });
    await expect(appPageNoAccount.getByRole("heading", { name: "Choose the interface" })).toBeVisible();
  });

  test("keeps waiting when access is denied, until cancelled", async ({ appPageNoAccount }) => {
    const loginUrl = await startSignIn(appPageNoAccount);

    await loginFlowAction(loginUrl, "deny");
    // A denied flow looks like a pending one to the client.
    await appPageNoAccount.waitForTimeout(4_000);
    await expect(appPageNoAccount.getByText("Waiting for you to grant access in the browser…")).toBeVisible();

    await appPageNoAccount.getByRole("button", { name: "Cancel" }).click();
    await expect(appPageNoAccount.getByRole("button", { name: "Sign in with browser" })).toBeVisible();
    await expect(appPageNoAccount.getByText("Connect your CERN account")).toBeVisible();
  });
});
