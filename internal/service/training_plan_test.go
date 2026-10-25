package service

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
)

// Fixture: group 5, Spring (part 12, alternating) of season 2026/27 and
// Autumn (part 11, not alternating). Teams: Lions (ages 9-10), Tigers
// (11-12), archived Owls. Coaches: 7 Anna coaches Lions, 8 Bela coaches
// Tigers. Resources: 1 Field (no limit), 2 Court (limit 1), 3 archived Hall.
// Weekday 2 = Tuesday; 960 = 16:00, 1020 = 17:00, 1080 = 18:00.

type trainingRepoStub struct {
	trainings []PlanTraining
	nextID    int64
	err       error
	replaced  int
}

func (r *trainingRepoStub) ListPlanTrainings(_ context.Context, groupID, partID int64) ([]PlanTraining, error) {
	if r.err != nil {
		return nil, r.err
	}
	var list []PlanTraining
	for _, training := range r.trainings {
		if training.GroupID == groupID && training.SeasonPartID == partID {
			list = append(list, training)
		}
	}
	return list, nil
}

func (r *trainingRepoStub) ReplacePlanTrainings(ctx context.Context, groupID, partID, createdAt int64, edit func([]PlanTraining) ([]PlanTraining, error)) (TrainingChanges, error) {
	current, _ := r.ListPlanTrainings(ctx, groupID, partID)
	desired, err := edit(current)
	if err != nil {
		return TrainingChanges{}, err
	}
	diff := DiffPlanTrainings(current, desired)
	r.trainings = slices.DeleteFunc(r.trainings, func(t PlanTraining) bool { return t.GroupID == groupID && t.SeasonPartID == partID })
	for _, training := range desired {
		if training.ID == 0 {
			r.nextID++
			training.ID = 1000 + r.nextID
		}
		training.GroupID, training.SeasonPartID, training.CreatedAt = groupID, partID, createdAt
		r.trainings = append(r.trainings, training)
	}
	r.replaced++
	return diff.Changes, nil
}

type trainingSeasonStub struct {
	seasons []Season
	coaches []SeasonCoach
}

func (s trainingSeasonStub) List(context.Context, *GroupAccess) ([]Season, error) {
	return slices.Clone(s.seasons), nil
}

func (s trainingSeasonStub) FindPart(_ context.Context, _ *GroupAccess, partID int64) (Season, SeasonPart, error) {
	for _, season := range s.seasons {
		if part, ok := season.Part(partID); ok {
			return season, part, nil
		}
	}
	return Season{}, SeasonPart{}, ErrSeasonNotFound
}

func (s trainingSeasonStub) Today(context.Context, *GroupAccess) (time.Time, error) {
	return day("2027-02-01"), nil
}

func (s trainingSeasonStub) SeasonCoaches(_ context.Context, _ *GroupAccess, seasonID int64) ([]SeasonCoach, error) {
	var list []SeasonCoach
	for _, coach := range s.coaches {
		if coach.SeasonID == seasonID {
			list = append(list, coach)
		}
	}
	return list, nil
}

type trainingSlotStub struct{ slots []PlanSlot }

func (s trainingSlotStub) Slots(_ context.Context, _ *GroupAccess, partID int64) ([]PlanSlot, error) {
	var list []PlanSlot
	for _, slot := range s.slots {
		if slot.SeasonPartID == partID {
			list = append(list, slot)
		}
	}
	return list, nil
}

type trainingResourceStub struct{ resources []Resource }

func (s trainingResourceStub) List(context.Context, *GroupAccess) ([]Resource, error) {
	return s.resources, nil
}

func (s trainingResourceStub) Find(_ context.Context, _ *GroupAccess, id int64) (Resource, error) {
	for _, resource := range s.resources {
		if resource.ID == id {
			return resource, nil
		}
	}
	return Resource{}, ErrResourceNotFound
}

type trainingAvailabilityStub struct{ windows []AvailabilityWindow }

func (s trainingAvailabilityStub) Availability(_ context.Context, access *GroupAccess, partID int64) (PartAvailability, error) {
	for _, season := range []Season{season2627(), boardSeason2526()} {
		if part, ok := season.Part(partID); ok {
			var windows []AvailabilityWindow
			for _, window := range s.windows {
				if window.SeasonPartID == partID {
					windows = append(windows, window)
				}
			}
			return PartAvailability{Season: season, Part: part, Windows: windows}, nil
		}
	}
	return PartAvailability{}, ErrSeasonNotFound
}

type trainingFixture struct {
	service *TrainingPlanService
	repo    *trainingRepoStub
	slots   *trainingSlotStub
	avail   *trainingAvailabilityStub
	auditor *auditRecorderStub
}

const (
	springPart = 12
	autumnPart = 11
)

func trainingResources() []Resource {
	one := 1
	return []Resource{
		{ID: 1, GroupID: 5, Name: "Field"},
		{ID: 2, GroupID: 5, Name: "Court", MaxConcurrentTeams: &one},
		{ID: 3, GroupID: 5, Name: "Hall", ArchivedAt: int64Ptr(1)},
	}
}

func newTrainingFixture(t *testing.T) trainingFixture {
	t.Helper()
	anna, bela := "Anna", "Bela"
	seasons := trainingSeasonStub{
		seasons: []Season{season2627(), boardSeason2526()},
		coaches: []SeasonCoach{
			{GroupID: 5, SeasonID: 1, TeamID: 1, CoachUserID: 7, DisplayName: &anna, IsHead: true},
			{GroupID: 5, SeasonID: 1, TeamID: 2, CoachUserID: 8, DisplayName: &bela},
		},
	}
	repo := &trainingRepoStub{}
	slots := &trainingSlotStub{}
	avail := &trainingAvailabilityStub{}
	service, err := NewTrainingPlanService(repo, seasons, slots, trainingResourceStub{trainingResources()},
		newTeamStub(boardLions, boardTigers, boardOwls), avail, usersWith(User{ID: 7, Email: strPtr("staff@example.test")}))
	if err != nil {
		t.Fatalf("NewTrainingPlanService() error = %v", err)
	}
	auditor := &auditRecorderStub{}
	service.SetAuditor(auditor)
	service.now = func() time.Time { return time.Date(2027, 2, 1, 12, 0, 0, 0, time.UTC) }
	return trainingFixture{service: service, repo: repo, slots: slots, avail: avail, auditor: auditor}
}

func slot(id, resource int64, week string, weekday, start, end int) PlanSlot {
	return PlanSlot{ID: id, GroupID: 5, SeasonPartID: springPart, ResourceID: resource, Week: week, Weekday: weekday, StartMinute: start, EndMinute: end}
}

func window(coach int64, week string, weekday, start, end int) AvailabilityWindow {
	return AvailabilityWindow{GroupID: 5, SeasonPartID: springPart, CoachUserID: coach, Week: week, Weekday: weekday, StartMinute: start, EndMinute: end}
}

func training(id, team, resource int64, week string, weekday, start, end int) PlanTraining {
	return PlanTraining{ID: id, GroupID: 5, SeasonPartID: springPart, TeamID: team, ResourceID: resource, Week: week, Weekday: weekday, StartMinute: start, EndMinute: end}
}

var springPartValue = func() SeasonPart { part, _ := season2627().Part(springPart); return part }()

func warningsFor(t *testing.T, trainings []PlanTraining, slots []PlanSlot, windows []AvailabilityWindow) []TrainingWarning {
	t.Helper()
	availability := PartAvailability{Season: season2627(), Part: springPartValue, Windows: windows}
	assigned := slices.Clone(trainings)
	for index := range assigned {
		if best, ok := bestSlot(assigned[index], slots, true); ok {
			assigned[index].PlanSlotID = best.ID
		}
	}
	return TrainingWarnings(TrainingWarningInput{
		Season: season2627(), Part: springPartValue, Trainings: assigned, Slots: slots, Resources: trainingResources(),
		Teams:        map[int64]Team{1: boardLions, 2: boardTigers, 6: boardOwls},
		Coaches:      map[int64][]int64{1: {7}, 2: {8}},
		Availability: &availability,
	})
}

func kinds(warnings []TrainingWarning, index int) []string {
	var list []string
	for _, warning := range warnings {
		if warning.Index == index {
			list = append(list, warning.Kind)
		}
	}
	return list
}

func TestTrainingWarningsCleanTraining(t *testing.T) {
	slots := []PlanSlot{slot(1, 1, SeasonWeekAll, 2, 960, 1080)}
	windows := []AvailabilityWindow{window(7, SeasonWeekAll, 2, 900, 1100)}
	warnings := warningsFor(t, []PlanTraining{training(1, 1, 1, SeasonWeekAll, 2, 960, 1050)}, slots, windows)
	if len(warnings) != 0 {
		t.Fatalf("warnings = %+v, want none", warnings)
	}
}

func TestTrainingWarningsPerRule(t *testing.T) {
	ageSlot := slot(1, 1, SeasonWeekAll, 2, 960, 1080)
	ageSlot.MinAge, ageSlot.MaxAge = intPtr(11), intPtr(14)
	teamSlot := slot(2, 1, SeasonWeekAll, 3, 960, 1080)
	teamSlot.MaxTeams = intPtr(1)
	everyone := []AvailabilityWindow{
		window(7, SeasonWeekAll, 2, 0, 1440), window(7, SeasonWeekAll, 3, 0, 1440), window(7, SeasonWeekAll, 4, 0, 1440), window(7, SeasonWeekAll, 5, 0, 1440),
		window(8, SeasonWeekAll, 2, 0, 1440), window(8, SeasonWeekAll, 3, 0, 1440), window(8, SeasonWeekAll, 4, 0, 1440), window(8, SeasonWeekAll, 5, 0, 1440),
	}
	roomy := slot(4, 1, SeasonWeekAll, 5, 900, 1100)
	roomy.MaxTeams = intPtr(2)
	courtSlot := slot(3, 2, SeasonWeekAll, 4, 960, 1080)
	courtSlot.MaxTeams = intPtr(5)
	for name, test := range map[string]struct {
		trainings []PlanTraining
		slots     []PlanSlot
		windows   []AvailabilityWindow
		index     int
		want      []string
	}{
		"no slot": {
			trainings: []PlanTraining{training(1, 1, 1, SeasonWeekAll, 2, 960, 1050)}, windows: everyone,
			want: []string{TrainingWarningNoSlot},
		},
		"partly outside the slot": {
			trainings: []PlanTraining{training(1, 1, 1, SeasonWeekAll, 2, 1020, 1140)}, slots: []PlanSlot{slot(1, 1, SeasonWeekAll, 2, 960, 1080)}, windows: everyone,
			want: []string{TrainingWarningOutsideSlot},
		},
		"slot ages": {
			trainings: []PlanTraining{training(1, 1, 1, SeasonWeekAll, 2, 960, 1050)}, slots: []PlanSlot{ageSlot}, windows: everyone,
			want: []string{TrainingWarningSlotAges},
		},
		"slot full": {
			trainings: []PlanTraining{training(1, 1, 1, SeasonWeekAll, 3, 960, 1050), training(2, 2, 1, SeasonWeekAll, 3, 1000, 1080)}, slots: []PlanSlot{teamSlot}, windows: everyone,
			want: []string{TrainingWarningSlotFull},
		},
		"resource full": {
			trainings: []PlanTraining{training(1, 1, 2, SeasonWeekAll, 4, 960, 1050), training(2, 2, 2, SeasonWeekAll, 4, 1000, 1080)}, slots: []PlanSlot{courtSlot}, windows: everyone,
			want: []string{TrainingWarningResourceFull},
		},
		"team twice at once": {
			trainings: []PlanTraining{training(1, 1, 1, SeasonWeekAll, 5, 960, 1050), training(2, 1, 1, SeasonWeekAll, 5, 1000, 1080)}, slots: []PlanSlot{roomy}, windows: everyone,
			want: []string{TrainingWarningTeamOverlap},
		},
		"coach not available": {
			trainings: []PlanTraining{training(1, 1, 1, SeasonWeekAll, 2, 960, 1050)}, slots: []PlanSlot{slot(1, 1, SeasonWeekAll, 2, 960, 1080)},
			windows: []AvailabilityWindow{window(7, SeasonWeekAll, 2, 1000, 1100)},
			want:    []string{TrainingWarningCoachUnavailable},
		},
		"coach available in week B only": {
			trainings: []PlanTraining{training(1, 1, 1, SeasonWeekAll, 2, 960, 1050)}, slots: []PlanSlot{slot(1, 1, SeasonWeekAll, 2, 960, 1080)},
			windows: []AvailabilityWindow{window(7, SeasonWeekB, 2, 900, 1100)},
			want:    []string{TrainingWarningCoachUnavailable},
		},
		"archived team and resource, no coach": {
			trainings: []PlanTraining{training(1, 6, 3, SeasonWeekAll, 2, 960, 1050)}, slots: []PlanSlot{slot(1, 3, SeasonWeekAll, 2, 960, 1080)}, windows: everyone,
			want: []string{TrainingWarningTeamArchived, TrainingWarningResourceArchived, TrainingWarningNoCoach},
		},
	} {
		got := kinds(warningsFor(t, test.trainings, test.slots, test.windows), test.index)
		if !slices.Equal(got, test.want) {
			t.Errorf("%s: warnings = %v, want %v", name, got, test.want)
		}
	}
}

func TestTrainingWarningsCoachBusyAcrossTeams(t *testing.T) {
	// Anna also coaches Tigers: the two trainings overlap on different fields.
	anna := []int64{7}
	availability := PartAvailability{Season: season2627(), Part: springPartValue, Windows: []AvailabilityWindow{window(7, SeasonWeekAll, 2, 0, 1440), window(8, SeasonWeekAll, 2, 0, 1440)}}
	trainings := []PlanTraining{training(1, 1, 1, SeasonWeekAll, 2, 960, 1050), training(2, 2, 1, SeasonWeekA, 2, 1000, 1080), training(3, 2, 1, SeasonWeekB, 2, 1000, 1080)}
	slots := []PlanSlot{slot(1, 1, SeasonWeekAll, 2, 900, 1100)}
	for index := range trainings {
		trainings[index].PlanSlotID = 1
	}
	warnings := TrainingWarnings(TrainingWarningInput{
		Season: season2627(), Part: springPartValue, Trainings: trainings, Slots: slots, Resources: trainingResources(),
		Teams:   map[int64]Team{1: boardLions, 2: boardTigers},
		Coaches: map[int64][]int64{1: anna, 2: {7, 8}}, Availability: &availability,
	})
	var busy []TrainingWarning
	for _, warning := range warnings {
		if warning.Kind == TrainingWarningCoachBusy {
			busy = append(busy, warning)
		}
	}
	// Lions' training meets both the A and the B training of Tigers; each of
	// those meets Lions' training; A and B never meet each other.
	if len(busy) != 4 {
		t.Fatalf("coach busy warnings = %+v, want 4", busy)
	}
	for _, warning := range busy {
		if warning.CoachUserID != 7 || warning.Other < 0 {
			t.Errorf("warning = %+v, want coach 7 and the other training", warning)
		}
	}
	if slices.ContainsFunc(warnings, func(w TrainingWarning) bool { return w.Kind == TrainingWarningTeamOverlap }) {
		t.Errorf("A and B trainings of the same team overlap warning in %+v", warnings)
	}
}

func TestSuggestTrainingsPrefersPreviousSeason(t *testing.T) {
	slots := []PlanSlot{slot(1, 1, SeasonWeekAll, 2, 960, 1080)}
	previous := []PlanTraining{
		training(50, 1, 1, SeasonWeekAll, 4, 1000, 1090),
		training(51, 1, 1, SeasonWeekAll, 4, 1000, 1090), // duplicate
		training(52, 1, 3, SeasonWeekAll, 4, 1000, 1090), // archived resource
		training(53, 2, 1, SeasonWeekAll, 5, 1000, 1090), // Tigers already planned
	}
	suggested, unplaced := SuggestTrainings(TrainingSuggestionInput{
		Season: season2627(), Part: springPartValue, Teams: []Team{boardLions, boardTigers},
		Coaches: map[int64][]int64{1: {7}, 2: {8}}, Slots: slots, Resources: trainingResources(),
		Previous: previous, Current: []PlanTraining{training(9, 2, 1, SeasonWeekAll, 3, 960, 1050)},
	})
	if unplaced != 0 || len(suggested) != 1 {
		t.Fatalf("suggested = %+v, unplaced = %d; want only Lions' previous training", suggested, unplaced)
	}
	got := suggested[0]
	if got.Reason != TrainingSuggestionPreviousSeason || got.Training.TeamID != 1 || got.Training.Weekday != 4 || got.Training.ID != 0 {
		t.Fatalf("suggestion = %+v, want Lions on Thursday as a new previous-season training", got)
	}
}

func TestSuggestTrainingsFitsSlots(t *testing.T) {
	tigersOnly := slot(1, 1, SeasonWeekAll, 2, 960, 1080)
	tigersOnly.MinAge, tigersOnly.MaxAge = intPtr(11), intPtr(14)
	anyAge := slot(2, 1, SeasonWeekAll, 2, 900, 1000)
	small := slot(3, 2, SeasonWeekAll, 3, 960, 1080)
	windows := []AvailabilityWindow{
		window(7, SeasonWeekAll, 2, 900, 1000), window(7, SeasonWeekAll, 3, 960, 1080),
		window(8, SeasonWeekAll, 2, 960, 1080), window(8, SeasonWeekAll, 3, 960, 1080),
	}
	availability := PartAvailability{Season: season2627(), Part: springPartValue, Windows: windows}
	input := TrainingSuggestionInput{
		Season: season2627(), Part: springPartValue, Teams: []Team{boardLions, boardTigers},
		Coaches: map[int64][]int64{1: {7}, 2: {8}}, Slots: []PlanSlot{anyAge, tigersOnly, small}, Resources: trainingResources(),
		Availability: &availability,
	}
	suggested, unplaced := SuggestTrainings(input)
	if unplaced != 0 || len(suggested) != 2 {
		t.Fatalf("suggested = %+v, unplaced = %d, want both teams placed", suggested, unplaced)
	}
	// Lions (9-10) cannot use the 11-14 slot; the open slot is theirs.
	lions, tigers := suggested[0].Training, suggested[1].Training
	if lions.TeamID != 1 || lions.PlanSlotID != 2 || lions.Weekday != 2 || lions.StartMinute != 900 {
		t.Errorf("Lions = %+v, want the open slot 2", lions)
	}
	// Tigers take the age-specific slot first (slots with ages come before open ones).
	if tigers.TeamID != 2 || tigers.PlanSlotID != 1 || tigers.StartMinute != 960 {
		t.Errorf("Tigers = %+v, want the aged slot 1", tigers)
	}
	for _, s := range suggested {
		if s.Reason != TrainingSuggestionSlot {
			t.Errorf("reason = %q, want slot", s.Reason)
		}
	}
	again, _ := SuggestTrainings(input)
	if !slices.EqualFunc(suggested, again, func(a, b SuggestedTraining) bool { return a == b }) {
		t.Fatalf("suggestion not deterministic: %+v vs %+v", suggested, again)
	}
}

func TestSuggestTrainingsRespectsCapacityAndCoaches(t *testing.T) {
	// One open slot with room for one team: the second team stays out.
	only := slot(1, 1, SeasonWeekAll, 2, 960, 1080)
	everyone := PartAvailability{Season: season2627(), Part: springPartValue, Windows: []AvailabilityWindow{
		window(7, SeasonWeekAll, 2, 0, 1440), window(8, SeasonWeekAll, 2, 0, 1440),
	}}
	input := TrainingSuggestionInput{
		Season: season2627(), Part: springPartValue, Teams: []Team{boardLions, boardTigers},
		Coaches: map[int64][]int64{1: {7}, 2: {8}}, Slots: []PlanSlot{only}, Resources: trainingResources(), Availability: &everyone,
	}
	suggested, unplaced := SuggestTrainings(input)
	if len(suggested) != 1 || unplaced != 1 || suggested[0].Training.TeamID != 1 {
		t.Fatalf("slot capacity: suggested = %+v, unplaced = %d, want Lions placed and Tigers unplaced", suggested, unplaced)
	}

	// Room for two, but one coach coaches both teams: no double booking.
	room := slot(1, 1, SeasonWeekAll, 2, 960, 1080)
	room.MaxTeams = intPtr(2)
	input.Slots = []PlanSlot{room}
	input.Coaches = map[int64][]int64{1: {7}, 2: {7}}
	suggested, unplaced = SuggestTrainings(input)
	if len(suggested) != 1 || unplaced != 1 {
		t.Fatalf("shared coach: suggested = %+v, unplaced = %d, want one team placed", suggested, unplaced)
	}

	// Different coaches: both fit the shared slot.
	input.Coaches = map[int64][]int64{1: {7}, 2: {8}}
	if suggested, unplaced = SuggestTrainings(input); len(suggested) != 2 || unplaced != 0 {
		t.Fatalf("room for two: suggested = %+v, unplaced = %d, want both placed", suggested, unplaced)
	}

	// Resource limit 1 on the court, slot allows two: the resource wins.
	court := slot(1, 2, SeasonWeekAll, 2, 960, 1080)
	court.MaxTeams = intPtr(2)
	input.Slots = []PlanSlot{court}
	if suggested, unplaced = SuggestTrainings(input); len(suggested) != 1 || unplaced != 1 {
		t.Fatalf("resource capacity: suggested = %+v, unplaced = %d, want one team placed", suggested, unplaced)
	}

	// All of a team's coaches must be available: Bela is not.
	input.Slots = []PlanSlot{room}
	input.Availability = &PartAvailability{Season: season2627(), Part: springPartValue, Windows: []AvailabilityWindow{window(7, SeasonWeekAll, 2, 0, 1440)}}
	suggested, unplaced = SuggestTrainings(input)
	if len(suggested) != 1 || suggested[0].Training.TeamID != 1 || unplaced != 1 {
		t.Fatalf("availability: suggested = %+v, unplaced = %d, want only Lions", suggested, unplaced)
	}

	// An every-week slot falls back to week A when the coach has only A.
	input.Teams = []Team{boardLions}
	input.Availability = &PartAvailability{Season: season2627(), Part: springPartValue, Windows: []AvailabilityWindow{window(7, SeasonWeekA, 2, 0, 1440)}}
	suggested, _ = SuggestTrainings(input)
	if len(suggested) != 1 || suggested[0].Training.Week != SeasonWeekA {
		t.Fatalf("week fallback: suggested = %+v, want a week A training", suggested)
	}
}

func TestSuggestTrainingsLeavesArchivedResourcesAndKeepsCurrent(t *testing.T) {
	hall := slot(1, 3, SeasonWeekAll, 2, 960, 1080)
	suggested, unplaced := SuggestTrainings(TrainingSuggestionInput{
		Season: season2627(), Part: springPartValue, Teams: []Team{boardLions},
		Coaches: map[int64][]int64{1: {7}}, Slots: []PlanSlot{hall}, Resources: trainingResources(),
	})
	if len(suggested) != 0 || unplaced != 1 {
		t.Fatalf("archived resource: suggested = %+v, unplaced = %d", suggested, unplaced)
	}
}

func TestPlanTrainingDiffAndRevision(t *testing.T) {
	current := []PlanTraining{training(1, 1, 1, SeasonWeekAll, 2, 960, 1050), training(2, 2, 1, SeasonWeekAll, 3, 960, 1050), training(3, 1, 1, SeasonWeekAll, 4, 960, 1050)}
	moved := training(1, 1, 1, SeasonWeekAll, 2, 1000, 1090)
	slotOnly := training(2, 2, 1, SeasonWeekAll, 3, 960, 1050)
	slotOnly.PlanSlotID = 9
	added := training(0, 2, 2, SeasonWeekAll, 5, 960, 1050)
	forged := training(99, 1, 2, SeasonWeekAll, 6, 960, 1050)
	diff := DiffPlanTrainings(current, []PlanTraining{moved, slotOnly, added, forged})
	if diff.Changes != (TrainingChanges{Added: 2, Moved: 1, Removed: 1}) {
		t.Fatalf("changes = %+v, want 2 added, 1 moved, 1 removed", diff.Changes)
	}
	if len(diff.Update) != 2 || diff.Update[0].ID != 1 || diff.Update[1].ID != 2 || len(diff.Delete) != 1 || diff.Delete[0].ID != 3 {
		t.Fatalf("diff = %+v", diff)
	}
	for _, insert := range diff.Insert {
		if insert.ID != 0 {
			t.Errorf("insert %+v kept an ID", insert)
		}
	}
	if none := DiffPlanTrainings(current, current); none.Changes.Total() != 0 || len(none.Update) != 0 {
		t.Fatalf("identical sets diff = %+v", none)
	}
	reversed := []PlanTraining{current[2], current[0], current[1]}
	if TrainingRevision(current) != TrainingRevision(reversed) {
		t.Error("revision depends on order")
	}
	if TrainingRevision(current) == TrainingRevision(current[:2]) || TrainingRevision(current) == TrainingRevision([]PlanTraining{moved, current[1], current[2]}) {
		t.Error("revision ignores a change")
	}
}

func TestNormalizeTrainingItems(t *testing.T) {
	a := TrainingDraftItem{PlanTraining: training(1, 1, 1, "", 2, 960, 1050)}
	dup := a
	dup.ID = 2
	got, err := normalizeTrainingItems(springPartValue, []TrainingDraftItem{a, dup})
	if err != nil || len(got) != 1 || got[0].Week != SeasonWeekAll {
		t.Fatalf("normalize = %+v, %v; want one every-week training", got, err)
	}
	autumn, _ := season2627().Part(autumnPart)
	b := TrainingDraftItem{PlanTraining: training(0, 1, 1, SeasonWeekB, 2, 960, 1050)}
	if got, err = normalizeTrainingItems(autumn, []TrainingDraftItem{b}); err != nil || got[0].Week != SeasonWeekAll {
		t.Fatalf("non-alternating part: %+v, %v; want every week", got, err)
	}
	for name, item := range map[string]PlanTraining{
		"weekday":     training(0, 1, 1, SeasonWeekAll, 0, 960, 1050),
		"end":         training(0, 1, 1, SeasonWeekAll, 2, 960, 1500),
		"start order": training(0, 1, 1, SeasonWeekAll, 2, 1050, 960),
	} {
		if _, err := normalizeTrainingItems(springPartValue, []TrainingDraftItem{{PlanTraining: item}}); !errors.Is(err, ErrInvalidTrainingTime) {
			t.Errorf("%s: error = %v, want ErrInvalidTrainingTime", name, err)
		}
	}
	if _, err := normalizeTrainingItems(springPartValue, []TrainingDraftItem{{PlanTraining: training(0, 1, 1, "C", 2, 960, 1050)}}); !errors.Is(err, ErrInvalidTrainingWeek) {
		t.Errorf("week error = %v", err)
	}
}

func TestBestSlotPrefersContainingSlot(t *testing.T) {
	long := slot(1, 1, SeasonWeekAll, 2, 900, 1200)
	short := slot(2, 1, SeasonWeekAll, 2, 960, 1080)
	other := slot(3, 1, SeasonWeekAll, 2, 1080, 1140)
	trainingIn := training(0, 1, 1, SeasonWeekAll, 2, 960, 1050)
	if best, ok := bestSlot(trainingIn, []PlanSlot{long, short}, true); !ok || best.ID != 2 {
		t.Fatalf("best = %+v, want the shortest containing slot 2", best)
	}
	straddle := training(0, 1, 1, SeasonWeekAll, 2, 1000, 1120)
	if best, ok := bestSlot(straddle, []PlanSlot{short, other}, true); !ok || best.ID != 2 {
		t.Fatalf("best = %+v, want the slot with most overlap", best)
	}
	if _, ok := bestSlot(training(0, 1, 2, SeasonWeekAll, 2, 960, 1050), []PlanSlot{short}, true); ok {
		t.Fatal("slot of another resource matched")
	}
	weekA := slot(4, 1, SeasonWeekA, 2, 900, 1200)
	if _, ok := bestSlot(trainingIn, []PlanSlot{weekA}, true); ok {
		t.Fatal("an A slot covers an every-week training")
	}
}

func TestTrainingPlanBoardAccess(t *testing.T) {
	f := newTrainingFixture(t)
	ctx := context.Background()
	for name, test := range map[string]struct {
		access *GroupAccess
		want   error
	}{
		"outsider": {accessFor(GroupAccessGrants{}), ErrGroupNotFound},
		"player":   {accessFor(memberWith(GroupRolePlayer)), ErrGroupForbidden},
		"parent":   {accessFor(memberWith(GroupRoleParent)), ErrGroupForbidden},
		"coach":    {accessFor(memberWith(GroupRoleCoach)), nil},
		"manager":  {accessFor(memberWith(GroupRoleManager)), nil},
	} {
		if _, err := f.service.Board(ctx, test.access, TrainingBoardRequest{PartID: springPart}); !errors.Is(err, test.want) {
			t.Errorf("%s: Board error = %v, want %v", name, err, test.want)
		}
		if _, err := f.service.Trainings(ctx, test.access, springPart); !errors.Is(err, test.want) {
			t.Errorf("%s: Trainings error = %v, want %v", name, err, test.want)
		}
	}
	if _, err := f.service.Board(ctx, accessFor(memberWith(GroupRoleAdmin)), TrainingBoardRequest{PartID: 77}); !errors.Is(err, ErrSeasonNotFound) {
		t.Errorf("unknown part error = %v, want ErrSeasonNotFound", err)
	}
	board, err := f.service.Board(ctx, accessFor(memberWith(GroupRoleAdmin)), TrainingBoardRequest{})
	if err != nil || board.Part.ID != springPart {
		t.Fatalf("default board = part %d, %v; want the part containing today (spring)", board.Part.ID, err)
	}
}

func TestTrainingPlanBoardReadOnlyForCoaches(t *testing.T) {
	f := newTrainingFixture(t)
	f.repo.trainings = []PlanTraining{training(1, 1, 1, SeasonWeekAll, 2, 960, 1050)}
	board, err := f.service.Board(context.Background(), accessFor(memberWith(GroupRoleCoach)), TrainingBoardRequest{
		PartID: springPart, Suggest: TrainingSuggestReplace, Draft: &TrainingDraft{Items: []TrainingDraftItem{{PlanTraining: training(0, 2, 1, SeasonWeekAll, 3, 960, 1050)}}},
	})
	if err != nil {
		t.Fatalf("Board(coach) error = %v", err)
	}
	if board.CanEdit || board.Changed || board.Suggested != 0 || len(board.Warnings) != 0 || len(board.Trainings) != 1 || board.Trainings[0].Training.ID != 1 {
		t.Fatalf("coach board = %+v; want the saved training only, no warnings or drafts", board)
	}
	if len(board.Teams) != 2 || len(board.Teams[0].Coaches) != 1 {
		t.Fatalf("teams = %+v; want team columns with their coaches", board.Teams)
	}
}

func TestTrainingPlanBoardSuggestAndWarnings(t *testing.T) {
	f := newTrainingFixture(t)
	f.slots.slots = []PlanSlot{slot(1, 1, SeasonWeekAll, 2, 960, 1080), slot(2, 1, SeasonWeekAll, 3, 960, 1080)}
	f.avail.windows = []AvailabilityWindow{window(7, SeasonWeekAll, 2, 900, 1100)}
	access := accessFor(memberWith(GroupRoleAdmin))
	board, err := f.service.Board(context.Background(), access, TrainingBoardRequest{PartID: springPart, Suggest: TrainingSuggestFill})
	if err != nil {
		t.Fatalf("Board() error = %v", err)
	}
	// Lions fit slot 1 (Anna is free Tuesday). Tigers are 11-12, Bela is not
	// available at all: no slot fits.
	if board.Suggested != 1 || board.Unplaced != 1 || !board.Changed || !board.Stale() == false {
		t.Fatalf("board = suggested %d, unplaced %d, changed %v; want 1, 1, true", board.Suggested, board.Unplaced, board.Changed)
	}
	if len(board.Trainings) != 1 || board.Trainings[0].Training.TeamID != 1 || board.Trainings[0].Saved || board.Trainings[0].Reason != TrainingSuggestionSlot {
		t.Fatalf("trainings = %+v", board.Trainings)
	}
	if !board.HasPrevious || board.PreviousSeason.ID != 3 {
		t.Errorf("previous = %v %d, want season 3", board.HasPrevious, board.PreviousSeason.ID)
	}
	// A hand-made draft moves Lions to Wednesday: Anna is not available.
	draft := TrainingDraft{Revision: board.Revision, Items: []TrainingDraftItem{{PlanTraining: training(0, 1, 1, SeasonWeekAll, 3, 960, 1050)}, {PlanTraining: training(0, 99, 1, SeasonWeekAll, 3, 960, 1050)}}}
	board, err = f.service.Board(context.Background(), access, TrainingBoardRequest{PartID: springPart, Draft: &draft})
	if err != nil || len(board.Trainings) != 1 {
		t.Fatalf("draft board = %+v, %v; want the unknown team dropped", board.Trainings, err)
	}
	if got := kinds(board.Warnings, 0); !slices.Equal(got, []string{TrainingWarningCoachUnavailable}) {
		t.Fatalf("warnings = %v, want coach unavailable", got)
	}
}

func TestTrainingPlanSave(t *testing.T) {
	f := newTrainingFixture(t)
	f.slots.slots = []PlanSlot{slot(1, 1, SeasonWeekAll, 2, 900, 1100)}
	ctx := context.Background()
	actor := adminActor()
	empty := TrainingRevision(nil)
	first := []PlanTraining{training(0, 1, 1, SeasonWeekAll, 2, 960, 1050), training(0, 2, 1, SeasonWeekA, 3, 960, 1050)}

	changes, err := f.service.Save(ctx, actor, springPart, empty, first)
	if err != nil || changes != (TrainingChanges{Added: 2}) {
		t.Fatalf("Save() = %+v, %v; want 2 added", changes, err)
	}
	if len(f.auditor.records) != 1 || f.auditor.records[0].Action != AuditActionTrainingPlanSaved || !strings.Contains(f.auditor.records[0].Detail, "added 2") {
		t.Fatalf("audit = %+v", f.auditor.records)
	}
	for _, record := range f.auditor.records {
		if strings.Contains(record.Detail, "Lions") {
			t.Errorf("audit detail names a team: %q", record.Detail)
		}
	}
	if len(f.repo.trainings) != 2 || f.repo.trainings[0].PlanSlotID != 1 || f.repo.trainings[1].PlanSlotID != 0 {
		t.Fatalf("stored = %+v; want the Tuesday training in slot 1 and the Wednesday one without", f.repo.trainings)
	}

	// Saving the same set again changes nothing and audits nothing.
	saved, _ := f.service.Trainings(ctx, actor.Access, springPart)
	revision := TrainingRevision(saved)
	if changes, err = f.service.Save(ctx, actor, springPart, revision, saved); err != nil || changes.Total() != 0 || len(f.auditor.records) != 1 {
		t.Fatalf("repeat Save() = %+v, %v, audits %d; want nothing changed, no audit", changes, err, len(f.auditor.records))
	}

	// A move keeps the training's identity.
	moved := slices.Clone(saved)
	moved[0].StartMinute, moved[0].EndMinute = 1000, 1090
	if changes, err = f.service.Save(ctx, actor, springPart, revision, moved); err != nil || changes != (TrainingChanges{Moved: 1}) {
		t.Fatalf("move Save() = %+v, %v", changes, err)
	}
	if !slices.ContainsFunc(f.repo.trainings, func(t PlanTraining) bool { return t.ID == saved[0].ID && t.StartMinute == 1000 }) {
		t.Fatalf("stored = %+v; want the moved training to keep its ID", f.repo.trainings)
	}

	// The old revision is stale; a vanished part is not a conflict.
	if _, err = f.service.Save(ctx, actor, springPart, revision, saved); !errors.Is(err, ErrOptimisticLockConflict) {
		t.Errorf("stale Save() error = %v, want conflict", err)
	}
	if _, err = f.service.Save(ctx, actor, 77, revision, saved); !errors.Is(err, ErrSeasonNotFound) {
		t.Errorf("unknown part error = %v", err)
	}
	current, _ := f.service.Trainings(ctx, actor.Access, springPart)
	for name, bad := range map[string]struct {
		set  []PlanTraining
		want error
	}{
		"unknown team":     {[]PlanTraining{training(0, 99, 1, SeasonWeekAll, 2, 960, 1050)}, ErrTeamNotFound},
		"unknown resource": {[]PlanTraining{training(0, 1, 99, SeasonWeekAll, 2, 960, 1050)}, ErrResourceNotFound},
		"bad time":         {[]PlanTraining{training(0, 1, 1, SeasonWeekAll, 2, 1050, 960)}, ErrInvalidTrainingTime},
		"bad week":         {[]PlanTraining{training(0, 1, 1, "X", 2, 960, 1050)}, ErrInvalidTrainingWeek},
	} {
		if _, err = f.service.Save(ctx, actor, springPart, TrainingRevision(current), bad.set); !errors.Is(err, bad.want) {
			t.Errorf("%s: error = %v, want %v", name, err, bad.want)
		}
	}
	if _, err = f.service.Save(ctx, actor, springPart, TrainingRevision(current), make([]PlanTraining, MaxTrainingsPerPart+1)); !errors.Is(err, ErrTrainingPlanTooLarge) {
		t.Errorf("too many error = %v", err)
	}
	for name, access := range map[string]*GroupAccess{
		"coach": accessFor(memberWith(GroupRoleCoach)), "player": accessFor(memberWith(GroupRolePlayer)),
	} {
		if _, err = f.service.Save(ctx, GroupActor{Access: access}, springPart, TrainingRevision(current), nil); !errors.Is(err, ErrGroupForbidden) {
			t.Errorf("%s: Save error = %v, want forbidden", name, err)
		}
	}
	if _, err = f.service.Save(ctx, GroupActor{Access: accessFor(GroupAccessGrants{})}, springPart, empty, nil); !errors.Is(err, ErrGroupNotFound) {
		t.Errorf("outsider Save error = %v, want not found", err)
	}
	if _, err = f.service.Save(ctx, siteAdminActor(), springPart, TrainingRevision(current), current); err != nil {
		t.Errorf("site admin Save error = %v", err)
	}
}

func TestNewTrainingPlanServiceRejectsNilDependencies(t *testing.T) {
	f := newTrainingFixture(t)
	seasons := trainingSeasonStub{}
	slots, resources, teams, avail, users := &trainingSlotStub{}, trainingResourceStub{}, newTeamStub(), &trainingAvailabilityStub{}, usersWith()
	for name, test := range map[string]struct {
		build func() (*TrainingPlanService, error)
		want  error
	}{
		"repository": {func() (*TrainingPlanService, error) {
			return NewTrainingPlanService(nil, seasons, slots, resources, teams, avail, users)
		}, ErrNilTrainingPlanRepository},
		"seasons": {func() (*TrainingPlanService, error) {
			return NewTrainingPlanService(f.repo, nil, slots, resources, teams, avail, users)
		}, ErrNilTrainingPlanSource},
		"availability": {func() (*TrainingPlanService, error) {
			return NewTrainingPlanService(f.repo, seasons, slots, resources, teams, nil, users)
		}, ErrNilTrainingPlanSource},
		"users": {func() (*TrainingPlanService, error) {
			return NewTrainingPlanService(f.repo, seasons, slots, resources, teams, avail, nil)
		}, ErrNilUserRepository},
	} {
		if _, err := test.build(); !errors.Is(err, test.want) {
			t.Errorf("%s: error = %v, want %v", name, err, test.want)
		}
	}
}
