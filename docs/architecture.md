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
Read-only grids may also set `URL` (the label becomes a link covering the
item), `Muted` (faded, label struck through, for example a cancelled event),
and `Badges` (short texts shown as small badges); all three are optional and
default to the plain item.
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

## Readable URLs

Groups and teams are addressed by name, not by ID: `/{language}/groups/{group}`
and `/{language}/groups/{group}/teams/{team}`, for example
`/en/groups/bvsc/teams/u10`. `{group}` is a group slug (unique site-wide) and
`{team}` a team slug (unique within its group). Slugs are lowercase ASCII with
hyphens, never only digits, and never a reserved route word (`new`, `edit`,
`settings`, `members`, ...); `internal/service/slug.go` generates and validates
them in one place, migration `000013` backfilled existing rows with the same
rules. Staff edit them on the group settings and team pages.

Every group route resolves `{group}` once in `groupHandler.groupRequest`
(`GroupService.BySlug`, then `GroupAccess` as before, so unknown slugs and
non-members both get 404), and team routes resolve `{team}` in
`teamPathRequest` (`TeamService.BySlug`, scoped to the group). Links to teams
known only by ID go through `groupHandler.teamPath`. The old numeric URLs
(`/groups/3`, `/teams/4`) answer GET with a 301 to the slug URL, after the
access check so outsiders learn nothing; POSTs to old URLs are 404. Tracing
and usage labels use the route pattern (`{group}`, `{team}`), never the slug.

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
  managers, site administrators), `CanViewSeasons` (those plus coaches),
  `CanCreateEvents` (staff, and coaches with a team this season),
  `CanCreateEvent(teamIDs)` (staff; coaches only for teams they all coach),
  `CanEditEvent(event)` (the same for the event's teams), `CanViewEvent(event)`
  (the Visibility rule plus the event's extra coaches),
  `CanViewCoaches` (staff and coaches), `CanManageQualifications` (staff),
  `CanViewQualification(id)`, `CanViewAvailability(id)`, and
  `CanEditAvailability(id)` (staff, and the coach themself),
  `CanViewAllAvailability` (staff), `CanManagePlan` (admins, managers, site
  administrators), `CanViewPlan` (those plus coaches),
  `CanRespondFor(event, userID)` (a player for themself, a linked parent for
  a child), `CanManageParticipants(event, teamCoachIDs)` (staff and the
  event's coaches), `CanNominate`, and `CanRecordAttendance`. Add a question
  here rather than checking roles in a handler.
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
- Pages: `/{language}/groups/{group}/invites` and `/{language}/groups/{group}/children`
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
  name, kind (a per-group `resource_kinds` row, #43), optional description, optional
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
- Pages: `/{language}/groups/{group}/resources` and
  `/{language}/groups/{group}/resources/{resourceID}` (`resource.go`,
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
- Pages: `/{language}/groups/{group}/seasons` and
  `/{language}/groups/{group}/seasons/{seasonID}` (`season.go`, `seasons.html`,
  catalogs `seasons.json`), plus the coaches card on the team page
  (`POST .../teams/{team}/coaches/add`, `/head`, `/remove`). Coaches see the
  season pages read only; players and parents get access denied and no
  navigation entry.

## Events

- `EventService` (`internal/service/event.go`, store
  `internal/store/postgres/events.go`, migration 000007) owns `events` (a per-group
  `event_kinds` row, title, `starts_at`/`ends_at` as
  `timestamptz`, exactly one of `resource_id` and a free-text `location`,
  `signup_mode` `open`/`nominated`, optional `max_participants` 1 to 999 and
  `signup_deadline` before the start, `cancelled_at`, `plan_training_id`
  without a foreign key until #14, `generated_for_week` (a Monday),
  `edited_at`, `version`), `event_teams` (one or more), and `event_coaches`
  (extra coaches on top of the teams' season coaches). The version covers the
  event with its teams and coaches.
- References stay inside the group: `(group_id, resource_id)` to
  `resources (group_id, id)`, `(group_id, team_id)` to `teams`, and
  `(group_id, coach_user_id, 'coach')` to `group_member_roles` (losing the
  coach role removes the extra coach entries). Later tables (#9, #11, #15)
  reference an event as `(group_id, event_id)` to `events (group_id, id)`.
- Times: the form takes wall-clock times (`<input type="datetime-local">`)
  in the group's time zone; `EventInstant(wall, zone)` converts them and
  `EventLocalTime(instant, zone)` converts back. A time skipped by a
  daylight-saving change (02:00 to 02:59 on the last Sunday of March in
  Europe/Budapest) is refused (`ErrEventTimeSkipped` in an `EventFieldError`
  naming the field); a time that happens twice (the same hour in October) is
  its first occurrence, still on summer time. Events keep their wall-clock
  times, so an event across a change lasts an hour less or more. An event lasts
  at most 31 days.
- Use cases: `List(access, EventQuery{From, To, TeamID, Descending, Limit})`
  and `ListBetween(access, from, to)` (events overlapping `[from, to)`,
  cancelled ones included, only those the account sees; for the weekly
  calendars of #10), `Find(access, id)` and `Detail(access, id)` (event,
  group time zone, the teams' season coaches in the season containing the
  event's first day, and warnings for accounts that edit it; invisible events
  are `ErrEventNotFound`, 404), `FormOptions(access, event)` (teams the account
  may use, active resources, coach-role members), `Create`, `Update` (at the
  revision; returns warnings), `SetCancelled` (cancel or reinstate; a
  cancelled event stays and is shown), and `Delete`, audited as
  `group.event.created`, `.updated`, `.cancelled`, `.restored`, `.deleted`
  (detail: ID, title, kind, UTC times, team IDs, resource ID or "location",
  extra coach IDs; never the location text). Editing or cancelling a
  generated event sets `edited_at`, which regeneration (#15) keeps.
- Warnings never block a write: the resource is over its
  `max_concurrent_teams` when the most teams on it at one moment of the event
  (its own plus overlapping non-cancelled events on the resource) exceed the
  limit; a coach is busy when one of the event's coaches (extra or season
  coach of its teams) also coaches an overlapping non-cancelled event.
  `Create` and `Update` return them; the event page shows them to accounts
  that may edit the event.
- Pages: `/{language}/groups/{group}/events` (upcoming, `?when=past`,
  `?team=<id>`; the create form for those who create events) and
  `/{language}/groups/{group}/events/{eventID}` with `POST .../cancel` and
  `.../delete` (`event.go`, `events.html`, catalogs `events.json`). Every
  member has the Events tab and sees the events visible to them.
- Deleting an event with participant rows is refused
  (`ErrEventHasParticipants`); `DeleteEvent` locks the event row and checks
  in the same transaction.

## Sign-up and attendance

- `ParticipationService` (`internal/service/participation.go`, store
  `internal/store/postgres/participants.go`, migration 000010) owns
  `event_participants (group_id, event_id, user_id, status, attended,
  updated_by, updated_at)`: status `signed_up`, `declined`, `nominated`, or
  NULL for a walk-in; `attended` NULL until confirmed. The event is
  referenced as `(group_id, event_id)` to `events` and the member as
  `(group_id, user_id)` to `group_members`, both cascading.
- Use cases: `Roster(access, EventDetail)` (the event's team players plus
  everyone with a row, each with the answers the account may give now,
  `Taken`/`Limit`, the phase `open`/`closed`/`started`/`ended`/`cancelled`,
  emails only for those who manage participants, and walk-in candidates once
  attendance is open); `Respond(actor, eventID, userID, status)` (status ""
  removes; rules in `checkParticipation`); `RecordAttendance(actor, eventID,
  marks)`; `ConfirmedAttendance(access, from, to)` for statistics (#11):
  `[]AttendanceRecord{EventID, TeamIDs, UserID, StartsAt}` of non-cancelled
  events starting in `[from, to)`, all for staff, otherwise only events of
  the teams the account sees.
- Call-ups (#47, migration 000015): `CallUp(actor, eventID, userID)` writes a
  row with `called_up` (status `nominated`, or `declined` after an opt-out)
  for a group player outside the event's teams, and returns the player's
  overlapping events as a warning (`ListOverlaps`). Status `left_out` leaves a
  team player out (`Respond`). `GroupAccessService` loads the called-up event
  IDs of the member and their children (`CallUpSource`, implemented by the
  participant store) so `CanViewEvent` and the event list scope
  (`EventScope.EventIDs`) include them; a call-up never grants team access.
  Statistics add called-up events to the player's figures and drop left-out
  ones. Rules and differences from nominations: `docs/design/booking.md`,
  "Refinements made in #47". The team page invite card is
  `internal/adapter/http/team_invites.go`.
- Concurrency: `ChangeParticipant` and `RecordAttendance` lock the event row
  (`SELECT ... FOR UPDATE`), read the member's row and the places taken, and
  let the service decide (`ParticipantDecision`) inside the transaction, so
  two players cannot both take the last place.
- Pages: the participants card on the event page (`templates/participants.html`,
  catalogs `participation.json`) with `POST
  /{language}/groups/{group}/events/{eventID}/participation` (`user_id`,
  `status`: an answer or `none`) and `.../attendance` (`listed`, `attended`,
  optional `walk_in`) in `internal/adapter/http/participation.go`. Both
  re-render the event page (HTMX: the fragment with out-of-band alerts;
  refusals 422).

## Team page tabs

- `/{language}/groups/{group}/teams/{team}` (Roster), `.../events` and
  `.../calendar` (`internal/adapter/http/team_tabs.go`, `templates/teams.html`
  `team-tabs`). Events and Calendar reuse `loadEventList` and
  `loadCalendarScope` with the team fixed, so visibility is the same
  `GroupAccess` rules; `POST .../teams/{team}/events` creates an event through
  the normal event use case and redirects to the event page. Every tab resolves
  the team with `TeamService.BySlug` first (invisible team: 404).

## Start page and role navigation

- `landingRoute` (`internal/adapter/http/landing.go`) decides where the SSO
  callback sends the browser: one active team, else a player or parent's
  group calendar, else the group page, else home. `landingFor` collects the
  account's groups and teams through `GroupAccess` and falls back to home on
  any failure; only members count a group.
- The group menu (`groupShell`) is built per role: accounts with only the
  player and parent roles (`GroupAccess.IsFamilyOnly`) get Calendar, Teams
  (the team's name and a direct link when they have exactly one team) and
  Events, without Overview, Members or Children. The Children entry is gone
  for everyone; its routes remain.

## Calendar

- `/{language}/groups/{group}/calendar?week=YYYY-MM-DD&team=&resource=&kind=`
  (`internal/adapter/http/calendar.go`, `templates/calendar.html`, catalogs
  `calendar.json`) shows one Monday-based week of events in the group's time
  zone. `week` is any day of the wanted week (missing or invalid: the current
  week). Every member has the Calendar tab.
- Data comes only from `EventService.List(access, EventQuery{From, To})`
  (the same as `ListBetween`), so visibility is the Visibility rule of
  `GroupAccess.CanViewEvent`; cancelled events are included and shown muted
  with a badge. Filters (`service.CalendarFilter`: team, resource, kind) are
  applied to that list and never widen it. The team filter offers the teams
  the account sees (`TeamService.List`); the resource filter offers the
  group's resources to accounts with `CanViewResources` and, for everyone,
  the resources named on the week's visible events. A requested team or
  resource that is not offered is dropped.
- Pure rules in `internal/service/calendar.go`: `NewCalendarWeek(day, zone)`
  (Monday, and `From`/`To` as local midnight on Monday and the next Monday,
  so a week across a clock change is 167 or 169 hours), `CalendarToday`,
  `ParseCalendarDate` (years 1970 to 2200), `CalendarWeek.Segments(event)`
  (one segment per local day in wall-clock minutes; events across midnight
  or longer than a day are split per day and flagged as continuing; an event
  at 01:30 on the night the clocks go back starts at minute 90),
  `FilterCalendarEvents`, and `CalendarEventResources`.
- The week grid shows 08:00 to 20:00, widened to whole hours so every
  segment fits, in 30-minute rows; items are colored by kind and link to the
  event page. Below the `md` breakpoint a list of days replaces the grid.
  Links and the filter form inside `#calendar` are boosted (`hx-boost`,
  target `#page-content`, URL pushed); without JavaScript they are plain
  links and a GET form. The group switcher lists the account's groups when
  there are several, keeping the week.
- `KindService` (`internal/service/kind.go`, store
  `internal/store/postgres/kinds.go`, migration 000014, #43) owns the per-group
  `event_kinds` and `resource_kinds`. Management page
  `/{language}/groups/{group}/kinds` (POSTs `/kinds/{events|resources}[/{id}[/archive|/move|/delete]]`)
  for admins and managers; the event `training` kind is protected. Audited as
  `group.event_kind.*` and `group.resource_kind.*`.
- Child colours (#46): when the viewer guards children (`ChildUserIDs`),
  `ProfileService.Children` supplies each child and their teams; events get
  the dots of the children whose teams they include (`WeekGridItem.Markers`
  on the grid, `CalendarItemView.Children` in the day list) and the
  `?child=` filter narrows the visible list. The palette classes live in
  `static/src/app.css`.
- Answer badges (#30): the handler asks
  `ParticipationService.CalendarAnswers(access, week.From, week.To)` once per
  page. It returns the viewer's own `event_participants` answers (`signed_up`,
  `declined`, `nominated`; rows without an answer are skipped) and those of
  `GroupAccess.ChildUserIDs()`, scoped to the group and to events overlapping
  the range (`ParticipantRepository.ListAnswers`, one query). The handler
  attaches them only to events `EventService.List` returned and the filter
  kept, so nobody sees an answer on an event they cannot see, and nobody's
  answer but their own and their children's. A child's badge reads
  "Anna: Coming" (`calendar.child_status`); `CalendarItemView.Answers`
  holds the badges, shown in the day list and as grid item badges.
  They are not filled yet; that needs a week-wide query of the viewer's
  `event_participants` rows (follow-up to #9).

## Participation statistics

- `StatisticsService` (`internal/service/statistics.go`, store
  `internal/store/postgres/statistics.go`, no migration) builds the period
  (week, month, season part, season) as instants `[From, To)` from calendar
  days in the group's time zone, and the store aggregates in SQL
  (`TeamStatistics`, `PlayerStatistics`); only events that are not
  cancelled and have ended count.
- Access stays in `GroupAccess`: `CanViewStatistics` (every member; others
  get 404), `StatisticsTeamIDs` (all teams for staff, coached teams for
  coaches, none for players and parents) and `StatisticsPlayerScope`
  (everyone, the players of the coached teams, plus the account and its
  linked children). Emails show for staff only.
- Page: `GET /{language}/groups/{group}/statistics?period=&date=&season=&part=`
  in `internal/adapter/http/statistics.go`, template `statistics.html`,
  catalogs `statistics.json`. Read-only, server-rendered tables.
## Coaches

- `CoachService` (`internal/service/coach.go`, `availability.go`, store
  `internal/store/postgres/coaches.go`, migration 000008) owns
  `coach_qualifications` (one age range per coach and group) and
  `coach_availability` (weekly windows per season part: week `A`, `B`, or
  `all`, weekday, minutes). Both reference the member's coach role by
  foreign key (removing the role removes them); windows reference the part
  as `(group_id, season_part_id)` and go with it.
- Pure helpers for the planning boards: `TeamAgeRange(team, season)`
  (season start year minus birth years), `CoachQualification.Covers(ages)`
  (the whole range inside), `NormalizeAvailability` (merge overlapping or
  touching windows of the same week and weekday), `DefaultSeasonPart`,
  `PreviousSeasonPart`, and `PartAvailability.Covers(coach, week, weekday,
  start, end)` (an A/B slot needs the week's plus every-week windows; an
  every-week slot in an alternating part needs both weeks; A/B windows
  count only while the part alternates).
- Use cases: `Coaches(access)` (staff: every coach; coaches: themselves),
  `Qualifications(access)` and `Availability(access, partID)` (every coach,
  staff only; for #12 and #14), `CoachAvailability(access, partID, coachID)`,
  `Parts(access)`, `PreviousPart`, `SetQualification`/`ClearQualification`
  (staff, audited), and `AddAvailability`, `MoveAvailability`,
  `DeleteAvailability`, `CopyPreviousAvailability` (staff and the coach
  themself; staff edits of another coach audited as `group.coach.*`).
- Edits go through `CoachRepository.EditAvailability`, which locks the
  coach's role row (`FOR NO KEY UPDATE`), reads the coach's windows of the
  part, applies the change, merges, and replaces them in one transaction, so
  concurrent edits never lose each other. Window IDs change on every edit;
  the grid re-renders after each change.
- Pages: `/{language}/groups/{group}/coaches` (qualifications and team ages),
  `/{language}/groups/{group}/coaches/{coachID}/availability?part=&week=` (the
  week grid with `move`, `create`, `delete`, `copy` posts), and
  `/{language}/groups/{group}/coaches/availability` (staff, side by side)
  (`coach.go`, `coaches.html`, catalogs `coaches.json`). The week grid is
  used unchanged: alternating parts get A / B / every-week tabs of their own,
  the grid's `Week` is `A` or `B` on those tabs and empty otherwise.

## Weekly plan: resource slots

- `PlanSlotService` (`internal/service/plan_slot.go`, store
  `internal/store/postgres/plan_slots.go`, migration 000009) owns
  `plan_slots`: per season part, a resource, `week` (`A`, `B`, or `all`;
  A and B only in parts with alternating weeks), ISO `weekday`,
  `start_minute`/`end_minute` (minutes after local midnight, end up to
  1440), optional `min_age`/`max_age` (1 to 99) and `max_teams` (1 to 99),
  and `version`. Slots may overlap; a part holds at most 500. References are
  `(group_id, season_part_id)` to `season_parts` and `(group_id,
  resource_id)` to `resources`, both cascading, so removing a part (or a
  group) removes its slots. `plan_slots_group_id_unique` lets the training
  plan (#14) reference a slot as `(group_id, plan_slot_id)`.
- Access: `CanManagePlan` (admins, managers, site administrators) edits,
  `CanViewPlan` (those plus coaches) reads; players and parents get access
  denied and no navigation entry. A coach's availability never changes the
  plan by itself.
- Use cases: `Slots(access, partID)` and `Slot(access, slotID)` (read, for
  #14), `Board(access, partID)` (the page: seasons for the part picker, the
  part, its slots, every resource, capacity warnings, copy sources; part 0
  picks the part containing today, else the current season's next or last
  part, else the next part to start, else the latest part), `Create(actor,
  partID, input)` (active resource only), `Update(actor, slotID, version,
  input)` (a slot may stay on an archived resource; changing the resource
  needs an active one), `Move(actor, slotID, version, move)` (week grid:
  resource, weekday, time; keeps week, ages, and team limit; an A/B slot in
  a part that no longer alternates becomes `all`), `Delete(actor, slotID,
  version)`, and `Copy(actor, targetPartID, sourcePartID, replace)`. Writes
  are revision-checked (`ClassifyStaleWrite`: a vanished slot is
  `ErrPlanSlotNotFound`) and audited as `group.plan.slot_created`,
  `.slot_updated`, `.slot_deleted`, and `group.plan.copied` (IDs, week, day,
  times, ages, team limit, counts).
- Copy (`CopyPlanSlots`): explicit, offered from the previous season's part
  at the same position and from the part before the target (part 1 of the
  same season, or the previous season's last part); the server accepts any
  other part of the group. It replaces the target's slots in one
  transaction (the part row is locked), needs `replace` when the target has
  slots, and can be redone. Copies keep resource, weekday, time, ages, and
  team limit; weeks stay as they are when the target alternates weeks,
  otherwise every copy is `all` and exact duplicates are copied once;
  slots on archived resources are skipped. Slots have no dates, so a copy
  into a part with other dates keeps weekdays and times.
- Warnings (`PlanWarnings`): stretches in which a resource's slots allow
  more teams than its `max_concurrent_teams` (a slot without `max_teams`
  counts as one team; resources without a limit never warn). Alternating
  parts check weeks A and B apart (`all` slots count in both); a warning of
  both weeks is reported once. They never block a write; the page lists
  them and marks the slots on the grid.
- Page: `/{language}/groups/{group}/plan?part=&week=&r=` (`plan.go`,
  `plan.html`, catalogs `plan.json`): a part picker, the week grid with the
  active resources (plus archived ones that still hold slots, marked and not
  draggable) as columns, at most eight at once (`r` picks them when there
  are more), the A/B switch for alternating parts, the slot list, and the
  copy form. Clicking an empty spot adds a one-hour slot for the shown week
  (`all` in parts without A/B weeks); dragging moves or resizes. Grid item
  IDs are `<slot ID>-<version>`, so moves are revision-checked. Grid changes
  post to `.../plan/slots/create` and `/move` and answer with the grid, the
  slot list (out of band), and an alert. A slot's page
  (`.../plan/slots/{slotID}`) edits week, ages, team limit, resource, day, and
  times and deletes it; coaches see it read only.

## Coach assignment board

- `CoachBoardService` (`internal/service/coach_board.go`, store method
  `SeasonRepository.ReplaceSeasonCoaches` in
  `internal/store/postgres/coach_board.go`, no migration) edits a season's
  `season_team_coaches` as a whole. Access: `CanManageSeasons` edits,
  `CanViewSeasons` (coaches) reads; players and parents get access denied.
- `Board(access, CoachBoardRequest{SeasonID, Draft, Suggest})`: staff get the
  posted draft (or the saved set) with the coach pool (qualification,
  availability, load), warnings, and optionally a suggestion (`fill` keeps the
  draft and fills active teams without a coach; `replace` starts from an empty
  board). Unknown teams and non-coaches in a draft are dropped; one head per
  team is kept. Coaches get the saved assignments only, for the teams that
  have coaches, without pool, qualifications, or warnings.
- Pure helpers for #14: `SuggestCoaches` (previous season's coaches of the
  same team, with their head mark, if still coaches; then one coach whose
  qualification covers the team's ages, ranked by availability in the
  season, fewest teams so far, name, user ID; unqualified teams stay empty),
  `CoachAssignmentWarnings` (`no_qualification`, `qualification_mismatch`,
  `no_availability` in any part of the season), `PreviousSeason` (latest
  season ending before the chosen one starts), `CoachAssignmentRevision`,
  and `DiffCoachAssignments`.
- `Save(actor, seasonID, revision, assignments)` validates (teams of the
  group, coach role, at most one head per team, at most
  `MaxCoachBoardAssignments`), then `ReplaceSeasonCoaches` locks the season
  row (`FOR UPDATE`; `AssignSeasonCoach` and `RemoveSeasonCoach` now take
  `FOR NO KEY UPDATE` on it, so they serialize), compares the revision, and
  applies the diff (remove, unmark heads, upsert) in one transaction. The
  revision token is a SHA-256 prefix of the sorted `(team, coach, head)` set:
  a different set is `ErrOptimisticLockConflict`, a deleted season
  `ErrSeasonNotFound`. Saves that change something are audited as
  `group.season.coaches_saved` (season ID and name, counts).
- Page: `/{language}/groups/{group}/seasons/{seasonID}/coach-board` (GET, POST
  saves) and `.../coach-board/draft` (re-render, add, remove, suggest)
  (`coach_board.go`, `coach_board.html`, catalogs `coach_board.json`), linked
  from the season page. The board is one form: hidden `a=<team>:<coach>`
  values, `head` checkboxes, `suggested` explanation values, per-team `add`
  selects, and `remove` buttons, so it works without JavaScript. The coach
  board block of `static/app.js` adds SortableJS: the pool clones cards onto
  teams, teams move cards between each other, dropping on the pool removes;
  after a drop it rewrites the card's values and posts the draft, and the
  server re-renders the board (`#coach-board`, outer swap, alerts out of
  band). The same pattern fits the training plan board (#14).

## Training plan board

- `TrainingPlanService` (`internal/service/training_plan.go`, store
  `PlanTrainingRepository` in `internal/store/postgres/plan_trainings.go`,
  migration `000011_create_plan_trainings.up.sql`) owns `plan_trainings`: a
  team on a resource, optionally in a resource slot, with its own week, day,
  start, and end. Access: `CanManagePlan` edits, `CanViewPlan` (coaches)
  reads; players and parents get access denied, non-members 404.
- `Trainings(access, partID)` is the read API for event generation (#15):
  every training of the part with `TeamID`, `ResourceID`, `PlanSlotID` (0 for
  none), `Week`, `Weekday`, `StartMinute`, `EndMinute`, and its `ID`.
- `Board(access, TrainingBoardRequest{PartID, Draft, Suggest})`: staff get the
  posted draft (or the saved set), the slots, resources, team columns with
  their season coaches, warnings, and optionally a suggestion (`fill` keeps
  the draft and adds trainings for active teams without one; `replace`
  starts empty). Coaches get the saved trainings only, without warnings
  (they depend on other coaches' availability). Unknown teams and resources in
  a draft are dropped.
- A training's slot is derived, never sent by the browser:
  `bestSlot` picks a slot of the same resource, day, and weeks that contains
  the training (the shortest), else the one with the largest overlap.
- Pure helpers: `SuggestTrainings` (previous season's trainings of the
  matching part first, on active resources, even if they now warn; then per
  team the first slot, slots with an age range first, where the team's ages
  fit, every coach of the team is available (`PartAvailability.Covers`), the
  slot and the resource have room, no coach trains another team then, and the
  team is not training then; an every-week slot is tried as every week,
  then A, then B), `TrainingWarnings` (`team_archived`, `resource_archived`,
  `no_coach`, `no_slot`, `outside_slot`, `slot_ages`, `slot_full`,
  `resource_full`, `team_overlap`, `coach_unavailable`, `coach_busy`),
  `TrainingRevision`, and `DiffPlanTrainings`.
- `Save(actor, partID, revision, trainings)` validates times and weeks,
  checks teams and resources against the group, recomputes slots, then
  `ReplacePlanTrainings` locks the part row, compares the revision (SHA-256
  prefix of the sorted `(id, team, resource, week, day, start, end)` set), and
  applies the diff: deletes, in-place updates (a moved training keeps its ID,
  and with it the events generated from it), inserts. The unique key is
  deferred so a save can swap two trainings. Audited as
  `group.plan.trainings_saved` (part ID and counts).
- `events (group_id, plan_training_id)` now has a composite foreign key with
  `ON DELETE SET NULL (plan_training_id)`; `plan_trainings.plan_slot_id` uses
  the same column-list form against `plan_slots`.
- Page: `/{language}/groups/{group}/training-plan?part=&week=` (GET, POST saves)
  and `.../training-plan/draft` (`training_plan.go`, `training_plan.html`,
  catalogs `training_plan.json`). Hidden `t=<id>:<team>:<resource>:<week>:
  <day>:<start>:<end>:<reason>` values hold the draft; the week grid beside
  the form (slots as `Background` items, trainings as draggable items,
  `hx-include` of the form) posts moves to the draft endpoint, which answers
  with the board and `HX-Retarget: #training-plan-board`. Teams are dragged
  from a sticky list (the training plan block of `static/app.js`) or added
  with a form; switching the A/B week with unsaved changes posts the draft.
- `WeekGridItem.Background` draws an item behind the others at full column
  width with no lane and no pointer events; `WeekGridConfig.DragHint`
  replaces the hint text.

## Event generation

- `EventGenerationService` (`internal/service/event_generation.go`, store
  `EventGenerationRepository` in `internal/store/postgres/event_generation.go`,
  migration `000012_events_generated_unique.up.sql`) turns the saved training
  plan of a season part into `training` events with open sign-up, linked by
  `plan_training_id` and `generated_for_week` (the week's Monday). Staff only
  (`CanManagePlan`: admins, managers, site administrators); other members get
  access denied, non-members 404.
- `Options`, `Preview`, and `Generate` take a part, the Monday of the first
  week, and one or two consecutive weeks; each week needs a day inside the
  part (a part that starts mid-week generates only its own days). `Preview`
  runs the same decision as `Generate` without locking or writing.
- `PlanGeneration` is the pure decision. A training makes one event in a week
  when its week (`A`, `B`, or `all`, resolved with `SeasonPart.WeekOn` for
  that day) matches and the day is inside the part. Local minutes become
  instants through `EventInstant` as a wall time in the group's time zone, so
  a 16:00 training stays at 16:00 across daylight-saving changes; a start or
  end the clocks skip is reported as skipped (`time_skipped`). Title is the
  team's name; archived teams and resources are skipped.
- Per (training, week), against the generated events that exist:
  none is created; one equal to the plan is `unchanged`; one that differs is
  rebuilt (deleted and created) only when `edited_at` is empty and it has no
  `event_participants` row; otherwise it is kept and listed with the reason
  (`edited`, `participants`). Generated events of the part's weeks that no
  training produces any more (the training was deleted, which clears
  `plan_training_id`, or it no longer applies to that week) are removed
  unless they are edited or have participants, in which case they are kept
  and listed.
- `EventGenerationRepository.Generate` runs the read, the decision, and the
  writes in one transaction. With `apply` it locks the season part row
  (`FOR UPDATE`, so board saves and other generations of the part wait) and
  the generated events of the weeks (`FOR UPDATE OF e`; participant writes
  lock their event row too), refuses to delete an event with participants,
  and creates events through `insertEvent`, the same insert as
  `EventRepository.CreateEvent`. The unique index `events_generated_unique`
  on `(group_id, plan_training_id, generated_for_week)` makes a second event
  for the same training and week impossible even in a race.
- Audit `group.plan.events_generated` (part, weeks, counts; never names),
  written only when something changed.
- Page: `/{language}/groups/{group}/event-generation?part=&week=&weeks=` (GET
  shows the choice and, with `week`, the preview; POST confirms and shows
  the result). Linked from the training plan board (`event_generation.go`,
  `event_generation.html`, catalogs `event_generation.json`). Plain forms,
  no HTMX; the confirmation recomputes the decision rather than trusting the
  preview.
