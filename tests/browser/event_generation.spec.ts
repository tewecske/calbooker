import { expect, test } from "@playwright/test";

import { appURL, createInvite, joinWithCode, poolUser, ssoEnabled, uniqueName } from "./support";

// Event generation (#15): staff turn the saved training plan into real
// events for next week, edit one by hand, regenerate, and the edited one is
// kept and listed. Opt-in like the other single sign-on specs:
// CALBOOKER_SSO_E2E=1 (make e2e) with goweb's Hydra and provider running and
// calbooker started with make run. Uses the pooled "owner" and "coach"
// accounts.

/** The calendar date `days` from now as YYYY-MM-DD (UTC; the margins are wide). */
function dateFromToday(days: number): string {
  return new Date(Date.now() + days * 24 * 60 * 60 * 1000).toISOString().slice(0, 10);
}

test.describe("event generation", () => {
  test.skip(!ssoEnabled, "set CALBOOKER_SSO_E2E=1 (make e2e) to run the event generation specs");
  test.describe.configure({ mode: "serial" });

  test("generate next week, edit one event, regenerate, and see the edited one kept", async ({ browser }) => {
    test.setTimeout(180_000);
    const owner = await poolUser(browser, "owner");
    const page = owner.page;
    const lions = uniqueName("Lions");
    const tigers = uniqueName("Tigers");
    const field = uniqueName("Field");

    await page.goto(`${appURL}/en/groups`);
    await page.locator("#group-new-name").fill(uniqueName("Club"));
    await page.locator("#group-new-name").locator("xpath=ancestor::form").getByRole("button").click();
    await page.waitForURL(/\/en\/groups\/(?!join$)[a-z0-9-]+$/);
    const groupURL = page.url();

    for (const name of [lions, tigers]) {
      await page.goto(`${groupURL}/teams`);
      await page.locator("#team-new-name").fill(name);
      await page.locator("#team-new-birth-year-from").fill("2015");
      await page.locator("#team-new-birth-year-to").fill("2016");
      await page.getByRole("button", { name: "Create a team" }).click();
      await page.waitForURL(/\/en\/groups\/[a-z0-9-]+\/teams\/[a-z0-9-]+$/);
    }
    await page.goto(`${groupURL}/resources`);
    await page.locator("#resource-new-name").fill(field);
    await page.getByRole("button", { name: "Add a resource" }).click();
    await expect(page.locator("#alerts")).toContainText("Resource added.");
    await page.goto(`${groupURL}/seasons`);
    await page.locator("#season-new-name").fill(uniqueName("Season"));
    await page.locator("#season-new-starts-on").fill(dateFromToday(-30));
    await page.locator("#season-new-ends-on").fill(dateFromToday(300));
    await page.locator("#season-new-part1-name").fill("Whole");
    await page.locator("#season-new-part1-starts-on").fill(dateFromToday(-30));
    await page.locator("#season-new-part1-ends-on").fill(dateFromToday(300));
    await page.getByRole("button", { name: "Add a season" }).click();
    await page.waitForURL(/\/en\/groups\/[a-z0-9-]+\/seasons\/\d+$/);

    // The plan: Lions on Tuesday, Tigers on Thursday, both at 16:00-17:30.
    await page.goto(`${groupURL}/training-plan`);
    for (const [team, weekday] of [
      [lions, "Tuesday"],
      [tigers, "Thursday"],
    ]) {
      await page.getByTestId("training-plan-add-team").selectOption({ label: team });
      await page.getByTestId("training-plan-add-day").selectOption({ label: weekday });
      await page.getByTestId("training-plan-add-submit").click();
      await expect(page.getByTestId("training-plan-row").filter({ hasText: team })).toHaveCount(1);
    }
    await page.getByTestId("training-plan-save").click();
    await expect(page.locator("#alerts")).toContainText("Training plan saved: 2 added");

    // Generate next week: the preview lists both, and nothing exists yet.
    await page.getByTestId("training-plan-generate-link").click();
    await page.waitForURL(/\/event-generation/);
    await expect(page.getByTestId("event-generation-form")).toBeVisible();
    await expect(page.getByTestId("event-generation-result")).toHaveCount(0);
    await page.getByTestId("event-generation-preview").click();
    await expect(page.getByTestId("event-generation-count-create")).toHaveText("2");
    const items = page.getByTestId("event-generation-item");
    await expect(items).toHaveCount(2);
    await expect(items.filter({ hasText: lions })).toContainText("Tuesday");
    await expect(items.filter({ hasText: lions })).toContainText("16:00–17:30");
    await expect(items.filter({ hasText: tigers })).toContainText("Thursday");
    const previewURL = page.url();
    await page.getByTestId("event-generation-confirm").click();
    await expect(page.locator("#alerts")).toContainText("Events generated: 2 created, 0 rebuilt, 0 removed, 0 kept.");
    await expect(page.getByTestId("event-generation-result")).toHaveAttribute("data-done", "true");

    // Generating again creates nothing new.
    await page.goto(previewURL);
    await expect(page.getByTestId("event-generation-count-create")).toHaveText("0");
    await expect(page.getByTestId("event-generation-count-unchanged")).toHaveText("2");
    await expect(page.getByTestId("event-generation-confirm")).toHaveCount(0);
    await expect(page.getByTestId("event-generation-nothing")).toBeVisible();

    // The generated events are listed; edit the Lions one by hand.
    await page.goto(`${groupURL}/events`);
    const rows = page.getByTestId("events").locator("tr[data-event-id]");
    await expect(rows.filter({ hasText: lions })).toHaveCount(1);
    await expect(rows.filter({ hasText: tigers })).toHaveCount(1);
    await rows.filter({ hasText: lions }).getByRole("link").first().click();
    await page.waitForURL(/\/en\/groups\/[a-z0-9-]+\/events\/\d+$/);
    await expect(page.getByTestId("event-signup")).toHaveText("Players sign up");
    const edited = `${lions} (moved)`;
    await page.locator("#event-edit-title").fill(edited);
    await page.getByRole("button", { name: "Save event" }).click();
    await expect(page.locator("#alerts")).toContainText("Event saved.");

    // Regenerate: the untouched event is unchanged, the edited one is kept
    // and listed with a link to fix it by hand.
    await page.goto(previewURL);
    await expect(page.getByTestId("event-generation-count-unchanged")).toHaveText("1");
    await expect(page.getByTestId("event-generation-count-keep")).toHaveText("1");
    await expect(page.getByTestId("event-generation-count-create")).toHaveText("0");
    const kept = page.getByTestId("event-generation-kept-item");
    await expect(kept).toHaveCount(1);
    await expect(kept).toContainText(lions);
    await expect(kept.getByRole("link")).toHaveAttribute("href", /\/en\/groups\/[a-z0-9-]+\/events\/\d+$/);
    await expect(kept).toContainText("edited or cancelled by hand");
    await expect(page.getByTestId("event-generation-confirm")).toHaveCount(0);

    // Coaches may not generate.
    const coach = await poolUser(browser, "coach");
    await joinWithCode(coach.page, appURL, await createInvite(page, groupURL, { role: "coach" }));
    await coach.page.waitForURL(groupURL);
    expect((await coach.page.goto(previewURL))?.status()).toBe(403);
    await coach.page.goto(groupURL);
    const refused = await coach.page.request.post(previewURL.split("?")[0], {
      headers: { "X-CSRF-Token": (await coach.page.locator('meta[name="csrf-token"]').getAttribute("content")) ?? "" },
      form: { part: "1", week: dateFromToday(0) },
    });
    expect(refused.status()).toBe(403);

    await coach.context.close();
    await owner.context.close();
  });
});
