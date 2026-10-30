package monitor

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abdullahPrasetio/relay-api/internal/domain/entity"
)

type fakeStore struct {
	mu       sync.Mutex
	snapshot *entity.WorkspaceSnapshot
	previous *entity.MonitorRun
	runs     []*entity.MonitorRun
}

func (f *fakeStore) LoadWorkspaceSnapshot(context.Context, string) (*entity.WorkspaceSnapshot, error) {
	if f.snapshot == nil {
		return nil, errors.New("workspace not found")
	}
	return f.snapshot, nil
}

func (f *fakeStore) RecordRun(_ context.Context, run *entity.MonitorRun) (*entity.MonitorRun, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	previous := f.previous
	f.runs = append(f.runs, run)
	f.previous = run
	return previous, nil
}

type fakeRunner struct {
	result Result
	err    error
	jobs   []Job
}

func (f *fakeRunner) Run(_ context.Context, job Job) (Result, error) {
	f.jobs = append(f.jobs, job)
	return f.result, f.err
}

type fakeNotifier struct {
	mu    sync.Mutex
	calls int
}

func (f *fakeNotifier) Notify(context.Context, *entity.Monitor, *entity.MonitorRun) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return nil
}

func (f *fakeNotifier) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func testExecutor(t *testing.T, result Result, runErr error, previous *entity.MonitorRun) (*Executor, *fakeStore, *fakeRunner, *fakeNotifier) {
	t.Helper()
	store := &fakeStore{
		snapshot: &entity.WorkspaceSnapshot{
			ID:           "ws_1",
			Name:         "Team",
			Collections:  []json.RawMessage{},
			Environments: []json.RawMessage{},
			Globals:      json.RawMessage("[]"),
			ActiveEnvID:  stringPtr("env_1"),
		},
		previous: previous,
	}
	runner := &fakeRunner{result: result, err: runErr}
	notifier := &fakeNotifier{}
	now := func() time.Time { return time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC) }
	return NewExecutor(store, runner, notifier, now), store, runner, notifier
}

func TestExecutorStoresFailedRunAndAlertsOnTransition(t *testing.T) {
	executor, store, runner, notifier := testExecutor(t,
		Result{StatusCode: 500, DurationMS: 33, Error: "server error"}, nil, nil)
	monitor := &entity.Monitor{ID: uuid.New(), WorkspaceID: "ws_1", RequestID: "req_1"}

	run, err := executor.Execute(context.Background(), monitor)
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if run.OK {
		t.Fatal("HTTP 500 run reported OK")
	}
	if len(store.runs) != 1 {
		t.Fatalf("stored runs = %d, want 1", len(store.runs))
	}
	if notifier.count() != 1 {
		t.Fatalf("alerts = %d, want 1 (first failure)", notifier.count())
	}
	if len(runner.jobs) != 1 || runner.jobs[0].RequestID != "req_1" || runner.jobs[0].EnvironmentID != "env_1" {
		t.Fatalf("unexpected jobs: %+v", runner.jobs)
	}
}

func TestExecutorDoesNotAlertOnRepeatedFailure(t *testing.T) {
	previous := &entity.MonitorRun{OK: false}
	executor, _, _, notifier := testExecutor(t,
		Result{StatusCode: 500}, nil, previous)

	run, err := executor.Execute(context.Background(), &entity.Monitor{ID: uuid.New(), WorkspaceID: "ws_1", RequestID: "req_1"})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if run.OK {
		t.Fatal("failed run reported OK")
	}
	if notifier.count() != 0 {
		t.Fatalf("alerts = %d, want 0 (fail→fail)", notifier.count())
	}
}

func TestExecutorRecordsRunnerInfrastructureFailure(t *testing.T) {
	executor, store, _, notifier := testExecutor(t, Result{}, errors.New("core bundle missing"), nil)

	run, err := executor.Execute(context.Background(), &entity.Monitor{ID: uuid.New(), WorkspaceID: "ws_1", RequestID: "req_1"})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if run.OK || run.Error == nil || *run.Error == "" {
		t.Fatalf("runner failure was not recorded: %+v", run)
	}
	if len(store.runs) != 1 {
		t.Fatalf("stored runs = %d, want 1", len(store.runs))
	}
	if notifier.count() != 1 {
		t.Fatalf("alerts = %d, want 1", notifier.count())
	}
}

func TestExecutorDoesNotAlertOnRecovery(t *testing.T) {
	executor, _, _, notifier := testExecutor(t,
		Result{StatusCode: 200}, nil, &entity.MonitorRun{OK: false})
	run, err := executor.Execute(context.Background(), &entity.Monitor{ID: uuid.New(), WorkspaceID: "ws_1", RequestID: "req_1"})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if !run.OK {
		t.Fatal("recovered run reported failure")
	}
	if notifier.count() != 0 {
		t.Fatalf("alerts = %d, want 0", notifier.count())
	}
}

func TestResultOK(t *testing.T) {
	cases := []struct {
		name string
		res  Result
		ok   bool
	}{
		{"2xx", Result{StatusCode: 200}, true},
		{"3xx", Result{StatusCode: 302}, true},
		{"4xx", Result{StatusCode: 404}, false},
		{"5xx", Result{StatusCode: 500}, false},
		{"no status", Result{StatusCode: 0}, false},
		{"assertions", Result{StatusCode: 200, AssertionFailures: 1}, false},
		{"error", Result{StatusCode: 200, Error: "boom"}, false},
	}
	for _, tc := range cases {
		if got := tc.res.OK(); got != tc.ok {
			t.Errorf("%s: OK() = %v, want %v", tc.name, got, tc.ok)
		}
	}
}
