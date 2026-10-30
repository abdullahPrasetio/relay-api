package monitor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog/log"

	"github.com/abdullahPrasetio/relay-api/internal/domain/entity"
)

// ExecutorStore is the persistence surface the executor needs.
type ExecutorStore interface {
	LoadWorkspaceSnapshot(ctx context.Context, workspaceID string) (*entity.WorkspaceSnapshot, error)
	RecordRun(ctx context.Context, run *entity.MonitorRun) (*entity.MonitorRun, error)
}

// Executor runs one monitor through the core JSON contract, stores the run and
// emits an alert on an ok→fail transition (or the first failure).
type Executor struct {
	store    ExecutorStore
	runner   Runner
	notifier AlertNotifier
	now      func() time.Time
}

// NewExecutor wires a runner, persistence and alert delivery.
func NewExecutor(store ExecutorStore, runner Runner, notifier AlertNotifier, now func() time.Time) *Executor {
	if now == nil {
		now = time.Now
	}
	if notifier == nil {
		notifier = NoopNotifier{}
	}
	if runner == nil {
		runner = &UnavailableRunner{Reason: "runner is not configured"}
	}
	return &Executor{store: store, runner: runner, notifier: notifier, now: now}
}

// Execute performs one monitor run. Infrastructure failures (unavailable
// runner, invalid output, transport error) are stored as a failed run, never
// masked as success. It returns an error only when the run cannot be
// persisted.
func (e *Executor) Execute(ctx context.Context, monitor *entity.Monitor) (*entity.MonitorRun, error) {
	if monitor == nil {
		return nil, errors.New("monitor is required")
	}
	started := e.now().UTC()

	var result Result
	var runErr error
	snapshot, snapErr := e.store.LoadWorkspaceSnapshot(ctx, monitor.WorkspaceID)
	if snapErr != nil {
		runErr = fmt.Errorf("load workspace %s: %w", monitor.WorkspaceID, snapErr)
	} else {
		workspace, marshalErr := json.Marshal(snapshot)
		if marshalErr != nil {
			runErr = fmt.Errorf("encode workspace: %w", marshalErr)
		} else {
			environmentID := ""
			if snapshot.ActiveEnvID != nil {
				environmentID = *snapshot.ActiveEnvID
			}
			result, runErr = e.runner.Run(ctx, Job{
				Workspace:     workspace,
				RequestID:     monitor.RequestID,
				EnvironmentID: environmentID,
			})
		}
	}
	finished := e.now().UTC()

	run := &entity.MonitorRun{
		ID:        uuid.New(),
		MonitorID: monitor.ID,
		StartedAt: started,
		OK:        false,
	}
	if runErr != nil {
		run.Error = stringPtr(runErr.Error())
	} else {
		run.StatusCode = result.StatusCode
		run.AssertionFailures = result.AssertionFailures
		run.DurationMS = result.DurationMS
		if run.DurationMS <= 0 {
			run.DurationMS = int(finished.Sub(started) / time.Millisecond)
		}
		run.OK = result.OK()
		if result.Error != "" {
			run.Error = stringPtr(result.Error)
		}
	}
	run.FinishedAt = &finished
	run.CreatedAt = finished

	previous, err := e.store.RecordRun(ctx, run)
	if err != nil {
		return nil, fmt.Errorf("record monitor run: %w", err)
	}

	if !run.OK && (previous == nil || previous.OK) {
		if err := e.notifier.Notify(ctx, monitor, run); err != nil {
			log.Warn().Err(err).
				Str("monitor_id", monitor.ID.String()).
				Str("run_id", run.ID.String()).
				Msg("monitor alert delivery failed")
		}
	}
	return run, nil
}

func stringPtr(s string) *string { return &s }
