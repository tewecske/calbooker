package service

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestNewRetentionServiceValidatesConfig(t *testing.T) {
	if _, err := NewRetentionService(nil, time.Hour); !errors.Is(err, ErrNilRetentionRepository) {
		t.Fatalf("NewRetentionService(nil repo) error = %v, want %v", err, ErrNilRetentionRepository)
	}
	if _, err := NewRetentionService(&retentionRepositoryStub{}, 0); !errors.Is(err, ErrInvalidRetentionConfig) {
		t.Fatalf("NewRetentionService(zero usage) error = %v, want %v", err, ErrInvalidRetentionConfig)
	}
	if _, err := NewRetentionService(&retentionRepositoryStub{}, -time.Second); !errors.Is(err, ErrInvalidRetentionConfig) {
		t.Fatalf("NewRetentionService(negative usage) error = %v, want %v", err, ErrInvalidRetentionConfig)
	}
}

func TestRetentionCleanupTokensRemovesExpiredFlowStates(t *testing.T) {
	repo := &retentionRepositoryStub{counts: []int{3}}
	service, err := NewRetentionService(repo, 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	service.now = func() time.Time { return time.Unix(50_000, 0) }

	removed, err := service.CleanupTokens(context.Background())
	if err != nil {
		t.Fatalf("CleanupTokens() error = %v", err)
	}
	if removed != 3 {
		t.Fatalf("CleanupTokens() removed = %d, want 3", removed)
	}
	if len(repo.tokenCutoffs) != 1 || repo.tokenCutoffs[0] != 50_000 {
		t.Fatalf("token cutoffs = %v, want [50000]", repo.tokenCutoffs)
	}
}

func TestRetentionCleanupAppliesConfiguredUsageCutoff(t *testing.T) {
	repo := &retentionRepositoryStub{counts: []int{7}}
	service, err := NewRetentionService(repo, 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	service.now = func() time.Time { return time.Unix(100_000, 0) }

	removed, err := service.CleanupUsageEvents(context.Background())
	if err != nil {
		t.Fatalf("CleanupUsageEvents() error = %v", err)
	}
	if removed != 7 || repo.usageCutoff != 100_000-int64((24*time.Hour)/time.Second) {
		t.Fatalf("usage cleanup = %d/%d, want 7/%d", removed, repo.usageCutoff, 100_000-int64((24*time.Hour)/time.Second))
	}
}

func TestRetentionCleanupReportsRepositoryFailure(t *testing.T) {
	backendErr := errors.New("delete failed")
	repo := &retentionRepositoryStub{errAt: 0, err: backendErr}
	service, err := NewRetentionService(repo, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.CleanupTokens(context.Background()); !errors.Is(err, backendErr) {
		t.Fatalf("CleanupTokens() error = %v, want errors.Is(_, %v)", err, backendErr)
	}
	if _, err := service.CleanupUsageEvents(context.Background()); !errors.Is(err, backendErr) {
		t.Fatalf("CleanupUsageEvents() error = %v, want errors.Is(_, %v)", err, backendErr)
	}
}

func TestRetentionServiceRejectsNilContext(t *testing.T) {
	service, err := NewRetentionService(&retentionRepositoryStub{}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	//nolint:staticcheck // intentional nil context
	if _, err := service.CleanupTokens(nil); err == nil {
		t.Fatal("CleanupTokens(nil) error = nil, want failure")
	}
}

func TestRetentionServiceReportsPeriod(t *testing.T) {
	service, err := NewRetentionService(&retentionRepositoryStub{}, 72*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if service.UsageRetention() != 72*time.Hour {
		t.Fatalf("usage retention = %s, want 72h", service.UsageRetention())
	}
}

type retentionRepositoryStub struct {
	counts       []int
	tokenCutoffs []int64
	usageCutoff  int64
	calls        int
	errAt        int
	err          error
}

func (r *retentionRepositoryStub) next() (int, error) {
	index := r.calls
	r.calls++
	if r.err != nil && (r.errAt == index || r.errAt == 0) {
		return 0, r.err
	}
	if index < len(r.counts) {
		return r.counts[index], nil
	}
	return 0, nil
}

func (r *retentionRepositoryStub) DeleteExpiredOAuthStates(_ context.Context, cutoff int64) (int, error) {
	r.tokenCutoffs = append(r.tokenCutoffs, cutoff)
	return r.next()
}

func (r *retentionRepositoryStub) DeleteUsageEventsBefore(_ context.Context, cutoff int64) (int, error) {
	r.usageCutoff = cutoff
	return r.next()
}
