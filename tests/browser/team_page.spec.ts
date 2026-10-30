import { expect, test } from "@playwright/test";

import { appURL, poolUser, ssoEnabled, uniqueName } from "./support";

// Team page with a submenu (#44): Roster, Events and Calendar. Opt-in like
// the other single sign-on specs: CALBOOKER_SSO_E2E=1 (make e2e) with
// goweb's Hydra and provider running and calbooker started with make run.
// The owner comes from the account pool.

/** The calendar date `days` from now as YYYY-MM-DD (UTC; events sit at midday). */
function dateFromToday(days: number): string {
  const date = new Date(Date.now() + days * 24 * 60 * 60 * 1000);
  return date.toISOString().slice(0, 10);
}

test.describe("team page", () => {
  test.skip(!ssoEnabled, "set CALBOOKER_SSO_E2E=1 (make e2e) to run the team page specs");
  test.describe.configure({ mode: "serial" });

  test("an admin switches the team's tabs and creates an event from Events", async ({ browser }) => {
    test.setTimeout(120_000);
    const owner = await poolUser(browser, "owner");
    const page = owner.page;
    const clubName = uniqueName("Club");
    const lions = uniqueName("Lions");
    const tigers = uniqueName("Tigers");
    const title = uniqueName("Training");
    const day = dateFromToday(7);

    await page.goto(`${appURL}/en/groups`);
    await page.locator("#group-new-name").fill(clubName);
    await page.locator("#group-new-name").locator("xpath=ancestor::form").getByRole("button").click();
    await page.waitForURL(/\/en\/groups\/(?!join$)[a-z0-9-]+$/);
    const groupURL = page.url();

    const teamURLs: Record<string, string> = {};
    for (const [name, year] of [[lions, "2015"], [tigers, "2017"]]) {
      await page.goto(`${groupURL}/teams`);
      await page.locator("#team-new-name").fill(name);
      await page.locator("#team-new-birth-year-from").fill(year);
      await page.locator("#team-new-birth-year-to").fill(year);
      await page.getByRole("button", { name: "Create a team" }).click();
      await page.waitForURL(/\/en\/groups\/[a-z0-9-]+\/teams\/[a-z0-9-]+$/);
      teamURLs[name] = page.url();
    }

    // The team page opens on the roster; the tabs switch.
    await page.goto(teamURLs[lions]);
    const tabs = page.getByTestId("team-tabs");
    await expect(tabs.locator("[data-team-tab=roster]")).toHaveAttribute("aria-current", "page");
    await tabs.locator("[data-team-tab=events]").click();
    await page.waitForURL(`${teamURLs[lions]}/events`);
    await expect(page.locator("#group-events-heading")).toBeVisible();

    // The create form has the team preselected.
    const teams = page.getByTestId("event-new-teams");
    await expect(teams.locator("option", { hasText: lions })).toHaveJSProperty("selected", true);
    await expect(teams.locator("option", { hasText: tigers })).toHaveJSProperty("selected", false);
    await page.locator("#event-new-title").fill(title);
    await page.locator("#event-new-starts-at").fill(`${day}T12:00`);
    await page.locator("#event-new-ends-at").fill(`${day}T13:00`);
    await page.locator("#event-new-location").fill("Club ground");
    await page.getByRole("button", { name: "Add an event" }).click();
    await page.waitForURL(/\/en\/groups\/[a-z0-9-]+\/events\/\d+$/);
    await expect(page.locator("#event-heading")).toContainText(title);
    await expect(page.getByTestId("event-teams")).toHaveText(lions);

    // The team's Events and Calendar show it; the other team's do not.
    await page.goto(`${teamURLs[lions]}/events`);
    await expect(page.getByTestId("events").locator("tr[data-event-id]").filter({ hasText: title })).toHaveCount(1);
    await page.getByTestId("team-tabs").locator("[data-team-tab=calendar]").click();
    await page.waitForURL(`${teamURLs[lions]}/calendar`);
    await page.goto(`${teamURLs[lions]}/calendar?week=${day}`);
    await expect(page.locator("#calendar-filter-team")).toHaveCount(0);
    await expect(page.getByTestId("calendar-day-list").locator("li[data-event-id]").filter({ hasText: title })).toHaveCount(1);
    await page.goto(`${teamURLs[tigers]}/calendar?week=${day}`);
    await expect(page.getByTestId("calendar-day-list").locator("li[data-event-id]").filter({ hasText: title })).toHaveCount(0);
    await page.goto(`${teamURLs[tigers]}/events`);
    await expect(page.getByTestId("events-empty")).toBeVisible();

    // Back on the roster tab.
    await page.getByTestId("team-tabs").locator("[data-team-tab=roster]").click();
    await page.waitForURL(teamURLs[tigers]);
    await expect(page.locator("#team-heading")).toContainText(tigers);

    await owner.context.close();
  });
});
