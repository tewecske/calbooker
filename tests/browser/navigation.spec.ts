import { expect, test } from "@playwright/test";

import { appURL, poolUser, ssoEnabled } from "./support";

// Navigation on small screens and the signed-in home page (#35).
test.describe("navigation", () => {
  test.skip(!ssoEnabled, "set CALBOOKER_SSO_E2E=1 (make e2e) to run the navigation specs");

  test("at phone width the menu reaches Groups, and home links to create and join", async ({ browser }) => {
    const user = await poolUser(browser, "owner");
    const page = user.page;
    await page.setViewportSize({ width: 375, height: 700 });

    await page.goto(`${appURL}/en/`);
    await expect(page.getByRole("navigation", { name: "Primary navigation" })).toBeHidden();

    // The signed-in home offers the group shortcuts.
    await expect(page.getByRole("link", { name: "Create a group" })).toBeVisible();
    await expect(page.getByRole("link", { name: "Join a group" })).toBeVisible();

    // Open the hamburger menu and go to Groups.
    await page.locator("#mobile-menu summary").click();
    await page.locator("#mobile-menu").getByRole("link", { name: "Groups" }).click();
    await page.waitForURL(/\/en\/groups$/);
    await expect(page.locator("#groups-mine-heading")).toBeVisible();
  });
});
