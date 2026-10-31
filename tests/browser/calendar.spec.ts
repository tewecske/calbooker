import { expect, test, type Page } from "@playwright/test";

import { appURL, becomeGuest, createInvite, joinWithCode, poolUser, ssoEnabled, uniqueName } from "./support";

// Weekly calendar (#10): every member sees the visible events of one week in
// the group's time zone, can move between weeks, filter by team, resource,
// and kind, open an event, and switch groups keeping the week. Opt-in like
// the other single sign-on specs: CALBOOKER_SSO_E2E=1 (make e2e) with goweb's
// Hydra and provider running and calbooker started with make run. The owner
// comes from the account pool; the player is a guest.

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

async function createEvent(page: Page, groupURL: string, event: { kind: string; title: string; team: string; day: string; from: string; to: string }): Promise<void> {
  await page.goto(`${groupURL}/events`);
  await page.locator("#event-new-kind").selectOption({ label: event.kind });
  await page.locator("#event-new-title").fill(event.title);
  await page.locator("#event-new-starts-at").fill(`${event.day}T${event.from}`);
  await page.locator("#event-new-ends-at").fill(`${event.day}T${event.to}`);
  const teams = page.getByRole("combobox", { name: "Teams" });
  await teams.fill(event.team);
  await teams.press("Enter");
  await page.locator("#event-new-location").fill("Club ground");
  await page.getByRole("button", { name: "Add an event" }).click();
  await page.waitForURL(/\/en\/groups\/[a-z0-9-]+\/events\/\d+$/);
}

test.describe("calendar", () => {
  test.skip(!ssoEnabled, "set CALBOOKER_SSO_E2E=1 (make e2e) to run the calendar specs");
  test.describe.configure({ mode: "serial" });

  test("members switch weeks, filter by team, open an event, and switch groups", async ({ browser }) => {
    test.setTimeout(120_000);
    const owner = await poolUser(browser, "owner");
    const page = owner.page;
    const clubName = uniqueName("Club");
    const otherClub = uniqueName("Other club");
    const lions = uniqueName("Lions");
    const tigers = uniqueName("Tigers");
    const training = uniqueName("Training");
    const match = uniqueName("Match");
    const nextWeekDay = dateFromToday(7);

    const otherURL = await createGroup(page, otherClub);
    const groupURL = await createGroup(page, clubName);
    for (const [name, year] of [[lions, "2015"], [tigers, "2017"]]) {
      await page.goto(`${groupURL}/teams`);
      await page.locator("#team-new-name").fill(name);
      await page.locator("#team-new-birth-year-from").fill(year);
      await page.locator("#team-new-birth-year-to").fill(year);
      await page.getByRole("button", { name: "Create a team" }).click();
      await page.waitForURL(/\/en\/groups\/[a-z0-9-]+\/teams\/[a-z0-9-]+$/);
    }
    await createEvent(page, groupURL, { kind: "Training", title: training, team: lions, day: nextWeekDay, from: "11:00", to: "12:30" });
    await createEvent(page, groupURL, { kind: "Match", title: match, team: tigers, day: nextWeekDay, from: "13:00", to: "14:30" });

    // This week is empty; the next week shows both events on the grid.
    await page.goto(groupURL);
    await page.locator("nav a[data-section=calendar]").click();
    await page.waitForURL(/\/calendar$/);
    const grid = page.locator("#calendar-grid");
    await expect(page.locator("#calendar-heading")).toHaveText("Weekly calendar");
    await expect(grid.getByRole("link", { name: training })).toHaveCount(0);
    const thisWeek = await page.locator("[data-calendar]").getAttribute("data-week");
    await grid.locator("[data-wg-next]").click();
    await page.waitForURL(/\/calendar\?week=\d{4}-\d{2}-\d{2}$/);
    await expect(page.locator("[data-calendar]")).not.toHaveAttribute("data-week", thisWeek ?? "");
    await expect(grid.getByRole("link", { name: training })).toBeVisible();
    await expect(grid.getByRole("link", { name: match })).toBeVisible();
    const nextWeek = await page.locator("[data-calendar]").getAttribute("data-week");

    // Filter by team: only the Lions training stays, and the week is kept.
    await page.locator("#calendar-filter-team").selectOption({ label: lions });
    await page.waitForURL(/team=\d+/);
    expect(page.url()).toContain(`week=${nextWeek}`);
    await expect(grid.getByRole("link", { name: match })).toHaveCount(0);
    await expect(grid.getByRole("link", { name: training })).toBeVisible();
    await page.getByTestId("calendar-clear").click();
    await expect(grid.getByRole("link", { name: match })).toBeVisible();

    // Back to this week with Today.
    await page.getByTestId("calendar-today").click();
    await expect(page.locator("[data-calendar]")).toHaveAttribute("data-week", thisWeek ?? "");
    await page.goBack();
    await expect(page.locator("[data-calendar]")).toHaveAttribute("data-week", nextWeek ?? "");

    // Narrow screens get a list of days with the same events.
    await page.setViewportSize({ width: 390, height: 844 });
    await expect(grid).toBeHidden();
    const dayList = page.getByTestId("calendar-day-list");
    await expect(dayList.getByRole("link", { name: match })).toBeVisible();
    await page.getByTestId("calendar-next").click();
    await page.waitForURL(/week=/);
    await expect(dayList.getByRole("link", { name: match })).toHaveCount(0);
    await page.getByTestId("calendar-previous").click();
    await expect(dayList.getByRole("link", { name: match })).toBeVisible();

    // The agenda is the phone default (#57); the toggle switches to the grid,
    // and the choice stays in the URL through navigation.
    await page.getByTestId("calendar-view-grid").click();
    await page.waitForURL(/view=grid/);
    await expect(grid).toBeVisible();
    await expect(dayList).toBeHidden();
    await page.getByTestId("calendar-today").click();
    await page.waitForURL(/view=grid/);
    await expect(grid).toBeVisible();
    await page.getByTestId("calendar-view-agenda").click();
    await page.waitForURL(/view=agenda/);
    await expect(dayList).toBeVisible();
    await expect(grid).toBeHidden();

    // On a wide screen the grid is the default and the agenda is selectable.
    await page.setViewportSize({ width: 1280, height: 800 });
    await page.goto(`${groupURL}/calendar`);
    await expect(grid).toBeVisible();
    await expect(dayList).toBeHidden();
    await page.getByTestId("calendar-view-agenda").click();
    await page.waitForURL(/view=agenda/);
    await expect(dayList).toBeVisible();
    await expect(grid).toBeHidden();
    await page.goto(`${groupURL}/calendar?week=${nextWeek}`);

    // Open an event from the grid.
    await grid.getByRole("link", { name: training }).click();
    await page.waitForURL(/\/events\/\d+$/);
    await expect(page.locator("#event-heading")).toContainText(training);

    // Switch to the other group in the same week.
    await page.goto(`${groupURL}/calendar?week=${nextWeek}`);
    await page.getByTestId("calendar-groups").locator("summary").click();
    await page.getByTestId("calendar-groups").getByRole("link", { name: otherClub }).click();
    await page.waitForURL(new RegExp(`${otherURL.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")}/calendar\\?week=${nextWeek}$`));
    await expect(page.locator("[data-calendar]")).toHaveAttribute("data-week", nextWeek ?? "");
    await expect(page.getByTestId("calendar-empty")).toBeVisible();

    // A Tigers player sees only the Tigers match.
    const playerCode = await createInvite(page, groupURL, { role: "player", team: tigers });
    const player = await becomeGuest(browser);
    await joinWithCode(player.page, appURL, playerCode);
    await player.page.waitForURL(/\/en\/groups\/[a-z0-9-]+\/teams\/[a-z0-9-]+$/);
    await player.page.goto(`${groupURL}/calendar?week=${nextWeek}`);
    await expect(player.page.locator("#calendar-grid").getByRole("link", { name: match })).toBeVisible();
    await expect(player.page.getByText(training)).toHaveCount(0);
    await expect(player.page.locator("#calendar-filter-team option")).toHaveText(["All teams", tigers]);

    // No answer, no badge; after signing up the player sees "Coming" on the
    // match, in the grid and in the day list, and nobody else's answer.
    await expect(player.page.locator("[data-calendar-status]")).toHaveCount(0);
    await player.page.locator("#calendar-grid").getByRole("link", { name: match }).click();
    await player.page.waitForURL(/\/events\/\d+$/);
    await player.page.getByTestId("participation").getByRole("button", { name: "I'll be there" }).click();
    await expect(player.page.locator("#alerts")).toContainText("Answer saved.");
    await player.page.goto(`${groupURL}/calendar?week=${nextWeek}`);
    await expect(player.page.locator("#calendar-grid").getByText("Coming")).toBeVisible();
    await expect(player.page.locator("[data-calendar-status=signed_up]")).toHaveCount(1);
    await owner.page.goto(`${groupURL}/calendar?week=${nextWeek}`);
    await expect(owner.page.locator("[data-calendar-status]")).toHaveCount(0);

    await player.context.close();
    await owner.context.close();
  });
});
