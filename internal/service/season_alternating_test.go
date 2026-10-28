package service

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestSeasonServiceSetPartAlternating(t *testing.T) {
	seasons := newSeasonStub(season2627())
	service, auditor := newTestSeasonService(t, seasons)
	updated, err := service.SetPartAlternating(context.Background(), adminActor(), 1, 11, 3, true)
	if err != nil || seasons.expectedVersion != 3 {
		t.Fatalf("SetPartAlternating() = %+v, %v (version %d)", updated, err, seasons.expectedVersion)
	}
	if len(updated.Parts) != 2 || !updated.Parts[0].AlternatingWeeks || !updated.Parts[1].AlternatingWeeks || updated.Parts[0].Name != "Autumn" || updated.Name != "2026/27" {
		t.Fatalf("parts = %+v, want only part 1 switched on", updated.Parts)
	}
	if len(auditor.records) != 1 || auditor.records[0].Action != AuditActionSeasonUpdated || !strings.Contains(auditor.records[0].Detail, "part 1 2026-08-01 to 2026-12-20 A/B") {
		t.Fatalf("audit = %+v", auditor.records)
	}
	service, _ = newTestSeasonService(t, newSeasonStub(season2627()))
	off, err := service.SetPartAlternating(context.Background(), adminActor(), 1, 12, 3, false)
	if err != nil || off.Parts[1].AlternatingWeeks || off.Parts[0].AlternatingWeeks {
		t.Fatalf("switch off = %+v, %v", off.Parts, err)
	}

	for _, test := range []struct {
		name    string
		actor   GroupActor
		part    int64
		repoErr error
		wantErr error
	}{
		{name: "coach is forbidden", actor: coachOf(1), part: 11, wantErr: ErrGroupForbidden},
		{name: "outsider is not found", actor: actorWith(GroupAccessGrants{}), part: 11, wantErr: ErrGroupNotFound},
		{name: "unknown part", actor: adminActor(), part: 77, wantErr: ErrSeasonNotFound},
		{name: "stale revision", actor: adminActor(), part: 11, repoErr: ErrOptimisticLockConflict, wantErr: ErrOptimisticLockConflict},
		{name: "vanished season is not a conflict", actor: adminActor(), part: 11, repoErr: ErrRecordNotFound, wantErr: ErrSeasonNotFound},
	} {
		t.Run(test.name, func(t *testing.T) {
			seasons := newSeasonStub(season2627())
			seasons.updateErr = test.repoErr
			service, auditor := newTestSeasonService(t, seasons)
			if _, err := service.SetPartAlternating(context.Background(), test.actor, 1, test.part, 3, true); !errors.Is(err, test.wantErr) {
				t.Fatalf("error = %v, want %v", err, test.wantErr)
			}
			if len(auditor.records) != 0 {
				t.Fatal("failed change was audited")
			}
		})
	}
}
