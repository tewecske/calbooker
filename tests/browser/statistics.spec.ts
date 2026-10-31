import { expect, test, type Page } from "@playwright/test";

import { appURL, becomeGuest, createInvite, joinWithCode, pickEventTeams, poolUser, ssoEnabled, uniqueName } from "./support";

// Participation statistics (#11): a coach opens the monthly statistics of
// their team; a player and a parent see only their own (or their child's)
// numbers. Opt-in like the other single sign-on specs: CALBOOKER_SSO_E2E=1
// (make e2e) with goweb's Hydra and provider running and calbooker started
// with make run. The owner and coach come from the account pool; the player
// and the parent are guests.

/** The calendar date `days` from now as YYYY-MM-DD (UTC; the margins are wide). */
function dateFromToday(days: number): string {
  const date = new Date(Date.now() + days * 24 * 60 * 60 * 1000);
  return date.toISOString().slice(0, 10);
}

/** Creates an event for one team on the events page and returns its URL. */
async function createEvent(page: Page, groupURL: string, team: string, title: string, day: string): Promise<string> {
  await page.goto(`${groupURL}/events`);
  await page.locator("#event-new-title").fill(title);
  await page.locator("#event-new-starts-at").fill(`${day}T09:00`);
  await page.locator("#event-new-ends-at").fill(`${day}T10:30`);
  await pickEventTeams(page, [team]);
  await page.locator("#event-new-location").fill("Clubhouse");
  await page.getByRole("button", { name: "Add an event" }).click();
  await page.waitForURL(/\/en\/groups\/[a-z0-9-]+\/events\/\d+$/);
  return page.url();
}

test.describe("statistics", () => {
  test.skip(!ssoEnabled, "set CALBOOKER_SSO_E2E=1 (make e2e) to run the statistics specs");
  test.describe.configure({ mode: "serial" });

  test("a coach opens the monthly statistics of their team; players and parents see their own numbers", async ({ browser }) => {
    test.setTimeout(180_000);
    const owner = await poolUser(browser, "owner");
    const page = owner.page;
    const lions = uniqueName("Lions");

    await page.goto(`${appURL}/en/groups`);
    await page.locator("#group-new-name").fill(uniqueName("Club"));
    await page.locator("#group-new-name").locator("xpath=ancestor::form").getByRole("button").click();
    await page.waitForURL(/\/en\/groups\/(?!join$)[a-z0-9-]+$/);
    const groupURL = page.url();

    await page.goto(`${groupURL}/teams`);
    await page.locator("#team-new-name").fill(lions);
    await page.locator("#team-new-birth-year-from").fill("2015");
    await page.locator("#team-new-birth-year-to").fill("2016");
    await page.getByRole("button", { name: "Create a team" }).click();
    await page.waitForURL(/\/en\/groups\/[a-z0-9-]+\/teams\/[a-z0-9-]+$/);
    const teamURL = page.url();

    // A season around today, so the coach assignment counts now.
    await page.goto(`${groupURL}/seasons`);
    await page.locator("#season-new-name").fill(uniqueName("Season"));
    await page.locator("#season-new-starts-on").fill(dateFromToday(-30));
    await page.locator("#season-new-ends-on").fill(dateFromToday(300));
    await page.locator("#season-new-part1-name").fill("Whole");
    await page.locator("#season-new-part1-starts-on").fill(dateFromToday(-30));
    await page.locator("#season-new-part1-ends-on").fill(dateFromToday(300));
    await page.getByRole("button", { name: "Add a season" }).click();
    await page.waitForURL(/\/en\/groups\/[a-z0-9-]+\/seasons\/\d+$/);

    const coach = await poolUser(browser, "coach");
    const coachCode = await createInvite(page, groupURL, { role: "coach" });
    await joinWithCode(coach.page, appURL, coachCode);
    await coach.page.waitForURL(groupURL);
    await page.goto(teamURL);
    await page.locator("#team-coach-user").selectOption({ index: 0 });
    await page.getByRole("button", { name: "Assign coach" }).click();
    await expect(page.locator("#alerts")).toContainText("Coach assigned.");

    // A guest player joins the Lions; a guest parent adds a child profile
    // that the owner puts on the team.
    const playerCode = await createInvite(page, groupURL, { role: "player", team: lions });
    const player = await becomeGuest(browser);
    await joinWithCode(player.page, appURL, playerCode);
    await player.page.waitForURL(/\/en\/groups\/[a-z0-9-]+\/teams\/[a-z0-9-]+$/);
    const parentCode = await createInvite(page, groupURL, { role: "parent" });
    const parent = await becomeGuest(browser);
    await joinWithCode(parent.page, appURL, parentCode);
    await parent.page.waitForURL(groupURL);
    const childName = uniqueName("Anna");
    await parent.page.goto(`${groupURL}/children`);
    await parent.page.locator("#child-new-name").fill(childName);
    await parent.page.locator("#child-new-birth-year").fill("2016");
    await parent.page.getByRole("button", { name: "Add a child" }).click();
    await expect(parent.page.locator("#alerts")).toContainText("Child added.");
    const childID = await parent.page.getByTestId("children").locator("li[data-child-id]").filter({ hasText: childName }).getAttribute("data-child-id");
    await page.goto(teamURL);
    await page.locator("#team-roster-entry").selectOption(`${childID}:player`);
    await page.getByRole("button", { name: "Add to team" }).click();
    await expect(page.locator("#alerts")).toContainText("Added to the team.");

    // A training that already ended; the coach confirms that the guest came
    // and the child did not.
    const day = dateFromToday(-2);
    const pastURL = await createEvent(page, groupURL, lions, uniqueName("Past training"), day);
    await coach.page.goto(pastURL);
    const form = coach.page.getByTestId("attendance-form");
    await form.getByLabel(childName).uncheck();
    const guestRow = coach.page.getByTestId("participation").locator("tr[data-participant-id]").filter({ hasNot: coach.page.getByText(childName) }).first();
    const guestRowID = await guestRow.getAttribute("data-participant-id");
    await form.locator(`#attended-${guestRowID}`).check();
    await form.getByRole("button", { name: "Save attendance" }).click();
    await expect(coach.page.locator("#alerts")).toContainText("Attendance saved.");

    // The coach opens the monthly statistics of the month of the training.
    await coach.page.goto(`${groupURL}/statistics?period=month&date=${day}`);
    await expect(coach.page.getByTestId("statistics-period")).toHaveText(day.slice(0, 7));
    const teamRow = coach.page.getByTestId("statistics-teams").locator("tr[data-team-id]").filter({ hasText: lions });
    await expect(teamRow.getByTestId("stat-players")).toHaveText("2");
    await expect(teamRow.getByTestId("stat-events")).toHaveText("1");
    await expect(teamRow.getByTestId("stat-attended")).toHaveText("1");
    await expect(teamRow.getByTestId("stat-rate")).toHaveText("50%");
    const childRow = coach.page.getByTestId("statistics-players").locator("tr[data-user-id]").filter({ hasText: childName });
    await expect(childRow.getByTestId("stat-rate")).toHaveText("0%");
    // The coach steps to the next month, where nothing was held.
    await coach.page.getByTestId("statistics-next").click();
    await expect(coach.page.getByTestId("statistics-teams").locator("tr[data-team-id]").filter({ hasText: lions }).getByTestId("stat-rate")).toHaveText("–");

    // The player sees only their own row and no team table.
    await player.page.goto(`${groupURL}/statistics?period=month&date=${day}`);
    await expect(player.page.getByTestId("statistics-teams")).toHaveCount(0);
    const ownRows = player.page.getByTestId("statistics-players").locator("tr[data-user-id]");
    await expect(ownRows).toHaveCount(1);
    await expect(ownRows.first().getByTestId("stat-rate")).toHaveText("100%");

    // The parent sees only the child.
    await parent.page.goto(`${groupURL}/statistics?period=month&date=${day}`);
    await expect(parent.page.getByTestId("statistics-teams")).toHaveCount(0);
    const parentRows = parent.page.getByTestId("statistics-players").locator("tr[data-user-id]");
    await expect(parentRows).toHaveCount(1);
    await expect(parentRows.first()).toContainText(childName);

    await parent.context.close();
    await player.context.close();
    await coach.context.close();
    await owner.context.close();
  });
});
