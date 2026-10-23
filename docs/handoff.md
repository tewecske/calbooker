# Handoff Notes

Context for the next work session. Created on 2026-10-23 from the goweb
template (`../goweb`, commit `8c03bc9`). Update or trim this file as the
booking features land.

## What calbooker is today

A copy of goweb's client app (`GOWEB_AUTH_MODE=client`) turned into a
standalone project with no booking features yet:

- Module `github.com/tewecske/calbooker`, env prefix `CALBOOKER_`, cookies
  `calbooker_session`, `calbooker_csrf`, `calbooker_oauth_state`,
  `calbooker_locale`.
- Kept: single sign-on client (`internal/adapter/oidc`,
  `internal/service/sso_client.go`), sessions with back-channel logout,
  guests (create, transfer code, upgrade through SSO), groups (create, invite,
  join, roles, leave), account settings (language, theme, link to the
  provider's account page), the administrator area (accounts, audit, system,
  maintenance, rate limits, usage, suspicious accounts), mail adapters (SMTP,
  Resend; nothing sends mail yet), tracing, usage events, en/hu locales.
- Removed (they belong to the provider, which stays in goweb): provider mode
  and the Hydra admin client, local sign-up and password sign-in, password
  hashing, password settings and reset, email confirmation, Google OAuth,
  linked-identity management, bootstrap administrator, sign-in history and
  lockouts, the `local`/`provider`/`client` mode switch. Google sign-in
  reaches calbooker through the provider.
- Schema squashed into three fresh migrations (`000001` accounts, `000002`
  groups, `000003` audit/usage/rate limits); no `password_hash`,
  `email_verification_tokens`, `password_reset_tokens`, `login_attempts`, or
  `sso_login_sessions`.
- Demo content from goweb still present and meant to be replaced: the home
  page text ("A server-rendered Go and HTMX application foundation."), the
  "Guest workspace" page at `/{language}/guest/write` (its Save button only
  creates the guest account), and the about page.

## Verified

- `make check`-equivalent (`gofmt`, `go vet`, unit tests), `go test -race
  -shuffle=on ./...`, and all integration tests (`-tags=integration`) against
  a real PostgreSQL 17 pass.
- Real end-to-end flows on loopback against goweb's Hydra and provider: sign-in
  page shows only the SSO button; first sign-in creates the account and lands
  on home; silent second sign-in; sign-out ends the provider session too;
  provider sign-out revokes the calbooker session through back-channel logout;
  guest upgrade through SSO keeps the account; all admin pages render after
  the SQL promotion in `docs/first-admin.md`.
- The NixOS module evaluates together with goweb's module (defaults inherited,
  ingress merged into goweb's tunnel, database init ordered before the
  container).

## Not verified yet

- `tests/browser/sso.spec.ts` has not been run (no Playwright browsers were
  installed; `npx playwright install chromium`, then `make e2e`).
- The tailnet branch of `scripts/dev-setup.sh` (relay container on
  `127.0.0.1:4447`) was written to mirror goweb's relay but only the loopback
  layout was exercised.
- Nothing has been deployed. The code is on GitHub (`tewecske/calbooker`,
  branch `master`); check that the CI `image` job is green and make the
  `calbooker` package public before `deploy/PREPARE.txt` step 4.
- goweb's Hydra contract test (`tests/hydra_integration_test.go`) was not
  carried over because it mostly tested the provider's admin client; the
  relying party keeps its unit tests in `internal/adapter/oidc`.

## Local state left behind

- In goweb's PostgreSQL volume: databases `calbooker` (dev, with a few test
  accounts) and `calbooker_test`.
- In goweb's Hydra database: OAuth2 client `calbooker-dev` registered for
  `http://127.0.0.1:8082` (loopback). Re-run `make dev-setup` to re-register
  it for your usual (tailnet) layout.
- `calbooker/.env` and `calbooker/.env.local` (git-ignored, mode 600).

## Ports and names

| | Development | Production |
| --- | --- | --- |
| calbooker URL | `http://127.0.0.1:8082` (or the goweb client host on 8082) | `https://calbooker.<domain>` |
| listen address | `127.0.0.1:8082` | `127.0.0.1:28090` |
| Hydra client ID | `calbooker-dev` | `calbooker` |
| database | `calbooker` in `goweb-postgres` | `calbooker` role and database |
| image | n/a | `ghcr.io/tewecske/calbooker:<commit sha>` |

## Coupling with goweb

Calbooker depends on these goweb details; if goweb changes them, update
`scripts/dev-setup.sh`, `deploy/`, and `nix/module.nix`:

- container names `goweb-postgres`, `goweb-hydra` and Hydra's admin API on
  `127.0.0.1:4445`
- goweb `.env` keys `POSTGRES_*`, `SSO_DEV_BIND`, `SSO_DEV_ISSUER_URL`,
  `SSO_DEV_PROVIDER_URL`, `SSO_DEV_CLIENT_URL`
- the provider's account page path `/en/account/settings`
- the NixOS options `services.goweb.issuerUrl`, `providerUrl`, `mailFrom`, and
  goweb's Cloudflare Tunnel (`services.cloudflared.tunnels.<id>`)

## Open questions for the booking features

Ask before designing the schema:

1. Who offers time and who books it? (people with calendars, rooms or other
   resources, or both; one host per booking or several)
2. How is availability defined? (weekly working hours, explicit slots,
   exceptions and holidays, buffers, minimum notice, maximum horizon)
3. Do groups become organizations or teams that own resources and members?
4. Can guests book without signing in, and what must a guest provide?
5. Time zones: per user, per resource, or one site time zone?
6. Notifications: confirmation, reminder, and cancellation mail; ICS
   attachments or calendar feeds; external calendar sync (Google, CalDAV)?
7. Rules: cancellation and rescheduling windows, approval by the host, limits
   per person, paid bookings?

Implementation hints carried over from the template's conventions: keep
booking times as `timestamptz`/`tstzrange` in PostgreSQL, prevent double
booking in the database (an exclusion constraint needs the `btree_gist`
extension, which a migration must create), use revision-checked writes for
edits, audit administrator actions, key all data by user ID so a guest's
bookings survive the SSO upgrade, and add en and hu catalog entries for every
new message.
