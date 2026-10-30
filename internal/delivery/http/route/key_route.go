package route

import (
	"github.com/gofiber/fiber/v2"

	"github.com/abdullahPrasetio/relay-api/internal/delivery/http/handler"
	"github.com/abdullahPrasetio/relay-api/pkg/auth"
)

// RegisterKeyRoutes mounts the device key registry and the encrypted workspace
// key endpoints behind Bearer auth.
//
//	PUT    /devices/keys                  register/replace my device key
//	GET    /devices/keys                  my keys (active and revoked)
//	DELETE /devices/keys/:id              revoke my key (idempotent)
//	GET    /workspaces/:id/device-keys    active recipient keys (any member)
//	POST   /workspaces/:id/keys           store wrapped keys (admin+)
//	GET    /workspaces/:id/keys           my wrapped keys (any member)
//	POST   /workspaces/:id/keys/rotate    allocate version+1 (admin+)
//
// Membership and role checks live in the usecase, so a workspace the caller
// cannot access is indistinguishable from a missing one.
func RegisterKeyRoutes(router fiber.Router, h *handler.KeyHandler, authCfg *auth.Config) {
	devices := router.Group("/devices", auth.Middleware(authCfg))
	devices.Put("/keys", h.Register)
	devices.Get("/keys", h.List)
	devices.Delete("/keys/:id", h.Revoke)

	ws := router.Group("/workspaces", auth.Middleware(authCfg))
	ws.Get("/:id/device-keys", h.ListWorkspaceDeviceKeys)
	ws.Post("/:id/keys", h.PutWorkspaceKeys)
	ws.Get("/:id/keys", h.ListMyWorkspaceKeys)
	ws.Post("/:id/keys/rotate", h.RotateWorkspaceKey)
}
