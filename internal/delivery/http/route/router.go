package route

import (
	"github.com/gofiber/fiber/v2"

	"github.com/abdullahPrasetio/relay-api/internal/delivery/http/handler"
	"github.com/abdullahPrasetio/relay-api/pkg/observability"
)

func Setup(app *fiber.App, userHandler *handler.UserHandler, healthHandler *handler.HealthHandler, appEnv string) {
	app.Get("/", welcomeHandler(appEnv))
	app.Get("/health", healthHandler.Check)
	app.Get("/metrics", prodGuard(appEnv), observability.MetricsHandler())

	v1 := app.Group("/api/v1")
	RegisterUserRoutes(v1, userHandler)
}

// prodGuard returns 404 for internal endpoints when APP_ENV=production.
func prodGuard(appEnv string) fiber.Handler {
	return func(c *fiber.Ctx) error {
		if appEnv == "production" {
			return fiber.ErrNotFound
		}
		return c.Next()
	}
}

func welcomeHandler(appEnv string) fiber.Handler {
	return func(c *fiber.Ctx) error {
		links := fiber.Map{"health": "/health"}
		if appEnv != "production" {
			links["metrics"] = "/metrics"
		}
		return c.JSON(fiber.Map{
			"service": "wapgo",
			"version": "1.0.0",
			"env":     appEnv,
			"links":   links,
		})
	}
}
