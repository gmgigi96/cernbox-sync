/**
 * Custom Playwright fixture that:
 *  - Starts an isolated daemon + HTTP proxy once per test.
 *  - Injects the Tauri mock into every page before scripts load.
 *  - Marks the first-start setup wizard as done, unless the test is about it.
 *  - Navigates to the app root and waits for React to mount.
 */

import { test as base, type BrowserContext, type Page } from "@playwright/test";
import { startDaemon, tauriMockScript, type DaemonHandle } from "./daemon";

export interface Fixtures {
  daemon: DaemonHandle;
  /** Daemon with NO account pre-configured (for AccountSetup tests). */
  daemonNoAccount: DaemonHandle;
  /** Page already at "/" with the Tauri shim injected. */
  appPage: Page;
  /** Page backed by a daemon with no account (for AccountSetup tests). */
  appPageNoAccount: Page;
  /** Page at "/" on first start: account configured, setup wizard not done yet. */
  appPageFirstStart: Page;
}

/** Install the Tauri shim and route interceptors on a browser context. */
async function setupContext(ctx: BrowserContext, proxyUrl: string, setupDone = true): Promise<void> {
  // Inject window.__TAURI_INTERNALS__ before any page script runs.
  await ctx.addInitScript(tauriMockScript(proxyUrl));
  // Skip the setup wizard shown on first start (a missing account shows it anyway).
  if (setupDone) {
    await ctx.addInitScript(() => localStorage.setItem("cernbox-sync-setup-done", "1"));
  }
}

/** Navigate to the app root and wait for React to mount. */
async function openApp(page: Page): Promise<void> {
  await page.goto("/");
  await page.waitForSelector("#root > *", { timeout: 10_000 });
}

export const test = base.extend<Fixtures>({
  daemon: async ({}, use) => {
    const handle = await startDaemon();
    await use(handle);
    handle.stop();
  },

  daemonNoAccount: async ({}, use) => {
    const handle = await startDaemon({ noAccount: true });
    await use(handle);
    handle.stop();
  },

  appPage: async ({ context, page, daemon }, use) => {
    await setupContext(context, daemon.proxy.url);
    await openApp(page);
    await use(page);
  },

  appPageFirstStart: async ({ context, page, daemon }, use) => {
    await setupContext(context, daemon.proxy.url, false);
    await openApp(page);
    await use(page);
  },

  appPageNoAccount: async ({ browser, daemonNoAccount }, use) => {
    const ctx = await browser.newContext();
    await setupContext(ctx, daemonNoAccount.proxy.url);
    const page = await ctx.newPage();
    await openApp(page);
    await use(page);
    await ctx.close();
  },
});

export { expect } from "@playwright/test";
