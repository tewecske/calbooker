package service

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestSlugify(t *testing.T) {
	tests := map[string]string{
		"BVSC":                       "bvsc",
		"Füzy":                       "fuzy",
		"  Őrség Úttörő  SE ":        "orseg-uttoro-se",
		"U10 (fiúk) / A":             "u10-fiuk-a",
		"Straße":                     "strasse",
		"---":                        "",
		"":                           "",
		strings.Repeat("a", 80):      strings.Repeat("a", slugBaseLength),
		strings.Repeat("ab ", 30):    strings.Repeat("ab-", 16)[:47],
		"Crème brûlée Fan Club 2026": "creme-brulee-fan-club-2026",
	}
	for name, want := range tests {
		if got := Slugify(name); got != want {
			t.Errorf("Slugify(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestValidateSlug(t *testing.T) {
	for _, valid := range []string{"bvsc", "u10", "a-b-c", "club-2", strings.Repeat("a", maxSlugLength)} {
		if err := ValidateSlug(valid); err != nil {
			t.Errorf("ValidateSlug(%q) = %v", valid, err)
		}
	}
	invalid := []string{"", "123", "Upper", "a b", "-a", "a-", "a--b", "ő", "new", "edit", "settings", "members", "join", strings.Repeat("a", maxSlugLength+1)}
	for _, slug := range invalid {
		if err := ValidateSlug(slug); !errors.Is(err, ErrInvalidSlug) {
			t.Errorf("ValidateSlug(%q) = %v, want ErrInvalidSlug", slug, err)
		}
	}
}

func TestUniqueSlugFallsBackAndNumbers(t *testing.T) {
	used := map[string]bool{"club": true, "club-2": true}
	taken := func(slug string) (bool, error) { return used[slug], nil }
	tests := []struct{ name, label, want string }{
		{"Club", "group", "club-3"},
		{"Other", "group", "other"},
		{"???", "group", "group"},
		{"2026", "team", "team-2026"},
		{"Settings", "group", "group-settings"},
	}
	for _, test := range tests {
		got, err := uniqueSlug(test.name, test.label, taken)
		if err != nil || got != test.want {
			t.Errorf("uniqueSlug(%q) = %q, %v, want %q", test.name, got, err, test.want)
		}
	}
	boom := errors.New("boom")
	if _, err := uniqueSlug("x", "group", func(string) (bool, error) { return false, boom }); !errors.Is(err, boom) {
		t.Fatalf("error = %v, want boom", err)
	}
}

func TestGroupServiceCreateDerivesUniqueSlug(t *testing.T) {
	user := User{ID: 7}
	groups := &groupRepositoryStub{takenSlugs: map[string]bool{"fuzy": true}}
	service, _ := newTestGroupService(t, groups, newMembershipStub(), usersWith(user))
	summary, err := service.Create(context.Background(), user.ID, "192.0.2.1", "Füzy", "")
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if summary.Group.Slug != "fuzy-2" {
		t.Fatalf("slug = %q, want fuzy-2", summary.Group.Slug)
	}
}

func TestGroupServiceCreateRetriesARacedSlug(t *testing.T) {
	user := User{ID: 7}
	memberships := newMembershipStub()
	memberships.createErrs = []error{ErrSlugTaken}
	service, _ := newTestGroupService(t, &groupRepositoryStub{}, memberships, usersWith(user))
	summary, err := service.Create(context.Background(), user.ID, "192.0.2.1", "Club", "")
	if err != nil || summary.Group.Slug != "club" || memberships.createCalls != 2 {
		t.Fatalf("Create() = %+v, %v after %d attempts", summary.Group, err, memberships.createCalls)
	}
	memberships = newMembershipStub()
	memberships.createErrs = []error{ErrSlugTaken, ErrSlugTaken, ErrSlugTaken, ErrSlugTaken}
	service, _ = newTestGroupService(t, &groupRepositoryStub{}, memberships, usersWith(user))
	if _, err := service.Create(context.Background(), user.ID, "192.0.2.1", "Club", ""); !errors.Is(err, ErrSlugTaken) {
		t.Fatalf("error = %v, want ErrSlugTaken after exhausting retries", err)
	}
}

func TestGroupServiceBySlug(t *testing.T) {
	groups := &groupRepositoryStub{group: Group{ID: 5, Name: "Club", Slug: "club"}}
	service, _ := newTestGroupService(t, groups, newMembershipStub(), usersWith(User{ID: 7}))
	if group, err := service.BySlug(context.Background(), "club"); err != nil || group.ID != 5 {
		t.Fatalf("BySlug(club) = %+v, %v", group, err)
	}
	for _, slug := range []string{"other", "5", "", "Club", "new"} {
		if _, err := service.BySlug(context.Background(), slug); !errors.Is(err, ErrGroupNotFound) {
			t.Errorf("BySlug(%q) error = %v, want ErrGroupNotFound", slug, err)
		}
	}
}

func TestTeamServiceSlugs(t *testing.T) {
	existing := Team{ID: 1, GroupID: 5, Name: "U10", Slug: "u10", BirthYearFrom: 2016, BirthYearTo: 2017, Version: 1}
	teams := newTeamStub(existing, Team{ID: 2, GroupID: 5, Name: "U13", Slug: "u13"}, Team{ID: 3, GroupID: 6, Name: "Other", Slug: "u10"})
	service, _ := newTestTeamService(t, teams, newMembershipStub())

	created, err := service.Create(context.Background(), adminActor(), TeamInput{Name: "U10", BirthYearFrom: 2016, BirthYearTo: 2017})
	if err != nil || created.Slug != "u10-2" {
		t.Fatalf("Create() = %+v, %v, want slug u10-2 next to the clash", created, err)
	}
	if _, err := service.Create(context.Background(), adminActor(), TeamInput{Name: "X", Slug: "u13", BirthYearFrom: 2016, BirthYearTo: 2017}); !errors.Is(err, ErrSlugTaken) {
		t.Fatalf("Create() with a taken slug error = %v, want ErrSlugTaken", err)
	}
	if _, err := service.Create(context.Background(), adminActor(), TeamInput{Name: "X", Slug: "edit", BirthYearFrom: 2016, BirthYearTo: 2017}); !errors.Is(err, ErrInvalidSlug) {
		t.Fatalf("Create() with a reserved slug error = %v, want ErrInvalidSlug", err)
	}

	input := TeamInput{Name: "U10 Blue", Slug: " U10-Blue ", BirthYearFrom: 2016, BirthYearTo: 2017}
	updated, err := service.Update(context.Background(), adminActor(), 1, 1, input)
	if err != nil || updated.Slug != "u10-blue" {
		t.Fatalf("Update() = %+v, %v, want slug u10-blue", updated, err)
	}
	input.Slug = "u13"
	if _, err := service.Update(context.Background(), adminActor(), 1, 1, input); !errors.Is(err, ErrSlugTaken) {
		t.Fatalf("Update() onto another team's slug error = %v, want ErrSlugTaken", err)
	}
	input.Slug = "u10"
	if updated, err := service.Update(context.Background(), adminActor(), 1, 1, input); err != nil || updated.Slug != "u10" {
		t.Fatalf("Update() keeping its own slug = %+v, %v", updated, err)
	}
	input.Slug = ""
	if updated, err := service.Update(context.Background(), adminActor(), 1, 1, input); err != nil || updated.Slug != "u10" {
		t.Fatalf("Update() with an empty slug = %+v, %v, want the slug kept", updated, err)
	}
}

func TestTeamServiceForEvent(t *testing.T) {
	teams := newTeamStub(Team{ID: 1, GroupID: 5, Name: "U10", Slug: "u10"}, Team{ID: 2, GroupID: 5, Name: "U13", Slug: "u13"})
	service, _ := newTestTeamService(t, teams, newMembershipStub())
	event := Event{ID: 9, GroupID: 5, Teams: []EventTeam{{ID: 1, Name: "U10"}}}
	admin := adminActor().Access

	if team, err := service.ForEvent(context.Background(), admin, "u10", event); err != nil || team.ID != 1 {
		t.Fatalf("ForEvent(u10) = %+v, %v", team, err)
	}
	// The event must belong to the team.
	if _, err := service.ForEvent(context.Background(), admin, "u13", event); !errors.Is(err, ErrTeamNotFound) {
		t.Fatalf("ForEvent(u13) error = %v, want ErrTeamNotFound", err)
	}
	// A viewer who cannot see the event gets not-found.
	if _, err := service.ForEvent(context.Background(), coachOf(2).Access, "u10", event); !errors.Is(err, ErrTeamNotFound) {
		t.Fatalf("ForEvent as another team's coach error = %v, want ErrTeamNotFound", err)
	}
	// A called-up player sees the event through the team although the team is not theirs.
	calledUp := actorWith(GroupAccessGrants{Member: true, Roles: []string{GroupRolePlayer}, CalledUpEventIDs: []int64{9}}).Access
	if team, err := service.ForEvent(context.Background(), calledUp, "u10", event); err != nil || team.ID != 1 {
		t.Fatalf("ForEvent as called-up player = %+v, %v", team, err)
	}
	if _, err := service.ForEvent(context.Background(), actorWith(GroupAccessGrants{}).Access, "u10", event); !errors.Is(err, ErrGroupNotFound) {
		t.Fatalf("ForEvent as outsider error = %v, want ErrGroupNotFound", err)
	}
}

func TestTeamServiceResolvesSlugsWithinTheGroup(t *testing.T) {
	teams := newTeamStub(Team{ID: 1, GroupID: 5, Name: "U10", Slug: "u10"}, Team{ID: 2, GroupID: 5, Name: "U13", Slug: "u13"}, Team{ID: 3, GroupID: 6, Name: "Other", Slug: "other"})
	service, _ := newTestTeamService(t, teams, newMembershipStub())
	access := adminActor().Access

	if team, err := service.BySlug(context.Background(), access, "u10"); err != nil || team.ID != 1 {
		t.Fatalf("BySlug(u10) = %+v, %v", team, err)
	}
	// A slug of another group's team is not found in this group.
	if _, err := service.BySlug(context.Background(), access, "other"); !errors.Is(err, ErrTeamNotFound) {
		t.Fatalf("BySlug(other) error = %v, want ErrTeamNotFound", err)
	}
	// A coach of another team may not resolve a team they cannot see.
	if _, err := service.BySlug(context.Background(), coachOf(2).Access, "u10"); !errors.Is(err, ErrTeamNotFound) {
		t.Fatalf("BySlug as coach error = %v", err)
	}
	if _, err := service.BySlug(context.Background(), actorWith(GroupAccessGrants{}).Access, "u10"); !errors.Is(err, ErrGroupNotFound) {
		t.Fatalf("BySlug as outsider error = %v, want ErrGroupNotFound", err)
	}
	if team, err := service.ByID(context.Background(), access, 2); err != nil || team.Slug != "u13" {
		t.Fatalf("ByID(2) = %+v, %v", team, err)
	}
	if _, err := service.ByID(context.Background(), access, 3); !errors.Is(err, ErrTeamNotFound) {
		t.Fatalf("ByID(3) error = %v, want ErrTeamNotFound", err)
	}
	slugs, err := service.Slugs(context.Background(), access)
	if err != nil || slugs[1] != "u10" || slugs[2] != "u13" {
		t.Fatalf("Slugs() = %v, %v", slugs, err)
	}
}
