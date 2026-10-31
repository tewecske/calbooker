import { expect, test, type Page } from "@playwright/test";

import { appURL, becomeGuest, createInvite, joinWithCode, pickEventTeams, poolUser, ssoEnabled, uniqueName } from "./support";

// Sign-up and attendance (#9): a guest player signs up for an open event, a
// parent signs up a child profile, and the coach confirms attendance after
// an event. Opt-in like the other single sign-on specs: CALBOOKER_SSO_E2E=1
// (make e2e) with goweb's Hydra and provider running and calbooker started
// with make run. The owner and coach come from the account pool; the player
// and the parent are guests.

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
  await pickEventTeams(page, [team]);
  await page.locator("#event-new-location").fill("Clubhouse");
  await page.getByRole("button", { name: "Add an event" }).click();
  await page.waitForURL(/\/en\/groups\/[a-z0-9-]+\/events\/\d+$/);
  return page.url();
}

test.describe("participation", () => {
  test.skip(!ssoEnabled, "set CALBOOKER_SSO_E2E=1 (make e2e) to run the participation specs");
  test.describe.configure({ mode: "serial" });

  test("a guest player signs up, a parent signs up a child profile, and the coach confirms attendance", async ({ browser }) => {
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

    // A guest player joins the Lions.
    const playerCode = await createInvite(page, groupURL, { role: "player", team: lions });
    const player = await becomeGuest(browser);
    await joinWithCode(player.page, appURL, playerCode);
    await player.page.waitForURL(/\/en\/groups\/[a-z0-9-]+\/teams\/[a-z0-9-]+$/);

    // A guest parent adds a child profile; the owner puts it on the Lions.
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

    // An upcoming open training: the guest signs up.
    const upcomingURL = await createEvent(page, groupURL, lions, uniqueName("Training"), dateFromToday(7));
    await player.page.goto(upcomingURL);
    const card = player.page.getByTestId("participation");
    await expect(card.getByTestId("participation-phase")).toHaveText("Sign-up is open.");
    const mine = card.locator("tr[data-participant-id]").filter({ hasText: "You" });
    await mine.getByRole("button", { name: "I'll be there" }).click();
    await expect(player.page.locator("#alerts")).toContainText("Answer saved.");
    await expect(mine).toHaveAttribute("data-status", "signed_up");
    await expect(card.getByTestId("participation-summary")).toHaveText("1 coming");
    // The player cannot answer for the child.
    const childRow = card.locator("tr[data-participant-id]").filter({ hasText: childName });
    await expect(childRow.getByRole("button")).toHaveCount(0);

    // The parent signs up the child profile.
    await parent.page.goto(upcomingURL);
    const parentChildRow = parent.page.getByTestId("participation").locator("tr[data-participant-id]").filter({ hasText: childName });
    await expect(parentChildRow).toContainText("Your child");
    await parentChildRow.getByRole("button", { name: `I'll be there – ${childName}` }).click();
    await expect(parent.page.locator("#alerts")).toContainText("Answer saved.");
    await expect(parentChildRow).toHaveAttribute("data-status", "signed_up");
    await expect(parent.page.getByTestId("participation-summary")).toHaveText("2 coming");

    // The upcoming event has participants: deleting it is refused.
    await page.goto(upcomingURL);
    page.once("dialog", (dialog) => dialog.accept());
    await page.getByRole("button", { name: "Delete event" }).click();
    await expect(page.locator("#alerts")).toContainText("Cancel it instead.");

    // A training that already ended: the coach confirms attendance, ticking
    // the guest and leaving the child out.
    const pastURL = await createEvent(page, groupURL, lions, uniqueName("Past training"), dateFromToday(-2));
    await coach.page.goto(pastURL);
    await expect(coach.page.getByTestId("participation-phase")).toHaveText("The event has ended.");
    const form = coach.page.getByTestId("attendance-form");
    const guestRowID = await coach.page.getByTestId("participation").locator("tr[data-participant-id]").filter({ hasNot: coach.page.getByText(childName) }).first().getAttribute("data-participant-id");
    await form.locator(`#attended-${guestRowID}`).check();
    await form.getByLabel(childName).uncheck();
    await form.getByRole("button", { name: "Save attendance" }).click();
    await expect(coach.page.locator("#alerts")).toContainText("Attendance saved.");
    await expect(coach.page.getByTestId(`participant-${guestRowID}`)).toHaveAttribute("data-attended", "yes");
    await expect(coach.page.getByTestId(`participant-${guestRowID}`).getByTestId("participant-status")).toHaveText("Walk-in");
    await expect(coach.page.getByTestId("attendance-recorded")).toBeVisible();

    // The player sees the confirmed attendance but no attendance form.
    await player.page.goto(pastURL);
    await expect(player.page.getByTestId(`participant-${guestRowID}`)).toHaveAttribute("data-attended", "yes");
    await expect(player.page.getByTestId("attendance-form")).toHaveCount(0);

    await parent.context.close();
    await player.context.close();
    await coach.context.close();
    await owner.context.close();
  });
});
