import { defineConfig } from "@playwright/test";

const headed = process.env.PWHEADED === "1";

export default defineConfig({
  testDir: "./e2e",
  timeout: headed ? 120_000 : 30_000,
  retries: 0,
  reporter: "line",
  use: {
    baseURL: "http://localhost:1420",
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
