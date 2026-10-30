import { expect, test } from "@playwright/test";

import { appURL, becomeGuest, createInvite, joinWithCode, poolUser, ssoEnabled, uniqueName } from "./support";

// Parent links on the Members page and the A/B weeks switch on the plan pages
// (#38). Opt-in like the other single sign-on specs: CALBOOKER_SSO_E2E=1
// (make e2e) with goweb's Hydra and provider running and calbooker started
// with make run.

/** The calendar date `days` from now as YYYY-MM-DD (UTC; the margins are wide). */
function dateFromToday(days: number): string {
  return new Date(Date.now() + days * 24 * 60 * 60 * 1000).toISOString().slice(0, 10);
}

test.describe("members links and A/B weeks", () => {
  test.skip(!ssoEnabled, "set CALBOOKER_SSO_E2E=1 (make e2e) to run the members and A/B specs");
  test.describe.configure({ mode: "serial" });

  test("staff link a parent to a player on Members and see it on the team roster", async ({ browser }) => {
    test.setTimeout(120_000);
    const owner = await poolUser(browser, "owner");
    const page = owner.page;

    await page.goto(`${appURL}/en/groups`);
    await page.locator("#group-new-name").fill(uniqueName("Club"));
    await page.locator("#group-new-name").locator("xpath=ancestor::form").getByRole("button").click();
    await page.waitForURL(/\/en\/groups\/\d+$/);
    const groupURL = page.url();

    await page.goto(`${groupURL}/teams`);
    await page.locator("#team-new-name").fill("Lions");
    await page.locator("#team-new-birth-year-from").fill("2015");
    await page.locator("#team-new-birth-year-to").fill("2016");
    await page.getByRole("button", { name: "Create a team" }).click();
    await page.waitForURL(/\/en\/groups\/\d+\/teams\/\d+$/);
    const teamURL = page.url();

    // A guest joins as a player, a signed-in account as a parent.
    const player = await becomeGuest(browser);
    await joinWithCode(player.page, appURL, await createInvite(page, groupURL, { role: "player" }));
    await player.page.waitForURL(groupURL);
    const parent = await poolUser(browser, "parent");
    await joinWithCode(parent.page, appURL, await createInvite(page, groupURL, { role: "parent" }));
    await parent.page.waitForURL(groupURL);

    // Put the player on the team.
    await page.goto(teamURL);
    const option = page.locator("#team-roster-entry option", { hasText: "(Player)" }).first();
    await page.locator("#team-roster-entry").selectOption({ value: (await option.getAttribute("value")) ?? "" });
    await page.getByRole("button", { name: "Add to team" }).click();
    await expect(page.locator("#alerts")).toContainText("Added to the team.");

    // Members: the parent row links to the player.
    await page.goto(`${groupURL}/members`);
    const parentRow = page.locator("tr[data-member-id]").filter({ has: page.locator("[data-role=parent]") });
    await expect(parentRow.getByTestId("member-players")).toContainText("None");
    // A short window makes the page scroll, so a jump to the bottom is visible (#41).
    await page.setViewportSize({ width: 1000, height: 130 });
    await page.evaluate(() => window.scrollTo(0, 0));
    await parentRow.scrollIntoViewIfNeeded();
    const select = parentRow.locator("select[name=child_id]");
    const playerValue = (await select.locator("option:not([value=''])").first().getAttribute("value")) ?? "";
    await select.selectOption(playerValue);
    await parentRow.getByRole("button", { name: "Link" }).evaluate((button) => button.scrollIntoView({ block: "start" }));
    await parentRow.getByRole("button", { name: "Link" }).click();
    await expect(page.locator("#alerts")).toContainText("Parent linked.");
    // The linked row stays in view after linking (#41).
    await expect(page.locator("tr[data-member-id]").filter({ has: page.locator("[data-role=parent]") })).toBeInViewport();
    await expect(page.locator("tr[data-member-id]").filter({ has: page.locator("[data-role=parent]") }).locator(`[data-player="${playerValue}"]`)).toBeVisible();
    // The player's row shows the parent.
    await expect(page.locator(`tr[data-member-id="${playerValue}"]`).getByTestId("member-parents")).toBeVisible();

    // The team roster lists the linked parent.
    await page.goto(teamURL);
    await expect(page.getByTestId("team-parents").locator("[data-roster-role=parent][data-roster-linked=true]")).toHaveCount(1);

    // Unlink on Members; the roster drops the parent.
    await page.goto(`${groupURL}/members`);
    await page.locator(`[data-player="${playerValue}"]`).click();
    await expect(page.locator("#alerts")).toContainText("Parent unlinked.");
    await expect(page.locator("tr[data-member-id]").filter({ has: page.locator("[data-role=parent]") })).toBeInViewport();
    await expect(page.locator(`tr[data-member-id="${playerValue}"]`).getByTestId("member-parents")).toHaveCount(0);
    await page.goto(teamURL);
    await expect(page.getByTestId("team-parents").locator("[data-roster-linked=true]")).toHaveCount(0);

    // Players see no link controls.
    await player.page.goto(`${groupURL}/members`);
    await expect(player.page.getByTestId("member-players")).toHaveCount(0);

    await parent.context.close();
    await player.context.close();
    await owner.context.close();
  });

  test("staff switch A/B weeks on the slot plan page and the training plan board", async ({ browser }) => {
    test.setTimeout(120_000);
    const owner = await poolUser(browser, "owner");
    const page = owner.page;
    const seasonName = uniqueName("Season");

    await page.goto(`${appURL}/en/groups`);
    await page.locator("#group-new-name").fill(uniqueName("Club"));
    await page.locator("#group-new-name").locator("xpath=ancestor::form").getByRole("button").click();
    await page.waitForURL(/\/en\/groups\/\d+$/);
    const groupURL = page.url();

    // A season whose only part does not alternate.
    await page.goto(`${groupURL}/seasons`);
    await page.locator("#season-new-name").fill(seasonName);
    await page.locator("#season-new-starts-on").fill(dateFromToday(-30));
    await page.locator("#season-new-ends-on").fill(dateFromToday(300));
    await page.locator("#season-new-part1-name").fill("Main");
    await page.locator("#season-new-part1-starts-on").fill(dateFromToday(-30));
    await page.locator("#season-new-part1-ends-on").fill(dateFromToday(300));
    await page.getByRole("button", { name: "Add a season" }).click();
    await page.waitForURL(/\/en\/groups\/\d+\/seasons\/\d+$/);
    const seasonURL = page.url();

    // The slot plan page: no week tabs until the switch is on.
    await page.goto(`${groupURL}/plan`);
    await expect(page.getByTestId("plan-part-info")).toContainText(seasonName);
    await expect(page.locator("[data-wg-week-tab]")).toHaveCount(0);
    await page.getByTestId("plan-alternating-form").getByTestId("part-alternating-toggle").click();
    await expect(page.locator("#alerts")).toContainText("A/B weeks saved.");
    await expect(page.locator("[data-wg-week-tab]")).toHaveCount(2);
    await expect(page.getByTestId("plan-part-info")).toContainText("A/B weeks");

    // The season form shows the value read only, with a link back to the plan.
    await page.goto(seasonURL);
    const readOnly = page.getByTestId("season-edit-part1-alternating-readonly");
    await expect(readOnly).toContainText("On");
    await expect(page.locator("#season-edit-part1-alternating")).toHaveCount(0);
    await readOnly.getByRole("link", { name: "Change on the plan page" }).click();
    await page.waitForURL(/\/plan\?part=\d+$/);

    // The training plan board has the same switch; turning it off asks first.
    await page.goto(`${groupURL}/training-plan`);
    await expect(page.getByTestId("training-plan-part-info")).toContainText("A/B weeks");
    page.once("dialog", (dialog) => void dialog.accept());
    await page.getByTestId("training-plan-alternating-form").getByTestId("part-alternating-toggle").click();
    await expect(page.locator("#alerts")).toContainText("A/B weeks saved.");
    await expect(page.getByTestId("training-plan-part-info")).not.toContainText("A/B weeks");
    await page.goto(`${groupURL}/plan`);
    await expect(page.locator("[data-wg-week-tab]")).toHaveCount(0);

    // A coach sees the plan but no switch.
    const coach = await poolUser(browser, "coach");
    await joinWithCode(coach.page, appURL, await createInvite(page, groupURL, { role: "coach" }));
    await coach.page.waitForURL(groupURL);
    await coach.page.goto(`${groupURL}/plan`);
    await expect(coach.page.getByTestId("plan-read-only")).toBeVisible();
    await expect(coach.page.getByTestId("part-alternating-toggle")).toHaveCount(0);

    await coach.context.close();
    await owner.context.close();
  });
});
