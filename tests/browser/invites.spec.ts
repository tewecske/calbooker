import { expect, test } from "@playwright/test";

import { appURL, becomeGuest, createInvite, joinWithCode, signUpUser, ssoEnabled, uniqueName } from "./support";

// Role-specific invites, child profiles, and guardians (#3). Opt-in like the
// other single sign-on specs: CALBOOKER_SSO_E2E=1 (make e2e) with goweb's
// Hydra and provider running and calbooker started with make run.
test.describe("invites and children", () => {
  test.skip(!ssoEnabled, "set CALBOOKER_SSO_E2E=1 (make e2e) to run the invite specs");
  test.describe.configure({ mode: "serial" });

  test("an admin invites a player to a team, a guest joins it, and a parent adds a child", async ({ browser }) => {
    const owner = await signUpUser(browser, "calbooker-invites");
    const page = owner.page;
    const clubName = uniqueName("Club");
    const teamName = uniqueName("Lions");

    await page.goto(`${appURL}/en/groups`);
    await page.locator("#group-new-name").fill(clubName);
    await page.locator("#group-new-name").locator("xpath=ancestor::form").getByRole("button").click();
    await page.waitForURL(/\/en\/groups\/\d+$/);
    const groupURL = page.url();

    await page.locator("nav a[data-section=teams]").click();
    await page.locator("#team-new-name").fill(teamName);
    await page.locator("#team-new-birth-year-from").fill("2015");
    await page.locator("#team-new-birth-year-to").fill("2016");
    await page.getByRole("button", { name: "Create a team" }).click();
    await page.waitForURL(/\/en\/groups\/\d+\/teams\/\d+$/);
    const teamURL = page.url();

    // The admin creates a player invite for the team with a use limit.
    const playerCode = await createInvite(page, groupURL, { role: "player", team: teamName, maxUses: 5 });
    const row = page.locator("tr[data-invite-status=active]").filter({ hasText: playerCode });
    await expect(row).toContainText(teamName);
    await expect(row).toContainText("0 / 5");

    // A guest joins with it, lands on the team, and sees it in the list.
    const guest = await becomeGuest(browser);
    await joinWithCode(guest.page, appURL, playerCode);
    await guest.page.waitForURL(teamURL);
    await expect(guest.page.locator("#team-heading")).toContainText(teamName);
    await expect(guest.page.getByTestId("group-viewer-roles")).toHaveText(/Player/);
    await guest.page.goto(`${groupURL}/teams`);
    await expect(guest.page.getByTestId("teams-active")).toContainText(teamName);
    await page.goto(`${groupURL}/invites`);
    await expect(page.locator("tr[data-invite-status=active]").filter({ hasText: playerCode })).toContainText("1 / 5");

    // A guest cannot use a coach invite.
    const coachCode = await createInvite(page, groupURL, { role: "coach" });
    await joinWithCode(guest.page, appURL, coachCode);
    await expect(guest.page.locator("#alerts")).toContainText("Upgrade to a full account");

    // A revoked code stops working.
    const revokedCode = await createInvite(page, groupURL, { role: "player" });
    page.once("dialog", (dialog) => void dialog.accept());
    await page.locator("tr[data-invite-status=active]").filter({ hasText: revokedCode }).getByRole("button", { name: /Revoke/ }).click();
    await expect(page.locator("tr[data-invite-status=revoked]").filter({ hasText: revokedCode })).toBeVisible();
    await joinWithCode(guest.page, appURL, revokedCode);
    await expect(guest.page.locator("#alerts")).toContainText("was revoked");

    // A parent joins with a parent invite and adds a child profile.
    const parentCode = await createInvite(page, groupURL, { role: "parent" });
    const parent = await becomeGuest(browser);
    await joinWithCode(parent.page, appURL, parentCode);
    await parent.page.waitForURL(groupURL);
    await expect(parent.page.getByTestId("group-viewer-roles")).toHaveText(/Parent/);
    await parent.page.locator("nav a[data-section=children]").click();
    await parent.page.locator("#child-new-name").fill("Anna");
    await parent.page.locator("#child-new-birth-year").fill("2016");
    await parent.page.getByRole("button", { name: "Add a child" }).click();
    await expect(parent.page.locator("#alerts")).toContainText("Child added.");
    const child = parent.page.getByTestId("children").locator("li[data-child-id]").filter({ hasText: "Anna" });
    await expect(child).toContainText("Profile");
    await expect(child.getByTestId("child-birth-year")).toHaveText("2016");
    const childID = await child.getAttribute("data-child-id");

    // The admin puts the child on the team; the parent then sees the team
    // and appears on its roster as the child's parent.
    await page.goto(teamURL);
    await page.locator("#team-roster-entry").selectOption(`${childID}:player`);
    await page.getByRole("button", { name: "Add to team" }).click();
    await expect(page.locator("#alerts")).toContainText("Added to the team.");
    await expect(page.getByTestId("team-parents").locator("[data-roster-linked=true]")).toContainText("Parent of Anna");

    await parent.page.goto(teamURL);
    await expect(parent.page.locator("#team-heading")).toContainText(teamName);
    await parent.page.goto(`${groupURL}/children`);
    await expect(parent.page.getByTestId("child-team")).toHaveText(teamName);

    // Outsiders still get 404 on the new pages.
    const outsider = await signUpUser(browser, "calbooker-invites-outsider");
    for (const url of [`${groupURL}/invites`, `${groupURL}/children`]) {
      const response = await outsider.page.goto(url);
      expect(response?.status(), url).toBe(404);
    }
    // A player sees neither invites nor children.
    expect((await guest.page.goto(`${groupURL}/invites`))?.status()).toBe(403);

    await outsider.context.close();
    await parent.context.close();
    await guest.context.close();
    await owner.context.close();
  });
});
