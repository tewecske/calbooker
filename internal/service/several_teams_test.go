package service

import (
	"slices"
	"testing"
)

func TestSeveralTeamsAreVisibleTogether(t *testing.T) {
	player := accessFor(GroupAccessGrants{Member: true, Roles: []string{GroupRolePlayer}, OwnTeams: []TeamRole{{TeamID: 2, Role: GroupRolePlayer}, {TeamID: 1, Role: GroupRolePlayer}}})
	parent := accessFor(GroupAccessGrants{Member: true, Roles: []string{GroupRoleParent}, ChildUserIDs: []int64{20, 23}, ChildTeamIDs: []int64{1, 3}})
	both := storedEvent(10, 1, 3)
	for _, test := range []struct {
		name    string
		access  *GroupAccess
		visible []int64
		events  map[int64]bool
	}{
		{name: "player in two teams", access: player, visible: []int64{1, 2}, events: map[int64]bool{1: true, 2: true, 3: false}},
		{name: "parent with children in two teams", access: parent, visible: []int64{1, 3}, events: map[int64]bool{1: true, 2: false, 3: true}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := test.access.VisibleTeamIDs(); !slices.Equal(got, test.visible) {
				t.Fatalf("VisibleTeamIDs() = %v, want %v", got, test.visible)
			}
			for team, want := range test.events {
				if got := test.access.CanViewEvent(storedEvent(team, team)); got != want {
					t.Errorf("CanViewEvent(team %d) = %v, want %v", team, got, want)
				}
			}
			if !test.access.CanViewEvent(both) {
				t.Error("an event of two teams is visible through either")
			}
		})
	}
	if got := parent.ChildUserIDs(); !slices.Equal(got, []int64{20, 23}) {
		t.Fatalf("ChildUserIDs() = %v", got)
	}
}
