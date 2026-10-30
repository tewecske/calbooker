import { expect, test, type Page } from "@playwright/test";

import { appURL, becomeGuest, createInvite, joinWithCode, poolUser, ssoEnabled, uniqueName } from "./support";

// Team invites, players in several teams, and per-event call-ups (#47). Opt-in
// like the other single sign-on specs: CALBOOKER_SSO_E2E=1 (make e2e) with
// goweb's Hydra and provider running and calbooker started with make run.
// The owner and coach come from the account pool; the players are guests.

/** The calendar date `days` from now as YYYY-MM-DD (UTC; the margins are wide). */
function dateFromToday(days: number): string {
  const date = new Date(Date.now() + days * 24 * 60 * 60 * 1000);
  return date.toISOString().slice(0, 10);
}

/** Creates an open event for one team on the events page and returns its URL. */
async function createEvent(page: Page, groupURL: string, team: string, title: string, day: string): Promise<string> {
  await page.goto(`${groupURL}/events`);
  await page.locator("#event-new-title").fill(title);
  await page.locator("#event-new-starts-at").fill(`${day}T09:00`);
  await page.locator("#event-new-ends-at").fill(`${day}T10:30`);
  await page.getByTestId("event-new-teams").getByLabel(team).check();
  await page.locator("#event-new-location").fill("Clubhouse");
  await page.getByRole("button", { name: "Add an event" }).click();
  await page.waitForURL(/\/en\/groups\/[a-z0-9-]+\/events\/\d+$/);
  return page.url();
}

/** Creates an invite on a team page's invite card and returns its code. */
async function createTeamInvite(page: Page, teamURL: string, role: "player" | "parent"): Promise<string> {
  await page.goto(`${teamURL}/invites`);
  const card = page.getByTestId("team-invites");
  await card.locator("#team-invite-role").selectOption(role);
  await card.getByRole("button", { name: "Create an invite" }).click();
  const code = page.getByTestId("invite-created-code");
  await expect(code).toBeVisible();
  return (await code.innerText()).trim();
}

async function createTeam(page: Page, groupURL: string, name: string, years: [string, string]): Promise<string> {
  await page.goto(`${groupURL}/teams`);
  await page.locator("#team-new-name").fill(name);
  await page.locator("#team-new-birth-year-from").fill(years[0]);
  await page.locator("#team-new-birth-year-to").fill(years[1]);
  await page.getByRole("button", { name: "Create a team" }).click();
  await page.waitForURL(/\/en\/groups\/[a-z0-9-]+\/teams\/[a-z0-9-]+$/);
  return page.url();
}

test.describe("team invites and call-ups", () => {
  test.skip(!ssoEnabled, "set CALBOOKER_SSO_E2E=1 (make e2e) to run the call-up specs");
  test.describe.configure({ mode: "serial" });

  test("a coach invites to the team, a player joins two teams, and a coach calls a player up from another team", async ({ browser }) => {
    test.setTimeout(240_000);
    const owner = await poolUser(browser, "owner");
    const page = owner.page;
    const lions = uniqueName("Lions");
    const tigers = uniqueName("Tigers");

    await page.goto(`${appURL}/en/groups`);
    await page.locator("#group-new-name").fill(uniqueName("Club"));
    await page.locator("#group-new-name").locator("xpath=ancestor::form").getByRole("button").click();
    await page.waitForURL(/\/en\/groups\/(?!join$)[a-z0-9-]+$/);
    const groupURL = page.url();
    const lionsURL = await createTeam(page, groupURL, lions, ["2015", "2016"]);
    const tigersURL = await createTeam(page, groupURL, tigers, ["2017", "2018"]);

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

    // The coach joins and coaches the Lions only.
    const coach = await poolUser(browser, "coach");
    await joinWithCode(coach.page, appURL, await createInvite(page, groupURL, { role: "coach" }));
    await coach.page.waitForURL(groupURL);
    await page.goto(lionsURL);
    await page.locator("#team-coach-user").selectOption({ index: 0 });
    await page.getByRole("button", { name: "Assign coach" }).click();
    await expect(page.locator("#alerts")).toContainText("Coach assigned.");

    // The Lions coach invites a player from the team page; the invite is bound
    // to the team, the card lists it, and the Tigers page has no card for
    // them.
    const lionsCode = await createTeamInvite(coach.page, lionsURL, "player");
    await expect(coach.page.getByTestId("team-invite-list")).toContainText(lionsCode);
    // The coach does not see the Tigers, so there is no invite tab for them.
    expect((await coach.page.goto(`${tigersURL}/invites`))?.ok()).toBe(false);
    const playerA = await becomeGuest(browser);
    await joinWithCode(playerA.page, appURL, lionsCode);
    await playerA.page.waitForURL(lionsURL);

    // Player A joins the Tigers too, with an invite the owner made on the
    // Tigers page: one player, two teams.
    await joinWithCode(playerA.page, appURL, await createTeamInvite(page, tigersURL, "player"));
    await playerA.page.waitForURL(tigersURL);
    await playerA.page.goto(`${groupURL}/teams`);
    await expect(playerA.page.getByRole("link", { name: new RegExp(lions) })).toBeVisible();
    await expect(playerA.page.getByRole("link", { name: new RegExp(tigers) })).toBeVisible();

    // Player B plays on the Tigers only.
    const playerB = await becomeGuest(browser);
    await joinWithCode(playerB.page, appURL, await createTeamInvite(page, tigersURL, "player"));
    await playerB.page.waitForURL(tigersURL);

    // A Tigers practice at the same time as the Lions training.
    const day = dateFromToday(7);
    await createEvent(page, groupURL, tigers, uniqueName("Tigers practice"), day);
    const lionsEventURL = await createEvent(page, groupURL, lions, uniqueName("Lions training"), day);

    // Player B does not see the Lions event.
    await playerB.page.goto(lionsEventURL);
    await expect(playerB.page.getByTestId("participation")).toHaveCount(0);

    // The Lions coach calls player B up, with a warning about the overlap.
    await coach.page.goto(lionsEventURL);
    const callUp = coach.page.getByTestId("call-up-form");
    await callUp.locator("select[name=user_id]").selectOption({ index: 1 });
    await callUp.getByRole("button", { name: "Call up" }).click();
    await expect(coach.page.locator("#alerts")).toContainText("Player called up.");
    await expect(coach.page.locator("#alerts")).toContainText("other events at the same time");
    const calledRow = coach.page.getByTestId("participation").locator("tr[data-status=nominated]").filter({ has: coach.page.getByTestId("participant-called-up") });
    await expect(calledRow).toHaveCount(1);
    await expect(calledRow.getByTestId("participant-status")).toHaveText("Called up");

    // Player B now sees the event, in the list and on its page, and answers.
    await playerB.page.goto(`${groupURL}/events`);
    await expect(playerB.page.getByRole("link", { name: /Lions training/ })).toBeVisible();
    await playerB.page.goto(lionsEventURL);
    const card = playerB.page.getByTestId("participation");
    const mine = card.locator("tr[data-participant-id]").filter({ hasText: "You" });
    await expect(mine).toHaveAttribute("data-status", "nominated");
    await expect(mine.getByTestId("participant-called-up")).toBeVisible();
    await mine.getByRole("button", { name: "Can't come" }).click();
    await expect(playerB.page.locator("#alerts")).toContainText("Answer saved.");
    await expect(mine).toHaveAttribute("data-status", "declined");
    await mine.getByRole("button", { name: "I'll come after all" }).click();
    await expect(mine).toHaveAttribute("data-status", "nominated");

    // The coach leaves a Lions player out; that player sees "Left out" and
    // cannot answer.
    await coach.page.goto(lionsEventURL);
    const playerARow = coach.page.getByTestId("participation").locator("tr[data-participant-id]").filter({ hasNot: coach.page.getByTestId("participant-called-up") }).first();
    await playerARow.getByRole("button", { name: /Leave out/ }).click();
    await expect(coach.page.locator("#alerts")).toContainText("Answer saved.");
    await playerA.page.goto(lionsEventURL);
    const own = playerA.page.getByTestId("participation").locator("tr[data-participant-id]").filter({ hasText: "You" });
    await expect(own).toHaveAttribute("data-status", "left_out");
    await expect(own.getByRole("button")).toHaveCount(0);

    // The coach withdraws the call-up: player B no longer sees the event.
    await coach.page.goto(lionsEventURL);
    await coach.page.getByTestId("participation").locator("tr[data-status=nominated]").filter({ has: coach.page.getByTestId("participant-called-up") }).getByRole("button", { name: /Withdraw call-up/ }).click();
    await expect(coach.page.locator("#alerts")).toContainText("Answer saved.");
    await playerB.page.goto(lionsEventURL);
    await expect(playerB.page.getByTestId("participation")).toHaveCount(0);
  });
});
