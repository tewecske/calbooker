import { devices, expect, test, type Locator, type Page } from "@playwright/test";

import { appURL, ssoEnabled as enabled } from "./support";

// Week grid component, exercised on the development-only demo page
// (/en/dev/weekgrid, CALBOOKER_ENV=development). The demo keeps its items in
// memory; every test starts from a reset.
//
// Opt-in with the other browser specs: CALBOOKER_SSO_E2E=1 (make e2e) and
// calbooker running with make run. No sign-in is needed.

const demoURL = `${appURL}/en/dev/weekgrid`;
// The demo grid shows 07:00-21:00 in 15-minute rows.
const gridStart = 7 * 60;
const gridStep = 15;

function minuteOffset(box: { y: number; height: number }, rows: number, minute: number): number {
  return box.y + ((minute - gridStart) / gridStep) * (box.height / rows);
}

async function openResetDemo(page: Page, query = ""): Promise<void> {
  await page.goto(demoURL + query);
  await page.locator("form[data-wg-reset] button").click();
  await expect(page.locator("[data-week-grid]")).toBeVisible();
}

function trackCSPViolations(page: Page): string[] {
  const violations: string[] = [];
  page.on("console", (message) => {
    if (message.type() === "error" && /Content Security Policy/i.test(message.text())) {
      violations.push(message.text());
    }
  });
  return violations;
}

async function box(locator: Locator): Promise<{ x: number; y: number; width: number; height: number }> {
  const result = await locator.boundingBox();
  if (!result) {
    throw new Error("element has no bounding box");
  }
  return result;
}

async function gridRows(page: Page): Promise<number> {
  return Number(await page.locator("[data-week-grid]").getAttribute("data-rows"));
}

test.describe("week grid", () => {
  test.skip(!enabled, "set CALBOOKER_SSO_E2E=1 (make e2e) to run the browser specs");
  // The demo state is shared by everyone using this server.
  test.describe.configure({ mode: "serial" });

  test("desktop: drag an item to another day and time", async ({ page }) => {
    const violations = trackCSPViolations(page);
    await openResetDemo(page);
    const rows = await gridRows(page);
    const item = page.locator('[data-wg-item="u8"]');
    await expect(item).toHaveAttribute("data-weekday", "1");
    await expect(item).toHaveAttribute("data-start", "960");

    await item.scrollIntoViewIfNeeded();
    const from = await box(item);
    const wednesday = await box(page.locator('[data-wg-column][data-weekday="3"]'));
    const grab = 6;
    await page.mouse.move(from.x + from.width / 2, from.y + grab);
    await page.mouse.down();
    await page.mouse.move(from.x + from.width / 2 + 20, from.y + grab + 10, { steps: 5 });
    await page.mouse.move(wednesday.x + wednesday.width / 2, minuteOffset(wednesday, rows, 13 * 60) + grab, { steps: 20 });
    await page.mouse.up();

    const moved = page.locator('[data-wg-item="u8"]');
    await expect(moved).toHaveAttribute("data-weekday", "3");
    await expect(moved).toHaveAttribute("data-start", "780");
    await expect(moved).toHaveAttribute("data-end", "870");
    await expect(page.locator("#alerts")).toContainText("Item updated.");
    await expect(page.locator('[data-wg-column][data-weekday="3"] [data-wg-item="u8"]')).toContainText("13:00–14:30");

    await page.reload();
    await expect(page.locator('[data-wg-item="u8"]')).toHaveAttribute("data-weekday", "3");
    expect(violations).toEqual([]);
  });

  test("desktop: dropping outside the grid keeps the item", async ({ page }) => {
    await openResetDemo(page);
    const item = page.locator('[data-wg-item="u10"]');
    await item.scrollIntoViewIfNeeded();
    const from = await box(item);
    await page.mouse.move(from.x + from.width / 2, from.y + 4);
    await page.mouse.down();
    await page.mouse.move(from.x + 30, from.y + 20, { steps: 5 });
    await page.mouse.move(2, from.y, { steps: 10 });
    await page.mouse.up();
    await expect(page.locator('[data-wg-column][data-weekday="1"] [data-wg-item="u10"]')).toHaveAttribute("data-start", "990");
    await expect(page.locator("#alerts")).not.toContainText("Item updated.");
  });

  test("desktop: drag the bottom edge to change the end", async ({ page }) => {
    await openResetDemo(page);
    const rows = await gridRows(page);
    const item = page.locator('[data-wg-item="warn"]');
    await expect(item).toHaveAttribute("data-end", "1110");
    await item.scrollIntoViewIfNeeded();
    const column = await box(page.locator('[data-wg-column][data-weekday="4"]'));
    const handle = await box(item.locator("[data-wg-resize]"));
    await page.mouse.move(handle.x + handle.width / 2, handle.y + handle.height / 2);
    await page.mouse.down();
    await page.mouse.move(handle.x + handle.width / 2, minuteOffset(column, rows, 19 * 60 + 30), { steps: 10 });
    await page.mouse.up();
    await expect(page.locator('[data-wg-item="warn"]')).toHaveAttribute("data-end", "1170");
    await expect(page.locator('[data-wg-item="warn"]')).toHaveAttribute("data-start", "1020");
  });

  test("desktop: click an empty slot to add an item", async ({ page }) => {
    await openResetDemo(page);
    const rows = await gridRows(page);
    await page.locator('[data-wg-item="week-a"]').scrollIntoViewIfNeeded();
    const sunday = await box(page.locator('[data-wg-column][data-weekday="7"]'));
    await page.mouse.click(sunday.x + sunday.width / 2, minuteOffset(sunday, rows, 9 * 60) + 3);
    const created = page.locator('[data-wg-item="new-1"]');
    await expect(created).toHaveAttribute("data-weekday", "7");
    await expect(created).toHaveAttribute("data-start", "540");
    await expect(created).toHaveAttribute("data-end", "600");
  });

  test("keyboard form moves an item without dragging", async ({ page }) => {
    await openResetDemo(page);
    await page.getByText("Edit without dragging").click();
    const form = page.locator("form[data-wg-move-form]");
    await form.getByLabel("Item").selectOption("u12");
    await form.getByLabel("Day").selectOption("5");
    await form.getByLabel("Start").selectOption(String(8 * 60));
    await form.getByLabel("End").selectOption(String(9 * 60));
    await form.getByRole("button", { name: "Move" }).click();
    await expect(page.locator('[data-wg-item="u12"]')).toHaveAttribute("data-weekday", "5");
    await expect(page.locator('[data-wg-item="u12"]')).toHaveAttribute("data-start", "480");
  });

  test("read-only view has nothing to drag", async ({ page }) => {
    await page.goto(`${demoURL}?readonly=1`);
    await expect(page.locator('[data-wg-item="u8"]')).toBeVisible();
    await expect(page.locator("[data-wg-draggable]")).toHaveCount(0);
    await expect(page.locator("[data-week-grid]")).not.toHaveAttribute("data-move-url", /.*/);
  });

  test("week switch shows the other week's items", async ({ page }) => {
    await openResetDemo(page);
    await expect(page.locator('[data-wg-item="week-a"]')).toBeVisible();
    await page.locator("[data-wg-week-tab]", { hasText: "Week B" }).click();
    await expect(page).toHaveURL(/week=B/);
    await expect(page.locator('[data-wg-item="week-b"]')).toBeVisible();
    await expect(page.locator('[data-wg-item="week-a"]')).toHaveCount(0);
  });

  test.describe("touch", () => {
    // Pixel 7 metrics without its defaultBrowserType, which would need a
    // separate worker.
    const { defaultBrowserType: _browser, ...phone } = devices["Pixel 7"];
    test.use(phone);

    test("touch: hold and drag an item to another day and time", async ({ page }) => {
      const violations = trackCSPViolations(page);
      await openResetDemo(page);
      const rows = await gridRows(page);

      // The grid scrolls sideways inside its container, not the page.
      const scroller = page.locator(".wg-scroll");
      expect(await scroller.evaluate((element) => element.scrollWidth > element.clientWidth)).toBe(true);
      expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);

      const item = page.locator('[data-wg-item="u8"]');
      await item.scrollIntoViewIfNeeded();
      const from = await box(item);
      const tuesday = await box(page.locator('[data-wg-column][data-weekday="2"]'));
      const grab = 6;
      const start = { x: from.x + from.width / 2, y: from.y + grab };
      const end = { x: tuesday.x + tuesday.width / 2, y: minuteOffset(tuesday, rows, 15 * 60) + grab };

      const client = await page.context().newCDPSession(page);
      await client.send("Input.dispatchTouchEvent", { type: "touchStart", touchPoints: [start] });
      // Sortable starts a touch drag after a short hold (delayOnTouchOnly).
      await page.waitForTimeout(400);
      const steps = 20;
      for (let step = 1; step <= steps; step++) {
        const point = {
          x: start.x + ((end.x - start.x) * step) / steps,
          y: start.y + ((end.y - start.y) * step) / steps,
        };
        await client.send("Input.dispatchTouchEvent", { type: "touchMove", touchPoints: [point] });
      }
      await client.send("Input.dispatchTouchEvent", { type: "touchEnd", touchPoints: [] });

      const moved = page.locator('[data-wg-item="u8"]');
      await expect(moved).toHaveAttribute("data-weekday", "2");
      await expect(moved).toHaveAttribute("data-start", "900");
      await expect(moved).toHaveAttribute("data-end", "990");
      expect(violations).toEqual([]);
    });
  });
});
