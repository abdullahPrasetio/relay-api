package handler_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/abdullahPrasetio/relay-api/internal/delivery/http/handler"
	"github.com/abdullahPrasetio/relay-api/internal/domain/entity"
	"github.com/abdullahPrasetio/relay-api/internal/usecase"
	"github.com/abdullahPrasetio/relay-api/pkg/auth"
)

// stubCommentUC records the parsed request so the test can assert on the
// mention payload and the response envelope.
type stubCommentUC struct {
	lastCreate *usecase.CreateCommentInput
	lastUpdate *usecase.UpdateCommentInput
	lastBefore *uuid.UUID
	lastLimit  int
	createErr  error
}

func (s *stubCommentUC) Create(_ context.Context, userID uuid.UUID, workspaceID string, input usecase.CreateCommentInput) (*entity.Comment, error) {
	s.lastCreate = &input
	if s.createErr != nil {
		return nil, s.createErr
	}
	return &entity.Comment{
		ID:          uuid.New(),
		WorkspaceID: workspaceID,
		EntityID:    input.EntityID,
		UserID:      userID,
		AuthorName:  "Vera Viewer",
		Body:        input.Body,
		Mentions:    []entity.CommentMention{{UserID: uuid.New(), Name: "Adam Admin"}},
	}, nil
}

func (s *stubCommentUC) Update(_ context.Context, userID uuid.UUID, commentID uuid.UUID, input usecase.UpdateCommentInput) (*entity.Comment, error) {
	s.lastUpdate = &input
	return &entity.Comment{ID: commentID, UserID: userID, Mentions: []entity.CommentMention{}}, nil
}

func (s *stubCommentUC) List(_ context.Context, _ uuid.UUID, _, _ string, before *uuid.UUID, limit int) ([]entity.Comment, string, error) {
	s.lastBefore = before
	s.lastLimit = limit
	return nil, "", nil
}

func (s *stubCommentUC) ListMentions(_ context.Context, _ uuid.UUID, _ string, before *uuid.UUID, limit int) ([]entity.Comment, string, error) {
	s.lastBefore = before
	s.lastLimit = limit
	return []entity.Comment{{ID: uuid.New(), Body: "hi", Mentions: []entity.CommentMention{}}}, "cursor-1", nil
}

func (s *stubCommentUC) Delete(context.Context, uuid.UUID, uuid.UUID) error { return nil }

// commentTestApp mounts the comment handler behind real bearer auth.
func commentTestApp(t *testing.T, uc usecase.CommentUseCase) (*fiber.App, *auth.Config, string) {
	t.Helper()
	cfg := &auth.Config{
		Secret:   "test-secret-at-least-32-bytes-long!!",
		Issuer:   "relay-test",
		Audience: "relay-test-client",
	}
	user := uuid.MustParse("dddddddd-4444-4444-8444-444444444444")
	token, err := auth.Sign(user.String(), nil, cfg)
	require.NoError(t, err)

	app := fiber.New()
	h := handler.NewCommentHandler(uc)
	app.Post("/api/v1/workspaces/:id/comments", auth.Middleware(cfg), h.Create)
	app.Patch("/api/v1/comments/:id", auth.Middleware(cfg), h.Update)
	app.Get("/api/v1/workspaces/:id/comments", auth.Middleware(cfg), h.List)
	app.Get("/api/v1/workspaces/:id/mentions", auth.Middleware(cfg), h.MentionsFeed)
	return app, cfg, token
}

func doJSON(t *testing.T, app *fiber.App, method, path, token, body string) (*http.Response, map[string]any) {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = bytes.NewBufferString(body)
	}
	req := httptest.NewRequest(method, path, reader)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := app.Test(req)
	require.NoError(t, err)
	var decoded map[string]any
	raw, err := io.ReadAll(res.Body)
	require.NoError(t, err)
	// Fiber's built-in 401 has a plain-text body; JSON assertions only apply
	// to the API envelope responses.
	_ = json.Unmarshal(raw, &decoded)
	return res, decoded
}

func TestCommentHandlerParsesMentionPayloads(t *testing.T) {
	uc := &stubCommentUC{}
	app, _, token := commentTestApp(t, uc)
	mentioned := uuid.New()
	second := uuid.New()

	res, body := doJSON(t, app, "POST", "/api/v1/workspaces/ws_1/comments", token,
		`{"entityId":"req_1","body":"ping","mentions":["`+mentioned.String()+`","`+second.String()+`"]}`)
	require.Equal(t, fiber.StatusCreated, res.StatusCode)
	require.NotNil(t, uc.lastCreate)
	require.Equal(t, []uuid.UUID{mentioned, second}, uc.lastCreate.Mentions)

	data, ok := body["data"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "Vera Viewer", data["authorName"])
	mentions, ok := data["mentions"].([]any)
	require.True(t, ok)
	require.Len(t, mentions, 1)
	first, ok := mentions[0].(map[string]any)
	require.True(t, ok)
	require.Contains(t, first, "userId")
	require.Equal(t, "Adam Admin", first["name"])

	// Update: absent mentions stays nil, an empty array clears the set.
	commentID := uuid.New().String()
	res, _ = doJSON(t, app, "PATCH", "/api/v1/comments/"+commentID, token, `{"body":"edited"}`)
	require.Equal(t, fiber.StatusOK, res.StatusCode)
	require.NotNil(t, uc.lastUpdate)
	require.Nil(t, uc.lastUpdate.Mentions)

	res, _ = doJSON(t, app, "PATCH", "/api/v1/comments/"+commentID, token, `{"mentions":[]}`)
	require.Equal(t, fiber.StatusOK, res.StatusCode)
	require.NotNil(t, uc.lastUpdate.Mentions)
	require.Empty(t, *uc.lastUpdate.Mentions)

	res, _ = doJSON(t, app, "PATCH", "/api/v1/comments/"+commentID, token, `{"mentions":["`+mentioned.String()+`"]}`)
	require.Equal(t, fiber.StatusOK, res.StatusCode)
	require.Equal(t, []uuid.UUID{mentioned}, *uc.lastUpdate.Mentions)

	// Malformed mention ids are a bad request, not a 500.
	res, _ = doJSON(t, app, "POST", "/api/v1/workspaces/ws_1/comments", token,
		`{"entityId":"req_1","body":"ping","mentions":["not-a-uuid"]}`)
	require.Equal(t, fiber.StatusBadRequest, res.StatusCode)
}

func TestCommentHandlerMentionsFeedAndErrors(t *testing.T) {
	uc := &stubCommentUC{}
	app, _, token := commentTestApp(t, uc)
	cursor := uuid.New()

	res, body := doJSON(t, app, "GET", "/api/v1/workspaces/ws_1/mentions?limit=2&before="+cursor.String(), token, "")
	require.Equal(t, fiber.StatusOK, res.StatusCode)
	require.NotNil(t, uc.lastBefore)
	require.Equal(t, cursor, *uc.lastBefore)
	require.Equal(t, 2, uc.lastLimit)

	data, ok := body["data"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "cursor-1", data["nextBefore"])
	comments, ok := data["comments"].([]any)
	require.True(t, ok)
	require.Len(t, comments, 1)

	// An invalid cursor is rejected before reaching the usecase.
	res, _ = doJSON(t, app, "GET", "/api/v1/workspaces/ws_1/mentions?before=nope", token, "")
	require.Equal(t, fiber.StatusBadRequest, res.StatusCode)

	// Unauthenticated requests never reach the usecase.
	res, _ = doJSON(t, app, "GET", "/api/v1/workspaces/ws_1/mentions", "", "")
	require.Equal(t, fiber.StatusUnauthorized, res.StatusCode)

	// Invalid mentions (non-members, unknown ids) surface as 422.
	uc.createErr = usecase.ErrValidation
	res, _ = doJSON(t, app, "POST", "/api/v1/workspaces/ws_1/comments", token,
		`{"entityId":"req_1","body":"ping","mentions":["`+uuid.New().String()+`"]}`)
	require.Equal(t, fiber.StatusUnprocessableEntity, res.StatusCode)

	uc.createErr = usecase.ErrNotFound
	res, _ = doJSON(t, app, "POST", "/api/v1/workspaces/ws_1/comments", token,
		`{"entityId":"req_1","body":"ping"}`)
	require.Equal(t, fiber.StatusNotFound, res.StatusCode)

	uc.createErr = errors.New("boom")
	res, _ = doJSON(t, app, "POST", "/api/v1/workspaces/ws_1/comments", token,
		`{"entityId":"req_1","body":"ping"}`)
	require.Equal(t, fiber.StatusInternalServerError, res.StatusCode)
}
