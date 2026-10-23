# Deployment and Migration Runbook

Calbooker runs as one container next to the goweb single sign-on stack (Ory
Hydra and the goweb provider), which must be deployed first from the goweb
repository. The step-by-step, copy-paste runbooks are in `deploy/`:

- `deploy/PREPARE.txt`: image, Cloudflare DNS route on goweb's tunnel, Resend, sops recipients.
- `deploy/SETUP.txt`: any Linux server with Docker or Podman, PostgreSQL, and systemd.
- `deploy/SETUP-nix.txt`: NixOS with this repository's flake (`services.calbooker`) next to goweb's `services.goweb`.

This document explains what those steps rely on.

## Topology

| Host | Service | Loopback port |
| --- | --- | --- |
| `sso.<domain>` | Hydra public API (goweb) | 24444 |
| `auth.<domain>` | goweb provider | 28080 |
| `calbooker.<domain>` | calbooker | 28090 |

Containers use the host network and bind to loopback; Cloudflare Tunnel (or
any TLS-terminating proxy on the same host, `CALBOOKER_TRUSTED_PROXY=127.0.0.1`)
publishes them. Calbooker has its own PostgreSQL role and database
(`calbooker`), on the same server as goweb's.

## Required production configuration

Startup fails before the listener opens unless the configuration is complete
and safe:

- Always: `CALBOOKER_DATABASE_URL`, `CALBOOKER_SSO_ISSUER_URL`,
  `CALBOOKER_SSO_CLIENT_ID`, `CALBOOKER_SSO_CLIENT_SECRET` (`config.ErrMissingDatabase`,
  `config.ErrInvalidSSO`).
- Production (`config.ErrUnsafeProductionConfig`):
  - `CALBOOKER_SESSION_COOKIE_SECURE` is `true` and `CALBOOKER_PUBLIC_URL` uses `https`.
  - `CALBOOKER_SESSION_SECRET` is set (at least 32 characters).
  - `CALBOOKER_MAIL_RELAY` or `CALBOOKER_RESEND_API_KEY` is set (only one);
    with Resend, `CALBOOKER_MAIL_FROM` is on a domain verified in Resend.
  - The issuer, callback (`CALBOOKER_SSO_REDIRECT_URL`, derived as
    `<public URL>/en/oauth/sso/callback`), and account URLs use `https`.

## Registering calbooker in Hydra

`deploy/register-client.sh` (re)creates the OAuth2 client in the running
`goweb-hydra` container and prints `CALBOOKER_SSO_CLIENT_ID` and
`CALBOOKER_SSO_CLIENT_SECRET`. It registers:

- client name "Calbooker": the provider shows "Signing in to Calbooker"
- redirect URI `<public URL>/en/oauth/sso/callback`
- post-logout redirect URI `<public URL>/en/sign-in`
- back-channel logout URI `<public URL>/oauth/sso/backchannel-logout` with
  `backchannel_logout_session_required`; Hydra must be able to reach it (it
  refuses carrier-grade NAT addresses such as Tailscale's `100.64.0.0/10`)
- `skip_consent`, `skip_logout_consent`, `client_secret_basic`, scopes
  `openid email profile`

Re-running it rotates the secret; update the environment file and restart.

## Deployment order

1. The goweb stack is healthy (`https://sso.<domain>/.well-known/openid-configuration`, `https://auth.<domain>/healthz`).
2. Back up the calbooker database (from the second deploy on).
3. Pull the pinned image (`ghcr.io/tewecske/calbooker:<commit sha>`; CI publishes it on pushes to master).
4. Start the container; startup migrates the schema, then serves.
5. Check `GET /healthz` locally and through the tunnel, then sign in.

## Startup migrations

- Migrations live in `migrations/` with sortable `NNNNNN_description.up.sql`
  names. Calbooker's history starts at `000001`; it does not share goweb's.
- Startup records installed versions in `schema_migrations`, takes a
  transaction-scoped advisory lock, and applies every pending migration in a
  single transaction. A failure rolls the whole batch back.
- A renamed, renumbered, or removed installed migration is rejected with
  `store.ErrMigrationDrift`. Never edit an installed migration; add a new one.
- The runner is forward-only; there are no down migrations.

## Rollback limits

- Rolling back the image does not revert the schema. An image rollback is only
  safe when the previous binary tolerates the new schema (additive changes).
- To undo a destructive migration, restore the pre-deployment backup and
  redeploy the matching image.

## Backups and retention

See [backup and retention](backup-retention.md).

## Failure recovery

- **Invalid configuration**: the container exits with a `config` error naming
  the variables (never their values). Fix the environment file and restart.
- **Sign-in fails with a generic error**: check the client ID and secret match
  Hydra (re-register), and that the issuer URL is exactly Hydra's issuer.
- **Users stay signed in after signing out elsewhere**: Hydra cannot reach the
  back-channel URL; check the tunnel route and the registered URI.
- **Migration failure or drift**: read the `apply migration` error, ship a
  forward migration, and redeploy; restore the exact released file on drift.
- **Graceful shutdown**: `SIGTERM` drains in-flight requests, then closes the
  database.

## Quick reference

```sh
go build -o bin/calbooker ./cmd/web
pg_dump --format=custom "$CALBOOKER_DATABASE_URL" > calbooker-$(date +%Y%m%dT%H%M%S).dump
curl -fsS "$CALBOOKER_PUBLIC_URL/healthz"
```
