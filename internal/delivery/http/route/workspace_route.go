package route

import (
	"github.com/gofiber/fiber/v2"

	"github.com/abdullahPrasetio/relay-api/internal/delivery/http/handler"
	"github.com/abdullahPrasetio/relay-api/pkg/auth"
)

// RegisterWorkspaceRoutes mounts the workspace API behind Bearer auth. Every
// route resolves membership inside the usecase, so a workspace the caller
// cannot access is indistinguishable from a missing one.
func RegisterWorkspaceRoutes(router fiber.Router, h *handler.WorkspaceHandler, authCfg *auth.Config) {
	ws := router.Group("/workspaces", auth.Middleware(authCfg))

	ws.Post("/", h.Create)
	ws.Get("/", h.List)
	ws.Get("/:id", h.Get)
	ws.Patch("/:id", h.Rename)
	ws.Delete("/:id", h.Delete)

	ws.Get("/:id/members", h.ListMembers)
	ws.Post("/:id/members", h.AddMember)
	ws.Patch("/:id/members/:userId", h.UpdateMemberRole)
	ws.Delete("/:id/members/:userId", h.RemoveMember)

	ws.Get("/:id/changes", h.Pull)
	ws.Post("/:id/changes", h.Push)
}
