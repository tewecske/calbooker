# Dependency Policy

## Baseline

Runtime code uses the standard library plus the PostgreSQL driver (`github.com/jackc/pgx/v5`), inherited from the goweb template.

| Need | Baseline |
| --- | --- |
| HTTP server and routing | `net/http` |
| Server-rendered HTML | `html/template` |
| Embedded templates and assets | `embed` |
| Structured logs | `log/slog` |
| Database contracts | `database/sql` with the `pgx` stdlib driver |
| Cryptography and secure randomness | `crypto/*` |
| Tests and HTTP tests | `testing`, `net/http/httptest` |
| Browser interactions | HTMX assets, without a Go runtime dependency |

Browser acceptance tests use Playwright as development-only tooling because a
real browser engine is required to verify redirects, cookies, accessibility,
and full-page versus HTMX behavior. Pin `@playwright/test` to an exact npm
version, the one whose browser revisions match the Nix-provided
`PLAYWRIGHT_BROWSERS_PATH` (see `docs/local-development.md`), and keep browser
tests opt-in until a complete application fixture is available.

Use standard-library functionality when it meets requirements. Do not add a package only to shorten small amounts of code.

## Adding a dependency

Every third-party dependency requires:

1. A documented reason that the standard library cannot meet the requirement.
2. Maintainer, license, release, and known-vulnerability review.
3. Direct imports in the smallest possible package.
4. An exact module version selected through normal Go module resolution.
5. `go mod tidy`, `go mod verify`, `go test ./...`, and `go vet ./...` before merge.
6. `govulncheck ./...` before release and after security-relevant changes.

Do not use floating source URLs, vendored copies without review, or dependencies that expose credentials in logs, responses, traces, or diagnostics.

Commit `go.sum`.

## Updates

Keep runtime dependencies on supported releases. Prefer patch updates, review transitive changes, rerun all checks, and record breaking upgrades in the change description.
