package service

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"
)

const (
	// MaintenanceJobGuestCleanup removes empty, abandoned guest accounts.
	MaintenanceJobGuestCleanup = "guest_cleanup"
	// MaintenanceJobTokenRetention removes expired or consumed sign-in flow
	// states.
	MaintenanceJobTokenRetention = "token_retention"
	// MaintenanceJobUsageRetention removes old usage events.
	MaintenanceJobUsageRetention = "usage_retention"
	// MaintenanceJobRateLimitRetention removes expired rate-limit windows.
	MaintenanceJobRateLimitRetention = "rate_limit_retention"
	// DefaultMaintenanceInterval is the baseline delay between scheduled runs.
	DefaultMaintenanceInterval = 6 * time.Hour
	maxMaintenanceJobName      = 64
)

var (
	// ErrInvalidMaintenanceConfig identifies unusable worker settings.
	ErrInvalidMaintenanceConfig = errors.New("service: invalid maintenance config")
	// ErrInvalidMaintenanceJob identifies a job without a name or operation.
	ErrInvalidMaintenanceJob = errors.New("service: invalid maintenance job")
	// ErrUnknownMaintenanceJob identifies a run request for an unregistered job.
	ErrUnknownMaintenanceJob = errors.New("service: unknown maintenance job")
)

// MaintenanceTask performs one bounded cleanup and reports how many records it
// changed. A task must be safe to run repeatedly and must never return secrets
// in its error message.
type MaintenanceTask func(context.Context) (int, error)

// MaintenanceJob couples a stable job name with its cleanup operation. The name
// is low-cardinality so job health can be aggregated by feature.
type MaintenanceJob struct {
	Name string
	Run  MaintenanceTask
}

// MaintenanceRun is the outcome of one job execution. Err is a safe, already
// validated message and never carries credentials.
type MaintenanceRun struct {
	Job        string
	StartedAt  int64
	FinishedAt int64
	Deleted    int
	Err        string
}

// Failed reports whether the job execution returned an error.
func (r MaintenanceRun) Failed() bool {
	return r.Err != ""
}

// MaintenanceStatus is the retained last-run state reported for one job.
type MaintenanceStatus struct {
	Job            string
	Runs           int
	Failures       int
	LastStartedAt  int64
	LastFinishedAt int64
	LastDuration   time.Duration
	LastDeleted    int
	LastError      string
}

// MaintenanceWorker runs registered cleanup jobs on an interval, retains the
// last-run state for each job, and continues after an individual job failure.
type MaintenanceWorker struct {
	jobs     []MaintenanceJob
	interval time.Duration
	now      func() time.Time
	logger   *slog.Logger

	mu     sync.Mutex
	order  []string
	status map[string]*MaintenanceStatus
}

// NewMaintenanceWorker constructs a worker over one or more named jobs. A
// non-positive interval falls back to DefaultMaintenanceInterval.
func NewMaintenanceWorker(interval time.Duration, jobs ...MaintenanceJob) (*MaintenanceWorker, error) {
	if interval <= 0 {
		interval = DefaultMaintenanceInterval
	}
	worker := &MaintenanceWorker{
		interval: interval,
		now:      time.Now,
		status:   make(map[string]*MaintenanceStatus, len(jobs)),
	}
	for _, job := range jobs {
		if err := validateMaintenanceJob(job); err != nil {
			return nil, err
		}
		if _, exists := worker.status[job.Name]; exists {
			return nil, ErrInvalidMaintenanceJob
		}
		worker.jobs = append(worker.jobs, job)
		worker.order = append(worker.order, job.Name)
		worker.status[job.Name] = &MaintenanceStatus{Job: job.Name}
	}
	if len(worker.jobs) == 0 {
		return nil, ErrInvalidMaintenanceConfig
	}
	return worker, nil
}

// SetLogger attaches a structured logger for job lifecycle events. It is
// optional and intended to be set once during startup.
func (w *MaintenanceWorker) SetLogger(logger *slog.Logger) {
	if w == nil {
		return
	}
	w.logger = logger
}

// Interval reports the configured delay between scheduled runs.
func (w *MaintenanceWorker) Interval() time.Duration {
	if w == nil {
		return 0
	}
	return w.interval
}

// Jobs returns the registered job names in registration order.
func (w *MaintenanceWorker) Jobs() []string {
	if w == nil {
		return nil
	}
	names := make([]string, len(w.order))
	copy(names, w.order)
	return names
}

// RunOnce executes every registered job and returns each outcome. An individual
// job failure is recorded and never prevents the remaining jobs from running.
func (w *MaintenanceWorker) RunOnce(ctx context.Context) ([]MaintenanceRun, error) {
	if w == nil || w.now == nil {
		return nil, ErrInvalidMaintenanceConfig
	}
	if ctx == nil {
		return nil, errors.New("service: nil maintenance context")
	}
	runs := make([]MaintenanceRun, 0, len(w.jobs))
	for _, job := range w.jobs {
		runs = append(runs, w.runJob(ctx, job))
	}
	return runs, nil
}

// RunJob executes one named job and returns its outcome.
func (w *MaintenanceWorker) RunJob(ctx context.Context, name string) (MaintenanceRun, error) {
	if w == nil || w.now == nil {
		return MaintenanceRun{}, ErrInvalidMaintenanceConfig
	}
	if ctx == nil {
		return MaintenanceRun{}, errors.New("service: nil maintenance context")
	}
	for _, job := range w.jobs {
		if job.Name == name {
			return w.runJob(ctx, job), nil
		}
	}
	return MaintenanceRun{}, ErrUnknownMaintenanceJob
}

// Run executes the worker until ctx is cancelled. It runs once immediately, then
// repeats on the configured interval. A cancelled context returns nil.
func (w *MaintenanceWorker) Run(ctx context.Context) error {
	if w == nil || w.now == nil || w.interval <= 0 {
		return ErrInvalidMaintenanceConfig
	}
	if ctx == nil {
		return errors.New("service: nil maintenance context")
	}
	if _, err := w.RunOnce(ctx); err != nil {
		return err
	}
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if _, err := w.RunOnce(ctx); err != nil {
				return err
			}
		}
	}
}

// Status returns a snapshot of the retained last-run state in job order.
func (w *MaintenanceWorker) Status() []MaintenanceStatus {
	if w == nil {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	statuses := make([]MaintenanceStatus, 0, len(w.order))
	for _, name := range w.order {
		if status, ok := w.status[name]; ok {
			statuses = append(statuses, *status)
		}
	}
	return statuses
}

func (w *MaintenanceWorker) runJob(ctx context.Context, job MaintenanceJob) MaintenanceRun {
	startedAt := w.now()
	deleted, err := job.Run(ctx)
	finishedAt := w.now()
	run := MaintenanceRun{
		Job:        job.Name,
		StartedAt:  startedAt.Unix(),
		FinishedAt: finishedAt.Unix(),
		Deleted:    deleted,
	}
	if err != nil {
		run.Err = safeMaintenanceError(err)
	}
	w.record(run, finishedAt.Sub(startedAt))
	return run
}

func (w *MaintenanceWorker) record(run MaintenanceRun, duration time.Duration) {
	w.mu.Lock()
	status := w.status[run.Job]
	if status == nil {
		status = &MaintenanceStatus{Job: run.Job}
		w.status[run.Job] = status
		w.order = append(w.order, run.Job)
	}
	status.Runs++
	status.LastStartedAt = run.StartedAt
	status.LastFinishedAt = run.FinishedAt
	status.LastDuration = duration
	status.LastDeleted = run.Deleted
	status.LastError = run.Err
	if run.Failed() {
		status.Failures++
	}
	w.mu.Unlock()

	if w.logger != nil {
		level := slog.LevelInfo
		if run.Failed() {
			level = slog.LevelError
		}
		w.logger.LogAttrs(context.Background(), level, "maintenance job finished",
			slog.String("job", run.Job),
			slog.Int("removed", run.Deleted),
			slog.Duration("duration", duration),
			slog.Bool("failed", run.Failed()),
		)
	}
}

func validateMaintenanceJob(job MaintenanceJob) error {
	if job.Run == nil {
		return ErrInvalidMaintenanceJob
	}
	if job.Name == "" || len(job.Name) > maxMaintenanceJobName || !printable(job.Name) {
		return ErrInvalidMaintenanceJob
	}
	return nil
}

// safeMaintenanceError keeps the error type discoverable while bounding the
// retained message so diagnostics never grow without limit.
func safeMaintenanceError(err error) string {
	if err == nil {
		return ""
	}
	message := err.Error()
	if len(message) > maxAuditDetailLength {
		message = message[:maxAuditDetailLength]
	}
	return message
}
