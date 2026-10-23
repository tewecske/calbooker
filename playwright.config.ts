import { defineConfig, devices } from "@playwright/test";

import { appURL } from "./tests/browser/support/env";

// Specs run against an already running stack (see tests/README.md); nothing
// is started from here. Browsers come from PLAYWRIGHT_BROWSERS_PATH (Nix);
// @playwright/test is pinned to the version whose browser revisions match it.
export default defineConfig({
  testDir: "./tests/browser",
  timeout: 30_000,
  fullyParallel: false,
  forbidOnly: Boolean(process.env.CI),
  retries: process.env.CI ? 2 : 0,
  reporter: "list",
  use: {
    ...devices["Desktop Chrome"],
    baseURL: appURL,
    trace: "retain-on-failure",
  },
});
