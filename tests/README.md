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

`browser/sso.spec.ts` drives calbooker, the goweb provider, and Hydra in a
real browser. It is opt-in with `CALBOOKER_SSO_E2E=1` (`make e2e`) and needs
the local stack from `docs/local-development.md`. It creates its own accounts
at the provider and never uses committed credentials.
