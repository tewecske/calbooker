//go:build integration

package postgres

import (
	"context"
	"errors"
	"testing"

	"github.com/tewecske/calbooker/internal/service"
)

func TestGroupAndTeamSlugs(t *testing.T) {
	ctx := context.Background()
	harness := migratedHarness(t)
	memberships := mustMembershipRepository(t, harness)
	groups, err := NewGroupRepository(harness.DB)
	if err != nil {
		t.Fatal(err)
	}
	teams := mustTeamRepository(t, harness)
	adminID := insertIntegrationUser(t, harness, "admin@example.test")

	club, _, err := memberships.CreateGroupWithAdmin(ctx, service.Group{Name: "Club", Slug: "club", NameNorm: "club", CreatedAt: 100}, adminID)
	if err != nil || club.Slug != "club" {
		t.Fatalf("CreateGroupWithAdmin() = %+v, %v", club, err)
	}
	// Group slugs are unique site-wide.
	if _, _, err := memberships.CreateGroupWithAdmin(ctx, service.Group{Name: "Club 2", Slug: "club", NameNorm: "club 2", CreatedAt: 100}, adminID); !errors.Is(err, service.ErrSlugTaken) {
		t.Fatalf("duplicate group slug error = %v, want ErrSlugTaken", err)
	}
	other, _, err := memberships.CreateGroupWithAdmin(ctx, service.Group{Name: "Other", Slug: "other", NameNorm: "other", CreatedAt: 100}, adminID)
	if err != nil {
		t.Fatal(err)
	}
	// Callers that give no slug get a unique fallback.
	bare, _, err := memberships.CreateGroupWithAdmin(ctx, service.Group{Name: "Bare", NameNorm: "bare", CreatedAt: 100}, adminID)
	if err != nil || bare.Slug == "" {
		t.Fatalf("slugless group = %+v, %v", bare, err)
	}

	if found, err := groups.FindGroupBySlug(ctx, "club"); err != nil || found.ID != club.ID {
		t.Fatalf("FindGroupBySlug(club) = %+v, %v", found, err)
	}
	if _, err := groups.FindGroupBySlug(ctx, "missing"); !errors.Is(err, service.ErrGroupNotFound) {
		t.Fatalf("FindGroupBySlug(missing) error = %v, want ErrGroupNotFound", err)
	}
	if taken, err := groups.GroupSlugTaken(ctx, "club", 0); err != nil || !taken {
		t.Fatalf("GroupSlugTaken(club) = %v, %v", taken, err)
	}
	if taken, err := groups.GroupSlugTaken(ctx, "club", club.ID); err != nil || taken {
		t.Fatalf("GroupSlugTaken(club, own ID) = %v, %v; want free", taken, err)
	}

	renamed := other
	renamed.Slug = "club"
	if _, err := groups.UpdateGroup(ctx, renamed, other.Version); !errors.Is(err, service.ErrSlugTaken) {
		t.Fatalf("UpdateGroup onto a taken slug error = %v, want ErrSlugTaken", err)
	}
	renamed.Slug = "other-club"
	if updated, err := groups.UpdateGroup(ctx, renamed, other.Version); err != nil || updated.Slug != "other-club" {
		t.Fatalf("UpdateGroup() = %+v, %v", updated, err)
	}

	// Team slugs are unique within a group only.
	lions, err := teams.CreateTeam(ctx, service.Team{GroupID: club.ID, Name: "Lions", Slug: "u10", BirthYearFrom: 2015, BirthYearTo: 2016, CreatedAt: 101})
	if err != nil || lions.Slug != "u10" {
		t.Fatalf("CreateTeam() = %+v, %v", lions, err)
	}
	if _, err := teams.CreateTeam(ctx, service.Team{GroupID: club.ID, Name: "Tigers", Slug: "u10", BirthYearFrom: 2015, BirthYearTo: 2016, CreatedAt: 101}); !errors.Is(err, service.ErrSlugTaken) {
		t.Fatalf("duplicate team slug error = %v, want ErrSlugTaken", err)
	}
	if _, err := teams.CreateTeam(ctx, service.Team{GroupID: other.ID, Name: "Lions", Slug: "u10", BirthYearFrom: 2015, BirthYearTo: 2016, CreatedAt: 101}); err != nil {
		t.Fatalf("same team slug in another group error = %v", err)
	}
	if found, err := teams.FindTeamBySlug(ctx, club.ID, "u10"); err != nil || found.ID != lions.ID {
		t.Fatalf("FindTeamBySlug() = %+v, %v", found, err)
	}
	if _, err := teams.FindTeamBySlug(ctx, bare.ID, "u10"); !errors.Is(err, service.ErrTeamNotFound) {
		t.Fatalf("FindTeamBySlug(other group) error = %v, want ErrTeamNotFound", err)
	}
	if taken, err := teams.TeamSlugTaken(ctx, club.ID, "u10", lions.ID); err != nil || taken {
		t.Fatalf("TeamSlugTaken(own ID) = %v, %v; want free", taken, err)
	}
	lions.Slug = "u10-blue"
	if updated, err := teams.UpdateTeam(ctx, lions, lions.Version); err != nil || updated.Slug != "u10-blue" {
		t.Fatalf("UpdateTeam() = %+v, %v", updated, err)
	}

	// The schema refuses malformed slugs even from callers that skip the service.
	if _, err := teams.CreateTeam(ctx, service.Team{GroupID: club.ID, Name: "Bad", Slug: "Not Valid", BirthYearFrom: 2015, BirthYearTo: 2016, CreatedAt: 101}); err == nil {
		t.Fatal("malformed team slug was stored")
	}
}
