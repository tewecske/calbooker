import { test, type Page } from "@playwright/test";

import { providerURL } from "./env";

// The goweb provider allows 10 sign-in and sign-up attempts per minute per
// client address (one fixed window shared by both forms), and every worktree
// on this machine shares that budget. These helpers submit a provider form,
// recognise the rate-limit answer, wait the window out, and try again.

/** goweb's en text for auth.error.rate_limited (shown after a sign-up). */
export const rateLimitText = "Too many attempts. Try again shortly.";

/**
 * goweb's en text for auth.error.invalid_credentials. A rate-limited sign-in
 * shows this text too (the provider does not reveal the limit there), so
 * callers that know their credentials are right treat it as a limit.
 */
export const invalidCredentialsText = "The email, username, or password is not correct.";

/** What a provider form submission led to. */
export type ProviderOutcome = "done" | "rate-limited" | "invalid-credentials" | "error";

/** Settings for submitWithBackoff; the defaults suit goweb's one-minute window. */
export interface BackoffOptions {
  /** Submissions in total, including the first. */
  attempts?: number;
  /** How long to wait after a rate-limited submission, before jitter. */
  waitMs?: number;
  /** Extra random wait (0 to jitterMs) so lanes do not retry in step. */
  jitterMs?: number;
  /** Treat "not correct" as a possible limit (only for known-good credentials). */
  retryInvalidCredentials?: boolean;
  /** No log lines (for faked providers, which spend no real budget). */
  quiet?: boolean;
}

const defaults = { attempts: 4, waitMs: 61_000, jitterMs: 10_000, retryInvalidCredentials: false, quiet: false };

// How long one submission may take to either leave the provider's sign-in
// and sign-up pages or show an error alert.
const outcomeTimeoutMs = 20_000;

function isProviderAuthPage(url: URL): boolean {
  return url.origin === providerURL && /^\/[a-z]{2}\/sign-(in|up)$/.test(url.pathname);
}

/** Waits for the result of a submitted provider sign-in or sign-up form. */
export async function providerOutcome(page: Page): Promise<ProviderOutcome> {
  const alert = page.locator("#alerts [role=alert]").first();
  const result = await Promise.race([
    page.waitForURL((url) => !isProviderAuthPage(url), { timeout: outcomeTimeoutMs }).then(() => "left" as const),
    alert.waitFor({ state: "visible", timeout: outcomeTimeoutMs }).then(() => "alert" as const),
  ]);
  if (result === "left") {
    return "done";
  }
  const text = (await alert.innerText()).trim();
  if (text.includes(rateLimitText)) {
    return "rate-limited";
  }
  if (text.includes(invalidCredentialsText)) {
    return "invalid-credentials";
  }
  return "error";
}

/**
 * Lengthens the running test's timeout so a deliberate wait does not fail
 * it; outside a test (global setup) there is nothing to lengthen.
 */
export function allowExtraTime(ms: number): void {
  try {
    const info = test.info();
    if (info.timeout > 0) {
      info.setTimeout(info.timeout + ms);
    }
  } catch {
    // Not inside a test.
  }
}

/**
 * Runs submit (which opens a provider form, fills it, and submits it) and
 * waits for the outcome. A rate-limited answer is logged, waited out, and
 * retried up to the attempt limit. Returns the last outcome; "done" means the
 * browser left the provider's sign-in and sign-up pages. Every submission
 * logs "[provider] <label> submitted", so a run's output shows how much of
 * the shared budget it used (grep -c '^\[provider\] ').
 */
export async function submitWithBackoff(
  page: Page,
  label: string,
  submit: () => Promise<void>,
  options: BackoffOptions = {},
): Promise<ProviderOutcome> {
  const settings = { ...defaults, ...options };
  let outcome: ProviderOutcome = "error";
  for (let attempt = 1; attempt <= settings.attempts; attempt++) {
    if (!settings.quiet) {
      console.log(`[provider] ${label} submitted`);
    }
    await submit();
    outcome = await providerOutcome(page);
    const limited =
      outcome === "rate-limited" || (outcome === "invalid-credentials" && settings.retryInvalidCredentials);
    if (!limited || attempt === settings.attempts) {
      return outcome;
    }
    const wait = settings.waitMs + Math.floor(Math.random() * settings.jitterMs);
    if (!settings.quiet) {
      console.log(
        `[provider backoff] ${label}: ${outcome} (attempt ${attempt}/${settings.attempts}); waiting ${Math.round(wait / 1000)}s for the rate-limit window`,
      );
    }
    allowExtraTime(wait + outcomeTimeoutMs);
    await page.waitForTimeout(wait);
  }
  return outcome;
}
