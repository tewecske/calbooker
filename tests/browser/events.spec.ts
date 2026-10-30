import { expect, test } from "@playwright/test";

import { appURL, becomeGuest, createInvite, joinWithCode, poolUser, ssoEnabled, uniqueName } from "./support";

// Events (#8): admins and managers create events for any team, coaches for
// the teams they coach in the current season; players see their teams'
// events only. Opt-in like the other single sign-on specs:
// CALBOOKER_SSO_E2E=1 (make e2e) with goweb's Hydra and provider running and
// calbooker started with make run. The owner and coach come from the account
// pool; the player is a guest.

/** The calendar date `days` from now as YYYY-MM-DD (UTC; the margins are wide). */
function dateFromToday(days: number): string {
  const date = new Date(Date.now() + days * 24 * 60 * 60 * 1000);
  return date.toISOString().slice(0, 10);
}

test.describe("events", () => {
  test.skip(!ssoEnabled, "set CALBOOKER_SSO_E2E=1 (make e2e) to run the event specs");
  test.describe.configure({ mode: "serial" });

  test("a coach creates a championship for their team with a free-text location; a player of another team cannot see it", async ({ browser }) => {
    test.setTimeout(120_000);
    const owner = await poolUser(browser, "owner");
    const page = owner.page;
    const clubName = uniqueName("Club");
    const lions = uniqueName("Lions");
    const tigers = uniqueName("Tigers");
    const title = uniqueName("Cup");
    const location = uniqueName("Stadium");

    await page.goto(`${appURL}/en/groups`);
    await page.locator("#group-new-name").fill(clubName);
    await page.locator("#group-new-name").locator("xpath=ancestor::form").getByRole("button").click();
    await page.waitForURL(/\/en\/groups\/(?!join$)[a-z0-9-]+$/);
    const groupURL = page.url();

    // Two teams.
    const teamURLs: Record<string, string> = {};
    for (const [name, from] of [[lions, "2015"], [tigers, "2017"]]) {
      await page.goto(`${groupURL}/teams`);
      await page.locator("#team-new-name").fill(name);
      await page.locator("#team-new-birth-year-from").fill(from);
      await page.locator("#team-new-birth-year-to").fill(from);
      await page.getByRole("button", { name: "Create a team" }).click();
      await page.waitForURL(/\/en\/groups\/[a-z0-9-]+\/teams\/[a-z0-9-]+$/);
      teamURLs[name] = page.url();
    }

    // A season around today, so coach assignments count now.
    await page.goto(`${groupURL}/seasons`);
    await page.locator("#season-new-name").fill(uniqueName("Season"));
    await page.locator("#season-new-starts-on").fill(dateFromToday(-30));
    await page.locator("#season-new-ends-on").fill(dateFromToday(300));
    await page.locator("#season-new-part1-name").fill("Whole");
    await page.locator("#season-new-part1-starts-on").fill(dateFromToday(-30));
    await page.locator("#season-new-part1-ends-on").fill(dateFromToday(300));
    await page.getByRole("button", { name: "Add a season" }).click();
    await page.waitForURL(/\/en\/groups\/[a-z0-9-]+\/seasons\/\d+$/);

    // The coach joins and is assigned to the Lions.
    const coach = await poolUser(browser, "coach");
    const coachCode = await createInvite(page, groupURL, { role: "coach" });
    await joinWithCode(coach.page, appURL, coachCode);
    await coach.page.waitForURL(groupURL);
    await page.goto(teamURLs[lions]);
    await page.locator("#team-coach-user").selectOption({ index: 0 });
    await page.getByRole("button", { name: "Assign coach" }).click();
    await expect(page.locator("#alerts")).toContainText("Coach assigned.");

    // A player of the Tigers joins as a guest.
    const playerCode = await createInvite(page, groupURL, { role: "player", team: tigers });
    const player = await becomeGuest(browser);
    await joinWithCode(player.page, appURL, playerCode);
    await player.page.waitForURL(/\/en\/groups\/[a-z0-9-]+\/teams\/[a-z0-9-]+$/);

    // The coach creates the championship; only the Lions are on offer.
    await coach.page.goto(groupURL);
    await coach.page.locator("nav a[data-section=events]").click();
    const teams = coach.page.getByTestId("event-new-teams");
    await expect(teams.getByLabel(lions)).toBeVisible();
    await expect(teams.getByLabel(tigers)).toHaveCount(0);
    await coach.page.locator("#event-new-kind").selectOption("championship");
    await coach.page.locator("#event-new-title").fill(title);
    await coach.page.locator("#event-new-starts-at").fill(`${dateFromToday(7)}T09:00`);
    await coach.page.locator("#event-new-ends-at").fill(`${dateFromToday(7)}T16:00`);
    await coach.page.locator("#event-new-signup-mode").selectOption("nominated");

    // Without a team or place the form is refused and kept.
    await coach.page.getByRole("button", { name: "Add an event" }).click();
    await expect(coach.page.locator("#event-new-teams-error")).toContainText("Pick at least one team");
    await expect(coach.page.locator("#event-new-title")).toHaveValue(title);
    await coach.page.getByTestId("event-new-teams").getByLabel(lions).check();
    await coach.page.getByRole("button", { name: "Add an event" }).click();
    await expect(coach.page.locator("#event-new-place-error")).toContainText("not both");

    await coach.page.locator("#event-new-location").fill(location);
    await coach.page.getByRole("button", { name: "Add an event" }).click();
    await coach.page.waitForURL(/\/en\/groups\/[a-z0-9-]+\/events\/\d+$/);
    const eventURL = coach.page.url();
    await expect(coach.page.locator("#event-heading")).toContainText(title);
    await expect(coach.page.locator("#event-heading [data-event-kind=championship]")).toHaveText("Championship");
    await expect(coach.page.getByTestId("event-place")).toHaveText(location);
    await expect(coach.page.getByTestId("event-teams")).toHaveText(lions);
    await expect(coach.page.getByTestId("event-when")).toContainText(`${dateFromToday(7)} 09:00 – 16:00`);
    await expect(coach.page.getByTestId("event-signup")).toHaveText("Coaches nominate");
    await expect(coach.page.locator("#event-edit-title")).toHaveValue(title);

    // The owner sees it in the list.
    await page.goto(`${groupURL}/events`);
    await expect(page.getByTestId("events").locator("tr[data-event-id]").filter({ hasText: title })).toContainText(location);

    // The Tigers player sees the events page, but not this event.
    await player.page.goto(groupURL);
    await player.page.locator("nav a[data-section=events]").click();
    await expect(player.page.locator("#group-events-heading")).toBeVisible();
    await expect(player.page.getByText(title)).toHaveCount(0);
    await expect(player.page.locator("#event-new-title")).toHaveCount(0);
    expect((await player.page.goto(eventURL))?.status()).toBe(404);

    // The coach cancels it; it stays listed as cancelled.
    await coach.page.goto(eventURL);
    await coach.page.getByRole("button", { name: "Cancel event" }).click();
    await expect(coach.page.locator("#alerts")).toContainText("Event cancelled.");
    await expect(coach.page.getByTestId("event-cancelled-badge")).toBeVisible();
    await page.goto(`${groupURL}/events`);
    await expect(page.locator("tr[data-event-cancelled=true]").filter({ hasText: title })).toHaveCount(1);

    await player.context.close();
    await coach.context.close();
    await owner.context.close();
  });
});
