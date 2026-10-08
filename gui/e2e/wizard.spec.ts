/**
 * First-start setup wizard tests, against a real daemon (make dev-up).
 *
 * The daemon runs with isolated config dirs, so no desktop client is found and
 * the import step is not offered.
 */

import { test, expect } from "./fixture";
import { login } from "./helpers";

const step = (page: import("@playwright/test").Page) =>
  page.getByRole("list", { name: "Setup progress" }).locator("[aria-current=step]");

test.describe("Setup wizard", () => {
  test("signs in, saves the interface choice and is not shown again", async ({ appPageNoAccount: page }) => {
    await expect(page.getByRole("heading", { name: "Welcome to CERNBox Sync" })).toBeVisible();
    await expect(page.getByRole("list", { name: "Setup progress" })).not.toContainText("Import");
    await page.getByRole("button", { name: "Get started" }).click();

    await expect(page.getByText("Connect your CERN account")).toBeVisible();
    await login(page); // browser login flow, granted as einstein

    await expect(step(page)).toContainText("Interface");
    await page.getByRole("radio", { name: /Advanced/ }).click();
    await expect(page.getByRole("radio", { name: /Advanced/ })).toHaveAttribute("aria-checked", "true");
    await page.getByRole("button", { name: "Continue" }).click();

    await expect(page.getByRole("heading", { name: "You're all set" })).toBeVisible();
    await expect(page.getByText("Signed in as einstein")).toBeVisible();
    await page.getByRole("button", { name: "Later" }).click();

    // The main app.
    await expect(page.getByRole("button", { name: /^Settings$/i })).toBeVisible();

    await page.reload();
    await page.waitForSelector("#root > *");
    await expect(page.getByRole("heading", { name: "Welcome to CERNBox Sync" })).not.toBeVisible();
  });

  test("keeps the configured account on first start and opens the add-folder flow", async ({ appPageFirstStart: page }) => {
    await page.getByRole("button", { name: "Get started" }).click();
    await expect(page.getByRole("heading", { name: "Signed in" })).toBeVisible();
    await expect(page.getByText("einstein")).toBeVisible();
    await page.getByRole("button", { name: "Continue" }).click();

    await page.getByRole("button", { name: "Continue" }).click(); // interface
    await expect(step(page)).toContainText("Finish");

    await page.getByRole("button", { name: "Add a folder" }).click();
    await expect(page.getByRole("heading", { name: "Welcome to CERNBox Sync" })).not.toBeVisible();
    await expect(page.getByRole("heading", { name: "Spaces" })).toBeVisible();
  });
});
