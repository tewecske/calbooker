import { expect, test, type Page } from "@playwright/test";

import { signInAtProvider, signUpAtProvider } from "./auth";
import { invalidCredentialsText, rateLimitText } from "./backoff";
import { clientName, providerURL } from "./env";

// Runs without the stack: the provider's pages are faked with page.route, so
// the rate-limit handling is checked without spending any of the provider's
// real budget.

const fast = { waitMs: 10, jitterMs: 10, quiet: true };
const doneURL = `${providerURL}/en/home`;

function form(kind: "sign-up" | "sign-in", alert = ""): string {
  const identifier = kind === "sign-up" ? "Email" : "Email or username";
  const button = kind === "sign-up" ? "Create account" : "Sign in";
  return `<!doctype html><html><body>
    <div id="alerts">${alert ? `<div class="alert alert-error" role="alert"><span>${alert}</span></div>` : ""}</div>
    <p data-testid="sso-client-name">${clientName}</p>
    <form method="post" action="/en/${kind}">
      <label>${identifier} <input name="identifier"></label>
      <label>Password <input name="password" type="password"></label>
      <button type="submit">${button}</button>
    </form></body></html>`;
}

// Fakes one provider form whose POST answers come from answers in order:
// an alert text, or "ok" to leave the sign-in and sign-up pages for doneURL.
async function fakeProvider(page: Page, kind: "sign-up" | "sign-in", answers: string[]): Promise<{ posts: number }> {
  const seen = { posts: 0 };
  await page.route(`${providerURL}/en/${kind}`, async (route) => {
    if (route.request().method() !== "POST") {
      await route.fulfill({ contentType: "text/html", body: form(kind) });
      return;
    }
    const answer = answers[Math.min(seen.posts, answers.length - 1)];
    seen.posts++;
    if (answer === "ok") {
      // A script navigation instead of a redirect: page.route does not see
      // redirected requests, and the real servers must stay out of this.
      await route.fulfill({ contentType: "text/html", body: `<script>location.replace("${doneURL}")</script>` });
      return;
    }
    await route.fulfill({ status: 422, contentType: "text/html", body: form(kind, answer) });
  });
  await page.route(doneURL, (route) => route.fulfill({ contentType: "text/html", body: "<h1>Home</h1>" }));
  return seen;
}

test.describe("support backoff", () => {
  test("waits out a rate-limited sign-up and retries it", async ({ page }) => {
    const seen = await fakeProvider(page, "sign-up", [rateLimitText, rateLimitText, "ok"]);
    const outcome = await signUpAtProvider(page, "pool@example.test", "secret-password", fast);
    expect(outcome).toBe("done");
    expect(seen.posts).toBe(3);
    await expect(page).toHaveURL(doneURL);
  });

  test("gives up after the attempt limit", async ({ page }) => {
    const seen = await fakeProvider(page, "sign-up", [rateLimitText]);
    const outcome = await signUpAtProvider(page, "pool@example.test", "secret-password", { ...fast, attempts: 2 });
    expect(outcome).toBe("rate-limited");
    expect(seen.posts).toBe(2);
  });

  test("does not retry other sign-up errors", async ({ page }) => {
    const seen = await fakeProvider(page, "sign-up", ["An account with this email already exists."]);
    expect(await signUpAtProvider(page, "pool@example.test", "secret-password", fast)).toBe("error");
    expect(seen.posts).toBe(1);
  });

  test("retries a rejected sign-in only for known-good credentials", async ({ page }) => {
    const seen = await fakeProvider(page, "sign-in", [invalidCredentialsText, "ok"]);
    await page.goto(`${providerURL}/en/sign-in`);
    expect(await signInAtProvider(page, "pool@example.test", "secret-password", fast)).toBe("invalid-credentials");
    expect(seen.posts).toBe(1);

    expect(
      await signInAtProvider(page, "pool@example.test", "secret-password", { ...fast, retryInvalidCredentials: true }),
    ).toBe("done");
    expect(seen.posts).toBe(2);
  });
});
