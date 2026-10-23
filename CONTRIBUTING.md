# Contributing

Read [local development setup](docs/local-development.md) before changing code.

## Workflow

1. Create a focused branch from `master`.
2. Keep changes within one ticket when possible.
3. Add success and failure tests for behavior changes.
4. Run `make check` and `go test -race ./...`.
5. Use Conventional Commits with an imperative subject.
6. Open a pull request with behavior, tests, and known limitations.

## Boundaries

- Keep domain code independent from HTTP, storage, and third-party packages.
- Wire dependencies explicitly in `cmd/web`.
- Validate external input at server boundaries.
- Never commit credentials, tokens, private keys, or secret fixtures.
- Keep request logs, responses, traces, and diagnostics free of secrets.

## Dependencies

Use standard-library packages unless a dependency is necessary. Follow
[dependency policy](docs/dependency-policy.md) before adding one.
