// Package demoseed builds the local demo data set: three clubs with their
// people, teams, season, plan, and events. It is development tooling and is
// only used by cmd/demo-seed; nothing in the web application imports it.
package demoseed

import (
	"fmt"
	"strings"
)

// Password is the provider password of every demo account.
const Password = "test1234"

// SiteAdminEmail is the provider account that becomes the calbooker site
// administrator. Its password is never touched.
const SiteAdminEmail = "admin@example.com"

// Roles used in the layout; they match group_member_roles.role.
const (
	RoleAdmin   = "admin"
	RoleManager = "manager"
	RoleCoach   = "coach"
	RolePlayer  = "player"
	RoleParent  = "parent"
)

// Member is one person in a group with one or more roles.
type Member struct {
	Email string
	Name  string
	Roles []string
}

// TeamPlan is one team with its roster and coaching staff.
type TeamPlan struct {
	Name          string
	BirthYearFrom int
	BirthYearTo   int
	// CoachMinAge and CoachMaxAge are the qualification of the team's coaches.
	CoachMinAge int
	CoachMaxAge int
	Players     []string // emails
	Parents     []string // emails; Parents[i] is the guardian of Players[i]
	Coaches     []string // emails
	Head        string   // email of the head coach
}

// GroupPlan is one club. Members[0] is the group administrator.
type GroupPlan struct {
	Name    string
	Slug    string
	Members []Member
	Teams   []TeamPlan
}

// Admin returns the group administrator's email.
func (g GroupPlan) Admin() string { return g.Members[0].Email }

// Account is one provider account the seed needs.
type Account struct {
	Email       string
	DisplayName string
	SiteAdmin   bool
}

// Email returns the demo address <slug>-<role>-<n>@example.com, or
// <slug>@example.com for the group administrator (empty role).
func Email(slug, role string, n int) string {
	if role == "" {
		return slug + "@example.com"
	}
	return fmt.Sprintf("%s-%s-%d@example.com", slug, role, n)
}

func displayName(group, role string, n int) string {
	return fmt.Sprintf("%s %s %d", group, strings.ToUpper(role[:1])+role[1:], n)
}

// Layout returns the three demo groups.
func Layout() []GroupPlan {
	return []GroupPlan{
		bigGroup("BVSC", "bvsc", [2]string{"BVSC U10", "BVSC U13"}),
		bigGroup("KISE", "kise", [2]string{"KISE U10", "KISE U13"}),
		fuzyGroup(),
	}
}

func bigGroup(name, slug string, teamNames [2]string) GroupPlan {
	g := GroupPlan{Name: name, Slug: slug}
	g.Members = append(g.Members, Member{Email: Email(slug, "", 0), Name: name + " Admin", Roles: []string{RoleAdmin}})
	add := func(role string, count int) {
		for n := 1; n <= count; n++ {
			g.Members = append(g.Members, Member{Email: Email(slug, role, n), Name: displayName(name, role, n), Roles: []string{role}})
		}
	}
	add(RoleManager, 2)
	add(RoleCoach, 4)
	add(RolePlayer, 10)
	add(RoleParent, 10)

	for t := range 2 {
		team := TeamPlan{Name: teamNames[t]}
		if t == 0 {
			team.BirthYearFrom, team.BirthYearTo, team.CoachMinAge, team.CoachMaxAge = 2016, 2017, 6, 12
		} else {
			team.BirthYearFrom, team.BirthYearTo, team.CoachMinAge, team.CoachMaxAge = 2013, 2014, 10, 16
		}
		for i := range 5 {
			n := t*5 + i + 1
			team.Players = append(team.Players, Email(slug, RolePlayer, n))
			team.Parents = append(team.Parents, Email(slug, RoleParent, n))
		}
		first := t*2 + 1
		team.Coaches = []string{Email(slug, RoleCoach, first), Email(slug, RoleCoach, first+1)}
		team.Head = team.Coaches[0]
		g.Teams = append(g.Teams, team)
	}
	return g
}

func fuzyGroup() GroupPlan {
	const name, slug = "Fuzy", "fuzy"
	g := GroupPlan{Name: name, Slug: slug}
	g.Members = append(g.Members,
		Member{Email: Email(slug, "", 0), Name: name + " Admin", Roles: []string{RoleAdmin}},
		Member{Email: Email(slug, RoleManager, 1), Name: displayName(name, RoleManager, 1), Roles: []string{RoleManager, RoleCoach}},
	)
	team := TeamPlan{Name: "Fuzy U12", BirthYearFrom: 2014, BirthYearTo: 2015, CoachMinAge: 8, CoachMaxAge: 14}
	for n := 1; n <= 10; n++ {
		g.Members = append(g.Members, Member{Email: Email(slug, RolePlayer, n), Name: displayName(name, RolePlayer, n), Roles: []string{RolePlayer}})
		team.Players = append(team.Players, Email(slug, RolePlayer, n))
	}
	for n := 1; n <= 10; n++ {
		g.Members = append(g.Members, Member{Email: Email(slug, RoleParent, n), Name: displayName(name, RoleParent, n), Roles: []string{RoleParent}})
		team.Parents = append(team.Parents, Email(slug, RoleParent, n))
	}
	team.Coaches = []string{Email(slug, RoleManager, 1)}
	team.Head = team.Coaches[0]
	g.Teams = []TeamPlan{team}
	return g
}

// Accounts returns every provider account the seed needs, site admin last.
func Accounts() []Account {
	var out []Account
	for _, g := range Layout() {
		for _, m := range g.Members {
			out = append(out, Account{Email: m.Email, DisplayName: m.Name})
		}
	}
	return append(out, Account{Email: SiteAdminEmail, DisplayName: "Site Admin", SiteAdmin: true})
}
