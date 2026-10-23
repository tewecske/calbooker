package service

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestSuspiciousAccountServiceDerivesFlags(t *testing.T) {
	repository := &suspiciousRepositoryStub{usage: []AccountUsage{
		{UserID: 1, Requests: SuspiciousActionCountThreshold, DistinctOrigins: 1},
		{UserID: 2, Requests: 1, DistinctOrigins: SuspiciousDistinctOriginThreshold},
		{UserID: 0, Requests: 999, DistinctOrigins: 9},
	}}
	service, err := NewSuspiciousAccountService(repository)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(500_000, 0)
	service.now = func() time.Time { return now }

	report, err := service.Report(context.Background(), "24h")
	if err != nil {
		t.Fatalf("Report() error = %v", err)
	}
	if len(report.Accounts) != 2 {
		t.Fatalf("accounts = %+v, want invalid user skipped", report.Accounts)
	}
	if !report.Accounts[0].ActionFlag || report.Accounts[0].OriginFlag {
		t.Fatalf("account 1 flags = %+v, want action-only flag", report.Accounts[0])
	}
	if report.Accounts[1].ActionFlag || !report.Accounts[1].OriginFlag {
		t.Fatalf("account 2 flags = %+v, want origin-only flag", report.Accounts[1])
	}
	if report.ActionThreshold != SuspiciousActionCountThreshold || report.OriginThreshold != SuspiciousDistinctOriginThreshold {
		t.Fatalf("thresholds = %d/%d", report.ActionThreshold, report.OriginThreshold)
	}
	if repository.query.Since != now.Add(-24*time.Hour).Unix() || repository.query.Until != now.Unix() {
		t.Fatalf("query window = %d..%d", repository.query.Since, repository.query.Until)
	}
}

func TestNewSuspiciousAccountServiceValidatesRepository(t *testing.T) {
	if _, err := NewSuspiciousAccountService(nil); !errors.Is(err, ErrNilSuspiciousAccountRepository) {
		t.Fatalf("NewSuspiciousAccountService(nil) error = %v, want %v", err, ErrNilSuspiciousAccountRepository)
	}
}

func TestSuspiciousAccountServicePropagatesFailures(t *testing.T) {
	backendErr := errors.New("query failed")
	service, err := NewSuspiciousAccountService(&suspiciousRepositoryStub{err: backendErr})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Report(context.Background(), "24h"); !errors.Is(err, backendErr) {
		t.Fatalf("Report() error = %v, want errors.Is(_, %v)", err, backendErr)
	}
}

type suspiciousRepositoryStub struct {
	usage []AccountUsage
	query AccountUsageQuery
	err   error
}

func (r *suspiciousRepositoryStub) QueryAccountUsage(_ context.Context, query AccountUsageQuery) ([]AccountUsage, error) {
	r.query = query
	if r.err != nil {
		return nil, r.err
	}
	return r.usage, nil
}
