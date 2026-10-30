package usecase

import (
	"context"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/abdullahPrasetio/relay-api/internal/domain/entity"
	domainrepo "github.com/abdullahPrasetio/relay-api/internal/domain/repository"
)

type fakeCommentAuth struct {
	roles map[uuid.UUID]entity.Role
}

func (f *fakeCommentAuth) GetMember(_ context.Context, workspaceID string, userID uuid.UUID) (*entity.WorkspaceMember, error) {
	role, ok := f.roles[userID]
	if !ok || workspaceID != "ws_1" {
		return nil, gorm.ErrRecordNotFound
	}
	return &entity.WorkspaceMember{WorkspaceID: workspaceID, UserID: userID, Role: role}, nil
}

type fakeCommentStore struct {
	comments     map[uuid.UUID]*entity.Comment
	entities     map[string]bool
	members      map[string]map[uuid.UUID]string
	actions      []string
	createErr    error
	updateErr    error
	deleteErr    error
	lastCreated  *entity.Comment
	lastMentions []uuid.UUID
	lastReplace  *[]uuid.UUID
}

func newFakeCommentStore() *fakeCommentStore {
	viewer := uuid.MustParse("dddddddd-4444-4444-8444-444444444444")
	admin := uuid.MustParse("bbbbbbbb-2222-4222-8222-222222222222")
	editor := uuid.MustParse("cccccccc-3333-4333-8333-333333333333")
	return &fakeCommentStore{
		comments: map[uuid.UUID]*entity.Comment{},
		entities: map[string]bool{"req_1": true},
		members: map[string]map[uuid.UUID]string{
			"ws_1": {
				viewer: "Vera Viewer",
				admin:  "Avery Admin",
				editor: "Eli Editor",
			},
		},
	}
}

// mentionEntities hydrates mention ids for the fake, mirroring the SQL join.
func (f *fakeCommentStore) mentionEntities(workspaceID string, ids []uuid.UUID) []entity.CommentMention {
	out := make([]entity.CommentMention, 0, len(ids))
	for _, id := range ids {
		out = append(out, entity.CommentMention{UserID: id, Name: f.members[workspaceID][id]})
	}
	return out
}

func (f *fakeCommentStore) CreateComment(_ context.Context, comment *entity.Comment, mentionIDs []uuid.UUID) error {
	if f.createErr != nil {
		return f.createErr
	}
	comment.Mentions = f.mentionEntities(comment.WorkspaceID, mentionIDs)
	comment.AuthorName = f.members[comment.WorkspaceID][comment.UserID]
	f.comments[comment.ID] = comment
	f.lastCreated = comment
	f.lastMentions = mentionIDs
	return nil
}

func (f *fakeCommentStore) GetComment(_ context.Context, id uuid.UUID) (*entity.Comment, error) {
	comment, ok := f.comments[id]
	if !ok {
		return nil, gorm.ErrRecordNotFound
	}
	return comment, nil
}

// sortComments orders newest-first with the id as the keyset tiebreaker.
func sortComments(out []entity.Comment) {
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].ID.String() > out[j].ID.String()
		}
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
}

func afterCursor(out []entity.Comment, anchor entity.Comment) []entity.Comment {
	filtered := out[:0]
	for _, comment := range out {
		if comment.CreatedAt.Before(anchor.CreatedAt) ||
			(comment.CreatedAt.Equal(anchor.CreatedAt) && comment.ID.String() < anchor.ID.String()) {
			filtered = append(filtered, comment)
		}
	}
	return filtered
}

func (f *fakeCommentStore) ListComments(_ context.Context, workspaceID, entityID string, before *uuid.UUID, limit int) ([]entity.Comment, error) {
	var out []entity.Comment
	for _, comment := range f.comments {
		if comment.WorkspaceID == workspaceID && comment.EntityID == entityID {
			out = append(out, *comment)
		}
	}
	sortComments(out)

	if before != nil {
		anchor, ok := f.comments[*before]
		if !ok || anchor.WorkspaceID != workspaceID || anchor.EntityID != entityID {
			return nil, domainrepo.ErrCommentCursorNotFound
		}
		out = afterCursor(out, *anchor)
	}
	if limit < len(out) {
		out = out[:limit]
	}
	return out, nil
}

func (f *fakeCommentStore) ListCommentsMentioning(_ context.Context, workspaceID string, userID uuid.UUID, before *uuid.UUID, limit int) ([]entity.Comment, error) {
	var out []entity.Comment
	for _, comment := range f.comments {
		if comment.WorkspaceID != workspaceID || !mentionsUser(comment, userID) {
			continue
		}
		out = append(out, *comment)
	}
	sortComments(out)

	if before != nil {
		anchor, ok := f.comments[*before]
		if !ok || anchor.WorkspaceID != workspaceID || !mentionsUser(anchor, userID) {
			return nil, domainrepo.ErrCommentCursorNotFound
		}
		out = afterCursor(out, *anchor)
	}
	if limit < len(out) {
		out = out[:limit]
	}
	return out, nil
}

func mentionsUser(comment *entity.Comment, userID uuid.UUID) bool {
	for _, mention := range comment.Mentions {
		if mention.UserID == userID {
			return true
		}
	}
	return false
}

func (f *fakeCommentStore) UpdateComment(_ context.Context, comment *entity.Comment, _ uuid.UUID, action string, mentionIDs *[]uuid.UUID) error {
	if f.updateErr != nil {
		return f.updateErr
	}
	if mentionIDs != nil {
		comment.Mentions = f.mentionEntities(comment.WorkspaceID, *mentionIDs)
	}
	comment.AuthorName = f.members[comment.WorkspaceID][comment.UserID]
	f.comments[comment.ID] = comment
	f.lastReplace = mentionIDs
	f.actions = append(f.actions, action)
	return nil
}

func (f *fakeCommentStore) DeleteComment(_ context.Context, comment *entity.Comment, _ uuid.UUID) error {
	if f.deleteErr != nil {
		return f.deleteErr
	}
	delete(f.comments, comment.ID)
	f.actions = append(f.actions, domainrepo.CommentActionDelete)
	return nil
}

func (f *fakeCommentStore) EntityExists(_ context.Context, workspaceID, entityID string) (bool, error) {
	return workspaceID == "ws_1" && f.entities[entityID], nil
}

func (f *fakeCommentStore) WorkspaceMemberNames(_ context.Context, workspaceID string, userIDs []uuid.UUID) (map[uuid.UUID]string, error) {
	out := make(map[uuid.UUID]string, len(userIDs))
	for _, userID := range userIDs {
		if name, ok := f.members[workspaceID][userID]; ok {
			out[userID] = name
		}
	}
	return out, nil
}

func newCommentUseCaseFixture() (*fakeCommentStore, CommentUseCase, uuid.UUID, uuid.UUID) {
	viewer := uuid.MustParse("dddddddd-4444-4444-8444-444444444444")
	admin := uuid.MustParse("bbbbbbbb-2222-4222-8222-222222222222")
	store := newFakeCommentStore()
	authz := &fakeCommentAuth{roles: map[uuid.UUID]entity.Role{
		viewer: entity.RoleViewer,
		admin:  entity.RoleAdmin,
		uuid.MustParse("cccccccc-3333-4333-8333-333333333333"): entity.RoleEditor,
	}}
	return store, NewCommentUseCase(authz, store), viewer, admin
}

func TestCommentCreateValidation(t *testing.T) {
	store, uc, viewer, _ := newCommentUseCaseFixture()
	ctx := context.Background()

	created, err := uc.Create(ctx, viewer, "ws_1", CreateCommentInput{
		EntityID: "  req_1  ",
		Body:     "  looks good  ",
	})
	require.NoError(t, err)
	require.Equal(t, "req_1", created.EntityID)
	require.Equal(t, "looks good", created.Body)
	require.Equal(t, viewer, created.UserID)
	require.Equal(t, "ws_1", created.WorkspaceID)
	require.NotEqual(t, uuid.Nil, created.ID)
	require.Same(t, created, store.lastCreated)

	_, err = uc.Create(ctx, viewer, "ws_1", CreateCommentInput{EntityID: "req_1", Body: "   "})
	require.ErrorIs(t, err, ErrValidation)

	_, err = uc.Create(ctx, viewer, "ws_1", CreateCommentInput{EntityID: "  ", Body: "hi"})
	require.ErrorIs(t, err, ErrValidation)

	_, err = uc.Create(ctx, viewer, "ws_1", CreateCommentInput{
		EntityID: "req_1",
		Body:     strings.Repeat("x", commentBodyMaxRunes+1),
	})
	require.ErrorIs(t, err, ErrValidation)

	_, err = uc.Create(ctx, viewer, "ws_1", CreateCommentInput{
		EntityID: "req_1",
		Body:     strings.Repeat("x", commentBodyMaxRunes),
	})
	require.NoError(t, err)

	other := uuid.MustParse("eeeeeeee-5555-4555-8555-555555555555")
	_, err = uc.Create(ctx, other, "ws_1", CreateCommentInput{EntityID: "req_1", Body: "hi"})
	require.ErrorIs(t, err, ErrNotFound)
}

func TestCommentCreateRejectsUnknownEntity(t *testing.T) {
	_, uc, viewer, _ := newCommentUseCaseFixture()

	_, err := uc.Create(context.Background(), viewer, "ws_1", CreateCommentInput{
		EntityID: "req_missing",
		Body:     "hi",
	})
	require.ErrorIs(t, err, ErrCommentEntityNotFound)
}

func TestCommentListPaginationAndCursor(t *testing.T) {
	store, uc, viewer, _ := newCommentUseCaseFixture()
	ctx := context.Background()

	now := time.Now().UTC()
	for i := range 3 {
		id := uuid.New()
		store.comments[id] = &entity.Comment{
			ID:          id,
			WorkspaceID: "ws_1",
			EntityID:    "req_1",
			UserID:      viewer,
			Body:        "body",
			CreatedAt:   now.Add(-time.Duration(i) * time.Minute),
		}
	}

	page1, next, err := uc.List(ctx, viewer, "ws_1", "req_1", nil, 2)
	require.NoError(t, err)
	require.Len(t, page1, 2)
	require.True(t, page1[0].CreatedAt.After(page1[1].CreatedAt))
	require.NotEmpty(t, next)

	cursor, err := uuid.Parse(next)
	require.NoError(t, err)
	page2, next2, err := uc.List(ctx, viewer, "ws_1", "req_1", &cursor, 2)
	require.NoError(t, err)
	require.Len(t, page2, 1)
	require.Empty(t, next2)
	require.True(t, page1[1].CreatedAt.After(page2[0].CreatedAt))

	// Cursor must belong to the requested feed.
	foreign, _, err := uc.List(ctx, viewer, "ws_1", "req_2", &cursor, 2)
	require.ErrorIs(t, err, ErrValidation)
	require.Empty(t, foreign)

	_, _, err = uc.List(ctx, viewer, "ws_1", "", nil, 2)
	require.ErrorIs(t, err, ErrValidation)

	other := uuid.MustParse("eeeeeeee-5555-4555-8555-555555555555")
	_, _, err = uc.List(ctx, other, "ws_1", "req_1", nil, 2)
	require.ErrorIs(t, err, ErrNotFound)
}

func TestCommentUpdateAndDeleteAuthorization(t *testing.T) {
	store, uc, viewer, admin := newCommentUseCaseFixture()
	ctx := context.Background()
	editor := uuid.MustParse("cccccccc-3333-4333-8333-333333333333")
	outsider := uuid.MustParse("eeeeeeee-5555-4555-8555-555555555555")

	created, err := uc.Create(ctx, viewer, "ws_1", CreateCommentInput{EntityID: "req_1", Body: "original"})
	require.NoError(t, err)

	updated, err := uc.Update(ctx, viewer, created.ID, UpdateCommentInput{Body: commentStrPtr(" edited ")})
	require.NoError(t, err)
	require.Equal(t, "edited", updated.Body)
	require.Equal(t, domainrepo.CommentActionUpdate, store.actions[len(store.actions)-1])

	_, err = uc.Update(ctx, editor, created.ID, UpdateCommentInput{Body: commentStrPtr("hijack")})
	require.ErrorIs(t, err, ErrForbidden)

	_, err = uc.Update(ctx, outsider, created.ID, UpdateCommentInput{Body: commentStrPtr("hijack")})
	require.ErrorIs(t, err, ErrNotFound)

	_, err = uc.Update(ctx, viewer, created.ID, UpdateCommentInput{})
	require.ErrorIs(t, err, ErrValidation)

	_, err = uc.Update(ctx, viewer, created.ID, UpdateCommentInput{Body: commentStrPtr("  ")})
	require.ErrorIs(t, err, ErrValidation)

	resolved, err := uc.Update(ctx, admin, created.ID, UpdateCommentInput{Resolved: commentBoolPtr(true)})
	require.NoError(t, err)
	require.NotNil(t, resolved.ResolvedAt)
	require.Equal(t, domainrepo.CommentActionResolve, store.actions[len(store.actions)-1])

	reopened, err := uc.Update(ctx, admin, created.ID, UpdateCommentInput{Resolved: commentBoolPtr(false)})
	require.NoError(t, err)
	require.Nil(t, reopened.ResolvedAt)
	require.Equal(t, domainrepo.CommentActionUnresolve, store.actions[len(store.actions)-1])

	require.ErrorIs(t, uc.Delete(ctx, editor, created.ID), ErrForbidden)
	require.ErrorIs(t, uc.Delete(ctx, outsider, created.ID), ErrNotFound)
	require.NoError(t, uc.Delete(ctx, admin, created.ID))

	_, err = store.GetComment(ctx, created.ID)
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
}

func commentStrPtr(value string) *string { return &value }

func commentBoolPtr(value bool) *bool { return &value }

func TestCommentMentionsValidatedAndDeduped(t *testing.T) {
	store, uc, viewer, admin := newCommentUseCaseFixture()
	ctx := context.Background()
	editor := uuid.MustParse("cccccccc-3333-4333-8333-333333333333")
	outsider := uuid.MustParse("eeeeeeee-5555-4555-8555-555555555555")

	created, err := uc.Create(ctx, viewer, "ws_1", CreateCommentInput{
		EntityID: "req_1",
		Body:     "cc @admin @admin @editor",
		Mentions: []uuid.UUID{admin, admin, editor},
	})
	require.NoError(t, err)
	require.Equal(t, []uuid.UUID{admin, editor}, store.lastMentions, "mention ids are deduped, first-seen order")
	require.Equal(t, "Vera Viewer", created.AuthorName)
	require.Equal(t, []entity.CommentMention{
		{UserID: admin, Name: "Avery Admin"},
		{UserID: editor, Name: "Eli Editor"},
	}, created.Mentions)

	// Non-members and empty ids are a validation error, not silently dropped.
	_, err = uc.Create(ctx, viewer, "ws_1", CreateCommentInput{
		EntityID: "req_1", Body: "hi", Mentions: []uuid.UUID{outsider},
	})
	require.ErrorIs(t, err, ErrValidation)
	_, err = uc.Create(ctx, viewer, "ws_1", CreateCommentInput{
		EntityID: "req_1", Body: "hi", Mentions: []uuid.UUID{uuid.Nil},
	})
	require.ErrorIs(t, err, ErrValidation)

	// A comment without mentions still serialises mentions as [].
	plain, err := uc.Create(ctx, viewer, "ws_1", CreateCommentInput{EntityID: "req_1", Body: "plain"})
	require.NoError(t, err)
	require.NotNil(t, plain.Mentions)
	require.Empty(t, plain.Mentions)

	// Update replaces the set; an empty list clears it.
	replaced, err := uc.Update(ctx, viewer, created.ID, UpdateCommentInput{Mentions: &[]uuid.UUID{admin}})
	require.NoError(t, err)
	require.Equal(t, []entity.CommentMention{{UserID: admin, Name: "Avery Admin"}}, replaced.Mentions)
	require.NotNil(t, store.lastReplace)
	require.Equal(t, []uuid.UUID{admin}, *store.lastReplace)

	cleared, err := uc.Update(ctx, admin, created.ID, UpdateCommentInput{Mentions: &[]uuid.UUID{}})
	require.NoError(t, err)
	require.Empty(t, cleared.Mentions)

	// Invalid mentions are rejected before any write.
	actions := len(store.actions)
	_, err = uc.Update(ctx, viewer, created.ID, UpdateCommentInput{Mentions: &[]uuid.UUID{outsider}})
	require.ErrorIs(t, err, ErrValidation)
	require.Len(t, store.actions, actions)

	// Author/admin rule still applies to mention edits.
	_, err = uc.Update(ctx, editor, created.ID, UpdateCommentInput{Mentions: &[]uuid.UUID{admin}})
	require.ErrorIs(t, err, ErrForbidden)
	_, err = uc.Update(ctx, outsider, created.ID, UpdateCommentInput{Mentions: &[]uuid.UUID{admin}})
	require.ErrorIs(t, err, ErrNotFound)
}

func TestCommentMentionsFeed(t *testing.T) {
	store, uc, viewer, admin := newCommentUseCaseFixture()
	ctx := context.Background()
	editor := uuid.MustParse("cccccccc-3333-4333-8333-333333333333")
	outsider := uuid.MustParse("eeeeeeee-5555-4555-8555-555555555555")

	base := time.Now().UTC().Add(-time.Hour)
	mentioned := make([]uuid.UUID, 0, 3)
	for i := range 3 {
		id := uuid.New()
		store.comments[id] = &entity.Comment{
			ID:          id,
			WorkspaceID: "ws_1",
			EntityID:    "req_1",
			UserID:      editor,
			AuthorName:  "Eli Editor",
			Body:        "ping " + string(rune('a'+i)),
			CreatedAt:   base.Add(time.Duration(i) * time.Minute),
			Mentions:    []entity.CommentMention{{UserID: admin, Name: "Avery Admin"}},
		}
		mentioned = append(mentioned, id)
	}
	other := uuid.New()
	store.comments[other] = &entity.Comment{
		ID:          other,
		WorkspaceID: "ws_1",
		EntityID:    "req_2",
		UserID:      editor,
		AuthorName:  "Eli Editor",
		Body:        "for viewer only",
		CreatedAt:   base.Add(10 * time.Minute),
		Mentions:    []entity.CommentMention{{UserID: viewer, Name: "Vera Viewer"}},
	}

	page1, next, err := uc.ListMentions(ctx, admin, "ws_1", nil, 2)
	require.NoError(t, err)
	require.Len(t, page1, 2)
	require.Equal(t, mentioned[2], page1[0].ID)
	require.Equal(t, mentioned[1], page1[1].ID)
	require.Equal(t, "Eli Editor", page1[0].AuthorName)
	require.NotEmpty(t, next)

	cursor, err := uuid.Parse(next)
	require.NoError(t, err)
	page2, next2, err := uc.ListMentions(ctx, admin, "ws_1", &cursor, 2)
	require.NoError(t, err)
	require.Len(t, page2, 1)
	require.Equal(t, mentioned[0], page2[0].ID)
	require.Empty(t, next2)

	// Only comments that mention the caller are visible in their feed.
	self, _, err := uc.ListMentions(ctx, viewer, "ws_1", nil, 10)
	require.NoError(t, err)
	require.Len(t, self, 1)
	require.Equal(t, other, self[0].ID)

	none, _, err := uc.ListMentions(ctx, editor, "ws_1", nil, 10)
	require.NoError(t, err)
	require.Empty(t, none)

	// Cursors must belong to the caller's own feed.
	_, _, err = uc.ListMentions(ctx, admin, "ws_1", &other, 2)
	require.ErrorIs(t, err, ErrValidation)

	// Non-members cannot read the feed, and the workspace is validated.
	_, _, err = uc.ListMentions(ctx, outsider, "ws_1", nil, 10)
	require.ErrorIs(t, err, ErrNotFound)
	_, _, err = uc.ListMentions(ctx, admin, "", nil, 10)
	require.ErrorIs(t, err, ErrValidation)
}
