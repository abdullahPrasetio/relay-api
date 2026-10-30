package route

import (
	"github.com/gofiber/fiber/v2"

	"github.com/abdullahPrasetio/relay-api/internal/delivery/http/handler"
	"github.com/abdullahPrasetio/relay-api/pkg/auth"
)

// RegisterAuthRoutes mounts the public token endpoints and the Bearer-protected
// device/me endpoints under /api/v1/auth.
func RegisterAuthRoutes(router fiber.Router, h *handler.AuthHandler, jwtCfg *auth.Config) {
	authGroup := router.Group("/auth")

	authGroup.Post("/register", h.Register)
	authGroup.Post("/login", h.Login)
	authGroup.Post("/refresh", h.Refresh)
	authGroup.Post("/logout", h.Logout)

	requireAuth := auth.Middleware(jwtCfg)
	authGroup.Get("/devices", requireAuth, h.ListDevices)
	authGroup.Delete("/devices/:id", requireAuth, h.RevokeDevice)
	authGroup.Get("/me", requireAuth, h.Me)
}
