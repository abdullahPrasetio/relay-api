//go:build integration

package integration

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/abdullahPrasetio/relay-api/internal/domain/entity"
	"github.com/abdullahPrasetio/relay-api/internal/usecase"
)

// TestCommentMentionsPersistedAndHydrated covers the mention set lifecycle:
// validation against workspace members, deduplication, replacement on update,
// hydration with display names, and cascade on delete.
func TestCommentMentionsPersistedAndHydrated(t *testing.T) {
	h := newCommentHarness(t)
	ctx := context.Background()

	created, err := h.comments.Create(ctx, h.viewer, h.workspaceID, usecase.CreateCommentInput{
		EntityID: "req_1",
		Body:     "cc @admin @admin @editor",
		Mentions: []uuid.UUID{h.admin, h.admin, h.editor},
	})
	require.NoError(t, err)
	require.Equal(t, "Vera Viewer", created.AuthorName)
	require.Equal(t, []entity.CommentMention{
		{UserID: h.admin, Name: "Adam Admin"},
		{UserID: h.editor, Name: "Eli Editor"},
	}, created.Mentions)
	require.EqualValues(t, 2, h.countMentionRows(t, created.ID))

	// The entity feed hydrates mentions and author names too.
	feed, _, err := h.comments.List(ctx, h.editor, h.workspaceID, "req_1", nil, 10)
	require.NoError(t, err)
	require.Len(t, feed, 1)
	require.Equal(t, "Vera Viewer", feed[0].AuthorName)
	require.Equal(t, created.Mentions, feed[0].Mentions)

	// A comment without mentions serialises as an empty array.
	plain, err := h.comments.Create(ctx, h.viewer, h.workspaceID, usecase.CreateCommentInput{
		EntityID: "req_1",
		Body:     "no mentions",
	})
	require.NoError(t, err)
	require.NotNil(t, plain.Mentions)
	require.Empty(t, plain.Mentions)

	// Non-members are rejected with a validation error (422 at the API layer)
	// and the comment is not created.
	_, err = h.comments.Create(ctx, h.viewer, h.workspaceID, usecase.CreateCommentInput{
		EntityID: "req_1",
		Body:     "hey @outsider",
		Mentions: []uuid.UUID{h.outsider},
	})
	require.ErrorIs(t, err, usecase.ErrValidation)

	// The author may replace the set; the old mention row is gone.
	replaced, err := h.comments.Update(ctx, h.viewer, created.ID, usecase.UpdateCommentInput{
		Mentions: &[]uuid.UUID{h.admin},
	})
	require.NoError(t, err)
	require.Equal(t, []entity.CommentMention{{UserID: h.admin, Name: "Adam Admin"}}, replaced.Mentions)
	require.EqualValues(t, 1, h.countMentionRows(t, created.ID))

	// Only mentions change: body and resolve state stay untouched.
	require.Equal(t, "cc @admin @admin @editor", replaced.Body)
	require.Nil(t, replaced.ResolvedAt)

	// An invalid replacement is rejected atomically: the previous set remains.
	_, err = h.comments.Update(ctx, h.viewer, created.ID, usecase.UpdateCommentInput{
		Mentions: &[]uuid.UUID{h.admin, h.outsider},
	})
	require.ErrorIs(t, err, usecase.ErrValidation)
	require.EqualValues(t, 1, h.countMentionRows(t, created.ID))

	// Admin (not the author) may clear the set.
	cleared, err := h.comments.Update(ctx, h.admin, created.ID, usecase.UpdateCommentInput{
		Mentions: &[]uuid.UUID{},
	})
	require.NoError(t, err)
	require.Empty(t, cleared.Mentions)
	require.EqualValues(t, 0, h.countMentionRows(t, created.ID))

	// Deleting the comment cascades to its mention rows.
	_, err = h.comments.Update(ctx, h.viewer, created.ID, usecase.UpdateCommentInput{
		Mentions: &[]uuid.UUID{h.admin, h.editor},
	})
	require.NoError(t, err)
	require.EqualValues(t, 2, h.countMentionRows(t, created.ID))
	require.NoError(t, h.comments.Delete(ctx, h.viewer, created.ID))

	var remaining int64
	require.NoError(t, h.db.Raw(
		`SELECT count(*) FROM comment_mentions WHERE comment_id = ?`, created.ID,
	).Scan(&remaining).Error)
	require.Zero(t, remaining)
}

// TestCommentMentionsFeedVisibility checks that the mentions feed only shows
// the caller's own mentions, paginates newest-first, and enforces membership.
func TestCommentMentionsFeedVisibility(t *testing.T) {
	h := newCommentHarness(t)
	ctx := context.Background()

	first, err := h.comments.Create(ctx, h.editor, h.workspaceID, usecase.CreateCommentInput{
		EntityID: "req_1",
		Body:     "for the admin",
		Mentions: []uuid.UUID{h.admin},
	})
	require.NoError(t, err)
	second, err := h.comments.Create(ctx, h.owner, h.workspaceID, usecase.CreateCommentInput{
		EntityID: "req_2",
		Body:     "for admin and viewer",
		Mentions: []uuid.UUID{h.admin, h.viewer},
	})
	require.NoError(t, err)

	// Deterministic ordering: `second` is newer.
	base := time.Now().UTC().Add(-time.Hour)
	require.NoError(t, h.db.Exec(`UPDATE comments SET created_at = ? WHERE id = ?`, base.Add(-time.Minute), first.ID).Error)
	require.NoError(t, h.db.Exec(`UPDATE comments SET created_at = ? WHERE id = ?`, base, second.ID).Error)

	adminFeed, next, err := h.comments.ListMentions(ctx, h.admin, h.workspaceID, nil, 10)
	require.NoError(t, err)
	require.Len(t, adminFeed, 2)
	require.Equal(t, second.ID, adminFeed[0].ID)
	require.Equal(t, first.ID, adminFeed[1].ID)
	require.Equal(t, "Olive Owner", adminFeed[0].AuthorName)
	require.Equal(t, "Eli Editor", adminFeed[1].AuthorName)
	require.Equal(t, []entity.CommentMention{
		{UserID: h.admin, Name: "Adam Admin"},
		{UserID: h.viewer, Name: "Vera Viewer"},
	}, adminFeed[0].Mentions)
	require.Empty(t, next)

	// Only the caller's own mentions are ever returned.
	viewerFeed, _, err := h.comments.ListMentions(ctx, h.viewer, h.workspaceID, nil, 10)
	require.NoError(t, err)
	require.Len(t, viewerFeed, 1)
	require.Equal(t, second.ID, viewerFeed[0].ID)

	editorFeed, _, err := h.comments.ListMentions(ctx, h.editor, h.workspaceID, nil, 10)
	require.NoError(t, err)
	require.Empty(t, editorFeed)

	// Keyset pagination with the last seen comment id.
	page1, next1, err := h.comments.ListMentions(ctx, h.admin, h.workspaceID, nil, 1)
	require.NoError(t, err)
	require.Len(t, page1, 1)
	require.Equal(t, second.ID, page1[0].ID)
	require.NotEmpty(t, next1)

	cursor, err := uuid.Parse(next1)
	require.NoError(t, err)
	page2, next2, err := h.comments.ListMentions(ctx, h.admin, h.workspaceID, &cursor, 1)
	require.NoError(t, err)
	require.Len(t, page2, 1)
	require.Equal(t, first.ID, page2[0].ID)
	require.Empty(t, next2)

	// A cursor that is not part of the caller's feed is invalid, whether it is
	// someone else's mention or an unknown id.
	_, _, err = h.comments.ListMentions(ctx, h.viewer, h.workspaceID, &first.ID, 10)
	require.ErrorIs(t, err, usecase.ErrValidation)
	random := uuid.New()
	_, _, err = h.comments.ListMentions(ctx, h.admin, h.workspaceID, &random, 10)
	require.ErrorIs(t, err, usecase.ErrValidation)

	// Membership is required; non-members see 404 semantics.
	_, _, err = h.comments.ListMentions(ctx, h.outsider, h.workspaceID, nil, 10)
	require.ErrorIs(t, err, usecase.ErrNotFound)

	// Removing the mention from the comment removes it from the feed.
	_, err = h.comments.Update(ctx, h.owner, second.ID, usecase.UpdateCommentInput{
		Mentions: &[]uuid.UUID{h.viewer},
	})
	require.NoError(t, err)
	adminFeed, _, err = h.comments.ListMentions(ctx, h.admin, h.workspaceID, nil, 10)
	require.NoError(t, err)
	require.Len(t, adminFeed, 1)
	require.Equal(t, first.ID, adminFeed[0].ID)
}

// TestWorkspaceMembersIncludeProfile checks the member listing now carries the
// user's display name and email without losing the existing fields.
func TestWorkspaceMembersIncludeProfile(t *testing.T) {
	h := newCommentHarness(t)
	ctx := context.Background()

	members, err := h.workspaceUC.ListMembers(ctx, h.owner, h.workspaceID)
	require.NoError(t, err)
	require.Len(t, members, 4)

	expected := map[uuid.UUID]struct{ name, email string }{
		h.owner:  {"Olive Owner", "owner@example.test"},
		h.admin:  {"Adam Admin", "admin@example.test"},
		h.editor: {"Eli Editor", "editor@example.test"},
		h.viewer: {"Vera Viewer", "viewer@example.test"},
	}
	for _, member := range members {
		want, ok := expected[member.UserID]
		require.True(t, ok, "unexpected member %s", member.UserID)
		require.Equal(t, want.name, member.Name)
		require.Equal(t, want.email, member.Email)
		require.Equal(t, h.workspaceID, member.WorkspaceID)
		require.True(t, member.Role.Valid())
	}

	// A non-member cannot enumerate the workspace.
	_, err = h.workspaceUC.ListMembers(ctx, h.outsider, h.workspaceID)
	require.ErrorIs(t, err, usecase.ErrNotFound)
}

// countMentionRows returns the number of stored mentions for one comment.
func (h *commentHarness) countMentionRows(t *testing.T, commentID uuid.UUID) int64 {
	t.Helper()
	var count int64
	require.NoError(t, h.db.Raw(
		`SELECT count(*) FROM comment_mentions WHERE comment_id = ?`, commentID,
	).Scan(&count).Error)
	return count
}
