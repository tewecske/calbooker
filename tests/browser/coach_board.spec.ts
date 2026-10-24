import { expect, test, type Locator, type Page } from "@playwright/test";

import { appURL, createInvite, joinWithCode, poolUser, ssoEnabled, uniqueName } from "./support";

// Coach assignment board (#12): staff fill the board with a suggestion, drag
// coaches between teams and from the pool, and save; coaches see the saved
// board read only. Opt-in like the other single sign-on specs:
// CALBOOKER_SSO_E2E=1 (make e2e) with goweb's Hydra and provider running and
// calbooker started with make run. Uses the pooled "owner" and "coach"
// accounts.

/** The calendar date `days` from now as YYYY-MM-DD (UTC; the margins are wide). */
function dateFromToday(days: number): string {
  const date = new Date(Date.now() + days * 24 * 60 * 60 * 1000);
  return date.toISOString().slice(0, 10);
}

async function box(locator: Locator): Promise<{ x: number; y: number; width: number; height: number }> {
  const result = await locator.boundingBox();
  if (!result) {
    throw new Error("element has no bounding box");
  }
  return result;
}

/** Drags with the mouse the way SortableJS's fallback mode expects. */
async function drag(page: Page, from: Locator, to: Locator): Promise<void> {
  await from.scrollIntoViewIfNeeded();
  const start = await box(from);
  const end = await box(to);
  await page.mouse.move(start.x + 12, start.y + 10);
  await page.mouse.down();
  await page.mouse.move(start.x + 30, start.y + 25, { steps: 5 });
  await page.mouse.move(end.x + end.width / 2, end.y + end.height / 2, { steps: 20 });
  await page.mouse.up();
}

function team(page: Page, name: string): Locator {
  return page.getByTestId("coach-board-team").filter({ has: page.getByRole("heading", { name }) });
}

async function createTeam(page: Page, groupURL: string, name: string, from: number, to: number): Promise<void> {
  await page.goto(`${groupURL}/teams`);
  await page.locator("#team-new-name").fill(name);
  await page.locator("#team-new-birth-year-from").fill(String(from));
  await page.locator("#team-new-birth-year-to").fill(String(to));
  await page.getByRole("button", { name: "Create a team" }).click();
  await page.waitForURL(/\/en\/groups\/\d+\/teams\/\d+$/);
}

test.describe("coach board", () => {
  test.skip(!ssoEnabled, "set CALBOOKER_SSO_E2E=1 (make e2e) to run the coach board specs");
  test.describe.configure({ mode: "serial" });

  test("staff suggest, drag, and save coach assignments; a coach sees them read only", async ({ browser }) => {
    test.setTimeout(150_000);
    const owner = await poolUser(browser, "owner");
    const page = owner.page;
    const lions = uniqueName("Lions");
    const tigers = uniqueName("Tigers");
    const seasonName = uniqueName("Season");
    const startYear = Number(dateFromToday(-30).slice(0, 4));

    await page.goto(`${appURL}/en/groups`);
    await page.locator("#group-new-name").fill(uniqueName("Club"));
    await page.locator("#group-new-name").locator("xpath=ancestor::form").getByRole("button").click();
    await page.waitForURL(/\/en\/groups\/\d+$/);
    const groupURL = page.url();

    // Lions are 9-10 and Tigers 11-12 in the season.
    await createTeam(page, groupURL, lions, startYear - 10, startYear - 9);
    await createTeam(page, groupURL, tigers, startYear - 12, startYear - 11);

    await page.goto(`${groupURL}/seasons`);
    await page.locator("#season-new-name").fill(seasonName);
    await page.locator("#season-new-starts-on").fill(dateFromToday(-30));
    await page.locator("#season-new-ends-on").fill(dateFromToday(300));
    await page.locator("#season-new-part1-name").fill("Whole");
    await page.locator("#season-new-part1-starts-on").fill(dateFromToday(-30));
    await page.locator("#season-new-part1-ends-on").fill(dateFromToday(300));
    await page.getByRole("button", { name: "Add a season" }).click();
    await page.waitForURL(/\/en\/groups\/\d+\/seasons\/\d+$/);
    const seasonURL = page.url();

    // The coach joins and is qualified for ages 9-10.
    const coach = await poolUser(browser, "coach");
    const code = await createInvite(page, groupURL, { role: "coach" });
    await joinWithCode(coach.page, appURL, code);
    await coach.page.waitForURL(groupURL);
    await page.goto(`${groupURL}/coaches`);
    const row = page.getByTestId("coaches").locator("tr[data-coach-id]");
    await row.getByLabel("From age").fill("9");
    await row.getByLabel("To age").fill("10");
    await row.getByRole("button", { name: /Save qualification/ }).click();
    await expect(page.locator("#alerts")).toContainText("Qualification saved.");

    // The season page links the board; it starts empty.
    await page.goto(seasonURL);
    await page.getByTestId("season-coach-board-link").click();
    await page.waitForURL(/\/coach-board$/);
    const boardURL = page.url();
    await expect(page.getByTestId("coach-board-chip")).toHaveCount(0);
    await expect(page.getByTestId("coach-board-pool-coach")).toHaveCount(1);

    // Suggest puts the qualified coach on Lions and leaves Tigers empty.
    await page.getByTestId("coach-board-suggest").click();
    await expect(page.locator("#alerts")).toContainText("1 suggestion added");
    const lionsChip = team(page, lions).getByTestId("coach-board-chip");
    await expect(lionsChip).toHaveCount(1);
    await expect(lionsChip.getByTestId("coach-board-explanation")).toHaveText("Qualified for ages 9–10, 0 teams so far");
    await expect(lionsChip.getByTestId("coach-board-chip-warning")).toHaveText("No availability in this season");
    await expect(team(page, tigers).getByTestId("coach-board-chip")).toHaveCount(0);
    await expect(page.getByTestId("coach-board-unsaved")).toBeVisible();

    // Drag the coach from Lions to Tigers: the qualification warning shows.
    await drag(page, lionsChip, team(page, tigers).getByTestId("coach-board-team-coaches"));
    const tigersChip = team(page, tigers).getByTestId("coach-board-chip");
    await expect(tigersChip).toHaveCount(1);
    await expect(team(page, lions).getByTestId("coach-board-chip")).toHaveCount(0);
    await expect(tigersChip.getByTestId("coach-board-chip-warning").first()).toHaveText("Qualified for ages 9–10, the team is 11–12");
    await expect(tigersChip.getByTestId("coach-board-explanation")).toHaveCount(0);

    // Drag a copy from the pool onto Lions: the coach is on both teams.
    await drag(page, page.getByTestId("coach-board-pool-coach").first(), team(page, lions).getByTestId("coach-board-team-coaches"));
    await expect(team(page, lions).getByTestId("coach-board-chip")).toHaveCount(1);
    await expect(page.getByTestId("coach-board-pool-teams")).toHaveText("2 teams");

    // Mark the head coach of Tigers and save.
    await tigersChip.getByTestId("coach-board-head").check();
    await page.getByTestId("coach-board-save").click();
    await expect(page.locator("#alerts")).toContainText("Coach assignments saved: 2 added, 0 removed, 0 head coach changes.");
    await expect(page.getByTestId("coach-board-unsaved")).toHaveCount(0);

    // Reload: the assignments persisted.
    await page.goto(boardURL);
    await expect(team(page, lions).getByTestId("coach-board-chip")).toHaveCount(1);
    await expect(team(page, tigers).getByTestId("coach-board-chip")).toHaveCount(1);
    await expect(team(page, tigers).getByTestId("coach-board-head")).toBeChecked();
    await expect(page.getByTestId("coach-board-warnings")).toContainText(tigers);

    // The coach sees the saved board read only.
    await coach.page.goto(boardURL);
    await expect(coach.page.getByTestId("coach-board-read-only")).toBeVisible();
    await expect(coach.page.getByTestId("coach-board-save")).toHaveCount(0);
    await expect(coach.page.getByTestId("coach-board-pool")).toHaveCount(0);
    await expect(team(coach.page, tigers).getByTestId("coach-board-chip")).toContainText("Head coach");
    await expect(team(coach.page, lions).getByTestId("coach-board-chip")).toHaveCount(1);
  });
});
