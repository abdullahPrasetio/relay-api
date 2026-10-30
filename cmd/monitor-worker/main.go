// Command monitor-worker executes due monitors from the Postgres queue.
//
// It is a separate process from the API so a slow request cannot block the
// HTTP server. The worker claims rows with FOR UPDATE SKIP LOCKED, advances
// next_run_at before executing, and runs the Node core bundle with the same
// variable/script semantics as the desktop app.
package main

import (
	"context"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/abdullahPrasetio/relay-api/config"
	"github.com/abdullahPrasetio/relay-api/internal/monitor"
	dbrepo "github.com/abdullahPrasetio/relay-api/internal/repository/db"
	"github.com/abdullahPrasetio/relay-api/pkg/database"
	applogger "github.com/abdullahPrasetio/relay-api/pkg/logger"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatal().Err(err).Msg("failed to load config")
	}
	applogger.Setup(cfg.App.Env, cfg.Log.Level, cfg.Log.FilePath, cfg.Log.ToFile, "monitor-worker")

	db, err := database.NewConnection(&cfg.DB)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to connect to database")
	}
	sqlDB, err := db.DB()
	if err != nil {
		log.Fatal().Err(err).Msg("failed to get underlying sql.DB")
	}

	repo := dbrepo.NewMonitorRepository(db)
	executor := monitor.NewExecutor(
		repo,
		monitor.NewCommandRunnerFromEnv(),
		monitor.NewWebhookNotifier(),
		time.Now,
	)
	worker := monitor.NewWorker(repo, executor, monitor.WorkerOptions{
		PollInterval: durationEnv("MONITOR_POLL_INTERVAL", 5*time.Second),
		BatchSize:    intEnv("MONITOR_BATCH_SIZE", 10),
		Now:          time.Now,
	})

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	log.Info().
		Str("poll_interval", durationEnv("MONITOR_POLL_INTERVAL", 5*time.Second).String()).
		Int("batch_size", intEnv("MONITOR_BATCH_SIZE", 10)).
		Msg("monitor worker started")

	worker.Run(ctx)

	log.Info().Msg("monitor worker stopped")
	_ = sqlDB.Close()
}

// durationEnv parses a Go duration, falling back to def so a typo never
// prevents the worker from starting.
func durationEnv(key string, def time.Duration) time.Duration {
	raw := os.Getenv(key)
	if raw == "" {
		return def
	}
	parsed, err := time.ParseDuration(raw)
	if err != nil || parsed <= 0 {
		log.Warn().Str("key", key).Str("value", raw).Dur("fallback", def).Msg("invalid duration")
		return def
	}
	return parsed
}

func intEnv(key string, def int) int {
	raw := os.Getenv(key)
	if raw == "" {
		return def
	}
	parsed, err := strconv.Atoi(raw)
	if err != nil || parsed <= 0 {
		log.Warn().Str("key", key).Str("value", raw).Int("fallback", def).Msg("invalid integer")
		return def
	}
	return parsed
}
