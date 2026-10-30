package service

import "testing"

func TestGroupAccessIsFamilyOnly(t *testing.T) {
	tests := []struct {
		name   string
		grants GroupAccessGrants
		want   bool
	}{
		{"player", GroupAccessGrants{Member: true, Roles: []string{GroupRolePlayer}}, true},
		{"parent", GroupAccessGrants{Member: true, Roles: []string{GroupRoleParent}}, true},
		{"player and parent", GroupAccessGrants{Member: true, Roles: []string{GroupRolePlayer, GroupRoleParent}}, true},
		{"parent and coach", GroupAccessGrants{Member: true, Roles: []string{GroupRoleCoach, GroupRoleParent}}, false},
		{"manager", GroupAccessGrants{Member: true, Roles: []string{GroupRoleManager}}, false},
		{"admin and player", GroupAccessGrants{Member: true, Roles: []string{GroupRoleAdmin, GroupRolePlayer}}, false},
		{"site admin who plays", GroupAccessGrants{Member: true, SiteAdmin: true, Roles: []string{GroupRolePlayer}}, false},
		{"no roles", GroupAccessGrants{Member: true}, false},
		{"outsider", GroupAccessGrants{Roles: []string{GroupRolePlayer}}, false},
	}
	for _, tt := range tests {
		if got := NewGroupAccess(3, 7, tt.grants).IsFamilyOnly(); got != tt.want {
			t.Errorf("%s: IsFamilyOnly = %v, want %v", tt.name, got, tt.want)
		}
	}
}
