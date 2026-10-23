# calbooker

Server-rendered Go and HTMX calendar booking app. Sign-in goes through the
goweb single sign-on stack (Ory Hydra and the goweb provider); calbooker keeps
no passwords. Started from the goweb template's client app; see
[docs/handoff.md](docs/handoff.md) for what was carried over and what comes
next.

## Requirements

- Go 1.26.x
- A goweb checkout next to this one (`../goweb`) for local PostgreSQL, Hydra, and the provider

## Run locally

```sh
# in ../goweb: make db-up && make sso-up && make run-provider
cp .env.example .env
make dev-setup     # calbooker database + Hydra client, writes .env.local
make run           # http://127.0.0.1:8082
```

Full walkthrough: [local development](docs/local-development.md).
Health check: `GET /healthz`.

## Configuration

Configuration comes from environment variables (`internal/config`):

| Variable | Default |
| --- | --- |
| `CALBOOKER_ENV` | `development` (`development`, `test`, `production`) |
| `CALBOOKER_HTTP_ADDR` | `127.0.0.1:8082` |
| `CALBOOKER_PUBLIC_URL` | `http://127.0.0.1:8082` |
| `CALBOOKER_DATABASE_URL` | required; PostgreSQL URL of calbooker's own database |
| `CALBOOKER_SSO_ISSUER_URL` | required; Hydra's public URL (OpenID Connect issuer). HTTPS in production |
| `CALBOOKER_SSO_CLIENT_ID` | required; client ID registered in Hydra |
| `CALBOOKER_SSO_CLIENT_SECRET` | required; client secret registered in Hydra |
| `CALBOOKER_SSO_REDIRECT_URL` | derived as `<CALBOOKER_PUBLIC_URL>/en/oauth/sso/callback` |
| `CALBOOKER_SSO_ACCOUNT_URL` | unset; the provider's account settings page linked from settings |
| `CALBOOKER_SESSION_COOKIE_SECURE` | `true` in production or when the public URL uses HTTPS, otherwise `false` |
| `CALBOOKER_SESSION_SECRET` | unset; at least 32 characters, required in production |
| `CALBOOKER_SESSION_LIFETIME` | `24h` (deployments use `8h`) |
| `CALBOOKER_GUEST_RETENTION` | `720h` (30 days) |
| `CALBOOKER_USAGE_RETENTION` | `2160h` (90 days) |
| `CALBOOKER_MAIL_RELAY` | unset; credential-free `smtp://` or `smtps://` relay. Production needs this or Resend; development keeps mail in memory |
| `CALBOOKER_RESEND_API_KEY` | unset; delivers mail through the Resend HTTP API instead (set only one). Production needs `CALBOOKER_MAIL_FROM` on a domain verified in Resend |
| `CALBOOKER_MAIL_FROM` | `no-reply@` plus the public URL host |
| `CALBOOKER_TRUSTED_PROXY` | unset; trusted proxy IP or CIDR, so rate limits and audit records use the client IP from `X-Forwarded-For` |

Secret values are redacted from formatting and JSON diagnostics.

## Verify

```sh
make check
go test -race -shuffle=on ./...
make test-integration   # against the local database
```

## Deploy

Calbooker deploys next to an existing goweb deployment: copy-paste runbooks in
[deploy/](deploy/) (`PREPARE.txt`, then `SETUP.txt` or `SETUP-nix.txt`), the
NixOS module in [nix/module.nix](nix/module.nix) (`services.calbooker`), and
the background in [docs/deployment.md](docs/deployment.md).

## Documentation

[Architecture](docs/architecture.md), [dependency policy](docs/dependency-policy.md),
[local development](docs/local-development.md), [deployment](docs/deployment.md),
[first administrator](docs/first-admin.md), [backup and retention](docs/backup-retention.md),
[threat model](docs/threat-model.md), [handoff notes](docs/handoff.md).

## License

GNU Affero General Public License v3.0; see [LICENSE](LICENSE).

## Credits

Theme icons use [Heroicons](https://github.com/tailwindlabs/heroicons) by
[Tailwind Labs](https://tailwindlabs.com), licensed under the
[MIT License](https://github.com/tailwindlabs/heroicons/blob/master/LICENSE).

Copyright (c) Tailwind Labs, Inc. The full license text is reproduced on the
application about page.
