import { expect, test, type Page } from "@playwright/test";

import { appURL, becomeGuest, createInvite, joinWithCode, poolUser, signUpUser, ssoEnabled, startSignIn, uniqueName } from "./support";

// Role-based start page and navigation (#46): a parent's calendar covers all
// their children's teams with a colour per child and a filter by child; a
// player in one team lands on that team after sign-in, and their menu has no
// Overview, Members or Children. Opt-in like the other single sign-on specs:
// CALBOOKER_SSO_E2E=1 (make e2e) with goweb's Hydra and provider running and
// calbooker started with make run.

/** The calendar date `days` from now as YYYY-MM-DD (UTC; events sit at midday). */
function dateFromToday(days: number): string {
  const date = new Date(Date.now() + days * 24 * 60 * 60 * 1000);
  return date.toISOString().slice(0, 10);
}

async function createGroup(page: Page, name: string): Promise<string> {
  await page.goto(`${appURL}/en/groups`);
  await page.locator("#group-new-name").fill(name);
  await page.locator("#group-new-name").locator("xpath=ancestor::form").getByRole("button").click();
  await page.waitForURL(/\/en\/groups\/(?!join$)[a-z0-9-]+$/);
  return page.url();
}

async function createTeam(page: Page, groupURL: string, name: string, year: string): Promise<string> {
  await page.goto(`${groupURL}/teams`);
  await page.locator("#team-new-name").fill(name);
  await page.locator("#team-new-birth-year-from").fill(year);
  await page.locator("#team-new-birth-year-to").fill(year);
  await page.getByRole("button", { name: "Create a team" }).click();
  await page.waitForURL(/\/en\/groups\/[a-z0-9-]+\/teams\/[a-z0-9-]+$/);
  return page.url();
}

async function createEvent(page: Page, groupURL: string, event: { title: string; team: string; day: string; from: string; to: string }): Promise<void> {
  await page.goto(`${groupURL}/events`);
  await page.locator("#event-new-kind").selectOption({ label: "Training" });
  await page.locator("#event-new-title").fill(event.title);
  await page.locator("#event-new-starts-at").fill(`${event.day}T${event.from}`);
  await page.locator("#event-new-ends-at").fill(`${event.day}T${event.to}`);
  await page.getByTestId("event-new-teams").getByLabel(event.team).check();
  await page.locator("#event-new-location").fill("Club ground");
  await page.getByRole("button", { name: "Add an event" }).click();
  await page.waitForURL(/\/en\/groups\/[a-z0-9-]+\/events\/\d+$/);
}

/** The group menu entries of the current page, in order. */
async function menu(page: Page): Promise<string[]> {
  return page.locator("a[data-section]").evaluateAll((links) => links.map((link) => link.getAttribute("data-section") ?? ""));
}

test.describe("role based start page and navigation", () => {
  test.skip(!ssoEnabled, "set CALBOOKER_SSO_E2E=1 (make e2e) to run the role navigation specs");
  test.describe.configure({ mode: "serial" });

  test("a parent sees colour-coded events of both children and filters by one", async ({ browser }) => {
    test.setTimeout(180_000);
    const owner = await poolUser(browser, "owner");
    const page = owner.page;
    const lions = uniqueName("Lions");
    const tigers = uniqueName("Tigers");
    const training = uniqueName("Training");
    const match = uniqueName("Match");
    const anna = uniqueName("Anna");
    const ben = uniqueName("Ben");
    const day = dateFromToday(7);

    const groupURL = await createGroup(page, uniqueName("Club"));
    const lionsURL = await createTeam(page, groupURL, lions, "2015");
    const tigersURL = await createTeam(page, groupURL, tigers, "2017");
    await createEvent(page, groupURL, { title: training, team: lions, day, from: "11:00", to: "12:30" });
    await createEvent(page, groupURL, { title: match, team: tigers, day, from: "13:00", to: "14:30" });

    // A guest parent adds two children; the owner puts one on each team.
    const parent = await becomeGuest(browser);
    await joinWithCode(parent.page, appURL, await createInvite(page, groupURL, { role: "parent" }));
    await parent.page.waitForURL(groupURL);
    const childIDs: string[] = [];
    for (const name of [anna, ben]) {
      await parent.page.goto(`${groupURL}/children`);
      await parent.page.locator("#child-new-name").fill(name);
      await parent.page.locator("#child-new-birth-year").fill("2016");
      await parent.page.getByRole("button", { name: "Add a child" }).click();
      await expect(parent.page.locator("#alerts")).toContainText("Child added.");
      childIDs.push((await parent.page.getByTestId("children").locator("li[data-child-id]").filter({ hasText: name }).getAttribute("data-child-id")) ?? "");
    }
    for (const [teamURL, childID] of [[lionsURL, childIDs[0]], [tigersURL, childIDs[1]]]) {
      await page.goto(teamURL);
      await page.locator("#team-roster-entry").selectOption(`${childID}:player`);
      await page.getByRole("button", { name: "Add to team" }).click();
      await expect(page.locator("#alerts")).toContainText("Added to the team.");
    }

    // The menu has no Overview, Members or Children; Teams stays (two teams).
    await parent.page.goto(`${groupURL}/calendar`);
    const entries = await menu(parent.page);
    expect(entries).toEqual(expect.arrayContaining(["calendar", "teams", "events"]));
    for (const hidden of ["overview", "members", "children"]) {
      expect(entries).not.toContain(hidden);
    }
    await expect(parent.page.locator("a[data-section=teams]")).toHaveText("Teams");

    // Next week's calendar: both events, each marked with its child's colour.
    await parent.page.locator("#calendar-grid [data-wg-next]").click();
    await parent.page.waitForURL(/week=/);
    const grid = parent.page.locator("#calendar-grid");
    const trainingItem = grid.locator("[data-wg-item]").filter({ hasText: training });
    const matchItem = grid.locator("[data-wg-item]").filter({ hasText: match });
    await expect(trainingItem).toBeVisible();
    await expect(matchItem).toBeVisible();
    const legend = parent.page.getByTestId("calendar-children");
    await expect(legend).toContainText(anna);
    await expect(legend).toContainText(ben);
    const trainingColour = await trainingItem.locator(".child-dot").getAttribute("class");
    const matchColour = await matchItem.locator(".child-dot").getAttribute("class");
    expect(trainingColour).toMatch(/child-color-\d/);
    expect(matchColour).toMatch(/child-color-\d/);
    expect(trainingColour).not.toEqual(matchColour);
    await expect(trainingItem.locator(".child-dot")).toHaveAttribute("title", anna);
    await expect(matchItem.locator(".child-dot")).toHaveAttribute("title", ben);

    // Filter by Ben: only the Tigers match stays, and the week is kept.
    await parent.page.locator("#calendar-filter-child").selectOption({ label: ben });
    await parent.page.waitForURL(/child=\d+/);
    expect(parent.page.url()).toContain("week=");
    await expect(grid.getByRole("link", { name: training })).toHaveCount(0);
    await expect(grid.getByRole("link", { name: match })).toBeVisible();
    await parent.page.getByTestId("calendar-clear").click();
    await expect(grid.getByRole("link", { name: training })).toBeVisible();

    await parent.context.close();
    await owner.context.close();
  });

  test("a player in one team lands on that team and has a lean menu", async ({ browser }) => {
    test.setTimeout(180_000);
    const owner = await poolUser(browser, "owner");
    const lions = uniqueName("Lions");
    const groupURL = await createGroup(owner.page, uniqueName("Club"));
    const teamURL = await createTeam(owner.page, groupURL, lions, "2015");
    const inviteCode = await createInvite(owner.page, groupURL, { role: "player", team: lions });

    // A brand-new account has no other group or team, so this is its only one.
    const player = await signUpUser(browser, "calbooker-player");
    await joinWithCode(player.page, appURL, inviteCode);
    await player.page.waitForURL(teamURL);
    const entries = await menu(player.page);
    expect(entries).toEqual(expect.arrayContaining(["calendar", "teams", "events"]));
    for (const hidden of ["overview", "members", "children"]) {
      expect(entries).not.toContain(hidden);
    }
    // One team: its name replaces "Teams" and links straight to the team page.
    const teamsEntry = player.page.locator("a[data-section=teams]");
    await expect(teamsEntry).toHaveText(lions);
    await expect(teamsEntry).toHaveAttribute("href", new URL(teamURL).pathname);

    // The player's calendar covers the team.
    await player.page.locator("a[data-section=calendar]").click();
    await player.page.waitForURL(/\/calendar$/);
    await expect(player.page.locator("#calendar-heading")).toBeVisible();

    // Signing in again (the provider session stays) lands on the team page.
    await player.context.clearCookies({ domain: new URL(appURL).hostname });
    await startSignIn(player.page);
    await expect(player.page).toHaveURL(teamURL);

    await player.context.close();
    await owner.context.close();
  });
});
