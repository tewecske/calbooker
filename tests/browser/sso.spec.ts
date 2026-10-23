import { expect, test } from "@playwright/test";

import {
  appURL,
  becomeGuest,
  providerAdmin,
  providerURL,
  signInAtProvider,
  signUpAtProvider,
  signUpUser as newUser,
  ssoEnabled as enabled,
  startSignIn,
  uniqueEmail,
  uniqueName,
  uniquePassword,
} from "./support";

// Single sign-on between calbooker and the goweb provider through Hydra.
//
// Opt-in: CALBOOKER_SSO_E2E=1 (make e2e) with goweb's Hydra and provider
// running and calbooker started with make run (see docs/local-development.md).
// The provider and calbooker must be on different host names because cookies
// ignore ports. URLs and the client name come from tests/browser/support/env.ts.
const providerAdminEmail = providerAdmin.email;
const providerAdminPassword = providerAdmin.password;

const invitePattern = /^[A-HJKMNPQRSTUVWXYZ2-9]{12}$/;

test.describe("single sign-on", () => {
  test.skip(!enabled, "set CALBOOKER_SSO_E2E=1 (make e2e) to run the single sign-on specs");
  // Sign-up is rate limited per origin at the provider, and the scenarios
  // share Hydra, so run them one after another.
  test.describe.configure({ mode: "serial" });

  test("first visit signs in through the provider and returns", async ({ browser }) => {
    const { context, page, email } = await newUser(browser);
    await expect(page.getByRole("heading", { level: 1, name: /your home/i })).toBeVisible();

    // Calbooker keeps no credentials: settings show the provider's account.
    await page.goto(`${appURL}/en/account/settings`);
    await expect(page.getByTestId("sso-account-email")).toHaveText(email);
    await expect(page.locator("input[type=password]")).toHaveCount(0);
    await context.close();
  });

  test("a second visit is silent", async ({ browser }) => {
    const { context, page } = await newUser(browser);
    // Forget the calbooker session only; the provider and Hydra sessions stay.
    await context.clearCookies({ domain: new URL(appURL).hostname });
    await startSignIn(page);
    await expect(page).toHaveURL(`${appURL}/en/home`);
    await context.close();
  });

  test("signing out also ends the provider session", async ({ browser }) => {
    const { context, page } = await newUser(browser);
    await page.locator("summary[aria-label='Account menu']").click();
    await page.getByRole("button", { name: /sign out/i }).click();
    await expect(page).toHaveURL(`${appURL}/en/sign-in`);

    await page.goto(`${providerURL}/en/home`);
    await expect(page).toHaveURL(`${providerURL}/en/sign-in`);
    await context.close();
  });

  test("a session revocation at the provider signs calbooker out", async ({ browser }) => {
    test.skip(!providerAdminEmail || !providerAdminPassword, "requires CALBOOKER_BROWSER_PROVIDER_ADMIN_EMAIL and _PASSWORD");
    const user = await newUser(browser);
    const adminContext = await browser.newContext();
    const admin = await adminContext.newPage();
    await admin.goto(`${providerURL}/en/sign-in`);
    await signInAtProvider(admin, providerAdminEmail, providerAdminPassword);
    await admin.goto(`${providerURL}/en/admin/users?q=${encodeURIComponent(user.email)}`);
    await admin.locator("tbody tr").filter({ hasText: user.email }).getByRole("link", { name: /view/i }).click();
    admin.once("dialog", (dialog) => void dialog.accept());
    await admin.locator("section[aria-labelledby=admin-sessions-heading] form button[type=submit]").click();
    await expect(admin.locator("[data-testid=session-count]")).toHaveText("0");

    // Back-channel logout is asynchronous, so poll until calbooker asks for a
    // sign-in.
    await expect
      .poll(
        async () => {
          await user.page.goto(`${appURL}/en/home`);
          return new URL(user.page.url()).pathname;
        },
        { timeout: 20_000, intervals: [500, 1000, 2000] },
      )
      .toBe("/en/sign-in");
    await adminContext.close();
    await user.context.close();
  });

  test("a guest upgrade through single sign-on keeps the guest's groups", async ({ browser }) => {
    const owner = await newUser(browser);
    await owner.page.goto(`${appURL}/en/groups`);
    await owner.page.locator("#group-new-name").fill(uniqueName("SSO"));
    await owner.page.locator("#group-new-name").locator("xpath=ancestor::form").getByRole("button").click();
    await owner.page.waitForURL(/\/en\/groups\/\d+$/);
    const inviteCode = (await owner.page.getByTestId("group-invite-code").innerText()).trim();
    expect(inviteCode).toMatch(invitePattern);
    const groupName = await owner.page.locator("#group-summary-heading").innerText();

    const { context: guestContext, page: guest } = await becomeGuest(browser);
    await guest.goto(`${appURL}/en/groups/join`);
    await guest.locator("#group-invite-code").fill(inviteCode);
    await guest.locator("#group-invite-code").locator("xpath=ancestor::form").getByRole("button").click();
    await guest.waitForURL(/\/en\/groups\/\d+$/);
    const groupURL = guest.url();

    await guest.goto(`${appURL}/en/guest/upgrade`);
    await expect(guest.locator("input[type=password]")).toHaveCount(0);
    await guest.getByTestId("guest-upgrade-sso").click();
    await expect(guest).toHaveURL(`${providerURL}/en/sign-in`);
    await signUpAtProvider(guest, uniqueEmail("calbooker-guest"), uniquePassword());
    await expect(guest).toHaveURL(`${appURL}/en/home`);

    await expect(guest.locator("[data-guest-banner]")).toHaveCount(0);
    await guest.goto(groupURL);
    await expect(guest.locator("#group-summary-heading")).toHaveText(groupName);
    await guestContext.close();
    await owner.context.close();
  });
});
