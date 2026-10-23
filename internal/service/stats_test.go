package service

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestNewDatastoreStatsServiceValidatesConfig(t *testing.T) {
	if _, err := NewDatastoreStatsService(nil, time.Second); !errors.Is(err, ErrNilDatastoreStatsRepository) {
		t.Fatalf("NewDatastoreStatsService(nil repo) error = %v, want %v", err, ErrNilDatastoreStatsRepository)
	}
	if _, err := NewDatastoreStatsService(&datastoreStatsRepositoryStub{}, -time.Second); !errors.Is(err, ErrInvalidDatastoreStatsConfig) {
		t.Fatalf("NewDatastoreStatsService(negative ttl) error = %v, want %v", err, ErrInvalidDatastoreStatsConfig)
	}
}

func TestDatastoreStatsServiceCachesAggregates(t *testing.T) {
	repository := &datastoreStatsRepositoryStub{accounts: AccountCounts{Total: 5, Guests: 2}}
	service, err := NewDatastoreStatsService(repository, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(10_000, 0)
	service.now = func() time.Time { return now }

	counts, err := service.Counts(context.Background())
	if err != nil {
		t.Fatalf("Counts() error = %v", err)
	}
	if counts.TotalAccounts != 5 || counts.GuestAccounts != 2 {
		t.Fatalf("counts = %+v, want total 5 and guests 2", counts)
	}
	if _, err := service.Counts(context.Background()); err != nil {
		t.Fatalf("Counts() error = %v", err)
	}
	if repository.accountCalls != 1 {
		t.Fatalf("account calls = %d, want cached single call", repository.accountCalls)
	}
	now = now.Add(2 * time.Minute)
	if _, err := service.Counts(context.Background()); err != nil {
		t.Fatal(err)
	}
	if repository.accountCalls != 2 {
		t.Fatalf("account calls after ttl = %d, want 2", repository.accountCalls)
	}
}

func TestDatastoreStatsServicePropagatesFailures(t *testing.T) {
	backendErr := errors.New("count failed")
	service, err := NewDatastoreStatsService(&datastoreStatsRepositoryStub{err: backendErr}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Counts(context.Background()); !errors.Is(err, backendErr) {
		t.Fatalf("Counts() error = %v, want errors.Is(_, %v)", err, backendErr)
	}
}

func TestDatastoreStatsServiceRejectsNilContext(t *testing.T) {
	service, err := NewDatastoreStatsService(&datastoreStatsRepositoryStub{}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Counts(nil); err == nil {
		t.Fatal("Counts(nil) error = nil, want failure")
	}
}

type datastoreStatsRepositoryStub struct {
	accounts     AccountCounts
	err          error
	accountCalls int
}

func (r *datastoreStatsRepositoryStub) CountAccounts(context.Context) (AccountCounts, error) {
	r.accountCalls++
	if r.err != nil {
		return AccountCounts{}, r.err
	}
	return r.accounts, nil
}

func (r *datastoreStatsRepositoryStub) CountActiveSessions(context.Context, int64) (int, error) {
	return 0, r.err
}

func (r *datastoreStatsRepositoryStub) CountExpiredSessions(context.Context, int64) (int, error) {
	return 0, r.err
}

func (r *datastoreStatsRepositoryStub) CountExpiredOAuthStates(context.Context, int64) (int, error) {
	return 0, r.err
}
