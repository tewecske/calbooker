# Local Development

Calbooker has no local sign-in: it always signs people in through the goweb
provider via Ory Hydra. Locally, PostgreSQL, Hydra, and the provider run from
the goweb checkout next to this one (`../goweb`, override with `GOWEB_DIR`).
Calbooker uses goweb's PostgreSQL container with its own `calbooker` database.

## Required tools

- Go 1.26.x; the repository pins toolchain `go1.26.5`
- `make`, `curl`
- Docker (for goweb's PostgreSQL and Hydra containers)
- Node.js 20+ and npm for the CSS build and browser specs
- A goweb checkout with its `.env` (goweb's `docs/local-development.md`)

## Layout

| Process | Address | Started from |
| --- | --- | --- |
| PostgreSQL (`goweb-postgres`) | `localhost:55432` | goweb: `make db-up` |
| Hydra (`goweb-hydra`) | issuer `http://127.0.0.1:4444`, admin `127.0.0.1:4445` | goweb: `make sso-up` |
| goweb provider | `http://localhost:8080` | goweb: `make run-provider` |
| calbooker | `http://127.0.0.1:8082` | calbooker: `make run` |

Calbooker must use a different host name than the provider (cookies ignore
ports): the defaults use `127.0.0.1` for calbooker and `localhost` for the
provider. Calbooker's cookies are named `calbooker_*`, so it can share a host
with goweb's own development client on port 8081.

The addresses follow goweb's `.env`: when goweb is set up for a tailnet
(`SSO_DEV_BIND`, `SSO_DEV_ISSUER_URL`, `SSO_DEV_PROVIDER_URL`,
`SSO_DEV_CLIENT_URL`), `make dev-setup` uses the same issuer and provider,
puts calbooker on the goweb client's host with port 8082, and binds to
`SSO_DEV_BIND`. Hydra refuses back-channel calls to tailnet addresses, so in
that case the script also starts a loopback relay container named after the
client ID (`calbooker-dev-backchannel-relay` on `127.0.0.1:4447` by default).
Override any of this with `CALBOOKER_DEV_URL`, `CALBOOKER_DEV_BIND`,
`CALBOOKER_DEV_ISSUER_URL`, and `CALBOOKER_DEV_PROVIDER_URL` (see
`scripts/dev-setup.sh`).

## Several checkouts at once

Each checkout (the main one and every git worktree) needs its own port,
database, Hydra client, and relay port so they can run and run browser specs
side by side. `make dev-setup` takes them from:

| Variable | Default | Meaning |
| --- | --- | --- |
| `CALBOOKER_DEV_URL` | goweb client host, port 8082 | where browsers reach this checkout; the port is also the listen port |
| `CALBOOKER_DEV_DATABASE` | `calbooker` | database in goweb's PostgreSQL |
| `CALBOOKER_DEV_CLIENT_ID` | `calbooker-dev` | Hydra client; also names the relay container `<client ID>-backchannel-relay` |
| `CALBOOKER_DEV_CLIENT_NAME` | `Calbooker (development)` | client name the provider shows |
| `CALBOOKER_DEV_RELAY_PORT` | `4447` | loopback port of the back-channel relay (tailnet layout only) |

For example, a worktree in the lane table of `docs/plan.md`:

```sh
cp .env.example .env
CALBOOKER_DEV_URL=http://<tailnet host>:8083 CALBOOKER_DEV_DATABASE=calbooker_a \
  CALBOOKER_DEV_CLIENT_ID=calbooker-dev-a CALBOOKER_DEV_RELAY_PORT=4448 \
  make dev-setup GOWEB_DIR=/path/to/goweb
make run
```

Re-running `make dev-setup` replaces only that checkout's client and relay
container. Remove a worktree's relay with `docker rm -f
<client ID>-backchannel-relay` when the worktree goes away.

## First run

```sh
# in ../goweb
make db-up
make sso-up
make run-provider          # keep running

# in calbooker
PLAYWRIGHT_SKIP_BROWSER_DOWNLOAD=1 npm ci
npm run build:css
cp .env.example .env
make dev-setup             # creates the calbooker database, registers calbooker-dev in Hydra, writes .env.local
make run
```

Open `http://127.0.0.1:8082/en/sign-in`, click sign in, and sign up or sign in
at the provider; you land back on calbooker's home page. To make that account
a calbooker administrator, see [first administrator](first-admin.md):

```sh
docker exec goweb-postgres psql -U goweb -d calbooker -c "UPDATE users SET is_admin = TRUE, version = version + 1 WHERE email = 'you@example.test' AND is_admin = FALSE;"
```

`make dev-setup` is safe to re-run: it keeps the database and rotates the
client secret in `.env.local`. Re-run it after resetting Hydra's database.
`.env` and `.env.local` are git-ignored; only `.env.example` is committed.

Health check:

```sh
curl -i http://127.0.0.1:8082/healthz
```

### Recreating the database

Issue #2 rewrote migrations 000001 to 000003 in place (nothing was deployed):
`users.kind` replaces `is_guest`, groups got a time zone, and roles and teams
are new tables. The migrator skips versions it already installed, so a
database migrated before that keeps the old schema and the app fails at run
time. Drop it once and run dev-setup again (use your checkout's database
name):

```sh
docker exec goweb-postgres psql -U goweb -d goweb -c "DROP DATABASE IF EXISTS calbooker WITH (FORCE)"
make dev-setup GOWEB_DIR=../goweb
```

Integration tests use throwaway schemas inside that database, so they need
nothing extra.

## Tests

```sh
make check                              # CSS build, gofmt, vet, unit tests
go test -race -shuffle=on ./...
make test-integration                   # needs .env.local (make dev-setup)
make e2e                                # browser specs against the running stack
```

`make test-integration` runs the `integration` build tag against the database
in `.env.local`; every test works in its own throwaway schema. CI runs the same
tests against a throwaway PostgreSQL.

`make e2e` runs the Playwright specs in `tests/browser/` against the running
stack. The revocation scenario also needs a provider administrator in
`CALBOOKER_BROWSER_PROVIDER_ADMIN_EMAIL` and `_PASSWORD` (goweb's bootstrap
administrator). The calbooker URL, provider URL, client name, and client ID
come from the environment or `.env.local` (written by `make dev-setup`); see
`tests/README.md`.

### Browser account pool and the provider rate limit

The provider allows 10 sign-in and sign-up attempts per minute per client
address, and every worktree on the machine shares that budget. The specs
therefore keep a pool of provider accounts per worktree: the first run signs
up one account per role (owner, coach, member, outsider, ...), and later runs
reuse them from their saved browser state. A full run then submits two
sign-ups (the first-visit and guest-upgrade scenarios, which must create new
accounts) and one sign-in (the sign-out scenario's account), down from about
eleven sign-ups.

The pool lives in `tests/browser/.auth/<provider host>_<client ID>_<hash>/`
(git-ignored, owner-only files): each role's email and random password and its
storage state. It is never committed. To start over (for example after
goweb's database was reset, or to inspect a clean run), delete it:

```sh
rm -rf tests/browser/.auth
```

A role whose saved password no longer works is replaced automatically, after
one rate-limit wait. Pool accounts keep their calbooker data between runs in
the worktree database, so specs name everything with `uniqueName` and never
assume an empty account. When the provider answers "Too many attempts. Try
again shortly." the helpers log `[provider backoff] ...`, wait out the
one-minute window with jitter, and retry (up to four attempts); each provider
form submission logs `[provider] sign-up submitted` or
`[provider] sign-in submitted`, so `make e2e | grep -c '^\[provider\] '`
counts a run's use of the budget.

### Playwright browsers

Browsers come from Nix, never from Playwright's downloader: home-manager sets
`PLAYWRIGHT_BROWSERS_PATH` to nixpkgs' `playwright-driver.browsers`. Never run
`npx playwright install`; install npm packages with
`PLAYWRIGHT_SKIP_BROWSER_DOWNLOAD=1 npm ci`.

`@playwright/test` is pinned exactly (`package.json`, currently `1.63.0`) to
the release whose bundled browser revisions match the directories in
`$PLAYWRIGHT_BROWSERS_PATH` (`chromium-1243`, `chromium_headless_shell-1243`,
`firefox-1543`, `webkit-2359`). The nixpkgs `playwright-driver` version number
can differ from the matching npm release, so compare revisions, not version
numbers. After a Nix update, re-check without downloading browsers:

```sh
ls "$PLAYWRIGHT_BROWSERS_PATH"
# browser revisions a candidate release expects (downloads only the npm tarball)
dir=$(mktemp -d) && (cd "$dir" && npm pack playwright-core@1.63.0 --silent >/dev/null \
  && tar -xzf playwright-core-*.tgz package/browsers.json && cat package/browsers.json)
```

Pick the release whose `chromium`, `chromium-headless-shell`, `firefox`, and
`webkit` revisions equal the directory suffixes, set it exactly in
`package.json`, and run `PLAYWRIGHT_SKIP_BROWSER_DOWNLOAD=1 npm install`. A
mismatch shows up as "Executable doesn't exist" when the specs start.

## Reading mail locally

Nothing sends mail yet. When booking notifications land, point
`CALBOOKER_MAIL_RELAY=smtp://localhost:1025` at goweb's Mailpit (started by
goweb's `make db-up`, inbox at `http://127.0.0.1:8025`).

## Week grid demo

With `CALBOOKER_ENV=development` (the `.env.example` default), calbooker
serves a demo of the week grid component at `/en/dev/weekgrid` (no sign-in;
items live in memory). Query parameters switch views: `week=B`,
`from=2026-10-19` (dated week), `resources=1`, `readonly=1`.
`tests/browser/weekgrid.spec.ts` drives it on a desktop and a touch viewport.
Other environments do not register the route.

## Conventions

- Configuration comes only from environment variables through
  `internal/config`; startup fails without the database and SSO settings.
- Keep secrets out of shell history, source files, logs, and test output.
- Templates use DaisyUI 5 components; the Content-Security-Policy forbids
  inline scripts and styles, so scripts go in `static/app.js` and styles in
  `static/src/app.css` (rebuild with `npm run build:css`).
- Each feature area owns its files: routes in a `register<Area>Routes`
  function beside its handlers in `internal/adapter/http/<area>.go` (one call
  in `handler.go`), messages in `internal/locale/catalogs/<lang>/<area>.json`,
  and browser specs in `tests/browser/<area>.spec.ts` using
  `tests/browser/support/`. The catalog files of a language are merged at
  load; a message ID defined in two files fails startup and the tests.
