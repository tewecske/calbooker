package httpadapter

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/tewecske/calbooker/internal/service"
)

func TestCallUpRouteAndWarning(t *testing.T) {
	dependencies, _, participation := participationDependencies(t, groupMemberAs(service.GroupRoleAdmin))
	response := serveGroups(t, dependencies, http.MethodPost, "/en/groups/lions/events/5/call-up", "user_id=21", true)
	body := response.Body.String()
	if response.Code != http.StatusOK || !strings.Contains(body, "Player called up.") || participation.lastEvent != 5 || participation.lastUser != 21 || participation.lastStatus != "call-up" {
		t.Fatalf("call-up = %d, args %d %d %q", response.Code, participation.lastEvent, participation.lastUser, participation.lastStatus)
	}
	if strings.Contains(body, "same time") {
		t.Fatal("no overlap, no warning")
	}

	participation.overlaps = []service.OverlapEvent{{EventID: 9, Title: "Away <b>game</b>", StartsAt: time.Now(), EndsAt: time.Now()}}
	body = serveGroups(t, dependencies, http.MethodPost, "/en/groups/lions/events/5/call-up", "user_id=21", true).Body.String()
	if !strings.Contains(body, "Player called up.") || !strings.Contains(body, "other events at the same time: Away &lt;b&gt;game&lt;/b&gt;") {
		t.Fatalf("overlap warning missing or unescaped: %s", body)
	}

	calls := participation.calls
	if response := serveGroups(t, dependencies, http.MethodPost, "/en/groups/lions/events/5/call-up", "user_id=abc", true); response.Code != http.StatusUnprocessableEntity || participation.lastStatus != "call-up" || participation.calls != calls+1 {
		t.Fatalf("bad user = %d", response.Code)
	}
	for _, test := range []struct {
		err  error
		code int
	}{
		{service.ErrGroupForbidden, http.StatusForbidden},
		{service.ErrParticipantNotEligible, http.StatusUnprocessableEntity},
		{service.ErrParticipationStarted, http.StatusUnprocessableEntity},
		{service.ErrEventNotFound, http.StatusNotFound},
	} {
		participation.writeErr = test.err
		if response := serveGroups(t, dependencies, http.MethodPost, "/en/groups/lions/events/5/call-up", "user_id=21", false); response.Code != test.code {
			t.Errorf("error %v = %d, want %d", test.err, response.Code, test.code)
		}
	}
}

func TestEventPageOffersCallUpsToCoaches(t *testing.T) {
	dependencies, _, participation := participationDependencies(t, groupMemberAs(service.GroupRoleAdmin))
	participation.roster.CanManage = true
	participation.roster.CallUps = []service.EventPlayer{{UserID: 31, DisplayName: eventStr("Cora Cross")}}
	participation.roster.Entries = append(participation.roster.Entries,
		service.ParticipationEntry{Player: service.EventPlayer{UserID: 32, DisplayName: eventStr("Cal Called")}, CalledUp: true, Status: service.ParticipantNominated, Choices: []string{service.ParticipantDeclined}, CanRemove: true},
		service.ParticipationEntry{Player: service.EventPlayer{UserID: 33, DisplayName: eventStr("Lou Left")}, OnEventTeam: true, Status: service.ParticipantLeftOut, Choices: []string{service.ParticipantSignedUp}},
	)
	body := serveGroups(t, dependencies, http.MethodGet, "/en/groups/lions/events/5", "", false).Body.String()
	for _, want := range []string{
		`data-testid="call-up-form"`, `action="/en/groups/lions/events/5/call-up"`, `<option value="31">Cora Cross</option>`,
		`data-testid="participant-called-up"`, "Called up", "Withdraw call-up – Cal Called", "Left out", `data-status="left_out"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("coach event page missing %q", want)
		}
	}

	participation.roster.CallUps = nil
	if body := serveGroups(t, dependencies, http.MethodGet, "/en/groups/lions/events/5", "", false).Body.String(); strings.Contains(body, `data-testid="call-up-form"`) {
		t.Fatal("no candidates, no call-up form")
	}
	hu := serveGroups(t, dependencies, http.MethodGet, "/hu/groups/lions/events/5", "", false).Body.String()
	if !strings.Contains(hu, "Beosztva") || !strings.Contains(hu, "Kihagyva") {
		t.Fatal("Hungarian call-up labels missing")
	}
}

func TestTeamPageInviteCard(t *testing.T) {
	maxUses := 5
	teamID := int64(4)
	listings := []service.GroupInviteListing{
		{Invite: service.GroupInvite{ID: 1, Code: "TEAMCODE2345", Role: service.GroupRolePlayer, TeamID: &teamID, MaxUses: &maxUses, Uses: 1}},
		{Invite: service.GroupInvite{ID: 2, Code: "OTHERTEAM234", Role: service.GroupRolePlayer, TeamID: new(int64)}},
		{Invite: service.GroupInvite{ID: 3, Code: "GROUPONLY234", Role: service.GroupRolePlayer}},
	}
	coach := groupAccessAs(service.GroupAccessGrants{Member: true, Roles: []string{service.GroupRoleCoach}, CoachedTeamIDs: []int64{4}})
	otherCoach := groupAccessAs(service.GroupAccessGrants{Member: true, Roles: []string{service.GroupRoleCoach}, CoachedTeamIDs: []int64{9}, OwnTeams: []service.TeamRole{{TeamID: 4, Role: service.GroupRolePlayer}}})
	player := playerOfTeam(4)
	for _, test := range []struct {
		name   string
		access *service.GroupAccess
		want   bool
	}{
		{name: "manager", access: groupMemberAs(service.GroupRoleManager), want: true},
		{name: "coach of the team", access: coach, want: true},
		{name: "coach of another team", access: otherCoach},
		{name: "player", access: player},
	} {
		t.Run(test.name, func(t *testing.T) {
			dependencies, _, teams := groupTestDependencies(t, test.access)
			teams.detail = service.TeamDetail{Team: service.Team{ID: 4, Slug: "t4", GroupID: 3, Name: "Lions", Version: 1}}
			invites := &invitesUseCaseStub{now: 100, listings: listings}
			dependencies.Invites = invites
			roster := serveGroups(t, dependencies, http.MethodGet, "/en/groups/lions/teams/t4", "", false).Body.String()
			if got := strings.Contains(roster, `data-team-tab="invites"`); got != test.want {
				t.Fatalf("invites tab shown = %v, want %v", got, test.want)
			}
			page := serveGroups(t, dependencies, http.MethodGet, "/en/groups/lions/teams/t4/invites", "", false)
			body := page.Body.String()
			if !test.want {
				if page.Code != http.StatusForbidden || invites.calls != 0 || strings.Contains(body, "TEAMCODE2345") {
					t.Fatalf("%s: status %d, invites loaded (%d calls) or leaked", test.name, page.Code, invites.calls)
				}
				return
			}
			if !strings.Contains(body, `data-testid="team-invites"`) {
				t.Fatal("invite card missing on the Invites tab")
			}
			for _, want := range []string{"TEAMCODE2345", "1 / 5", `action="/en/groups/lions/teams/t4/invites"`, `action="/en/groups/lions/teams/t4/invites/1/revoke"`, `<option value="player"`, `<option value="parent"`} {
				if !strings.Contains(body, want) {
					t.Errorf("invite card missing %q", want)
				}
			}
			if strings.Contains(body, "OTHERTEAM234") || strings.Contains(body, "GROUPONLY234") || strings.Contains(body, `<option value="coach"`) {
				t.Error("card shows other teams' invites or offers a non-roster role")
			}
		})
	}
}

func TestTeamInviteWrites(t *testing.T) {
	coach := groupAccessAs(service.GroupAccessGrants{Member: true, Roles: []string{service.GroupRoleCoach}, CoachedTeamIDs: []int64{4}})
	dependencies, _, teams := groupTestDependencies(t, coach)
	teams.detail = service.TeamDetail{Team: service.Team{ID: 4, Slug: "t4", GroupID: 3, Name: "Lions", Version: 1}}
	invites := &invitesUseCaseStub{now: 100}
	dependencies.Invites = invites

	created := serveGroups(t, dependencies, http.MethodPost, "/en/groups/lions/teams/t4/invites", "role=parent&expires_in=7&max_uses=3&team_id=99", true)
	body := created.Body.String()
	if created.Code != http.StatusOK || !strings.Contains(body, `data-testid="invite-created-code"`) || !strings.Contains(body, "NEWCODE23456") {
		t.Fatalf("create = %d", created.Code)
	}
	// The team comes from the URL, never from the form.
	if invites.lastInput != (service.InviteInput{Role: service.GroupRoleParent, TeamID: 4, ExpiresInDays: 7, MaxUses: 3}) {
		t.Fatalf("input = %+v", invites.lastInput)
	}

	invites.writeErr = service.ErrInvalidInvite
	invalid := serveGroups(t, dependencies, http.MethodPost, "/en/groups/lions/teams/t4/invites", "role=player", true)
	if invalid.Code != http.StatusUnprocessableEntity || !strings.Contains(invalid.Body.String(), `data-testid="team-invites"`) {
		t.Fatalf("invalid create = %d", invalid.Code)
	}
	invites.writeErr = service.ErrGroupForbidden
	if response := serveGroups(t, dependencies, http.MethodPost, "/en/groups/lions/teams/t4/invites", "role=player", false); response.Code != http.StatusForbidden {
		t.Fatalf("forbidden create = %d, want 403", response.Code)
	}
	invites.writeErr = nil
	if response := serveGroups(t, dependencies, http.MethodPost, "/en/groups/lions/teams/t4/invites", "role=player&max_uses=abc", true); response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("malformed use limit = %d, want 422", response.Code)
	}

	revoked := serveGroups(t, dependencies, http.MethodPost, "/en/groups/lions/teams/t4/invites/6/revoke", "", true)
	if revoked.Code != http.StatusOK || invites.lastRevoked != 6 || !strings.Contains(revoked.Body.String(), "Invite revoked.") {
		t.Fatalf("revoke = %d (revoked %d)", revoked.Code, invites.lastRevoked)
	}
	if response := serveGroups(t, dependencies, http.MethodPost, "/en/groups/lions/teams/t4/invites/abc/revoke", "", true); response.Code != http.StatusNotFound {
		t.Fatalf("bad invite id = %d, want 404", response.Code)
	}
}
