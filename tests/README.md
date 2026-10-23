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
create their own accounts at the provider and never use committed
credentials. Browsers come from `PLAYWRIGHT_BROWSERS_PATH` (Nix); never run
`npx playwright install` (see "Playwright browsers" in
`docs/local-development.md`).

Name a feature's spec `browser/<area>.spec.ts` and build it from the shared
helpers in `browser/support/` (import from `./support`):

| Helper | What it does |
| --- | --- |
| `signUpUser(browser, prefix?)` | new context; signs up a new account at the provider through calbooker's sign-in and lands on `/en/home`; returns `{context, page, email, password}` |
| `signIn(browser, email, password)` | new context; signs an existing provider account in to calbooker |
| `becomeGuest(browser)` | new context holding a calbooker guest account |
| `startSignIn(page)`, `signUpAtProvider(...)`, `signInAtProvider(...)` | the individual steps, for flows that branch (guest upgrade, sign-out) |
| `uniqueEmail`, `uniquePassword`, `uniqueName` | values that never collide across runs or worktrees |
| `appURL`, `providerURL`, `clientName`, `providerAdmin`, `ssoEnabled` | settings (below) |

Settings are read from the environment first, then from `.env.local` and
`.env` in the repository root, then loopback defaults:

| Setting | Variables, in order | Default |
| --- | --- | --- |
| calbooker URL | `CALBOOKER_BROWSER_BASE_URL`, `CALBOOKER_PUBLIC_URL` | `http://127.0.0.1:8082` |
| provider URL | `CALBOOKER_BROWSER_PROVIDER_URL`, origin of `CALBOOKER_SSO_ACCOUNT_URL` | `http://localhost:8080` |
| client name | `CALBOOKER_BROWSER_CLIENT_NAME` | `Calbooker (development)` |
| provider administrator | `CALBOOKER_BROWSER_PROVIDER_ADMIN_EMAIL`, `_PASSWORD` | unset; scenarios needing it skip |

`make dev-setup` writes the calbooker URL, provider URL, and client name into
`.env.local`, so each worktree's specs follow its own setup.
`browser/support/env.spec.ts` checks the `.env` parser without a browser.
