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
that case the script also starts a loopback relay container
(`calbooker-backchannel-relay`, `127.0.0.1:4447`). Override any of this with
`CALBOOKER_DEV_URL`, `CALBOOKER_DEV_BIND`, `CALBOOKER_DEV_ISSUER_URL`, and
`CALBOOKER_DEV_PROVIDER_URL` (see `scripts/dev-setup.sh`).

## First run

```sh
# in ../goweb
make db-up
make sso-up
make run-provider          # keep running

# in calbooker
npm install
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

`make e2e` runs `tests/browser/sso.spec.ts` with Playwright (`npx playwright
install chromium` once). It signs up new accounts at the provider; the
revocation scenario also needs a provider administrator in
`CALBOOKER_BROWSER_PROVIDER_ADMIN_EMAIL` and `_PASSWORD` (goweb's bootstrap
administrator).

## Reading mail locally

Nothing sends mail yet. When booking notifications land, point
`CALBOOKER_MAIL_RELAY=smtp://localhost:1025` at goweb's Mailpit (started by
goweb's `make db-up`, inbox at `http://127.0.0.1:8025`).

## Conventions

- Configuration comes only from environment variables through
  `internal/config`; startup fails without the database and SSO settings.
- Keep secrets out of shell history, source files, logs, and test output.
- Templates use DaisyUI 5 components; the Content-Security-Policy forbids
  inline scripts and styles, so scripts go in `static/app.js` and styles in
  `static/src/app.css` (rebuild with `npm run build:css`).
