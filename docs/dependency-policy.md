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

## Vendored browser assets

Browser libraries are copied into `static/` at an exact version and served
from the application (no CDN, no npm runtime dependency for the app). Each
one records its source, checksum, and license here, and its license notice
appears on the about page. Download into a scratch directory, verify the
registry integrity, then copy only the distributed file.

| Asset | Version | Source | SHA-256 of the vendored file | License |
| --- | --- | --- | --- | --- |
| `static/sortable.min.js` (SortableJS, week grid drag and drop) | 1.15.7 | `npm pack sortablejs@1.15.7` (`https://registry.npmjs.org/sortablejs/-/sortablejs-1.15.7.tgz`, integrity `sha512-Kk8wLQPlS+yi1ZEf48a4+fzHa4yxjC30M/Sr2AnQu+f/MPwvvX9XjZ6OWejiz8crBsLwSq8GHqaxaET7u6ux0A==`), file `package/Sortable.min.js` | `bf4241bc73fef7f11c59a283a69fe8051cdd31c6d8ff5a2b9ba219e7831fcf76` | MIT |

SortableJS has no dependencies, sets styles only through the CSSOM (allowed
by the Content-Security-Policy), and does not use `eval`.
`static/static_test.go` checks the checksum, so an unreviewed change to the
file fails the tests. HTMX is copied from the pinned `htmx.org` npm package by
`npm run build:css`.

## Updates

Keep runtime dependencies on supported releases. Prefer patch updates, review transitive changes, rerun all checks, and record breaking upgrades in the change description.
