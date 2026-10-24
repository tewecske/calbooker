package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// inviteRepositoryStub keeps invites in memory. RedeemInvite applies the
// same state checks as the store and records what it granted.
type inviteRepositoryStub struct {
	invites    map[int64]GroupInvite
	nextID     int64
	collisions int
	createErr  error
	redeemErr  error
	redeemed   []int64
	changed    bool
}

func newInviteStub(invites ...GroupInvite) *inviteRepositoryStub {
	stub := &inviteRepositoryStub{invites: make(map[int64]GroupInvite), nextID: 100, changed: true}
	for _, invite := range invites {
		stub.invites[invite.ID] = invite
	}
	return stub
}

func (r *inviteRepositoryStub) CreateInvite(_ context.Context, invite GroupInvite) (GroupInvite, error) {
	if r.createErr != nil {
		return GroupInvite{}, r.createErr
	}
	if r.collisions > 0 {
		r.collisions--
		return GroupInvite{}, ErrGroupInviteTaken
	}
	r.nextID++
	invite.ID = r.nextID
	r.invites[invite.ID] = invite
	return invite, nil
}

func (r *inviteRepositoryStub) ListInvites(_ context.Context, groupID int64) ([]GroupInviteListing, error) {
	listings := make([]GroupInviteListing, 0, len(r.invites))
	for id := int64(0); id <= r.nextID+100; id++ {
		if invite, ok := r.invites[id]; ok && invite.GroupID == groupID {
			listings = append(listings, GroupInviteListing{Invite: invite})
		}
	}
	return listings, nil
}

func (r *inviteRepositoryStub) FindInvite(_ context.Context, groupID, inviteID int64) (GroupInvite, error) {
	invite, ok := r.invites[inviteID]
	if !ok || invite.GroupID != groupID {
		return GroupInvite{}, ErrGroupInviteNotFound
	}
	return invite, nil
}

func (r *inviteRepositoryStub) FindInviteByCode(_ context.Context, code string) (GroupInvite, error) {
	for _, invite := range r.invites {
		if invite.Code == code {
			return invite, nil
		}
	}
	return GroupInvite{}, ErrGroupInviteNotFound
}

func (r *inviteRepositoryStub) RevokeInvite(_ context.Context, groupID, inviteID, revokedAt int64) (GroupInvite, error) {
	invite, ok := r.invites[inviteID]
	if !ok || invite.GroupID != groupID {
		return GroupInvite{}, ErrGroupInviteNotFound
	}
	if invite.RevokedAt == nil {
		invite.RevokedAt = &revokedAt
	}
	r.invites[inviteID] = invite
	return invite, nil
}

func (r *inviteRepositoryStub) RedeemInvite(_ context.Context, inviteID, userID, now int64) (InviteRedemption, error) {
	if r.redeemErr != nil {
		return InviteRedemption{}, r.redeemErr
	}
	invite := r.invites[inviteID]
	if err := invite.Status(now).err(); err != nil {
		return InviteRedemption{}, err
	}
	if r.changed {
		invite.Uses++
		r.invites[inviteID] = invite
	}
	r.redeemed = append(r.redeemed, userID)
	return InviteRedemption{Membership: GroupMembership{GroupID: invite.GroupID, UserID: userID, Roles: []string{invite.Role}}, TeamID: invite.Team(), Changed: r.changed}, nil
}

const inviteTestNow = int64(1_000_000)

func newTestInviteService(t *testing.T, invites *inviteRepositoryStub, users *groupUserRepositoryStub) (*InviteService, *auditRecorderStub) {
	t.Helper()
	team := Team{ID: 4, GroupID: 5, Name: "Lions"}
	other := Team{ID: 6, GroupID: 5, Name: "Tigers"}
	service, err := NewInviteService(invites, newTeamStub(team, other), users)
	if err != nil {
		t.Fatalf("NewInviteService() error = %v", err)
	}
	service.now = func() time.Time { return time.Unix(inviteTestNow, 0) }
	auditor := &auditRecorderStub{}
	service.SetAuditor(auditor)
	return service, auditor
}

func intPtr(value int) *int       { return &value }
func int64Ptr(value int64) *int64 { return &value }

func TestInviteServiceCreateRoleLimits(t *testing.T) {
	coach := GroupAccessGrants{Member: true, Roles: []string{GroupRoleCoach}, CoachedTeamIDs: []int64{4}}
	tests := []struct {
		name    string
		grants  GroupAccessGrants
		input   InviteInput
		wantErr error
	}{
		{name: "admin invites an admin", grants: memberWith(GroupRoleAdmin), input: InviteInput{Role: GroupRoleAdmin}},
		{name: "site administrator invites an admin", grants: GroupAccessGrants{SiteAdmin: true}, input: InviteInput{Role: GroupRoleAdmin}},
		{name: "manager may not invite an admin", grants: memberWith(GroupRoleManager), input: InviteInput{Role: GroupRoleAdmin}, wantErr: ErrGroupForbidden},
		{name: "manager invites a manager", grants: memberWith(GroupRoleManager), input: InviteInput{Role: GroupRoleManager}},
		{name: "manager invites a coach", grants: memberWith(GroupRoleManager), input: InviteInput{Role: GroupRoleCoach}},
		{name: "manager invites a player to a team", grants: memberWith(GroupRoleManager), input: InviteInput{Role: GroupRolePlayer, TeamID: 4}},
		{name: "coach invites a player to a coached team", grants: coach, input: InviteInput{Role: GroupRolePlayer, TeamID: 4}},
		{name: "coach invites a parent to a coached team", grants: coach, input: InviteInput{Role: GroupRoleParent, TeamID: 4}},
		{name: "coach may not invite to another team", grants: GroupAccessGrants{Member: true, Roles: []string{GroupRoleCoach}, CoachedTeamIDs: []int64{4}, OwnTeams: []TeamRole{{TeamID: 6, Role: GroupRolePlayer}}}, input: InviteInput{Role: GroupRolePlayer, TeamID: 6}, wantErr: ErrGroupForbidden},
		{name: "coach may not invite to an invisible team", grants: coach, input: InviteInput{Role: GroupRolePlayer, TeamID: 6}, wantErr: ErrTeamNotFound},
		{name: "coach may not invite without a team", grants: coach, input: InviteInput{Role: GroupRolePlayer}, wantErr: ErrGroupForbidden},
		{name: "coach may not invite a coach", grants: coach, input: InviteInput{Role: GroupRoleCoach}, wantErr: ErrGroupForbidden},
		{name: "coach without coached teams may not invite", grants: memberWith(GroupRoleCoach), input: InviteInput{Role: GroupRolePlayer, TeamID: 4}, wantErr: ErrTeamNotFound},
		{name: "player may not invite", grants: memberWith(GroupRolePlayer), input: InviteInput{Role: GroupRolePlayer}, wantErr: ErrGroupForbidden},
		{name: "parent may not invite", grants: memberWith(GroupRoleParent), input: InviteInput{Role: GroupRoleParent}, wantErr: ErrGroupForbidden},
		{name: "outsider gets not found", grants: GroupAccessGrants{}, input: InviteInput{Role: GroupRolePlayer}, wantErr: ErrGroupNotFound},
		{name: "unknown role", grants: memberWith(GroupRoleAdmin), input: InviteInput{Role: "owner"}, wantErr: ErrInvalidGroupRole},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			invites := newInviteStub()
			service, auditor := newTestInviteService(t, invites, usersWith(User{ID: 7}))
			invite, err := service.Create(context.Background(), actorWith(test.grants), test.input)
			if test.wantErr != nil {
				if !errors.Is(err, test.wantErr) {
					t.Fatalf("Create() error = %v, want %v", err, test.wantErr)
				}
				if len(invites.invites) != 0 || len(auditor.records) != 0 {
					t.Fatal("refused invite was stored or audited")
				}
				return
			}
			if err != nil {
				t.Fatalf("Create() error = %v", err)
			}
			if invite.Role != test.input.Role || invite.Team() != test.input.TeamID || len(invite.Code) != groupInviteCodeLength || invite.GroupID != 5 {
				t.Fatalf("invite = %+v", invite)
			}
			if len(auditor.records) != 1 || auditor.records[0].Action != AuditActionInviteCreated || strings.Contains(auditor.records[0].Detail, invite.Code) {
				t.Fatalf("audit = %+v, want one record without the code", auditor.records)
			}
			if test.grants.SiteAdmin && !strings.Contains(auditor.records[0].Detail, "site administrator") {
				t.Fatal("site administrator invite not marked in the audit trail")
			}
		})
	}
}

func TestInviteServiceCreateLimits(t *testing.T) {
	admin := adminActor()
	tests := []struct {
		name    string
		input   InviteInput
		wantErr error
		check   func(*testing.T, GroupInvite)
	}{
		{name: "team only for roster roles", input: InviteInput{Role: GroupRoleCoach, TeamID: 4}, wantErr: ErrInvalidInvite},
		{name: "unknown team", input: InviteInput{Role: GroupRolePlayer, TeamID: 99}, wantErr: ErrTeamNotFound},
		{name: "negative expiry", input: InviteInput{Role: GroupRolePlayer, ExpiresInDays: -1}, wantErr: ErrInvalidInvite},
		{name: "expiry beyond a year", input: InviteInput{Role: GroupRolePlayer, ExpiresInDays: MaxInviteDays + 1}, wantErr: ErrInvalidInvite},
		{name: "negative use limit", input: InviteInput{Role: GroupRolePlayer, MaxUses: -1}, wantErr: ErrInvalidInvite},
		{name: "use limit too high", input: InviteInput{Role: GroupRolePlayer, MaxUses: MaxInviteUses + 1}, wantErr: ErrInvalidInvite},
		{name: "expiry and use limit", input: InviteInput{Role: GroupRolePlayer, TeamID: 4, ExpiresInDays: 7, MaxUses: 3}, check: func(t *testing.T, invite GroupInvite) {
			if invite.ExpiresAt == nil || *invite.ExpiresAt != inviteTestNow+7*24*60*60 || invite.MaxUses == nil || *invite.MaxUses != 3 || invite.CreatedBy == nil || *invite.CreatedBy != 7 {
				t.Fatalf("invite = %+v", invite)
			}
		}},
		{name: "no limits", input: InviteInput{Role: GroupRoleParent}, check: func(t *testing.T, invite GroupInvite) {
			if invite.ExpiresAt != nil || invite.MaxUses != nil || invite.TeamID != nil {
				t.Fatalf("invite = %+v, want no limits", invite)
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service, _ := newTestInviteService(t, newInviteStub(), usersWith(User{ID: 7}))
			invite, err := service.Create(context.Background(), admin, test.input)
			if test.wantErr != nil {
				if !errors.Is(err, test.wantErr) {
					t.Fatalf("Create() error = %v, want %v", err, test.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Create() error = %v", err)
			}
			test.check(t, invite)
		})
	}
}

func TestInviteServiceCreateRetriesCodeCollisions(t *testing.T) {
	invites := newInviteStub()
	invites.collisions = 2
	service, _ := newTestInviteService(t, invites, usersWith(User{ID: 7}))
	if _, err := service.Create(context.Background(), adminActor(), InviteInput{Role: GroupRolePlayer}); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	invites.collisions = groupInviteCollisionTries
	if _, err := service.Create(context.Background(), adminActor(), InviteInput{Role: GroupRolePlayer}); !errors.Is(err, ErrGroupInviteTaken) {
		t.Fatalf("Create() error = %v, want ErrGroupInviteTaken after every retry", err)
	}
	service.newCode = func() (string, error) { return "", ErrGroupInviteGeneration }
	if _, err := service.Create(context.Background(), adminActor(), InviteInput{Role: GroupRolePlayer}); !errors.Is(err, ErrGroupInviteGeneration) {
		t.Fatalf("Create() error = %v, want ErrGroupInviteGeneration", err)
	}
}

func TestInviteServiceJoinRules(t *testing.T) {
	past := inviteTestNow - 1
	future := inviteTestNow + 100
	revokedAt := inviteTestNow - 10
	invites := []GroupInvite{
		{ID: 1, GroupID: 5, Code: "PLAYERCODE22", Role: GroupRolePlayer, TeamID: int64Ptr(4)},
		{ID: 2, GroupID: 5, Code: "COACHCODE222", Role: GroupRoleCoach},
		{ID: 3, GroupID: 5, Code: "EXPIREDCODE2", Role: GroupRolePlayer, ExpiresAt: &past},
		{ID: 4, GroupID: 5, Code: "USEDUPCODE22", Role: GroupRolePlayer, MaxUses: intPtr(2), Uses: 2},
		{ID: 5, GroupID: 5, Code: "REVOKEDCODE2", Role: GroupRolePlayer, RevokedAt: &revokedAt},
		{ID: 6, GroupID: 5, Code: "PARENTCODE22", Role: GroupRoleParent, ExpiresAt: &future, MaxUses: intPtr(1)},
		{ID: 7, GroupID: 5, Code: "ADMINCODE222", Role: GroupRoleAdmin},
	}
	users := usersWith(User{ID: 7}, User{ID: 8, Kind: UserKindGuest}, User{ID: 9, Kind: UserKindProfile})
	tests := []struct {
		name    string
		userID  int64
		code    string
		wantErr error
		wantTo  int64
	}{
		{name: "guest joins as player onto the team", userID: 8, code: " playercode22 ", wantTo: 4},
		{name: "guest joins as parent", userID: 8, code: "PARENTCODE22"},
		{name: "account joins as coach", userID: 7, code: "COACHCODE222"},
		{name: "account joins as admin", userID: 7, code: "ADMINCODE222"},
		{name: "guest may not join as coach", userID: 8, code: "COACHCODE222", wantErr: ErrRoleNeedsAccount},
		{name: "guest may not join as admin", userID: 8, code: "ADMINCODE222", wantErr: ErrRoleNeedsAccount},
		{name: "profile never joins", userID: 9, code: "PLAYERCODE22", wantErr: ErrProfileCannotSignIn},
		{name: "expired code", userID: 7, code: "EXPIREDCODE2", wantErr: ErrInviteExpired},
		{name: "used up code", userID: 7, code: "USEDUPCODE22", wantErr: ErrInviteUsedUp},
		{name: "revoked code", userID: 7, code: "REVOKEDCODE2", wantErr: ErrInviteRevoked},
		{name: "unknown code", userID: 7, code: "UNKNOWN00000", wantErr: ErrGroupInviteNotFound},
		{name: "empty code", userID: 7, code: "   ", wantErr: ErrGroupInviteNotFound},
		{name: "oversized code", userID: 7, code: strings.Repeat("A", 65), wantErr: ErrGroupInviteNotFound},
		{name: "invalid account", userID: 0, code: "PLAYERCODE22", wantErr: ErrInvalidUserID},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repository := newInviteStub(invites...)
			service, _ := newTestInviteService(t, repository, users)
			redemption, err := service.Join(context.Background(), test.userID, test.code)
			if test.wantErr != nil {
				if !errors.Is(err, test.wantErr) {
					t.Fatalf("Join() error = %v, want %v", err, test.wantErr)
				}
				if len(repository.redeemed) != 0 {
					t.Fatal("refused join reached the store")
				}
				return
			}
			if err != nil {
				t.Fatalf("Join() error = %v", err)
			}
			if redemption.Membership.GroupID != 5 || redemption.TeamID != test.wantTo || len(repository.redeemed) != 1 {
				t.Fatalf("Join() = %+v", redemption)
			}
		})
	}
}

func TestInviteServiceJoinUseLimitAndRaces(t *testing.T) {
	repository := newInviteStub(GroupInvite{ID: 1, GroupID: 5, Code: "ONEUSECODE22", Role: GroupRolePlayer, MaxUses: intPtr(1)})
	service, _ := newTestInviteService(t, repository, usersWith(User{ID: 7}, User{ID: 8}))
	if _, err := service.Join(context.Background(), 7, "ONEUSECODE22"); err != nil {
		t.Fatalf("first Join() error = %v", err)
	}
	if _, err := service.Join(context.Background(), 8, "ONEUSECODE22"); !errors.Is(err, ErrInviteUsedUp) {
		t.Fatalf("second Join() error = %v, want ErrInviteUsedUp", err)
	}
	// The store's own re-check wins over a stale read.
	for _, storeErr := range []error{ErrInviteUsedUp, ErrInviteRevoked, ErrInviteExpired} {
		repository := newInviteStub(GroupInvite{ID: 1, GroupID: 5, Code: "RACECODE2222", Role: GroupRolePlayer})
		repository.redeemErr = storeErr
		service, _ := newTestInviteService(t, repository, usersWith(User{ID: 7}))
		if _, err := service.Join(context.Background(), 7, "RACECODE2222"); !errors.Is(err, storeErr) {
			t.Fatalf("Join() error = %v, want %v", err, storeErr)
		}
	}
}

func TestInviteServiceRevoke(t *testing.T) {
	repository := newInviteStub(
		GroupInvite{ID: 1, GroupID: 5, Code: "TEAMFOURCODE", Role: GroupRolePlayer, TeamID: int64Ptr(4)},
		GroupInvite{ID: 2, GroupID: 5, Code: "TEAMSIXCODE2", Role: GroupRolePlayer, TeamID: int64Ptr(6)},
		GroupInvite{ID: 3, GroupID: 5, Code: "ADMINCODE222", Role: GroupRoleAdmin},
		GroupInvite{ID: 4, GroupID: 9, Code: "OTHERGROUP22", Role: GroupRolePlayer},
	)
	service, auditor := newTestInviteService(t, repository, usersWith(User{ID: 7}))
	coach := coachOf(4)

	if _, err := service.Revoke(context.Background(), coach, 2); !errors.Is(err, ErrGroupInviteNotFound) {
		t.Fatalf("coach revoking another team's invite error = %v, want not found", err)
	}
	if _, err := service.Revoke(context.Background(), actorWith(memberWith(GroupRoleManager)), 3); !errors.Is(err, ErrGroupInviteNotFound) {
		t.Fatalf("manager revoking an admin invite error = %v, want not found", err)
	}
	if _, err := service.Revoke(context.Background(), adminActor(), 4); !errors.Is(err, ErrGroupInviteNotFound) {
		t.Fatalf("revoking another group's invite error = %v, want not found", err)
	}
	if _, err := service.Revoke(context.Background(), actorWith(memberWith(GroupRolePlayer)), 1); !errors.Is(err, ErrGroupForbidden) {
		t.Fatalf("player revoke error = %v, want forbidden", err)
	}
	revoked, err := service.Revoke(context.Background(), coach, 1)
	if err != nil || revoked.RevokedAt == nil || *revoked.RevokedAt != inviteTestNow {
		t.Fatalf("Revoke() = %+v, %v", revoked, err)
	}
	if _, err := service.Revoke(context.Background(), coach, 1); err != nil {
		t.Fatalf("second Revoke() error = %v, want idempotent", err)
	}
	if len(auditor.records) != 1 || auditor.records[0].Action != AuditActionInviteRevoked || strings.Contains(auditor.records[0].Detail, "TEAMFOURCODE") {
		t.Fatalf("audit = %+v, want one revocation without the code", auditor.records)
	}
	if _, err := service.Join(context.Background(), 7, "TEAMFOURCODE"); !errors.Is(err, ErrInviteRevoked) {
		t.Fatalf("Join(revoked) error = %v", err)
	}
}

func TestInviteServiceListFiltersByRole(t *testing.T) {
	repository := newInviteStub(
		GroupInvite{ID: 1, GroupID: 5, Code: "TEAMFOURCODE", Role: GroupRolePlayer, TeamID: int64Ptr(4)},
		GroupInvite{ID: 2, GroupID: 5, Code: "TEAMSIXCODE2", Role: GroupRolePlayer, TeamID: int64Ptr(6)},
		GroupInvite{ID: 3, GroupID: 5, Code: "ADMINCODE222", Role: GroupRoleAdmin},
		GroupInvite{ID: 4, GroupID: 5, Code: "PLAYERCODE22", Role: GroupRolePlayer},
	)
	service, _ := newTestInviteService(t, repository, usersWith(User{ID: 7}))
	for _, test := range []struct {
		name  string
		actor GroupActor
		want  int
	}{
		{"admin sees all", adminActor(), 4},
		{"manager sees all but admin invites", actorWith(memberWith(GroupRoleManager)), 3},
		{"coach sees their team's invites", coachOf(4), 1},
	} {
		listings, err := service.List(context.Background(), test.actor.Access)
		if err != nil || len(listings) != test.want {
			t.Fatalf("%s: List() = %d, %v; want %d", test.name, len(listings), err, test.want)
		}
	}
	if _, err := service.List(context.Background(), actorWith(memberWith(GroupRoleParent)).Access); !errors.Is(err, ErrGroupForbidden) {
		t.Fatalf("parent List() error = %v, want forbidden", err)
	}
	if _, err := service.List(context.Background(), nil); !errors.Is(err, ErrGroupNotFound) {
		t.Fatalf("outsider List() error = %v, want not found", err)
	}
}

func TestInviteStatus(t *testing.T) {
	revoked := int64(5)
	expires := int64(10)
	for _, test := range []struct {
		invite GroupInvite
		now    int64
		want   InviteStatus
	}{
		{GroupInvite{}, 1, InviteActive},
		{GroupInvite{ExpiresAt: &expires}, 9, InviteActive},
		{GroupInvite{ExpiresAt: &expires}, 10, InviteExpired},
		{GroupInvite{MaxUses: intPtr(2), Uses: 1}, 1, InviteActive},
		{GroupInvite{MaxUses: intPtr(2), Uses: 2}, 1, InviteUsedUp},
		{GroupInvite{RevokedAt: &revoked, ExpiresAt: &expires, MaxUses: intPtr(1), Uses: 1}, 20, InviteRevoked},
	} {
		if got := test.invite.Status(test.now); got != test.want {
			t.Errorf("Status(%+v, %d) = %s, want %s", test.invite, test.now, got, test.want)
		}
	}
}

func TestNewInviteServiceRejectsNilDependencies(t *testing.T) {
	if _, err := NewInviteService(nil, newTeamStub(), usersWith()); !errors.Is(err, ErrNilInviteRepository) {
		t.Fatalf("nil invites error = %v", err)
	}
	if _, err := NewInviteService(newInviteStub(), nil, usersWith()); !errors.Is(err, ErrNilTeamRepository) {
		t.Fatalf("nil teams error = %v", err)
	}
	if _, err := NewInviteService(newInviteStub(), newTeamStub(), nil); !errors.Is(err, ErrNilUserRepository) {
		t.Fatalf("nil users error = %v", err)
	}
}
