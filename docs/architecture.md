# Architecture

Package responsibilities are listed in `AGENTS.md`; the booking design is in
`design/booking.md`. This file describes shared building blocks that several
features use.

## Week grid

A server-rendered calendar week used by coach availability (#7), the weekly
calendars (#10), the resource slot plan (#13), and the training plan board
(#14). Code: `internal/adapter/http/weekgrid.go` (view model, layout, change
parsing), `templates/weekgrid.html` (the `weekgrid` template), the week grid
section of `static/src/app.css`, the week grid block of `static/app.js`, and
messages in `internal/locale/catalogs/<lang>/weekgrid.json`.

### Feeding it

A feature handler describes the frame and the items; it never deals with
rows, lanes, or CSS:

```go
grid, err := BuildWeekGrid(WeekGridConfig{
	ID:          "plan-grid",          // DOM id, [a-z][a-z0-9-]*
	StartMinute: 7 * 60,               // visible range, multiples of Step
	EndMinute:   21 * 60,
	Step:        15,                   // 5, 10, 15, 20, 30, or 60; at most 96 rows
	Weekdays:    nil,                  // ISO weekdays to show; nil = Monday..Sunday
	Resources:   []WeekGridResource{{Key: "7", Label: "Court 1"}}, // optional columns per day
	Week:        "A",                  // alternating plans: "A" or "B"
	WeekAURL:    urlA, WeekBURL: urlB, // show the A/B switch
	FirstDay:    monday,               // dated views: day headers show dates
	PreviousURL: prev, NextURL: next,  // dated views: week navigation
	MoveURL:     moveURL,              // enables moving and resizing
	CreateURL:   createURL,            // enables adding (click an empty slot)
	CSRFToken:   token,                // for the keyboard forms without HTMX
}, items, renderer.WeekGridTranslator(language))
```

Each `WeekGridItem` has an opaque `ID`, `Weekday` (1 = Monday),
`StartMinute`/`EndMinute` (minutes after local midnight, as in the data
model), optional `Resource` (a resource key), `Label`, optional `Detail`,
`Variant` (`neutral`, `primary`, `secondary`, `accent`, `info`, `success`,
`warning`, `error`), `Draggable`, and `Warning` with optional `WarningText`.
Render the result with `{{template "weekgrid" .Grid}}`.

### Layout

Each day (or resource inside a day) is a CSS grid column with one row per
step. Items occupy every step slot they touch, clipped to the visible range
(clipped items are flagged and marked). Items whose rows overlap, directly or
through a chain, form a cluster; the cluster gets as many lanes as it needs
and each item takes the first lane free at its start row, so items that only
touch share a lane. Lanes divide twelve sub-columns; at most 12 lanes show,
further items share the last one. Items outside the shown days, resources, or
hours are listed under the grid instead of disappearing.

The Content-Security-Policy forbids `style` attributes, so placement uses
classes (`wg-row-N`, `wg-rspan-N`, `wg-col-N`, `wg-cspan-N`, `wg-rows-N`,
`wg-cols-N`, `wg-step-N`, `wg-hour-at-N`) that `static/src/app.css` generates
with Tailwind `@utility` and `@source inline`. A test checks that every class
the view model can emit exists in the built stylesheet.

### Changes

The browser only proposes changes. Dropping an item (SortableJS, also on
touch after a short hold), dragging its bottom edge, clicking an empty slot,
or submitting the keyboard form posts `item`, `weekday`, `start`, `end`,
`resource`, and `week` with HTMX and the CSRF header. The handler calls
`ParseWeekGridChange(request, config, requireItem)`, which validates the
values against the grid the user saw and snaps times to the step, then
authorizes and applies the change and answers with the re-rendered grid
(outer swap of the grid element) plus out-of-band alerts. A rejected change
answers 422 with the unchanged grid and an error alert; a plain form post
redirects back (303).

### Demo

`/{language}/dev/weekgrid` (`weekgrid_demo.go`) exercises every variant with
in-memory items. It is registered only when `Dependencies.DevelopmentRoutes`
is true, which `cmd/web` sets from `Config.DevelopmentRoutes()` (only
`CALBOOKER_ENV=development`).

## Group access

Every group-scoped page and use case is authorized by one type,
`service.GroupAccess` (`internal/service/group_access.go`). It is built per
request by `GroupAccessService.For(ctx, userID, groupID)` from the member's
roles (`group_member_roles`), own teams (`team_members`), and two extension
sources. Accounts that may not see the group, and groups that do not exist,
get `service.ErrGroupNotFound`, which handlers render as 404.

- Handlers call `groupRequest` (`internal/adapter/http/group.go`), which
  authenticates, builds the access, and answers 404 to outsiders. They then
  ask the access questions only to decide what to render.
- Use cases take the access (`*GroupAccess`) or a `GroupActor` (access plus
  the client origin for audit) and check it again: not allowed is
  `ErrGroupForbidden` (403; HTMX 422), not visible is not-found (404).
- Questions: `CanView`, `IsMember`, `IsSiteAdmin`, `HasRole`, `Roles`,
  `CanManageGroup` (admin, manager, site administrator), `CanManageRole(role)`
  (only admins handle `admin`), `CanRemoveMember`,
  `CanViewAllMembers`, `CanCreateTeam`, `CanEditTeam(id)`,
  `CanManageRoster(id)` (managers and the team's coaches), `CoachesTeam(id)`,
  `SeesAllTeams`, `VisibleTeamIDs()`, `CanViewTeam(id)`,
  `CanViewTeamEmails(id)`, `CanManageInvites` (admins, managers, coaches),
  `CanCreateInvite(role, teamID)` (also decides which invites an account sees
  and revokes), `ChildUserIDs()`, `IsGuardianOf(id)`, `CanCreateProfile`
  (staff and parents), `CanEditProfile(id)` (staff and the child's
  guardians), `CanManageGuardians` (staff), `CanViewChildren`,
  `CanManageResources` (admins, managers, site administrators),
  `CanViewResources` (those plus coaches), `CanManageSeasons` (admins,
  managers, site administrators), `CanViewSeasons` (those plus coaches). Add
  a question here rather than checking roles in a handler.
- Extension points: `GroupAccessService.SetCoachedTeams(source)` takes a
  `TeamRelationSource.TeamIDs(ctx, groupID, userID)`; `service.SeasonService`
  implements it from `season_team_coaches` of the group's current season and
  `internal/app` wires it. Coached teams count only for members with the
  coach role, so the source is asked only for them. `SetChildTeams(source)`
  takes a `ChildRelationSource` (`ChildUserIDs` plus `TeamIDs`, the teams on
  which the member's children play); `postgres.ProfileRepository` implements
  it from `guardians` and `internal/app` wires it.

## Invites, child profiles, and guardians

- `InviteService` (`internal/service/invite.go`, store
  `internal/store/postgres/invites.go`) owns `group_invites`: each code carries
  a role and optionally a team (player and parent only), an expiry, and a use
  limit. `Create(actor, InviteInput)`, `List(access)`, `Revoke(actor, id)`,
  and `Join(userID, code)` (wrapped by `RateLimitedGroupJoiner`) return
  `InviteRedemption{Membership, TeamID, Changed}`. `RedeemInvite` locks the
  invite row, re-checks it, and adds the membership, the role, and the roster
  entry in one transaction; a use counts only when something was added.
  Joining as an existing member adds the role and team. The group's single
  `groups.invite_code` was dropped in migration 000004; existing codes became
  player invites.
- `ProfileService` (`internal/service/profile.go`, store
  `internal/store/postgres/profiles.go`) owns child profiles (`users.kind =
  'profile'` plus `child_profiles`: owning group, birth year, revision) and
  `guardians` (parent, child, group). A profile is a group member with the
  player role only. A guardian link needs the parent role (foreign key to
  `group_member_roles`) and goes with it.
- Linked parents are derived, not stored: `TeamRepository.ListRoster` adds the
  guardians of the team's players as parent entries (`Linked`, with
  `ChildUserIDs`), and `ListTeams` counts each parent once. `team_members`
  stays the record of explicit roster entries, so its foreign key to the
  member's roles is unchanged.
- Pages: `/{language}/groups/{id}/invites` and `/{language}/groups/{id}/children`
  (`internal/adapter/http/invite.go`, `children.go`; templates
  `invites.html`, `children.html`; catalogs `invites.json`, `children.json`).
- Roles: `admin`, `manager`, `coach` need a full account
  (`users.kind = 'account'`); `player` and `parent` may be guests. A team
  roster entry (`player` or `parent`) needs the matching group role; the
  foreign key removes it when the role goes.
- Every admin and manager action in a group is audited by the group and team
  services with the group as target (`group.*` action codes).

## Resources

- `ResourceService` (`internal/service/resource.go`, store
  `internal/store/postgres/resources.go`, migration 000005) owns `resources`:
  name, kind (a stable code from `service.ResourceKinds()`: `field`, `court`,
  `hall`, `pool`, `room`, `other`, labelled per language as
  `resources.kind.<code>`), optional description, optional
  `max_concurrent_teams` (1 to 99, empty is no limit), `archived_at`, and
  `version` for revision-checked edits.
- Use cases: `List(access)` (all, active first) and `ActiveResources(access)`
  (not archived, for pickers) need `CanViewResources`; `Find(access, id)`
  loads any resource of the group, archived included, for every account that
  sees the group, so plans and events can show what they reference;
  `Usable(access, id)` is `Find` plus `ErrResourceArchived` for archived
  resources, for validating new plan slots and events; `Create`, `Update`, and
  `SetArchived` (archive and restore) need `CanManageResources` and are
  audited as `group.resource.created`, `.updated`, `.archived`, `.restored`.
- A name is unique per group among non-archived resources, ignoring case
  (partial unique index `resources_active_name_unique`,
  `ErrResourceNameTaken`); restoring fails while an active resource holds the
  name.
- Later tables reference a resource as `(group_id, resource_id)` with a
  composite foreign key to `resources (group_id, id)`
  (`resources_group_id_unique`), so a reference never crosses groups.
- Pages: `/{language}/groups/{id}/resources` and
  `/{language}/groups/{id}/resources/{resourceID}` (`resource.go`,
  `resources.html`, catalogs `resources.json`). Coaches see them read only;
  players and parents get access denied and no navigation entry.

## Seasons

- `SeasonService` (`internal/service/season.go`, store
  `internal/store/postgres/seasons.go`, migration 000006) owns `seasons`
  (name, `starts_on`, `ends_on` as `date` in the group's calendar, both ends
  included, `version`), `season_parts` (position 1 or 2, name, dates,
  `alternating_weeks`), and `season_team_coaches` (season, team, coach,
  `is_head`).
- Validation (`buildSeason`): a name, a first day before the last day, one
  or two named parts, each inside the season with its first day before its
  last, part 2 starting after part 1 ends. The database repeats what it can:
  an exclusion constraint keeps a group's seasons from overlapping
  (`seasons_no_overlap`, `ErrSeasonOverlap`; range types, no extension), a
  deferred one keeps parts apart, names are unique per group ignoring case.
- Dates are `time.Time` at midnight UTC holding the calendar date
  (`SeasonDate`, `ParseSeasonDate`, `FormatSeasonDate`); `LocalDate(instant,
  zone)` gives the group's day. The current season contains today in the
  group's time zone; the service takes an injected clock, so tests pin the
  instant across zones.
- Use cases: `List(access)` and `SeasonCoaches(access, seasonID)` need
  `CanViewSeasons`; `Find(access, id)`, `FindPart(access, partID)` (season
  and part), `Current(access)` (season, found), and `Today(access)` work for
  every account that sees the group, so later features can name and resolve
  seasons and parts; `TeamCoaching(access, teamID)` backs the team page (every
  season for those who see seasons, the current season's coaches for
  players and parents, emails only where `CanViewTeamEmails`); `Create`,
  `Update` (season and parts together at the season's revision; parts keep
  their IDs by position, dropping part 2 deletes it), `Delete`,
  `AssignCoach(CoachAssignment)` (add, or change the head flag), and
  `RemoveCoach` need `CanManageSeasons` and are audited as
  `group.season.created`, `.updated`, `.deleted`, `.coach_assigned`,
  `.coach_removed`. `Season.PartOn(day)`, `Season.Part(id)`, and
  `SeasonPart.WeekOn(day)` (`A`/`B` for alternating parts, week 1 being the
  Monday-based week holding the part's first day; `all` otherwise) are for
  the weekly plans.
- A coach assignment references the member's coach role by foreign key
  (`(group_id, coach_user_id, coach_role)` to `group_member_roles`), so
  removing the role or the member removes the assignments; a partial unique
  index allows one head coach per team and season (marking a new one
  unmarks the old one in the same transaction).
- Later tables reference a season as `(group_id, season_id)` to
  `seasons (group_id, id)` and a part as `(group_id, season_part_id)` to
  `season_parts (group_id, id)`, so a reference never crosses groups.
- Pages: `/{language}/groups/{id}/seasons` and
  `/{language}/groups/{id}/seasons/{seasonID}` (`season.go`, `seasons.html`,
  catalogs `seasons.json`), plus the coaches card on the team page
  (`POST .../teams/{teamID}/coaches/add`, `/head`, `/remove`). Coaches see the
  season pages read only; players and parents get access denied and no
  navigation entry.
