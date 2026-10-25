# Delivery Plan

How the booking features get built. Product decisions and the data model are
in [design/booking.md](design/booking.md). Tasks are GitHub issues in
`tewecske/calbooker`, grouped into milestones M1 to M4.

## Roles

- **Manager**: the main Claude Code session started in this folder. It
  briefs subagents, reviews their PRs, merges them, and keeps this file and
  the issues current. It does not write feature code itself, except to
  resolve merge conflicts.
- **Subagents**: one per issue (or a short chain of issues in the same lane),
  each in its own git worktree (`Agent` with `isolation: "worktree"`). A
  subagent ends with a pushed branch and an open PR that says `Closes #N`.

The owner agreed (2026-10-23): GitHub issues and milestones, PRs reviewed and
merged by the manager once CI is green, and few parallel agents. Ask the owner
whenever a decision is not covered by the design doc. Ask rather than guess.

## Milestones and lanes

Issue numbers are the GitHub ones. "After" lists what must be merged first.

| # | Issue | Milestone | After | Lane |
| --- | --- | --- | --- | --- |
| 1 | Foundation for parallel work | M1 Foundation | | solo |
| 2 | Group roles, time zone, access, teams | M1 Foundation | 1 | solo |
| 3 | Invites, child profiles, guardians | M1 Foundation | 2 | solo |
| 4 | Resources | M2 Season setup | 3 | A |
| 5 | Seasons, parts, season coaches | M2 Season setup | 4 | A |
| 6 | Week grid component and SortableJS | M2 Season setup | 1 | B |
| 7 | Coach qualifications and availability | M2 Season setup | 5, 6 | B |
| 8 | Events | M3 Events | 5 | A |
| 9 | Sign-up and attendance | M3 Events | 8 | A |
| 10 | Weekly calendar views | M3 Events | 6, 8 | B |
| 11 | Participation statistics | M3 Events | 9 | A |
| 12 | Coach assignment board | M4 Season planning | 7 | C |
| 13 | Resource slot plan | M4 Season planning | 5, 6 | C |
| 14 | Training plan board | M4 Season planning | 12, 13 | C |
| 15 | Generate events from the plan | M4 Season planning | 8, 14 | C |
| 30 | Own answer on calendar entries | M3 Events | 10, 11 | A |

- M1 is one subagent at a time: everything else builds on it.
- Then at most three lanes run in parallel (A: data and events, B: grid and
  calendar UI, C: planning boards). Start a lane's next issue only when its
  "after" issues are merged.
- Lane B can start #6 as soon as #1 is merged, alongside #2 and #3.

## Working rules for every subagent

Put these in each brief together with the issue number:

1. Read `AGENTS.md`, `docs/design/booking.md`, and the issue. Load the
   `samber/cc-skills-golang@golang-how-to` skill before Go work and the
   `daisyui` skill before templates.
2. Branch `issue-<N>-<slug>` from the latest `master`.
3. Migrations: take the next free number on `master` when opening the PR. If
   another PR claimed it first, rename the file on rebase. Never edit a
   migration that is on `master`, except 000001 to 000003 inside issue #2
   (nothing is deployed yet). After a rename, drop and re-create the
   worktree's database.
4. Own files per area (see "Code areas" in the design doc). Keep edits to
   shared files minimal.
5. Every new message gets `en` and `hu` entries.
6. Tests: unit tests for services and handlers (success and failure,
   authorization included), integration tests (`-tags=integration`) for new
   store code, and a Playwright spec in `tests/browser/` for the main user
   path of the issue.
7. Before the PR: `make check`, `go test -race -shuffle=on ./...`,
   `go build ./...`, `make test-integration`, and the issue's Playwright spec.
   Paste the result summary into the PR.
8. Update docs touched by the change (README configuration table,
   `docs/architecture.md`, `docs/threat-model.md`, this file if the plan
   changes).
9. Do not merge. Do not push to `master`. Do not change anything outside the
   worktree except the shared goweb containers described below.
10. Stop only the processes you started, by PID or process group. Never
    `pkill -f "go run ./cmd/web"` or similar: it also kills goweb's provider.

## Local environment

Each worktree runs its own calbooker on its own port, database, and Hydra
client, so lanes can run browser tests at the same time:

| Lane | Port | Database | Hydra client | Back-channel relay |
| --- | --- | --- | --- | --- |
| solo / A | 8083 | `calbooker_a` | `calbooker-dev-a` | 4448 |
| B | 8084 | `calbooker_b` | `calbooker-dev-b` | 4449 |
| C | 8085 | `calbooker_c` | `calbooker-dev-c` | 4450 |

The main checkout keeps 8082, `calbooker`, `calbooker-dev`, relay 4447. The
owner's goweb `.env` uses the tailnet layout (`SSO_DEV_BIND` is a tailnet
address), so `dev-setup` starts a back-channel relay per checkout: its port
comes from `CALBOOKER_DEV_RELAY_PORT` and its container name from the client
ID (`<client ID>-backchannel-relay`), so the checkouts run side by side.

In a worktree:

```sh
cp .env.example .env
CALBOOKER_DEV_URL=http://omnissiah.tail17501b.ts.net:8083 CALBOOKER_DEV_DATABASE=calbooker_a \
  CALBOOKER_DEV_CLIENT_ID=calbooker-dev-a CALBOOKER_DEV_RELAY_PORT=4448 \
  make dev-setup GOWEB_DIR=/home/tewe/projects/golang-spikes/goweb
make run GOWEB_DIR=/home/tewe/projects/golang-spikes/goweb
make e2e   # reads the base URL from .env.local
```

These need goweb's stack (`make db-up`, `make sso-up`, `make run-provider` in
`../goweb`). That is outside this folder: the manager asks the owner to start
it, or gets an explicit yes to start it, before the first subagent needs it.
When the goweb `.env` uses the tailnet layout, `dev-setup` derives the host
from it; check its printed `.env.local` values.

Browser specs take full users from the account pool (`poolUser`, issue #21),
and goweb's `make run-provider` raises the provider's sign-in/sign-up limit to
600 per minute (`GOWEB_AUTH_RATE_LIMIT`, goweb PR #141), so lanes can run
`make e2e` side by side.

Playwright browsers come from Nix: home-manager sets
`PLAYWRIGHT_BROWSERS_PATH` from nixpkgs `playwright-driver`, and
`@playwright/test` must match the browser revisions in
`$PLAYWRIGHT_BROWSERS_PATH`, not the driver's version number. On 2026-10-23
the directory held chromium-1243, chromium_headless_shell-1243, firefox-1543,
and webkit-2359, which is `@playwright/test` 1.63.0 (1.64.x expects
chromium-1248), so `package.json` pins 1.63.0. How to re-check after a Nix
update is in `docs/local-development.md` ("Playwright browsers"). Install npm
packages with `PLAYWRIGHT_SKIP_BROWSER_DOWNLOAD=1 npm ci` and never run
`npx playwright install`.

## Review checklist for the manager

- CI green; the PR lists the local checks including the Playwright spec.
- Group scoping: every query filters by group; non-members get 404; a test
  proves it.
- Authorization goes through `GroupAccess`; coach and parent limits tested.
- No secrets, tokens, or emails in logs; admin actions audited.
- Migration number is the next free one; no edits to merged migrations.
- en and hu catalog entries present.
- Merge with a merge commit or squash (keep the `Closes #N`), then update the
  status column below and tell lanes waiting on it to rebase.

## Next step

Progress is in the Status table below. The original first steps were:

1. ~~Ask the owner the open questions~~ (answered, see below).
2. Ask the owner to start goweb's stack, or for an explicit yes to start it.
3. Done: #1 merged as #16 on 2026-10-24.
   Brief one subagent for #1 (`isolation: "worktree"`, lane solo/A
   environment), review and merge its PR.
4. Then #2 and #6 in parallel (#6 in lane B), then #3, then lanes A, B, and C
   as the table allows.

## Status

| # | State | PR |
| --- | --- | --- |
| 1 | merged | #16 |
| 2 | merged | #18 |
| 3 | merged | #19 |
| 4 | merged | #20 |
| 5 | merged | #23 |
| 7 | merged | #25 |
| 10 | merged | #28 |
| 8 | merged | #24 |
| 9 | merged | #27 |
| 13 | merged | #26 |
| 12 | merged | #29 |
| 21 | merged (browser account pool, rate-limit backoff) | #22 |
| 6 | merged | #17 |
| 11 | merged | #31 |
| 30 | merged | #32 |
| 14 | merged | #33 |
| 15 | in progress (lane C) | |

## Owner answers (2026-10-23)

- Optional participant limit and sign-up deadline per event: yes (#9; the
  columns go into `events` in #8).
- Coach qualification is an age range in years: confirmed (#7).
- Coaches see the season plan read only (#12, #14). They influence it only by
  changing their availability; the plan does not update itself, an admin or
  manager re-runs the suggestion and regenerates events if they want to.
