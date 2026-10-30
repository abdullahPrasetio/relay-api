package route

import (
	"github.com/gofiber/fiber/v2"

	"github.com/abdullahPrasetio/relay-api/internal/delivery/http/handler"
	"github.com/abdullahPrasetio/relay-api/pkg/auth"
)

// RegisterRealtimeRoutes mounts the SSE change stream and workspace presence.
// Membership is checked inside every handler (viewer+), so a workspace the
// caller cannot see is indistinguishable from a missing one.
func RegisterRealtimeRoutes(router fiber.Router, h *handler.RealtimeHandler, authCfg *auth.Config) {
	ws := router.Group("/workspaces", auth.Middleware(authCfg))

	ws.Get("/:id/stream", h.Stream)
	ws.Post("/:id/presence", h.PostPresence)
	ws.Get("/:id/presence", h.GetPresence)
}
