package service

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
)

// AuditActionSeasonCoachesSaved records a save of the coach assignment
// board: the season and how many assignments were added, removed, or had
// their head coach mark changed.
const AuditActionSeasonCoachesSaved = "group.season.coaches_saved"

// MaxCoachBoardAssignments bounds the assignment set one board save or
// draft may carry.
const MaxCoachBoardAssignments = 1000

var (
	// ErrCoachBoardTwoHeads identifies a board save that marks more than one
	// head coach for a team.
	ErrCoachBoardTwoHeads = errors.New("service: more than one head coach for a team")
	// ErrCoachBoardTooLarge identifies a board save or draft with more than
	// MaxCoachBoardAssignments assignments.
	ErrCoachBoardTooLarge = errors.New("service: too many coach assignments")
	// ErrNilCoachBoardRepository identifies missing season coach persistence.
	ErrNilCoachBoardRepository = errors.New("service: nil coach board repository")
)

// Coach board warning kinds. Warnings never block a save.
const (
	// CoachWarningNoQualification: the coach has no qualification set.
	CoachWarningNoQualification = "no_qualification"
	// CoachWarningQualificationMismatch: the coach's qualification does not
	// cover the team's whole age range this season.
	CoachWarningQualificationMismatch = "qualification_mismatch"
	// CoachWarningNoAvailability: the coach has no availability window that
	// counts in any part of the season.
	CoachWarningNoAvailability = "no_availability"
)

// Coach suggestion reasons.
const (
	// CoachSuggestionPreviousSeason: the coach coached the same team in the
	// previous season.
	CoachSuggestionPreviousSeason = "previous_season"
	// CoachSuggestionQualification: the coach's qualification covers the
	// team's ages this season; Load is how many teams they had then.
	CoachSuggestionQualification = "qualification"
)

// Coach board suggestion modes.
const (
	// CoachSuggestNone shows the draft as it is.
	CoachSuggestNone = ""
	// CoachSuggestFill keeps the draft and suggests coaches for active
	// teams that have none.
	CoachSuggestFill = "fill"
	// CoachSuggestReplace drops the draft and starts from the suggestion.
	CoachSuggestReplace = "replace"
)

// CoachAssignmentChanges counts what a board save changed.
type CoachAssignmentChanges struct {
	Added       int
	Removed     int
	HeadChanged int
}

// Total is the number of changed assignments.
func (c CoachAssignmentChanges) Total() int {
	return c.Added + c.Removed + c.HeadChanged
}

// CoachAssignmentDiff is what turns one assignment set of a season into
// another: Remove are deleted, Unhead lose the head mark, Upsert are
// inserted or get the head mark. Applying them in this order never holds two
// head coaches of a team at once.
type CoachAssignmentDiff struct {
	Remove  []CoachAssignment
	Unhead  []CoachAssignment
	Upsert  []CoachAssignment
	Changes CoachAssignmentChanges
}

// DiffCoachAssignments compares two assignment sets of one season by team
// and coach.
func DiffCoachAssignments(current, desired []CoachAssignment) CoachAssignmentDiff {
	type key struct{ team, coach int64 }
	before := make(map[key]bool, len(current))
	for _, assignment := range current {
		before[key{assignment.TeamID, assignment.CoachUserID}] = assignment.Head
	}
	after := make(map[key]bool, len(desired))
	for _, assignment := range desired {
		after[key{assignment.TeamID, assignment.CoachUserID}] = assignment.Head
	}
	var diff CoachAssignmentDiff
	for _, assignment := range current {
		head, kept := after[key{assignment.TeamID, assignment.CoachUserID}]
		switch {
		case !kept:
			diff.Remove = append(diff.Remove, assignment)
			diff.Changes.Removed++
		case assignment.Head && !head:
			changed := assignment
			changed.Head = false
			diff.Unhead = append(diff.Unhead, changed)
			diff.Changes.HeadChanged++
		}
	}
	for _, assignment := range desired {
		head, existed := before[key{assignment.TeamID, assignment.CoachUserID}]
		switch {
		case !existed:
			diff.Upsert = append(diff.Upsert, assignment)
			diff.Changes.Added++
		case assignment.Head && !head:
			diff.Upsert = append(diff.Upsert, assignment)
			diff.Changes.HeadChanged++
		}
	}
	return diff
}

// CoachAssignmentRevision is the revision token of a season's coach
// assignments: a hash of the sorted (team, coach, head) set. The board
// sends it back on save; a different current set is a stale save. It names
// no account and is not secret.
func CoachAssignmentRevision(assignments []CoachAssignment) string {
	sorted := slices.Clone(assignments)
	sortCoachAssignments(sorted)
	var builder strings.Builder
	for _, assignment := range sorted {
		builder.WriteString(strconv.FormatInt(assignment.TeamID, 10))
		builder.WriteByte(':')
		builder.WriteString(strconv.FormatInt(assignment.CoachUserID, 10))
		if assignment.Head {
			builder.WriteString(":h")
		}
		builder.WriteByte('\n')
	}
	sum := sha256.Sum256([]byte(builder.String()))
	return hex.EncodeToString(sum[:16])
}

func sortCoachAssignments(assignments []CoachAssignment) {
	slices.SortFunc(assignments, func(a, b CoachAssignment) int {
		return cmp.Or(cmp.Compare(a.TeamID, b.TeamID), cmp.Compare(a.CoachUserID, b.CoachUserID))
	})
}

// normalizeCoachAssignments sets the season, drops duplicates (a duplicate
// marked head keeps the mark), and sorts by team and coach.
func normalizeCoachAssignments(seasonID int64, assignments []CoachAssignment) []CoachAssignment {
	normalized := make([]CoachAssignment, 0, len(assignments))
	for _, assignment := range assignments {
		assignment.SeasonID = seasonID
		index := slices.IndexFunc(normalized, func(a CoachAssignment) bool {
			return a.TeamID == assignment.TeamID && a.CoachUserID == assignment.CoachUserID
		})
		if index >= 0 {
			normalized[index].Head = normalized[index].Head || assignment.Head
			continue
		}
		normalized = append(normalized, assignment)
	}
	sortCoachAssignments(normalized)
	return normalized
}

// CoachSuggestion explains why the board suggested an assignment.
type CoachSuggestion struct {
	Reason string
	// Qualification is the coach's qualification (CoachSuggestionQualification).
	Qualification AgeRange
	// Load is how many teams the coach had in the draft when suggested.
	Load int
}

// SuggestedAssignment is an assignment the suggestion added, with its
// explanation.
type SuggestedAssignment struct {
	Assignment CoachAssignment
	Suggestion CoachSuggestion
}

// CoachCandidate is a coach the suggestion may pick.
type CoachCandidate struct {
	UserID        int64
	DisplayName   *string
	Username      *string
	Qualification *CoachQualification
	// HasAvailability is true when the coach has availability that counts
	// in some part of the season.
	HasAvailability bool
}

// CoachSuggestionInput is what SuggestCoaches works from.
type CoachSuggestionInput struct {
	Season Season
	// Teams are the teams to fill, in board order. A team that already has
	// a coach in Current is left as it is.
	Teams []Team
	// Coaches are the group's coaches.
	Coaches []CoachCandidate
	// Previous are the assignments of the previous season.
	Previous []CoachAssignment
	// Current is the draft the suggestion adds to.
	Current []CoachAssignment
}

// SuggestCoaches proposes coaches for teams without one, never removing or
// changing what Current holds:
//
//  1. a team gets the coaches who coached it in the previous season (with
//     their head coach mark) when they are still coaches of the group;
//  2. a team still without a coach gets one coach whose qualification
//     covers the team's ages this season: coaches with availability in the
//     season first, then the fewest teams so far, then by name and user ID;
//  3. a team nobody qualified can take stays without a coach.
//
// Teams are filled in the given order, and every pick counts towards the
// coach's load for the next team, so the result is deterministic.
func SuggestCoaches(input CoachSuggestionInput) []SuggestedAssignment {
	coaches := slices.Clone(input.Coaches)
	slices.SortFunc(coaches, func(a, b CoachCandidate) int {
		return cmp.Or(
			strings.Compare(coachSortName(a.DisplayName, a.Username), coachSortName(b.DisplayName, b.Username)),
			cmp.Compare(a.UserID, b.UserID),
		)
	})
	isCoach := make(map[int64]bool, len(coaches))
	for _, coach := range coaches {
		isCoach[coach.UserID] = true
	}
	load := make(map[int64]int)
	staffed := make(map[int64]bool)
	for _, assignment := range input.Current {
		load[assignment.CoachUserID]++
		staffed[assignment.TeamID] = true
	}
	var suggested []SuggestedAssignment
	add := func(assignment CoachAssignment, suggestion CoachSuggestion) {
		assignment.SeasonID = input.Season.ID
		suggested = append(suggested, SuggestedAssignment{Assignment: assignment, Suggestion: suggestion})
		load[assignment.CoachUserID]++
		staffed[assignment.TeamID] = true
	}
	previous := normalizeCoachAssignments(input.Season.ID, input.Previous)
	for _, team := range input.Teams {
		if staffed[team.ID] {
			continue
		}
		heads := 0
		for _, assignment := range previous {
			if assignment.TeamID != team.ID || !isCoach[assignment.CoachUserID] {
				continue
			}
			if assignment.Head {
				heads++
			}
			assignment.Head = assignment.Head && heads == 1
			add(assignment, CoachSuggestion{Reason: CoachSuggestionPreviousSeason, Load: load[assignment.CoachUserID]})
		}
	}
	for _, team := range input.Teams {
		if staffed[team.ID] {
			continue
		}
		ages := TeamAgeRange(team, input.Season)
		best := -1
		for index, coach := range coaches {
			if coach.Qualification == nil || !coach.Qualification.Covers(ages) {
				continue
			}
			if best < 0 || betterCandidate(coach, coaches[best], load) {
				best = index
			}
		}
		if best < 0 {
			continue
		}
		coach := coaches[best]
		add(CoachAssignment{TeamID: team.ID, CoachUserID: coach.UserID}, CoachSuggestion{
			Reason:        CoachSuggestionQualification,
			Qualification: coach.Qualification.Range(),
			Load:          load[coach.UserID],
		})
	}
	return suggested
}

// betterCandidate reports whether a beats b, which comes earlier in name
// order: availability first, then fewer teams. Equal candidates keep b.
func betterCandidate(a, b CoachCandidate, load map[int64]int) bool {
	if a.HasAvailability != b.HasAvailability {
		return a.HasAvailability
	}
	return load[a.UserID] < load[b.UserID]
}

func coachSortName(displayName, username *string) string {
	for _, value := range []*string{displayName, username} {
		if value != nil && strings.TrimSpace(*value) != "" {
			return strings.ToLower(strings.TrimSpace(*value))
		}
	}
	return ""
}

// CoachBoardWarning is one warning about an assignment.
type CoachBoardWarning struct {
	Kind        string
	TeamID      int64
	CoachUserID int64
	// TeamAges and Qualification are set for qualification mismatches.
	TeamAges      AgeRange
	Qualification AgeRange
}

// CoachAssignmentWarnings returns the warnings of one assignment, in a fixed
// order: no qualification or a qualification mismatch, then no
// availability in the season.
func CoachAssignmentWarnings(coach CoachCandidate, team Team, season Season) []CoachBoardWarning {
	var warnings []CoachBoardWarning
	ages := TeamAgeRange(team, season)
	switch {
	case coach.Qualification == nil:
		warnings = append(warnings, CoachBoardWarning{Kind: CoachWarningNoQualification, TeamID: team.ID, CoachUserID: coach.UserID})
	case !coach.Qualification.Covers(ages):
		warnings = append(warnings, CoachBoardWarning{Kind: CoachWarningQualificationMismatch, TeamID: team.ID, CoachUserID: coach.UserID, TeamAges: ages, Qualification: coach.Qualification.Range()})
	}
	if !coach.HasAvailability {
		warnings = append(warnings, CoachBoardWarning{Kind: CoachWarningNoAvailability, TeamID: team.ID, CoachUserID: coach.UserID})
	}
	return warnings
}

// PreviousSeason returns the latest season that ends before season starts.
func PreviousSeason(seasons []Season, season Season) (Season, bool) {
	var (
		previous Season
		found    bool
	)
	for _, candidate := range seasons {
		if candidate.ID == season.ID || !candidate.EndsOn.Before(season.StartsOn) {
			continue
		}
		if !found || candidate.EndsOn.After(previous.EndsOn) {
			previous, found = candidate, true
		}
	}
	return previous, found
}

// CoachBoardDraft is the board state the page sends back: the assignment
// set the user is editing, the revision of the saved set it started from,
// and the explanations of the suggestions it holds.
type CoachBoardDraft struct {
	Revision    string
	Assignments []CoachAssignment
	Suggestions []SuggestedAssignment
}

// CoachBoardRequest selects what the board shows: the saved assignments
// (Draft nil), or a draft, optionally with a suggestion (Suggest is one of
// CoachSuggestNone, CoachSuggestFill, CoachSuggestReplace).
type CoachBoardRequest struct {
	SeasonID int64
	Draft    *CoachBoardDraft
	Suggest  string
}

// CoachBoard is the coach assignment board of one season.
type CoachBoard struct {
	Season         Season
	PreviousSeason Season
	HasPrevious    bool
	// CanEdit is true for accounts that manage seasons; coaches see the
	// saved assignments read only.
	CanEdit bool
	// Revision is the revision the shown draft started from; SavedRevision
	// is the current one. They differ when the assignments changed since the
	// board was loaded.
	Revision      string
	SavedRevision string
	// Changed is true when the shown assignments differ from the saved ones.
	Changed bool
	// Suggested is how many assignments this request's suggestion added.
	Suggested int
	Teams     []CoachBoardTeam
	// Coaches is the coach pool (staff only), by name.
	Coaches  []CoachBoardCoach
	Warnings []CoachBoardWarning
}

// CoachBoardTeam is one team column of the board.
type CoachBoardTeam struct {
	Team        Team
	Ages        AgeRange
	Assignments []CoachBoardAssignment
}

// CoachBoardAssignment is one coach on a team.
type CoachBoardAssignment struct {
	CoachUserID int64
	DisplayName *string
	Username    *string
	Head        bool
	// Saved is true when the assignment (with the same head mark) is saved.
	Saved      bool
	Suggestion *CoachSuggestion
	Warnings   []CoachBoardWarning
}

// CoachBoardCoach is one card of the coach pool.
type CoachBoardCoach struct {
	CoachCandidate
	// Teams is how many teams the coach has on the shown board.
	Teams int
}

// CoachBoardRepository persists a season's coach assignments for the board.
// SeasonRepository's store implements it.
type CoachBoardRepository interface {
	FindSeason(ctx context.Context, groupID, seasonID int64) (Season, error)
	ListSeasons(ctx context.Context, groupID int64) ([]Season, error)
	ListSeasonCoaches(ctx context.Context, groupID int64, filter SeasonCoachFilter) ([]SeasonCoach, error)
	// ReplaceSeasonCoaches locks the season, reads its assignments, and
	// replaces them with what edit returns in one transaction. It returns
	// ErrSeasonNotFound when the season is gone, an error from edit
	// unchanged (nothing written), ErrTeamNotFound and
	// ErrSeasonCoachRoleMissing from foreign keys.
	ReplaceSeasonCoaches(ctx context.Context, groupID, seasonID, createdAt int64, edit func([]CoachAssignment) ([]CoachAssignment, error)) (CoachAssignmentChanges, error)
}

// CoachBoardService owns the coach assignment board: the board view with
// suggestions and warnings, and saving the whole assignment set of a season.
type CoachBoardService struct {
	seasons     CoachBoardRepository
	teams       TeamRepository
	coaches     CoachRepository
	memberships GroupMembershipRepository
	users       UserRepository
	auditor     AuditRecorder
	now         func() time.Time
}

// NewCoachBoardService constructs coach board use cases with explicit
// dependencies.
func NewCoachBoardService(seasons CoachBoardRepository, teams TeamRepository, coaches CoachRepository, memberships GroupMembershipRepository, users UserRepository) (*CoachBoardService, error) {
	switch {
	case seasons == nil:
		return nil, ErrNilCoachBoardRepository
	case teams == nil:
		return nil, ErrNilTeamRepository
	case coaches == nil:
		return nil, ErrNilCoachRepository
	case memberships == nil:
		return nil, ErrNilMembershipRepository
	case users == nil:
		return nil, ErrNilUserRepository
	}
	return &CoachBoardService{seasons: seasons, teams: teams, coaches: coaches, memberships: memberships, users: users, now: time.Now}, nil
}

// SetAuditor attaches the audit trail. It is intended to be called once
// during startup.
func (s *CoachBoardService) SetAuditor(auditor AuditRecorder) {
	if s != nil {
		s.auditor = auditor
	}
}

// Board returns the coach board of a season. Staff (CanManageSeasons) get
// the draft they sent (or the saved assignments), optionally with a
// suggestion, the coach pool, and warnings. Coaches get the saved
// assignments read only: only the teams that have coaches, no pool, no
// qualifications or warnings. Players and parents are forbidden.
func (s *CoachBoardService) Board(ctx context.Context, access *GroupAccess, request CoachBoardRequest) (CoachBoard, error) {
	if err := s.validate(ctx); err != nil {
		return CoachBoard{}, err
	}
	if !access.CanView() {
		return CoachBoard{}, ErrGroupNotFound
	}
	if !access.CanViewSeasons() {
		return CoachBoard{}, ErrGroupForbidden
	}
	if request.SeasonID <= 0 {
		return CoachBoard{}, ErrSeasonNotFound
	}
	groupID := access.GroupID()
	season, err := s.seasons.FindSeason(ctx, groupID, request.SeasonID)
	if err != nil {
		return CoachBoard{}, err
	}
	saved, err := s.seasons.ListSeasonCoaches(ctx, groupID, SeasonCoachFilter{SeasonID: season.ID})
	if err != nil {
		return CoachBoard{}, fmt.Errorf("list season coaches: %w", err)
	}
	savedSet := assignmentsOf(saved)
	board := CoachBoard{Season: season, CanEdit: access.CanManageSeasons(), SavedRevision: CoachAssignmentRevision(savedSet)}
	board.Revision = board.SavedRevision
	if !board.CanEdit {
		return readOnlyCoachBoard(board, saved), nil
	}
	return s.staffBoard(ctx, access, board, saved, request)
}

// readOnlyCoachBoard lists the saved assignments by team, for coaches.
func readOnlyCoachBoard(board CoachBoard, saved []SeasonCoach) CoachBoard {
	for _, coach := range saved {
		index := slices.IndexFunc(board.Teams, func(team CoachBoardTeam) bool { return team.Team.ID == coach.TeamID })
		if index < 0 {
			board.Teams = append(board.Teams, CoachBoardTeam{Team: Team{ID: coach.TeamID, GroupID: coach.GroupID, Name: coach.TeamName}})
			index = len(board.Teams) - 1
		}
		board.Teams[index].Assignments = append(board.Teams[index].Assignments, CoachBoardAssignment{
			CoachUserID: coach.CoachUserID, DisplayName: coach.DisplayName, Username: coach.Username, Head: coach.IsHead, Saved: true,
		})
	}
	return board
}

func (s *CoachBoardService) staffBoard(ctx context.Context, access *GroupAccess, board CoachBoard, saved []SeasonCoach, request CoachBoardRequest) (CoachBoard, error) {
	groupID := access.GroupID()
	season := board.Season
	teams, err := s.teams.ListTeams(ctx, groupID)
	if err != nil {
		return CoachBoard{}, fmt.Errorf("list teams: %w", err)
	}
	coaches, err := s.candidates(ctx, groupID, season)
	if err != nil {
		return CoachBoard{}, err
	}
	seasons, err := s.seasons.ListSeasons(ctx, groupID)
	if err != nil {
		return CoachBoard{}, fmt.Errorf("list seasons: %w", err)
	}
	board.PreviousSeason, board.HasPrevious = PreviousSeason(seasons, season)

	teamByID := make(map[int64]Team, len(teams))
	for _, summary := range teams {
		teamByID[summary.Team.ID] = summary.Team
	}
	coachByID := make(map[int64]CoachCandidate, len(coaches))
	for _, coach := range coaches {
		coachByID[coach.UserID] = coach
	}
	savedSet := assignmentsOf(saved)

	// The draft keeps only teams of the group and current coaches; it comes
	// from the browser.
	draft := savedSet
	suggestions := map[[2]int64]CoachSuggestion{}
	if request.Draft != nil {
		if len(request.Draft.Assignments) > MaxCoachBoardAssignments {
			return CoachBoard{}, ErrCoachBoardTooLarge
		}
		draft = draft[:0:0]
		for _, assignment := range normalizeCoachAssignments(season.ID, request.Draft.Assignments) {
			if _, ok := teamByID[assignment.TeamID]; !ok {
				continue
			}
			if _, ok := coachByID[assignment.CoachUserID]; !ok {
				continue
			}
			draft = append(draft, assignment)
		}
		draft = singleHeads(draft)
		for _, suggested := range request.Draft.Suggestions {
			suggestions[[2]int64{suggested.Assignment.TeamID, suggested.Assignment.CoachUserID}] = suggested.Suggestion
		}
		if request.Draft.Revision != "" {
			board.Revision = request.Draft.Revision
		}
	}
	if request.Suggest == CoachSuggestReplace {
		draft = nil
		suggestions = map[[2]int64]CoachSuggestion{}
	}
	if request.Suggest == CoachSuggestFill || request.Suggest == CoachSuggestReplace {
		var previous []CoachAssignment
		if board.HasPrevious {
			previousCoaches, err := s.seasons.ListSeasonCoaches(ctx, groupID, SeasonCoachFilter{SeasonID: board.PreviousSeason.ID})
			if err != nil {
				return CoachBoard{}, fmt.Errorf("list previous season coaches: %w", err)
			}
			previous = assignmentsOf(previousCoaches)
		}
		var active []Team
		for _, summary := range teams {
			if !summary.Team.Archived() {
				active = append(active, summary.Team)
			}
		}
		added := SuggestCoaches(CoachSuggestionInput{Season: season, Teams: active, Coaches: coaches, Previous: previous, Current: draft})
		for _, suggested := range added {
			draft = append(draft, suggested.Assignment)
			suggestions[[2]int64{suggested.Assignment.TeamID, suggested.Assignment.CoachUserID}] = suggested.Suggestion
		}
		board.Suggested = len(added)
		draft = normalizeCoachAssignments(season.ID, draft)
	}
	board.Changed = CoachAssignmentRevision(draft) != board.SavedRevision

	savedHead := make(map[[2]int64]bool, len(savedSet))
	for _, assignment := range savedSet {
		savedHead[[2]int64{assignment.TeamID, assignment.CoachUserID}] = assignment.Head
	}
	load := make(map[int64]int)
	for _, summary := range teams {
		team := summary.Team
		column := CoachBoardTeam{Team: team, Ages: TeamAgeRange(team, season)}
		for _, assignment := range draft {
			if assignment.TeamID != team.ID {
				continue
			}
			key := [2]int64{assignment.TeamID, assignment.CoachUserID}
			coach := coachByID[assignment.CoachUserID]
			head, saved := savedHead[key]
			entry := CoachBoardAssignment{
				CoachUserID: coach.UserID, DisplayName: coach.DisplayName, Username: coach.Username,
				Head: assignment.Head, Saved: saved && head == assignment.Head,
				Warnings: CoachAssignmentWarnings(coach, team, season),
			}
			if suggestion, ok := suggestions[key]; ok && !saved && validSuggestion(suggestion, coach, team, season) {
				entry.Suggestion = &suggestion
			}
			load[coach.UserID]++
			board.Warnings = append(board.Warnings, entry.Warnings...)
			column.Assignments = append(column.Assignments, entry)
		}
		// Archived teams show only while they hold assignments.
		if team.Archived() && len(column.Assignments) == 0 {
			continue
		}
		slices.SortStableFunc(column.Assignments, func(a, b CoachBoardAssignment) int {
			if a.Head != b.Head {
				if a.Head {
					return -1
				}
				return 1
			}
			return cmp.Or(strings.Compare(coachSortName(a.DisplayName, a.Username), coachSortName(b.DisplayName, b.Username)), cmp.Compare(a.CoachUserID, b.CoachUserID))
		})
		board.Teams = append(board.Teams, column)
	}
	for _, coach := range coaches {
		board.Coaches = append(board.Coaches, CoachBoardCoach{CoachCandidate: coach, Teams: load[coach.UserID]})
	}
	return board, nil
}

// validSuggestion keeps a carried explanation only when it still holds.
func validSuggestion(suggestion CoachSuggestion, coach CoachCandidate, team Team, season Season) bool {
	switch suggestion.Reason {
	case CoachSuggestionPreviousSeason:
		return suggestion.Load >= 0 && suggestion.Load <= MaxCoachBoardAssignments
	case CoachSuggestionQualification:
		return coach.Qualification != nil && coach.Qualification.Range() == suggestion.Qualification && coach.Qualification.Covers(TeamAgeRange(team, season)) &&
			suggestion.Load >= 0 && suggestion.Load <= MaxCoachBoardAssignments
	}
	return false
}

// singleHeads keeps the first head coach mark of each team in a sorted
// draft.
func singleHeads(assignments []CoachAssignment) []CoachAssignment {
	headed := make(map[int64]bool)
	for index := range assignments {
		if !assignments[index].Head {
			continue
		}
		if headed[assignments[index].TeamID] {
			assignments[index].Head = false
			continue
		}
		headed[assignments[index].TeamID] = true
	}
	return assignments
}

// candidates returns the group's coaches by name with their qualification
// and whether they have availability that counts in some part of season.
func (s *CoachBoardService) candidates(ctx context.Context, groupID int64, season Season) ([]CoachCandidate, error) {
	members, err := s.memberships.ListMembers(ctx, groupID)
	if err != nil {
		return nil, fmt.Errorf("list coaches: %w", err)
	}
	qualifications, err := s.coaches.ListQualifications(ctx, groupID)
	if err != nil {
		return nil, fmt.Errorf("list coach qualifications: %w", err)
	}
	available := make(map[int64]bool)
	for _, part := range season.Parts {
		windows, err := s.coaches.ListAvailability(ctx, groupID, part.ID, 0)
		if err != nil {
			return nil, fmt.Errorf("list availability: %w", err)
		}
		view := PartAvailability{Season: season, Part: part}
		for _, window := range windows {
			if view.Applies(window) {
				available[window.CoachUserID] = true
			}
		}
	}
	coaches := make([]CoachCandidate, 0)
	for _, member := range members {
		if !member.HasRole(GroupRoleCoach) {
			continue
		}
		coach := CoachCandidate{UserID: member.UserID, DisplayName: member.DisplayName, Username: member.Username, HasAvailability: available[member.UserID]}
		if index := slices.IndexFunc(qualifications, func(q CoachQualification) bool { return q.CoachUserID == member.UserID }); index >= 0 {
			qualification := qualifications[index]
			coach.Qualification = &qualification
		}
		coaches = append(coaches, coach)
	}
	slices.SortFunc(coaches, func(a, b CoachCandidate) int {
		return cmp.Or(strings.Compare(coachSortName(a.DisplayName, a.Username), coachSortName(b.DisplayName, b.Username)), cmp.Compare(a.UserID, b.UserID))
	})
	return coaches, nil
}

// Save replaces a season's coach assignments with assignments, at the
// revision the board was loaded with (CoachAssignmentRevision). Every coach
// must hold the coach role and every team belong to the group; a team has
// at most one head coach. A different current set is
// ErrOptimisticLockConflict; a season that is gone is ErrSeasonNotFound.
// Admins, managers, and site administrators; audited when something changed.
func (s *CoachBoardService) Save(ctx context.Context, actor GroupActor, seasonID int64, revision string, assignments []CoachAssignment) (CoachAssignmentChanges, error) {
	if err := s.validate(ctx); err != nil {
		return CoachAssignmentChanges{}, err
	}
	if err := authorizeGroupAction(actor, actor.Access.CanManageSeasons()); err != nil {
		return CoachAssignmentChanges{}, err
	}
	if seasonID <= 0 {
		return CoachAssignmentChanges{}, ErrSeasonNotFound
	}
	if len(assignments) > MaxCoachBoardAssignments {
		return CoachAssignmentChanges{}, ErrCoachBoardTooLarge
	}
	groupID := actor.Access.GroupID()
	season, err := s.seasons.FindSeason(ctx, groupID, seasonID)
	if err != nil {
		return CoachAssignmentChanges{}, err
	}
	desired := normalizeCoachAssignments(season.ID, assignments)
	if err := s.checkAssignments(ctx, groupID, desired); err != nil {
		return CoachAssignmentChanges{}, err
	}
	changes, err := s.seasons.ReplaceSeasonCoaches(ctx, groupID, season.ID, s.now().Unix(), func(current []CoachAssignment) ([]CoachAssignment, error) {
		if CoachAssignmentRevision(current) != revision {
			return nil, ErrOptimisticLockConflict
		}
		return desired, nil
	})
	switch {
	case err == nil:
	case errors.Is(err, ErrSeasonNotFound), errors.Is(err, ErrRecordNotFound):
		// A season that vanished is not-found, never a conflict.
		return CoachAssignmentChanges{}, ErrSeasonNotFound
	case errors.Is(err, ErrOptimisticLockConflict), errors.Is(err, ErrTeamNotFound), errors.Is(err, ErrSeasonCoachRoleMissing):
		return CoachAssignmentChanges{}, err
	default:
		return CoachAssignmentChanges{}, fmt.Errorf("save season coaches: %w", err)
	}
	if changes.Total() > 0 {
		detail := "season " + strconv.FormatInt(season.ID, 10) + ": " + season.Name +
			"; added " + strconv.Itoa(changes.Added) + "; removed " + strconv.Itoa(changes.Removed) +
			"; head changed " + strconv.Itoa(changes.HeadChanged) + "; assignments " + strconv.Itoa(len(desired))
		if actor.Access.IsSiteAdmin() && !actor.Access.IsMember() {
			detail += " (site administrator)"
		}
		recordGroupAudit(ctx, s.auditor, s.users, actor.Access.UserID(), actor.Origin, groupID, AuditActionSeasonCoachesSaved, detail)
	}
	return changes, nil
}

// checkAssignments validates a normalized assignment set against the group.
func (s *CoachBoardService) checkAssignments(ctx context.Context, groupID int64, assignments []CoachAssignment) error {
	teams, err := s.teams.ListTeams(ctx, groupID)
	if err != nil {
		return fmt.Errorf("list teams: %w", err)
	}
	members, err := s.memberships.ListMembers(ctx, groupID)
	if err != nil {
		return fmt.Errorf("list coaches: %w", err)
	}
	heads := make(map[int64]bool)
	for _, assignment := range assignments {
		if !slices.ContainsFunc(teams, func(team TeamSummary) bool { return team.Team.ID == assignment.TeamID }) {
			return ErrTeamNotFound
		}
		if !slices.ContainsFunc(members, func(member GroupMember) bool {
			return member.UserID == assignment.CoachUserID && member.HasRole(GroupRoleCoach)
		}) {
			return ErrSeasonCoachRoleMissing
		}
		if assignment.Head {
			if heads[assignment.TeamID] {
				return ErrCoachBoardTwoHeads
			}
			heads[assignment.TeamID] = true
		}
	}
	return nil
}

func (s *CoachBoardService) validate(ctx context.Context) error {
	if s == nil || s.seasons == nil || s.teams == nil || s.coaches == nil || s.memberships == nil || s.users == nil || s.now == nil {
		return ErrInvalidGroup
	}
	if ctx == nil {
		return errors.New("service: nil coach board context")
	}
	return nil
}

func assignmentsOf(coaches []SeasonCoach) []CoachAssignment {
	assignments := make([]CoachAssignment, 0, len(coaches))
	for _, coach := range coaches {
		assignments = append(assignments, CoachAssignment{SeasonID: coach.SeasonID, TeamID: coach.TeamID, CoachUserID: coach.CoachUserID, Head: coach.IsHead})
	}
	sortCoachAssignments(assignments)
	return assignments
}

// Stale reports whether the saved assignments changed since the shown draft
// was loaded; saving it would be a conflict.
func (b CoachBoard) Stale() bool {
	return b.Revision != b.SavedRevision
}
