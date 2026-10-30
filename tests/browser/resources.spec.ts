import { expect, test } from "@playwright/test";

import { appURL, becomeGuest, createInvite, joinWithCode, poolUser, ssoEnabled, uniqueName } from "./support";

// Resources (#4): admins and managers create, edit, archive, and restore
// resources; coaches see a read-only list; players and parents do not see
// the resource pages. Opt-in like the other single sign-on specs:
// CALBOOKER_SSO_E2E=1 (make e2e) with goweb's Hydra and provider running and
// calbooker started with make run.
test.describe("resources", () => {
  test.skip(!ssoEnabled, "set CALBOOKER_SSO_E2E=1 (make e2e) to run the resource specs");
  test.describe.configure({ mode: "serial" });

  test("an admin creates, edits, and archives a resource; a coach reads the list", async ({ browser }) => {
    const owner = await poolUser(browser, "owner");
    const page = owner.page;
    const clubName = uniqueName("Club");
    const fieldName = uniqueName("Field");
    const renamed = `${fieldName} North`;

    await page.goto(`${appURL}/en/groups`);
    await page.locator("#group-new-name").fill(clubName);
    await page.locator("#group-new-name").locator("xpath=ancestor::form").getByRole("button").click();
    await page.waitForURL(/\/en\/groups\/(?!join$)[a-z0-9-]+$/);
    const groupURL = page.url();

    // Create a field with a team limit.
    await page.locator("nav a[data-section=resources]").click();
    await expect(page.getByTestId("resources-empty")).toBeVisible();
    await page.locator("#resource-new-name").fill(fieldName);
    await page.locator("#resource-new-kind").selectOption({ label: "Field" });
    await page.locator("#resource-new-max-teams").fill("2");
    await page.locator("#resource-new-description").fill("Behind the clubhouse");
    await page.getByRole("button", { name: "Add a resource" }).click();
    await expect(page.locator("#alerts")).toContainText("Resource added.");
    const row = page.getByTestId("resources-active").locator("tr[data-resource-id]").filter({ hasText: fieldName });
    await expect(row).toContainText("Field");
    await expect(row.getByTestId("resource-capacity")).toHaveText("2");

    // A second active resource with the same name is refused.
    await page.locator("#resource-new-name").fill(fieldName.toUpperCase());
    await page.getByRole("button", { name: "Add a resource" }).click();
    await expect(page.locator("#resource-new-name-error")).toContainText("already has this name");

    // Edit it: rename, change the kind, and drop the limit.
    await row.getByRole("link", { name: fieldName }).click();
    await page.waitForURL(/\/en\/groups\/[a-z0-9-]+\/resources\/\d+$/);
    const resourceURL = page.url();
    await expect(page.locator("#resource-heading")).toHaveText(fieldName);
    await page.locator("#resource-edit-name").fill(renamed);
    await page.locator("#resource-edit-kind").selectOption({ label: "Court" });
    await page.locator("#resource-edit-max-teams").fill("");
    await page.getByRole("button", { name: "Save resource" }).click();
    await expect(page.locator("#alerts")).toContainText("Resource saved.");
    await expect(page.locator("#resource-heading")).toHaveText(renamed);
    await expect(page.getByTestId("resource-kind")).toHaveText("Court");
    await expect(page.getByTestId("resource-capacity")).toHaveText("No limit");

    // Archive it; it moves to the archived list.
    page.once("dialog", (dialog) => void dialog.accept());
    await page.getByRole("button", { name: "Archive resource" }).click();
    await expect(page.locator("#alerts")).toContainText("Resource archived.");
    await expect(page.getByTestId("resource-archived-badge")).toBeVisible();
    await page.goto(`${groupURL}/resources`);
    await expect(page.getByTestId("resources-archived")).toContainText(renamed);
    await expect(page.getByTestId("resources-empty")).toBeVisible();

    // Restore it again, then add a second resource the coach will see active.
    await page.goto(resourceURL);
    await page.getByRole("button", { name: "Restore resource" }).click();
    await expect(page.locator("#alerts")).toContainText("Resource restored.");
    const hallName = uniqueName("Hall");
    await page.goto(`${groupURL}/resources`);
    await page.locator("#resource-new-name").fill(hallName);
    await page.locator("#resource-new-kind").selectOption({ label: "Sports hall" });
    await page.getByRole("button", { name: "Add a resource" }).click();
    await expect(page.locator("#alerts")).toContainText("Resource added.");
    await page.goto(resourceURL);
    page.once("dialog", (dialog) => void dialog.accept());
    await page.getByRole("button", { name: "Archive resource" }).click();
    await expect(page.locator("#alerts")).toContainText("Resource archived.");

    // Before joining, the future coach gets 404 like any outsider.
    const coach = await poolUser(browser, "coach");
    expect((await coach.page.goto(`${groupURL}/resources`))?.status()).toBe(404);

    // A coach sees the list read only.
    const coachCode = await createInvite(page, groupURL, { role: "coach" });
    await joinWithCode(coach.page, appURL, coachCode);
    await coach.page.waitForURL(groupURL);
    await coach.page.locator("nav a[data-section=resources]").click();
    await expect(coach.page.getByTestId("resources-read-only")).toBeVisible();
    await expect(coach.page.getByTestId("resources-active")).toContainText(hallName);
    await expect(coach.page.getByTestId("resources-archived")).toContainText(renamed);
    await expect(coach.page.locator("#resource-new-name")).toHaveCount(0);
    await coach.page.goto(resourceURL);
    await expect(coach.page.locator("#resource-heading")).toContainText(renamed);
    await expect(coach.page.locator("#resource-edit-name")).toHaveCount(0);
    await expect(coach.page.getByRole("button", { name: "Restore resource" })).toHaveCount(0);

    // A player has no resources tab and is denied the page.
    const playerCode = await createInvite(page, groupURL, { role: "player" });
    const player = await becomeGuest(browser);
    await joinWithCode(player.page, appURL, playerCode);
    await player.page.waitForURL(groupURL);
    await expect(player.page.locator("nav a[data-section=resources]")).toHaveCount(0);
    expect((await player.page.goto(`${groupURL}/resources`))?.status()).toBe(403);

    await player.context.close();
    await coach.context.close();
    await owner.context.close();
  });
});
