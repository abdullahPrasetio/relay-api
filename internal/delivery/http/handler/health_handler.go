package handler

import (
	"context"
	"database/sql"
	"time"

	"github.com/gofiber/fiber/v2"
)

type Checker func(ctx context.Context) string

type HealthHandler struct {
	db        *sql.DB
	extras    map[string]Checker
	startTime time.Time
	version   string
}

func NewHealthHandler(db *sql.DB, startTime time.Time, version string) *HealthHandler {
	return &HealthHandler{
		db:        db,
		startTime: startTime,
		version:   version,
	}
}

// AddChecker registers an additional dependency health check (redis, kafka, …).
func (h *HealthHandler) AddChecker(name string, fn Checker) *HealthHandler {
	if h.extras == nil {
		h.extras = make(map[string]Checker)
	}
	h.extras[name] = fn
	return h
}

func (h *HealthHandler) Check(c *fiber.Ctx) error {
	services := make(map[string]string)
	overall := "ok"
	httpCode := fiber.StatusOK

	dbCtx, dbCancel := context.WithTimeout(c.UserContext(), 2*time.Second)
	defer dbCancel()
	if err := h.db.PingContext(dbCtx); err != nil {
		services["database"] = "down"
		overall = "degraded"
		httpCode = fiber.StatusServiceUnavailable
	} else {
		services["database"] = "ok"
	}

	for name, check := range h.extras {
		result := check(c.UserContext())
		services[name] = result
		if result == "down" {
			overall = "degraded"
			httpCode = fiber.StatusServiceUnavailable
		}
	}

	return c.Status(httpCode).JSON(fiber.Map{
		"status":   overall,
		"services": services,
		"version":  h.version,
		"uptime":   time.Since(h.startTime).Round(time.Second).String(),
	})
}
