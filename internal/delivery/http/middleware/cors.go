package middleware

import (
	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/cors"
)

func CORS(allowedOrigins string) fiber.Handler {
	if allowedOrigins == "" {
		allowedOrigins = "http://localhost:3000"
	}
	return cors.New(cors.Config{
		AllowOrigins:     allowedOrigins,
		AllowMethods:     "GET,POST,PUT,DELETE,OPTIONS",
		AllowHeaders:     "Origin,Content-Type,Accept,Authorization,X-Request-ID",
		ExposeHeaders:    "X-Request-ID",
		AllowCredentials: false,
		MaxAge:           300,
	})
}
