import { expect, test } from "@playwright/test";

import { appURL, becomeGuest, createInvite, poolUser, ssoEnabled, uniqueName } from "./support";

// Clubs with roles, a time zone, and teams (#2). Opt-in like the single
// sign-on specs: CALBOOKER_SSO_E2E=1 (make e2e) with goweb's Hydra and
// provider running and calbooker started with make run.
test.describe("groups", () => {
  test.skip(!ssoEnabled, "set CALBOOKER_SSO_E2E=1 (make e2e) to run the group specs");
  test.describe.configure({ mode: "serial" });

  test("an admin sets up a club, a team, and a member with two roles; outsiders get 404", async ({ browser }) => {
    const owner = await poolUser(browser, "owner");
    const page = owner.page;
    const clubName = uniqueName("Club");

    // Create the club in another time zone than the default.
    await page.goto(`${appURL}/en/groups`);
    await page.locator("#group-new-name").fill(clubName);
    await page.locator("#group-new-time-zone").selectOption("Europe/Vienna");
    await page.locator("#group-new-name").locator("xpath=ancestor::form").getByRole("button").click();
    await page.waitForURL(/\/en\/groups\/\d+$/);
    const groupURL = page.url();
    await expect(page.locator("#group-summary-heading")).toHaveText(clubName);
    await expect(page.getByTestId("group-time-zone")).toHaveText("Europe/Vienna");
    await expect(page.getByTestId("group-viewer-roles")).toContainText("Administrator");

    // Settings: set the time zone.
    await page.locator("nav a[data-section=settings]").click();
    await page.locator("#group-settings-time-zone").selectOption("Europe/Budapest");
    await page.getByRole("button", { name: "Save settings" }).click();
    await expect(page.locator("#alerts")).toContainText("Group settings saved.");
    await page.goto(groupURL);
    await expect(page.getByTestId("group-time-zone")).toHaveText("Europe/Budapest");

    // Create a team.
    await page.locator("nav a[data-section=teams]").click();
    await page.locator("#team-new-name").fill("Lions");
    await page.locator("#team-new-birth-year-from").fill("2015");
    await page.locator("#team-new-birth-year-to").fill("2016");
    await page.locator("#team-new-description").fill("U10");
    await page.getByRole("button", { name: "Create a team" }).click();
    await page.waitForURL(/\/en\/groups\/\d+\/teams\/\d+$/);
    await expect(page.locator("#team-heading")).toContainText("Lions");
    await expect(page.getByTestId("team-birth-years")).toHaveText("2015–2016");
    const teamURL = page.url();

    // A group invite without a team makes the new member a player.
    const inviteCode = await createInvite(page, groupURL, { role: "player" });

    // A guest joins with the invite code and becomes a player.
    const guest = await becomeGuest(browser);
    await guest.page.goto(`${appURL}/en/groups/join`);
    await guest.page.locator("#group-invite-code").fill(inviteCode);
    await guest.page.locator("#group-invite-code").locator("xpath=ancestor::form").getByRole("button").click();
    await guest.page.waitForURL(groupURL);
    await expect(guest.page.getByTestId("group-viewer-roles")).toHaveText(/Player/);
    // Guests cannot create clubs and see why.
    await guest.page.goto(`${appURL}/en/groups`);
    await expect(guest.page.getByTestId("group-create-requires-account")).toBeVisible();
    await expect(guest.page.locator("#group-new-name")).toHaveCount(0);
    // A player has no settings.
    const settings = await guest.page.goto(`${groupURL}/settings`);
    expect(settings?.status()).toBe(403);

    // The admin adds the parent role: the guest now holds two roles. Staff
    // roles are not offered to a guest.
    await page.goto(`${groupURL}/members`);
    const guestRow = page.locator("tr[data-member-id]").filter({ hasText: "Guest" });
    await expect(guestRow.locator("select[name=role] option[value=coach]")).toHaveCount(0);
    await guestRow.locator("select[name=role]").selectOption("parent");
    await guestRow.getByRole("button", { name: "Add role" }).click();
    await expect(page.locator("#alerts")).toContainText("Role added.");
    const roles = page.locator("tr[data-member-id]").filter({ hasText: "Guest" }).getByTestId("member-roles");
    await expect(roles.locator("[data-role=player]")).toBeVisible();
    await expect(roles.locator("[data-role=parent]")).toBeVisible();

    // Put the member on the team as a player.
    await page.goto(teamURL);
    const option = page.locator("#team-roster-entry option", { hasText: "(Player)" }).first();
    await page.locator("#team-roster-entry").selectOption({ value: (await option.getAttribute("value")) ?? "" });
    await page.getByRole("button", { name: "Add to team" }).click();
    await expect(page.locator("#alerts")).toContainText("Added to the team.");
    await expect(page.getByTestId("team-players").locator("[data-roster-role=player]")).toHaveCount(1);

    // The player now sees the team, but not its edit form.
    await guest.page.goto(teamURL);
    await expect(guest.page.locator("#team-heading")).toContainText("Lions");
    await expect(guest.page.locator("#team-edit-name")).toHaveCount(0);

    // An outsider (another full user) gets 404 on every group page.
    const outsider = await poolUser(browser, "outsider");
    for (const url of [groupURL, `${groupURL}/members`, `${groupURL}/teams`, teamURL, `${groupURL}/settings`]) {
      const response = await outsider.page.goto(url);
      expect(response?.status(), url).toBe(404);
      await expect(outsider.page.locator("body")).not.toContainText(clubName);
    }

    await outsider.context.close();
    await guest.context.close();
    await owner.context.close();
  });
});
