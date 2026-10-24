package service

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
)

// coachRepositoryStub keeps qualifications and windows of group 5 in
// memory. Coaches are the members with the coach role in coachIDs; parts
// are those listed in parts.
type coachRepositoryStub struct {
	coachIDs       []int64
	parts          []int64
	qualifications map[int64]CoachQualification
	windows        []AvailabilityWindow
	nextID         int64
	setErr         error
	edits          int
}

func newCoachStub(coachIDs []int64, parts []int64) *coachRepositoryStub {
	return &coachRepositoryStub{coachIDs: coachIDs, parts: parts, qualifications: map[int64]CoachQualification{}, nextID: 100}
}

func (r *coachRepositoryStub) ListQualifications(_ context.Context, groupID int64) ([]CoachQualification, error) {
	list := make([]CoachQualification, 0)
	for _, q := range r.qualifications {
		if q.GroupID == groupID {
			list = append(list, q)
		}
	}
	slices.SortFunc(list, func(a, b CoachQualification) int { return int(a.CoachUserID - b.CoachUserID) })
	return list, nil
}

func (r *coachRepositoryStub) SetQualification(_ context.Context, q CoachQualification, _ int64) error {
	if r.setErr != nil {
		return r.setErr
	}
	if !slices.Contains(r.coachIDs, q.CoachUserID) {
		return ErrCoachNotFound
	}
	r.qualifications[q.CoachUserID] = q
	return nil
}

func (r *coachRepositoryStub) ClearQualification(_ context.Context, _, coachUserID int64) error {
	delete(r.qualifications, coachUserID)
	return nil
}

func (r *coachRepositoryStub) ListAvailability(_ context.Context, groupID, partID, coachUserID int64) ([]AvailabilityWindow, error) {
	list := make([]AvailabilityWindow, 0)
	for _, w := range r.windows {
		if w.GroupID == groupID && w.SeasonPartID == partID && (coachUserID == 0 || w.CoachUserID == coachUserID) {
			list = append(list, w)
		}
	}
	return list, nil
}

func (r *coachRepositoryStub) EditAvailability(ctx context.Context, groupID, partID, coachUserID, _ int64, edit func([]AvailabilityWindow) ([]AvailabilityWindow, error)) error {
	if !slices.Contains(r.coachIDs, coachUserID) {
		return ErrCoachNotFound
	}
	if !slices.Contains(r.parts, partID) {
		return ErrSeasonNotFound
	}
	current, _ := r.ListAvailability(ctx, groupID, partID, coachUserID)
	changed, err := edit(current)
	if err != nil {
		return err
	}
	r.edits++
	r.windows = slices.DeleteFunc(r.windows, func(w AvailabilityWindow) bool {
		return w.GroupID == groupID && w.SeasonPartID == partID && w.CoachUserID == coachUserID
	})
	for _, w := range changed {
		r.nextID++
		w.ID, w.GroupID, w.SeasonPartID, w.CoachUserID = r.nextID, groupID, partID, coachUserID
		r.windows = append(r.windows, w)
	}
	return nil
}

// newTestCoachService has coaches 7 (the acting user) and 8, player 9, and
// the seasons 2026/27 (parts 11 and 12, 12 alternating) and 2027/28 (21).
func newTestCoachService(t *testing.T) (*CoachService, *coachRepositoryStub, *auditRecorderStub) {
	t.Helper()
	seasons := newSeasonStub(season2627(), season2728())
	seasonService, _ := newTestSeasonService(t, seasons)
	memberships := newMembershipStub()
	coachName, otherName := "Carla", "Otto"
	memberships.members = []GroupMember{
		{UserID: 7, Roles: []string{GroupRoleCoach}, DisplayName: &coachName, Email: strPtr("carla@example.test")},
		{UserID: 8, Roles: []string{GroupRoleCoach, GroupRolePlayer}, DisplayName: &otherName, Email: strPtr("otto@example.test")},
		{UserID: 9, Roles: []string{GroupRolePlayer}},
	}
	repository := newCoachStub([]int64{7, 8}, []int64{11, 12, 21})
	service, err := NewCoachService(repository, seasonService, memberships, usersWith(User{ID: 7, Email: strPtr("staff@example.test")}))
	if err != nil {
		t.Fatalf("NewCoachService() error = %v", err)
	}
	auditor := &auditRecorderStub{}
	service.SetAuditor(auditor)
	return service, repository, auditor
}

func selfCoachActor() GroupActor {
	return actorWith(memberWith(GroupRoleCoach))
}

func TestNewCoachServiceRejectsNilDependencies(t *testing.T) {
	seasons, _ := newTestSeasonService(t, newSeasonStub())
	repository := newCoachStub(nil, nil)
	users := usersWith()
	memberships := newMembershipStub()
	for name, test := range map[string]struct {
		build func() (*CoachService, error)
		want  error
	}{
		"repository":  {func() (*CoachService, error) { return NewCoachService(nil, seasons, memberships, users) }, ErrNilCoachRepository},
		"seasons":     {func() (*CoachService, error) { return NewCoachService(repository, nil, memberships, users) }, ErrNilCoachSeasons},
		"memberships": {func() (*CoachService, error) { return NewCoachService(repository, seasons, nil, users) }, ErrNilMembershipRepository},
		"users":       {func() (*CoachService, error) { return NewCoachService(repository, seasons, memberships, nil) }, ErrNilUserRepository},
	} {
		if _, err := test.build(); !errors.Is(err, test.want) {
			t.Errorf("%s: error = %v, want %v", name, err, test.want)
		}
	}
	var nilService *CoachService
	if _, err := nilService.Coaches(context.Background(), accessFor(memberWith(GroupRoleAdmin))); !errors.Is(err, ErrInvalidGroup) {
		t.Fatalf("nil service error = %v", err)
	}
}

func TestTeamAgeRange(t *testing.T) {
	season := Season{StartsOn: day("2026-08-01")}
	for _, test := range []struct {
		from, to int
		want     AgeRange
	}{
		{2015, 2016, AgeRange{Min: 10, Max: 11}},
		{2016, 2016, AgeRange{Min: 10, Max: 10}},
		{2016, 2015, AgeRange{Min: 10, Max: 11}},
		{1990, 2008, AgeRange{Min: 18, Max: 36}},
	} {
		if got := TeamAgeRange(Team{BirthYearFrom: test.from, BirthYearTo: test.to}, season); got != test.want {
			t.Errorf("TeamAgeRange(%d-%d) = %+v, want %+v", test.from, test.to, got, test.want)
		}
	}
	// A season starting in January counts from that year.
	if got := TeamAgeRange(Team{BirthYearFrom: 2015, BirthYearTo: 2016}, Season{StartsOn: day("2027-01-04")}); got != (AgeRange{Min: 11, Max: 12}) {
		t.Errorf("January season = %+v", got)
	}
}

func TestQualificationCoversWholeRange(t *testing.T) {
	q := CoachQualification{MinAge: 9, MaxAge: 12}
	for ages, want := range map[AgeRange]bool{
		{Min: 9, Max: 12}:  true,
		{Min: 10, Max: 11}: true,
		{Min: 8, Max: 10}:  false,
		{Min: 11, Max: 13}: false,
		{Min: 13, Max: 14}: false,
		{Min: 12, Max: 12}: true,
	} {
		if q.Covers(ages) != want {
			t.Errorf("Covers(%+v) = %v, want %v", ages, !want, want)
		}
	}
	if q.Range() != (AgeRange{Min: 9, Max: 12}) {
		t.Fatalf("Range() = %+v", q.Range())
	}
}

func TestNormalizeAvailabilityMergesOverlapsAndTouches(t *testing.T) {
	windows := []AvailabilityWindow{
		{ID: 3, CoachUserID: 7, Week: SeasonWeekAll, Weekday: 1, StartMinute: 17 * 60, EndMinute: 18 * 60},
		{ID: 1, CoachUserID: 7, Week: SeasonWeekAll, Weekday: 1, StartMinute: 16 * 60, EndMinute: 17 * 60},
		{ID: 2, CoachUserID: 7, Week: SeasonWeekAll, Weekday: 1, StartMinute: 16*60 + 30, EndMinute: 16*60 + 45},
		{ID: 4, CoachUserID: 7, Week: SeasonWeekAll, Weekday: 1, StartMinute: 19 * 60, EndMinute: 20 * 60},
		{ID: 5, CoachUserID: 7, Week: SeasonWeekA, Weekday: 1, StartMinute: 16 * 60, EndMinute: 21 * 60},
		{ID: 6, CoachUserID: 7, Week: SeasonWeekAll, Weekday: 2, StartMinute: 16 * 60, EndMinute: 17 * 60},
		{ID: 7, CoachUserID: 8, Week: SeasonWeekAll, Weekday: 1, StartMinute: 17 * 60, EndMinute: 19 * 60},
	}
	got := NormalizeAvailability(windows)
	want := []AvailabilityWindow{
		{ID: 1, CoachUserID: 7, Week: SeasonWeekAll, Weekday: 1, StartMinute: 16 * 60, EndMinute: 18 * 60},
		{ID: 4, CoachUserID: 7, Week: SeasonWeekAll, Weekday: 1, StartMinute: 19 * 60, EndMinute: 20 * 60},
		{ID: 6, CoachUserID: 7, Week: SeasonWeekAll, Weekday: 2, StartMinute: 16 * 60, EndMinute: 17 * 60},
		{ID: 5, CoachUserID: 7, Week: SeasonWeekA, Weekday: 1, StartMinute: 16 * 60, EndMinute: 21 * 60},
		{ID: 7, CoachUserID: 8, Week: SeasonWeekAll, Weekday: 1, StartMinute: 17 * 60, EndMinute: 19 * 60},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("NormalizeAvailability() =\n%+v\nwant\n%+v", got, want)
	}
	if windows[0].ID != 3 {
		t.Fatal("NormalizeAvailability changed its input")
	}
}

func TestPartAvailabilityCovers(t *testing.T) {
	windows := []AvailabilityWindow{
		{CoachUserID: 7, Week: SeasonWeekAll, Weekday: 1, StartMinute: 16 * 60, EndMinute: 17 * 60},
		{CoachUserID: 7, Week: SeasonWeekA, Weekday: 1, StartMinute: 17 * 60, EndMinute: 19 * 60},
		{CoachUserID: 7, Week: SeasonWeekB, Weekday: 2, StartMinute: 9 * 60, EndMinute: 12 * 60},
		{CoachUserID: 8, Week: SeasonWeekAll, Weekday: 3, StartMinute: 9 * 60, EndMinute: 10 * 60},
	}
	alternating := PartAvailability{Part: SeasonPart{AlternatingWeeks: true}, Windows: windows}
	plain := PartAvailability{Part: SeasonPart{}, Windows: windows}
	for _, test := range []struct {
		name               string
		view               PartAvailability
		coach              int64
		week               string
		weekday, from, end int
		want               bool
	}{
		{"every-week window alone", plain, 7, SeasonWeekAll, 1, 16 * 60, 17 * 60, true},
		{"inside a window", plain, 7, SeasonWeekAll, 1, 16*60 + 15, 16*60 + 45, true},
		{"past the window", plain, 7, SeasonWeekAll, 1, 16 * 60, 18 * 60, false},
		{"A/B windows do not count without alternation", plain, 7, SeasonWeekA, 1, 17 * 60, 18 * 60, false},
		{"week A joins every-week and A windows", alternating, 7, SeasonWeekA, 1, 16 * 60, 19 * 60, true},
		{"week B lacks the A window", alternating, 7, SeasonWeekB, 1, 16 * 60, 19 * 60, false},
		{"every-week slot needs both weeks", alternating, 7, SeasonWeekAll, 1, 16 * 60, 18 * 60, false},
		{"every-week slot inside the shared window", alternating, 7, SeasonWeekAll, 1, 16 * 60, 17 * 60, true},
		{"week B window", alternating, 7, SeasonWeekB, 2, 10 * 60, 11 * 60, true},
		{"another coach's window", plain, 7, SeasonWeekAll, 3, 9 * 60, 10 * 60, false},
		{"other coach", plain, 8, SeasonWeekAll, 3, 9 * 60, 10 * 60, true},
		{"empty slot", plain, 7, SeasonWeekAll, 1, 16 * 60, 16 * 60, false},
	} {
		if got := test.view.Covers(test.coach, test.week, test.weekday, test.from, test.end); got != test.want {
			t.Errorf("%s: Covers() = %v, want %v", test.name, got, test.want)
		}
	}
	if len(alternating.ForCoach(7)) != 3 || len(alternating.ForCoach(9)) != 0 {
		t.Fatal("ForCoach() filtered wrongly")
	}
}

func TestDefaultAndPreviousSeasonPart(t *testing.T) {
	seasons := []Season{season2728(), season2627()}
	for raw, want := range map[string]int64{
		"2026-10-24": 11, // inside part 1
		"2026-12-25": 12, // winter break: the next part
		"2027-03-01": 12,
		"2026-01-01": 11, // before every season: the first
		"2029-01-01": 21, // after every season: the last
	} {
		_, part, ok := DefaultSeasonPart(seasons, day(raw))
		if !ok || part.ID != want {
			t.Errorf("DefaultSeasonPart(%s) = %d, %v, want %d", raw, part.ID, ok, want)
		}
	}
	if _, _, ok := DefaultSeasonPart(nil, day("2026-10-24")); ok {
		t.Fatal("DefaultSeasonPart() without seasons found a part")
	}
	for partID, want := range map[int64]int64{11: 0, 12: 11, 21: 12, 99: 0} {
		season, part, ok := PreviousSeasonPart(seasons, partID)
		if ok != (want != 0) || part.ID != want {
			t.Errorf("PreviousSeasonPart(%d) = %d, %v, want %d", partID, part.ID, ok, want)
		}
		if want == 12 && season.ID != 1 {
			t.Errorf("PreviousSeasonPart(21) season = %d", season.ID)
		}
	}
}

func TestCoachServiceCoachesAndQualifications(t *testing.T) {
	service, repository, auditor := newTestCoachService(t)
	ctx := context.Background()
	repository.qualifications[8] = CoachQualification{GroupID: 5, CoachUserID: 8, MinAge: 9, MaxAge: 12}

	staff, err := service.Coaches(ctx, accessFor(memberWith(GroupRoleManager)))
	if err != nil || len(staff) != 2 || staff[1].UserID != 8 || staff[1].Qualification == nil || staff[1].Qualification.MaxAge != 12 || staff[1].Email == nil || staff[0].Qualification != nil {
		t.Fatalf("staff Coaches() = %+v, %v", staff, err)
	}
	self, err := service.Coaches(ctx, accessFor(memberWith(GroupRoleCoach)))
	if err != nil || len(self) != 1 || self[0].UserID != 7 || self[0].Email == nil {
		t.Fatalf("coach Coaches() = %+v, %v; want only themselves", self, err)
	}
	if _, err := service.Coaches(ctx, accessFor(memberWith(GroupRolePlayer))); !errors.Is(err, ErrGroupForbidden) {
		t.Fatalf("player Coaches() error = %v", err)
	}
	if _, err := service.Coaches(ctx, accessFor(GroupAccessGrants{})); !errors.Is(err, ErrGroupNotFound) {
		t.Fatalf("outsider Coaches() error = %v", err)
	}
	if list, err := service.Qualifications(ctx, accessFor(GroupAccessGrants{SiteAdmin: true})); err != nil || len(list) != 1 {
		t.Fatalf("Qualifications() = %+v, %v", list, err)
	}
	if _, err := service.Qualifications(ctx, accessFor(memberWith(GroupRoleCoach))); !errors.Is(err, ErrGroupForbidden) {
		t.Fatalf("coach Qualifications() error = %v", err)
	}

	for _, test := range []struct {
		name     string
		actor    GroupActor
		coach    int64
		min, max int
		want     error
	}{
		{"admin", adminActor(), 7, 9, 12, nil},
		{"manager", actorWith(memberWith(GroupRoleManager)), 8, 0, 99, nil},
		{"site administrator", siteAdminActor(), 8, 5, 5, nil},
		{"coach themself", selfCoachActor(), 7, 9, 12, ErrGroupForbidden},
		{"player", playerOn(4), 8, 9, 12, ErrGroupForbidden},
		{"outsider", actorWith(GroupAccessGrants{}), 8, 9, 12, ErrGroupNotFound},
		{"not a coach", adminActor(), 9, 9, 12, ErrCoachNotFound},
		{"minimum above maximum", adminActor(), 8, 12, 9, ErrInvalidQualification},
		{"negative", adminActor(), 8, -1, 9, ErrInvalidQualification},
		{"too old", adminActor(), 8, 9, 100, ErrInvalidQualification},
	} {
		auditor.records = nil
		_, err := service.SetQualification(ctx, test.actor, test.coach, test.min, test.max)
		if !errors.Is(err, test.want) {
			t.Errorf("%s: SetQualification() error = %v, want %v", test.name, err, test.want)
			continue
		}
		if test.want != nil {
			if len(auditor.records) != 0 {
				t.Errorf("%s: refused change was audited", test.name)
			}
			continue
		}
		stored := repository.qualifications[test.coach]
		if stored.MinAge != test.min || stored.MaxAge != test.max || len(auditor.records) != 1 || auditor.records[0].Action != AuditActionCoachQualificationSet || auditor.records[0].TargetID != "5" {
			t.Errorf("%s: stored %+v, audit %+v", test.name, stored, auditor.records)
		}
		if strings.Contains(auditor.records[0].Detail, "(site administrator)") != (test.name == "site administrator") {
			t.Errorf("%s: audit detail %q", test.name, auditor.records[0].Detail)
		}
	}
	repository.setErr = errors.New("boom")
	if _, err := service.SetQualification(ctx, adminActor(), 8, 9, 12); err == nil || errors.Is(err, ErrCoachNotFound) {
		t.Fatalf("storage error = %v", err)
	}

	auditor.records = nil
	if err := service.ClearQualification(ctx, selfCoachActor(), 7); !errors.Is(err, ErrGroupForbidden) {
		t.Fatalf("coach ClearQualification() error = %v", err)
	}
	if err := service.ClearQualification(ctx, adminActor(), 8); err != nil {
		t.Fatalf("ClearQualification() error = %v", err)
	}
	if _, ok := repository.qualifications[8]; ok || len(auditor.records) != 1 || auditor.records[0].Action != AuditActionCoachQualificationCleared || auditor.records[0].Detail != "coach 8" {
		t.Fatalf("clear left %+v, audit %+v", repository.qualifications, auditor.records)
	}
}

func TestCoachServiceAvailabilityAccess(t *testing.T) {
	ctx := context.Background()
	for _, test := range []struct {
		name    string
		actor   GroupActor
		coach   int64
		want    error
		audited bool
	}{
		{name: "the coach themself", actor: selfCoachActor(), coach: 7},
		{name: "another coach", actor: selfCoachActor(), coach: 8, want: ErrGroupForbidden},
		{name: "admin", actor: adminActor(), coach: 8, audited: true},
		{name: "manager", actor: actorWith(memberWith(GroupRoleManager)), coach: 8, audited: true},
		{name: "site administrator", actor: siteAdminActor(), coach: 8, audited: true},
		{name: "admin who is the coach", actor: actorWith(memberWith(GroupRoleAdmin, GroupRoleCoach)), coach: 7},
		{name: "player", actor: playerOn(4), coach: 8, want: ErrGroupForbidden},
		{name: "parent", actor: parentActor(), coach: 7, want: ErrGroupForbidden},
		{name: "non-member", actor: actorWith(GroupAccessGrants{}), coach: 8, want: ErrGroupNotFound},
		{name: "member who is no coach", actor: adminActor(), coach: 9, want: ErrCoachNotFound},
	} {
		t.Run(test.name, func(t *testing.T) {
			service, repository, auditor := newTestCoachService(t)
			input := AvailabilityInput{Week: SeasonWeekAll, Weekday: 2, StartMinute: 16 * 60, EndMinute: 18 * 60}
			err := service.AddAvailability(ctx, test.actor, 11, test.coach, input)
			if !errors.Is(err, test.want) {
				t.Fatalf("AddAvailability() error = %v, want %v", err, test.want)
			}
			if _, viewErr := service.CoachAvailability(ctx, test.actor.Access, 11, test.coach); !errors.Is(viewErr, test.want) {
				t.Fatalf("CoachAvailability() error = %v, want %v", viewErr, test.want)
			}
			if test.want != nil {
				if repository.edits != 0 || len(auditor.records) != 0 {
					t.Fatal("refused change was stored or audited")
				}
				return
			}
			view, err := service.CoachAvailability(ctx, test.actor.Access, 11, test.coach)
			if err != nil || len(view.Windows) != 1 || view.Part.ID != 11 || view.Season.ID != 1 || view.Windows[0].StartMinute != 960 {
				t.Fatalf("CoachAvailability() = %+v, %v", view, err)
			}
			if (len(auditor.records) == 1) != test.audited {
				t.Fatalf("audit records = %+v, audited want %v", auditor.records, test.audited)
			}
			if test.audited {
				record := auditor.records[0]
				if record.Action != AuditActionCoachAvailabilityChanged || record.Detail[:len("coach 8; part 11; added week all day 2 16:00-18:00")] != "coach 8; part 11; added week all day 2 16:00-18:00" {
					t.Fatalf("audit = %+v", record)
				}
			}
		})
	}

	service, _, _ := newTestCoachService(t)
	if _, err := service.Availability(ctx, accessFor(memberWith(GroupRoleCoach)), 11); !errors.Is(err, ErrGroupForbidden) {
		t.Fatalf("coach side-by-side error = %v", err)
	}
	if _, err := service.Availability(ctx, accessFor(GroupAccessGrants{}), 11); !errors.Is(err, ErrGroupNotFound) {
		t.Fatalf("outsider side-by-side error = %v", err)
	}
	if _, err := service.Availability(ctx, accessFor(memberWith(GroupRoleAdmin)), 999); !errors.Is(err, ErrSeasonNotFound) {
		t.Fatalf("unknown part error = %v", err)
	}
	_ = service.AddAvailability(ctx, selfCoachActor(), 11, 7, AvailabilityInput{Week: SeasonWeekAll, Weekday: 1, StartMinute: 600, EndMinute: 700})
	_ = service.AddAvailability(ctx, adminActor(), 11, 8, AvailabilityInput{Week: SeasonWeekAll, Weekday: 1, StartMinute: 600, EndMinute: 700})
	if all, err := service.Availability(ctx, accessFor(memberWith(GroupRoleManager)), 11); err != nil || len(all.Windows) != 2 {
		t.Fatalf("side-by-side = %+v, %v", all, err)
	}
	if err := service.AddAvailability(ctx, selfCoachActor(), 999, 7, AvailabilityInput{Week: SeasonWeekAll, Weekday: 1, StartMinute: 600, EndMinute: 700}); !errors.Is(err, ErrSeasonNotFound) {
		t.Fatalf("unknown part add error = %v", err)
	}
}

func TestCoachServiceAvailabilityEdits(t *testing.T) {
	ctx := context.Background()
	service, repository, auditor := newTestCoachService(t)
	add := func(part int64, week string, weekday, start, end int) error {
		return service.AddAvailability(ctx, selfCoachActor(), part, 7, AvailabilityInput{Week: week, Weekday: weekday, StartMinute: start, EndMinute: end})
	}
	for name, test := range map[string]struct {
		part                int64
		week                string
		weekday, start, end int
		want                error
	}{
		"week A without alternation": {11, SeasonWeekA, 1, 600, 700, ErrAvailabilityWeek},
		"unknown week":               {12, "C", 1, 600, 700, ErrAvailabilityWeek},
		"weekday 0":                  {11, SeasonWeekAll, 0, 600, 700, ErrInvalidAvailability},
		"weekday 8":                  {11, SeasonWeekAll, 8, 600, 700, ErrInvalidAvailability},
		"end before start":           {11, SeasonWeekAll, 1, 700, 600, ErrInvalidAvailability},
		"past midnight":              {11, SeasonWeekAll, 1, 1400, 1445, ErrInvalidAvailability},
		"off the 5-minute step":      {11, SeasonWeekAll, 1, 601, 700, ErrInvalidAvailability},
	} {
		if err := add(test.part, test.week, test.weekday, test.start, test.end); !errors.Is(err, test.want) {
			t.Errorf("%s: error = %v, want %v", name, err, test.want)
		}
	}
	if repository.edits != 0 {
		t.Fatal("invalid windows were stored")
	}

	// Overlapping and touching windows merge.
	for _, window := range [][2]int{{16 * 60, 17 * 60}, {17 * 60, 18 * 60}, {16*60 + 30, 17*60 + 30}, {20 * 60, 21 * 60}} {
		if err := add(11, SeasonWeekAll, 2, window[0], window[1]); err != nil {
			t.Fatalf("add %v error = %v", window, err)
		}
	}
	view, _ := service.CoachAvailability(ctx, selfCoachActor().Access, 11, 7)
	if len(view.Windows) != 2 || view.Windows[0].StartMinute != 960 || view.Windows[0].EndMinute != 1080 || view.Windows[1].StartMinute != 1200 {
		t.Fatalf("merged windows = %+v", view.Windows)
	}
	if len(auditor.records) != 0 {
		t.Fatal("self-service edits were audited")
	}

	// Moving the evening window onto the afternoon one merges them too.
	evening := view.Windows[1].ID
	if err := service.MoveAvailability(ctx, selfCoachActor(), 11, 7, evening, AvailabilityInput{Week: SeasonWeekAll, Weekday: 2, StartMinute: 18 * 60, EndMinute: 19 * 60}); err != nil {
		t.Fatalf("MoveAvailability() error = %v", err)
	}
	view, _ = service.CoachAvailability(ctx, selfCoachActor().Access, 11, 7)
	if len(view.Windows) != 1 || view.Windows[0].EndMinute != 19*60 {
		t.Fatalf("after move = %+v", view.Windows)
	}
	if err := service.MoveAvailability(ctx, selfCoachActor(), 11, 7, evening, AvailabilityInput{Week: SeasonWeekAll, Weekday: 3, StartMinute: 600, EndMinute: 700}); !errors.Is(err, ErrAvailabilityNotFound) {
		t.Fatalf("stale window move error = %v", err)
	}
	if err := service.MoveAvailability(ctx, adminActor(), 11, 8, view.Windows[0].ID, AvailabilityInput{Week: SeasonWeekAll, Weekday: 3, StartMinute: 600, EndMinute: 700}); !errors.Is(err, ErrAvailabilityNotFound) {
		t.Fatalf("another coach's window move error = %v", err)
	}
	if err := service.DeleteAvailability(ctx, selfCoachActor(), 11, 7, 0); !errors.Is(err, ErrAvailabilityNotFound) {
		t.Fatalf("delete of nothing error = %v", err)
	}
	if err := service.DeleteAvailability(ctx, actorWith(memberWith(GroupRoleCoach, GroupRolePlayer)), 11, 8, view.Windows[0].ID); !errors.Is(err, ErrGroupForbidden) {
		t.Fatalf("other coach delete error = %v", err)
	}
	if err := service.DeleteAvailability(ctx, selfCoachActor(), 11, 7, view.Windows[0].ID); err != nil {
		t.Fatalf("DeleteAvailability() error = %v", err)
	}
	if view, _ = service.CoachAvailability(ctx, selfCoachActor().Access, 11, 7); len(view.Windows) != 0 {
		t.Fatalf("after delete = %+v", view.Windows)
	}

	// A and B windows in the alternating part; staff edits are audited.
	if err := service.AddAvailability(ctx, adminActor(), 12, 8, AvailabilityInput{Week: SeasonWeekB, Weekday: 5, StartMinute: 600, EndMinute: 700}); err != nil {
		t.Fatalf("week B add error = %v", err)
	}
	moved := repository.windows[len(repository.windows)-1].ID
	if err := service.MoveAvailability(ctx, adminActor(), 12, 8, moved, AvailabilityInput{Week: SeasonWeekB, Weekday: 5, StartMinute: 700, EndMinute: 800}); err != nil {
		t.Fatalf("staff move error = %v", err)
	}
	moved = repository.windows[len(repository.windows)-1].ID
	if err := service.DeleteAvailability(ctx, adminActor(), 12, 8, moved); err != nil {
		t.Fatalf("staff delete error = %v", err)
	}
	if len(auditor.records) != 3 || !strings.Contains(auditor.records[1].Detail, "moved window") || !strings.Contains(auditor.records[2].Detail, "removed week B day 5 11:40-13:20") {
		t.Fatalf("staff audit = %+v", auditor.records)
	}

	// At most MaxAvailabilityWindows windows.
	repository.windows = nil
	for index := range MaxAvailabilityWindows {
		repository.windows = append(repository.windows, AvailabilityWindow{ID: int64(index + 1), GroupID: 5, SeasonPartID: 21, CoachUserID: 7, Week: SeasonWeekAll, Weekday: 1 + index%7, StartMinute: (index / 7) * 60, EndMinute: (index/7)*60 + 30})
	}
	if err := add(21, SeasonWeekAll, 7, 23*60, 23*60+30); !errors.Is(err, ErrTooManyAvailabilityWindows) {
		t.Fatalf("101st window error = %v", err)
	}
}

func TestCoachServiceCopyPreviousAvailability(t *testing.T) {
	ctx := context.Background()
	service, repository, auditor := newTestCoachService(t)
	window := func(part int64, coach int64, week string, weekday, start, end int) AvailabilityWindow {
		return AvailabilityWindow{ID: int64(len(repository.windows) + 1), GroupID: 5, SeasonPartID: part, CoachUserID: coach, Week: week, Weekday: weekday, StartMinute: start, EndMinute: end}
	}

	// The group's first part has nothing before it.
	if _, err := service.CopyPreviousAvailability(ctx, selfCoachActor(), 11, 7); !errors.Is(err, ErrNoPreviousSeasonPart) {
		t.Fatalf("first part copy error = %v", err)
	}
	if _, _, found, err := service.PreviousPart(ctx, selfCoachActor().Access, 11); err != nil || found {
		t.Fatalf("PreviousPart(11) = %v, %v", found, err)
	}
	if _, _, _, err := service.PreviousPart(ctx, selfCoachActor().Access, 999); !errors.Is(err, ErrSeasonNotFound) {
		t.Fatalf("PreviousPart(unknown) error = %v", err)
	}
	// Nothing to copy keeps the target untouched.
	repository.windows = append(repository.windows, window(12, 7, SeasonWeekA, 1, 600, 700))
	if _, err := service.CopyPreviousAvailability(ctx, selfCoachActor(), 12, 7); !errors.Is(err, ErrNothingToCopy) {
		t.Fatalf("empty source copy error = %v", err)
	}
	if len(repository.windows) != 1 {
		t.Fatal("empty copy replaced the target's windows")
	}

	// Part 1 (every week) into part 2 (A/B): replaces part 2's windows.
	repository.windows = append(repository.windows,
		window(11, 7, SeasonWeekAll, 2, 960, 1080),
		window(11, 7, SeasonWeekAll, 4, 1020, 1140),
		window(11, 7, SeasonWeekA, 5, 600, 700), // stale: part 1 does not alternate
		window(11, 8, SeasonWeekAll, 1, 600, 700),
	)
	copied, err := service.CopyPreviousAvailability(ctx, selfCoachActor(), 12, 7)
	if err != nil || copied != 2 {
		t.Fatalf("CopyPreviousAvailability() = %d, %v", copied, err)
	}
	target, _ := service.CoachAvailability(ctx, selfCoachActor().Access, 12, 7)
	if len(target.Windows) != 2 || target.Windows[0].Weekday != 2 || target.Windows[1].Weekday != 4 || target.Windows[0].Week != SeasonWeekAll {
		t.Fatalf("copied windows = %+v", target.Windows)
	}
	if len(auditor.records) != 0 {
		t.Fatal("self-service copy was audited")
	}

	// Part 2 (A/B) into the next season's part (every week): only the
	// every-week windows; staff copies are audited.
	repository.windows = append(repository.windows, window(12, 8, SeasonWeekB, 3, 600, 700), window(12, 8, SeasonWeekAll, 6, 600, 700))
	copied, err = service.CopyPreviousAvailability(ctx, adminActor(), 21, 8)
	if err != nil || copied != 1 {
		t.Fatalf("staff copy = %d, %v", copied, err)
	}
	if len(auditor.records) != 1 || auditor.records[0].Action != AuditActionCoachAvailabilityCopied || auditor.records[0].Detail != "coach 8; part 21; from part 12; 1 windows" {
		t.Fatalf("copy audit = %+v", auditor.records)
	}
	if _, err := service.CopyPreviousAvailability(ctx, selfCoachActor(), 21, 8); !errors.Is(err, ErrGroupForbidden) {
		t.Fatalf("other coach copy error = %v", err)
	}
}

func TestCoachServiceParts(t *testing.T) {
	service, _, _ := newTestCoachService(t)
	parts, err := service.Parts(context.Background(), accessFor(memberWith(GroupRoleCoach)))
	if err != nil || len(parts.Seasons) != 2 || !parts.HasDefault || parts.Default.ID != 11 || parts.DefaultSeason.ID != 1 {
		t.Fatalf("Parts() = %+v, %v", parts, err)
	}
	if _, err := service.Parts(context.Background(), accessFor(memberWith(GroupRoleParent))); !errors.Is(err, ErrGroupForbidden) {
		t.Fatalf("parent Parts() error = %v", err)
	}
	if _, err := service.Parts(context.Background(), accessFor(GroupAccessGrants{})); !errors.Is(err, ErrGroupNotFound) {
		t.Fatalf("outsider Parts() error = %v", err)
	}
}

func TestCoachAccessQuestions(t *testing.T) {
	for _, test := range []struct {
		name                              string
		grants                            GroupAccessGrants
		coaches, manage, all, self, other bool
	}{
		{"admin", memberWith(GroupRoleAdmin), true, true, true, true, true},
		{"manager", memberWith(GroupRoleManager), true, true, true, true, true},
		{"site administrator", GroupAccessGrants{SiteAdmin: true}, true, true, true, true, true},
		{"coach", memberWith(GroupRoleCoach), true, false, false, true, false},
		{"player", memberWith(GroupRolePlayer), false, false, false, false, false},
		{"parent", memberWith(GroupRoleParent), false, false, false, false, false},
		{"outsider", GroupAccessGrants{}, false, false, false, false, false},
	} {
		access := accessFor(test.grants)
		got := []bool{access.CanViewCoaches(), access.CanManageQualifications(), access.CanViewAllAvailability(),
			access.CanEditAvailability(7) && access.CanViewAvailability(7) && access.CanViewQualification(7),
			access.CanEditAvailability(8) || access.CanViewAvailability(8) || access.CanViewQualification(8)}
		want := []bool{test.coaches, test.manage, test.all, test.self, test.other}
		if !slices.Equal(got, want) {
			t.Errorf("%s: got %v, want %v", test.name, got, want)
		}
	}
	if accessFor(memberWith(GroupRoleCoach)).CanEditAvailability(0) {
		t.Fatal("coach may edit user 0")
	}
}
