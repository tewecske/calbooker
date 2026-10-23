import { existsSync, readFileSync } from "node:fs";
import { join } from "node:path";

// Settings for the browser specs. Each value comes from the environment
// first (make e2e exports .env and .env.local), then from .env.local and
// .env in the repository root (so a plain `npx playwright test` works too),
// then from the loopback defaults in docs/local-development.md.
//
// scripts/dev-setup.sh writes CALBOOKER_PUBLIC_URL,
// CALBOOKER_BROWSER_PROVIDER_URL, and CALBOOKER_BROWSER_CLIENT_NAME into
// .env.local, so a worktree's specs follow its own port and Hydra client.

const repositoryRoot = join(__dirname, "..", "..", "..");

// parseDotEnv reads the KEY=value lines a POSIX shell would source from
// .env files written by hand or by dev-setup. Values may be wrapped in single
// or double quotes; there is no variable expansion.
export function parseDotEnv(text: string): Record<string, string> {
  const values: Record<string, string> = {};
  for (const rawLine of text.split(/\r?\n/)) {
    const line = rawLine.trim();
    if (line === "" || line.startsWith("#")) {
      continue;
    }
    const match = /^(?:export\s+)?([A-Za-z_][A-Za-z0-9_]*)=(.*)$/.exec(line);
    if (!match) {
      continue;
    }
    let value = match[2];
    const quote = value[0];
    if ((quote === "'" || quote === '"') && value.length >= 2 && value.endsWith(quote)) {
      value = value.slice(1, -1);
    }
    values[match[1]] = value;
  }
  return values;
}

function readDotEnv(name: string): Record<string, string> {
  const path = join(repositoryRoot, name);
  if (!existsSync(path)) {
    return {};
  }
  return parseDotEnv(readFileSync(path, "utf8"));
}

const fileValues = { ...readDotEnv(".env"), ...readDotEnv(".env.local") };

function setting(...names: string[]): string | undefined {
  for (const name of names) {
    const value = process.env[name] ?? fileValues[name];
    if (value !== undefined && value !== "") {
      return value;
    }
  }
  return undefined;
}

function origin(url: string | undefined): string | undefined {
  if (!url) {
    return undefined;
  }
  try {
    return new URL(url).origin;
  } catch {
    return undefined;
  }
}

function trimSlash(url: string): string {
  return url.replace(/\/$/, "");
}

/** Whether the opt-in single sign-on specs run (CALBOOKER_SSO_E2E=1, make e2e). */
export const ssoEnabled = process.env.CALBOOKER_SSO_E2E === "1";

/** Where browsers reach calbooker. */
export const appURL = trimSlash(setting("CALBOOKER_BROWSER_BASE_URL", "CALBOOKER_PUBLIC_URL") ?? "http://127.0.0.1:8082");

/**
 * Where browsers reach the goweb provider. Falls back to the origin of the
 * provider account link calbooker is configured with.
 */
export const providerURL = trimSlash(
  setting("CALBOOKER_BROWSER_PROVIDER_URL") ?? origin(setting("CALBOOKER_SSO_ACCOUNT_URL")) ?? "http://localhost:8080",
);

/** The Hydra client name the provider shows on its sign-in and sign-up pages. */
export const clientName = setting("CALBOOKER_BROWSER_CLIENT_NAME") ?? "Calbooker (development)";

/**
 * A provider administrator for scenarios that act at the provider (goweb's
 * bootstrap administrator in development). Never committed; empty skips them.
 */
export const providerAdmin = {
  email: setting("CALBOOKER_BROWSER_PROVIDER_ADMIN_EMAIL") ?? "",
  password: setting("CALBOOKER_BROWSER_PROVIDER_ADMIN_PASSWORD") ?? "",
};
