import { expect, test, type Locator, type Page } from "@playwright/test";

import { appURL, createInvite, joinWithCode, poolUser, ssoEnabled, uniqueName } from "./support";

// Coach qualifications and availability (#7): staff set a coach's age
// range; the coach marks their weekly availability on the week grid by
// clicking and dragging; staff see every coach's availability side by side.
// Opt-in like the other single sign-on specs: CALBOOKER_SSO_E2E=1 (make e2e)
// with goweb's Hydra and provider running and calbooker started with make
// run. Uses the pooled "owner" and "coach" accounts.

// The availability grid shows 06:00-22:00 in 15-minute rows.
const gridStart = 6 * 60;
const gridStep = 15;

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

function minuteOffset(column: { y: number; height: number }, rows: number, minute: number): number {
  return column.y + ((minute - gridStart) / gridStep) * (column.height / rows);
}

function column(page: Page, weekday: number): Locator {
  return page.locator(`#availability-grid [data-wg-column][data-weekday="${weekday}"]`);
}

test.describe("coaches", () => {
  test.skip(!ssoEnabled, "set CALBOOKER_SSO_E2E=1 (make e2e) to run the coach specs");
  test.describe.configure({ mode: "serial" });

  test("a coach marks availability on the grid and an admin sees it side by side", async ({ browser }) => {
    test.setTimeout(150_000);
    const owner = await poolUser(browser, "owner");
    const page = owner.page;
    const clubName = uniqueName("Club");
    const teamName = uniqueName("Tigers");
    const seasonName = uniqueName("Season");
    const thisYear = Number(dateFromToday(-30).slice(0, 4));

    await page.goto(`${appURL}/en/groups`);
    await page.locator("#group-new-name").fill(clubName);
    await page.locator("#group-new-name").locator("xpath=ancestor::form").getByRole("button").click();
    await page.waitForURL(/\/en\/groups\/\d+$/);
    const groupURL = page.url();

    // A team born ten and eleven years before the season starts.
    await page.locator("nav a[data-section=teams]").click();
    await page.locator("#team-new-name").fill(teamName);
    await page.locator("#team-new-birth-year-from").fill(String(thisYear - 11));
    await page.locator("#team-new-birth-year-to").fill(String(thisYear - 10));
    await page.getByRole("button", { name: "Create a team" }).click();
    await page.waitForURL(/\/en\/groups\/\d+\/teams\/\d+$/);

    // A season around today: part 1 every week, part 2 with A/B weeks.
    await page.goto(`${groupURL}/seasons`);
    await page.locator("#season-new-name").fill(seasonName);
    await page.locator("#season-new-starts-on").fill(dateFromToday(-30));
    await page.locator("#season-new-ends-on").fill(dateFromToday(300));
    await page.locator("#season-new-part1-name").fill("Autumn");
    await page.locator("#season-new-part1-starts-on").fill(dateFromToday(-30));
    await page.locator("#season-new-part1-ends-on").fill(dateFromToday(60));
    await page.locator("#season-new-part2-name").fill("Spring");
    await page.locator("#season-new-part2-starts-on").fill(dateFromToday(61));
    await page.locator("#season-new-part2-ends-on").fill(dateFromToday(300));
    await page.locator("#season-new-part2-alternating").check();
    await page.getByRole("button", { name: "Add a season" }).click();
    await page.waitForURL(/\/en\/groups\/\d+\/seasons\/\d+$/);

    // The coach joins.
    const coach = await poolUser(browser, "coach");
    const code = await createInvite(page, groupURL, { role: "coach" });
    await joinWithCode(coach.page, appURL, code);
    await coach.page.waitForURL(groupURL);

    // The admin sets the coach's qualification; the team's age shows with
    // the qualified coach.
    await page.goto(`${groupURL}/coaches`);
    const row = page.getByTestId("coaches").locator("tr[data-coach-id]");
    await expect(row).toHaveCount(1);
    await row.getByLabel("From age").fill("12");
    await row.getByLabel("To age").fill("9");
    await row.getByRole("button", { name: /Save qualification/ }).click();
    await expect(page.locator("#alerts")).toContainText("Enter whole ages from 0 to 99");
    await row.getByLabel("From age").fill("9");
    await row.getByLabel("To age").fill("12");
    await row.getByRole("button", { name: /Save qualification/ }).click();
    await expect(page.locator("#alerts")).toContainText("Qualification saved.");
    await expect(row.getByTestId("coach-qualification")).toHaveText("9–12 years");
    const team = page.getByTestId("coaches-team-ages").locator("tr", { hasText: teamName });
    await expect(team.getByTestId("team-age")).toHaveText("10–11 years");
    await expect(team.getByTestId("team-qualified-coaches")).not.toContainText("None");

    // The coach sees their qualification read only and opens their grid.
    await coach.page.goto(`${groupURL}/coaches`);
    await expect(coach.page.getByTestId("coach-qualification")).toHaveText("9–12 years");
    await expect(coach.page.getByLabel("From age")).toHaveCount(0);
    await coach.page.getByRole("link", { name: "Edit my availability" }).click();
    await coach.page.waitForURL(/\/coaches\/\d+\/availability/);
    const coachURL = coach.page.url().split("?")[0];
    await expect(coach.page.locator("#availability-part option:checked")).toHaveText(`${seasonName} · Autumn`);
    const grid = coach.page.locator("#availability-grid");
    const rows = Number(await grid.getAttribute("data-rows"));

    // Clicking an empty slot on Tuesday at 16:00 adds two hours.
    await column(coach.page, 2).scrollIntoViewIfNeeded();
    const tuesday = await box(column(coach.page, 2));
    await coach.page.mouse.click(tuesday.x + tuesday.width / 2, minuteOffset(tuesday, rows, 16 * 60) + 3);
    await expect(coach.page.locator("#alerts")).toContainText("Availability added.");
    const added = column(coach.page, 2).locator("[data-wg-item]");
    await expect(added).toHaveAttribute("data-start", "960");
    await expect(added).toHaveAttribute("data-end", "1080");

    // Dragging it to Thursday at 17:00 moves it.
    // Center it so the drag needs no scrolling.
    await added.evaluate((element) => element.scrollIntoView({ block: "center" }));
    const from = await box(added);
    const thursday = await box(column(coach.page, 4));
    const grab = 6;
    await coach.page.mouse.move(from.x + from.width / 2, from.y + grab);
    await coach.page.mouse.down();
    await coach.page.mouse.move(from.x + from.width / 2 + 20, from.y + grab + 10, { steps: 5 });
    await coach.page.mouse.move(thursday.x + thursday.width / 2, minuteOffset(thursday, rows, 17 * 60) + grab, { steps: 20 });
    await coach.page.mouse.up();
    await expect(coach.page.locator("#alerts")).toContainText("Availability updated.");
    const moved = column(coach.page, 4).locator("[data-wg-item]");
    await expect(moved).toHaveAttribute("data-start", "1020");
    await expect(moved).toHaveAttribute("data-end", "1140");
    await expect(column(coach.page, 2).locator("[data-wg-item]")).toHaveCount(0);

    // A second window touching the first merges with it.
    // The grid ignores clicks right after a drag (static/app.js).
    await coach.page.waitForTimeout(500);
    const thursdayNow = await box(column(coach.page, 4));
    await coach.page.mouse.click(thursdayNow.x + thursdayNow.width / 2, minuteOffset(thursdayNow, rows, 19 * 60) + 3);
    await expect(coach.page.locator("#alerts")).toContainText("Availability added.");
    await expect(column(coach.page, 4).locator("[data-wg-item]")).toHaveCount(1);
    await expect(column(coach.page, 4).locator("[data-wg-item]")).toHaveAttribute("data-end", String(21 * 60));
    await coach.page.reload();
    await expect(coach.page.getByTestId("availability-windows").locator("tr[data-window-id]")).toHaveCount(1);
    await expect(coach.page.getByTestId("availability-no-previous")).toBeVisible();

    // Coaches do not see the side-by-side view.
    expect((await coach.page.goto(`${groupURL}/coaches/availability`))?.status()).toBe(403);

    // In part 2 the coach copies part 1's windows (after confirming).
    await coach.page.goto(coachURL);
    await coach.page.locator("#availability-part").selectOption({ label: `${seasonName} · Spring` });
    await coach.page.getByRole("button", { name: "Show" }).click();
    await expect(coach.page.getByTestId("availability-weeks")).toBeVisible();
    coach.page.once("dialog", (dialog) => void dialog.accept());
    await coach.page.getByTestId("availability-copy").click();
    await expect(coach.page.locator("#alerts")).toContainText("Availability copied");
    await expect(coach.page.getByTestId("availability-windows").locator("tr[data-window-id]")).toHaveCount(1);
    await expect(coach.page.getByTestId("availability-windows")).toContainText("Every week");

    // The admin sees the coach's window in the side-by-side view.
    await page.goto(`${groupURL}/coaches`);
    await page.getByTestId("coaches-overview-link").click();
    await page.waitForURL(/\/coaches\/availability/);
    const overview = page.locator("#availability-overview-grid");
    await expect(overview.locator('[data-wg-column][data-weekday="4"] [data-wg-item]')).toHaveAttribute("data-start", "1020");
    await expect(overview.locator("[data-wg-draggable]")).toHaveCount(0);
    await expect(page.getByTestId("availability-legend").locator("li")).toHaveCount(1);

    await coach.context.close();
    await owner.context.close();
  });
});
