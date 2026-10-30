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
  delete an event, but not one with participants (#9: any answer or
  attendance; cancel it instead). Cancelling or editing a generated event
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

Refinements made in #9 (open for the owner to confirm):

- Participants are the players of the event's teams (`team_members` with the
  player role: accounts, guests, and child profiles). Players answer for
  themselves, linked parents for their children (profiles and child
  accounts). Everyone who sees the event sees the list with display names and
  answers; emails only for staff and the event's coaches.
- "The event's coaches" who manage participants are staff (admins, managers,
  site administrators), coaches of one of its teams this season, its extra
  coaches, and the season coaches of its teams in the season of the event.
  They change anyone's answer and record attendance; their changes to
  someone else's row are audited (`group.event.participant_updated`,
  `.participant_nominated`, `.participant_removed`,
  `group.event.attendance_recorded`, naming members by ID). A player's or
  parent's own answer is self-service and not audited.
- `open` events: players and parents choose "I'll be there" or "Can't come"
  and may switch. `nominated` events: coaches nominate (or mark not coming,
  or remove); a nominated player or parent opts out, and may take the place
  back ("I'll come after all") while sign-up is open and the limit allows.
  Players do not sign themselves up for nominated events.
- Signed up and nominated count against the limit; declined does not. At
  the limit, players and parents cannot take a place; opting out and staff
  and coach changes still work (staff may go over the limit). The limit is
  enforced in the writing transaction with a row lock on the event.
- After the deadline (from the deadline instant on) players and parents
  cannot change their answer; staff and coaches can until the event starts.
  From the start nobody changes answers. Cancelled events take no sign-up
  changes and no attendance.
- After the event ends, the event's coaches and staff confirm attendance on
  a checklist prefilled from the answers (or the previous confirmation): a
  ticked player attended, an unticked one did not. A walk-in can be any
  member with the player role in the group, also from other teams; a walk-in
  without an answer is stored with no status and removed again when
  unticked. Attendance can be corrected later; before the end it is refused.
- Rows are keyed by user ID, so a guest's answers survive the upgrade; a
  member who leaves the group loses their rows.

Refinements made in #10 (open for the owner to confirm):

- The calendar is a group page (`/groups/{group}/calendar`) with a tab for
  every member; it shows exactly the events the Events tab shows, one
  Monday-based week at a time in the group's time zone, cancelled events
  included (faded, struck through, with a badge).
- Events across midnight or longer than a day are split into one block per
  day; the day list notes "spans several days" with the whole time range.
- Filters: team (the teams the viewer sees), resource, and kind. Staff and
  coaches pick from the group's resources; players and parents from the
  resources on their visible events of the shown week. Filters only narrow
  what the viewer sees; a team or resource the viewer may not pick is
  ignored.
- Switching groups keeps the week and the kind filter and drops team and
  resource filters. The switcher shows only for members of several groups.
- Narrow screens (below `md`) get a list of days instead of the grid.
- The viewer's own sign-up status is shown as a badge on each calendar
  entry (filled in #30).
Refinements made in #30 (open for the owner to confirm):

- Calendar entries show the viewer's own answer as a badge ("Coming", "Not
  coming", "Nominated", the same words as the event page); events without an
  answer show none. A parent also sees each linked child's answer, prefixed
  with the child's name ("Anna: Coming"). A viewer who is both a player and a
  parent can see several badges on one entry.
- Nobody else's answer appears on the calendar, coaches and staff included;
  they open the event for the roster. Badges appear only on events the
  calendar already lists, so filters and visibility are unchanged.
- One query per week page; no migration.

Refinements made in #12 (open for the owner to confirm):

- The coach board edits a draft in the page; nothing is saved until Save,
  which replaces the season's whole assignment set. A save based on an older
  set (someone changed assignments meanwhile) is refused with a conflict and
  the draft stays on screen.
- Suggest only fills active teams that have no coach and never removes or
  changes what is on the board; "Start over from suggestion" replaces the
  board (after a confirmation). Archived teams are never filled and show only
  while they hold assignments.
- The suggestion takes the team's previous-season coaches (with the head
  mark) first, even if their qualification no longer covers the team (the
  board then warns); otherwise one qualified coach, preferring coaches with
  availability in the season, then the fewest teams, then name. Coaches from
  qualification are not marked head. Unqualified teams stay empty.
- Warnings (never blocking): no qualification, qualification does not cover
  the team's ages, no availability in any part of the season.
- Coaches see the board read only: only the teams that have coaches in the
  season (as on the season page), coach names and head marks, no
  qualifications, availability, or warnings.

Refinements made in #11 (open for the owner to confirm):

- The statistics page is `/{language}/groups/{group}/statistics`. A period is a
  week (Monday to Sunday), a calendar month, a season part, or a season, in
  the group's time zone; weeks and months work without seasons.
- Only events that are not cancelled and have already ended count. A team's
  "opportunities" are held events times its current players; sign-ups are
  answers "signed up" plus nominations; attended is confirmed attendance;
  the rate is attended over opportunities (a player: over held events of
  their current teams), rounded to whole percent, a dash when nothing was
  held.
- Team membership has no history: current rosters are used for past
  periods. Walk-in attendance at another team's event is not counted in a
  player's figures.
- Admins, managers, and site administrators see every non-archived team and
  player; coaches see the teams they coach this season and those teams'
  players (and themselves if they also play); players and parents see only
  their own and their linked children's rows and no team table. Emails show
  for staff only. Non-members get 404.
- No charts and no migration.

Refinements made in #14 (open for the owner to confirm):

- Admins, managers, and site administrators edit the training plan; coaches
  see the saved plan read only, all teams, without warnings (they rest on
  other coaches' availability); players and parents do not see it.
- The board edits a draft; nothing is saved until Save, which replaces the
  part's whole training set. A save based on an older set is refused as a
  conflict. A moved training keeps its identity, so events generated from it
  (#15) keep their link; deleting a training keeps its events, unlinked.
- Trainings of a team may repeat (several per week); a team has a week of
  `A`, `B`, or `all` like slots. A training sits in the slot that contains it
  (the shortest), else the slot it overlaps most; it may reach beyond its
  slot, which is a warning.
- Warnings, never blocking: archived team or resource, team without a season
  coach, no slot, partly outside the slot, team ages outside the slot's ages,
  more teams in a slot or on a resource than allowed (slots without a limit
  count as one), the team twice at once, a coach not available for the whole
  training (every week of an every-week training), a coach with another team
  at the same time.
- Suggest fills only active teams without a training, one training per team
  from slots, and never removes anything; "Start over from suggestion"
  replaces the board. Order: last season's matching part's trainings (same
  resource, day, and time; archived resources skipped; even if they now
  warn), then slots (age-specific first) where ages, all coaches, capacity,
  and coach double booking fit. Teams no slot fits stay empty and the count
  is shown.
- A part that does not alternate stores every training as `all`.


Refinements made in #15 (open for the owner to confirm):

- Generation is staff only (admins, managers, site administrators) and lives
  on its own page, linked from the training plan board. Pick a season part,
  the first week (any week with a day inside the part, default next week)
  and one or two weeks; the preview lists what will be created, rebuilt,
  removed, kept, or skipped, with times in the group's time zone; confirming
  recomputes it. A part that starts or ends mid-week generates only the days
  inside it.
- A generated event is a `training` with open sign-up, titled with the
  team's name, with the training's team and resource, no coaches (the
  team's season coaches apply as for any event), no limit and no deadline.
  A training in an A/B part makes an event only in weeks of its letter.
- Times keep their wall-clock time across daylight-saving changes (a Sunday
  training at 08:00 is at 08:00 on the day clocks change; one that crosses
  the change is an hour shorter or longer). A start or end that does not
  exist that day (02:30 on the last Sunday of March in Budapest) is skipped
  and listed rather than moved.
- Regenerating compares by training and week. An event is rebuilt (deleted
  and created again) only if it differs from the plan, `edited_at` is empty,
  and it has no participant row; events that are edited by hand (including
  cancelled ones) or have participants are kept and listed with the reason
  and a link, even when the plan moved. Generating twice without a change
  writes and audits nothing, and a unique index allows one generated event
  per training and week.
- Trainings deleted from the plan: the foreign key clears the event's
  training link, so the next generation of those weeks removes their
  unedited, participant-free events (they are recognised by
  `generated_for_week` and by lying inside the part's dates) and lists the
  others as kept. Weeks never regenerated keep their events until then.
- A hand-deleted generated event is created again by the next generation.
  Trainings of archived teams or resources are skipped and listed.
- Audit: `group.plan.events_generated` with the part, the weeks, and counts
  of created, rebuilt, removed, kept, and unchanged events.

Refinements made in #38 (open for the owner to confirm):

- The Members page shows the guardian links to staff (`CanManageGuardians`,
  the same people as on the Children page; nobody else sees them or loads
  them). A parent's row lists the linked players, each an unlink button, and
  a "Link to player" select of the player members not yet linked; a player's
  row lists the linked parents. It reuses the Children routes
  (`/groups/{group}/guardians/add|remove`) and their service, authorization,
  audit and messages; a hidden `return=members` makes the write render the
  Members page again (errors included) instead of the Children page. Child
  profiles appear on Members as players, so a parent can be linked to a
  profile there too.
- The A/B flag stays on `season_parts.alternating_weeks` (week 1 of the part
  is A). Staff switch it with one button on the slot plan page and on the
  training plan board, for the selected part: `POST
  /groups/{group}/seasons/{seasonID}/parts/{partID}/alternating` with the
  season's `version`. It is a season edit (`SeasonService.SetPartAlternating`
  rebuilds the season input with one flag changed and goes through
  `Update`): `CanManageSeasons` (admins, managers, site administrators;
  `CanManagePlan` is the same set), revision check with `ClassifyStaleWrite`
  (stale is a conflict, a vanished season is not-found), and the
  `group.season.updated` audit entry. Coaches see the plan pages read only,
  without the switch; a forged request from a coach is refused with 403.
- Turning a part off keeps the stored week of its slots and trainings; the
  plans and the event generation treat them as every-week, as in #7, #13 and
  #14. The switch asks for confirmation before turning it off. The page
  re-renders with the new state (plan page: whole page; training plan: the
  board, discarding an unsaved draft).
- The season edit form shows the flag of a stored part read only, with a link
  to its plan page, and still posts the current value in a hidden field so
  saving the season keeps it. The create form, and a part 2 added later, keep
  the checkbox because those parts have no plan page yet.


Refinements made in #42 (open for the owner to confirm):

- Group and team pages use names in the URL: `/groups/bvsc/teams/u10`. A
  group has a `slug` (unique site-wide) and a team has one (unique within its
  group). The slug comes from the name: lowercase, accents folded to ASCII
  (`Füzy` becomes `fuzy`), hyphens, and a numeric suffix on a clash (`club`,
  `club-2`). A name that gives nothing usable, only digits, or a reserved word
  (`new`, `edit`, `settings`, `members`, `teams`, ...) gets a `group-` or
  `team-` prefix.
- Staff edit the slug: admins and managers on the group settings page, and on
  the team page. Validation: lowercase letters, digits and single hyphens, up
  to 64 characters, not only digits, not reserved, not taken. Renaming a group
  or team does not change its slug; changing a slug changes the web address, so
  the page continues at the new URL. A new team gets a derived slug (no field on
  the create form).
- The old numeric URLs keep working for GET as permanent redirects (after the
  access check); POSTs to old URLs are 404. Other lanes build links with the
  slug.

Refinements made in #44 (open for the owner to confirm):

- A team page has a submenu with three tabs: Roster (the team page itself,
  `.../teams/{team}`), Events (`.../teams/{team}/events`) and Calendar
  (`.../teams/{team}/calendar`). The group Teams tab stays highlighted. New
  team tabs (invites, #47) are added to one list in `team_tabs.go`.
- Roster: each player row lists the player's parents on the team, so a parent
  with two children on the team appears under both; the parents list keeps
  showing "Parent of" for each parent. Parents come from the derived
  guardian links (#3), as before. Players in several teams show in each
  team's roster.
- Events tab: the group's event list with the team fixed (upcoming and past,
  no team filter). Rows link to the normal event page. The create form posts
  to the tab, preselects the team (other teams can still be ticked), and is
  shown only to accounts whose form offers that team, the same rule as the
  group list (`CanCreateEvent`). Edit and cancel stay on the event page.
- Calendar tab: the #10 week calendar with the team fixed: events visible to
  the viewer that include the team; no team filter and no group switcher; week
  and kind/resource filters and the own-answer badges (#30) work as before.
  The group-level Events and Calendar are unchanged.
- A team the account cannot see, or a non-member, gets 404 on every tab.

Refinements made in #47 (open for the owner to confirm):

- Team invites: the team page has an invite card for staff and the team's
  coaches (the `CanCreateInvite` rules): pick player or parent, an optional
  expiry and use limit, and the code is bound to that team. Joining adds the
  player or parent to the team (an existing member gets the team added).
  Staff still add existing members to a team by hand. The group's Invites
  page stays and shows every invite.
- Several teams: a player can be on several teams and a parent can have
  children on several teams (several invites, or by hand). Access, the
  calendar, the event list, and sign-up already work from the union of the
  teams; an event of two of a player's teams is one event (counted once in
  statistics). Tests cover a player on two teams and a parent with children on
  two teams.
- Call-ups: a coach of the event (or staff) calls up any player of the group
  who is not on the event's teams (`event_participants.called_up`). A call-up
  is a nomination across teams: the row has the status `nominated`, counts
  against the participant limit, and works in open and nominated events. It
  needs a player who is a member of the group with the player role. Coaches
  and staff may go over the limit.
- Visibility: a called-up player, and a parent of a called-up child, can see
  that one event (list, calendar, event page, and the participant list), and
  nothing else of the event's teams: no team page, no roster, no other
  events. `GroupAccess` gets the called-up event IDs per request
  (`CanViewEvent`), and the event list query adds them to the visible scope.
  Withdrawing the call-up (clearing the row) takes the event away again.
- Answering: a called-up player (or parent) can opt out ("Can't come") and
  take the place back ("I'll come after all") while sign-up is open and the
  limit allows, in every sign-up mode. The row stays a call-up (a `declined`
  call-up still shows the event, so the place can be taken back). Coaches
  can mark the call-up not coming, nominate again, or withdraw it.
- Leaving out: a coach of the event or staff can leave a player of the
  event's teams out (status `left_out`, the "Leave out" button). The player
  sees "Left out" and cannot answer; the place is not counted against the
  limit; a coach can clear it again. It does not apply to called-up players
  (withdraw them instead).
- Overlap warning: after a call-up, the page lists the player's other events
  at the same time (not cancelled; events the player is signed up or called
  up for, and events of the player's teams the player has not declined or
  been left out of). It is only a warning; the call-up stays. Sign-ups and
  nominations by the player do not warn.
- Statistics: confirmed attendance at a called-up event counts for the
  called-up player: the event counts as one of the player's events (the
  denominator) and, when the coach confirms attendance, as attended. A
  declined call-up is not counted. A player left out of an event is not
  counted for it either, unless attendance was confirmed anyway. A player
  with a call-up appears in the player statistics even without a team. Team
  rows are unchanged: they count the team's own players at the team's own
  events, so a called-up guest never changes another team's figures.
- Call-ups against nominations: a nomination picks a player of the event's
  teams for a nominated event (the player already sees the event). A call-up
  brings in a player from outside the teams for any event, and is what makes
  the event visible to that player. Both use the same status and limit.
- Audit: `group.event.participant_called_up` and
  `group.event.participant_left_out`; removals use
  `group.event.participant_removed`. Own answers stay unaudited.

## Data model

All tables carry `group_id` (directly or through their parent) so every query
is scoped to one group. Mutable rows have `version` for revision-checked
writes. Times are `timestamptz`; plan times are minutes after local midnight
(`start_minute`, `end_minute`) plus `weekday` (1 = Monday, ISO) and `week`
(`A`, `B`, or `all`).

```
users (+ kind: 'account' | 'guest' | 'profile')    profiles never sign in, are not deleted by guest retention
groups (+ slug, time_zone)
group_members (group_id, user_id)
group_member_roles (group_id, user_id, role)        several rows per member
group_invites (group_id, code, role, team_id?, expires_at?, max_uses?, uses, created_by, revoked_at?)
teams (group_id, name, slug, birth_year_from, birth_year_to, description?, archived_at?)
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
plan_trainings (group_id, season_part_id, team_id, resource_id, plan_slot_id?, week, weekday, start_minute, end_minute)   several per team; slot optional, own times
events (group_id, kind, title, starts_at, ends_at, resource_id?, location?, signup_mode,
        max_participants?, signup_deadline?,
        cancelled_at?, plan_training_id?, generated_for_week?, edited_at?, version)
event_teams (event_id, team_id)
event_coaches (event_id, coach_user_id)              extra coaches only
event_participants (group_id, event_id, user_id, status?: signed_up | declined | nominated | left_out,
                    called_up bool, attended bool?, updated_by?, updated_at)   no status: a walk-in added at attendance;
                                                                              called_up: status nominated or declined (#47)
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
