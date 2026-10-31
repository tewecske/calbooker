import { expect, test } from "@playwright/test";

import { appURL, becomeGuest, createInvite, joinWithCode, pickEventTeams, poolUser, ssoEnabled, uniqueName } from "./support";

// Add my child (#62): a parent adds a child without an account to a team
// from the team page, sees the child on the roster and the calendar, and
// signs the child up for an event. Opt-in like the other single sign-on
// specs: CALBOOKER_SSO_E2E=1 (make e2e) with goweb's Hydra and provider
// running and calbooker started with make run.

/** The calendar date `days` from now as YYYY-MM-DD (UTC; the margins are wide). */
function dateFromToday(days: number): string {
  const date = new Date(Date.now() + days * 24 * 60 * 60 * 1000);
  return date.toISOString().slice(0, 10);
}

test.describe("add my child", () => {
  test.skip(!ssoEnabled, "set CALBOOKER_SSO_E2E=1 (make e2e) to run the add-child spec");
  test.describe.configure({ mode: "serial" });

  test("a parent adds a child from the team page, sees them on the roster and calendar, and signs them up", async ({ browser }) => {
    test.setTimeout(180_000);
    const owner = await poolUser(browser, "owner");
    const page = owner.page;
    const lions = uniqueName("Lions");
    const tigers = uniqueName("Tigers");

    await page.goto(`${appURL}/en/groups`);
    await page.locator("#group-new-name").fill(uniqueName("Club"));
    await page.locator("#group-new-name").locator("xpath=ancestor::form").getByRole("button").click();
    await page.waitForURL(/\/en\/groups\/(?!join$)[a-z0-9-]+$/);
    const groupURL = page.url();

    const teamURLs: Record<string, string> = {};
    for (const name of [lions, tigers]) {
      await page.goto(`${groupURL}/teams`);
      await page.locator("#team-new-name").fill(name);
      await page.locator("#team-new-birth-year-from").fill("2015");
      await page.locator("#team-new-birth-year-to").fill("2016");
      await page.getByRole("button", { name: "Create a team" }).click();
      await page.waitForURL(/\/en\/groups\/[a-z0-9-]+\/teams\/[a-z0-9-]+$/);
      teamURLs[name] = page.url();
    }

    // A parent joins the Lions with a parent invite and is offered the form.
    const parentCode = await createInvite(page, groupURL, { role: "parent", team: lions });
    const parent = await becomeGuest(browser);
    await joinWithCode(parent.page, appURL, parentCode);
    await parent.page.waitForURL(`${teamURLs[lions]}?add_child=1`);
    await expect(parent.page.getByTestId("add-child-prompt")).toBeVisible();

    const childName = uniqueName("Anna");
    await parent.page.locator("#team-child-name").fill(childName);
    await parent.page.locator("#team-child-birth-year").fill("2016");
    await parent.page.getByRole("button", { name: "Add my child" }).click();
    await expect(parent.page.locator("#alerts")).toContainText("Your child was added to the team.");

    // The child is on the roster with the no-account marker.
    const rosterRow = parent.page.getByTestId("team-players").locator("li[data-roster-user]").filter({ hasText: childName });
    await expect(rosterRow).toContainText("No account");
    await expect(parent.page.getByTestId("team-my-children")).toContainText(childName);

    // Editing: correct the birth year.
    const mine = parent.page.getByTestId("team-my-children").locator("li[data-child-user]").filter({ hasText: childName });
    await mine.getByText("Edit profile").click();
    await mine.locator("input[name=birth_year]").fill("2015");
    await mine.getByRole("button", { name: "Save" }).click();
    await expect(parent.page.locator("#alerts")).toContainText("Child saved.");
    await expect(mine).toContainText("2015");

    // A parent of the Lions cannot add the child to a team they are not a parent on.
    await parent.page.goto(teamURLs[tigers]);
    await expect(parent.page.getByTestId("team-add-child")).toHaveCount(0);

    // An event for the Lions: the child shows on the parent's calendar and the
    // parent signs the child up.
    const title = uniqueName("Training");
    const day = dateFromToday(7);
    await page.goto(`${groupURL}/events`);
    await page.locator("#event-new-title").fill(title);
    await page.locator("#event-new-starts-at").fill(`${day}T09:00`);
    await page.locator("#event-new-ends-at").fill(`${day}T10:30`);
    await pickEventTeams(page, [lions]);
    await page.locator("#event-new-location").fill("Clubhouse");
    await page.getByRole("button", { name: "Add an event" }).click();
    await page.waitForURL(/\/en\/groups\/[a-z0-9-]+\/events\/\d+$/);
    const eventURL = page.url();

    await parent.page.goto(`${teamURLs[lions]}/calendar?week=${day}`);
    await expect(parent.page.getByTestId("calendar-day-list").locator("li[data-event-id]").filter({ hasText: title })).toHaveCount(1);

    await parent.page.goto(eventURL);
    const childRow = parent.page.getByTestId("participation").locator("tr[data-participant-id]").filter({ hasText: childName });
    await expect(childRow).toContainText("Your child");
    await childRow.getByRole("button", { name: `I'll be there – ${childName}` }).click();
    await expect(parent.page.locator("#alerts")).toContainText("Answer saved.");
    await expect(childRow).toHaveAttribute("data-status", "signed_up");

    // Remove the child from the team again.
    await parent.page.goto(teamURLs[lions]);
    parent.page.once("dialog", (dialog) => void dialog.accept());
    await parent.page.getByTestId("team-my-children").getByRole("button", { name: `Remove from team: ${childName}` }).click();
    await expect(parent.page.locator("#alerts")).toContainText("Your child was removed from this team.");
    await expect(parent.page.getByTestId("team-players").locator("li[data-roster-user]").filter({ hasText: childName })).toHaveCount(0);

    await parent.context.close();
    await owner.context.close();
  });
});
