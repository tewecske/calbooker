import { defineConfig, devices } from "@playwright/test";

// Specs run against an already running stack (see tests/README.md); nothing
// is started from here.
const baseURL = process.env.CALBOOKER_BROWSER_BASE_URL ?? process.env.CALBOOKER_PUBLIC_URL ?? "http://127.0.0.1:8082";

export default defineConfig({
  testDir: "./tests/browser",
  timeout: 30_000,
  fullyParallel: false,
  forbidOnly: Boolean(process.env.CI),
  retries: process.env.CI ? 2 : 0,
  reporter: "list",
  use: {
    ...devices["Desktop Chrome"],
    baseURL,
    trace: "retain-on-failure",
  },
});
