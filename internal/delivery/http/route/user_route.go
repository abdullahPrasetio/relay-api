package route

import (
	"github.com/gofiber/fiber/v2"

	"github.com/abdullahPrasetio/relay-api/internal/delivery/http/handler"
)

func RegisterUserRoutes(router fiber.Router, h *handler.UserHandler) {
	users := router.Group("/users")
	users.Get("/", h.ListUsers)
	users.Get("/:id", h.GetUser)
	users.Post("/", h.CreateUser)
	users.Put("/:id", h.UpdateUser)
	users.Delete("/:id", h.DeleteUser)
}
