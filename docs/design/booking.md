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
group_invites (group_id, code, role, team_id?, expires_at?, max_uses?, uses, created_by)
teams (group_id, name, birth_year_from, birth_year_to, description?, archived_at?)
team_members (team_id, user_id, role: player | parent)
guardians (group_id, parent_user_id, child_user_id)
resources (group_id, name, kind, description?, max_concurrent_teams?, archived_at?)
seasons (group_id, name, starts_on, ends_on)
season_parts (season_id, name, starts_on, ends_on, alternating_weeks bool)
season_team_coaches (season_id, team_id, coach_user_id, is_head)
coach_qualifications (group_id, coach_user_id, min_age, max_age)
coach_availability (season_part_id, coach_user_id, week, weekday, start_minute, end_minute)
plan_slots (season_part_id, resource_id, week, weekday, start_minute, end_minute, min_age?, max_age?, max_teams?)
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
- `internal/locale/catalogs/<lang>/<area>.json` (after issue #1 splits the
  catalogs)
- `tests/browser/<area>.spec.ts` using helpers from `tests/browser/support/`

Shared hot spots: `handler.go` (one line per area), `GroupAccess`, the group
navigation in `templates/groups.html`, `static/app.js`, and `cmd/web` wiring.
Keep changes there small and rebase often.
