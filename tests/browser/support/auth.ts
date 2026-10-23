import { expect, type Browser, type BrowserContext, type Page } from "@playwright/test";

import { appURL, clientName, providerURL } from "./env";
import { uniqueEmail, uniquePassword } from "./names";

/** A browser context signed in to calbooker, with the account it used. */
export interface SignedInUser {
  context: BrowserContext;
  page: Page;
  email: string;
  password: string;
}

/**
 * Signs up a new account at the provider. The provider's sign-up form
 * redirects straight back to Hydra when a login challenge is pending, so this
 * finishes a calbooker sign-in that is already under way.
 */
export async function signUpAtProvider(page: Page, email: string, password: string): Promise<void> {
  await page.goto(`${providerURL}/en/sign-up`);
  await expect(page.getByTestId("sso-client-name")).toContainText(clientName);
  await page.getByLabel("Email", { exact: true }).fill(email);
  await page.getByLabel("Password", { exact: true }).fill(password);
  await page.getByRole("button", { name: /create account/i }).click();
}

/** Fills and submits the provider's sign-in form on the current page. */
export async function signInAtProvider(page: Page, identifier: string, password: string): Promise<void> {
  await page.getByLabel("Email or username", { exact: true }).fill(identifier);
  await page.getByLabel("Password", { exact: true }).fill(password);
  await page.getByRole("button", { name: /^sign in$/i }).click();
}

/** Starts calbooker's single sign-on flow from its sign-in page. */
export async function startSignIn(page: Page): Promise<void> {
  await page.goto(`${appURL}/en/sign-in`);
  await page.getByTestId("sso-sign-in").click();
}

/**
 * A brand-new person opens calbooker in a fresh context, is sent to the
 * provider, signs up there, and lands on the calbooker home signed in.
 */
export async function signUpUser(browser: Browser, prefix = "calbooker"): Promise<SignedInUser> {
  const context = await browser.newContext();
  const page = await context.newPage();
  const email = uniqueEmail(prefix);
  const password = uniquePassword();
  await startSignIn(page);
  await expect(page).toHaveURL(`${providerURL}/en/sign-in`);
  await expect(page.getByTestId("sso-client-name")).toContainText(clientName);
  await signUpAtProvider(page, email, password);
  await expect(page).toHaveURL(`${appURL}/en/home`);
  return { context, page, email, password };
}

/**
 * Signs an existing provider account in to calbooker in a fresh context
 * (for example a user from signUpUser after its context was closed).
 */
export async function signIn(browser: Browser, email: string, password: string): Promise<SignedInUser> {
  const context = await browser.newContext();
  const page = await context.newPage();
  await startSignIn(page);
  await expect(page).toHaveURL(`${providerURL}/en/sign-in`);
  await signInAtProvider(page, email, password);
  await expect(page).toHaveURL(`${appURL}/en/home`);
  return { context, page, email, password };
}

/** A browser context holding a calbooker guest account. */
export interface Guest {
  context: BrowserContext;
  page: Page;
}

/** Becomes a guest in a fresh context by saving on the guest write page. */
export async function becomeGuest(browser: Browser): Promise<Guest> {
  const context = await browser.newContext();
  const page = await context.newPage();
  await page.goto(`${appURL}/en/guest/write`);
  await page.getByRole("button", { name: /save/i }).first().click();
  await expect(page.locator("[data-guest-banner]")).toBeVisible();
  return { context, page };
}
