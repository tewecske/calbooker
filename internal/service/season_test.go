package service

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
)

func day(raw string) time.Time {
	parsed, err := time.Parse(SeasonDateLayout, raw)
	if err != nil {
		panic(err)
	}
	return parsed
}

// seasonRepositoryStub keeps seasons and coach assignments of group 5 in
// memory.
type seasonRepositoryStub struct {
	zone            string
	zoneErr         error
	seasons         map[int64]Season
	coaches         []SeasonCoach
	createErr       error
	updateErr       error
	deleteErr       error
	assignErr       error
	removeErr       error
	coachedDays     []time.Time
	expectedVersion int64
	assigned        []CoachAssignment
	removed         int
	deleted         int
}

func newSeasonStub(seasons ...Season) *seasonRepositoryStub {
	stub := &seasonRepositoryStub{zone: "Europe/Budapest", seasons: make(map[int64]Season)}
	for _, season := range seasons {
		stub.seasons[season.ID] = season
	}
	return stub
}

func (r *seasonRepositoryStub) GroupTimeZone(context.Context, int64) (string, error) {
	return r.zone, r.zoneErr
}

func (r *seasonRepositoryStub) CreateSeason(_ context.Context, season Season) (Season, error) {
	if r.createErr != nil {
		return Season{}, r.createErr
	}
	season.ID = 99
	for index := range season.Parts {
		season.Parts[index].ID = int64(990 + index)
		season.Parts[index].SeasonID = 99
	}
	r.seasons[season.ID] = season
	return season, nil
}

func (r *seasonRepositoryStub) FindSeason(_ context.Context, groupID, seasonID int64) (Season, error) {
	season, ok := r.seasons[seasonID]
	if !ok || season.GroupID != groupID {
		return Season{}, ErrSeasonNotFound
	}
	return season, nil
}

func (r *seasonRepositoryStub) FindSeasonByPart(_ context.Context, groupID, partID int64) (Season, error) {
	for _, season := range r.seasons {
		if _, ok := season.Part(partID); ok && season.GroupID == groupID {
			return season, nil
		}
	}
	return Season{}, ErrSeasonNotFound
}

func (r *seasonRepositoryStub) ListSeasons(_ context.Context, groupID int64) ([]Season, error) {
	list := make([]Season, 0, len(r.seasons))
	for _, season := range r.seasons {
		if season.GroupID == groupID {
			list = append(list, season)
		}
	}
	slices.SortFunc(list, func(a, b Season) int { return b.StartsOn.Compare(a.StartsOn) })
	return list, nil
}

func (r *seasonRepositoryStub) SeasonOn(_ context.Context, groupID int64, on time.Time) (Season, error) {
	for _, season := range r.seasons {
		if season.GroupID == groupID && season.Contains(on) {
			return season, nil
		}
	}
	return Season{}, ErrSeasonNotFound
}

func (r *seasonRepositoryStub) UpdateSeason(_ context.Context, season Season, expectedVersion int64) (Season, error) {
	r.expectedVersion = expectedVersion
	if r.updateErr != nil {
		return Season{}, r.updateErr
	}
	season.Version++
	r.seasons[season.ID] = season
	return season, nil
}

func (r *seasonRepositoryStub) DeleteSeason(_ context.Context, _, seasonID, expectedVersion int64) error {
	r.expectedVersion = expectedVersion
	if r.deleteErr != nil {
		return r.deleteErr
	}
	r.deleted++
	delete(r.seasons, seasonID)
	return nil
}

func (r *seasonRepositoryStub) ListSeasonCoaches(_ context.Context, groupID int64, filter SeasonCoachFilter) ([]SeasonCoach, error) {
	list := make([]SeasonCoach, 0)
	for _, coach := range r.coaches {
		if coach.GroupID == groupID && (filter.SeasonID == 0 || coach.SeasonID == filter.SeasonID) && (filter.TeamID == 0 || coach.TeamID == filter.TeamID) {
			list = append(list, coach)
		}
	}
	return list, nil
}

func (r *seasonRepositoryStub) AssignSeasonCoach(_ context.Context, assignment CoachAssignment, _, _ int64) error {
	if r.assignErr != nil {
		return r.assignErr
	}
	r.assigned = append(r.assigned, assignment)
	return nil
}

func (r *seasonRepositoryStub) RemoveSeasonCoach(context.Context, int64, int64, int64, int64) error {
	if r.removeErr != nil {
		return r.removeErr
	}
	r.removed++
	return nil
}

func (r *seasonRepositoryStub) CoachedTeamIDs(_ context.Context, groupID, coachUserID int64, on time.Time) ([]int64, error) {
	r.coachedDays = append(r.coachedDays, on)
	ids := make([]int64, 0)
	for _, coach := range r.coaches {
		season, ok := r.seasons[coach.SeasonID]
		if coach.GroupID == groupID && coach.CoachUserID == coachUserID && ok && season.Contains(on) {
			ids = append(ids, coach.TeamID)
		}
	}
	slices.Sort(ids)
	return slices.Compact(ids), nil
}

func newTestSeasonService(t *testing.T, seasons *seasonRepositoryStub, members ...GroupMember) (*SeasonService, *auditRecorderStub) {
	t.Helper()
	memberships := newMembershipStub()
	memberships.members = members
	service, err := NewSeasonService(seasons, memberships, usersWith(User{ID: 7, Email: strPtr("staff@example.test")}))
	if err != nil {
		t.Fatalf("NewSeasonService() error = %v", err)
	}
	auditor := &auditRecorderStub{}
	service.SetAuditor(auditor)
	service.now = func() time.Time { return time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC) }
	return service, auditor
}

// season2627 is a two-part season of group 5 containing 2026-10-24.
func season2627() Season {
	return Season{
		ID: 1, GroupID: 5, Name: "2026/27", StartsOn: day("2026-08-01"), EndsOn: day("2027-06-30"), Version: 3,
		Parts: []SeasonPart{
			{ID: 11, GroupID: 5, SeasonID: 1, Position: 1, Name: "Autumn", StartsOn: day("2026-08-01"), EndsOn: day("2026-12-20")},
			{ID: 12, GroupID: 5, SeasonID: 1, Position: 2, Name: "Spring", StartsOn: day("2027-01-04"), EndsOn: day("2027-06-30"), AlternatingWeeks: true},
		},
	}
}

func season2728() Season {
	return Season{
		ID: 2, GroupID: 5, Name: "2027/28", StartsOn: day("2027-07-01"), EndsOn: day("2028-06-30"),
		Parts: []SeasonPart{{ID: 21, GroupID: 5, SeasonID: 2, Position: 1, Name: "Whole", StartsOn: day("2027-07-01"), EndsOn: day("2028-06-30")}},
	}
}

func validSeasonInput() SeasonInput {
	return SeasonInput{
		Name: " 2026/27 ", StartsOn: day("2026-08-01"), EndsOn: day("2027-06-30"),
		Parts: []SeasonPartInput{
			{Name: " Autumn ", StartsOn: day("2026-08-01"), EndsOn: day("2026-12-20")},
			{Name: "Spring", StartsOn: day("2027-01-04"), EndsOn: day("2027-06-30"), AlternatingWeeks: true},
		},
	}
}

func TestNewSeasonServiceRejectsNilDependencies(t *testing.T) {
	if _, err := NewSeasonService(nil, newMembershipStub(), usersWith()); !errors.Is(err, ErrNilSeasonRepository) {
		t.Fatalf("nil repository error = %v", err)
	}
	if _, err := NewSeasonService(newSeasonStub(), nil, usersWith()); !errors.Is(err, ErrNilMembershipRepository) {
		t.Fatalf("nil memberships error = %v", err)
	}
	if _, err := NewSeasonService(newSeasonStub(), newMembershipStub(), nil); !errors.Is(err, ErrNilUserRepository) {
		t.Fatalf("nil users error = %v", err)
	}
	var missing *SeasonService
	if _, err := missing.List(context.Background(), adminActor().Access); !errors.Is(err, ErrInvalidGroup) {
		t.Fatalf("nil service error = %v", err)
	}
	if _, err := missing.TeamIDs(context.Background(), 5, 7); !errors.Is(err, ErrInvalidGroup) {
		t.Fatalf("nil service TeamIDs error = %v", err)
	}
}

func TestBuildSeasonValidation(t *testing.T) {
	onePart := func(mutate func(*SeasonInput)) SeasonInput {
		input := validSeasonInput()
		input.Parts = input.Parts[:1]
		if mutate != nil {
			mutate(&input)
		}
		return input
	}
	twoParts := func(mutate func(*SeasonInput)) SeasonInput {
		input := validSeasonInput()
		mutate(&input)
		return input
	}
	tests := []struct {
		name         string
		input        SeasonInput
		wantErr      error
		wantPosition int
	}{
		{name: "two parts", input: validSeasonInput()},
		{name: "one part", input: onePart(nil)},
		{name: "one part covering the season", input: onePart(func(i *SeasonInput) { i.Parts[0].EndsOn = i.EndsOn })},
		{name: "empty name", input: onePart(func(i *SeasonInput) { i.Name = "  " }), wantErr: ErrInvalidSeasonName},
		{name: "long name", input: onePart(func(i *SeasonInput) { i.Name = strings.Repeat("a", 65) }), wantErr: ErrInvalidSeasonName},
		{name: "control character", input: onePart(func(i *SeasonInput) { i.Name = "a\tb" }), wantErr: ErrInvalidSeasonName},
		{name: "missing start", input: onePart(func(i *SeasonInput) { i.StartsOn = time.Time{} }), wantErr: ErrInvalidSeasonDates},
		{name: "missing end", input: onePart(func(i *SeasonInput) { i.EndsOn = time.Time{} }), wantErr: ErrInvalidSeasonDates},
		{name: "start equals end", input: onePart(func(i *SeasonInput) { i.EndsOn = i.StartsOn }), wantErr: ErrInvalidSeasonDates},
		{name: "start after end", input: onePart(func(i *SeasonInput) { i.StartsOn, i.EndsOn = i.EndsOn, i.StartsOn }), wantErr: ErrInvalidSeasonDates},
		{name: "year out of range", input: onePart(func(i *SeasonInput) { i.StartsOn = day("1899-12-31") }), wantErr: ErrInvalidSeasonDates},
		{name: "no parts", input: onePart(func(i *SeasonInput) { i.Parts = nil }), wantErr: ErrInvalidSeasonParts},
		{name: "three parts", input: twoParts(func(i *SeasonInput) { i.Parts = append(i.Parts, i.Parts[1]) }), wantErr: ErrInvalidSeasonParts},
		{name: "part 1 without name", input: onePart(func(i *SeasonInput) { i.Parts[0].Name = "" }), wantErr: ErrInvalidSeasonPartName, wantPosition: 1},
		{name: "part 2 without name", input: twoParts(func(i *SeasonInput) { i.Parts[1].Name = " " }), wantErr: ErrInvalidSeasonPartName, wantPosition: 2},
		{name: "part start equals end", input: onePart(func(i *SeasonInput) { i.Parts[0].EndsOn = i.Parts[0].StartsOn }), wantErr: ErrInvalidSeasonPartDates, wantPosition: 1},
		{name: "part 2 missing end", input: twoParts(func(i *SeasonInput) { i.Parts[1].EndsOn = time.Time{} }), wantErr: ErrInvalidSeasonPartDates, wantPosition: 2},
		{name: "part starts before the season", input: onePart(func(i *SeasonInput) { i.Parts[0].StartsOn = day("2026-07-31") }), wantErr: ErrSeasonPartOutsideSeason, wantPosition: 1},
		{name: "part ends after the season", input: twoParts(func(i *SeasonInput) { i.Parts[1].EndsOn = day("2027-07-01") }), wantErr: ErrSeasonPartOutsideSeason, wantPosition: 2},
		{name: "parts touch on the same day", input: twoParts(func(i *SeasonInput) { i.Parts[1].StartsOn = i.Parts[0].EndsOn }), wantErr: ErrSeasonPartsOverlap, wantPosition: 2},
		{name: "parts overlap", input: twoParts(func(i *SeasonInput) { i.Parts[1].StartsOn = day("2026-12-01") }), wantErr: ErrSeasonPartsOverlap, wantPosition: 2},
		{name: "parts out of order", input: twoParts(func(i *SeasonInput) { i.Parts[0], i.Parts[1] = i.Parts[1], i.Parts[0] }), wantErr: ErrSeasonPartsOverlap, wantPosition: 2},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			season, err := buildSeason(test.input)
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("buildSeason() error = %v, want %v", err, test.wantErr)
			}
			var partErr *SeasonPartError
			if errors.As(err, &partErr) != (test.wantPosition > 0) || (partErr != nil && partErr.Position != test.wantPosition) {
				t.Fatalf("part error = %v, want position %d", err, test.wantPosition)
			}
			if test.wantErr != nil {
				return
			}
			if season.Name != "2026/27" || len(season.Parts) != len(test.input.Parts) || season.Parts[0].Name != "Autumn" || season.Parts[0].Position != 1 {
				t.Fatalf("season = %+v", season)
			}
		})
	}
}

func TestSeasonServiceCreate(t *testing.T) {
	tests := []struct {
		name    string
		actor   GroupActor
		wantErr error
	}{
		{name: "admin creates", actor: adminActor()},
		{name: "manager creates", actor: actorWith(memberWith(GroupRoleManager))},
		{name: "site administrator creates", actor: siteAdminActor()},
		{name: "coach is forbidden", actor: coachOf(1), wantErr: ErrGroupForbidden},
		{name: "player is forbidden", actor: playerOn(1), wantErr: ErrGroupForbidden},
		{name: "parent is forbidden", actor: parentActor(), wantErr: ErrGroupForbidden},
		{name: "outsider is not found", actor: actorWith(GroupAccessGrants{}), wantErr: ErrGroupNotFound},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			seasons := newSeasonStub()
			service, auditor := newTestSeasonService(t, seasons)
			created, err := service.Create(context.Background(), test.actor, validSeasonInput())
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("Create() error = %v, want %v", err, test.wantErr)
			}
			if test.wantErr != nil {
				if len(seasons.seasons) != 0 || len(auditor.records) != 0 {
					t.Fatal("refused create stored or audited a season")
				}
				return
			}
			if created.GroupID != 5 || created.Name != "2026/27" || len(created.Parts) != 2 || !created.Parts[1].AlternatingWeeks || created.CreatedAt == 0 {
				t.Fatalf("created = %+v", created)
			}
			if len(auditor.records) != 1 {
				t.Fatalf("audit records = %d, want 1", len(auditor.records))
			}
			record := auditor.records[0]
			if record.Action != AuditActionSeasonCreated || record.TargetType != "group" || record.TargetID != "5" || record.IP != "192.0.2.1" {
				t.Fatalf("audit record = %+v", record)
			}
			if !strings.Contains(record.Detail, "season 99: 2026/27; 2026-08-01 to 2027-06-30") || !strings.Contains(record.Detail, "part 2 2027-01-04 to 2027-06-30 A/B") {
				t.Fatalf("audit detail = %q", record.Detail)
			}
			if strings.Contains(record.Detail, "(site administrator)") != (test.name == "site administrator creates") {
				t.Fatalf("site administrator marker in %q", record.Detail)
			}
		})
	}

	seasons := newSeasonStub()
	service, _ := newTestSeasonService(t, seasons)
	if _, err := service.Create(context.Background(), adminActor(), SeasonInput{Name: "X"}); !errors.Is(err, ErrInvalidSeasonDates) {
		t.Fatalf("invalid input error = %v", err)
	}
	for _, repoErr := range []error{ErrSeasonOverlap, ErrSeasonNameTaken, ErrSeasonPartsOverlap} {
		seasons.createErr = repoErr
		if _, err := service.Create(context.Background(), adminActor(), validSeasonInput()); !errors.Is(err, repoErr) {
			t.Errorf("repository error %v = %v", repoErr, err)
		}
	}
	errBoom := errors.New("boom")
	seasons.createErr = errBoom
	if _, err := service.Create(context.Background(), adminActor(), validSeasonInput()); !errors.Is(err, errBoom) || !strings.Contains(err.Error(), "create season") {
		t.Fatalf("storage error = %v", err)
	}
}

func TestSeasonServiceUpdateAndDelete(t *testing.T) {
	seasons := newSeasonStub(season2627())
	service, auditor := newTestSeasonService(t, seasons)
	input := validSeasonInput()
	input.Name = "Season 26/27"
	input.Parts = input.Parts[:1]
	updated, err := service.Update(context.Background(), adminActor(), 1, 3, input)
	if err != nil || updated.Name != "Season 26/27" || len(updated.Parts) != 1 || updated.ID != 1 || seasons.expectedVersion != 3 {
		t.Fatalf("Update() = %+v, %v (version %d)", updated, err, seasons.expectedVersion)
	}
	if auditor.records[0].Action != AuditActionSeasonUpdated {
		t.Fatalf("audit = %+v", auditor.records)
	}

	for _, test := range []struct {
		name    string
		actor   GroupActor
		id      int64
		version int64
		input   SeasonInput
		repoErr error
		wantErr error
	}{
		{name: "coach is forbidden", actor: coachOf(1), id: 1, input: validSeasonInput(), wantErr: ErrGroupForbidden},
		{name: "outsider is not found", actor: actorWith(GroupAccessGrants{}), id: 1, input: validSeasonInput(), wantErr: ErrGroupNotFound},
		{name: "unknown season", actor: adminActor(), id: 77, input: validSeasonInput(), wantErr: ErrSeasonNotFound},
		{name: "invalid id", actor: adminActor(), id: 0, input: validSeasonInput(), wantErr: ErrSeasonNotFound},
		{name: "negative version", actor: adminActor(), id: 1, version: -1, input: validSeasonInput(), wantErr: ErrInvalidGroup},
		{name: "invalid input", actor: adminActor(), id: 1, input: SeasonInput{Name: "X"}, wantErr: ErrInvalidSeasonDates},
		{name: "stale revision", actor: adminActor(), id: 1, input: validSeasonInput(), repoErr: ErrOptimisticLockConflict, wantErr: ErrOptimisticLockConflict},
		{name: "vanished season is not a conflict", actor: adminActor(), id: 1, input: validSeasonInput(), repoErr: ErrRecordNotFound, wantErr: ErrSeasonNotFound},
		{name: "overlap", actor: adminActor(), id: 1, input: validSeasonInput(), repoErr: ErrSeasonOverlap, wantErr: ErrSeasonOverlap},
	} {
		t.Run(test.name, func(t *testing.T) {
			seasons := newSeasonStub(season2627())
			seasons.updateErr = test.repoErr
			service, auditor := newTestSeasonService(t, seasons)
			if _, err := service.Update(context.Background(), test.actor, test.id, test.version, test.input); !errors.Is(err, test.wantErr) {
				t.Fatalf("Update() error = %v, want %v", err, test.wantErr)
			}
			if len(auditor.records) != 0 {
				t.Fatal("failed update was audited")
			}
		})
	}

	if err := service.Delete(context.Background(), coachOf(1), 1, 4); !errors.Is(err, ErrGroupForbidden) {
		t.Fatalf("coach delete error = %v", err)
	}
	seasons.deleteErr = ErrOptimisticLockConflict
	if err := service.Delete(context.Background(), adminActor(), 1, 3); !errors.Is(err, ErrOptimisticLockConflict) {
		t.Fatalf("stale delete error = %v", err)
	}
	seasons.deleteErr = ErrRecordNotFound
	if err := service.Delete(context.Background(), adminActor(), 1, 4); !errors.Is(err, ErrSeasonNotFound) {
		t.Fatalf("vanished delete error = %v, want not found", err)
	}
	seasons.deleteErr = nil
	if err := service.Delete(context.Background(), siteAdminActor(), 1, 4); err != nil || seasons.deleted != 1 || seasons.expectedVersion != 4 {
		t.Fatalf("Delete() error = %v (deleted %d, version %d)", err, seasons.deleted, seasons.expectedVersion)
	}
	last := auditor.records[len(auditor.records)-1]
	if last.Action != AuditActionSeasonDeleted || !strings.Contains(last.Detail, "season 1: Season 26/27") || !strings.Contains(last.Detail, "(site administrator)") {
		t.Fatalf("delete audit = %+v", last)
	}
	if err := service.Delete(context.Background(), adminActor(), 1, 5); !errors.Is(err, ErrSeasonNotFound) {
		t.Fatalf("deleted season error = %v", err)
	}
}

func TestSeasonServiceListFindAndParts(t *testing.T) {
	seasons := newSeasonStub(season2627(), season2728(), Season{ID: 3, GroupID: 6, Name: "Other group", StartsOn: day("2026-01-01"), EndsOn: day("2026-12-31")})
	service, _ := newTestSeasonService(t, seasons)
	for _, actor := range []GroupActor{adminActor(), siteAdminActor(), coachOf()} {
		list, err := service.List(context.Background(), actor.Access)
		if err != nil || len(list) != 2 || list[0].ID != 2 {
			t.Fatalf("List() = %+v, %v; want both seasons of the group, latest first", list, err)
		}
	}
	for _, actor := range []GroupActor{playerOn(1), parentActor()} {
		if _, err := service.List(context.Background(), actor.Access); !errors.Is(err, ErrGroupForbidden) {
			t.Fatalf("player List() error = %v, want forbidden", err)
		}
	}
	if _, err := service.List(context.Background(), actorWith(GroupAccessGrants{}).Access); !errors.Is(err, ErrGroupNotFound) {
		t.Fatalf("outsider List() error = %v", err)
	}

	// Every member may load a season and its parts by ID.
	if found, err := service.Find(context.Background(), playerOn(1).Access, 1); err != nil || found.Name != "2026/27" {
		t.Fatalf("Find() = %+v, %v", found, err)
	}
	if _, err := service.Find(context.Background(), adminActor().Access, 3); !errors.Is(err, ErrSeasonNotFound) {
		t.Fatalf("Find(other group) error = %v, want not found", err)
	}
	if _, err := service.Find(context.Background(), actorWith(GroupAccessGrants{}).Access, 1); !errors.Is(err, ErrGroupNotFound) {
		t.Fatalf("outsider Find() error = %v", err)
	}
	season, part, err := service.FindPart(context.Background(), coachOf().Access, 12)
	if err != nil || season.ID != 1 || part.Name != "Spring" || !part.AlternatingWeeks {
		t.Fatalf("FindPart() = %+v %+v, %v", season, part, err)
	}
	if _, _, err := service.FindPart(context.Background(), adminActor().Access, 0); !errors.Is(err, ErrSeasonNotFound) {
		t.Fatalf("FindPart(0) error = %v", err)
	}
	if _, _, err := service.FindPart(context.Background(), adminActor().Access, 404); !errors.Is(err, ErrSeasonNotFound) {
		t.Fatalf("FindPart(unknown) error = %v", err)
	}
}

// TestSeasonServiceCurrentAcrossTimeZones checks that the current season
// follows the group's calendar day, not the server's.
func TestSeasonServiceCurrentAcrossTimeZones(t *testing.T) {
	tests := []struct {
		name    string
		zone    string
		instant time.Time
		wantID  int64
		today   string
	}{
		// 21:30 UTC on the last day: still 23:30 that day in Budapest
		// (summer time), already the next morning in Auckland.
		{name: "Budapest late on the last day", zone: "Europe/Budapest", instant: time.Date(2027, 6, 30, 21, 30, 0, 0, time.UTC), wantID: 1, today: "2027-06-30"},
		{name: "Auckland the next morning", zone: "Pacific/Auckland", instant: time.Date(2027, 6, 30, 21, 30, 0, 0, time.UTC), wantID: 2, today: "2027-07-01"},
		// 23:30 UTC: past midnight in Budapest, still the last day in Los Angeles.
		{name: "Budapest after midnight", zone: "Europe/Budapest", instant: time.Date(2027, 6, 30, 23, 30, 0, 0, time.UTC), wantID: 2, today: "2027-07-01"},
		{name: "Los Angeles still on the last day", zone: "America/Los_Angeles", instant: time.Date(2027, 6, 30, 23, 30, 0, 0, time.UTC), wantID: 1, today: "2027-06-30"},
		{name: "UTC on the first day", zone: "UTC", instant: time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC), wantID: 1, today: "2026-08-01"},
		{name: "before any season", zone: "Pacific/Auckland", instant: time.Date(2026, 7, 31, 11, 59, 0, 0, time.UTC), wantID: 0, today: "2026-07-31"},
		{name: "first day in Auckland while UTC is the day before", zone: "Pacific/Auckland", instant: time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC), wantID: 1, today: "2026-08-01"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			seasons := newSeasonStub(season2627(), season2728())
			seasons.zone = test.zone
			service, _ := newTestSeasonService(t, seasons)
			service.now = func() time.Time { return test.instant }
			current, found, err := service.Current(context.Background(), playerOn(1).Access)
			if err != nil {
				t.Fatalf("Current() error = %v", err)
			}
			if found != (test.wantID != 0) || current.ID != test.wantID {
				t.Fatalf("Current() = %d (found %v), want %d", current.ID, found, test.wantID)
			}
			today, err := service.Today(context.Background(), playerOn(1).Access)
			if err != nil || FormatSeasonDate(today) != test.today {
				t.Fatalf("Today() = %v, %v, want %s", today, err, test.today)
			}
		})
	}

	seasons := newSeasonStub(season2627())
	service, _ := newTestSeasonService(t, seasons)
	if _, _, err := service.Current(context.Background(), actorWith(GroupAccessGrants{}).Access); !errors.Is(err, ErrGroupNotFound) {
		t.Fatalf("outsider Current() error = %v", err)
	}
	seasons.zone = "Mars/Olympus"
	if _, _, err := service.Current(context.Background(), adminActor().Access); !errors.Is(err, ErrInvalidTimeZone) {
		t.Fatalf("invalid zone error = %v", err)
	}
	seasons.zone, seasons.zoneErr = "UTC", ErrGroupNotFound
	if _, err := service.Today(context.Background(), adminActor().Access); !errors.Is(err, ErrGroupNotFound) {
		t.Fatalf("missing group error = %v", err)
	}
}

func TestSeasonServiceTeamIDsUsesGroupDay(t *testing.T) {
	seasons := newSeasonStub(season2627(), season2728())
	seasons.zone = "Pacific/Auckland"
	seasons.coaches = []SeasonCoach{
		{GroupID: 5, SeasonID: 1, TeamID: 4, CoachUserID: 7},
		{GroupID: 5, SeasonID: 1, TeamID: 3, CoachUserID: 7, IsHead: true},
		{GroupID: 5, SeasonID: 2, TeamID: 8, CoachUserID: 7},
		{GroupID: 5, SeasonID: 1, TeamID: 9, CoachUserID: 8},
	}
	service, _ := newTestSeasonService(t, seasons)
	service.now = func() time.Time { return time.Date(2027, 6, 30, 10, 0, 0, 0, time.UTC) }
	ids, err := service.TeamIDs(context.Background(), 5, 7)
	if err != nil || !slices.Equal(ids, []int64{3, 4}) {
		t.Fatalf("TeamIDs() = %v, %v; want the 2026/27 teams", ids, err)
	}
	// Two hours later it is 1 July in Auckland: the next season's team.
	service.now = func() time.Time { return time.Date(2027, 6, 30, 12, 0, 0, 0, time.UTC) }
	ids, err = service.TeamIDs(context.Background(), 5, 7)
	if err != nil || !slices.Equal(ids, []int64{8}) || FormatSeasonDate(seasons.coachedDays[1]) != "2027-07-01" {
		t.Fatalf("TeamIDs() = %v, %v (day %v); want the 2027/28 team", ids, err, seasons.coachedDays)
	}
	if ids, err := service.TeamIDs(context.Background(), 0, 7); err != nil || ids != nil {
		t.Fatalf("TeamIDs(invalid group) = %v, %v", ids, err)
	}
	seasons.zoneErr = errors.New("boom")
	if _, err := service.TeamIDs(context.Background(), 5, 7); err == nil {
		t.Fatal("TeamIDs() ignored a time zone lookup failure")
	}
}

// TestSeasonCoachesGrantRosterAndInvites wires SeasonService into
// GroupAccessService the way internal/app does and checks that a season
// coach can manage the roster and player invites of the team only while
// the season is current.
func TestSeasonCoachesGrantRosterAndInvites(t *testing.T) {
	seasons := newSeasonStub(season2627())
	seasons.coaches = []SeasonCoach{{GroupID: 5, SeasonID: 1, TeamID: 4, CoachUserID: 7}}
	seasonService, _ := newTestSeasonService(t, seasons)
	repository := &groupAccessRepositoryStub{access: MemberAccess{Roles: []string{GroupRoleCoach}}}
	accessService, err := NewGroupAccessService(repository, usersWith(User{ID: 7}))
	if err != nil {
		t.Fatalf("NewGroupAccessService() error = %v", err)
	}
	accessService.SetCoachedTeams(seasonService)

	access, err := accessService.For(context.Background(), 7, 5)
	if err != nil {
		t.Fatalf("For() error = %v", err)
	}
	if !access.CoachesTeam(4) || !access.CanManageRoster(4) || !access.CanViewTeam(4) || !access.CanViewTeamEmails(4) {
		t.Fatal("season coach does not manage the coached team")
	}
	if !access.CanCreateInvite(GroupRolePlayer, 4) || !access.CanCreateInvite(GroupRoleParent, 4) || access.CanCreateInvite(GroupRoleCoach, 4) || access.CanCreateInvite(GroupRolePlayer, 0) {
		t.Fatal("season coach invite rights wrong")
	}
	if access.CanManageRoster(5) || access.CanCreateInvite(GroupRolePlayer, 5) || access.CanManageSeasons() || !access.CanViewSeasons() {
		t.Fatal("season coach got rights beyond the coached team")
	}

	// After the season ends the assignment no longer counts.
	seasonService.now = func() time.Time { return time.Date(2027, 7, 2, 12, 0, 0, 0, time.UTC) }
	access, err = accessService.For(context.Background(), 7, 5)
	if err != nil || access.CoachesTeam(4) || access.CanManageRoster(4) {
		t.Fatalf("past season still grants roster rights (err %v)", err)
	}

	// A member without the coach role gets nothing from assignments.
	seasonService.now = func() time.Time { return time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC) }
	repository.access = MemberAccess{Roles: []string{GroupRolePlayer}}
	access, err = accessService.For(context.Background(), 7, 5)
	if err != nil || access.CoachesTeam(4) || len(seasons.coachedDays) != 2 {
		t.Fatalf("non-coach got coached teams (err %v, lookups %d)", err, len(seasons.coachedDays))
	}
}

func TestSeasonServiceTeamCoaching(t *testing.T) {
	email := "coach@example.test"
	other := "other@example.test"
	coaches := []SeasonCoach{
		{GroupID: 5, SeasonID: 1, TeamID: 4, CoachUserID: 8, IsHead: true, Email: &email, SeasonName: "2026/27"},
		{GroupID: 5, SeasonID: 2, TeamID: 4, CoachUserID: 9, Email: &other, SeasonName: "2027/28"},
		{GroupID: 5, SeasonID: 1, TeamID: 6, CoachUserID: 9, Email: &other},
	}
	members := []GroupMember{{UserID: 8, Roles: []string{GroupRoleCoach}}, {UserID: 9, Roles: []string{GroupRoleCoach, GroupRolePlayer}}, {UserID: 10, Roles: []string{GroupRolePlayer}}}
	tests := []struct {
		name           string
		actor          GroupActor
		wantErr        error
		wantSeasons    []int64
		wantCoaches    int
		wantEmails     bool
		wantCandidates int
	}{
		{name: "admin sees every season and the candidates", actor: adminActor(), wantSeasons: []int64{2, 1}, wantCoaches: 2, wantEmails: true, wantCandidates: 2},
		{name: "site administrator assigns too", actor: siteAdminActor(), wantSeasons: []int64{2, 1}, wantCoaches: 2, wantEmails: true, wantCandidates: 2},
		{name: "coach of the team reads every season", actor: coachOf(4), wantSeasons: []int64{2, 1}, wantCoaches: 2, wantEmails: true},
		{name: "player on the team sees the current coaches only", actor: playerOn(4), wantSeasons: []int64{1}, wantCoaches: 1},
		{name: "player elsewhere gets not found", actor: playerOn(5), wantErr: ErrTeamNotFound},
		{name: "outsider gets not found", actor: actorWith(GroupAccessGrants{}), wantErr: ErrGroupNotFound},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			seasons := newSeasonStub(season2627(), season2728())
			seasons.coaches = coaches
			service, _ := newTestSeasonService(t, seasons, members...)
			coaching, err := service.TeamCoaching(context.Background(), test.actor.Access, 4)
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("TeamCoaching() error = %v, want %v", err, test.wantErr)
			}
			if test.wantErr != nil {
				return
			}
			ids := make([]int64, 0, len(coaching.Seasons))
			for _, season := range coaching.Seasons {
				ids = append(ids, season.ID)
			}
			if !slices.Equal(ids, test.wantSeasons) || coaching.CurrentSeasonID != 1 {
				t.Fatalf("seasons = %v (current %d), want %v", ids, coaching.CurrentSeasonID, test.wantSeasons)
			}
			if len(coaching.Coaches) != test.wantCoaches || len(coaching.Candidates) != test.wantCandidates {
				t.Fatalf("coaches %d candidates %d, want %d and %d", len(coaching.Coaches), len(coaching.Candidates), test.wantCoaches, test.wantCandidates)
			}
			for _, coach := range coaching.Coaches {
				if coach.TeamID != 4 || (coach.Email != nil) != test.wantEmails {
					t.Fatalf("coach %+v, want team 4 with emails %v", coach, test.wantEmails)
				}
			}
		})
	}

	// Players see no coaches when no season is current.
	seasons := newSeasonStub(season2728())
	seasons.coaches = coaches
	service, _ := newTestSeasonService(t, seasons, members...)
	coaching, err := service.TeamCoaching(context.Background(), playerOn(4).Access, 4)
	if err != nil || len(coaching.Seasons) != 0 || len(coaching.Coaches) != 0 || coaching.CurrentSeasonID != 0 {
		t.Fatalf("TeamCoaching() without a current season = %+v, %v", coaching, err)
	}
}

func TestSeasonServiceSeasonCoaches(t *testing.T) {
	email := "coach@example.test"
	seasons := newSeasonStub(season2627())
	seasons.coaches = []SeasonCoach{
		{GroupID: 5, SeasonID: 1, TeamID: 4, CoachUserID: 8, Email: &email},
		{GroupID: 5, SeasonID: 1, TeamID: 6, CoachUserID: 9, Email: &email},
	}
	service, _ := newTestSeasonService(t, seasons)
	list, err := service.SeasonCoaches(context.Background(), adminActor().Access, 1)
	if err != nil || len(list) != 2 || list[0].Email == nil || list[1].Email == nil {
		t.Fatalf("admin SeasonCoaches() = %+v, %v", list, err)
	}
	list, err = service.SeasonCoaches(context.Background(), coachOf(4).Access, 1)
	if err != nil || len(list) != 2 || list[0].Email == nil || list[1].Email != nil {
		t.Fatalf("coach SeasonCoaches() = %+v, %v; want emails of the coached team only", list, err)
	}
	if _, err := service.SeasonCoaches(context.Background(), playerOn(4).Access, 1); !errors.Is(err, ErrGroupForbidden) {
		t.Fatalf("player SeasonCoaches() error = %v", err)
	}
	if _, err := service.SeasonCoaches(context.Background(), adminActor().Access, 2); !errors.Is(err, ErrSeasonNotFound) {
		t.Fatalf("unknown season error = %v", err)
	}
	if _, err := service.SeasonCoaches(context.Background(), adminActor().Access, 0); !errors.Is(err, ErrSeasonNotFound) {
		t.Fatalf("invalid season error = %v", err)
	}
}

func TestSeasonServiceAssignAndRemoveCoach(t *testing.T) {
	tests := []struct {
		name    string
		actor   GroupActor
		input   CoachAssignment
		repoErr error
		wantErr error
	}{
		{name: "admin assigns a head coach", actor: adminActor(), input: CoachAssignment{SeasonID: 1, TeamID: 4, CoachUserID: 8, Head: true}},
		{name: "manager assigns", actor: actorWith(memberWith(GroupRoleManager)), input: CoachAssignment{SeasonID: 1, TeamID: 4, CoachUserID: 8}},
		{name: "site administrator assigns", actor: siteAdminActor(), input: CoachAssignment{SeasonID: 1, TeamID: 4, CoachUserID: 8}},
		{name: "coach of the team is forbidden", actor: coachOf(4), input: CoachAssignment{SeasonID: 1, TeamID: 4, CoachUserID: 8}, wantErr: ErrGroupForbidden},
		{name: "player on the team is forbidden", actor: playerOn(4), input: CoachAssignment{SeasonID: 1, TeamID: 4, CoachUserID: 8}, wantErr: ErrGroupForbidden},
		{name: "player elsewhere gets not found", actor: playerOn(5), input: CoachAssignment{SeasonID: 1, TeamID: 4, CoachUserID: 8}, wantErr: ErrTeamNotFound},
		{name: "outsider gets not found", actor: actorWith(GroupAccessGrants{}), input: CoachAssignment{SeasonID: 1, TeamID: 4, CoachUserID: 8}, wantErr: ErrGroupNotFound},
		{name: "invalid season", actor: adminActor(), input: CoachAssignment{TeamID: 4, CoachUserID: 8}, wantErr: ErrSeasonNotFound},
		{name: "invalid coach", actor: adminActor(), input: CoachAssignment{SeasonID: 1, TeamID: 4}, wantErr: ErrSeasonCoachRoleMissing},
		{name: "member without the coach role", actor: adminActor(), input: CoachAssignment{SeasonID: 1, TeamID: 4, CoachUserID: 10}, repoErr: ErrSeasonCoachRoleMissing, wantErr: ErrSeasonCoachRoleMissing},
		{name: "season of another group", actor: adminActor(), input: CoachAssignment{SeasonID: 3, TeamID: 4, CoachUserID: 8}, repoErr: ErrSeasonNotFound, wantErr: ErrSeasonNotFound},
		{name: "team of another group", actor: adminActor(), input: CoachAssignment{SeasonID: 1, TeamID: 40, CoachUserID: 8}, repoErr: ErrTeamNotFound, wantErr: ErrTeamNotFound},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			seasons := newSeasonStub(season2627())
			seasons.assignErr = test.repoErr
			service, auditor := newTestSeasonService(t, seasons)
			err := service.AssignCoach(context.Background(), test.actor, test.input)
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("AssignCoach() error = %v, want %v", err, test.wantErr)
			}
			if test.wantErr != nil {
				if len(auditor.records) != 0 || (test.repoErr == nil && len(seasons.assigned) != 0) {
					t.Fatal("refused assignment was stored or audited")
				}
				return
			}
			if len(seasons.assigned) != 1 || seasons.assigned[0] != test.input {
				t.Fatalf("assigned = %+v", seasons.assigned)
			}
			record := auditor.records[0]
			if record.Action != AuditActionSeasonCoachAssigned || record.TargetID != "5" || !strings.HasPrefix(record.Detail, "season 1; team 4; coach 8") || strings.Contains(record.Detail, "head coach") != test.input.Head {
				t.Fatalf("audit = %+v", record)
			}
		})
	}

	seasons := newSeasonStub(season2627())
	service, auditor := newTestSeasonService(t, seasons)
	if err := service.RemoveCoach(context.Background(), coachOf(4), 1, 4, 8); !errors.Is(err, ErrGroupForbidden) {
		t.Fatalf("coach RemoveCoach() error = %v", err)
	}
	if err := service.RemoveCoach(context.Background(), adminActor(), 1, 4, 8); err != nil || seasons.removed != 1 {
		t.Fatalf("RemoveCoach() error = %v", err)
	}
	if auditor.records[0].Action != AuditActionSeasonCoachRemoved || auditor.records[0].Detail != "season 1; team 4; coach 8" {
		t.Fatalf("remove audit = %+v", auditor.records[0])
	}
	seasons.removeErr = ErrSeasonCoachNotFound
	if err := service.RemoveCoach(context.Background(), adminActor(), 1, 4, 8); !errors.Is(err, ErrSeasonCoachNotFound) {
		t.Fatalf("missing assignment error = %v", err)
	}
	errBoom := errors.New("boom")
	seasons.removeErr = errBoom
	if err := service.RemoveCoach(context.Background(), adminActor(), 1, 4, 8); !errors.Is(err, errBoom) {
		t.Fatalf("storage error = %v", err)
	}
	seasons.assignErr = errBoom
	if err := service.AssignCoach(context.Background(), adminActor(), CoachAssignment{SeasonID: 1, TeamID: 4, CoachUserID: 8}); !errors.Is(err, errBoom) {
		t.Fatalf("assign storage error = %v", err)
	}
	if len(auditor.records) != 1 {
		t.Fatalf("failed writes were audited: %d records", len(auditor.records))
	}
}

func TestSeasonDates(t *testing.T) {
	season := season2627()
	part, ok := season.PartOn(day("2027-02-10"))
	if !ok || part.Position != 2 || !season.Contains(day("2026-08-01")) || !season.Contains(day("2027-06-30")) || season.Contains(day("2027-07-01")) {
		t.Fatalf("PartOn/Contains wrong: %+v %v", part, ok)
	}
	if _, ok := season.PartOn(day("2026-12-25")); ok {
		t.Fatal("PartOn() found a part in the winter break")
	}
	if _, ok := season.Part(404); ok {
		t.Fatal("Part() found an unknown part")
	}

	// Part 2 starts on Monday 2027-01-04; alternate weeks from there.
	spring := season.Parts[1]
	for raw, want := range map[string]string{
		"2027-01-04": SeasonWeekA, "2027-01-10": SeasonWeekA, "2027-01-11": SeasonWeekB,
		"2027-01-17": SeasonWeekB, "2027-01-18": SeasonWeekA, "2027-06-30": SeasonWeekB, "2027-07-01": "",
	} {
		if got := spring.WeekOn(day(raw)); got != want {
			t.Errorf("WeekOn(%s) = %q, want %q", raw, got, want)
		}
	}
	// A part starting mid-week: that whole Monday-based week is A.
	midweek := SeasonPart{StartsOn: day("2026-08-05"), EndsOn: day("2026-12-20"), AlternatingWeeks: true}
	for raw, want := range map[string]string{"2026-08-05": SeasonWeekA, "2026-08-09": SeasonWeekA, "2026-08-10": SeasonWeekB, "2026-08-04": ""} {
		if got := midweek.WeekOn(day(raw)); got != want {
			t.Errorf("mid-week WeekOn(%s) = %q, want %q", raw, got, want)
		}
	}
	if got := season.Parts[0].WeekOn(day("2026-09-01")); got != SeasonWeekAll {
		t.Fatalf("non-alternating WeekOn() = %q", got)
	}

	if !ParseSeasonDate("bad").IsZero() || !ParseSeasonDate("2026-02-30").IsZero() || FormatSeasonDate(ParseSeasonDate(" 2026-02-28 ")) != "2026-02-28" || FormatSeasonDate(time.Time{}) != "" {
		t.Fatal("ParseSeasonDate/FormatSeasonDate wrong")
	}
	local := time.Date(2026, 10, 24, 23, 30, 0, 0, time.FixedZone("x", 2*3600))
	if FormatSeasonDate(SeasonDate(local)) != "2026-10-24" || !SeasonDate(time.Time{}).IsZero() {
		t.Fatal("SeasonDate() must keep the calendar date of its location")
	}
	for _, zone := range []string{"", "Local", "Mars/Olympus"} {
		if _, err := LocalDate(time.Now(), zone); !errors.Is(err, ErrInvalidTimeZone) {
			t.Errorf("LocalDate(%q) error = %v", zone, err)
		}
	}
}

func TestSeasonAccessQuestions(t *testing.T) {
	for _, test := range []struct {
		grants         GroupAccessGrants
		manage, viewIt bool
	}{
		{memberWith(GroupRoleAdmin), true, true},
		{memberWith(GroupRoleManager), true, true},
		{GroupAccessGrants{SiteAdmin: true}, true, true},
		{memberWith(GroupRoleCoach), false, true},
		{memberWith(GroupRolePlayer), false, false},
		{memberWith(GroupRoleParent), false, false},
		{memberWith(), false, false},
		{GroupAccessGrants{}, false, false},
	} {
		access := accessFor(test.grants)
		if access.CanManageSeasons() != test.manage || access.CanViewSeasons() != test.viewIt {
			t.Errorf("grants %+v: manage %v view %v, want %v %v", test.grants, access.CanManageSeasons(), access.CanViewSeasons(), test.manage, test.viewIt)
		}
	}
}
