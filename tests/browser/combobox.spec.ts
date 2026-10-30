import { expect, test } from "@playwright/test";

import { appURL, createInvite, joinWithCode, poolUser, ssoEnabled, uniqueName } from "./support";

// Event form pickers (#45): the team and extra-coach selects are enhanced into
// comboboxes (static/app.js). Opt-in like the other single sign-on specs:
// CALBOOKER_SSO_E2E=1 (make e2e) with goweb's Hydra and provider running and
// calbooker started with make run.

test.describe("combobox pickers", () => {
  test.skip(!ssoEnabled, "set CALBOOKER_SSO_E2E=1 (make e2e) to run the combobox specs");
  test.describe.configure({ mode: "serial" });

  test("the owner filters teams, adds two coaches with the keyboard, removes one and saves", async ({ browser }) => {
    test.setTimeout(150_000);
    const owner = await poolUser(browser, "owner");
    const page = owner.page;
    const suffix = uniqueName("X");
    const unitedName = `Ünited ${suffix}`;
    const tigers = `Tigers ${suffix}`;
    const title = uniqueName("Cup");

    await page.goto(`${appURL}/en/groups`);
    await page.locator("#group-new-name").fill(uniqueName("Club"));
    await page.locator("#group-new-name").locator("xpath=ancestor::form").getByRole("button").click();
    await page.waitForURL(/\/en\/groups\/(?!join$)[a-z0-9-]+$/);
    const groupURL = page.url();

    for (const [name, from] of [[unitedName, "2015"], [tigers, "2017"]]) {
      await page.goto(`${groupURL}/teams`);
      await page.locator("#team-new-name").fill(name);
      await page.locator("#team-new-birth-year-from").fill(from);
      await page.locator("#team-new-birth-year-to").fill(from);
      await page.getByRole("button", { name: "Create a team" }).click();
      await page.waitForURL(/\/en\/groups\/[a-z0-9-]+\/teams\/[a-z0-9-]+$/);
    }

    const coachA = await poolUser(browser, "coach");
    const coachB = await poolUser(browser, "member");
    for (const coach of [coachA, coachB]) {
      await joinWithCode(coach.page, appURL, await createInvite(page, groupURL, { role: "coach" }));
      await coach.page.waitForURL(groupURL);
    }

    await page.goto(`${groupURL}/events`);
    await page.locator("#event-new-title").fill(title);
    await page.locator("#event-new-starts-at").fill("2027-05-06T09:00");
    await page.locator("#event-new-ends-at").fill("2027-05-06T10:00");
    await page.locator("#event-new-location").fill("Pitch");

    // Teams: type without the accent, the list narrows, Enter picks.
    const teams = page.getByRole("combobox", { name: "Teams" });
    await teams.fill("unit");
    const options = page.getByTestId("event-new-teams").getByRole("option");
    await expect(options).toHaveCount(1);
    await expect(options.first()).toHaveText(unitedName);
    await teams.press("Enter");
    await expect(page.getByTestId("event-new-teams").locator(".combobox-chip")).toContainText(unitedName);

    // Coaches: arrows and Enter add two, Backspace removes the last.
    const coaches = page.getByRole("combobox", { name: "Extra coaches" });
    await coaches.focus();
    await coaches.press("ArrowDown");
    await expect(coaches).toHaveAttribute("aria-expanded", "true");
    await coaches.press("Enter");
    await coaches.press("ArrowDown");
    await coaches.press("Enter");
    const chips = page.getByTestId("event-new-coaches").locator(".combobox-chip");
    await expect(chips).toHaveCount(2);
    await coaches.press("Escape");
    await expect(coaches).toHaveAttribute("aria-expanded", "false");
    await coaches.press("Backspace");
    await expect(chips).toHaveCount(1);
    await expect(page.locator("#event-new-coaches-select-input")).toBeVisible();
    await expect(page.locator("select[name=coach_user_id] option:checked")).toHaveCount(1);

    await page.getByRole("button", { name: "Add an event" }).click();
    await page.waitForURL(/\/en\/groups\/[a-z0-9-]+\/events\/\d+$/);
    await expect(page.getByTestId("event-teams")).toHaveText(unitedName);
    await expect(page.locator("select[name=coach_user_id] option:checked")).toHaveCount(1);

    for (const user of [coachB, coachA, owner]) await user.context.close();
  });
});
