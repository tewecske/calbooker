# Booking Design

Calbooker plans trainings and events for sport clubs. This file records the
product decisions (agreed with the owner on 2026-10-23) and the data model the
issues build on. Change it in the same PR when a decision changes.

## Decisions

| Topic | Decision |
| --- | --- |
| Organization | A group is a club. Only full users (signed in through SSO, not guests) create groups; the creator becomes the group's admin. Nobody outside a group can see anything about it: every group-scoped route answers 404 to non-members. |
| Time zone | Each group has an IANA time zone. Plans store local wall-clock times; events store `timestamptz` computed in the group's zone. |
| Roles | Per group: `admin`, `manager`, `coach`, `player`, `parent`. A member can hold several roles; permissions are the union. Admins, managers, and coaches must be full users; players and parents may be guests. |
| Joining | Invite codes carry a role and optionally a team, with an optional expiry and use limit. Admins and managers invite any role; coaches invite only players and parents to teams they coach. Admins, managers, and coaches can also add people directly. |
| Teams | A team is defined by a birth-year range (`birth_year_from`..`birth_year_to`) plus a name; it persists across seasons. A free-text description (for example "U10") is optional and carries no logic. |
| Team membership | Players and parents belong to teams permanently until someone changes it by hand. Coach assignments are per season. A team can have several coaches and a coach several teams. |
| Parents | Both: a parent can be attached to a team (view only), and can be linked to specific children. A child is either a profile with no login (created by the parent or staff) or an account of its own (guest or full user). A linked parent sees the child's teams and signs the child up. |
| Coaches | A coach has a qualification (the age range they can coach, in years, confirmed; a team's age in a season is the season's start year minus its birth years) and weekly availability per season part. |
| Resources | Fields, courts, rooms. Optional limit on how many teams can use the resource at the same time. |
| Seasons | Start and end date, with one or two parts (for example August to December, January to June). |
| Weekly plan | Per season part. Weeks can alternate A/B (week 1 of the part is A). The plan has three layers: coach-to-team assignment (per season), resource slots (resource, weekday, time, age range, optional max teams), and trainings (team in a resource slot at a time). Slots and trainings may overlap; the only limits are the optional max-teams values. |
| Planning UI | Admins and managers edit. Drag-and-drop boards, each prefilled with a suggestion: last season's setup first, then matching by qualification, availability, age range, and capacity. Breaking a rule is allowed but shown as a warning. Coaches see the season plan (coach assignments and trainings) read only. A coach influences the plan only by changing their own availability; the plan does not follow such changes by itself. The board shows the resulting warnings, and an admin or manager re-runs the suggestion (and regenerates events) if they want to. |
| Events | Kinds: training, match, championship, other. An event has one or more teams, a resource or a free-text location, and optional extra coaches on top of the teams' coaches. Admins and managers create events for any team; coaches for teams they coach. |
| Generating events | The weekly plan is a template. An admin or manager generates the real events for a chosen week or two at any time. Regenerating a week rebuilds only events that were not edited by hand and have no participants; the rest are kept and listed. |
| Sign-up | Per event: `open` (players opt in, "I'll be there" / "Can't come") or `nominated` (a coach picks players, who can then opt out; for matches and championships). Each event has an optional participant limit and an optional sign-up deadline (both confirmed). When the limit is reached, further players cannot sign up (opting out and staff changes still work); after the deadline, players and parents cannot change their answer, staff still can. |
| Attendance | After the event a coach confirms who attended (removing no-shows, adding walk-ins). Statistics count confirmed attendance per player and team, weekly, monthly, per season part, and per season. |
| Visibility | Admins and managers see everything in the group. Coaches see their teams' events and rosters. Players see their teams' events; parents see their teams' and children's teams' events. On their teams, players and parents see display names of players and coaches and who signed up; emails and the full member list are for admins and managers (coaches: their teams). |
| Calendar UI | Own server-rendered CSS-grid week view (DaisyUI, HTMX). Drag-and-drop through SortableJS, vendored into `static/` (MIT, no dependencies, works on touch). No calendar library. |

Refinements made in #2 (confirmed by the owner on 2026-10-24):

- Only admins grant or remove the `admin` role and remove an admin; managers
  handle every other role and member.
- Until invites carry a role (#3), joining with the group's invite code makes
  the new member a `player`. From #3 on, the invite code defines the role (and
  optionally the team) the new member gets.
- Site administrators (calbooker operators) keep full access to every group,
  as before #2; their group actions are audited and marked as such. This is
  the one exception to "nobody outside a group can see anything about it".
- A team roster entry needs the matching group role (a player on a team holds
  the `player` role in the group); removing the role takes the member off the
  rosters.
- A member may hold no roles; such a member sees only the group overview.
- New groups default to the `Europe/Budapest` time zone.

Refinements made in #3 (open for the owner to confirm):

- The single group invite code is gone (`groups.invite_code` dropped in
  migration 000004; existing codes became player invites). A new group has no
  invite until staff create one on the Invites page.
- Joining with a code as an existing member adds the code's role (and team);
  if the member already has both, nothing changes and no use is counted.
- Invites stay valid until revoked, expired, or used up, even if their
  creator later loses the role that allowed them. Team invites carry only the
  player or parent role. Coaches see and revoke only the player and parent
  invites of teams they coach; managers see every invite but admin invites.
- Codes are stored in clear (staff can share them again); audit entries name
  an invite by ID only.
- A child profile belongs to one group (`child_profiles.group_id`) and holds
  only the player role there; a child in two clubs gets two profiles.
  Profiles are created by admins, managers, and parents (a parent's profile
  is linked to them), not by coaches. Profiles are deleted, not removed as
  members; nothing deletes them automatically.
- Only staff link a parent to an existing member account or unlink parents;
  a guardian needs the parent role, and losing it removes the link.
- Linked parents appear on their children's team rosters as parents. This is
  derived from `guardians` at read time, not stored in `team_members`, and
  such entries are removed by unlinking.
- Audit entries name profiles by ID, never by the child's name or birth year.

Refinements made in #4 (open for the owner to confirm):

- A resource's kind is one of a fixed list stored as a stable code: field,
  court, sports hall, pool, room, other (labelled in en and hu). Free text
  would make filtering and later rules harder; "other" plus the description
  covers the rest.
- The optional concurrent-team limit is a whole number from 1 to 99; empty
  means no limit.
- Admins, managers, and site administrators create, edit, archive, and
  restore resources; coaches see the resource list and pages read only.
  Players and parents do not see the resource pages (no navigation entry,
  access denied); events and plans will show them the resource's name.
- Names are unique per group among resources in use, ignoring case; an
  archived resource frees its name, and restoring it fails while another
  resource holds the name.
- Archived resources stay loadable for the plans and events that reference
  them, are left out of pickers, can still be edited, and can be restored.
  Nothing deletes a resource except deleting its group.
- Audit entries name a resource by ID and name (`group.resource.*`).

Refinements made in #5 (open for the owner to confirm):

- Season and part dates are calendar days in the group's time zone, both
  ends included. A season needs a first day before its last; it has one or
  two parts, each inside the season, and part 2 starts after part 1 ends.
  Parts need not cover the whole season (a winter break is allowed).
- Seasons of a group never overlap, so "the current season" (the one
  containing today in the group's time zone) is unique; there may be none.
  Season names are unique per group, ignoring case.
- A part with alternating weeks starts with an A week: the Monday-based week
  that holds the part's first day.
- Admins, managers, and site administrators create, edit, and delete
  seasons and assign coaches. Coaches see the season pages read only.
  Players and parents do not see the season pages (no navigation entry,
  access denied); on their teams' pages they see the current season's
  coaches.
- Editing a season edits its parts in the same form under one revision;
  removing part 2 deletes it with whatever later features stored for it.
  Deleting a season deletes its parts and coach assignments.
- Coach assignments are made on the team page for any season, past ones
  included. Only members with the coach role can be assigned; removing the
  role (or the member) removes their assignments. At most one head coach per
  team and season: marking another coach as head unmarks the previous one.
- Coach rights on a team (roster, invites, emails) come only from the
  current season's assignments. Between seasons, coaches manage no team
  until an admin or manager assigns them in the new season.
- Archived teams keep their assignments and can still be assigned.
- Audit entries name seasons by ID and name and coach assignments by season,
  team, and member ID (`group.season.*`).

Refinements made in #8 (open for the owner to confirm):

- An event has exactly one of a resource and a free-text location; neither
  is refused. Archived resources are not offered and are refused for new
  events, but an event keeps the archived resource it already has.
- Times are entered as wall-clock times in the group's time zone. A time
  skipped by a daylight-saving change is refused with a message; a time that
  happens twice is its first occurrence (summer time). An event lasts at
  most 31 days. The sign-up deadline, when set, is before the start; the
  participant limit is 1 to 999.
- Coaches create and edit events only for teams they all coach in the current
  season; an event with a team they do not coach is read only for them.
  Extra coaches must hold the coach role. An extra coach sees the event even
  without seeing its teams.
- Every member has the Events tab and sees the events of their own,
  coached, and children's teams (staff: all); other events answer 404.
- Warnings (resource over its team limit, coach busy) are shown on the event
  page to those who may edit the event, not to players and parents. The
  coaches of an event are its extra coaches plus the season coaches of its
  teams in the season containing the event's first day.
- Cancel is a soft state that can be reinstated; cancelled events stay
  listed and count for no warnings. Staff and the event's coaches may also
  delete an event (until #9 adds participants; #9 decides whether events with
  participants can be deleted). Cancelling or editing a generated event
  marks it edited by hand.
- Audit entries name events by ID, title, kind, times, and team IDs
  (`group.event.*`), never by the location text.
Refinements made in #7 (open for the owner to confirm):

- A qualification is one age range per coach and group (whole years, 0 to
  99). A team's age in a season is the season's start year minus its birth
  years, so a team born 2015-2016 is 10-11 in a season starting in 2026. A
  qualification covers a team only when the team's whole age range is inside
  it (`CoachQualification.Covers`); the planning boards (#12, #14) use this.
- Admins, managers, and site administrators set and clear qualifications
  (audited as `group.coach.qualification_set`, `.qualification_cleared`).
  Coaches see their own qualification read only and nobody else's.
- Availability is weekly windows per season part. A part with alternating
  weeks has windows for week A, week B, and every week; other parts only
  every-week windows. When a part stops alternating its A/B windows stay
  stored and listed as "not counted"; they no longer count.
- Overlapping or touching windows of the same week and weekday merge into
  one (16:00-17:00 plus 17:00-18:00 is 16:00-18:00). Times are 5-minute
  steps (the grid snaps to 15). A coach has at most 100 windows per part.
- The coach edits their own availability; admins, managers, and site
  administrators edit anyone's. Coaches do not see other coaches'
  availability. A coach's own edits are self-service and not audited; staff
  edits of another coach are (`group.coach.availability_changed`,
  `.availability_copied`).
- "Copy from the previous part" is per coach and replaces the target part's
  windows after a confirmation. The previous part is part 1 of the same
  season for part 2, else the last part of the previous season. Into a part
  without A/B weeks only every-week windows are copied. If nothing fits,
  nothing is replaced.
- Staff see every coach's availability side by side in one read-only grid
  per part and week, one color per coach.
- Availability changes never change plans or events (owner decision); #12
  and #14 show the resulting warnings.

Refinements made in #13 (open for the owner to confirm):

- Admins, managers, and site administrators edit the resource slot plan;
  coaches see it read only; players and parents do not see it (no
  navigation entry, access denied).
- Slots have a week of `A`, `B`, or `all`; A and B only in parts with
  alternating weeks. Clicking the grid adds a slot for the shown week; the
  slot's page turns it into an every-week slot. A slot in a part that later
  stops alternating is shown in every week and becomes `all` when moved.
- Ages are whole years from 1 to 99 (either end optional); the team limit
  is 1 to 99 or none. Times are inside one day (end up to midnight).
- A new part starts empty. Copying is explicit, offered from the previous
  season's matching part and the part before, replaces the target's slots
  after confirmation, and can be redone. Weeks are kept when the target
  alternates; otherwise all copies become every-week slots and exact
  duplicates are copied once. Slots on archived resources are skipped (the
  page says how many).
- Capacity warnings count a slot without a team limit as one team. They
  never block a change.
- Slots on archived resources stay, shown in a marked column and not
  draggable; they can be edited or deleted, and moved to an active resource.
- Removing a season part removes its slots. Audit entries name slots by ID
  with their resource ID, week, day, times, ages, and team limit
  (`group.plan.*`).

## Data model

All tables carry `group_id` (directly or through their parent) so every query
is scoped to one group. Mutable rows have `version` for revision-checked
writes. Times are `timestamptz`; plan times are minutes after local midnight
(`start_minute`, `end_minute`) plus `weekday` (1 = Monday, ISO) and `week`
(`A`, `B`, or `all`).

```
users (+ kind: 'account' | 'guest' | 'profile')    profiles never sign in, are not deleted by guest retention
groups (+ time_zone)
group_members (group_id, user_id)
group_member_roles (group_id, user_id, role)        several rows per member
group_invites (group_id, code, role, team_id?, expires_at?, max_uses?, uses, created_by, revoked_at?)
teams (group_id, name, birth_year_from, birth_year_to, description?, archived_at?)
team_members (team_id, user_id, role: player | parent)
child_profiles (user_id, group_id, birth_year, created_by, version)   the users row has kind 'profile'
guardians (group_id, parent_user_id, child_user_id)  parent needs the parent role; linked parents are derived onto rosters
resources (group_id, name, kind, description?, max_concurrent_teams?, archived_at?, version)   kind: field | court | hall | pool | room | other
seasons (group_id, name, starts_on, ends_on, version)   dates in the group's calendar; seasons of a group never overlap
season_parts (group_id, season_id, position 1 | 2, name, starts_on, ends_on, alternating_weeks bool)   edited with the season's version
season_team_coaches (group_id, season_id, team_id, coach_user_id, is_head)   coach needs the coach role; one head coach per team and season
coach_qualifications (group_id, coach_user_id, min_age, max_age)   ages 0..99; coach needs the coach role
coach_availability (group_id, season_part_id, coach_user_id, week, weekday, start_minute, end_minute)   merged per week and weekday
plan_slots (group_id, season_part_id, resource_id, week, weekday, start_minute, end_minute, min_age?, max_age?, max_teams?, version)   A/B only in alternating parts; slots may overlap
plan_trainings (season_part_id, team_id, resource_id, plan_slot_id?, week, weekday, start_minute, end_minute)
events (group_id, kind, title, starts_at, ends_at, resource_id?, location?, signup_mode,
        max_participants?, signup_deadline?,
        cancelled_at?, plan_training_id?, generated_for_week?, edited_at?, version)
event_teams (event_id, team_id)
event_coaches (event_id, coach_user_id)              extra coaches only
event_participants (event_id, user_id, status: signed_up | declined | nominated,
                    attended bool?, updated_by, updated_at)
```

Authorization lives in one service type, `GroupAccess` (built per request from
the member's roles, coached teams for the current season, own teams, and
children). Handlers ask it questions (`CanManageGroup`, `CanEditTeam(id)`,
`VisibleTeamIDs()`, …) and never check roles themselves.

## Code areas

Each feature owns its own files so parallel branches rarely touch the same
lines:

- `migrations/0000NN_<area>.up.sql`
- `internal/service/<area>.go`, `internal/store/postgres/<area>.go`
- `internal/adapter/http/<area>.go` with a `register<Area>Routes` function
  called from `handler.go`
- `templates/<area>.html`
- `internal/locale/catalogs/<lang>/<area>.json` (merged at load; a message
  ID in two files is an error)
- `tests/browser/<area>.spec.ts` using helpers from `tests/browser/support/`

Shared hot spots: `handler.go` (one line per area), `GroupAccess`, the group
navigation in `templates/groups.html`, `static/app.js`, and `cmd/web` wiring.
Keep changes there small and rebase often.
