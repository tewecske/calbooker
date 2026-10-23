package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestNewMaintenanceWorkerValidatesJobs(t *testing.T) {
	if _, err := NewMaintenanceWorker(time.Minute); !errors.Is(err, ErrInvalidMaintenanceConfig) {
		t.Fatalf("NewMaintenanceWorker(no jobs) error = %v, want %v", err, ErrInvalidMaintenanceConfig)
	}
	if _, err := NewMaintenanceWorker(time.Minute, MaintenanceJob{Name: "job"}); !errors.Is(err, ErrInvalidMaintenanceJob) {
		t.Fatalf("NewMaintenanceWorker(nil task) error = %v, want %v", err, ErrInvalidMaintenanceJob)
	}
	if _, err := NewMaintenanceWorker(time.Minute, MaintenanceJob{Run: func(context.Context) (int, error) { return 0, nil }}); !errors.Is(err, ErrInvalidMaintenanceJob) {
		t.Fatalf("NewMaintenanceWorker(empty name) error = %v, want %v", err, ErrInvalidMaintenanceJob)
	}
	duplicate := MaintenanceJob{Name: "dup", Run: func(context.Context) (int, error) { return 0, nil }}
	if _, err := NewMaintenanceWorker(time.Minute, duplicate, duplicate); !errors.Is(err, ErrInvalidMaintenanceJob) {
		t.Fatalf("NewMaintenanceWorker(duplicate) error = %v, want %v", err, ErrInvalidMaintenanceJob)
	}
}

func TestMaintenanceWorkerRunOnceSurvivesIndividualFailure(t *testing.T) {
	var order []string
	backendErr := errors.New("cleanup failed")
	worker, err := NewMaintenanceWorker(time.Minute,
		MaintenanceJob{Name: "first", Run: func(context.Context) (int, error) {
			order = append(order, "first")
			return 3, nil
		}},
		MaintenanceJob{Name: "broken", Run: func(context.Context) (int, error) {
			order = append(order, "broken")
			return 0, backendErr
		}},
		MaintenanceJob{Name: "last", Run: func(context.Context) (int, error) {
			order = append(order, "last")
			return 1, nil
		}},
	)
	if err != nil {
		t.Fatalf("NewMaintenanceWorker() error = %v", err)
	}
	worker.now = func() time.Time { return time.Unix(1_000, 0) }

	runs, err := worker.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}
	if len(runs) != 3 {
		t.Fatalf("RunOnce() runs = %d, want 3", len(runs))
	}
	if len(order) != 3 || order[0] != "first" || order[1] != "broken" || order[2] != "last" {
		t.Fatalf("job order = %v, want first, broken, last", order)
	}
	if runs[0].Deleted != 3 || runs[0].Failed() {
		t.Fatalf("first run = %+v, want 3 removed", runs[0])
	}
	if !runs[1].Failed() || runs[1].Err != backendErr.Error() {
		t.Fatalf("broken run = %+v, want recorded failure", runs[1])
	}
	if runs[2].Failed() {
		t.Fatalf("last run = %+v, want success after a failure", runs[2])
	}

	statuses := worker.Status()
	if len(statuses) != 3 {
		t.Fatalf("Status() len = %d, want 3", len(statuses))
	}
	if statuses[1].Job != "broken" || statuses[1].Failures != 1 || statuses[1].Runs != 1 || statuses[1].LastError != backendErr.Error() {
		t.Fatalf("broken status = %+v, want one failure", statuses[1])
	}
	if statuses[0].LastDeleted != 3 || statuses[0].Failures != 0 {
		t.Fatalf("first status = %+v, want 3 removed, no failures", statuses[0])
	}
}

func TestMaintenanceWorkerRunJobRejectsUnknownName(t *testing.T) {
	worker, err := NewMaintenanceWorker(time.Minute, MaintenanceJob{Name: "known", Run: func(context.Context) (int, error) { return 0, nil }})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := worker.RunJob(context.Background(), "missing"); !errors.Is(err, ErrUnknownMaintenanceJob) {
		t.Fatalf("RunJob(unknown) error = %v, want %v", err, ErrUnknownMaintenanceJob)
	}
	run, err := worker.RunJob(context.Background(), "known")
	if err != nil || run.Job != "known" {
		t.Fatalf("RunJob(known) = %+v, %v", run, err)
	}
}

func TestMaintenanceWorkerRejectsNilContext(t *testing.T) {
	worker, err := NewMaintenanceWorker(time.Minute, MaintenanceJob{Name: "job", Run: func(context.Context) (int, error) { return 0, nil }})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := worker.RunOnce(nil); err == nil {
		t.Fatal("RunOnce(nil) error = nil, want failure")
	}
	if _, err := worker.RunJob(nil, "job"); err == nil {
		t.Fatal("RunJob(nil) error = nil, want failure")
	}
}

func TestMaintenanceWorkerRunStopsOnCancel(t *testing.T) {
	executed := make(chan struct{}, 4)
	worker, err := NewMaintenanceWorker(time.Millisecond, MaintenanceJob{Name: "job", Run: func(context.Context) (int, error) {
		executed <- struct{}{}
		return 1, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- worker.Run(ctx) }()
	select {
	case <-executed:
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not run immediately")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run() error = %v, want nil after cancel", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not stop after cancel")
	}
	if len(worker.Status()) != 1 || worker.Status()[0].Runs < 1 {
		t.Fatalf("status = %+v, want at least one run", worker.Status())
	}
}

func TestMaintenanceWorkerBoundsRecordedError(t *testing.T) {
	longErr := errors.New(strings.Repeat("x", maxAuditDetailLength+50))
	worker, err := NewMaintenanceWorker(time.Minute, MaintenanceJob{Name: "job", Run: func(context.Context) (int, error) {
		return 0, longErr
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := worker.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	status := worker.Status()[0]
	if status.Failures != 1 || len(status.LastError) != maxAuditDetailLength {
		t.Fatalf("status = %+v, want one bounded failure", status)
	}
}
