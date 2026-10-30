import { expect, test } from "@playwright/test";

import { appURL, createInvite, joinWithCode, poolUser, ssoEnabled, uniqueName } from "./support";

// Event kinds and resource kinds per group (#43): a manager adds an event kind
// and uses it on a new event; archived kinds leave the picker and a kind in use
// cannot be deleted. Opt-in like the other single sign-on specs:
// CALBOOKER_SSO_E2E=1 (make e2e) with goweb's Hydra and provider running and
// calbooker started with make run. The owner and manager come from the pool.
test.describe("kinds", () => {
  test.skip(!ssoEnabled, "set CALBOOKER_SSO_E2E=1 (make e2e) to run the kind specs");
  test.describe.configure({ mode: "serial" });

  test("a manager adds an event kind and uses it on a new event", async ({ browser }) => {
    test.setTimeout(120_000);
    const owner = await poolUser(browser, "owner");
    const page = owner.page;
    const clubName = uniqueName("Club");
    const teamName = uniqueName("Lions");
    const kindName = uniqueName("Camp");
    const title = uniqueName("Summer");

    await page.goto(`${appURL}/en/groups`);
    await page.locator("#group-new-name").fill(clubName);
    await page.locator("#group-new-name").locator("xpath=ancestor::form").getByRole("button").click();
    await page.waitForURL(/\/en\/groups\/(?!join$)[a-z0-9-]+$/);
    const groupURL = page.url();

    await page.goto(`${groupURL}/teams`);
    await page.locator("#team-new-name").fill(teamName);
    await page.locator("#team-new-birth-year-from").fill("2015");
    await page.locator("#team-new-birth-year-to").fill("2016");
    await page.getByRole("button", { name: "Create a team" }).click();
    await page.waitForURL(/\/en\/groups\/[a-z0-9-]+\/teams\/[a-z0-9-]+$/);

    // The manager joins.
    const manager = await poolUser(browser, "member");
    const code = await createInvite(page, groupURL, { role: "manager" });
    await joinWithCode(manager.page, appURL, code);
    await manager.page.waitForURL(groupURL);

    // The manager finds the kinds page through Settings and adds a kind.
    const mp = manager.page;
    await mp.locator("nav a[data-section=settings]").click();
    await mp.getByTestId("settings-kinds-link").click();
    await expect(mp.locator("#kinds-events-heading")).toBeVisible();
    const events = mp.getByTestId("kinds-events");
    await expect(events.locator("tr[data-kind-code=training]")).toContainText("Required");
    await expect(events.locator("tr[data-kind-code=training]").getByRole("button", { name: "Archive" })).toHaveCount(0);
    await mp.locator("#kind-new-events-en").fill(kindName);
    await mp.locator("#kind-new-events-hu").fill("Tábor");
    await events.getByRole("button", { name: "Add kind" }).click();
    await expect(mp.locator("#alerts")).toContainText("Kind added.");
    await expect(events.locator("tr[data-kind-id]").filter({ hasText: kindName })).toHaveCount(1);

    // The kind is offered on the event form and shown on the event.
    await mp.goto(`${groupURL}/events`);
    await mp.locator("#event-new-kind").selectOption({ label: kindName });
    await mp.locator("#event-new-title").fill(title);
    await mp.locator("#event-new-starts-at").fill("2027-03-06T09:00");
    await mp.locator("#event-new-ends-at").fill("2027-03-06T11:00");
    await mp.locator("select[name=team_id]").selectOption({ label: teamName }, { force: true });
    await mp.locator("#event-new-location").fill("Lake");
    await mp.getByRole("button", { name: "Add an event" }).click();
    await mp.waitForURL(/\/en\/groups\/[a-z0-9-]+\/events\/\d+$/);
    await expect(mp.locator("#event-heading")).toContainText(kindName);

    // In use: no delete button, and a kind that is archived leaves the picker.
    await mp.goto(`${groupURL}/kinds`);
    const row = mp.getByTestId("kinds-events").locator("tr[data-kind-id]").filter({ has: mp.locator(`input[value="${kindName}"]`) });
    await expect(row.getByTestId("kind-uses")).toHaveText("1");
    await expect(row.getByRole("button", { name: "Delete" })).toHaveCount(0);
    await row.getByRole("button", { name: "Archive" }).click();
    await expect(mp.locator("#alerts")).toContainText("Kind archived.");
    await mp.goto(`${groupURL}/events`);
    await expect(mp.locator("#event-new-kind option", { hasText: kindName })).toHaveCount(0);

    await manager.context.close();
    await owner.context.close();
  });
});
