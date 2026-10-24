import { expect, test, type Locator, type Page } from "@playwright/test";

import { appURL, becomeGuest, createInvite, joinWithCode, poolUser, ssoEnabled, uniqueName } from "./support";

// Resource slot plan (#13): admins and managers build a season part's weekly
// slot plan on the week grid, copy last season's plan into a new part, move
// slots, and add slots for one A/B week; coaches see the plan read only;
// players and parents do not see it. Opt-in like the other single sign-on
// specs: CALBOOKER_SSO_E2E=1 (make e2e) with goweb's Hydra and provider
// running and calbooker started with make run.

/** The calendar date `days` from now as YYYY-MM-DD (UTC; the margins are wide). */
function dateFromToday(days: number): string {
  return new Date(Date.now() + days * 24 * 60 * 60 * 1000).toISOString().slice(0, 10);
}

async function box(locator: Locator): Promise<{ x: number; y: number; width: number; height: number }> {
  const result = await locator.boundingBox();
  if (!result) {
    throw new Error("element has no bounding box");
  }
  return result;
}

/** The page y of `minute` in a grid column. */
async function minuteY(page: Page, column: Locator, minute: number): Promise<number> {
  const grid = page.locator("[data-week-grid]");
  const start = Number(await grid.getAttribute("data-start"));
  const step = Number(await grid.getAttribute("data-step"));
  const rows = Number(await grid.getAttribute("data-rows"));
  const area = await box(column);
  return area.y + ((minute - start) / step) * (area.height / rows);
}

/** Clicks an empty spot of a weekday column at `minute` to add a slot. */
async function addSlot(page: Page, weekday: number, minute: number): Promise<void> {
  const column = page.locator(`[data-wg-column][data-weekday="${weekday}"]`).first();
  await column.scrollIntoViewIfNeeded();
  const area = await box(column);
  await page.mouse.click(area.x + area.width / 2, (await minuteY(page, column, minute)) + 3);
  await expect(page.locator("#alerts")).toContainText("Plan saved.");
}

/** Creates a season with one part that alternates A/B weeks. */
async function createSeason(page: Page, groupURL: string, name: string, from: number, to: number): Promise<void> {
  await page.goto(`${groupURL}/seasons`);
  await page.locator("#season-new-name").fill(name);
  await page.locator("#season-new-starts-on").fill(dateFromToday(from));
  await page.locator("#season-new-ends-on").fill(dateFromToday(to));
  await page.locator("#season-new-part1-name").fill("Main");
  await page.locator("#season-new-part1-starts-on").fill(dateFromToday(from));
  await page.locator("#season-new-part1-ends-on").fill(dateFromToday(to));
  await page.locator("#season-new-part1-alternating").check();
  await page.getByRole("button", { name: "Add a season" }).click();
  await page.waitForURL(/\/en\/groups\/\d+\/seasons\/\d+$/);
}

test.describe("plan", () => {
  test.skip(!ssoEnabled, "set CALBOOKER_SSO_E2E=1 (make e2e) to run the plan specs");
  test.describe.configure({ mode: "serial" });

  test("an admin copies last season's plan, moves a slot, and adds one for week B only", async ({ browser }) => {
    test.setTimeout(120_000);
    const owner = await poolUser(browser, "owner");
    const page = owner.page;
    const courtName = uniqueName("Court");
    const lastSeason = uniqueName("Last");
    const thisSeason = uniqueName("This");

    await page.goto(`${appURL}/en/groups`);
    await page.locator("#group-new-name").fill(uniqueName("Club"));
    await page.locator("#group-new-name").locator("xpath=ancestor::form").getByRole("button").click();
    await page.waitForURL(/\/en\/groups\/\d+$/);
    const groupURL = page.url();

    // Before seasons exist the plan says so.
    await page.locator("nav a[data-section=plan]").click();
    await expect(page.getByTestId("plan-no-seasons")).toBeVisible();

    // A court that takes one team at a time, and two seasons with A/B weeks.
    await page.goto(`${groupURL}/resources`);
    await page.locator("#resource-new-name").fill(courtName);
    await page.locator("#resource-new-kind").selectOption("court");
    await page.locator("#resource-new-max-teams").fill("1");
    await page.getByRole("button", { name: "Add a resource" }).click();
    await expect(page.locator("#alerts")).toContainText("Resource added.");
    await createSeason(page, groupURL, lastSeason, -400, -40);
    await createSeason(page, groupURL, thisSeason, -30, 300);

    // Last season's plan: an every-week slot for ages 8-10 and two teams,
    // and a week B slot.
    await page.goto(`${groupURL}/plan`);
    await page.locator("#plan-part").selectOption({ label: `${lastSeason}: Main` });
    await page.getByTestId("plan-part-picker").getByRole("button", { name: "Show" }).click();
    await expect(page.getByTestId("plan-part-info")).toContainText(lastSeason);
    await expect(page.getByTestId("plan-slots-empty")).toBeVisible();
    await addSlot(page, 1, 17 * 60);
    const mondaySlot = page.getByTestId("plan-slots").locator("tr[data-slot-id]");
    await expect(mondaySlot).toHaveCount(1);
    await expect(mondaySlot).toContainText("17:00–18:00");
    await expect(mondaySlot).toContainText("Week A only");
    await mondaySlot.getByRole("link", { name: /Open/ }).click();
    await page.waitForURL(/\/plan\/slots\/\d+$/);
    await page.locator("#plan-slot-week").selectOption("all");
    await page.locator("#plan-slot-min-age").fill("8");
    await page.locator("#plan-slot-max-age").fill("10");
    await page.locator("#plan-slot-max-teams").fill("2");
    await page.getByRole("button", { name: "Save slot" }).click();
    await page.waitForURL(/\/plan\?part=\d+$/);
    // Two teams on a one-team court is a warning, not a block.
    await expect(page.getByTestId("plan-warnings")).toContainText("slots allow 2 teams at once, the resource takes 1");
    await expect(page.locator("[data-wg-item][data-weekday='1'] [data-wg-warning]")).toHaveCount(1);
    await page.locator("[data-wg-week-tab]", { hasText: "Week B" }).click();
    await expect(page.locator("[data-week-grid]")).toHaveAttribute("data-week", "B");
    await addSlot(page, 4, 10 * 60);
    await expect(page.getByTestId("plan-slots").locator("tr[data-slot-id]")).toHaveCount(2);

    // This season's part is empty and opens by default; copy last season's.
    await page.goto(`${groupURL}/plan`);
    await expect(page.getByTestId("plan-part-info")).toContainText(thisSeason);
    await expect(page.getByTestId("plan-slots-empty")).toBeVisible();
    await expect(page.locator("#plan-copy-source option:checked")).toContainText(`Last season: ${lastSeason}, Main (2 slots)`);
    await page.getByRole("button", { name: "Copy plan" }).click();
    await expect(page.locator("#alerts")).toContainText("Copied 2 slots.");
    const weekA = page.getByTestId("plan-slots").locator("tr[data-slot-id]");
    await expect(weekA).toHaveCount(1);
    await expect(weekA).toContainText("Ages 8–10");
    await expect(weekA).toContainText("Every week");
    await expect(weekA).toContainText("Up to 2 teams");

    // Copying again replaces the slots after confirmation.
    await page.locator("#plan-copy-replace").check();
    page.once("dialog", (dialog) => void dialog.accept());
    await page.getByRole("button", { name: "Copy plan" }).click();
    await expect(page.locator("#alerts")).toContainText("Copied 2 slots.");
    await expect(page.getByTestId("plan-slots").locator("tr[data-slot-id]")).toHaveCount(1);

    // Move the Monday slot to Wednesday 13:00 by dragging it.
    const item = page.locator("[data-wg-column][data-weekday='1'] [data-wg-item]");
    await item.scrollIntoViewIfNeeded();
    const from = await box(item);
    const wednesday = page.locator("[data-wg-column][data-weekday='3']").first();
    const target = await box(wednesday);
    const grab = 6;
    await page.mouse.move(from.x + from.width / 2, from.y + grab);
    await page.mouse.down();
    await page.mouse.move(from.x + from.width / 2 + 20, from.y + grab + 10, { steps: 5 });
    await page.mouse.move(target.x + target.width / 2, (await minuteY(page, wednesday, 13 * 60)) + grab, { steps: 20 });
    await page.mouse.up();
    await expect(page.locator("#alerts")).toContainText("Plan saved.");
    const moved = page.locator("[data-wg-column][data-weekday='3'] [data-wg-item]");
    await expect(moved).toHaveAttribute("data-start", "780");
    await expect(moved).toHaveAttribute("data-end", "840");
    await expect(page.getByTestId("plan-slots").locator("tr[data-slot-id]")).toContainText("Wednesday");
    await expect(page.getByTestId("plan-slots").getByTestId("plan-slot-time")).toHaveText("13:00–14:00");

    // Add a slot for week B only: week B shows it, week A does not.
    await page.locator("[data-wg-week-tab]", { hasText: "Week B" }).click();
    await expect(page.locator("[data-week-grid]")).toHaveAttribute("data-week", "B");
    // The copied week B slot and the moved every-week slot.
    await expect(page.getByTestId("plan-slots").locator("tr[data-slot-id]")).toHaveCount(2);
    await addSlot(page, 5, 18 * 60);
    const weekB = page.getByTestId("plan-slots").locator("tr[data-slot-id]");
    await expect(weekB).toHaveCount(3);
    await expect(weekB.filter({ hasText: "Friday" })).toContainText("Week B only");
    await page.reload();
    await expect(page.getByTestId("plan-slots").locator("tr[data-slot-id]").filter({ hasText: "Friday" })).toHaveCount(1);
    await page.locator("[data-wg-week-tab]", { hasText: "Week A" }).click();
    await expect(page.locator("[data-week-grid]")).toHaveAttribute("data-week", "A");
    await expect(page.getByTestId("plan-slots").locator("tr[data-slot-id]").filter({ hasText: "Friday" })).toHaveCount(0);

    // A coach sees the plan read only; a player does not see it at all.
    const coach = await poolUser(browser, "coach");
    await joinWithCode(coach.page, appURL, await createInvite(page, groupURL, { role: "coach" }));
    await coach.page.waitForURL(groupURL);
    await coach.page.locator("nav a[data-section=plan]").click();
    await expect(coach.page.getByTestId("plan-read-only")).toBeVisible();
    await expect(coach.page.getByTestId("plan-slots").locator("tr[data-slot-id]")).toHaveCount(1);
    await expect(coach.page.locator("[data-wg-draggable]")).toHaveCount(0);
    await expect(coach.page.locator("#plan-copy-source")).toHaveCount(0);

    const player = await becomeGuest(browser);
    await joinWithCode(player.page, appURL, await createInvite(page, groupURL, { role: "player" }));
    await player.page.waitForURL(groupURL);
    await expect(player.page.locator("nav a[data-section=plan]")).toHaveCount(0);
    expect((await player.page.goto(`${groupURL}/plan`))?.status()).toBe(403);

    await player.context.close();
    await coach.context.close();
    await owner.context.close();
  });
});
