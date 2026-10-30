//go:build integration

package integration

import (
	"context"
	"database/sql"
	"os"
	"testing"

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
	"github.com/abdullahPrasetio/relay-api/internal/repository/db"
	"github.com/abdullahPrasetio/relay-api/internal/usecase"
	"github.com/abdullahPrasetio/relay-api/migrations"
)

// Requires a reachable PostgreSQL database, e.g.:
//
//	TEST_DATABASE_URL='postgres://postgres@127.0.0.1:55432/relay_domain?sslmode=disable' \
//	  go test -tags=integration ./internal/integration/...
func TestWorkspaceDeltaSync(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	ws, err := h.uc.CreateWorkspace(ctx, h.owner, "Team API")
	require.NoError(t, err)
	require.NotEmpty(t, ws.ID)
	require.Contains(t, ws.ID, "ws_")

	workspaces, err := h.uc.ListWorkspaces(ctx, h.owner)
	require.NoError(t, err)
	require.Len(t, workspaces, 1)
	require.Equal(t, entity.RoleOwner, workspaces[0].Role)

	_, err = h.uc.AddMember(ctx, h.owner, ws.ID, h.editor, entity.RoleEditor)
	require.NoError(t, err)
	_, err = h.uc.AddMember(ctx, h.owner, ws.ID, h.viewer, entity.RoleViewer)
	require.NoError(t, err)

	// Create: editor pushes a new request entity.
	create := entity.Change{
		OpID:    "op-create",
		ID:      "req_1",
		Kind:    entity.KindRequest,
		BaseRev: 0,
		Payload: []byte(`{"name":"First","method":"GET"}`),
	}
	results, seq1, err := h.uc.PushChanges(ctx, ws.ID, h.editor, []entity.Change{create})
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.True(t, results[0].Applied)
	require.False(t, results[0].Conflict)
	require.Positive(t, seq1)

	// Pull cursor: viewer sees the create exactly once.
	page, err := h.uc.PullChanges(ctx, ws.ID, h.viewer, 0, 500)
	require.NoError(t, err)
	require.Len(t, page.Changes, 1)
	require.Equal(t, seq1, page.NextSeq)
	require.False(t, page.HasMore)
	require.Equal(t, "req_1", page.Changes[0].ID)
	require.Equal(t, entity.KindRequest, page.Changes[0].Kind)
	require.EqualValues(t, 1, page.Changes[0].Rev)
	require.False(t, page.Changes[0].Deleted)
	require.JSONEq(t, `{"name":"First","method":"GET"}`, string(page.Changes[0].Payload))

	// Idempotency: resending the same opId returns the stored result and does
	// not append a second change or bump the rev.
	results, seqRetry, err := h.uc.PushChanges(ctx, ws.ID, h.editor, []entity.Change{create})
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.True(t, results[0].Applied)
	require.Equal(t, seq1, seqRetry)

	retryPage, err := h.uc.PullChanges(ctx, ws.ID, h.viewer, 0, 500)
	require.NoError(t, err)
	require.Len(t, retryPage.Changes, 1)

	// Update with the matching base rev.
	update := entity.Change{
		OpID:    "op-update",
		ID:      "req_1",
		BaseRev: 1,
		Payload: []byte(`{"name":"Second","method":"POST"}`),
	}
	results, seq2, err := h.uc.PushChanges(ctx, ws.ID, h.editor, []entity.Change{update})
	require.NoError(t, err)
	require.True(t, results[0].Applied)

	updatePage, err := h.uc.PullChanges(ctx, ws.ID, h.viewer, page.NextSeq, 500)
	require.NoError(t, err)
	require.Len(t, updatePage.Changes, 1)
	require.EqualValues(t, 2, updatePage.Changes[0].Rev)
	require.JSONEq(t, `{"name":"Second","method":"POST"}`, string(updatePage.Changes[0].Payload))
	require.Equal(t, seq2, updatePage.NextSeq)

	// Stale base rev: conflict carrying server rev + payload, no change appended.
	conflict := entity.Change{
		OpID:    "op-conflict",
		ID:      "req_1",
		BaseRev: 1,
		Payload: []byte(`{"name":"Loser"}`),
	}
	results, seqConflict, err := h.uc.PushChanges(ctx, ws.ID, h.editor, []entity.Change{conflict})
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.False(t, results[0].Applied)
	require.True(t, results[0].Conflict)
	require.NotNil(t, results[0].Server)
	require.EqualValues(t, 2, results[0].Server.Rev)
	require.JSONEq(t, `{"name":"Second","method":"POST"}`, string(results[0].Server.Payload))
	require.False(t, results[0].Server.Deleted)
	require.Equal(t, seq2, seqConflict)

	// Retrying a conflict op returns the same conflict, still without applying.
	results, _, err = h.uc.PushChanges(ctx, ws.ID, h.editor, []entity.Change{conflict})
	require.NoError(t, err)
	require.True(t, results[0].Conflict)
	require.EqualValues(t, 2, results[0].Server.Rev)

	// Delete: tombstone with matching base rev.
	deletion := entity.Change{OpID: "op-delete", ID: "req_1", BaseRev: 2, Deleted: true}
	results, seq3, err := h.uc.PushChanges(ctx, ws.ID, h.editor, []entity.Change{deletion})
	require.NoError(t, err)
	require.True(t, results[0].Applied)

	deletePage, err := h.uc.PullChanges(ctx, ws.ID, h.viewer, updatePage.NextSeq, 500)
	require.NoError(t, err)
	require.Len(t, deletePage.Changes, 1)
	require.True(t, deletePage.Changes[0].Deleted)
	require.EqualValues(t, 3, deletePage.Changes[0].Rev)
	require.Equal(t, seq3, deletePage.NextSeq)

	// Re-creating an existing id (baseRev 0) conflicts with the tombstone.
	recreate := create
	recreate.OpID = "op-recreate"
	results, _, err = h.uc.PushChanges(ctx, ws.ID, h.editor, []entity.Change{recreate})
	require.NoError(t, err)
	require.True(t, results[0].Conflict)
	require.NotNil(t, results[0].Server)

	// Cursor paging: three more entities, then walk the whole log two at a time.
	for _, id := range []string{"req_a", "req_b", "req_c"} {
		_, _, err := h.uc.PushChanges(ctx, ws.ID, h.editor, []entity.Change{{
			OpID:    "op-" + id,
			ID:      id,
			Kind:    entity.KindRequest,
			BaseRev: 0,
			Payload: []byte(`{"name":"` + id + `"}`),
		}})
		require.NoError(t, err)
	}

	var pulled []entity.PulledChange
	var cursor int64
	for {
		p, err := h.uc.PullChanges(ctx, ws.ID, h.viewer, cursor, 2)
		require.NoError(t, err)
		pulled = append(pulled, p.Changes...)
		cursor = p.NextSeq
		if !p.HasMore {
			break
		}
		require.Len(t, p.Changes, 2)
	}
	require.Len(t, pulled, 6) // create + update + tombstone + 3 creates
	for i := 1; i < len(pulled); i++ {
		require.Greater(t, pulled[i].Seq, pulled[i-1].Seq)
	}
	require.Equal(t, seq3, pulled[2].Seq)

	// Empty page is stable and does not move the cursor.
	empty, err := h.uc.PullChanges(ctx, ws.ID, h.viewer, cursor, 500)
	require.NoError(t, err)
	require.Empty(t, empty.Changes)
	require.Equal(t, cursor, empty.NextSeq)
	require.False(t, empty.HasMore)
}

func TestWorkspacePermissions(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	ws, err := h.uc.CreateWorkspace(ctx, h.owner, "Permissions")
	require.NoError(t, err)

	_, err = h.uc.AddMember(ctx, h.owner, ws.ID, h.editor, entity.RoleEditor)
	require.NoError(t, err)
	_, err = h.uc.AddMember(ctx, h.owner, ws.ID, h.viewer, entity.RoleViewer)
	require.NoError(t, err)
	_, err = h.uc.AddMember(ctx, h.owner, ws.ID, h.admin, entity.RoleAdmin)
	require.NoError(t, err)

	// Viewer cannot push.
	_, _, err = h.uc.PushChanges(ctx, ws.ID, h.viewer, []entity.Change{{
		OpID: "op-viewer", ID: "req_x", Kind: entity.KindRequest, Payload: []byte(`{}`),
	}})
	require.ErrorIs(t, err, usecase.ErrForbidden)

	// Non-member cannot pull nor read the workspace.
	_, err = h.uc.PullChanges(ctx, ws.ID, h.outsider, 0, 100)
	require.ErrorIs(t, err, usecase.ErrNotFound)
	_, err = h.uc.GetWorkspace(ctx, h.outsider, ws.ID)
	require.ErrorIs(t, err, usecase.ErrNotFound)

	// Editor cannot invite or change roles.
	_, err = h.uc.AddMember(ctx, h.editor, ws.ID, h.outsider, entity.RoleViewer)
	require.ErrorIs(t, err, usecase.ErrForbidden)
	_, err = h.uc.UpdateMemberRole(ctx, h.editor, ws.ID, h.viewer, entity.RoleEditor)
	require.ErrorIs(t, err, usecase.ErrForbidden)

	// Admin can invite and manage editor/viewer roles but cannot delete the
	// workspace (owner-only).
	_, err = h.uc.AddMember(ctx, h.admin, ws.ID, h.outsider, entity.RoleViewer)
	require.NoError(t, err)
	_, err = h.uc.UpdateMemberRole(ctx, h.admin, ws.ID, h.viewer, entity.RoleEditor)
	require.NoError(t, err)
	err = h.uc.DeleteWorkspace(ctx, h.admin, ws.ID)
	require.ErrorIs(t, err, usecase.ErrForbidden)

	// The owner role itself cannot be changed or removed.
	_, err = h.uc.UpdateMemberRole(ctx, h.owner, ws.ID, h.owner, entity.RoleAdmin)
	require.ErrorIs(t, err, usecase.ErrForbidden)
	err = h.uc.RemoveMember(ctx, h.owner, ws.ID, h.owner)
	require.ErrorIs(t, err, usecase.ErrForbidden)

	// A previous addition is idempotency-safe: adding again is rejected.
	_, err = h.uc.AddMember(ctx, h.owner, ws.ID, h.editor, entity.RoleViewer)
	require.ErrorIs(t, err, usecase.ErrAlreadyMember)

	// Owner deletes the workspace; it disappears from listings and denies pull.
	require.NoError(t, h.uc.DeleteWorkspace(ctx, h.owner, ws.ID))
	workspaces, err := h.uc.ListWorkspaces(ctx, h.owner)
	require.NoError(t, err)
	require.Empty(t, workspaces)
	_, err = h.uc.PullChanges(ctx, ws.ID, h.owner, 0, 100)
	require.ErrorIs(t, err, usecase.ErrNotFound)
}

type harness struct {
	uc       usecase.WorkspaceUseCase
	owner    uuid.UUID
	admin    uuid.UUID
	editor   uuid.UUID
	viewer   uuid.UUID
	outsider uuid.UUID
}

func newHarness(t *testing.T) *harness {
	t.Helper()

	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}

	sqlDB, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, sqlDB.Ping())

	// Fresh schema so migrations are always applied from scratch.
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
		if sqlDB2, err := gdb.DB(); err == nil {
			_ = sqlDB2.Close()
		}
	})

	h := &harness{
		uc:       usecase.NewWorkspaceUseCase(db.NewWorkspaceRepository(gdb)),
		owner:    uuid.MustParse("11111111-1111-4111-8111-111111111111"),
		admin:    uuid.MustParse("22222222-2222-4222-8222-222222222222"),
		editor:   uuid.MustParse("33333333-3333-4333-8333-333333333333"),
		viewer:   uuid.MustParse("44444444-4444-4444-8444-444444444444"),
		outsider: uuid.MustParse("55555555-5555-4555-8555-555555555555"),
	}

	for i, id := range []uuid.UUID{h.owner, h.admin, h.editor, h.viewer, h.outsider} {
		res := gdb.Exec(
			`INSERT INTO users (id, name, email, password) VALUES (?, ?, ?, ?)`,
			id, "user", "user"+string(rune('a'+i))+"@example.test", "hash",
		)
		require.NoError(t, res.Error)
	}

	return h
}
