package service

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func parentOnTeam(teamID int64, children ...int64) GroupActor {
	return actorWith(GroupAccessGrants{Member: true, Roles: []string{GroupRoleParent}, OwnTeams: []TeamRole{{TeamID: teamID, Role: GroupRoleParent}}, ChildUserIDs: children})
}

func TestProfileServiceAddChildToTeam(t *testing.T) {
	tests := []struct {
		name    string
		actor   GroupActor
		teamID  int64
		input   ProfileInput
		wantErr error
	}{
		{name: "parent of the team", actor: parentOnTeam(4), teamID: 4, input: ProfileInput{DisplayName: " Anna ", BirthYear: 2016}},
		{name: "parent through a linked child's team", actor: actorWith(GroupAccessGrants{Member: true, Roles: []string{GroupRoleParent}, ChildTeamIDs: []int64{4}, ChildUserIDs: []int64{30}}), teamID: 4, input: ProfileInput{DisplayName: "Anna", BirthYear: 2016}},
		{name: "parent of another team", actor: parentOnTeam(9), teamID: 4, input: ProfileInput{DisplayName: "Anna", BirthYear: 2016}, wantErr: ErrTeamNotFound},
		{name: "parent who sees the team but is not its parent", actor: actorWith(GroupAccessGrants{Member: true, Roles: []string{GroupRoleParent}, OwnTeams: []TeamRole{{TeamID: 4, Role: GroupRolePlayer}}}), teamID: 4, input: ProfileInput{DisplayName: "Anna", BirthYear: 2016}, wantErr: ErrGroupForbidden},
		{name: "coach is not a parent", actor: actorWith(GroupAccessGrants{Member: true, Roles: []string{GroupRoleCoach}, CoachedTeamIDs: []int64{4}}), teamID: 4, input: ProfileInput{DisplayName: "Anna", BirthYear: 2016}, wantErr: ErrGroupForbidden},
		{name: "non-member", actor: actorWith(GroupAccessGrants{}), teamID: 4, input: ProfileInput{DisplayName: "Anna", BirthYear: 2016}, wantErr: ErrGroupNotFound},
		{name: "empty name", actor: parentOnTeam(4), teamID: 4, input: ProfileInput{DisplayName: " ", BirthYear: 2016}, wantErr: ErrInvalidProfileName},
		{name: "birth year", actor: parentOnTeam(4), teamID: 4, input: ProfileInput{DisplayName: "Anna", BirthYear: 1800}, wantErr: ErrInvalidProfileBirthYear},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repository := newProfileStub()
			service, auditor := newTestProfileService(t, repository)
			_, err := service.AddChildToTeam(context.Background(), test.actor, test.teamID, test.input)
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("AddChildToTeam() error = %v, want %v", err, test.wantErr)
			}
			if test.wantErr != nil {
				if repository.created.UserID != 0 || len(auditor.records) != 0 {
					t.Fatal("failed add changed state")
				}
				return
			}
			if repository.guardianID != 7 || repository.teamID != 4 || repository.created.DisplayName != "Anna" || repository.created.GroupID != 5 {
				t.Fatalf("created %+v guardian %d team %d", repository.created, repository.guardianID, repository.teamID)
			}
			if len(auditor.records) != 1 || auditor.records[0].Action != AuditActionProfileCreated || strings.Contains(auditor.records[0].Detail, "Anna") || strings.Contains(auditor.records[0].Detail, "2016") {
				t.Fatalf("audit = %+v, want one entry naming the profile by ID only", auditor.records)
			}
		})
	}
}

func TestProfileServiceAddChildToArchivedTeam(t *testing.T) {
	service, _ := newTestProfileService(t, newProfileStub())
	archivedAt := int64(1)
	service.teams = newTeamStub(Team{ID: 4, GroupID: 5, ArchivedAt: &archivedAt})
	if _, err := service.AddChildToTeam(context.Background(), parentOnTeam(4), 4, ProfileInput{DisplayName: "Anna", BirthYear: 2016}); !errors.Is(err, ErrGroupForbidden) {
		t.Fatalf("archived team error = %v, want ErrGroupForbidden", err)
	}
}

func TestProfileServiceJoinAndLeaveTeam(t *testing.T) {
	repository := newProfileStub(ChildProfile{UserID: 30, GroupID: 5, DisplayName: "Anna", BirthYear: 2016})
	service, auditor := newTestProfileService(t, repository)
	teams := newTeamStub(Team{ID: 4, GroupID: 5}, Team{ID: 6, GroupID: 5})
	service.teams = teams
	ctx := context.Background()

	if err := service.JoinChildToTeam(ctx, parentOnTeam(4, 30), 4, 30); err != nil {
		t.Fatalf("JoinChildToTeam() error = %v", err)
	}
	if len(teams.rosterAdds) != 1 || teams.rosterAdds[0] != GroupRolePlayer {
		t.Fatalf("roster adds = %v, want one player entry", teams.rosterAdds)
	}
	// Only linked parents act for the child.
	if err := service.JoinChildToTeam(ctx, parentOnTeam(4), 4, 30); !errors.Is(err, ErrProfileNotFound) {
		t.Fatalf("unlinked parent join error = %v, want ErrProfileNotFound", err)
	}
	// The parent must be a parent of the target team.
	if err := service.JoinChildToTeam(ctx, parentOnTeam(4, 30), 6, 30); !errors.Is(err, ErrTeamNotFound) {
		t.Fatalf("join other team error = %v, want ErrTeamNotFound", err)
	}
	if err := service.RemoveChildFromTeam(ctx, parentOnTeam(4, 30), 4, 30); err != nil {
		t.Fatalf("RemoveChildFromTeam() error = %v", err)
	}
	if len(teams.rosterRemoves) != 1 {
		t.Fatalf("roster removes = %v", teams.rosterRemoves)
	}
	if err := service.RemoveChildFromTeam(ctx, parentOnTeam(4), 4, 30); !errors.Is(err, ErrProfileNotFound) {
		t.Fatalf("unlinked parent leave error = %v, want ErrProfileNotFound", err)
	}
	// Account children (not profiles) are not handled here.
	if err := service.JoinChildToTeam(ctx, parentOnTeam(4, 31), 4, 31); !errors.Is(err, ErrProfileNotFound) {
		t.Fatalf("account child join error = %v, want ErrProfileNotFound", err)
	}
	if len(auditor.records) != 2 || auditor.records[0].Action != AuditActionProfileTeamAdded || auditor.records[1].Action != AuditActionProfileTeamRemoved {
		t.Fatalf("audit = %+v", auditor.records)
	}
}

func TestGroupAccessChildTeamQuestions(t *testing.T) {
	parent := accessFor(GroupAccessGrants{Member: true, Roles: []string{GroupRoleParent}, OwnTeams: []TeamRole{{TeamID: 4, Role: GroupRoleParent}}, ChildUserIDs: []int64{30}})
	if !parent.CanAddChildToTeam(4) || parent.CanAddChildToTeam(5) || parent.CanAddChildToTeam(0) {
		t.Error("CanAddChildToTeam must hold for the parent's own team only")
	}
	if !parent.CanRemoveChildFromTeam(4, 30) || parent.CanRemoveChildFromTeam(4, 31) || parent.CanRemoveChildFromTeam(5, 30) {
		t.Error("CanRemoveChildFromTeam must hold for the guardian's child on a visible team only")
	}
	if accessFor(GroupAccessGrants{SiteAdmin: true}).CanAddChildToTeam(4) || accessFor(GroupAccessGrants{Member: true, Roles: []string{GroupRoleAdmin}}).CanAddChildToTeam(4) {
		t.Error("only parents of the team add children through the team page")
	}
}

func TestCanRespondForProfileChildIsGuardiansOnly(t *testing.T) {
	event := Event{ID: 1, GroupID: 5}
	guardian := accessFor(GroupAccessGrants{Member: true, Roles: []string{GroupRoleParent}, ChildUserIDs: []int64{30}, CalledUpEventIDs: []int64{1}})
	other := accessFor(GroupAccessGrants{Member: true, Roles: []string{GroupRoleParent}, CalledUpEventIDs: []int64{1}})
	if !guardian.CanRespondFor(event, 30) {
		t.Error("the linked parent must answer for the profile child")
	}
	if other.CanRespondFor(event, 30) {
		t.Error("an unlinked parent must not answer for the profile child")
	}
}
