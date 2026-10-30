import { createHash } from "node:crypto";
import { chmodSync, closeSync, existsSync, mkdirSync, openSync, readFileSync, renameSync, rmSync, statSync, writeFileSync } from "node:fs";
import { join } from "node:path";

import { expect, type Browser, type BrowserContext, type Page } from "@playwright/test";

import { landingPattern, settleOnHome, signInAtProvider, signUpAtProvider, startSignIn, type SignedInUser } from "./auth";
import { allowExtraTime } from "./backoff";
import { appURL, clientID, clientName, providerURL, repositoryRoot } from "./env";
import { uniqueEmail, uniquePassword } from "./names";

// A pool of full provider accounts, created once per worktree and reused by
// every spec and run, so a run spends almost none of the provider's shared
// sign-in and sign-up budget (backoff.ts).
//
// Each role ("owner", "coach", "member", "outsider", or any other short
// name) is one provider account. Its email and random password, and the
// browser storage state of its last sign-in, live in a git-ignored directory
// (.auth/ beside the specs) keyed by provider URL, Hydra client, and
// calbooker URL. Delete tests/browser/.auth/ to start over.
//
// Calbooker data made by pool accounts (groups, teams, memberships) stays in
// the worktree database between runs: give everything a uniqueName and never
// assume an account starts empty.

/** Where this worktree keeps all pools (git-ignored). */
export const authRoot = join(repositoryRoot, "tests", "browser", ".auth");

function slug(value: string): string {
  return value.replace(/[^A-Za-z0-9.-]+/g, "-").replace(/^-+|-+$/g, "") || "default";
}

/** This stack's pool directory: one per provider URL, client, and calbooker URL. */
export const poolDir = join(
  authRoot,
  [
    slug(new URL(providerURL).host),
    slug(clientID || clientName),
    createHash("sha256").update(`${providerURL}\n${clientID}\n${clientName}\n${appURL}`).digest("hex").slice(0, 10),
  ].join("_"),
);

const rolePattern = /^[a-z][a-z0-9-]{0,31}$/;

interface PoolAccount {
  email: string;
  password: string;
  created: string;
}

function accountPath(role: string): string {
  return join(poolDir, `${role}.account.json`);
}

function statePath(role: string): string {
  return join(poolDir, `${role}.state.json`);
}

function ensurePoolDir(): void {
  mkdirSync(poolDir, { recursive: true, mode: 0o700 });
}

function readAccount(role: string): PoolAccount | undefined {
  try {
    const value = JSON.parse(readFileSync(accountPath(role), "utf8")) as Partial<PoolAccount>;
    if (typeof value.email === "string" && typeof value.password === "string") {
      return { email: value.email, password: value.password, created: String(value.created ?? "") };
    }
  } catch {
    // Missing or unreadable: the role gets a new account.
  }
  return undefined;
}

// Writes owner-only files through a rename, so a crashed run never leaves a
// half-written account or state behind.
function writePrivate(path: string, text: string): void {
  const temporary = `${path}.${process.pid}.tmp`;
  writeFileSync(temporary, text, { mode: 0o600 });
  renameSync(temporary, path);
}

async function saveState(context: BrowserContext, role: string): Promise<void> {
  const temporary = `${statePath(role)}.${process.pid}.tmp`;
  await context.storageState({ path: temporary });
  chmodSync(temporary, 0o600);
  renameSync(temporary, statePath(role));
}

function forget(role: string): void {
  rmSync(accountPath(role), { force: true });
  rmSync(statePath(role), { force: true });
}

// One worker at a time prepares a role, so parallel spec files never sign
// up the same role twice. A lock older than lockStaleMs belongs to a run
// that died and is taken over.
const lockStaleMs = 10 * 60_000;

async function withRoleLock<T>(role: string, work: () => Promise<T>): Promise<T> {
  ensurePoolDir();
  const path = join(poolDir, `${role}.lock`);
  for (;;) {
    try {
      closeSync(openSync(path, "wx", 0o600));
      break;
    } catch (error) {
      if ((error as NodeJS.ErrnoException).code !== "EEXIST") {
        throw error;
      }
      try {
        if (Date.now() - statSync(path).mtimeMs > lockStaleMs) {
          rmSync(path, { force: true });
          continue;
        }
      } catch {
        continue;
      }
      allowExtraTime(1_000);
      await new Promise((resolve) => setTimeout(resolve, 1_000));
    }
  }
  try {
    return await work();
  } finally {
    rmSync(path, { force: true });
  }
}

async function onAppHome(page: Page): Promise<boolean> {
  await page.goto(`${appURL}/en/home`);
  return page.url() === `${appURL}/en/home`;
}

async function onProviderHome(page: Page): Promise<boolean> {
  await page.goto(`${providerURL}/en/home`);
  return page.url() === `${providerURL}/en/home`;
}

// Brings a stored account's context to a signed-in calbooker home with a
// live provider session, spending a provider sign-in only when neither
// session survived. Returns false when the provider still rejects the
// password after waiting out the rate-limit window (the account is gone,
// for example after goweb's database was reset); other failures throw.
async function restore(context: BrowserContext, page: Page, account: PoolAccount): Promise<boolean> {
  const appSignedIn = await onAppHome(page);
  const providerSignedIn = await onProviderHome(page);
  if (appSignedIn && providerSignedIn) {
    return onAppHome(page);
  }
  if (providerSignedIn) {
    // Silent single sign-on through Hydra; no provider form involved.
    await startSignIn(page);
    if (landingPattern.test(page.url())) {
      await settleOnHome(page);
      return true;
    }
  }
  await context.clearCookies();
  await startSignIn(page);
  await expect(page).toHaveURL(`${providerURL}/en/sign-in`);
  const outcome = await signInAtProvider(page, account.email, account.password, {
    attempts: 3,
    retryInvalidCredentials: true,
  });
  if (outcome === "invalid-credentials") {
    return false;
  }
  if (outcome !== "done") {
    throw new Error(`pool sign-in failed at the provider: ${outcome}`);
  }
  await settleOnHome(page);
  return true;
}

async function create(browser: Browser, role: string): Promise<SignedInUser> {
  const context = await browser.newContext();
  const page = await context.newPage();
  const email = uniqueEmail(`calbooker-pool-${role}`);
  const password = uniquePassword();
  await startSignIn(page);
  await expect(page).toHaveURL(`${providerURL}/en/sign-in`);
  await signUpAtProvider(page, email, password);
  await expect(page).toHaveURL(`${appURL}/en/home`);
  writePrivate(accountPath(role), `${JSON.stringify({ email, password, created: new Date().toISOString() }, null, 2)}\n`);
  await saveState(context, role);
  console.log(`[account pool] ${role}: created a provider account`);
  return { context, page, email, password };
}

/**
 * A full account for role from this worktree's pool, signed in to calbooker
 * in a fresh context that starts on /en/home. The first call for a role signs
 * up a provider account; later calls (in this and later runs) reuse it from
 * its stored browser state, falling back to a silent single sign-on or a
 * password sign-in when the sessions ended. Close the context when done.
 *
 * Specs needing several distinct people use one role per person; the same
 * role in two specs is the same person, which is fine as long as each spec
 * creates its own uniquely named groups. Give a role of its own to any user
 * a scenario signs out or revokes, so other specs keep a live session.
 */
export async function poolUser(browser: Browser, role: string): Promise<SignedInUser> {
  if (!rolePattern.test(role)) {
    throw new Error(`pool role ${JSON.stringify(role)} must match ${rolePattern}`);
  }
  return withRoleLock(role, async () => {
    const account = readAccount(role);
    if (!account) {
      return create(browser, role);
    }
    const context = await browser.newContext(existsSync(statePath(role)) ? { storageState: statePath(role) } : {});
    const page = await context.newPage();
    if (await restore(context, page, account)) {
      await saveState(context, role);
      return { context, page, email: account.email, password: account.password };
    }
    await context.close();
    console.log(`[account pool] ${role}: the stored account no longer signs in; replacing it`);
    forget(role);
    return create(browser, role);
  });
}
