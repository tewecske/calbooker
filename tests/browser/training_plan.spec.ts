import { expect, test, type Locator, type Page } from "@playwright/test";

import { appURL, createInvite, joinWithCode, poolUser, ssoEnabled, uniqueName } from "./support";

// Training plan board (#14): staff suggest trainings into the resource slots,
// move a team to another slot, see the coach-availability warning, and save;
// teams can also be dragged from the list; coaches see the saved plan read
// only. Opt-in like the other single sign-on specs: CALBOOKER_SSO_E2E=1
// (make e2e) with goweb's Hydra and provider running and calbooker started
// with make run. Uses the pooled "owner" and "coach" accounts.

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

/** The page y of `minute` in a grid column of the grid with `selector`. */
async function minuteY(page: Page, selector: string, column: Locator, minute: number): Promise<number> {
  const grid = page.locator(selector);
  const start = Number(await grid.getAttribute("data-start"));
  const step = Number(await grid.getAttribute("data-step"));
  const rows = Number(await grid.getAttribute("data-rows"));
  const area = await box(column);
  return area.y + ((minute - start) / step) * (area.height / rows);
}

/** Drags with the mouse the way SortableJS's fallback mode expects. */
async function dragTo(page: Page, from: Locator, x: number, y: number, grabY = 6): Promise<void> {
  await from.scrollIntoViewIfNeeded();
  const start = await box(from);
  await page.mouse.move(start.x + start.width / 2, start.y + grabY);
  await page.mouse.down();
  await page.mouse.move(start.x + start.width / 2 + 20, start.y + grabY + 10, { steps: 5 });
  await page.mouse.move(x, y, { steps: 20 });
  await page.mouse.up();
}

test.describe("training plan", () => {
  test.skip(!ssoEnabled, "set CALBOOKER_SSO_E2E=1 (make e2e) to run the training plan specs");
  test.describe.configure({ mode: "serial" });

  test("staff suggest, move a team to another slot, see the availability warning, and save", async ({ browser }) => {
    test.setTimeout(180_000);
    const owner = await poolUser(browser, "owner");
    const page = owner.page;
    const lions = uniqueName("Lions");
    const seasonName = uniqueName("Season");
    const field = uniqueName("Field");
    const startYear = Number(dateFromToday(-30).slice(0, 4));

    await page.goto(`${appURL}/en/groups`);
    await page.locator("#group-new-name").fill(uniqueName("Club"));
    await page.locator("#group-new-name").locator("xpath=ancestor::form").getByRole("button").click();
    await page.waitForURL(/\/en\/groups\/\d+$/);
    const groupURL = page.url();

    // A team aged 9-10, one resource, and a season around today.
    await page.goto(`${groupURL}/teams`);
    await page.locator("#team-new-name").fill(lions);
    await page.locator("#team-new-birth-year-from").fill(String(startYear - 10));
    await page.locator("#team-new-birth-year-to").fill(String(startYear - 9));
    await page.getByRole("button", { name: "Create a team" }).click();
    await page.waitForURL(/\/en\/groups\/\d+\/teams\/\d+$/);
    const teamURL = page.url();
    await page.goto(`${groupURL}/resources`);
    await page.locator("#resource-new-name").fill(field);
    await page.getByRole("button", { name: "Add a resource" }).click();
    await expect(page.locator("#alerts")).toContainText("Resource added.");
    await page.goto(`${groupURL}/seasons`);
    await page.locator("#season-new-name").fill(seasonName);
    await page.locator("#season-new-starts-on").fill(dateFromToday(-30));
    await page.locator("#season-new-ends-on").fill(dateFromToday(300));
    await page.locator("#season-new-part1-name").fill("Whole");
    await page.locator("#season-new-part1-starts-on").fill(dateFromToday(-30));
    await page.locator("#season-new-part1-ends-on").fill(dateFromToday(300));
    await page.getByRole("button", { name: "Add a season" }).click();
    await page.waitForURL(/\/en\/groups\/\d+\/seasons\/\d+$/);

    // The coach joins, coaches the team, and is free on Tuesday only.
    const coach = await poolUser(browser, "coach");
    await joinWithCode(coach.page, appURL, await createInvite(page, groupURL, { role: "coach" }));
    await coach.page.waitForURL(groupURL);
    await page.goto(teamURL);
    await expect(page.locator("#team-coach-season option:checked")).toHaveText(seasonName);
    await page.locator("#team-coach-user").selectOption({ index: 0 });
    await page.getByRole("button", { name: "Assign coach" }).click();
    await expect(page.locator("#alerts")).toContainText("Coach assigned.");
    await coach.page.goto(`${groupURL}/coaches`);
    await coach.page.getByRole("link", { name: "Edit my availability" }).click();
    await coach.page.waitForURL(/\/coaches\/\d+\/availability/);
    const availability = "#availability-grid";
    const tuesdayColumn = coach.page.locator(`${availability} [data-wg-column][data-weekday="2"]`);
    await tuesdayColumn.scrollIntoViewIfNeeded();
    const tuesdayArea = await box(tuesdayColumn);
    await coach.page.mouse.click(tuesdayArea.x + tuesdayArea.width / 2, (await minuteY(coach.page, availability, tuesdayColumn, 16 * 60)) + 3);
    await expect(coach.page.locator("#alerts")).toContainText("Availability added.");

    // Two one-hour slots at 16:00, Tuesday and Wednesday.
    await page.goto(`${groupURL}/plan`);
    for (const weekday of [2, 3]) {
      const column = page.locator(`[data-wg-column][data-weekday="${weekday}"]`).first();
      await column.scrollIntoViewIfNeeded();
      const area = await box(column);
      await page.mouse.click(area.x + area.width / 2, (await minuteY(page, "[data-week-grid]", column, 16 * 60)) + 3);
      await expect(page.locator("#alerts")).toContainText("Plan saved.");
      await page.waitForTimeout(500);
    }
    await expect(page.getByTestId("plan-slots").locator("tr[data-slot-id]")).toHaveCount(2);

    // The training plan starts empty; Suggest fits the team into the first slot
    // where its coach is free.
    await page.locator("nav a[data-section=training-plan]").click();
    await page.waitForURL(/\/training-plan/);
    const boardURL = page.url();
    await expect(page.getByTestId("training-plan-empty")).toBeVisible();
    await expect(page.getByTestId("training-plan-team-coaches")).not.toHaveText("");
    await page.getByTestId("training-plan-suggest").click();
    await expect(page.locator("#alerts")).toContainText("1 training suggested.");
    const row = page.getByTestId("training-plan-row");
    await expect(row).toHaveCount(1);
    await expect(row).toContainText("Tuesday");
    await expect(row.getByTestId("training-plan-row-time")).toContainText("16:00–17:00");
    await expect(row.getByTestId("training-plan-explanation")).toHaveText("Fits a resource slot");
    await expect(row.getByTestId("training-plan-row-warning")).toHaveCount(0);
    await expect(page.getByTestId("training-plan-warnings")).toHaveCount(0);
    await expect(page.getByTestId("training-plan-unsaved")).toBeVisible();

    // Drag the training to Wednesday at 16:00: the coach is not available.
    const item = page.locator("[data-wg-column][data-weekday='2'] .wg-item-movable");
    await item.scrollIntoViewIfNeeded();
    const wednesday = page.locator("[data-wg-column][data-weekday='3']").first();
    const target = await box(wednesday);
    await dragTo(page, item, target.x + target.width / 2, (await minuteY(page, "[data-week-grid]", wednesday, 16 * 60)) + 6);
    await expect(page.locator("[data-wg-column][data-weekday='3'] .wg-item-movable")).toHaveAttribute("data-start", "960");
    await expect(page.locator("[data-wg-column][data-weekday='2'] .wg-item-movable")).toHaveCount(0);
    await expect(row.getByTestId("training-plan-row-time")).toContainText("Slot: 16:00–17:00");
    await expect(page.getByTestId("training-plan-warnings")).toContainText("is not available at this time");
    await expect(row.getByTestId("training-plan-row-warning")).toContainText("is not available at this time");

    // Save: warnings never block.
    await page.getByTestId("training-plan-save").click();
    await expect(page.locator("#alerts")).toContainText("Training plan saved: 1 added, 0 moved, 0 removed.");
    await expect(page.getByTestId("training-plan-unsaved")).toHaveCount(0);
    await page.goto(boardURL);
    await expect(row).toHaveCount(1);
    await expect(row).toContainText("Wednesday");
    await expect(page.getByTestId("training-plan-warnings")).toContainText("is not available at this time");

    // Drag the team from the list onto Thursday 10:00, then discard.
    await expect(page.getByTestId("training-plan-team-count")).toHaveText("1 training");
    const thursday = page.locator("[data-wg-column][data-weekday='4']").first();
    await thursday.scrollIntoViewIfNeeded();
    const thursdayArea = await box(thursday);
    const pointY = (await minuteY(page, "[data-week-grid]", thursday, 10 * 60)) + 4;
    const card = page.getByTestId("training-plan-team");
    const cardArea = await box(card);
    await page.mouse.move(cardArea.x + 10, cardArea.y + 10);
    await page.mouse.down();
    await page.mouse.move(cardArea.x + 30, cardArea.y + 30, { steps: 5 });
    await page.mouse.move(thursdayArea.x + thursdayArea.width / 2, pointY, { steps: 20 });
    await page.mouse.up();
    await expect(page.getByTestId("training-plan-row")).toHaveCount(2);
    await expect(page.getByTestId("training-plan-unsaved")).toBeVisible();
    await expect(page.getByTestId("training-plan-warnings")).toContainText("No resource slot covers this training");
    await page.getByTestId("training-plan-discard").click();
    await expect(page.getByTestId("training-plan-row")).toHaveCount(1);

    // The coach sees the saved plan read only, without warnings.
    await coach.page.goto(boardURL);
    await expect(coach.page.getByTestId("training-plan-read-only")).toBeVisible();
    await expect(coach.page.getByTestId("training-plan-save")).toHaveCount(0);
    await expect(coach.page.getByTestId("training-plan-pool")).toHaveCount(0);
    await expect(coach.page.getByTestId("training-plan-warnings")).toHaveCount(0);
    await expect(coach.page.getByTestId("training-plan-row")).toContainText("Wednesday");
    const draft = await coach.page.request.post(boardURL.replace("/training-plan", "/training-plan/draft"), {
      headers: { "X-CSRF-Token": (await coach.page.locator('meta[name="csrf-token"]').getAttribute("content")) ?? "" },
      form: { suggest: "fill" },
    });
    expect(draft.status()).toBe(403);
  });
});
