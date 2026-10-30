import { expect, test } from "@playwright/test";

import { appURL, becomeGuest, createInvite, joinWithCode, poolUser, ssoEnabled, uniqueName } from "./support";

// Seasons (#5): admins and managers create seasons with one or two parts and
// assign coaches to teams per season; coaches of the current season manage
// their teams' rosters and player invites. Opt-in like the other single
// sign-on specs: CALBOOKER_SSO_E2E=1 (make e2e) with goweb's Hydra and
// provider running and calbooker started with make run. The owner and coach
// come from the account pool; the player is a guest.

/** The calendar date `days` from now as YYYY-MM-DD (UTC; the margins are wide). */
function dateFromToday(days: number): string {
  const date = new Date(Date.now() + days * 24 * 60 * 60 * 1000);
  return date.toISOString().slice(0, 10);
}

test.describe("seasons", () => {
  test.skip(!ssoEnabled, "set CALBOOKER_SSO_E2E=1 (make e2e) to run the season specs");
  test.describe.configure({ mode: "serial" });

  test("an admin creates a two-part season and assigns a coach, who then manages the team", async ({ browser }) => {
    test.setTimeout(120_000);
    const owner = await poolUser(browser, "owner");
    const page = owner.page;
    const clubName = uniqueName("Club");
    const teamName = uniqueName("Lions");
    const seasonName = uniqueName("Season");

    await page.goto(`${appURL}/en/groups`);
    await page.locator("#group-new-name").fill(clubName);
    await page.locator("#group-new-name").locator("xpath=ancestor::form").getByRole("button").click();
    await page.waitForURL(/\/en\/groups\/(?!join$)[a-z0-9-]+$/);
    const groupURL = page.url();

    // A team to coach.
    await page.locator("nav a[data-section=teams]").click();
    await page.locator("#team-new-name").fill(teamName);
    await page.locator("#team-new-birth-year-from").fill("2015");
    await page.locator("#team-new-birth-year-to").fill("2016");
    await page.getByRole("button", { name: "Create a team" }).click();
    await page.waitForURL(/\/en\/groups\/[a-z0-9-]+\/teams\/[a-z0-9-]+$/);
    const teamURL = page.url();
    await expect(page.getByTestId("team-coaches-no-seasons")).toBeVisible();

    // A season around today with two parts; part 2 has A/B weeks.
    await page.locator("nav a[data-section=seasons]").click();
    await expect(page.getByTestId("seasons-empty")).toBeVisible();
    await page.locator("#season-new-name").fill(seasonName);
    await page.locator("#season-new-starts-on").fill(dateFromToday(-30));
    await page.locator("#season-new-ends-on").fill(dateFromToday(300));
    await page.locator("#season-new-part1-name").fill("Autumn");
    await page.locator("#season-new-part1-starts-on").fill(dateFromToday(-30));
    await page.locator("#season-new-part1-ends-on").fill(dateFromToday(60));

    // Part 2 overlapping part 1 is refused with the form kept.
    await page.locator("#season-new-part2-name").fill("Spring");
    await page.locator("#season-new-part2-starts-on").fill(dateFromToday(50));
    await page.locator("#season-new-part2-ends-on").fill(dateFromToday(300));
    await page.locator("#season-new-part2-alternating").check();
    await page.getByRole("button", { name: "Add a season" }).click();
    await expect(page.locator("#season-new-part2-dates-error")).toContainText("Part 2 must start after part 1 ends");
    await expect(page.locator("#season-new-name")).toHaveValue(seasonName);

    await page.locator("#season-new-part2-starts-on").fill(dateFromToday(61));
    await page.getByRole("button", { name: "Add a season" }).click();
    await page.waitForURL(/\/en\/groups\/[a-z0-9-]+\/seasons\/\d+$/);
    const seasonURL = page.url();
    await expect(page.locator("#season-heading")).toContainText(seasonName);
    await expect(page.getByTestId("season-current-badge")).toBeVisible();
    await expect(page.getByTestId("season-parts").locator("li")).toHaveCount(2);
    await expect(page.locator("li[data-part-position='2']")).toContainText("Spring");
    await expect(page.locator("li[data-part-position='2']").getByTestId("season-part-alternating")).toBeVisible();
    await expect(page.getByTestId("season-coaches-empty")).toBeVisible();

    // A second season may not overlap the first.
    await page.goto(`${groupURL}/seasons`);
    await page.locator("#season-new-name").fill(uniqueName("Overlap"));
    await page.locator("#season-new-starts-on").fill(dateFromToday(200));
    await page.locator("#season-new-ends-on").fill(dateFromToday(500));
    await page.locator("#season-new-part1-name").fill("Whole");
    await page.locator("#season-new-part1-starts-on").fill(dateFromToday(200));
    await page.locator("#season-new-part1-ends-on").fill(dateFromToday(500));
    await page.getByRole("button", { name: "Add a season" }).click();
    await expect(page.locator("#season-new-dates-error")).toContainText("overlap another season");

    // The coach joins; before an assignment the team stays hidden.
    const coach = await poolUser(browser, "coach");
    const coachCode = await createInvite(page, groupURL, { role: "coach" });
    await joinWithCode(coach.page, appURL, coachCode);
    await coach.page.waitForURL(groupURL);
    expect((await coach.page.goto(teamURL))?.status()).toBe(404);

    // A player (guest) joins without a team, to be put on the roster.
    const playerCode = await createInvite(page, groupURL, { role: "player" });
    const player = await becomeGuest(browser);
    await joinWithCode(player.page, appURL, playerCode);
    await player.page.waitForURL(groupURL);

    // The admin assigns the coach as head coach for the current season.
    await page.goto(teamURL);
    const coaches = page.getByTestId("team-coaches");
    await expect(page.locator("#team-coach-season option:checked")).toHaveText(seasonName);
    await page.locator("#team-coach-user").selectOption({ index: 0 });
    await page.locator("#team-coach-head").check();
    await page.getByRole("button", { name: "Assign coach" }).click();
    await expect(page.locator("#alerts")).toContainText("Coach assigned.");
    const currentSeason = coaches.locator("[data-testid=team-coaches-season][data-season-current=true]");
    await expect(currentSeason.locator("li[data-coach-id][data-coach-head=true]")).toHaveCount(1);
    await expect(currentSeason).toContainText("Head coach");

    // The coach now sees the team and manages its roster.
    await coach.page.goto(teamURL);
    await expect(coach.page.locator("#team-heading")).toContainText(teamName);
    await expect(coach.page.getByTestId("team-coaches")).toContainText("Head coach");
    await expect(coach.page.locator("#team-coach-user")).toHaveCount(0);
    await coach.page.locator("#team-roster-entry").selectOption({ index: 0 });
    await coach.page.getByRole("button", { name: "Add to team" }).click();
    await expect(coach.page.locator("#alerts")).toContainText("Added to the team.");
    await expect(coach.page.getByTestId("team-players").locator("li[data-roster-role=player]")).toHaveCount(1);

    // ...and creates a player invite for the team.
    const teamInvite = await createInvite(coach.page, groupURL, { role: "player", team: teamName });
    expect(teamInvite).not.toBe("");
    await expect(coach.page.locator("tr[data-invite-status=active]").filter({ hasText: teamInvite })).toContainText(teamName);

    // Coaches see the seasons read only.
    await coach.page.goto(`${groupURL}/seasons`);
    await expect(coach.page.getByTestId("seasons-read-only")).toBeVisible();
    await expect(coach.page.locator("#season-new-name")).toHaveCount(0);
    await expect(coach.page.locator("tr[data-season-current=true]")).toContainText(seasonName);
    await coach.page.goto(seasonURL);
    await expect(coach.page.getByTestId("season-coaches")).toContainText(teamName);
    await expect(coach.page.locator("#season-edit-name")).toHaveCount(0);

    // The player sees the team's current coach but not the season pages.
    await player.page.goto(teamURL);
    await expect(player.page.getByTestId("team-coaches")).toContainText("Head coach");
    await expect(player.page.locator("nav a[data-section=seasons]")).toHaveCount(0);
    expect((await player.page.goto(`${groupURL}/seasons`))?.status()).toBe(403);

    // Removing the coach takes the team away from them again.
    await page.goto(teamURL);
    await currentSeason.getByRole("button", { name: /Remove coach/ }).click();
    await expect(page.locator("#alerts")).toContainText("Coach removed.");
    expect((await coach.page.goto(teamURL))?.status()).toBe(404);

    await player.context.close();
    await coach.context.close();
    await owner.context.close();
  });
});
