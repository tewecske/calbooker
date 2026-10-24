package service

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
)

type teamRepositoryStub struct {
	teams           map[int64]Team
	summaries       []TeamSummary
	roster          []TeamRosterEntry
	createErr       error
	updateErr       error
	addErr          error
	removeErr       error
	expectedVersion int64
	created         Team
	rosterAdds      []string
	rosterRemoves   []string
}

func newTeamStub(teams ...Team) *teamRepositoryStub {
	stub := &teamRepositoryStub{teams: make(map[int64]Team)}
	for _, team := range teams {
		stub.teams[team.ID] = team
		stub.summaries = append(stub.summaries, TeamSummary{Team: team})
	}
	return stub
}

func (r *teamRepositoryStub) CreateTeam(_ context.Context, team Team) (Team, error) {
	if r.createErr != nil {
		return Team{}, r.createErr
	}
	team.ID = 99
	r.created = team
	return team, nil
}

func (r *teamRepositoryStub) FindTeam(_ context.Context, groupID, teamID int64) (Team, error) {
	team, ok := r.teams[teamID]
	if !ok || team.GroupID != groupID {
		return Team{}, ErrTeamNotFound
	}
	return team, nil
}

func (r *teamRepositoryStub) ListTeams(context.Context, int64) ([]TeamSummary, error) {
	return r.summaries, nil
}

func (r *teamRepositoryStub) UpdateTeam(_ context.Context, team Team, expectedVersion int64) (Team, error) {
	r.expectedVersion = expectedVersion
	if r.updateErr != nil {
		return Team{}, r.updateErr
	}
	team.Version++
	return team, nil
}

func (r *teamRepositoryStub) ListRoster(context.Context, int64, int64) ([]TeamRosterEntry, error) {
	return slices.Clone(r.roster), nil
}

func (r *teamRepositoryStub) AddRosterEntry(_ context.Context, _, _, _ int64, role string, _ int64) error {
	r.rosterAdds = append(r.rosterAdds, role)
	return r.addErr
}

func (r *teamRepositoryStub) RemoveRosterEntry(_ context.Context, _, _, _ int64, role string) error {
	r.rosterRemoves = append(r.rosterRemoves, role)
	return r.removeErr
}

func newTestTeamService(t *testing.T, teams *teamRepositoryStub, memberships *groupMembershipRepositoryStub) (*TeamService, *auditRecorderStub) {
	t.Helper()
	service, err := NewTeamService(teams, memberships, usersWith(User{ID: 7, Email: strPtr("staff@example.test")}))
	if err != nil {
		t.Fatalf("NewTeamService() error = %v", err)
	}
	auditor := &auditRecorderStub{}
	service.SetAuditor(auditor)
	return service, auditor
}

func coachOf(teamIDs ...int64) GroupActor {
	return actorWith(GroupAccessGrants{Member: true, Roles: []string{GroupRoleCoach}, CoachedTeamIDs: teamIDs})
}

func playerOn(teamIDs ...int64) GroupActor {
	grants := GroupAccessGrants{Member: true, Roles: []string{GroupRolePlayer}}
	for _, id := range teamIDs {
		grants.OwnTeams = append(grants.OwnTeams, TeamRole{TeamID: id, Role: GroupRolePlayer})
	}
	return actorWith(grants)
}

func TestTeamServiceCreate(t *testing.T) {
	valid := TeamInput{Name: " U10 Lions ", BirthYearFrom: 2015, BirthYearTo: 2016, Description: " U10 "}
	tests := []struct {
		name    string
		actor   GroupActor
		input   TeamInput
		wantErr error
	}{
		{name: "admin creates", actor: adminActor(), input: valid},
		{name: "manager creates without description", actor: actorWith(memberWith(GroupRoleManager)), input: TeamInput{Name: "Seniors", BirthYearFrom: 1950, BirthYearTo: 2005}},
		{name: "coach is forbidden", actor: coachOf(1), input: valid, wantErr: ErrGroupForbidden},
		{name: "player is forbidden", actor: playerOn(1), input: valid, wantErr: ErrGroupForbidden},
		{name: "outsider is not found", actor: actorWith(GroupAccessGrants{}), input: valid, wantErr: ErrGroupNotFound},
		{name: "empty name", actor: adminActor(), input: TeamInput{Name: " ", BirthYearFrom: 2015, BirthYearTo: 2016}, wantErr: ErrInvalidTeamName},
		{name: "long name", actor: adminActor(), input: TeamInput{Name: strings.Repeat("a", 65), BirthYearFrom: 2015, BirthYearTo: 2016}, wantErr: ErrInvalidTeamName},
		{name: "reversed years", actor: adminActor(), input: TeamInput{Name: "A", BirthYearFrom: 2016, BirthYearTo: 2015}, wantErr: ErrInvalidTeamBirthYears},
		{name: "missing years", actor: adminActor(), input: TeamInput{Name: "A"}, wantErr: ErrInvalidTeamBirthYears},
		{name: "year too late", actor: adminActor(), input: TeamInput{Name: "A", BirthYearFrom: 2015, BirthYearTo: 2201}, wantErr: ErrInvalidTeamBirthYears},
		{name: "long description", actor: adminActor(), input: TeamInput{Name: "A", BirthYearFrom: 2015, BirthYearTo: 2015, Description: strings.Repeat("d", 256)}, wantErr: ErrInvalidTeamDescription},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			teams := newTeamStub()
			service, auditor := newTestTeamService(t, teams, newMembershipStub())
			created, err := service.Create(context.Background(), test.actor, test.input)
			if test.wantErr != nil {
				if !errors.Is(err, test.wantErr) {
					t.Fatalf("Create() error = %v, want %v", err, test.wantErr)
				}
				if len(auditor.records) != 0 {
					t.Fatal("failed create was audited")
				}
				return
			}
			if err != nil {
				t.Fatalf("Create() error = %v", err)
			}
			if created.GroupID != 5 || created.Name != strings.TrimSpace(test.input.Name) {
				t.Fatalf("created = %+v, want trimmed team in group 5", created)
			}
			if test.input.Description == "" && created.Description != nil {
				t.Fatalf("description = %q, want none", *created.Description)
			}
			if len(auditor.records) != 1 || auditor.records[0].Action != AuditActionTeamCreated || auditor.records[0].TargetType != "group" || auditor.records[0].TargetID != "5" {
				t.Fatalf("audit = %+v", auditor.records)
			}
		})
	}
}

func TestTeamServiceListFiltersByVisibility(t *testing.T) {
	teams := newTeamStub(Team{ID: 1, GroupID: 5, Name: "A"}, Team{ID: 2, GroupID: 5, Name: "B"}, Team{ID: 3, GroupID: 5, Name: "C"})
	service, _ := newTestTeamService(t, teams, newMembershipStub())
	for _, test := range []struct {
		name  string
		actor GroupActor
		want  []int64
	}{
		{"admin sees all", adminActor(), []int64{1, 2, 3}},
		{"player sees own team", playerOn(2), []int64{2}},
		{"coach sees coached teams", coachOf(1, 3), []int64{1, 3}},
		{"member without teams sees none", actorWith(memberWith()), nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			list, err := service.List(context.Background(), test.actor.Access)
			if err != nil {
				t.Fatalf("List() error = %v", err)
			}
			var ids []int64
			for _, team := range list {
				ids = append(ids, team.Team.ID)
			}
			if !slices.Equal(ids, test.want) {
				t.Fatalf("teams = %v, want %v", ids, test.want)
			}
		})
	}
	if _, err := service.List(context.Background(), nil); !errors.Is(err, ErrGroupNotFound) {
		t.Fatalf("outsider List() error = %v", err)
	}
}

func TestTeamServiceDetail(t *testing.T) {
	email := "player@example.test"
	teams := newTeamStub(Team{ID: 1, GroupID: 5, Name: "A"}, Team{ID: 2, GroupID: 5, Name: "B"})
	teams.roster = []TeamRosterEntry{{UserID: 8, Role: GroupRolePlayer, Email: &email}}
	memberships := newMembershipStub()
	memberships.members = []GroupMember{
		{UserID: 8, Roles: []string{GroupRolePlayer}},
		{UserID: 9, Roles: []string{GroupRoleCoach}},
		{UserID: 10, Roles: []string{GroupRoleParent}},
	}
	service, _ := newTestTeamService(t, teams, memberships)

	detail, err := service.Detail(context.Background(), adminActor().Access, 1)
	if err != nil {
		t.Fatalf("admin Detail() error = %v", err)
	}
	if detail.Roster[0].Email == nil || len(detail.Candidates) != 2 {
		t.Fatalf("admin detail = %+v, want email and player/parent candidates", detail)
	}

	detail, err = service.Detail(context.Background(), playerOn(1).Access, 1)
	if err != nil {
		t.Fatalf("player Detail() error = %v", err)
	}
	if detail.Roster[0].Email != nil || len(detail.Candidates) != 0 {
		t.Fatalf("player detail = %+v, want no emails and no candidates", detail)
	}

	detail, err = service.Detail(context.Background(), coachOf(1).Access, 1)
	if err != nil || detail.Roster[0].Email == nil || len(detail.Candidates) != 2 {
		t.Fatalf("coach Detail() = %+v, %v; want emails and candidates", detail, err)
	}

	if _, err := service.Detail(context.Background(), playerOn(1).Access, 2); !errors.Is(err, ErrTeamNotFound) {
		t.Fatalf("other team Detail() error = %v, want ErrTeamNotFound", err)
	}
	if _, err := service.Detail(context.Background(), adminActor().Access, 42); !errors.Is(err, ErrTeamNotFound) {
		t.Fatalf("missing team Detail() error = %v, want ErrTeamNotFound", err)
	}
	if _, err := service.Detail(context.Background(), actorWith(GroupAccessGrants{}).Access, 1); !errors.Is(err, ErrGroupNotFound) {
		t.Fatalf("outsider Detail() error = %v, want ErrGroupNotFound", err)
	}
}

func TestTeamServiceUpdateAndArchive(t *testing.T) {
	input := TeamInput{Name: "Renamed", BirthYearFrom: 2014, BirthYearTo: 2015}
	tests := []struct {
		name     string
		actor    GroupActor
		writeErr error
		wantErr  error
	}{
		{name: "manager edits", actor: actorWith(memberWith(GroupRoleManager))},
		{name: "coach of the team cannot edit it", actor: coachOf(1), wantErr: ErrGroupForbidden},
		{name: "player of another team gets not found", actor: playerOn(2), wantErr: ErrTeamNotFound},
		{name: "stale revision", actor: adminActor(), writeErr: ErrOptimisticLockConflict, wantErr: ErrOptimisticLockConflict},
		{name: "vanished team is not found, not a conflict", actor: adminActor(), writeErr: ErrRecordNotFound, wantErr: ErrTeamNotFound},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			teams := newTeamStub(Team{ID: 1, GroupID: 5, Name: "A", BirthYearFrom: 2015, BirthYearTo: 2015, Version: 2})
			teams.updateErr = test.writeErr
			service, auditor := newTestTeamService(t, teams, newMembershipStub())
			updated, err := service.Update(context.Background(), test.actor, 1, 2, input)
			if test.wantErr != nil {
				if !errors.Is(err, test.wantErr) {
					t.Fatalf("Update() error = %v, want %v", err, test.wantErr)
				}
				if IsWriteConflict(err) && errors.Is(test.writeErr, ErrRecordNotFound) {
					t.Fatal("vanished team reported as a conflict")
				}
				return
			}
			if err != nil {
				t.Fatalf("Update() error = %v", err)
			}
			if updated.Name != "Renamed" || teams.expectedVersion != 2 || len(auditor.records) != 1 || auditor.records[0].Action != AuditActionTeamUpdated {
				t.Fatalf("updated = %+v version %d audit %+v", updated, teams.expectedVersion, auditor.records)
			}
		})
	}

	teams := newTeamStub(Team{ID: 1, GroupID: 5, Name: "A", BirthYearFrom: 2015, BirthYearTo: 2015, Version: 2})
	service, auditor := newTestTeamService(t, teams, newMembershipStub())
	archived, err := service.SetArchived(context.Background(), adminActor(), 1, 2, true)
	if err != nil || !archived.Archived() {
		t.Fatalf("SetArchived(true) = %+v, %v", archived, err)
	}
	teams.teams[1] = archived
	restored, err := service.SetArchived(context.Background(), adminActor(), 1, archived.Version, false)
	if err != nil || restored.Archived() {
		t.Fatalf("SetArchived(false) = %+v, %v", restored, err)
	}
	if len(auditor.records) != 2 || auditor.records[0].Action != AuditActionTeamArchived || auditor.records[1].Action != AuditActionTeamRestored {
		t.Fatalf("audit = %+v", auditor.records)
	}
	if _, err := service.SetArchived(context.Background(), playerOn(1), 1, 2, true); !errors.Is(err, ErrGroupForbidden) {
		t.Fatalf("player SetArchived() error = %v, want ErrGroupForbidden", err)
	}
	teams.teams[1] = restored
	unchanged, err := service.SetArchived(context.Background(), adminActor(), 1, restored.Version, false)
	if err != nil || len(auditor.records) != 2 || unchanged.Version != restored.Version {
		t.Fatalf("no-op SetArchived() = %+v, %v; want unchanged and unaudited", unchanged, err)
	}
}

func TestTeamServiceRoster(t *testing.T) {
	tests := []struct {
		name     string
		actor    GroupActor
		teamID   int64
		role     string
		writeErr error
		wantErr  error
	}{
		{name: "admin adds a player", actor: adminActor(), teamID: 1, role: GroupRolePlayer},
		{name: "coach of the team adds a parent", actor: coachOf(1), teamID: 1, role: GroupRoleParent},
		{name: "coach of another team gets not found", actor: coachOf(2), teamID: 1, role: GroupRolePlayer, wantErr: ErrTeamNotFound},
		{name: "player on the team is forbidden", actor: playerOn(1), teamID: 1, role: GroupRolePlayer, wantErr: ErrGroupForbidden},
		{name: "coach role is not a roster role", actor: adminActor(), teamID: 1, role: GroupRoleCoach, wantErr: ErrInvalidRosterRole},
		{name: "member without the group role", actor: adminActor(), teamID: 1, role: GroupRolePlayer, writeErr: ErrRosterRoleMissing, wantErr: ErrRosterRoleMissing},
		{name: "missing team", actor: adminActor(), teamID: 42, role: GroupRolePlayer, wantErr: ErrTeamNotFound},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			teams := newTeamStub(Team{ID: 1, GroupID: 5, Name: "A"})
			teams.addErr = test.writeErr
			teams.removeErr = test.writeErr
			service, auditor := newTestTeamService(t, teams, newMembershipStub())
			err := service.AddToRoster(context.Background(), test.actor, test.teamID, 8, test.role)
			if test.wantErr != nil {
				if !errors.Is(err, test.wantErr) {
					t.Fatalf("AddToRoster() error = %v, want %v", err, test.wantErr)
				}
				if len(auditor.records) != 0 {
					t.Fatal("failed roster change was audited")
				}
				return
			}
			if err != nil {
				t.Fatalf("AddToRoster() error = %v", err)
			}
			if err := service.RemoveFromRoster(context.Background(), test.actor, test.teamID, 8, test.role); err != nil {
				t.Fatalf("RemoveFromRoster() error = %v", err)
			}
			if len(auditor.records) != 2 || auditor.records[0].Action != AuditActionRosterAdded || auditor.records[1].Action != AuditActionRosterRemoved {
				t.Fatalf("audit = %+v", auditor.records)
			}
		})
	}

	teams := newTeamStub(Team{ID: 1, GroupID: 5, Name: "A"})
	teams.removeErr = ErrRosterEntryNotFound
	service, _ := newTestTeamService(t, teams, newMembershipStub())
	if err := service.RemoveFromRoster(context.Background(), adminActor(), 1, 8, GroupRolePlayer); !errors.Is(err, ErrRosterEntryNotFound) {
		t.Fatalf("RemoveFromRoster(missing) error = %v, want ErrRosterEntryNotFound", err)
	}
}

func TestNewTeamServiceRejectsNilDependencies(t *testing.T) {
	if _, err := NewTeamService(nil, newMembershipStub(), usersWith()); !errors.Is(err, ErrNilTeamRepository) {
		t.Fatalf("nil teams error = %v", err)
	}
	if _, err := NewTeamService(newTeamStub(), nil, usersWith()); !errors.Is(err, ErrNilMembershipRepository) {
		t.Fatalf("nil memberships error = %v", err)
	}
	if _, err := NewTeamService(newTeamStub(), newMembershipStub(), nil); !errors.Is(err, ErrNilUserRepository) {
		t.Fatalf("nil users error = %v", err)
	}
}
