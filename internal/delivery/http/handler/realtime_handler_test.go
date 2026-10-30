package handler_test

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp/fasthttputil"

	"github.com/abdullahPrasetio/relay-api/internal/delivery/http/handler"
	"github.com/abdullahPrasetio/relay-api/internal/delivery/http/route"
	"github.com/abdullahPrasetio/relay-api/internal/domain/entity"
	"github.com/abdullahPrasetio/relay-api/internal/realtime"
	"github.com/abdullahPrasetio/relay-api/internal/usecase"
	"github.com/abdullahPrasetio/relay-api/pkg/auth"
)

// realtimeStubWorkspaceUC only implements GetWorkspace; the embedded interface
// keeps the stub compiling if the usecase grows new methods.
type realtimeStubWorkspaceUC struct {
	usecase.WorkspaceUseCase
	getWorkspace func(ctx context.Context, userID uuid.UUID, workspaceID string) (*entity.WorkspaceWithRole, error)
}

func (s *realtimeStubWorkspaceUC) GetWorkspace(ctx context.Context, userID uuid.UUID, workspaceID string) (*entity.WorkspaceWithRole, error) {
	if s.getWorkspace == nil {
		return nil, usecase.ErrNotFound
	}
	return s.getWorkspace(ctx, userID, workspaceID)
}

func memberWorkspaces(members ...uuid.UUID) *realtimeStubWorkspaceUC {
	allowed := make(map[uuid.UUID]struct{}, len(members))
	for _, id := range members {
		allowed[id] = struct{}{}
	}
	return &realtimeStubWorkspaceUC{
		getWorkspace: func(_ context.Context, userID uuid.UUID, workspaceID string) (*entity.WorkspaceWithRole, error) {
			if _, ok := allowed[userID]; !ok {
				return nil, usecase.ErrNotFound
			}
			return &entity.WorkspaceWithRole{
				Workspace: entity.Workspace{ID: workspaceID},
				Role:      entity.RoleViewer,
			}, nil
		},
	}
}

type realtimeFakeSource struct {
	seqs     chan int64
	canceled chan string
}

func newRealtimeFakeSource() *realtimeFakeSource {
	return &realtimeFakeSource{
		seqs:     make(chan int64, 4),
		canceled: make(chan string, 4),
	}
}

func (f *realtimeFakeSource) Listen(ctx context.Context, workspaceID string) (<-chan int64, error) {
	go func() {
		<-ctx.Done()
		select {
		case f.canceled <- workspaceID:
		default:
		}
	}()
	return f.seqs, nil
}

type realtimeFakePresence struct {
	mu      sync.Mutex
	upserts []realtime.Presence
	rows    []realtime.Presence
	since   time.Time
}

func (f *realtimeFakePresence) UpsertPresence(_ context.Context, p realtime.Presence) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.upserts = append(f.upserts, p)
	return nil
}

func (f *realtimeFakePresence) PresenceSince(_ context.Context, workspaceID string, since time.Time) ([]realtime.Presence, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.since = since
	out := make([]realtime.Presence, 0)
	for _, row := range f.rows {
		if row.WorkspaceID == workspaceID && !row.LastSeen.Before(since) {
			out = append(out, row)
		}
	}
	return out, nil
}

func (f *realtimeFakePresence) lastUpsert() *realtime.Presence {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.upserts) == 0 {
		return nil
	}
	last := f.upserts[len(f.upserts)-1]
	return &last
}

func (f *realtimeFakePresence) cutoff() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.since
}

func realtimeAuthConfig() *auth.Config {
	return &auth.Config{
		Secret:   strings.Repeat("r", 32),
		Issuer:   "relay-test",
		Audience: "relay-test",
		Expiry:   time.Hour,
	}
}

func realtimeToken(t *testing.T, cfg *auth.Config, uid uuid.UUID) string {
	t.Helper()
	token, err := auth.Sign(uid.String(), []string{"user"}, cfg)
	require.NoError(t, err)
	return token
}

func newRealtimeTestApp(h *handler.RealtimeHandler, cfg *auth.Config) *fiber.App {
	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	route.RegisterRealtimeRoutes(app.Group("/api/v1"), h, cfg)
	return app
}

func TestPostPresenceUpsertsActivity(t *testing.T) {
	now := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	store := &realtimeFakePresence{}
	member := uuid.MustParse("11111111-1111-4111-8111-111111111111")
	h := handler.NewRealtimeHandler(memberWorkspaces(member), newRealtimeFakeSource(), store,
		handler.WithRealtimeClock(func() time.Time { return now }))
	cfg := realtimeAuthConfig()
	app := newRealtimeTestApp(h, cfg)

	req := httptestNewRequest(http.MethodPost, "/api/v1/workspaces/ws_1/presence",
		`{"entityId":"  req_abc  "}`, realtimeToken(t, cfg, member))
	resp, err := app.Test(req)
	require.NoError(t, err)
	require.Equal(t, fiber.StatusOK, resp.StatusCode)

	rec := store.lastUpsert()
	require.NotNil(t, rec)
	require.Equal(t, "ws_1", rec.WorkspaceID)
	require.Equal(t, member, rec.UserID)
	require.NotNil(t, rec.EntityID)
	require.Equal(t, "req_abc", *rec.EntityID)
	require.Equal(t, now, rec.LastSeen)

	var body struct {
		Status bool `json:"status"`
		Data   struct {
			WorkspaceID string `json:"workspaceId"`
			UserID      string `json:"userId"`
		} `json:"data"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	require.True(t, body.Status)
	require.Equal(t, "ws_1", body.Data.WorkspaceID)
	require.Equal(t, member.String(), body.Data.UserID)
}

func TestPostPresenceRequiresMembership(t *testing.T) {
	store := &realtimeFakePresence{}
	outsider := uuid.MustParse("22222222-2222-4222-8222-222222222222")
	h := handler.NewRealtimeHandler(memberWorkspaces(uuid.New()), newRealtimeFakeSource(), store)
	cfg := realtimeAuthConfig()
	app := newRealtimeTestApp(h, cfg)

	for _, method := range []string{http.MethodGet, http.MethodPost} {
		path := "/api/v1/workspaces/ws_1/presence"
		req := httptestNewRequest(method, path, `{}`, realtimeToken(t, cfg, outsider))
		resp, err := app.Test(req)
		require.NoError(t, err)
		require.Equal(t, fiber.StatusNotFound, resp.StatusCode)
	}
	require.Nil(t, store.lastUpsert())
}

func TestPresenceRoutesRequireAuth(t *testing.T) {
	h := handler.NewRealtimeHandler(memberWorkspaces(uuid.New()), newRealtimeFakeSource(), &realtimeFakePresence{})
	app := newRealtimeTestApp(h, realtimeAuthConfig())

	for _, method := range []string{http.MethodGet, http.MethodPost} {
		req := httptestNewRequest(method, "/api/v1/workspaces/ws_1/presence", "", "")
		resp, err := app.Test(req)
		require.NoError(t, err)
		require.Equal(t, fiber.StatusUnauthorized, resp.StatusCode)
	}
}

func TestPostPresenceRejectsInvalidBody(t *testing.T) {
	member := uuid.MustParse("33333333-3333-4333-8333-333333333333")
	store := &realtimeFakePresence{}
	h := handler.NewRealtimeHandler(memberWorkspaces(member), newRealtimeFakeSource(), store)
	cfg := realtimeAuthConfig()
	app := newRealtimeTestApp(h, cfg)

	req := httptestNewRequest(http.MethodPost, "/api/v1/workspaces/ws_1/presence", `{`, realtimeToken(t, cfg, member))
	resp, err := app.Test(req)
	require.NoError(t, err)
	require.Equal(t, fiber.StatusBadRequest, resp.StatusCode)

	tooLong := `{"entityId":"` + strings.Repeat("e", 300) + `"}`
	req = httptestNewRequest(http.MethodPost, "/api/v1/workspaces/ws_1/presence", tooLong, realtimeToken(t, cfg, member))
	resp, err = app.Test(req)
	require.NoError(t, err)
	require.Equal(t, fiber.StatusUnprocessableEntity, resp.StatusCode)
	require.Nil(t, store.lastUpsert())
}

func TestGetPresenceFiltersByTTL(t *testing.T) {
	now := time.Date(2026, 5, 6, 7, 8, 9, 0, time.UTC)
	member := uuid.MustParse("44444444-4444-4444-8444-444444444444")
	fresh := uuid.MustParse("55555555-5555-4555-8555-555555555555")
	stale := uuid.MustParse("66666666-6666-4666-8666-666666666666")
	entityID := "req_live"
	store := &realtimeFakePresence{rows: []realtime.Presence{
		{WorkspaceID: "ws_1", UserID: fresh, EntityID: &entityID, LastSeen: now.Add(-10 * time.Second)},
		{WorkspaceID: "ws_1", UserID: stale, LastSeen: now.Add(-2 * time.Minute)},
		{WorkspaceID: "ws_other", UserID: stale, LastSeen: now.Add(-5 * time.Second)},
	}}
	h := handler.NewRealtimeHandler(memberWorkspaces(member), newRealtimeFakeSource(), store,
		handler.WithRealtimeClock(func() time.Time { return now }))
	cfg := realtimeAuthConfig()
	app := newRealtimeTestApp(h, cfg)

	req := httptestNewRequest(http.MethodGet, "/api/v1/workspaces/ws_1/presence", "", realtimeToken(t, cfg, member))
	resp, err := app.Test(req)
	require.NoError(t, err)
	require.Equal(t, fiber.StatusOK, resp.StatusCode)

	require.WithinDuration(t, now.Add(-60*time.Second), store.cutoff(), time.Millisecond)

	var body struct {
		Data struct {
			WorkspaceID string              `json:"workspaceId"`
			TTLSeconds  int                 `json:"ttlSeconds"`
			Members     []realtime.Presence `json:"members"`
		} `json:"data"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	require.Equal(t, "ws_1", body.Data.WorkspaceID)
	require.Equal(t, 60, body.Data.TTLSeconds)
	require.Len(t, body.Data.Members, 1)
	require.Equal(t, fresh, body.Data.Members[0].UserID)
	require.NotNil(t, body.Data.Members[0].EntityID)
	require.Equal(t, entityID, *body.Data.Members[0].EntityID)
}

func TestStreamRequiresMembership(t *testing.T) {
	outsider := uuid.MustParse("77777777-7777-4777-8777-777777777777")
	h := handler.NewRealtimeHandler(memberWorkspaces(uuid.New()), newRealtimeFakeSource(), &realtimeFakePresence{})
	cfg := realtimeAuthConfig()
	app := newRealtimeTestApp(h, cfg)

	req := httptestNewRequest(http.MethodGet, "/api/v1/workspaces/ws_1/stream", "", realtimeToken(t, cfg, outsider))
	resp, err := app.Test(req)
	require.NoError(t, err)
	require.Equal(t, fiber.StatusNotFound, resp.StatusCode)
}

func TestStreamRequiresAuth(t *testing.T) {
	h := handler.NewRealtimeHandler(memberWorkspaces(uuid.New()), newRealtimeFakeSource(), &realtimeFakePresence{})
	app := newRealtimeTestApp(h, realtimeAuthConfig())

	req := httptestNewRequest(http.MethodGet, "/api/v1/workspaces/ws_1/stream", "", "")
	resp, err := app.Test(req)
	require.NoError(t, err)
	require.Equal(t, fiber.StatusUnauthorized, resp.StatusCode)
}

func TestStreamDeliversSequenceAndStopsOnDisconnect(t *testing.T) {
	member := uuid.MustParse("88888888-8888-4888-8888-888888888888")
	source := newRealtimeFakeSource()
	// A short heartbeat is what detects a half-open client connection: the
	// write after disconnect fails and cancels the subscription.
	h := handler.NewRealtimeHandler(memberWorkspaces(member), source, &realtimeFakePresence{},
		handler.WithHeartbeatInterval(50*time.Millisecond))
	cfg := realtimeAuthConfig()

	client, cleanup := startRealtimeServer(t, newRealtimeTestApp(h, cfg))
	defer cleanup()

	resp, err := client.Do(realtimeStreamRequest(t, cfg, member))
	require.NoError(t, err)
	defer resp.Body.Close() //nolint:errcheck
	require.Equal(t, fiber.StatusOK, resp.StatusCode)
	require.Equal(t, "text/event-stream", resp.Header.Get("Content-Type"))

	lines := realtimeLines(resp.Body)
	require.True(t, waitForLine(t, lines, func(s string) bool { return strings.HasPrefix(s, "retry:") }, 3*time.Second))

	source.seqs <- 42
	require.True(t, waitForLine(t, lines, func(s string) bool { return s == "id: 42" }, 3*time.Second))
	require.True(t, waitForLine(t, lines, func(s string) bool { return s == `data: {"seq":42}` }, 3*time.Second))

	// Closing the stream must cancel the subscription context, releasing the
	// realtime listener subscription and its watcher goroutine.
	require.NoError(t, resp.Body.Close())
	select {
	case ws := <-source.canceled:
		require.Equal(t, "ws_1", ws)
	case <-time.After(5 * time.Second):
		t.Fatal("stream subscription was not cancelled after the client disconnected")
	}
}

func TestStreamSendsHeartbeatComments(t *testing.T) {
	member := uuid.MustParse("99999999-9999-4999-8999-999999999999")
	source := newRealtimeFakeSource()
	h := handler.NewRealtimeHandler(memberWorkspaces(member), source, &realtimeFakePresence{},
		handler.WithHeartbeatInterval(40*time.Millisecond))
	cfg := realtimeAuthConfig()

	client, cleanup := startRealtimeServer(t, newRealtimeTestApp(h, cfg))
	defer cleanup()

	resp, err := client.Do(realtimeStreamRequest(t, cfg, member))
	require.NoError(t, err)
	defer resp.Body.Close() //nolint:errcheck
	require.Equal(t, fiber.StatusOK, resp.StatusCode)

	lines := realtimeLines(resp.Body)
	require.True(t, waitForLine(t, lines, func(s string) bool { return s == ": heartbeat" }, 3*time.Second))
}

// ---- helpers -------------------------------------------------------------

func httptestNewRequest(method, path, body, token string) *http.Request {
	var reader *strings.Reader
	if body == "" {
		reader = strings.NewReader("")
	} else {
		reader = strings.NewReader(body)
	}
	req := httptestRequest(method, path, reader)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return req
}

func httptestRequest(method, path string, body *strings.Reader) *http.Request {
	req, err := http.NewRequest(method, path, body)
	if err != nil {
		panic(err)
	}
	return req
}

func realtimeStreamRequest(t *testing.T, cfg *auth.Config, member uuid.UUID) *http.Request {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, "http://relay/api/v1/workspaces/ws_1/stream", nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+realtimeToken(t, cfg, member))
	req.Header.Set("Accept", "text/event-stream")
	return req
}

// startRealtimeServer serves the app on an in-memory listener so streaming
// responses can be read incrementally (app.Test buffers the whole body).
func startRealtimeServer(t *testing.T, app *fiber.App) (*http.Client, func()) {
	t.Helper()
	ln := fasthttputil.NewInmemoryListener()

	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = app.Listener(ln)
	}()

	client := &http.Client{Transport: &http.Transport{
		DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
			return ln.Dial()
		},
	}}

	cleanup := func() {
		_ = app.Shutdown()
		_ = ln.Close()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
		}
	}
	return client, cleanup
}

func realtimeLines(body io.Reader) <-chan string {
	lines := make(chan string, 64)
	go func() {
		defer close(lines)
		br := bufio.NewReader(body)
		for {
			line, err := br.ReadString('\n')
			if err != nil {
				return
			}
			line = strings.TrimRight(line, "\r\n")
			if line == "" {
				continue
			}
			lines <- line
		}
	}()
	return lines
}

func waitForLine(t *testing.T, lines <-chan string, want func(string) bool, timeout time.Duration) bool {
	t.Helper()
	deadline := time.After(timeout)
	for {
		select {
		case line, ok := <-lines:
			if !ok {
				return false
			}
			if want(line) {
				return true
			}
		case <-deadline:
			return false
		}
	}
}
