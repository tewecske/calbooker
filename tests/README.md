# Tests

Keep unit tests beside the package they cover. Use this directory for tests
that span packages or require a full application boundary.

Integration tests must use the `integration` build tag and must not contain
credentials, tokens, or other secret fixtures.

`postgres` provides isolated PostgreSQL schemas for integration tests. Tests
using `postgres.New(t)` require `CALBOOKER_DATABASE_URL`; they skip when it is
not configured. Each test gets a throwaway schema, so any database works,
including the local `calbooker` one:

```sh
make test-integration
# or
CALBOOKER_DATABASE_URL='postgres://goweb:...@localhost:55432/calbooker?sslmode=disable' go test -tags=integration ./...
```

`fixtures` inserts explicit users, groups, and memberships. Fixture helpers
accept a database or caller-owned transaction and never create transactions
implicitly.

## Browser specs

`browser/` holds Playwright specs that drive calbooker, the goweb provider,
and Hydra in a real browser. They are opt-in with `CALBOOKER_SSO_E2E=1`
(`make e2e`) and need the local stack from `docs/local-development.md`. They
create their own accounts at the provider (see "Account pool" below) and never
use committed credentials. Browsers come from `PLAYWRIGHT_BROWSERS_PATH` (Nix); never run
`npx playwright install` (see "Playwright browsers" in
`docs/local-development.md`).

Name a feature's spec `browser/<area>.spec.ts` and build it from the shared
helpers in `browser/support/` (import from `./support`):

| Helper | What it does |
| --- | --- |
| `poolUser(browser, role)` | new context signed in to calbooker on `/en/home` as the worktree's pooled provider account for `role`; returns `{context, page, email, password}`. Use this for every full user a scenario needs |
| `signUpUser(browser, prefix?)` | new context; signs up a brand-new account at the provider through calbooker's sign-in and lands on `/en/home`. Spends a provider sign-up: only for scenarios about a first sign-up |
| `signIn(browser, email, password)` | new context; signs an existing provider account in to calbooker |
| `becomeGuest(browser)` | new context holding a calbooker guest account (no provider attempt) |
| `startSignIn(page)`, `signUpAtProvider(...)`, `signInAtProvider(...)` | the individual steps, for flows that branch (guest upgrade, sign-out); both provider steps back off on the rate limit |
| `submitWithBackoff(page, label, submit, options?)`, `rateLimitText` | the rate-limit backoff, for a new provider form |
| `uniqueEmail`, `uniquePassword`, `uniqueName` | values that never collide across runs or worktrees |
| `appURL`, `providerURL`, `clientName`, `clientID`, `providerAdmin`, `ssoEnabled` | settings (below) |

### Account pool

The provider allows 10 sign-in and sign-up attempts per minute per client
address, shared by every worktree on the machine. So specs take full users
from a per-worktree pool instead of signing up new ones:

```ts
const owner = await poolUser(browser, "owner");
const coach = await poolUser(browser, "coach");
// ... owner.page, coach.page; close the contexts at the end
```

- A role is any short lowercase name (`owner`, `coach`, `member`,
  `outsider`, ...); one role is one provider account. Use a different role for
  each distinct person in a scenario. The same role in two spec files is the
  same person, which is fine because every spec creates its own uniquely
  named groups.
- The first `poolUser` call for a role signs up a provider account with a
  random password; later calls, in this run and later ones, load its saved
  browser state. If the sessions ended, the helper first tries a silent
  single sign-on through the provider session and only then signs in with the
  password; a role whose password stopped working is replaced.
- Give a role of its own to a user whose sessions a scenario ends (sign-out,
  revocation): `signout` and `revoked` in `sso.spec.ts`. Otherwise parallel
  spec files sharing the role would lose their session.
- Pool accounts keep their calbooker data between runs in the worktree
  database. Name groups, teams, and resources with `uniqueName`, look rows up
  by those names, and never assume an empty account or an empty groups list.
- Credentials and storage states live in `tests/browser/.auth/`, in one
  directory per provider URL, Hydra client ID, and calbooker URL. The
  directory is git-ignored and its files are owner-only; never commit or copy
  them. Delete the directory to reset the pool.
- A worker preparing a role holds `<role>.lock` in that directory, so parallel
  workers never sign up the same role twice.

`signUpAtProvider` and `signInAtProvider` recognise the provider's
"Too many attempts. Try again shortly." alert (goweb's
`auth.error.rate_limited`), log `[provider backoff] ...`, wait 61 to 71
seconds, and retry up to four attempts, lengthening the test timeout by the
wait. A rate-limited sign-in only says "The email, username, or password is
not correct.", so pool sign-ins (known-good passwords) retry on that text
too (`retryInvalidCredentials`). Each submission logs `[provider] sign-up
submitted` or `[provider] sign-in submitted`; a full run with a ready pool
logs two sign-ups (first visit, guest upgrade) and one sign-in (sign-out).
`browser/support/backoff.spec.ts` checks the backoff against faked provider
pages, without the stack.

Settings are read from the environment first, then from `.env.local` and
`.env` in the repository root, then loopback defaults:

| Setting | Variables, in order | Default |
| --- | --- | --- |
| calbooker URL | `CALBOOKER_BROWSER_BASE_URL`, `CALBOOKER_PUBLIC_URL` | `http://127.0.0.1:8082` |
| provider URL | `CALBOOKER_BROWSER_PROVIDER_URL`, origin of `CALBOOKER_SSO_ACCOUNT_URL` | `http://localhost:8080` |
| client name | `CALBOOKER_BROWSER_CLIENT_NAME` | `Calbooker (development)` |
| client ID (pool key) | `CALBOOKER_SSO_CLIENT_ID` | unset; the client name keys the pool |
| provider administrator | `CALBOOKER_BROWSER_PROVIDER_ADMIN_EMAIL`, `_PASSWORD` | unset; scenarios needing it skip |

`make dev-setup` writes the calbooker URL, provider URL, client name, and client ID into
`.env.local`, so each worktree's specs follow its own setup.
`browser/support/env.spec.ts` checks the `.env` parser without a browser.
