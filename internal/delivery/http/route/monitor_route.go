package route

import (
	"github.com/gofiber/fiber/v2"

	"github.com/abdullahPrasetio/relay-api/internal/delivery/http/handler"
	"github.com/abdullahPrasetio/relay-api/pkg/auth"
)

// RegisterMonitorRoutes mounts monitor CRUD under the workspace, and run
// history/manual execution under the monitor id. Membership and role checks
// happen inside the usecase.
func RegisterMonitorRoutes(router fiber.Router, h *handler.MonitorHandler, authCfg *auth.Config) {
	ws := router.Group("/workspaces", auth.Middleware(authCfg))
	ws.Get("/:id/monitors", h.List)
	ws.Post("/:id/monitors", h.Create)

	monitors := router.Group("/monitors", auth.Middleware(authCfg))
	monitors.Get("/:id", h.Get)
	monitors.Patch("/:id", h.Update)
	monitors.Delete("/:id", h.Delete)
	monitors.Get("/:id/runs", h.ListRuns)
	monitors.Post("/:id/run", h.Run)
}
