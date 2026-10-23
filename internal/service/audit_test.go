package service

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestAuditServiceRecordStoresValidatedEntry(t *testing.T) {
	repository := &auditRepositoryStub{}
	audit, err := NewAuditService(repository)
	if err != nil {
		t.Fatalf("NewAuditService() error = %v", err)
	}
	if err := audit.Record(context.Background(), AuditRecord{
		ActorUserID: 7,
		ActorEmail:  "admin@example.test",
		Action:      AuditActionAccountUpdated,
		TargetType:  "user",
		TargetID:    "42",
		Detail:      "created account",
		IP:          "203.0.113.7",
	}); err != nil {
		t.Fatalf("Record() error = %v", err)
	}
	if len(repository.entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(repository.entries))
	}
	entry := repository.entries[0]
	if entry.Action != AuditActionAccountUpdated || entry.OccurredAt <= 0 {
		t.Fatalf("entry = %+v, want action and timestamp", entry)
	}
	if entry.ActorUserID == nil || *entry.ActorUserID != 7 {
		t.Fatalf("actor = %v, want 7", entry.ActorUserID)
	}
	if entry.ActorEmail == nil || *entry.ActorEmail != "admin@example.test" {
		t.Fatalf("actor email = %v, want snapshot", entry.ActorEmail)
	}
}

func TestAuditServiceRecordRejectsInvalidInput(t *testing.T) {
	tests := []struct {
		name   string
		record AuditRecord
	}{
		{name: "missing actor", record: AuditRecord{Action: "x"}},
		{name: "missing action", record: AuditRecord{ActorUserID: 1}},
		{name: "control characters", record: AuditRecord{ActorUserID: 1, Action: "bad\naction"}},
		{name: "oversized detail", record: AuditRecord{ActorUserID: 1, Action: "ok", Detail: strings.Repeat("a", 1001)}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			audit, err := NewAuditService(&auditRepositoryStub{})
			if err != nil {
				t.Fatalf("NewAuditService() error = %v", err)
			}
			if err := audit.Record(context.Background(), test.record); !errors.Is(err, ErrInvalidAuditEntry) {
				t.Fatalf("Record() error = %v, want %v", err, ErrInvalidAuditEntry)
			}
		})
	}
}

func TestAuditServiceRecordPropagatesStorageFailure(t *testing.T) {
	failure := errors.New("storage down")
	audit, err := NewAuditService(&auditRepositoryStub{err: failure})
	if err != nil {
		t.Fatalf("NewAuditService() error = %v", err)
	}
	err = audit.Record(context.Background(), AuditRecord{ActorUserID: 1, Action: "ok"})
	if !errors.Is(err, failure) {
		t.Fatalf("Record() error = %v, want %v", err, failure)
	}
}

func TestNewAuditServiceRejectsNilRepository(t *testing.T) {
	if _, err := NewAuditService(nil); !errors.Is(err, ErrNilAuditRepository) {
		t.Fatalf("error = %v, want %v", err, ErrNilAuditRepository)
	}
}

type auditRepositoryStub struct {
	entries []AuditEntry
	page    AuditPage
	err     error
}

func (s *auditRepositoryStub) CreateAuditEntry(_ context.Context, entry AuditEntry) error {
	if s.err != nil {
		return s.err
	}
	s.entries = append(s.entries, entry)
	return nil
}

func (s *auditRepositoryStub) ListAuditEntries(_ context.Context, query AuditQuery) (AuditPage, error) {
	if s.err != nil {
		return AuditPage{}, s.err
	}
	page := s.page
	page.Page = query.Page
	page.Size = query.Size
	return page, nil
}

func TestAuditServiceRecordEmitsToSink(t *testing.T) {
	sink := &auditSinkStub{}
	audit, err := NewAuditService(&auditRepositoryStub{})
	if err != nil {
		t.Fatalf("NewAuditService() error = %v", err)
	}
	audit.SetSink(sink)
	record := AuditRecord{ActorUserID: 1, Action: AuditActionAccountUpdated, TargetID: "9", Detail: "user@example.test"}
	if err := audit.Record(context.Background(), record); err != nil {
		t.Fatalf("Record() error = %v", err)
	}
	if len(sink.records) != 1 || sink.records[0].Action != AuditActionAccountUpdated || sink.outcomes[0] != AuditOutcomeRecorded {
		t.Fatalf("sink records = %+v %v, want one recorded account-created record", sink.records, sink.outcomes)
	}
}

func TestAuditServiceReportsLostRecordsToSink(t *testing.T) {
	storeErr := errors.New("audit store unavailable")
	for name, test := range map[string]struct {
		repository *auditRepositoryStub
		record     AuditRecord
	}{
		"store failure":  {repository: &auditRepositoryStub{err: storeErr}, record: AuditRecord{ActorUserID: 1, Action: AuditActionAdminGranted, TargetType: "user", TargetID: "9"}},
		"invalid record": {repository: &auditRepositoryStub{}, record: AuditRecord{ActorUserID: 1, Action: AuditActionAdminGranted, Detail: "bad\x00detail"}},
	} {
		t.Run(name, func(t *testing.T) {
			sink := &auditSinkStub{}
			audit, err := NewAuditService(test.repository)
			if err != nil {
				t.Fatal(err)
			}
			audit.SetSink(sink)
			if err := audit.Record(context.Background(), test.record); err == nil {
				t.Fatal("Record() error = nil, want the failure returned to the caller")
			}
			if len(sink.outcomes) != 1 || sink.outcomes[0] != AuditOutcomeFailed || sink.records[0].Action != AuditActionAdminGranted {
				t.Fatalf("sink saw %v %+v, want one failed %s event", sink.outcomes, sink.records, AuditActionAdminGranted)
			}
		})
	}
}

func TestAuditServiceSinkFailureDoesNotUndoStoredRecord(t *testing.T) {
	repository := &auditRepositoryStub{}
	audit, err := NewAuditService(repository)
	if err != nil {
		t.Fatalf("NewAuditService() error = %v", err)
	}
	audit.SetSink(erroringAuditSink{})
	if err := audit.Record(context.Background(), AuditRecord{ActorUserID: 1, Action: "x"}); err != nil {
		t.Fatalf("Record() error = %v, want nil", err)
	}
	if len(repository.entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(repository.entries))
	}
}

type auditSinkStub struct {
	records  []AuditRecord
	outcomes []string
}

func (s *auditSinkStub) AuditRecorded(_ context.Context, record AuditRecord, outcome string) {
	s.records = append(s.records, record)
	s.outcomes = append(s.outcomes, outcome)
}

type erroringAuditSink struct{}

func (erroringAuditSink) AuditRecorded(context.Context, AuditRecord, string) {}

func TestAuditQueryNormalizeBoundsValues(t *testing.T) {
	got := (AuditQuery{Action: " created ", Actor: "a@example.test", Target: "9", Page: -2, Size: 1000}).Normalize()
	if got.Action != "created" || got.Actor != "a@example.test" || got.Target != "9" {
		t.Fatalf("normalize filters = %+v, want trimmed values", got)
	}
	if got.Page != 1 || got.Size != MaxAuditPageSize {
		t.Fatalf("normalize page/size = %d/%d, want 1/%d", got.Page, got.Size, MaxAuditPageSize)
	}
	defaults := (AuditQuery{}).Normalize()
	if defaults.Page != 1 || defaults.Size != DefaultAuditPageSize {
		t.Fatalf("defaults = %+v, want page 1 size %d", defaults, DefaultAuditPageSize)
	}
	if (AuditQuery{Page: 3, Size: 20}).Offset() != 40 {
		t.Fatal("Offset() = wrong offset")
	}
}

func TestAuditServiceListNormalizesAndCountsPages(t *testing.T) {
	repository := &auditRepositoryStub{page: AuditPage{Total: 45, Entries: []AuditEntry{{ID: 1, Action: "x"}}}}
	audit, err := NewAuditService(repository)
	if err != nil {
		t.Fatalf("NewAuditService() error = %v", err)
	}
	page, err := audit.List(context.Background(), AuditQuery{Size: 1000})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if page.Size != MaxAuditPageSize || page.Total != 45 || page.Pages != 1 {
		t.Fatalf("page = %+v, want size %d total 45 pages 1", page, MaxAuditPageSize)
	}
}
