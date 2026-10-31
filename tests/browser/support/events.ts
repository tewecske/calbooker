import type { Page } from "@playwright/test";

async function pick(page: Page, label: string, names: string[]): Promise<void> {
  const combobox = page.getByRole("combobox", { name: label });
  for (const name of names) {
    await combobox.fill(name);
    await combobox.press("Enter");
  }
}

/** Picks teams on the event form's Teams combobox by (partial) name. */
export async function pickEventTeams(page: Page, names: string[]): Promise<void> {
  await pick(page, "Teams", names);
}

/** Picks extra coaches on the event form's Extra coaches combobox by (partial) name. */
export async function pickEventCoaches(page: Page, names: string[]): Promise<void> {
  await pick(page, "Extra coaches", names);
}
