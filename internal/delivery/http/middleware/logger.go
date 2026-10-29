package middleware

import (
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"

	applogger "github.com/abdullahPrasetio/relay-api/pkg/logger"
)

func RequestID() fiber.Handler {
	return func(c *fiber.Ctx) error {
		id := c.Get(fiber.HeaderXRequestID)
		if id == "" {
			id = uuid.New().String()
		}
		c.Set(fiber.HeaderXRequestID, id)

		ctx := applogger.WithRequestID(c.UserContext(), id)
		c.SetUserContext(ctx)

		return c.Next()
	}
}

func RequestLogger() fiber.Handler {
	return func(c *fiber.Ctx) error {
		start := time.Now()
		err := c.Next()
		latency := time.Since(start)

		log := applogger.FromContext(c.UserContext())
		log.Info().
			Str("method", c.Method()).
			Str("path", c.Path()).
			Int("status", c.Response().StatusCode()).
			Dur("latency", latency).
			Str("ip", c.IP()).
			Msg("request")

		return err
	}
}
