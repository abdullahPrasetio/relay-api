//go:build integration

package integration

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-migrate/migrate/v4"
	migratepostgres "github.com/golang-migrate/migrate/v4/database/postgres"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/google/uuid"
	_ "github.com/lib/pq"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"github.com/abdullahPrasetio/relay-api/internal/domain/entity"
	domainrepo "github.com/abdullahPrasetio/relay-api/internal/domain/repository"
	"github.com/abdullahPrasetio/relay-api/internal/monitor"
	dbrepo "github.com/abdullahPrasetio/relay-api/internal/repository/db"
	"github.com/abdullahPrasetio/relay-api/internal/usecase"
	"github.com/abdullahPrasetio/relay-api/migrations"
)

type monitorHarness struct {
	db                                     *gorm.DB
	workspaceUC                            usecase.WorkspaceUseCase
	monitorRepo                            domainrepo.MonitorRepository
	owner, admin, editor, viewer, outsider uuid.UUID
	workspaceID                            string
}

func newMonitorHarness(t *testing.T) *monitorHarness {
	t.Helper()

	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}

	sqlDB, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, sqlDB.Ping())

	_, err = sqlDB.Exec(`DROP SCHEMA IF EXISTS public CASCADE`)
	require.NoError(t, err)
	_, err = sqlDB.Exec(`CREATE SCHEMA public`)
	require.NoError(t, err)

	src, err := iofs.New(migrations.FS, ".")
	require.NoError(t, err)
	driver, err := migratepostgres.WithInstance(sqlDB, &migratepostgres.Config{})
	require.NoError(t, err)
	m, err := migrate.NewWithInstance("iofs", src, "postgres", driver)
	require.NoError(t, err)
	require.NoError(t, m.Up())

	gdb, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: gormlogger.Default.LogMode(gormlogger.Silent)})
	require.NoError(t, err)
	t.Cleanup(func() {
		if raw, err := gdb.DB(); err == nil {
			_ = raw.Close()
		}
	})

	h := &monitorHarness{
		db:          gdb,
		workspaceUC: usecase.NewWorkspaceUseCase(dbrepo.NewWorkspaceRepository(gdb)),
		owner:       uuid.MustParse("aaaaaaaa-1111-4111-8111-111111111111"),
		admin:       uuid.MustParse("bbbbbbbb-2222-4222-8222-222222222222"),
		editor:      uuid.MustParse("cccccccc-3333-4333-8333-333333333333"),
		viewer:      uuid.MustParse("dddddddd-4444-4444-8444-444444444444"),
		outsider:    uuid.MustParse("eeeeeeee-5555-4555-8555-555555555555"),
	}
	h.monitorRepo = dbrepo.NewMonitorRepository(gdb)

	for i, id := range []uuid.UUID{h.owner, h.admin, h.editor, h.viewer, h.outsider} {
		res := gdb.Exec(
			`INSERT INTO users (id, name, email, password) VALUES (?, ?, ?, ?)`,
			id, "user", "monitor"+string(rune('a'+i))+"@example.test", "hash",
		)
		require.NoError(t, res.Error)
	}

	ws, err := h.workspaceUC.CreateWorkspace(context.Background(), h.owner, "Monitor Team")
	require.NoError(t, err)
	h.workspaceID = ws.ID
	_, err = h.workspaceUC.AddMember(context.Background(), h.owner, ws.ID, h.admin, entity.RoleAdmin)
	require.NoError(t, err)
	_, err = h.workspaceUC.AddMember(context.Background(), h.owner, ws.ID, h.editor, entity.RoleEditor)
	require.NoError(t, err)
	_, err = h.workspaceUC.AddMember(context.Background(), h.owner, ws.ID, h.viewer, entity.RoleViewer)
	require.NoError(t, err)

	h.seedEntities(t)
	return h
}

// seedEntities pushes a collection tree, an environment and globals so the
// workspace snapshot has the same shape the desktop app sends to the CLI.
func (h *monitorHarness) seedEntities(t *testing.T) {
	t.Helper()
	collectionID := "col_1"
	parent := collectionID
	changes := []entity.Change{
		{OpID: "op-col", ID: collectionID, Kind: entity.KindCollection, Payload: json.RawMessage(`{"name":"API"}`)},
		{OpID: "op-req", ID: "req_1", Kind: entity.KindRequest, ParentID: &parent, Payload: json.RawMessage(`{"name":"Health","method":"GET","url":"http://example.test/health"}`)},
		{OpID: "op-env", ID: "env_1", Kind: entity.KindEnvironment, Payload: json.RawMessage(`{"name":"Prod","variables":[{"key":"base","value":"https://api.test","enabled":true}]}`)},
		{OpID: "op-globals", ID: "globals_1", Kind: entity.KindGlobals, Payload: json.RawMessage(`{"variables":[{"key":"shared","value":"yes","enabled":true}]}`)},
	}
	results, _, err := h.workspaceUC.PushChanges(context.Background(), h.workspaceID, h.owner, changes)
	require.NoError(t, err)
	for _, result := range results {
		require.True(t, result.Applied, "seed change failed: %+v", result)
	}
}

func (h *monitorHarness) usecase(t *testing.T, runner monitor.Runner, notifier monitor.AlertNotifier) usecase.MonitorUseCase {
	t.Helper()
	executor := monitor.NewExecutor(h.monitorRepo, runner, notifier, time.Now)
	return usecase.NewMonitorUseCase(dbrepo.NewWorkspaceRepository(h.db), h.monitorRepo, executor)
}

type stubRunner struct {
	mu     sync.Mutex
	result monitor.Result
	err    error
	calls  int
	jobs   []monitor.Job
}

func (s *stubRunner) Run(_ context.Context, job monitor.Job) (monitor.Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	s.jobs = append(s.jobs, job)
	return s.result, s.err
}

func (s *stubRunner) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func TestMonitorCRUDAndPermissions(t *testing.T) {
	h := newMonitorHarness(t)
	ctx := context.Background()
	runner := &stubRunner{result: monitor.Result{StatusCode: 200, DurationMS: 10}}
	uc := h.usecase(t, runner, monitor.NoopNotifier{})

	// Viewer cannot create; editor can.
	_, err := uc.Create(ctx, h.viewer, h.workspaceID, usecase.CreateMonitorInput{
		Name: "Health", RequestID: "req_1", Cron: "*/5 * * * *",
	})
	require.ErrorIs(t, err, usecase.ErrForbidden)

	created, err := uc.Create(ctx, h.editor, h.workspaceID, usecase.CreateMonitorInput{
		Name: "Health", RequestID: "req_1", Cron: "*/5 * * * *",
		AlertWebhook: strPtr("https://hooks.example.test/relay"),
	})
	require.NoError(t, err)
	require.NotEqual(t, uuid.Nil, created.ID)
	require.True(t, created.Enabled)
	require.True(t, created.NextRunAt.After(time.Now().UTC()))

	// Duplicate name conflicts; bad cron/request are validation errors.
	_, err = uc.Create(ctx, h.editor, h.workspaceID, usecase.CreateMonitorInput{
		Name: "Health", RequestID: "req_1", Cron: "* * * * *",
	})
	require.ErrorIs(t, err, usecase.ErrConflict)
	_, err = uc.Create(ctx, h.editor, h.workspaceID, usecase.CreateMonitorInput{
		Name: "Bad cron", RequestID: "req_1", Cron: "not cron",
	})
	require.ErrorIs(t, err, usecase.ErrValidation)
	_, err = uc.Create(ctx, h.editor, h.workspaceID, usecase.CreateMonitorInput{
		Name: "Bad request", RequestID: "req_missing", Cron: "* * * * *",
	})
	require.ErrorIs(t, err, usecase.ErrValidation)

	// Viewer can read list/get/runs; outsider sees 404.
	list, err := uc.List(ctx, h.viewer, h.workspaceID)
	require.NoError(t, err)
	require.Len(t, list, 1)
	_, err = uc.Get(ctx, h.viewer, created.ID)
	require.NoError(t, err)
	runs, err := uc.ListRuns(ctx, h.viewer, created.ID, 10)
	require.NoError(t, err)
	require.Empty(t, runs)
	_, err = uc.List(ctx, h.outsider, h.workspaceID)
	require.ErrorIs(t, err, usecase.ErrNotFound)
	_, err = uc.Get(ctx, h.outsider, created.ID)
	require.ErrorIs(t, err, usecase.ErrNotFound)

	// Update (editor) recalculates the deadline and can clear the webhook.
	updated, err := uc.Update(ctx, h.editor, created.ID, usecase.UpdateMonitorInput{
		Cron: strPtr("0 9 * * *"), Enabled: boolPtr(true), AlertWebhookSet: true,
	})
	require.NoError(t, err)
	require.Equal(t, "0 9 * * *", updated.Cron)
	require.Nil(t, updated.AlertWebhook)
	require.False(t, updated.NextRunAt.Equal(created.NextRunAt))

	// Viewer cannot mutate or run.
	_, err = uc.Update(ctx, h.viewer, created.ID, usecase.UpdateMonitorInput{Name: strPtr("nope")})
	require.ErrorIs(t, err, usecase.ErrForbidden)
	_, err = uc.RunNow(ctx, h.viewer, created.ID)
	require.ErrorIs(t, err, usecase.ErrForbidden)

	// Editor manual run stores a successful run.
	run, err := uc.RunNow(ctx, h.editor, created.ID)
	require.NoError(t, err)
	require.True(t, run.OK)
	require.Equal(t, 200, run.StatusCode)
	require.Equal(t, 1, runner.count())
	runs, err = uc.ListRuns(ctx, h.viewer, created.ID, 10)
	require.NoError(t, err)
	require.Len(t, runs, 1)

	// Viewer cannot delete; editor can, cascading runs.
	require.ErrorIs(t, uc.Delete(ctx, h.viewer, created.ID), usecase.ErrForbidden)
	require.NoError(t, uc.Delete(ctx, h.editor, created.ID))
	_, err = uc.Get(ctx, h.editor, created.ID)
	require.ErrorIs(t, err, usecase.ErrNotFound)

	var runCount int64
	require.NoError(t, h.db.Raw(`SELECT count(*) FROM monitor_runs`).Scan(&runCount).Error)
	require.Zero(t, runCount)
}

func TestMonitorWorkspaceSnapshot(t *testing.T) {
	h := newMonitorHarness(t)
	snapshot, err := h.monitorRepo.LoadWorkspaceSnapshot(context.Background(), h.workspaceID)
	require.NoError(t, err)
	require.Equal(t, h.workspaceID, snapshot.ID)
	require.Equal(t, "Monitor Team", snapshot.Name)
	require.Len(t, snapshot.Collections, 1)
	require.Len(t, snapshot.Environments, 1)
	require.JSONEq(t, `[{"key":"shared","value":"yes","enabled":true}]`, string(snapshot.Globals))

	var collection map[string]any
	require.NoError(t, json.Unmarshal(snapshot.Collections[0], &collection))
	require.Equal(t, "col_1", collection["id"])
	items, ok := collection["items"].([]any)
	require.True(t, ok)
	require.Len(t, items, 1)
	request, ok := items[0].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "req_1", request["id"])
	require.Equal(t, "Health", request["name"])

	var env map[string]any
	require.NoError(t, json.Unmarshal(snapshot.Environments[0], &env))
	require.Equal(t, "env_1", env["id"])
	require.Equal(t, "Prod", env["name"])
}

func TestWorkerClaimsOnceAcrossTwoWorkers(t *testing.T) {
	h := newMonitorHarness(t)
	ctx := context.Background()

	// Due now (next_run_at in the past).
	due := &entity.Monitor{
		ID:          uuid.New(),
		WorkspaceID: h.workspaceID,
		Name:        "Due",
		RequestID:   "req_1",
		Cron:        "*/5 * * * *",
		Enabled:     true,
		NextRunAt:   time.Now().UTC().Add(-time.Minute),
		CreatedBy:   h.owner,
	}
	require.NoError(t, h.monitorRepo.CreateMonitor(ctx, due))

	// Not due yet.
	future := &entity.Monitor{
		ID:          uuid.New(),
		WorkspaceID: h.workspaceID,
		Name:        "Future",
		RequestID:   "req_1",
		Cron:        "*/5 * * * *",
		Enabled:     true,
		NextRunAt:   time.Now().UTC().Add(time.Hour),
		CreatedBy:   h.owner,
	}
	require.NoError(t, h.monitorRepo.CreateMonitor(ctx, future))

	runner := &stubRunner{result: monitor.Result{StatusCode: 200, DurationMS: 7}}
	executor := monitor.NewExecutor(h.monitorRepo, runner, monitor.NoopNotifier{}, time.Now)
	workerA := monitor.NewWorker(h.monitorRepo, executor, monitor.WorkerOptions{BatchSize: 10})
	workerB := monitor.NewWorker(h.monitorRepo, executor, monitor.WorkerOptions{BatchSize: 10})

	var total atomic.Int32
	var wg sync.WaitGroup
	wg.Add(2)
	for _, worker := range []*monitor.Worker{workerA, workerB} {
		go func(w *monitor.Worker) {
			defer wg.Done()
			n, err := w.RunOnce(ctx)
			require.NoError(t, err)
			total.Add(int32(n))
		}(worker)
	}
	wg.Wait()

	require.EqualValues(t, 1, total.Load(), "exactly one worker may run the due monitor")
	require.Equal(t, 1, runner.count())

	var runCount int64
	require.NoError(t, h.db.Raw(`SELECT count(*) FROM monitor_runs WHERE monitor_id = ?`, due.ID).Scan(&runCount).Error)
	require.EqualValues(t, 1, runCount)

	// The deadline moved forward, so an immediate second claim is empty.
	claimed, err := h.monitorRepo.ClaimDue(ctx, time.Now().UTC(), 10, monitor.NextRun)
	require.NoError(t, err)
	require.Empty(t, claimed)

	var next time.Time
	require.NoError(t, h.db.Raw(`SELECT next_run_at FROM monitors WHERE id = ?`, due.ID).Scan(&next).Error)
	require.True(t, next.After(time.Now().UTC()))
}

func TestAlertWebhookRetryAndTransition(t *testing.T) {
	h := newMonitorHarness(t)
	ctx := context.Background()

	var attempts atomic.Int32
	var received map[string]json.RawMessage
	var bodyMu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempt := attempts.Add(1)
		if attempt == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		body, _ := io.ReadAll(r.Body)
		bodyMu.Lock()
		require.NoError(t, json.Unmarshal(body, &received))
		bodyMu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	webhook := server.URL
	notifier := &monitor.WebhookNotifier{
		Client:    server.Client(),
		Attempts:  3,
		BaseDelay: time.Millisecond,
		MaxDelay:  2 * time.Millisecond,
	}
	runner := &stubRunner{result: monitor.Result{StatusCode: 503, DurationMS: 20}}
	uc := h.usecase(t, runner, notifier)

	created, err := uc.Create(ctx, h.editor, h.workspaceID, usecase.CreateMonitorInput{
		Name: "Down API", RequestID: "req_1", Cron: "*/5 * * * *", AlertWebhook: &webhook,
	})
	require.NoError(t, err)

	// First failure: webhook fails once, retries, then succeeds.
	run, err := uc.RunNow(ctx, h.editor, created.ID)
	require.NoError(t, err)
	require.False(t, run.OK)
	require.EqualValues(t, 2, attempts.Load())
	bodyMu.Lock()
	require.Contains(t, received, "monitor")
	require.Contains(t, received, "run")
	require.JSONEq(t, `"`+h.workspaceID+`"`, string(received["workspaceId"]))
	bodyMu.Unlock()

	// Second failure is fail→fail: no alert.
	second, err := uc.RunNow(ctx, h.editor, created.ID)
	require.NoError(t, err)
	require.False(t, second.OK)
	require.EqualValues(t, 2, attempts.Load())

	// A success after failure is fail→ok: no recovery alert.
	runner.result = monitor.Result{StatusCode: 200, DurationMS: 5}
	third, err := uc.RunNow(ctx, h.editor, created.ID)
	require.NoError(t, err)
	require.True(t, third.OK)
	require.EqualValues(t, 2, attempts.Load())

	// A later failure after success alerts again.
	runner.result = monitor.Result{StatusCode: 500, DurationMS: 5}
	fourth, err := uc.RunNow(ctx, h.editor, created.ID)
	require.NoError(t, err)
	require.False(t, fourth.OK)
	require.EqualValues(t, 3, attempts.Load())

	// Every manual execution persisted a run.
	runs, err := uc.ListRuns(ctx, h.editor, created.ID, 10)
	require.NoError(t, err)
	require.Len(t, runs, 4)
}

func TestMonitorRunnerUnavailableStoresFailure(t *testing.T) {
	h := newMonitorHarness(t)
	ctx := context.Background()
	uc := h.usecase(t, &monitor.UnavailableRunner{Reason: "core bundle missing"}, monitor.NoopNotifier{})

	created, err := uc.Create(ctx, h.editor, h.workspaceID, usecase.CreateMonitorInput{
		Name: "No runner", RequestID: "req_1", Cron: "*/5 * * * *",
	})
	require.NoError(t, err)

	run, err := uc.RunNow(ctx, h.editor, created.ID)
	require.NoError(t, err)
	require.False(t, run.OK)
	require.NotNil(t, run.Error)
	require.Contains(t, *run.Error, "core bundle missing")
	require.Zero(t, run.StatusCode)
}

func strPtr(value string) *string { return &value }

func boolPtr(value bool) *bool { return &value }

// TestRealCoreBundle executes a monitor through the extracted Node CLI. It is
// gated on TEST_CORE_PATH (e.g. /path/to/postman-rea) so the Go suite stays
// runnable without the app repo.
func TestRealCoreBundle(t *testing.T) {
	corePath := os.Getenv("TEST_CORE_PATH")
	if corePath == "" {
		t.Skip("TEST_CORE_PATH not set")
	}
	h := newMonitorHarness(t)
	ctx := context.Background()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	defer server.Close()

	payload, err := json.Marshal(map[string]any{
		"name": "Live", "method": "GET", "url": server.URL + "/health",
		"params": []any{}, "headers": []any{},
		"body":       map[string]any{"mode": "none"},
		"auth":       map[string]any{"type": "none"},
		"preRequest": "", "tests": "",
	})
	require.NoError(t, err)
	parent := "col_1"
	_, _, err = h.workspaceUC.PushChanges(ctx, h.workspaceID, h.owner, []entity.Change{{
		OpID: "op-live", ID: "req_live", Kind: entity.KindRequest,
		ParentID: &parent, Payload: payload,
	}})
	require.NoError(t, err)

	runner := &monitor.CommandRunner{
		Args:    []string{"node", "dist-core/relay-core.mjs"},
		Dir:     corePath,
		Timeout: 30 * time.Second,
	}
	uc := h.usecase(t, runner, monitor.NoopNotifier{})
	created, err := uc.Create(ctx, h.editor, h.workspaceID, usecase.CreateMonitorInput{
		Name: "Live core", RequestID: "req_live", Cron: "*/5 * * * *",
	})
	require.NoError(t, err)

	run, err := uc.RunNow(ctx, h.editor, created.ID)
	require.NoError(t, err)
	if !run.OK {
		t.Fatalf("core run failed: status=%d error=%v", run.StatusCode, run.Error)
	}
	require.Equal(t, 200, run.StatusCode)
	require.Zero(t, run.AssertionFailures)
}
