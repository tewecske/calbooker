package service

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
)

// coachBoardStub is the season stub plus ReplaceSeasonCoaches over its
// assignment list.
type coachBoardStub struct {
	*seasonRepositoryStub
	replaceErr error
	replaced   int
}

func (r *coachBoardStub) ReplaceSeasonCoaches(_ context.Context, groupID, seasonID, _ int64, edit func([]CoachAssignment) ([]CoachAssignment, error)) (CoachAssignmentChanges, error) {
	if r.replaceErr != nil {
		return CoachAssignmentChanges{}, r.replaceErr
	}
	if _, ok := r.seasons[seasonID]; !ok {
		return CoachAssignmentChanges{}, ErrSeasonNotFound
	}
	var current []CoachAssignment
	for _, coach := range r.coaches {
		if coach.GroupID == groupID && coach.SeasonID == seasonID {
			current = append(current, CoachAssignment{SeasonID: seasonID, TeamID: coach.TeamID, CoachUserID: coach.CoachUserID, Head: coach.IsHead})
		}
	}
	desired, err := edit(current)
	if err != nil {
		return CoachAssignmentChanges{}, err
	}
	diff := DiffCoachAssignments(current, desired)
	r.coaches = slices.DeleteFunc(r.coaches, func(coach SeasonCoach) bool { return coach.SeasonID == seasonID })
	for _, assignment := range desired {
		r.coaches = append(r.coaches, SeasonCoach{GroupID: groupID, SeasonID: seasonID, TeamID: assignment.TeamID, CoachUserID: assignment.CoachUserID, IsHead: assignment.Head})
	}
	r.replaced++
	return diff.Changes, nil
}

// Board fixture: group 5, seasons 2025/26 (ID 3, previous) and 2026/27
// (ID 1, parts 11 and 12). Teams: Lions (born 2016-2017, ages 9-10 in
// 2026), Tigers (2014-2015, 11-12), Bears (2019, 7), archived Owls
// (2010). Coaches: 7 Anna (6-10, availability), 8 Bela (9-14, no
// availability), 10 Cili (no qualification, availability). Player 9.
func boardSeason2526() Season {
	return Season{ID: 3, GroupID: 5, Name: "2025/26", StartsOn: day("2025-08-01"), EndsOn: day("2026-06-30"),
		Parts: []SeasonPart{{ID: 31, GroupID: 5, SeasonID: 3, Position: 1, Name: "Whole", StartsOn: day("2025-08-01"), EndsOn: day("2026-06-30")}}}
}

var (
	boardLions  = Team{ID: 1, GroupID: 5, Name: "Lions", BirthYearFrom: 2016, BirthYearTo: 2017}
	boardTigers = Team{ID: 2, GroupID: 5, Name: "Tigers", BirthYearFrom: 2014, BirthYearTo: 2015}
	boardBears  = Team{ID: 4, GroupID: 5, Name: "Bears", BirthYearFrom: 2019, BirthYearTo: 2019}
	boardOwls   = Team{ID: 6, GroupID: 5, Name: "Owls", BirthYearFrom: 2010, BirthYearTo: 2010, ArchivedAt: int64Ptr(1)}
)

type boardFixture struct {
	service *CoachBoardService
	seasons *coachBoardStub
	coaches *coachRepositoryStub
	auditor *auditRecorderStub
}

func newBoardFixture(t *testing.T) boardFixture {
	t.Helper()
	seasons := &coachBoardStub{seasonRepositoryStub: newSeasonStub(season2627(), boardSeason2526())}
	teams := newTeamStub(boardBears, boardLions, boardTigers, boardOwls)
	memberships := newMembershipStub()
	anna, bela, cili := "Anna", "Bela", "Cili"
	memberships.members = []GroupMember{
		{UserID: 7, Roles: []string{GroupRoleAdmin, GroupRoleCoach}, DisplayName: &anna},
		{UserID: 8, Roles: []string{GroupRoleCoach}, DisplayName: &bela},
		{UserID: 9, Roles: []string{GroupRolePlayer}},
		{UserID: 10, Roles: []string{GroupRoleCoach}, DisplayName: &cili},
	}
	coaches := newCoachStub([]int64{7, 8, 10}, []int64{11, 12, 31})
	coaches.qualifications[7] = CoachQualification{GroupID: 5, CoachUserID: 7, MinAge: 6, MaxAge: 10}
	coaches.qualifications[8] = CoachQualification{GroupID: 5, CoachUserID: 8, MinAge: 9, MaxAge: 14}
	coaches.windows = []AvailabilityWindow{
		{ID: 1, GroupID: 5, SeasonPartID: 11, CoachUserID: 7, Week: SeasonWeekAll, Weekday: 1, StartMinute: 960, EndMinute: 1080},
		{ID: 2, GroupID: 5, SeasonPartID: 12, CoachUserID: 10, Week: SeasonWeekA, Weekday: 2, StartMinute: 960, EndMinute: 1080},
		// Part 11 does not alternate, so this A window does not count.
		{ID: 3, GroupID: 5, SeasonPartID: 11, CoachUserID: 8, Week: SeasonWeekA, Weekday: 3, StartMinute: 960, EndMinute: 1080},
	}
	service, err := NewCoachBoardService(seasons, teams, coaches, memberships, usersWith(User{ID: 7, Email: strPtr("staff@example.test")}))
	if err != nil {
		t.Fatalf("NewCoachBoardService() error = %v", err)
	}
	auditor := &auditRecorderStub{}
	service.SetAuditor(auditor)
	service.now = func() time.Time { return time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC) }
	return boardFixture{service: service, seasons: seasons, coaches: coaches, auditor: auditor}
}

func (f boardFixture) assign(seasonID, teamID, coachID int64, head bool) {
	f.seasons.coaches = append(f.seasons.coaches, SeasonCoach{GroupID: 5, SeasonID: seasonID, TeamID: teamID, CoachUserID: coachID, IsHead: head})
}

func candidate(id int64, name string, minAge, maxAge int, available bool) CoachCandidate {
	coach := CoachCandidate{UserID: id, DisplayName: &name, HasAvailability: available}
	if minAge >= 0 {
		coach.Qualification = &CoachQualification{CoachUserID: id, MinAge: minAge, MaxAge: maxAge}
	}
	return coach
}

func TestNewCoachBoardServiceRejectsNilDependencies(t *testing.T) {
	seasons := &coachBoardStub{seasonRepositoryStub: newSeasonStub()}
	teams, coaches, members, users := newTeamStub(), newCoachStub(nil, nil), newMembershipStub(), usersWith()
	for name, test := range map[string]struct {
		build func() (*CoachBoardService, error)
		want  error
	}{
		"seasons":     {func() (*CoachBoardService, error) { return NewCoachBoardService(nil, teams, coaches, members, users) }, ErrNilCoachBoardRepository},
		"teams":       {func() (*CoachBoardService, error) { return NewCoachBoardService(seasons, nil, coaches, members, users) }, ErrNilTeamRepository},
		"coaches":     {func() (*CoachBoardService, error) { return NewCoachBoardService(seasons, teams, nil, members, users) }, ErrNilCoachRepository},
		"memberships": {func() (*CoachBoardService, error) { return NewCoachBoardService(seasons, teams, coaches, nil, users) }, ErrNilMembershipRepository},
		"users":       {func() (*CoachBoardService, error) { return NewCoachBoardService(seasons, teams, coaches, members, nil) }, ErrNilUserRepository},
	} {
		if _, err := test.build(); !errors.Is(err, test.want) {
			t.Errorf("%s: error = %v, want %v", name, err, test.want)
		}
	}
}

func TestSuggestCoachesPrefersPreviousSeasonCoach(t *testing.T) {
	season := season2627()
	// Bela coached Lions last season (as head) and Cili too; both stay
	// coaches. Anna is better qualified but the previous coaches win.
	suggested := SuggestCoaches(CoachSuggestionInput{
		Season:   season,
		Teams:    []Team{boardLions},
		Coaches:  []CoachCandidate{candidate(7, "Anna", 6, 10, true), candidate(8, "Bela", 11, 14, false), candidate(10, "Cili", -1, 0, true)},
		Previous: []CoachAssignment{{SeasonID: 3, TeamID: 1, CoachUserID: 8, Head: true}, {SeasonID: 3, TeamID: 1, CoachUserID: 10}, {SeasonID: 3, TeamID: 1, CoachUserID: 99}},
	})
	if len(suggested) != 2 || suggested[0].Assignment.CoachUserID != 8 || !suggested[0].Assignment.Head || suggested[1].Assignment.CoachUserID != 10 || suggested[1].Assignment.Head {
		t.Fatalf("suggested = %+v; want Bela (head) then Cili, and no former coach 99", suggested)
	}
	for _, s := range suggested {
		if s.Suggestion.Reason != CoachSuggestionPreviousSeason || s.Assignment.SeasonID != season.ID {
			t.Errorf("suggestion = %+v; want previous-season reason in season 1", s)
		}
	}
}

func TestSuggestCoachesByQualificationBalancesLoad(t *testing.T) {
	season := season2627()
	// Three teams aged 9-10; Anna and Dora both qualified and available.
	teams := []Team{
		{ID: 1, Name: "A", BirthYearFrom: 2016, BirthYearTo: 2017},
		{ID: 2, Name: "B", BirthYearFrom: 2016, BirthYearTo: 2017},
		{ID: 3, Name: "C", BirthYearFrom: 2016, BirthYearTo: 2017},
	}
	coaches := []CoachCandidate{candidate(12, "dora", 8, 12, true), candidate(7, "Anna", 6, 10, true)}
	suggested := SuggestCoaches(CoachSuggestionInput{Season: season, Teams: teams, Coaches: coaches})
	var picks []int64
	for _, s := range suggested {
		picks = append(picks, s.Assignment.CoachUserID)
		if s.Suggestion.Reason != CoachSuggestionQualification || s.Assignment.Head {
			t.Errorf("suggestion = %+v; want qualification reason, not head", s)
		}
	}
	// Ties go by name (Anna before dora, case-insensitive), then load.
	if !slices.Equal(picks, []int64{7, 12, 7}) {
		t.Fatalf("picks = %v, want Anna, Dora, Anna", picks)
	}
	if suggested[0].Suggestion.Load != 0 || suggested[2].Suggestion.Load != 1 || suggested[0].Suggestion.Qualification != (AgeRange{Min: 6, Max: 10}) {
		t.Fatalf("explanations = %+v", suggested)
	}
	// The same input gives the same answer.
	again := SuggestCoaches(CoachSuggestionInput{Season: season, Teams: teams, Coaches: slices.Clone(coaches)})
	if !slices.EqualFunc(suggested, again, func(a, b SuggestedAssignment) bool { return a == b }) {
		t.Fatalf("suggestion not deterministic: %+v vs %+v", suggested, again)
	}
}

func TestSuggestCoachesTieBreaksAndAvailability(t *testing.T) {
	season := season2627()
	team := []Team{{ID: 1, Name: "A", BirthYearFrom: 2016, BirthYearTo: 2017}}
	// Same name: the lower user ID wins.
	twins := SuggestCoaches(CoachSuggestionInput{Season: season, Teams: team, Coaches: []CoachCandidate{candidate(9, "Sam", 6, 12, true), candidate(4, "Sam", 6, 12, true)}})
	if len(twins) != 1 || twins[0].Assignment.CoachUserID != 4 {
		t.Fatalf("twins = %+v, want user 4", twins)
	}
	// A coach with availability beats one without, even with more teams.
	busy := SuggestCoaches(CoachSuggestionInput{
		Season: season, Teams: team,
		Coaches: []CoachCandidate{candidate(1, "Aaron", 6, 12, false), candidate(2, "Zoe", 6, 12, true)},
		Current: []CoachAssignment{{TeamID: 50, CoachUserID: 2}, {TeamID: 51, CoachUserID: 2}},
	})
	if len(busy) != 1 || busy[0].Assignment.CoachUserID != 2 || busy[0].Suggestion.Load != 2 {
		t.Fatalf("busy = %+v, want Zoe with load 2", busy)
	}
}

func TestSuggestCoachesKeepsDraftAndSkipsUnqualified(t *testing.T) {
	season := season2627()
	suggested := SuggestCoaches(CoachSuggestionInput{
		Season: season,
		Teams:  []Team{boardLions, boardTigers, boardBears},
		// Anna 6-10 covers Lions (9-10) and Bears (7); nobody covers Tigers (11-12).
		Coaches: []CoachCandidate{candidate(7, "Anna", 6, 10, true), candidate(10, "Cili", -1, 0, true)},
		Current: []CoachAssignment{{TeamID: boardLions.ID, CoachUserID: 10}},
	})
	if len(suggested) != 1 || suggested[0].Assignment.TeamID != boardBears.ID || suggested[0].Assignment.CoachUserID != 7 {
		t.Fatalf("suggested = %+v; want only Anna on Bears", suggested)
	}
}

func TestCoachAssignmentWarnings(t *testing.T) {
	season := season2627()
	tests := []struct {
		name  string
		coach CoachCandidate
		team  Team
		want  []string
	}{
		{"covered and available", candidate(7, "Anna", 6, 10, true), boardLions, nil},
		{"mismatch", candidate(7, "Anna", 6, 10, true), boardTigers, []string{CoachWarningQualificationMismatch}},
		{"no qualification, no availability", candidate(10, "Cili", -1, 0, false), boardLions, []string{CoachWarningNoQualification, CoachWarningNoAvailability}},
		{"no availability", candidate(8, "Bela", 9, 14, false), boardTigers, []string{CoachWarningNoAvailability}},
	}
	for _, test := range tests {
		warnings := CoachAssignmentWarnings(test.coach, test.team, season)
		var kinds []string
		for _, warning := range warnings {
			kinds = append(kinds, warning.Kind)
			if warning.TeamID != test.team.ID || warning.CoachUserID != test.coach.UserID {
				t.Errorf("%s: warning = %+v", test.name, warning)
			}
		}
		if !slices.Equal(kinds, test.want) {
			t.Errorf("%s: kinds = %v, want %v", test.name, kinds, test.want)
		}
	}
	mismatch := CoachAssignmentWarnings(candidate(7, "Anna", 6, 10, true), boardTigers, season)[0]
	if mismatch.TeamAges != (AgeRange{Min: 11, Max: 12}) || mismatch.Qualification != (AgeRange{Min: 6, Max: 10}) {
		t.Fatalf("mismatch = %+v", mismatch)
	}
}

func TestPreviousSeason(t *testing.T) {
	older := Season{ID: 4, Name: "2024/25", StartsOn: day("2024-08-01"), EndsOn: day("2025-06-30")}
	seasons := []Season{season2728(), season2627(), boardSeason2526(), older}
	if previous, ok := PreviousSeason(seasons, season2627()); !ok || previous.ID != 3 {
		t.Fatalf("PreviousSeason(2026/27) = %+v, %v; want 2025/26", previous, ok)
	}
	if _, ok := PreviousSeason(seasons, older); ok {
		t.Fatal("the first season has a previous one")
	}
}

func TestCoachAssignmentRevisionAndDiff(t *testing.T) {
	a := []CoachAssignment{{TeamID: 1, CoachUserID: 7, Head: true}, {TeamID: 2, CoachUserID: 8}}
	b := []CoachAssignment{{TeamID: 2, CoachUserID: 8}, {TeamID: 1, CoachUserID: 7, Head: true}}
	if CoachAssignmentRevision(a) != CoachAssignmentRevision(b) {
		t.Fatal("revision depends on order")
	}
	if CoachAssignmentRevision(a) == CoachAssignmentRevision([]CoachAssignment{{TeamID: 1, CoachUserID: 7}, {TeamID: 2, CoachUserID: 8}}) {
		t.Fatal("revision ignores the head mark")
	}
	if len(CoachAssignmentRevision(nil)) != 32 {
		t.Fatalf("revision = %q", CoachAssignmentRevision(nil))
	}
	desired := []CoachAssignment{{TeamID: 1, CoachUserID: 7}, {TeamID: 1, CoachUserID: 8, Head: true}, {TeamID: 3, CoachUserID: 7}}
	diff := DiffCoachAssignments(a, desired)
	if diff.Changes != (CoachAssignmentChanges{Added: 2, Removed: 1, HeadChanged: 1}) || len(diff.Remove) != 1 || diff.Remove[0].TeamID != 2 ||
		len(diff.Unhead) != 1 || diff.Unhead[0].CoachUserID != 7 || len(diff.Upsert) != 2 {
		t.Fatalf("diff = %+v", diff)
	}
	if same := DiffCoachAssignments(a, b); same.Changes.Total() != 0 {
		t.Fatalf("diff of equal sets = %+v", same)
	}
}

func TestCoachBoardAccess(t *testing.T) {
	f := newBoardFixture(t)
	ctx := context.Background()
	request := CoachBoardRequest{SeasonID: 1}
	for name, test := range map[string]struct {
		access *GroupAccess
		want   error
	}{
		"outsider":     {accessFor(GroupAccessGrants{}), ErrGroupNotFound},
		"player":       {accessFor(memberWith(GroupRolePlayer)), ErrGroupForbidden},
		"parent":       {accessFor(memberWith(GroupRoleParent)), ErrGroupForbidden},
		"other season": {accessFor(memberWith(GroupRoleAdmin)), nil},
	} {
		req := request
		if name == "other season" {
			req.SeasonID = 77
			test.want = ErrSeasonNotFound
		}
		if _, err := f.service.Board(ctx, test.access, req); !errors.Is(err, test.want) {
			t.Errorf("%s: error = %v, want %v", name, err, test.want)
		}
	}
	for name, access := range map[string]*GroupAccess{"manager": accessFor(memberWith(GroupRoleManager)), "site admin": siteAdminActor().Access} {
		board, err := f.service.Board(ctx, access, request)
		if err != nil || !board.CanEdit || len(board.Coaches) != 3 {
			t.Errorf("%s: board = %+v, %v; want an editable board with three coaches", name, board, err)
		}
	}
}

func TestCoachBoardReadOnlyForCoaches(t *testing.T) {
	f := newBoardFixture(t)
	f.assign(1, boardTigers.ID, 8, true)
	f.seasons.coaches[0].TeamName = "Tigers"
	board, err := f.service.Board(context.Background(), accessFor(memberWith(GroupRoleCoach)), CoachBoardRequest{
		SeasonID: 1, Suggest: CoachSuggestReplace, Draft: &CoachBoardDraft{Assignments: []CoachAssignment{{TeamID: 1, CoachUserID: 7}}},
	})
	if err != nil {
		t.Fatalf("Board(coach) error = %v", err)
	}
	if board.CanEdit || board.Changed || len(board.Coaches) != 0 || len(board.Warnings) != 0 || board.Suggested != 0 {
		t.Fatalf("coach board = %+v; want read only without pool, warnings, or drafts", board)
	}
	if len(board.Teams) != 1 || board.Teams[0].Team.Name != "Tigers" || len(board.Teams[0].Assignments) != 1 || !board.Teams[0].Assignments[0].Head {
		t.Fatalf("coach board teams = %+v; want only the saved Tigers assignment", board.Teams)
	}
}

func TestCoachBoardStaffView(t *testing.T) {
	f := newBoardFixture(t)
	f.assign(1, boardLions.ID, 7, true)
	f.assign(1, boardTigers.ID, 10, false)
	board, err := f.service.Board(context.Background(), accessFor(memberWith(GroupRoleAdmin)), CoachBoardRequest{SeasonID: 1})
	if err != nil {
		t.Fatalf("Board() error = %v", err)
	}
	if board.Changed || board.Stale() || !board.HasPrevious || board.PreviousSeason.ID != 3 {
		t.Fatalf("board = %+v", board)
	}
	// Active teams in repository order; archived Owls without coaches hidden.
	var names []string
	for _, team := range board.Teams {
		names = append(names, team.Team.Name)
	}
	if !slices.Equal(names, []string{"Bears", "Lions", "Tigers"}) {
		t.Fatalf("teams = %v", names)
	}
	lions := board.Teams[1]
	if lions.Ages != (AgeRange{Min: 9, Max: 10}) || len(lions.Assignments) != 1 || !lions.Assignments[0].Saved || len(lions.Assignments[0].Warnings) != 0 {
		t.Fatalf("lions = %+v", lions)
	}
	tigers := board.Teams[2].Assignments[0]
	if len(tigers.Warnings) != 1 || tigers.Warnings[0].Kind != CoachWarningNoQualification {
		t.Fatalf("tigers warnings = %+v", tigers.Warnings)
	}
	// Bela's only window is an A window in a part without A/B weeks.
	for _, coach := range board.Coaches {
		wantAvailable := coach.UserID != 8
		if coach.HasAvailability != wantAvailable {
			t.Errorf("coach %d availability = %v", coach.UserID, coach.HasAvailability)
		}
	}
	if len(board.Warnings) != 1 {
		t.Fatalf("summary warnings = %+v", board.Warnings)
	}
}

func TestCoachBoardDraftAndSuggest(t *testing.T) {
	f := newBoardFixture(t)
	ctx := context.Background()
	staff := accessFor(memberWith(GroupRoleAdmin))
	// Last season: Bela coached Tigers as head.
	f.assign(3, boardTigers.ID, 8, true)
	// Saved now: Cili on Lions.
	f.assign(1, boardLions.ID, 10, false)
	saved, _ := f.service.Board(ctx, staff, CoachBoardRequest{SeasonID: 1})

	// The draft drops unknown teams and non-coaches, keeps one head per team.
	draft := &CoachBoardDraft{Revision: saved.Revision, Assignments: []CoachAssignment{
		{TeamID: boardLions.ID, CoachUserID: 10, Head: true},
		{TeamID: boardLions.ID, CoachUserID: 7, Head: true},
		{TeamID: 99, CoachUserID: 7},
		{TeamID: boardBears.ID, CoachUserID: 9},
	}}
	board, err := f.service.Board(ctx, staff, CoachBoardRequest{SeasonID: 1, Draft: draft})
	if err != nil {
		t.Fatalf("Board(draft) error = %v", err)
	}
	lions := board.Teams[1].Assignments
	if !board.Changed || len(lions) != 2 || !lions[0].Head || lions[1].Head || lions[0].Saved || len(board.Teams[0].Assignments) != 0 {
		t.Fatalf("draft board = %+v", board.Teams)
	}

	// Fill: Tigers gets last season's Bela (head); Bears gets Anna (6-10).
	board, err = f.service.Board(ctx, staff, CoachBoardRequest{SeasonID: 1, Draft: draft, Suggest: CoachSuggestFill})
	if err != nil {
		t.Fatalf("Board(fill) error = %v", err)
	}
	if board.Suggested != 2 {
		t.Fatalf("suggested = %d, want 2", board.Suggested)
	}
	bears, tigers := board.Teams[0].Assignments, board.Teams[2].Assignments
	if len(bears) != 1 || bears[0].CoachUserID != 7 || bears[0].Suggestion == nil || bears[0].Suggestion.Reason != CoachSuggestionQualification || bears[0].Suggestion.Load != 1 {
		t.Fatalf("bears = %+v", bears)
	}
	if len(tigers) != 1 || tigers[0].CoachUserID != 8 || !tigers[0].Head || tigers[0].Suggestion.Reason != CoachSuggestionPreviousSeason {
		t.Fatalf("tigers = %+v", tigers)
	}
	if len(board.Teams[1].Assignments) != 2 {
		t.Fatal("suggest removed a draft assignment")
	}

	// Explanations ride along with the draft while they still hold.
	carried := &CoachBoardDraft{Revision: saved.Revision, Assignments: []CoachAssignment{{TeamID: boardBears.ID, CoachUserID: 7}, {TeamID: boardTigers.ID, CoachUserID: 7}},
		Suggestions: []SuggestedAssignment{
			{Assignment: CoachAssignment{TeamID: boardBears.ID, CoachUserID: 7}, Suggestion: CoachSuggestion{Reason: CoachSuggestionQualification, Qualification: AgeRange{Min: 6, Max: 10}, Load: 1}},
			{Assignment: CoachAssignment{TeamID: boardTigers.ID, CoachUserID: 7}, Suggestion: CoachSuggestion{Reason: CoachSuggestionQualification, Qualification: AgeRange{Min: 6, Max: 10}}},
		}}
	board, _ = f.service.Board(ctx, staff, CoachBoardRequest{SeasonID: 1, Draft: carried})
	if board.Teams[0].Assignments[0].Suggestion == nil || board.Teams[2].Assignments[0].Suggestion != nil {
		t.Fatalf("carried suggestions = %+v / %+v; want only the one that still holds", board.Teams[0].Assignments, board.Teams[2].Assignments)
	}

	// Replace starts from scratch: Cili's saved Lions assignment goes, Anna
	// (fewest teams, available) takes Lions, Bears goes to Anna too.
	board, err = f.service.Board(ctx, staff, CoachBoardRequest{SeasonID: 1, Draft: draft, Suggest: CoachSuggestReplace})
	if err != nil {
		t.Fatalf("Board(replace) error = %v", err)
	}
	if got := board.Teams[1].Assignments; len(got) != 1 || got[0].CoachUserID != 7 {
		t.Fatalf("replace lions = %+v", got)
	}

	// A draft started from an older revision is stale.
	board, _ = f.service.Board(ctx, staff, CoachBoardRequest{SeasonID: 1, Draft: &CoachBoardDraft{Revision: "old"}})
	if !board.Stale() {
		t.Fatal("old revision not stale")
	}
	if _, err := f.service.Board(ctx, staff, CoachBoardRequest{SeasonID: 1, Draft: &CoachBoardDraft{Assignments: make([]CoachAssignment, MaxCoachBoardAssignments+1)}}); !errors.Is(err, ErrCoachBoardTooLarge) {
		t.Fatalf("oversized draft error = %v", err)
	}
}

func TestCoachBoardSave(t *testing.T) {
	f := newBoardFixture(t)
	ctx := context.Background()
	f.assign(1, boardLions.ID, 10, true)
	revision := CoachAssignmentRevision([]CoachAssignment{{TeamID: boardLions.ID, CoachUserID: 10, Head: true}})
	desired := []CoachAssignment{
		{TeamID: boardLions.ID, CoachUserID: 10},
		{TeamID: boardLions.ID, CoachUserID: 7, Head: true},
		{TeamID: boardOwls.ID, CoachUserID: 8},
		{TeamID: boardOwls.ID, CoachUserID: 8},
	}
	changes, err := f.service.Save(ctx, adminActor(), 1, revision, desired)
	if err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	if changes != (CoachAssignmentChanges{Added: 2, HeadChanged: 1}) || len(f.seasons.coaches) != 3 {
		t.Fatalf("changes = %+v, stored = %+v", changes, f.seasons.coaches)
	}
	if len(f.auditor.records) != 1 || f.auditor.records[0].Action != AuditActionSeasonCoachesSaved ||
		!strings.Contains(f.auditor.records[0].Detail, "season 1: 2026/27; added 2; removed 0; head changed 1; assignments 3") {
		t.Fatalf("audit = %+v", f.auditor.records)
	}
	// Saving the same set again changes nothing and records nothing.
	now := CoachAssignmentRevision(normalizeCoachAssignments(1, desired))
	if changes, err := f.service.Save(ctx, adminActor(), 1, now, desired); err != nil || changes.Total() != 0 || len(f.auditor.records) != 1 {
		t.Fatalf("repeat save = %+v, %v, audits %d", changes, err, len(f.auditor.records))
	}
	// The old revision is stale now.
	if _, err := f.service.Save(ctx, adminActor(), 1, revision, nil); !errors.Is(err, ErrOptimisticLockConflict) {
		t.Fatalf("stale save error = %v", err)
	}
	// A site administrator's save is marked.
	if _, err := f.service.Save(ctx, siteAdminActor(), 1, now, nil); err != nil {
		t.Fatalf("site admin save error = %v", err)
	}
	if detail := f.auditor.records[len(f.auditor.records)-1].Detail; !strings.HasSuffix(detail, "(site administrator)") || !strings.Contains(detail, "removed 3") {
		t.Fatalf("site admin audit = %q", detail)
	}
}

func TestCoachBoardSaveFailures(t *testing.T) {
	f := newBoardFixture(t)
	ctx := context.Background()
	empty := CoachAssignmentRevision(nil)
	for name, test := range map[string]struct {
		actor       GroupActor
		seasonID    int64
		assignments []CoachAssignment
		want        error
	}{
		"outsider":       {actorWith(GroupAccessGrants{}), 1, nil, ErrGroupNotFound},
		"coach":          {actorWith(memberWith(GroupRoleCoach)), 1, nil, ErrGroupForbidden},
		"player":         {actorWith(memberWith(GroupRolePlayer)), 1, nil, ErrGroupForbidden},
		"unknown season": {adminActor(), 77, nil, ErrSeasonNotFound},
		"unknown team":   {adminActor(), 1, []CoachAssignment{{TeamID: 99, CoachUserID: 7}}, ErrTeamNotFound},
		"player coach":   {adminActor(), 1, []CoachAssignment{{TeamID: 1, CoachUserID: 9}}, ErrSeasonCoachRoleMissing},
		"two heads":      {adminActor(), 1, []CoachAssignment{{TeamID: 1, CoachUserID: 7, Head: true}, {TeamID: 1, CoachUserID: 8, Head: true}}, ErrCoachBoardTwoHeads},
		"too many":       {adminActor(), 1, make([]CoachAssignment, MaxCoachBoardAssignments+1), ErrCoachBoardTooLarge},
	} {
		if _, err := f.service.Save(ctx, test.actor, test.seasonID, empty, test.assignments); !errors.Is(err, test.want) {
			t.Errorf("%s: error = %v, want %v", name, err, test.want)
		}
	}
	if f.seasons.replaced != 0 || len(f.auditor.records) != 0 {
		t.Fatalf("failed saves wrote %d times, audited %d", f.seasons.replaced, len(f.auditor.records))
	}
	// A season deleted during the save is not-found, never a conflict.
	for _, storeErr := range []error{ErrSeasonNotFound, ErrRecordNotFound} {
		f.seasons.replaceErr = storeErr
		if _, err := f.service.Save(ctx, adminActor(), 1, empty, nil); !errors.Is(err, ErrSeasonNotFound) {
			t.Errorf("vanished season (%v) error = %v", storeErr, err)
		}
	}
	f.seasons.replaceErr = errors.New("boom")
	if _, err := f.service.Save(ctx, adminActor(), 1, empty, nil); err == nil || errors.Is(err, ErrSeasonNotFound) {
		t.Fatalf("store failure error = %v", err)
	}
}
