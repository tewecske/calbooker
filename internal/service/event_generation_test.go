package service

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
)

// Fixture: group 5 in Europe/Budapest, Spring (part 12, alternating, starts
// Monday 2027-01-04, so that week is A) and Autumn (part 11, every week,
// 2026-08-01 to 2026-12-20). Teams: 1 Lions, 2 Tigers, 3 archived Owls.
// Resources: 1 Field, 3 archived Hall. Weekday 2 = Tuesday; 960 = 16:00.

type generationRepoStub struct {
	zone         string
	trainings    []PlanTraining
	events       []Event
	participants map[int64]bool
	nextID       int64
	writes       int
}

func (r *generationRepoStub) Generate(_ context.Context, _, partID int64, weeks []time.Time, apply bool, plan func(GenerationState) (GenerationPlan, error)) error {
	if partID != springPart && partID != autumnPart {
		return ErrSeasonNotFound
	}
	state := GenerationState{TimeZone: r.zone}
	for _, training := range r.trainings {
		if training.SeasonPartID == partID {
			state.Trainings = append(state.Trainings, training)
		}
	}
	for _, event := range r.events {
		if event.GeneratedForWeek != nil && slices.ContainsFunc(weeks, func(week time.Time) bool { return week.Equal(*event.GeneratedForWeek) }) {
			state.Existing = append(state.Existing, GeneratedEvent{Event: event, HasParticipants: r.participants[event.ID]})
		}
	}
	result, err := plan(state)
	if err != nil || !apply {
		return err
	}
	for _, id := range result.Delete {
		if r.participants[id] {
			return ErrEventHasParticipants
		}
		r.events = slices.DeleteFunc(r.events, func(event Event) bool { return event.ID == id })
	}
	for _, event := range result.Create {
		for _, other := range r.events {
			if other.PlanTrainingID != nil && event.PlanTrainingID != nil && *other.PlanTrainingID == *event.PlanTrainingID && other.GeneratedForWeek.Equal(*event.GeneratedForWeek) {
				return errors.New("duplicate generated event")
			}
		}
		r.nextID++
		event.ID = r.nextID
		r.events = append(r.events, event)
	}
	r.writes++
	return nil
}

type generationFixture struct {
	service *EventGenerationService
	repo    *generationRepoStub
	auditor *auditRecorderStub
}

func newGenerationFixture(t *testing.T, trainings ...PlanTraining) generationFixture {
	t.Helper()
	repo := &generationRepoStub{zone: "Europe/Budapest", trainings: trainings, participants: map[int64]bool{}}
	teams := newTeamStub(boardLions, boardTigers, boardOwls)
	service, err := NewEventGenerationService(repo, trainingSeasonStub{seasons: []Season{season2627(), boardSeason2526()}}, trainingResourceStub{trainingResources()},
		teams, usersWith(User{ID: 7, Email: strPtr("staff@example.test")}))
	if err != nil {
		t.Fatalf("NewEventGenerationService() error = %v", err)
	}
	auditor := &auditRecorderStub{}
	service.SetAuditor(auditor)
	service.now = func() time.Time { return time.Date(2027, 2, 1, 12, 0, 0, 0, time.UTC) }
	return generationFixture{service: service, repo: repo, auditor: auditor}
}

func autumnTraining(id, team, resource int64, week string, weekday, start, end int) PlanTraining {
	t := training(id, team, resource, week, weekday, start, end)
	t.SeasonPartID = autumnPart
	return t
}

func mustInstant(t *testing.T, wall string) time.Time {
	t.Helper()
	instant, err := EventInstant(ParseEventLocalTime(wall), "Europe/Budapest")
	if err != nil {
		t.Fatalf("EventInstant(%q) error = %v", wall, err)
	}
	return instant
}

func generate(t *testing.T, f generationFixture, part int64, week string, weeks int) GenerationPreview {
	t.Helper()
	result, err := f.service.Generate(context.Background(), adminActor(), GenerationRequest{PartID: part, Week: day(week), Weeks: weeks})
	if err != nil {
		t.Fatalf("Generate(%d, %s, %d) error = %v", part, week, weeks, err)
	}
	return result
}

func TestGenerateCreatesEventsFromThePlan(t *testing.T) {
	f := newGenerationFixture(t, training(101, 1, 1, SeasonWeekAll, 2, 960, 1050), training(102, 2, 1, SeasonWeekAll, 4, 1020, 1110))
	result := generate(t, f, springPart, "2027-01-04", 2)
	if result.Counts.Create != 4 || len(f.repo.events) != 4 {
		t.Fatalf("counts = %+v, events = %d, want 4 created", result.Counts, len(f.repo.events))
	}
	first := f.repo.events[0]
	if first.KindCode != EventKindTraining || first.SignupMode != EventSignupOpen || first.Title != "Lions" || first.ResourceID == nil || *first.ResourceID != 1 {
		t.Fatalf("event = %+v, want an open training of Lions on the field", first)
	}
	if !first.StartsAt.Equal(mustInstant(t, "2027-01-05T16:00")) || !first.EndsAt.Equal(mustInstant(t, "2027-01-05T17:30")) {
		t.Fatalf("times = %v to %v, want Tuesday 16:00-17:30 local", first.StartsAt, first.EndsAt)
	}
	if first.PlanTrainingID == nil || *first.PlanTrainingID != 101 || first.GeneratedForWeek == nil || !first.GeneratedForWeek.Equal(day("2027-01-04")) {
		t.Fatalf("links = %v %v, want training 101 for week 2027-01-04", first.PlanTrainingID, first.GeneratedForWeek)
	}
	if len(first.Teams) != 1 || first.Teams[0].ID != 1 || first.EditedAt != nil {
		t.Fatalf("teams = %+v edited = %v, want team 1 and not edited", first.Teams, first.EditedAt)
	}
	if len(f.auditor.records) != 1 || f.auditor.records[0].Action != AuditActionEventsGenerated || !strings.Contains(f.auditor.records[0].Detail, "created 4") {
		t.Fatalf("audit = %+v, want one group.plan.events_generated with counts", f.auditor.records)
	}
	if strings.Contains(f.auditor.records[0].Detail, "Lions") {
		t.Fatalf("audit detail %q names a team", f.auditor.records[0].Detail)
	}
}

func TestGenerateTwiceCreatesNothingNew(t *testing.T) {
	f := newGenerationFixture(t, training(101, 1, 1, SeasonWeekAll, 2, 960, 1050))
	generate(t, f, springPart, "2027-01-04", 2)
	writes := f.repo.writes
	second := generate(t, f, springPart, "2027-01-04", 2)
	if second.Counts.Create+second.Counts.Rebuild+second.Counts.Remove != 0 || second.Counts.Unchanged != 2 || len(f.repo.events) != 2 {
		t.Fatalf("counts = %+v, events = %d, want 2 unchanged and nothing written", second.Counts, len(f.repo.events))
	}
	if len(f.auditor.records) != 1 {
		t.Fatalf("audit entries = %d, want only the first generation audited", len(f.auditor.records))
	}
	_ = writes
	preview, err := f.service.Preview(context.Background(), adminActor().Access, GenerationRequest{PartID: springPart, Week: day("2027-01-04"), Weeks: 2})
	if err != nil || preview.Counts.Unchanged != 2 || preview.Counts.Changed() {
		t.Fatalf("Preview() = %+v, %v, want 2 unchanged", preview.Counts, err)
	}
}

func TestGeneratePreviewWritesNothing(t *testing.T) {
	f := newGenerationFixture(t, training(101, 1, 1, SeasonWeekAll, 2, 960, 1050))
	preview, err := f.service.Preview(context.Background(), adminActor().Access, GenerationRequest{PartID: springPart, Week: day("2027-01-04")})
	if err != nil || preview.Counts.Create != 1 || len(f.repo.events) != 0 || f.repo.writes != 0 || len(f.auditor.records) != 0 {
		t.Fatalf("Preview() = %+v, %v; events %d writes %d audit %d, want one planned create and nothing written", preview.Counts, err, len(f.repo.events), f.repo.writes, len(f.auditor.records))
	}
}

func TestGenerateSelectsWeekAandB(t *testing.T) {
	f := newGenerationFixture(t,
		training(101, 1, 1, SeasonWeekA, 2, 960, 1050),
		training(102, 2, 1, SeasonWeekB, 2, 960, 1050),
		training(103, 1, 1, SeasonWeekAll, 3, 960, 1050))
	titles := func() []string {
		var list []string
		for _, event := range f.repo.events {
			list = append(list, event.Title+"@"+event.StartsAt.In(budapest).Format("01-02"))
		}
		slices.Sort(list)
		return list
	}
	generate(t, f, springPart, "2027-01-04", 2)
	want := []string{"Lions@01-05", "Lions@01-06", "Lions@01-13", "Tigers@01-12"}
	if got := titles(); !slices.Equal(got, want) {
		t.Fatalf("events = %v, want %v (week A Lions Tuesday, week B Tigers Tuesday, every week Lions Wednesday)", got, want)
	}
}

func TestGenerateKeepsClockTimeAcrossDaylightSavingChanges(t *testing.T) {
	cases := []struct {
		name          string
		part          int64
		week          string
		training      PlanTraining
		start, end    string
		wantDuration  time.Duration
		wantSkipCount int
	}{
		{name: "spring forward, Sunday morning", part: springPart, week: "2027-03-22", training: training(201, 1, 1, SeasonWeekAll, 7, 480, 570),
			start: "2027-03-28T08:00", end: "2027-03-28T09:30", wantDuration: 90 * time.Minute},
		{name: "spring forward, across the change", part: springPart, week: "2027-03-22", training: training(202, 1, 1, SeasonWeekAll, 7, 0, 240),
			start: "2027-03-28T00:00", end: "2027-03-28T04:00", wantDuration: 3 * time.Hour},
		{name: "fall back, across the change", part: autumnPart, week: "2026-10-19", training: autumnTraining(203, 1, 1, SeasonWeekAll, 7, 0, 240),
			start: "2026-10-25T00:00", end: "2026-10-25T04:00", wantDuration: 5 * time.Hour},
		{name: "fall back, day after", part: autumnPart, week: "2026-10-19", training: autumnTraining(204, 1, 1, SeasonWeekAll, 1, 960, 1050),
			start: "2026-10-19T16:00", end: "2026-10-19T17:30", wantDuration: 90 * time.Minute},
		{name: "skipped hour", part: springPart, week: "2027-03-22", training: training(205, 1, 1, SeasonWeekAll, 7, 150, 210), wantSkipCount: 1},
		{name: "ends at midnight", part: springPart, week: "2027-03-22", training: training(206, 1, 1, SeasonWeekAll, 6, 1380, 1440),
			start: "2027-03-27T23:00", end: "2027-03-28T00:00", wantDuration: time.Hour},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newGenerationFixture(t, tc.training)
			result := generate(t, f, tc.part, tc.week, 1)
			if tc.wantSkipCount > 0 {
				if result.Counts.Skip != 1 || len(f.repo.events) != 0 || result.Items[0].Reason != GenerationReasonTimeSkipped {
					t.Fatalf("result = %+v, events = %d, want the skipped hour reported and nothing created", result.Counts, len(f.repo.events))
				}
				return
			}
			if len(f.repo.events) != 1 {
				t.Fatalf("events = %d, want 1", len(f.repo.events))
			}
			event := f.repo.events[0]
			if !event.StartsAt.Equal(mustInstant(t, tc.start)) || !event.EndsAt.Equal(mustInstant(t, tc.end)) || event.EndsAt.Sub(event.StartsAt) != tc.wantDuration {
				t.Fatalf("event = %v to %v (%v), want %s to %s lasting %v", event.StartsAt, event.EndsAt, event.EndsAt.Sub(event.StartsAt), tc.start, tc.end, tc.wantDuration)
			}
			again := generate(t, f, tc.part, tc.week, 1)
			if again.Counts.Unchanged != 1 || again.Counts.Changed() {
				t.Fatalf("second run = %+v, want unchanged", again.Counts)
			}
		})
	}
}

func TestGenerateRespectsPartDates(t *testing.T) {
	// Autumn ends on Sunday 2026-12-20; a Tuesday and a Monday training of
	// the week 2026-12-14 are in, and week 2026-12-21 is outside.
	f := newGenerationFixture(t, autumnTraining(301, 1, 1, SeasonWeekAll, 2, 960, 1050))
	if _, err := f.service.Generate(context.Background(), adminActor(), GenerationRequest{PartID: autumnPart, Week: day("2026-12-21")}); !errors.Is(err, ErrInvalidGenerationWeek) {
		t.Fatalf("week after the part error = %v, want ErrInvalidGenerationWeek", err)
	}
	if _, err := f.service.Generate(context.Background(), adminActor(), GenerationRequest{PartID: autumnPart, Week: day("2026-12-14"), Weeks: 2}); !errors.Is(err, ErrInvalidGenerationWeek) {
		t.Fatalf("second week outside the part error = %v, want ErrInvalidGenerationWeek", err)
	}
	if len(f.repo.events) != 0 {
		t.Fatalf("events = %d, want none after refused requests", len(f.repo.events))
	}
	// The part's first week starts mid-week: Spring begins on Monday, so use
	// a part starting on Wednesday instead.
	part := SeasonPart{ID: 99, StartsOn: day("2027-01-06"), EndsOn: day("2027-01-31")}
	in := GenerationInput{
		GroupID: 5, Part: part, Weeks: []time.Time{day("2027-01-04")}, Teams: map[int64]Team{1: boardLions}, Resources: map[int64]Resource{1: {ID: 1, Name: "Field"}},
		State: GenerationState{TimeZone: "Europe/Budapest", Trainings: []PlanTraining{
			training(1, 1, 1, SeasonWeekAll, 2, 960, 1050), training(2, 1, 1, SeasonWeekAll, 3, 960, 1050),
		}},
	}
	outcome, err := PlanGeneration(in)
	if err != nil || outcome.Counts.Create != 1 || outcome.Plan.Create[0].PlanTrainingID == nil || *outcome.Plan.Create[0].PlanTrainingID != 2 {
		t.Fatalf("PlanGeneration() = %+v, %v, want only the Wednesday training inside the part", outcome.Counts, err)
	}
}

func TestGenerateValidatesWeeks(t *testing.T) {
	f := newGenerationFixture(t, training(101, 1, 1, SeasonWeekAll, 2, 960, 1050))
	for name, request := range map[string]GenerationRequest{
		"not a monday":  {PartID: springPart, Week: day("2027-01-05")},
		"no date":       {PartID: springPart},
		"three weeks":   {PartID: springPart, Week: day("2027-01-04"), Weeks: 3},
		"negative":      {PartID: springPart, Week: day("2027-01-04"), Weeks: -1},
		"before part":   {PartID: springPart, Week: day("2026-12-28")},
		"week of start": {PartID: springPart, Week: day("2026-12-28"), Weeks: 2},
	} {
		if _, err := f.service.Generate(context.Background(), adminActor(), request); !errors.Is(err, ErrInvalidGenerationWeek) {
			t.Fatalf("%s error = %v, want ErrInvalidGenerationWeek", name, err)
		}
	}
	if _, err := f.service.Generate(context.Background(), adminActor(), GenerationRequest{PartID: 999, Week: day("2027-01-04")}); !errors.Is(err, ErrSeasonNotFound) {
		t.Fatalf("unknown part error = %v, want ErrSeasonNotFound", err)
	}
}

func generated(f generationFixture, trainingID int64, week string) *Event {
	for index := range f.repo.events {
		event := &f.repo.events[index]
		if event.PlanTrainingID != nil && *event.PlanTrainingID == trainingID && event.GeneratedForWeek.Equal(day(week)) {
			return event
		}
	}
	return nil
}

func TestRegenerateRebuildsOnlyUntouchedEvents(t *testing.T) {
	f := newGenerationFixture(t,
		training(101, 1, 1, SeasonWeekAll, 2, 960, 1050),
		training(102, 2, 1, SeasonWeekAll, 3, 960, 1050),
		training(103, 1, 1, SeasonWeekAll, 4, 960, 1050),
		training(104, 2, 1, SeasonWeekAll, 5, 960, 1050))
	generate(t, f, springPart, "2027-01-04", 1)
	edited := generated(f, 101, "2027-01-04")
	edited.EditedAt = int64Ptr(5)
	withAnswers := generated(f, 102, "2027-01-04")
	f.repo.participants[withAnswers.ID] = true
	untouchedID := generated(f, 103, "2027-01-04").ID
	// Move all trainings an hour later, and drop training 104.
	for index := range f.repo.trainings {
		f.repo.trainings[index].StartMinute += 60
		f.repo.trainings[index].EndMinute += 60
	}
	f.repo.trainings = f.repo.trainings[:3]
	// Deleting a training clears the link of its events (foreign key).
	generated(f, 104, "2027-01-04").PlanTrainingID = nil

	preview, err := f.service.Preview(context.Background(), adminActor().Access, GenerationRequest{PartID: springPart, Week: day("2027-01-04")})
	if err != nil {
		t.Fatalf("Preview() error = %v", err)
	}
	if c := preview.Counts; c.Rebuild != 1 || c.Remove != 1 || c.Keep != 2 || c.Create != 0 {
		t.Fatalf("preview counts = %+v, want 1 rebuild, 1 remove, 2 kept", c)
	}
	result := generate(t, f, springPart, "2027-01-04", 1)
	if result.Counts != preview.Counts {
		t.Fatalf("result counts = %+v, preview = %+v, want the same", result.Counts, preview.Counts)
	}
	reasons := map[string]string{}
	for _, item := range result.Items {
		if item.Action == GenerationKeep {
			reasons[item.TeamName+item.Reason] = item.Reason
		}
	}
	if len(reasons) != 2 || reasons["Lions"+GenerationReasonEdited] == "" || reasons["Tigers"+GenerationReasonParticipants] == "" {
		t.Fatalf("kept = %v, want the edited Lions event and the Tigers event with answers", reasons)
	}
	if got := generated(f, 101, "2027-01-04"); got == nil || got.ID != edited.ID || !got.StartsAt.Equal(mustInstant(t, "2027-01-05T16:00")) {
		t.Fatalf("edited event = %+v, want it untouched at 16:00", got)
	}
	if got := generated(f, 102, "2027-01-04"); got == nil || got.ID != withAnswers.ID || !got.StartsAt.Equal(mustInstant(t, "2027-01-06T16:00")) {
		t.Fatalf("event with answers = %+v, want it untouched at 16:00", got)
	}
	rebuilt := generated(f, 103, "2027-01-04")
	if rebuilt == nil || rebuilt.ID == untouchedID || !rebuilt.StartsAt.Equal(mustInstant(t, "2027-01-07T17:00")) {
		t.Fatalf("rebuilt event = %+v, want a new event at 17:00", rebuilt)
	}
	if generated(f, 104, "2027-01-04") != nil {
		t.Fatal("event of the removed training still exists, want it removed")
	}
	if len(f.repo.events) != 3 {
		t.Fatalf("events = %d, want 3", len(f.repo.events))
	}
}

func TestRegenerateAfterTrainingDeletedRemovesUntouchedUnlinkedEvents(t *testing.T) {
	// The database clears plan_training_id when a training is deleted; the
	// event still carries generated_for_week.
	f := newGenerationFixture(t, training(101, 1, 1, SeasonWeekAll, 2, 960, 1050), training(102, 2, 1, SeasonWeekAll, 3, 960, 1050))
	generate(t, f, springPart, "2027-01-04", 1)
	for index := range f.repo.events {
		f.repo.events[index].PlanTrainingID = nil
	}
	f.repo.events[1].EditedAt = int64Ptr(9)
	f.repo.trainings = nil
	result := generate(t, f, springPart, "2027-01-04", 1)
	if result.Counts.Remove != 1 || result.Counts.Keep != 1 || len(f.repo.events) != 1 || f.repo.events[0].EditedAt == nil {
		t.Fatalf("result = %+v, events = %d, want the untouched event removed and the edited one kept", result.Counts, len(f.repo.events))
	}
}

func TestRegenerateKeepsAnEventAnotherPartGenerated(t *testing.T) {
	f := newGenerationFixture(t, training(101, 1, 1, SeasonWeekAll, 2, 960, 1050), autumnTraining(401, 2, 1, SeasonWeekAll, 2, 960, 1050))
	generate(t, f, autumnPart, "2026-12-14", 1)
	if len(f.repo.events) != 1 {
		t.Fatalf("events = %d, want the autumn event", len(f.repo.events))
	}
	// Generating a spring week never lists or touches autumn events, even
	// when asked for the same Monday.
	result, err := f.service.Preview(context.Background(), adminActor().Access, GenerationRequest{PartID: springPart, Week: day("2027-01-04")})
	if err != nil || result.Counts.Remove != 0 || result.Counts.Keep != 0 {
		t.Fatalf("spring preview = %+v, %v, want no interference from autumn events", result.Counts, err)
	}
}

func TestGenerateSkipsArchivedTeamsAndResources(t *testing.T) {
	f := newGenerationFixture(t, training(101, 3, 1, SeasonWeekAll, 2, 960, 1050), training(102, 1, 3, SeasonWeekAll, 3, 960, 1050), training(103, 1, 1, SeasonWeekAll, 4, 960, 1050))
	result := generate(t, f, springPart, "2027-01-04", 1)
	if result.Counts.Skip != 2 || result.Counts.Create != 1 || len(f.repo.events) != 1 {
		t.Fatalf("counts = %+v, events = %d, want 2 skipped and 1 created", result.Counts, len(f.repo.events))
	}
	reasons := []string{}
	for _, item := range result.Items {
		if item.Action == GenerationSkip {
			reasons = append(reasons, item.Reason)
		}
	}
	slices.Sort(reasons)
	if !slices.Equal(reasons, []string{GenerationReasonArchivedResource, GenerationReasonArchivedTeam}) {
		t.Fatalf("skip reasons = %v, want archived team and archived resource", reasons)
	}
}

func TestGenerationAuthorization(t *testing.T) {
	f := newGenerationFixture(t, training(101, 1, 1, SeasonWeekAll, 2, 960, 1050))
	request := GenerationRequest{PartID: springPart, Week: day("2027-01-04")}
	for name, actor := range map[string]GroupActor{
		"admin":      adminActor(),
		"manager":    actorWith(memberWith(GroupRoleManager)),
		"site admin": siteAdminActor(),
	} {
		if _, err := f.service.Preview(context.Background(), actor.Access, request); err != nil {
			t.Fatalf("%s Preview() error = %v", name, err)
		}
		if _, err := f.service.Options(context.Background(), actor.Access, 0); err != nil {
			t.Fatalf("%s Options() error = %v", name, err)
		}
	}
	for name, actor := range map[string]GroupActor{
		"coach":  actorWith(memberWith(GroupRoleCoach)),
		"player": playerOn(1),
		"parent": parentActor(),
	} {
		if _, err := f.service.Generate(context.Background(), actor, request); !errors.Is(err, ErrGroupForbidden) {
			t.Fatalf("%s Generate() error = %v, want ErrGroupForbidden", name, err)
		}
		if _, err := f.service.Preview(context.Background(), actor.Access, request); !errors.Is(err, ErrGroupForbidden) {
			t.Fatalf("%s Preview() error = %v, want ErrGroupForbidden", name, err)
		}
		if _, err := f.service.Options(context.Background(), actor.Access, 0); !errors.Is(err, ErrGroupForbidden) {
			t.Fatalf("%s Options() error = %v, want ErrGroupForbidden", name, err)
		}
	}
	outsider := GroupActor{Access: accessFor(GroupAccessGrants{})}
	if _, err := f.service.Generate(context.Background(), outsider, request); !errors.Is(err, ErrGroupNotFound) {
		t.Fatalf("non-member Generate() error = %v, want ErrGroupNotFound", err)
	}
	if _, err := f.service.Preview(context.Background(), outsider.Access, request); !errors.Is(err, ErrGroupNotFound) {
		t.Fatalf("non-member Preview() error = %v, want ErrGroupNotFound", err)
	}
	if len(f.repo.events) != 0 {
		t.Fatalf("events = %d, want none from refused requests", len(f.repo.events))
	}
}

func TestGenerationOptionsDefaultToNextWeek(t *testing.T) {
	f := newGenerationFixture(t)
	options, err := f.service.Options(context.Background(), adminActor().Access, springPart)
	if err != nil || !options.Found || options.Part.ID != springPart {
		t.Fatalf("Options() = %+v, %v, want the spring part", options, err)
	}
	// Today in the stub is 2027-02-01 (a Monday); next week is 2027-02-08.
	if !options.DefaultWeek.Equal(day("2027-02-08")) || !options.Weeks[0].Equal(day("2027-01-04")) || !options.Weeks[len(options.Weeks)-1].Equal(day("2027-06-28")) {
		t.Fatalf("weeks = %v..%v default %v, want 2027-01-04..2027-06-28 defaulting to 2027-02-08", options.Weeks[0], options.Weeks[len(options.Weeks)-1], options.DefaultWeek)
	}
}
