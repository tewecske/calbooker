# Project Instructions

Before any Go coding, review and load the `samber/cc-skills-golang@golang-how-to` skill first. It routes each task to relevant Go skills.

Calbooker is a calendar booking app that signs people in only through the goweb single sign-on stack (Ory Hydra and the goweb provider, in `../goweb`). Start with `docs/plan.md`: it is the delivery plan for the booking features (milestones, GitHub issues, lanes, subagent rules, local environment). Product decisions and the data model are in `docs/design/booking.md`; what was carried over from goweb is in `docs/handoff.md`.

## Architecture

Use hexagonal boundaries and manual constructor wiring. Keep domain code independent of adapters and third-party packages. Define ports at consuming boundaries only when a second implementation or test double needs them.

Package responsibilities:

- `cmd/web` owns process startup and dependency wiring. Keep business logic out.
- `internal/app` owns explicit application graph composition; it does not own resource lifecycle.
- `internal/config` loads typed environment configuration (`CALBOOKER_*`) and redacts secrets.
- `internal/locale` owns supported language codes (en, hu), canonical localized paths, and translation catalogs. Catalogs are split per area in `internal/locale/catalogs/<lang>/<area>.json` (`common`, `auth`, `guest`, `settings`, `groups`, `admin`, `about`, ...) and merged at load; a message ID defined in two files is a load error. Keep a message in the same area file for every language.
- `internal/service` contains application use cases.
- `internal/adapter/http` owns routes, page rendering, full-page/HTMX responses, and transport adapters. Each area registers its routes in a `register<Area>Routes` function beside its handlers (`<area>.go`, looping over `localizedPrefixes()`); `handler.go` keeps one call per area.
- `internal/adapter/http/middleware` owns request context, recovery, logging, tracing, authentication, and CSRF boundaries.
- `internal/adapter/mail` owns mail delivery adapters (SMTP relay, Resend); mail ports stay in consuming services.
- `internal/adapter/oidc` owns the OpenID Connect relying party for Hydra (discovery, JWKS, RS256-only ID and logout token verification, standard library only); the `SSORelyingParty` port and account mapping stay in `internal/service`.
- `internal/server` owns listener lifecycle, graceful shutdown, and dependency closure.
- `internal/store` owns database connection settings, migration execution, and persistence adapters.
- `migrations/` stores ordered database schema changes. Calbooker's history starts at `000001`; never edit an installed migration.
- `templates/` stores server-rendered HTML templates.
- `static/` stores browser assets.
- `tests/browser` contains opt-in Playwright specs (`<area>.spec.ts`) built on the shared helpers in `tests/browser/support/` (sign up, sign in, become a guest, unique names, settings from the environment and `.env.local`); do not commit browser credentials or bearer-token fixtures. `@playwright/test` is pinned to the release matching the Nix `PLAYWRIGHT_BROWSERS_PATH` revisions; never run `npx playwright install`.
- `deploy/` stores container run scripts, the Hydra client registration script, the env template, and copy-paste runbooks (`PREPARE.txt`, `SETUP.txt`, `SETUP-nix.txt`; commands and values only). Keep them in step with the scripts, env template, and the NixOS module. They assume goweb's deployment (Hydra, provider, tunnel) is already up.
- `flake.nix` and `nix/module.nix` are the NixOS module (`services.calbooker`); it defaults issuer, provider, and mail settings from `services.goweb` and adds its host to goweb's Cloudflare Tunnel.
- `scripts/dev-setup.sh` prepares local development against goweb's containers (`make dev-setup`). `CALBOOKER_DEV_URL`, `CALBOOKER_DEV_DATABASE`, `CALBOOKER_DEV_CLIENT_ID`, and `CALBOOKER_DEV_RELAY_PORT` give each checkout or worktree its own port, database, Hydra client, and back-channel relay (container `<client ID>-backchannel-relay`).
- Keep tests beside packages they cover. Use `tests/` for cross-package or integration support.

## Go development

Use Go 1.26 language features only. Prefer standard-library packages. Add third-party dependencies only under the policy in `docs/dependency-policy.md`.

## Identity

- Calbooker stores no credentials and has no password, sign-up, recovery, email confirmation, or Google sign-in code. Those live at the provider; do not add them here.
- Accounts are created on the first single sign-on sign-in and mapped by issuer subject (`oauth_identities`, provider `sso`). Email and display name are issuer-owned and refreshed on sign-in; administrators change only roles.
- Guests are local accounts without an identity; `/{language}/guest/upgrade` turns a guest into an issuer account and keeps its data. New features that store per-user data must work for guests and survive the upgrade (key data by user ID).
- Sign-out ends the local session and then the Hydra session. `POST /oauth/sso/backchannel-logout` revokes sessions by Hydra `sid`; it is CSRF-exempt and authenticated by the signed logout token.
- `SessionService.RevokeUser` ends calbooker's sessions only. Delete accounts only after revoking, because deletion cascades away the recorded session IDs.

## Runtime and configuration

- Load configuration from environment variables through `internal/config`. `CALBOOKER_DATABASE_URL` and the SSO issuer, client ID, and client secret are always required.
- Validate external values at server boundaries. Do not trust client-side validation.
- Start HTTP through `internal/server` and handle `SIGINT` and `SIGTERM` with graceful draining.
- Close registered dependencies after HTTP shutdown, even when startup fails.
- Startup opens PostgreSQL and applies migrations before serving requests.
- Production startup fails through `config.ErrUnsafeProductionConfig` unless `CALBOOKER_SESSION_COOKIE_SECURE` is true, `CALBOOKER_PUBLIC_URL` and the SSO issuer, callback, and account URLs use HTTPS, `CALBOOKER_SESSION_SECRET` is set, and mail delivery (`CALBOOKER_MAIL_RELAY` or `CALBOOKER_RESEND_API_KEY`) is configured.
- A background maintenance worker runs scheduled cleanup jobs through `internal/service`; `cmd/web` starts it with the signal context so it stops on shutdown. Job status is live process state and never stores credentials.
- Retention for guests and usage events is configured through `CALBOOKER_GUEST_RETENTION` and `CALBOOKER_USAGE_RETENTION`; cleanup only removes expired, consumed, or over-age records.

## HTTP and security

- Generate server-side request IDs and return them in `X-Request-ID`.
- Use structured `slog` fields with stable, low-cardinality values.
- Never log or return hashes, session credentials, tokens, issuer subjects, database credentials, mail credentials, query strings, or other secrets.
- Do not log full URLs. Use safe method, status, duration, and request ID fields.
- Keep security events separately identifiable from ordinary application logs.
- Start one server span per request with stable, low-cardinality route names. Continue an incoming W3C trace context only from a configured trusted proxy, and keep query strings out of span names and attributes.
- Use `html/template` for user-controlled HTML output. Keep authorization, CSRF protection, and validation server-side.
- State-changing form and HTMX requests require a server-validated CSRF token; compare tokens in constant time.
- Protected routes must authenticate explicitly in their handlers.
- Revision-checked writes go through `ClassifyStaleWrite`/`IsWriteConflict`: a stale revision is a conflict, a vanished record is not-found. Do not show a conflict for a deleted record.
- HTMX fragments repeat authorization, include their own headings and alerts, and carry an out-of-band `#alerts` region. The layout configures `htmx.config.responseHandling` so `422` validation fragments swap; other `4xx`/`5xx` stay errors.
- Administrator routes pass through the shared admin authorization guard. Record every administrator action through the audit service with an actor snapshot, target, detail, time, and origin, mirror it to the security log stream, and never store credentials, session identifiers, or tokens.

## Tests and CI

Run before submitting changes:

```sh
make check
go test -race -shuffle=on ./...
go build ./...
```

Run `make test-integration` (needs `make dev-setup` and goweb's PostgreSQL) when touching persistence. Tests must cover success and failure paths, remain isolated and repeatable, and avoid secret fixtures. GitHub Actions repeats module verification, formatting, vet, race tests, integration tests against a throwaway PostgreSQL, and package builds, then publishes the image on master.

- Browser specs use `make e2e` (`CALBOOKER_SSO_E2E=1`) and need goweb's Hydra and provider plus calbooker running; never commit credentials, tokens, or bearer fixtures.

## Frontend

- Use DaisyUI 5 components for frontend UI instead of hand-rolled equivalents.
- Before writing HTML or JSX, load the `daisyui` skill and follow its component, usage, configuration, and color guidance.
- Keep templates semantic and accessible. Preserve stable accessible names and test hooks when adding controls.
- The Content-Security-Policy forbids inline scripts, inline styles, and `style` attributes. Put scripts in `static/app.js` and styles in `static/src/app.css`.
- Keep localized URLs explicit and catalog IDs stable; validate catalog completeness and placeholders in tests. Every new message needs both `en` and `hu` entries, in the same `<area>.json` file of each language.
- Anonymous theme preference may use browser storage; authenticated theme preference must persist through a server-side use case and revert UI state when rejected.

## Documentation

Read `docs/architecture.md`, `docs/dependency-policy.md`, `docs/local-development.md`, `docs/deployment.md`, `docs/first-admin.md`, `docs/backup-retention.md`, and `docs/threat-model.md` before changing related boundaries. Update documentation when commands, configuration, package responsibilities, deployment steps, or security controls change.
