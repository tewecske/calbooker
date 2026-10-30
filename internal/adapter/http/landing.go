package httpadapter

import (
	"context"
	"log/slog"

	"github.com/tewecske/calbooker/internal/service"
)

// landingGroup is what the start page decision needs to know about one group
// of the account.
type landingGroup struct {
	Slug string
	// FamilyOnly is true when the account only plays or is a parent there.
	FamilyOnly bool
	// TeamSlugs are the active teams the account sees in the group.
	TeamSlugs []string
}

// landingRoute decides where a signed-in account starts (#46). It returns an
// unlocalized route; it never uses a caller-supplied address.
//
//  1. Someone in exactly one team (over all groups) starts on that team.
//  2. A player or parent whose only group this is starts on its calendar.
//  3. Someone in exactly one group starts on the group page.
//  4. Everyone else starts on the home page.
func landingRoute(groups []landingGroup) string {
	teams := 0
	for _, group := range groups {
		teams += len(group.TeamSlugs)
	}
	if teams == 1 {
		for _, group := range groups {
			if len(group.TeamSlugs) == 1 {
				return "/groups/" + group.Slug + "/teams/" + group.TeamSlugs[0]
			}
		}
	}
	if len(groups) == 1 {
		if groups[0].FamilyOnly {
			return "/groups/" + groups[0].Slug + "/calendar"
		}
		return "/groups/" + groups[0].Slug
	}
	return "/home"
}

// landingFor collects the account's groups and teams, each through
// GroupAccess, and applies landingRoute. Site administrators who are not
// members of a group do not count it. Any failure falls back to the home
// page: the landing is a convenience, not a gate.
func landingFor(ctx context.Context, dependencies Dependencies, userID int64) string {
	if dependencies.Groups == nil || dependencies.GroupAccess == nil {
		return "/home"
	}
	summaries, err := dependencies.Groups.List(ctx, userID)
	if err != nil {
		slog.WarnContext(ctx, "landing groups failed", "event", "landing_failed")
		return "/home"
	}
	groups := make([]landingGroup, 0, len(summaries))
	for _, summary := range summaries {
		access, err := dependencies.GroupAccess.For(ctx, userID, summary.Group.ID)
		if err != nil || !access.IsMember() {
			continue
		}
		entry := landingGroup{Slug: summary.Group.Slug, FamilyOnly: access.IsFamilyOnly()}
		// Staff see every team without being on one; only teams the account
		// belongs to (player, parent, coach) count.
		if dependencies.Teams != nil && !access.SeesAllTeams() {
			teams, err := dependencies.Teams.List(ctx, access)
			if err != nil {
				slog.WarnContext(ctx, "landing teams failed", "event", "landing_failed")
				return "/home"
			}
			for _, team := range activeTeams(teams) {
				entry.TeamSlugs = append(entry.TeamSlugs, team.Slug)
			}
		}
		groups = append(groups, entry)
	}
	return landingRoute(groups)
}

// activeTeams are the teams that are not archived.
func activeTeams(summaries []service.TeamSummary) []service.Team {
	teams := make([]service.Team, 0, len(summaries))
	for _, summary := range summaries {
		if !summary.Team.Archived() {
			teams = append(teams, summary.Team)
		}
	}
	return teams
}

// singleTeam returns the only active team a player or parent sees, for the
// navigation entry that replaces "Teams".
func (h *groupHandler) singleTeam(current groupRequest) (service.Team, bool) {
	if h.dependencies.Teams == nil || current.teamSlugs == nil {
		return service.Team{}, false
	}
	summaries, err := h.dependencies.Teams.List(current.teamSlugs.ctx, current.access)
	if err != nil {
		return service.Team{}, false
	}
	teams := activeTeams(summaries)
	if len(teams) != 1 {
		return service.Team{}, false
	}
	return teams[0], true
}
