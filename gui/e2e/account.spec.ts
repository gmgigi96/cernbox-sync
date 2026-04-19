/**
 * AccountSetup page tests.
 *
 * Uses appPageNoAccount — a daemon with no credentials stored — so the app
 * always lands on the AccountSetup form first.
 */

import { test, expect } from "./fixture";

test.describe("AccountSetup", () => {
  test("shows the account setup form when no account is configured", async ({ appPageNoAccount }) => {
    await expect(appPageNoAccount.getByText("Connect your CERN account")).toBeVisible();
    await expect(appPageNoAccount.getByPlaceholder("your-cern-username")).toBeVisible();
    await expect(appPageNoAccount.getByPlaceholder("••••••••")).toBeVisible();
  });

  test("shows validation error when username is empty", async ({ appPageNoAccount }) => {
    await appPageNoAccount.getByRole("button", { name: "Get Started" }).click();
    await expect(appPageNoAccount.getByText("Username is required.")).toBeVisible();
  });

  test("shows validation error when password is empty", async ({ appPageNoAccount }) => {
    await appPageNoAccount.getByPlaceholder("your-cern-username").fill("einstein");
    await appPageNoAccount.getByRole("button", { name: "Get Started" }).click();
    await expect(appPageNoAccount.getByText("Password is required.")).toBeVisible();
  });

  test("navigates away from setup after valid credentials are submitted", async ({ appPageNoAccount }) => {
    await appPageNoAccount.getByPlaceholder("your-cern-username").fill("einstein");
    await appPageNoAccount.getByPlaceholder("••••••••").fill("relativity");
    await appPageNoAccount.getByRole("button", { name: "Get Started" }).click();

    // After a successful set-account the app moves past AccountSetup.
    await expect(appPageNoAccount.getByText("Connect your CERN account")).not.toBeVisible({ timeout: 5_000 });
  });
});
