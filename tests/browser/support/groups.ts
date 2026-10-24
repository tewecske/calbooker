import { expect, type Page } from "@playwright/test";

/** Options for createInvite: the role and, for players and parents, a team. */
export interface InviteOptions {
  role: "admin" | "manager" | "coach" | "player" | "parent";
  team?: string;
  maxUses?: number;
}

/**
 * Creates an invite on the group's invite page (the page must belong to an
 * admin, manager, or coach) and returns its code.
 */
export async function createInvite(page: Page, groupURL: string, options: InviteOptions): Promise<string> {
  await page.goto(`${groupURL}/invites`);
  await page.locator("#invite-role").selectOption(options.role);
  if (options.team) {
    const option = page.locator("#invite-team option", { hasText: options.team }).first();
    await page.locator("#invite-team").selectOption((await option.getAttribute("value")) ?? "");
  }
  if (options.maxUses) {
    await page.locator("#invite-max-uses").fill(String(options.maxUses));
  }
  await page.getByRole("button", { name: "Create an invite" }).click();
  const code = page.getByTestId("invite-created-code");
  await expect(code).toBeVisible();
  return (await code.innerText()).trim();
}

/** Joins a group with an invite code from the groups page. */
export async function joinWithCode(page: Page, appURL: string, code: string): Promise<void> {
  await page.goto(`${appURL}/en/groups/join`);
  await page.locator("#group-invite-code").fill(code);
  await page.locator("#group-invite-code").locator("xpath=ancestor::form").getByRole("button").click();
}
