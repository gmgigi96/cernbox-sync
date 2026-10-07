import { defineConfig } from "@playwright/test";
import { CLIENT_UA } from "./e2e/loginflow";

const headed = process.env.PWHEADED === "1";

export default defineConfig({
  testDir: "./e2e",
  timeout: headed ? 120_000 : 30_000,
  retries: 0,
  reporter: "line",
  use: {
    baseURL: "http://localhost:1420",
    // Like the Tauri webview: the app's own requests to the server use the
    // account's app password, which is only accepted from sync clients.
    userAgent: CLIENT_UA,
    headless: !headed,
    launchOptions: headed ? { slowMo: 1000 } : {},
    contextOptions: {
      serviceWorkers: "block",
    },
  },
  webServer: {
    command: "npm run dev",
    url: "http://localhost:1420",
    reuseExistingServer: false,
    timeout: 15_000,
  },
});
