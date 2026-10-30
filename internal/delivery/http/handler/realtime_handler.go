package handler

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"

	"github.com/abdullahPrasetio/relay-api/internal/realtime"
	"github.com/abdullahPrasetio/relay-api/internal/usecase"
	"github.com/abdullahPrasetio/relay-api/pkg/response"
)

const (
	defaultHeartbeatInterval = 25 * time.Second
	defaultPresenceTTL       = 60 * time.Second
	maxEntityIDLength        = 256
)

// RealtimeHandler serves the SSE change stream and workspace presence.
//
// The stream is a wake-up signal, not a data channel: it only says "the
// workspace moved to sequence N". Clients then pull /workspaces/:id/changes
// with their own cursor, which makes dropped connections and dropped
// notifications harmless.
type RealtimeHandler struct {
	workspaces  usecase.WorkspaceUseCase
	changes     realtime.ChangeSource
	presence    realtime.PresenceStore
	heartbeat   time.Duration
	presenceTTL time.Duration
	now         func() time.Time
}

// RealtimeOption customises the realtime handler.
type RealtimeOption func(*RealtimeHandler)

// WithHeartbeatInterval overrides the SSE comment heartbeat (default 25s).
func WithHeartbeatInterval(d time.Duration) RealtimeOption {
	return func(h *RealtimeHandler) {
		if d > 0 {
			h.heartbeat = d
		}
	}
}

// WithPresenceTTL overrides how long a presence row counts as "online"
// (default 60s).
func WithPresenceTTL(d time.Duration) RealtimeOption {
	return func(h *RealtimeHandler) {
		if d > 0 {
			h.presenceTTL = d
		}
	}
}

// WithRealtimeClock overrides the clock (tests and deterministic TTL checks).
func WithRealtimeClock(fn func() time.Time) RealtimeOption {
	return func(h *RealtimeHandler) {
		if fn != nil {
			h.now = fn
		}
	}
}

// NewRealtimeHandler wires the workspace usecase, the change source and the
// presence store into the HTTP layer.
func NewRealtimeHandler(
	workspaces usecase.WorkspaceUseCase,
	changes realtime.ChangeSource,
	presence realtime.PresenceStore,
	opts ...RealtimeOption,
) *RealtimeHandler {
	h := &RealtimeHandler{
		workspaces:  workspaces,
		changes:     changes,
		presence:    presence,
		heartbeat:   defaultHeartbeatInterval,
		presenceTTL: defaultPresenceTTL,
		now:         time.Now,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(h)
		}
	}
	return h
}

// errRealtimeUnauthorized marks a missing or invalid Bearer token. It is kept
// separate from usecase errors so the caller can write the right envelope.
var errRealtimeUnauthorized = errors.New("realtime: unauthorized")

// authorize resolves the caller and verifies viewer+ membership. It does not
// write the response: callers map the error through writeRealtimeError so a
// failed authorization never falls through into the handler body.
func (h *RealtimeHandler) authorize(c *fiber.Ctx) (uuid.UUID, string, error) {
	uid, ok := callerID(c)
	if !ok {
		return uuid.Nil, "", errRealtimeUnauthorized
	}
	workspaceID := strings.TrimSpace(c.Params("id"))
	if workspaceID == "" {
		return uuid.Nil, "", usecase.ErrNotFound
	}
	// GetWorkspace requires the pull permission, so viewer and above pass and
	// non-members get the same 404 as a missing workspace.
	if _, err := h.workspaces.GetWorkspace(c.UserContext(), uid, workspaceID); err != nil {
		return uuid.Nil, "", err
	}
	return uid, workspaceID, nil
}

// writeRealtimeError maps authorize/usecase errors onto the API envelope.
func writeRealtimeError(c *fiber.Ctx, err error) error {
	if errors.Is(err, errRealtimeUnauthorized) {
		return response.Unauthorized(c)
	}
	return workspaceError(c, err)
}

// Stream serves GET /workspaces/:id/stream as Server-Sent Events.
//
// Every notification becomes:
//
//	id: <seq>
//	data: {"seq":<seq>}
//
// plus a `: heartbeat` comment every heartbeat interval. Last-Event-ID is
// accepted but deliberately advisory: clients resume by pulling the delta log
// with their own cursor instead of relying on a replay buffer here.
func (h *RealtimeHandler) Stream(c *fiber.Ctx) error {
	_, workspaceID, err := h.authorize(c)
	if err != nil {
		return writeRealtimeError(c, err)
	}
	if h.changes == nil {
		return response.InternalError(c)
	}

	// The writer runs after this handler returns, so the subscription context
	// is cancelled from inside the writer (or when the listener closes the
	// channel) rather than by a deferred cancel here.
	ctx, cancel := context.WithCancel(c.UserContext())
	seqs, err := h.changes.Listen(ctx, workspaceID)
	if err != nil {
		cancel()
		return response.InternalError(c)
	}

	c.Set(fiber.HeaderContentType, "text/event-stream")
	c.Set(fiber.HeaderCacheControl, "no-cache")
	c.Set(fiber.HeaderConnection, "keep-alive")
	c.Set("X-Accel-Buffering", "no")
	c.Status(fiber.StatusOK)

	// Last-Event-ID is advisory only; see the doc comment above.
	_ = c.Get("Last-Event-ID")

	heartbeat := h.heartbeat
	if heartbeat <= 0 {
		heartbeat = defaultHeartbeatInterval
	}
	requestDone := c.Context().Done()

	c.Context().SetBodyStreamWriter(func(w *bufio.Writer) {
		defer cancel()

		// NOTE: the RequestCtx is recycled once this handler returns, so the
		// writer goroutine must not touch c.Context(); everything it needs is
		// captured before SetBodyStreamWriter is called. fasthttp applies
		// fiber.Config.WriteTimeout to the whole response write, so the server
		// must be configured with WriteTimeout: 0 (or a value longer than any
		// stream) for SSE connections to survive.
		//
		// Opening comment + retry hint flushes the headers immediately so
		// proxies and clients see an established stream before seq 0.
		if _, err := w.WriteString("retry: 3000\n\n"); err != nil {
			return
		}
		if err := w.Flush(); err != nil {
			return
		}

		ticker := time.NewTicker(heartbeat)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-requestDone:
				return
			case seq, ok := <-seqs:
				if !ok {
					return
				}
				data, err := json.Marshal(fiber.Map{"seq": seq})
				if err != nil {
					return
				}
				if _, err := fmt.Fprintf(w, "id: %d\ndata: %s\n\n", seq, data); err != nil {
					return
				}
				if err := w.Flush(); err != nil {
					return
				}
			case <-ticker.C:
				if _, err := w.WriteString(": heartbeat\n\n"); err != nil {
					return
				}
				if err := w.Flush(); err != nil {
					return
				}
			}
		}
	})

	return nil
}

// PostPresence upserts the caller's presence in a workspace.
//
// body: {"entityId": "req_…"} (optional; omitted/null clears it)
func (h *RealtimeHandler) PostPresence(c *fiber.Ctx) error {
	uid, workspaceID, err := h.authorize(c)
	if err != nil {
		return writeRealtimeError(c, err)
	}
	if h.presence == nil {
		return response.InternalError(c)
	}

	var req struct {
		EntityID *string `json:"entityId"`
	}
	if len(c.Body()) > 0 {
		if err := c.BodyParser(&req); err != nil {
			return response.BadRequest(c, "invalid request body")
		}
	}

	entityID := normalizeEntityID(req.EntityID)
	if entityID != nil && len(*entityID) > maxEntityIDLength {
		return response.ValidationError(c, "entityId is too long")
	}

	record := realtime.Presence{
		WorkspaceID: workspaceID,
		UserID:      uid,
		EntityID:    entityID,
		LastSeen:    h.now().UTC(),
	}
	if err := h.presence.UpsertPresence(c.UserContext(), record); err != nil {
		return response.InternalError(c)
	}
	return response.Success(c, "presence recorded", record)
}

// GetPresence lists workspace members seen within the presence TTL.
func (h *RealtimeHandler) GetPresence(c *fiber.Ctx) error {
	_, workspaceID, err := h.authorize(c)
	if err != nil {
		return writeRealtimeError(c, err)
	}
	if h.presence == nil {
		return response.InternalError(c)
	}

	ttl := h.presenceTTL
	if ttl <= 0 {
		ttl = defaultPresenceTTL
	}
	since := h.now().UTC().Add(-ttl)

	members, err := h.presence.PresenceSince(c.UserContext(), workspaceID, since)
	if err != nil {
		return response.InternalError(c)
	}
	if members == nil {
		members = []realtime.Presence{}
	}
	return response.Success(c, "presence", fiber.Map{
		"workspaceId": workspaceID,
		"ttlSeconds":  int(ttl / time.Second),
		"members":     members,
	})
}

// normalizeEntityID trims the optional entity id and folds the empty string to
// "no entity selected".
func normalizeEntityID(raw *string) *string {
	if raw == nil {
		return nil
	}
	v := strings.TrimSpace(*raw)
	if v == "" {
		return nil
	}
	return &v
}
