//go:build integration

package integration

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
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
	dbrepo "github.com/abdullahPrasetio/relay-api/internal/repository/db"
	"github.com/abdullahPrasetio/relay-api/internal/usecase"
	"github.com/abdullahPrasetio/relay-api/migrations"
)

// commentHarness resets the schema, applies every embedded migration and seeds
// one workspace with owner/admin/editor/viewer/outsider members plus two
// request entities (req_1, req_2).
//
//	TEST_DATABASE_URL='postgres://postgres@127.0.0.1:55432/phase4_comments?sslmode=disable' \
//	  go test -tags=integration ./internal/integration/...
type commentHarness struct {
	db                                     *gorm.DB
	workspaceUC                            usecase.WorkspaceUseCase
	comments                               usecase.CommentUseCase
	owner, admin, editor, viewer, outsider uuid.UUID
	workspaceID                            string
}

func newCommentHarness(t *testing.T) *commentHarness {
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

	h := &commentHarness{
		db:          gdb,
		workspaceUC: usecase.NewWorkspaceUseCase(dbrepo.NewWorkspaceRepository(gdb)),
		owner:       uuid.MustParse("aaaaaaaa-1111-4111-8111-111111111111"),
		admin:       uuid.MustParse("bbbbbbbb-2222-4222-8222-222222222222"),
		editor:      uuid.MustParse("cccccccc-3333-4333-8333-333333333333"),
		viewer:      uuid.MustParse("dddddddd-4444-4444-8444-444444444444"),
		outsider:    uuid.MustParse("eeeeeeee-5555-4555-8555-555555555555"),
	}
	h.comments = usecase.NewCommentUseCase(
		dbrepo.NewWorkspaceRepository(gdb),
		dbrepo.NewCommentRepository(gdb),
	)

	profiles := []struct{ name, email string }{
		{"Olive Owner", "owner@example.test"},
		{"Adam Admin", "admin@example.test"},
		{"Eli Editor", "editor@example.test"},
		{"Vera Viewer", "viewer@example.test"},
		{"Oscar Outsider", "outsider@example.test"},
	}
	for i, id := range []uuid.UUID{h.owner, h.admin, h.editor, h.viewer, h.outsider} {
		res := gdb.Exec(
			`INSERT INTO users (id, name, email, password) VALUES (?, ?, ?, ?)`,
			id, profiles[i].name, profiles[i].email, "hash",
		)
		require.NoError(t, res.Error)
	}

	ctx := context.Background()
	ws, err := h.workspaceUC.CreateWorkspace(ctx, h.owner, "Comment Team")
	require.NoError(t, err)
	h.workspaceID = ws.ID
	_, err = h.workspaceUC.AddMember(ctx, h.owner, ws.ID, h.admin, entity.RoleAdmin)
	require.NoError(t, err)
	_, err = h.workspaceUC.AddMember(ctx, h.owner, ws.ID, h.editor, entity.RoleEditor)
	require.NoError(t, err)
	_, err = h.workspaceUC.AddMember(ctx, h.owner, ws.ID, h.viewer, entity.RoleViewer)
	require.NoError(t, err)

	changes := []entity.Change{
		{OpID: "op-req-1", ID: "req_1", Kind: entity.KindRequest, Payload: json.RawMessage(`{"name":"First","method":"GET"}`)},
		{OpID: "op-req-2", ID: "req_2", Kind: entity.KindRequest, Payload: json.RawMessage(`{"name":"Second","method":"POST"}`)},
	}
	results, _, err := h.workspaceUC.PushChanges(ctx, ws.ID, h.owner, changes)
	require.NoError(t, err)
	for _, result := range results {
		require.True(t, result.Applied, "seed change failed: %+v", result)
	}
	return h
}

func TestCommentCRUDAndRoleRules(t *testing.T) {
	h := newCommentHarness(t)
	ctx := context.Background()

	// Every role, viewer included, may comment on an existing entity.
	byViewer, err := h.comments.Create(ctx, h.viewer, h.workspaceID, usecase.CreateCommentInput{
		EntityID: " req_1 ",
		Body:     "  please check headers  ",
	})
	require.NoError(t, err)
	require.Equal(t, "please check headers", byViewer.Body)
	require.Equal(t, "req_1", byViewer.EntityID)
	require.Equal(t, h.viewer, byViewer.UserID)

	byEditor, err := h.comments.Create(ctx, h.editor, h.workspaceID, usecase.CreateCommentInput{
		EntityID: "req_1",
		Body:     "on it",
	})
	require.NoError(t, err)

	// Non-members and unknown entities are rejected.
	_, err = h.comments.Create(ctx, h.outsider, h.workspaceID, usecase.CreateCommentInput{EntityID: "req_1", Body: "nope"})
	require.ErrorIs(t, err, usecase.ErrNotFound)
	_, err = h.comments.Create(ctx, h.viewer, h.workspaceID, usecase.CreateCommentInput{EntityID: "req_missing", Body: "hi"})
	require.ErrorIs(t, err, usecase.ErrCommentEntityNotFound)

	list, next, err := h.comments.List(ctx, h.viewer, h.workspaceID, "req_1", nil, 10)
	require.NoError(t, err)
	require.Len(t, list, 2)
	require.Empty(t, next)
	seen := map[uuid.UUID]bool{}
	for _, comment := range list {
		seen[comment.ID] = true
	}
	require.True(t, seen[byViewer.ID])
	require.True(t, seen[byEditor.ID])

	_, _, err = h.comments.List(ctx, h.outsider, h.workspaceID, "req_1", nil, 10)
	require.ErrorIs(t, err, usecase.ErrNotFound)
	_, _, err = h.comments.List(ctx, h.viewer, h.workspaceID, "  ", nil, 10)
	require.ErrorIs(t, err, usecase.ErrValidation)

	// Author may edit; another editor may not; admin may.
	edited, err := h.comments.Update(ctx, h.viewer, byViewer.ID, usecase.UpdateCommentInput{Body: commentStringPtr("  edited by author  ")})
	require.NoError(t, err)
	require.Equal(t, "edited by author", edited.Body)

	_, err = h.comments.Update(ctx, h.editor, byViewer.ID, usecase.UpdateCommentInput{Body: commentStringPtr("hijack")})
	require.ErrorIs(t, err, usecase.ErrForbidden)
	_, err = h.comments.Update(ctx, h.outsider, byViewer.ID, usecase.UpdateCommentInput{Body: commentStringPtr("hijack")})
	require.ErrorIs(t, err, usecase.ErrNotFound)

	_, err = h.comments.Update(ctx, h.viewer, byViewer.ID, usecase.UpdateCommentInput{})
	require.ErrorIs(t, err, usecase.ErrValidation)

	resolved, err := h.comments.Update(ctx, h.admin, byViewer.ID, usecase.UpdateCommentInput{Resolved: commentBoolPtr(true)})
	require.NoError(t, err)
	require.NotNil(t, resolved.ResolvedAt)
	require.False(t, resolved.ResolvedAt.IsZero())

	reopened, err := h.comments.Update(ctx, h.admin, byViewer.ID, usecase.UpdateCommentInput{Resolved: commentBoolPtr(false)})
	require.NoError(t, err)
	require.Nil(t, reopened.ResolvedAt)

	// Delete: author or admin only.
	require.ErrorIs(t, h.comments.Delete(ctx, h.editor, byViewer.ID), usecase.ErrForbidden)
	require.ErrorIs(t, h.comments.Delete(ctx, h.outsider, byViewer.ID), usecase.ErrNotFound)
	require.NoError(t, h.comments.Delete(ctx, h.viewer, byViewer.ID))
	require.NoError(t, h.comments.Delete(ctx, h.admin, byEditor.ID))

	list, _, err = h.comments.List(ctx, h.viewer, h.workspaceID, "req_1", nil, 10)
	require.NoError(t, err)
	require.Empty(t, list)

	// Audit trail covers create, resolve/reopen and delete.
	var created, resolvedRows, deleted int64
	require.NoError(t, h.db.Raw(
		`SELECT count(*) FROM audit_events WHERE workspace_id = ? AND action = 'comment.create'`,
		h.workspaceID,
	).Scan(&created).Error)
	require.NoError(t, h.db.Raw(
		`SELECT count(*) FROM audit_events WHERE workspace_id = ? AND action = 'comment.resolve'`,
		h.workspaceID,
	).Scan(&resolvedRows).Error)
	require.NoError(t, h.db.Raw(
		`SELECT count(*) FROM audit_events WHERE workspace_id = ? AND action = 'comment.delete'`,
		h.workspaceID,
	).Scan(&deleted).Error)
	require.EqualValues(t, 2, created)
	require.EqualValues(t, 1, resolvedRows)
	require.EqualValues(t, 2, deleted)
}

func TestCommentPaginationAndEntityValidation(t *testing.T) {
	h := newCommentHarness(t)
	ctx := context.Background()

	base := time.Now().UTC().Add(-time.Hour)
	ids := make([]uuid.UUID, 0, 5)
	for i := range 5 {
		comment, err := h.comments.Create(ctx, h.viewer, h.workspaceID, usecase.CreateCommentInput{
			EntityID: "req_1",
			Body:     "comment " + string(rune('a'+i)),
		})
		require.NoError(t, err)
		ids = append(ids, comment.ID)
	}

	// Deterministic ordering: newest first (ids[0] is newest).
	for i, id := range ids {
		require.NoError(t, h.db.Exec(
			`UPDATE comments SET created_at = ? WHERE id = ?`,
			base.Add(-time.Duration(i)*time.Minute), id,
		).Error)
	}

	page1, next1, err := h.comments.List(ctx, h.viewer, h.workspaceID, "req_1", nil, 2)
	require.NoError(t, err)
	require.Len(t, page1, 2)
	require.Equal(t, ids[0], page1[0].ID)
	require.Equal(t, ids[1], page1[1].ID)
	require.NotEmpty(t, next1)

	cursor1, err := uuid.Parse(next1)
	require.NoError(t, err)
	page2, next2, err := h.comments.List(ctx, h.viewer, h.workspaceID, "req_1", &cursor1, 2)
	require.NoError(t, err)
	require.Len(t, page2, 2)
	require.Equal(t, ids[2], page2[0].ID)
	require.Equal(t, ids[3], page2[1].ID)
	require.NotEmpty(t, next2)

	cursor2, err := uuid.Parse(next2)
	require.NoError(t, err)
	page3, next3, err := h.comments.List(ctx, h.viewer, h.workspaceID, "req_1", &cursor2, 2)
	require.NoError(t, err)
	require.Len(t, page3, 1)
	require.Equal(t, ids[4], page3[0].ID)
	require.Empty(t, next3)

	// Empty page past the oldest comment.
	oldest := ids[4]
	page4, next4, err := h.comments.List(ctx, h.viewer, h.workspaceID, "req_1", &oldest, 2)
	require.NoError(t, err)
	require.Empty(t, page4)
	require.Empty(t, next4)

	// Cursor is validated against the requested feed.
	other, err := h.comments.Create(ctx, h.viewer, h.workspaceID, usecase.CreateCommentInput{EntityID: "req_2", Body: "other feed"})
	require.NoError(t, err)
	_, _, err = h.comments.List(ctx, h.viewer, h.workspaceID, "req_1", &other.ID, 2)
	require.ErrorIs(t, err, usecase.ErrValidation)

	random := uuid.New()
	_, _, err = h.comments.List(ctx, h.viewer, h.workspaceID, "req_1", &random, 2)
	require.ErrorIs(t, err, usecase.ErrValidation)

	// Soft-deleted entities cannot receive comments.
	deletion := entity.Change{OpID: "op-del-req-2", ID: "req_2", BaseRev: 1, Deleted: true}
	results, _, err := h.workspaceUC.PushChanges(ctx, h.workspaceID, h.owner, []entity.Change{deletion})
	require.NoError(t, err)
	require.True(t, results[0].Applied)

	_, err = h.comments.Create(ctx, h.viewer, h.workspaceID, usecase.CreateCommentInput{EntityID: "req_2", Body: "too late"})
	require.ErrorIs(t, err, usecase.ErrCommentEntityNotFound)

	var count int64
	require.NoError(t, h.db.Raw(`SELECT count(*) FROM comments WHERE workspace_id = ? AND entity_id = 'req_2'`, h.workspaceID).Scan(&count).Error)
	require.EqualValues(t, 1, count)
}

func commentStringPtr(value string) *string { return &value }

func commentBoolPtr(value bool) *bool { return &value }
