package route

import (
	"github.com/gofiber/fiber/v2"

	"github.com/abdullahPrasetio/relay-api/internal/delivery/http/handler"
	"github.com/abdullahPrasetio/relay-api/pkg/auth"
)

// RegisterInviteRoutes mounts invites and the workspace audit feed.
//
// GET /invites/:token is intentionally public: the token itself is the
// capability, and the response only exposes workspace name, role and expiry.
func RegisterInviteRoutes(router fiber.Router, h *handler.InviteHandler, ws *handler.WorkspaceHandler, authCfg *auth.Config) {
	public := router.Group("/invites")
	public.Get("/:token", h.Get)

	invites := router.Group("/invites", auth.Middleware(authCfg))
	invites.Post("/:token/accept", h.Accept)

	workspaces := router.Group("/workspaces", auth.Middleware(authCfg))
	workspaces.Post("/:id/invites", h.Create)
	workspaces.Get("/:id/audit", ws.ListAudit)
}
