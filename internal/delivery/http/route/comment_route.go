package route

import (
	"github.com/gofiber/fiber/v2"

	"github.com/abdullahPrasetio/relay-api/internal/delivery/http/handler"
	"github.com/abdullahPrasetio/relay-api/pkg/auth"
)

// RegisterCommentRoutes mounts the per-entity comment feed under the workspace
// and body/resolve/delete under the comment id. Membership and author/admin
// checks happen inside the usecase.
func RegisterCommentRoutes(router fiber.Router, h *handler.CommentHandler, authCfg *auth.Config) {
	ws := router.Group("/workspaces", auth.Middleware(authCfg))
	ws.Get("/:id/comments", h.List)
	ws.Post("/:id/comments", h.Create)

	comments := router.Group("/comments", auth.Middleware(authCfg))
	comments.Patch("/:id", h.Update)
	comments.Delete("/:id", h.Delete)
}
