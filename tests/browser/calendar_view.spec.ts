import { expect, test } from "@playwright/test";

import { appURL, poolUser, ssoEnabled, uniqueName } from "./support";

// The calendar remembers the Grid/Agenda choice per device in a cookie (#63).
// Opt-in: CALBOOKER_SSO_E2E=1 (make e2e) with goweb running and calbooker
// started with make run.

test.describe("calendar view cookie", () => {
  test.skip(!ssoEnabled, "set CALBOOKER_SSO_E2E=1 (make e2e) to run the calendar view spec");

  test("the Agenda choice survives a reload without the view parameter", async ({ browser }) => {
    const owner = await poolUser(browser, "owner");
    const page = owner.page;
    await page.setViewportSize({ width: 1280, height: 800 });
    await page.goto(`${appURL}/en/groups`);
    await page.locator("#group-new-name").fill(uniqueName("Club"));
    await page.locator("#group-new-name").locator("xpath=ancestor::form").getByRole("button").click();
    await page.waitForURL(/\/en\/groups\/(?!join$)[a-z0-9-]+$/);
    const calendarURL = `${page.url()}/calendar`;

    await page.goto(calendarURL);
    await expect(page.getByTestId("calendar-grid-view")).toBeVisible();
    await page.getByTestId("calendar-view-agenda").click();
    await page.waitForURL(/view=agenda/);
    await expect(page.getByTestId("calendar-day-list")).toBeVisible();

    await page.goto(calendarURL);
    await expect(page.getByTestId("calendar-day-list")).toBeVisible();
    await expect(page.getByTestId("calendar-grid-view")).toBeHidden();
    await expect(page.getByTestId("calendar-view-agenda")).toHaveAttribute("aria-current", "true");

    await page.getByTestId("calendar-view-grid").click();
    await page.waitForURL(/view=grid/);
    await page.goto(calendarURL);
    await expect(page.getByTestId("calendar-grid-view")).toBeVisible();
    await expect(page.getByTestId("calendar-day-list")).toBeHidden();
  });
});
