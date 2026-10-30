package main

import (
	"context"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/rs/zerolog/log"

	"github.com/abdullahPrasetio/relay-api/config"
	"github.com/abdullahPrasetio/relay-api/internal/delivery/http/handler"
	mw "github.com/abdullahPrasetio/relay-api/internal/delivery/http/middleware"
	"github.com/abdullahPrasetio/relay-api/internal/delivery/http/route"
	"github.com/abdullahPrasetio/relay-api/internal/domain/entity"
	"github.com/abdullahPrasetio/relay-api/internal/monitor"
	"github.com/abdullahPrasetio/relay-api/internal/realtime"
	dbrepo "github.com/abdullahPrasetio/relay-api/internal/repository/db"
	"github.com/abdullahPrasetio/relay-api/internal/usecase"
	"github.com/abdullahPrasetio/relay-api/pkg/auth"
	"github.com/abdullahPrasetio/relay-api/pkg/database"
	applogger "github.com/abdullahPrasetio/relay-api/pkg/logger"
	"github.com/abdullahPrasetio/relay-api/pkg/observability"
	"github.com/abdullahPrasetio/relay-api/pkg/validator"
)

const version = "0.1.0"

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatal().Err(err).Msg("failed to load config")
	}

	applogger.Setup(cfg.App.Env, cfg.Log.Level, cfg.Log.FilePath, cfg.Log.ToFile, cfg.App.Name)

	// Structured sinks → logs/{api,consumer,thirdparty,trace}.log
	if err := applogger.SetupSinks(applogger.SinkConfig{
		Dir:        cfg.Log.Dir,
		Rotation:   cfg.Log.Rotation,
		MaxAgeDays: cfg.Log.MaxAgeDays,
		Console:    cfg.App.Env != "production",
	}); err != nil {
		log.Fatal().Err(err).Msg("failed to setup log sinks")
	}

	log.Info().Str("version", version).Str("env", cfg.App.Env).
		Str("observability", cfg.Observability.Provider).Msg("starting service")

	// Observability provider: "otel", "elastic_apm", or "none"
	obsProvider, err := observability.New(context.Background(), &cfg.Observability, cfg.App.Name, version, cfg.App.Env)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to setup observability provider")
	}

	db, err := database.NewConnection(&cfg.DB)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to connect to database")
	}
	if err := obsProvider.InstrumentGORM(db); err != nil {
		log.Warn().Err(err).Msg("gorm instrumentation failed")
	}
	sqlDB, err := db.DB()
	if err != nil {
		log.Fatal().Err(err).Msg("failed to get underlying sql.DB")
	}

	if cfg.DB.AutoMigrate {
		if cfg.App.Env == "production" {
			log.Fatal().Msg("DB_AUTO_MIGRATE is not allowed in production; run ./cmd/migrate instead")
		}
		log.Warn().Msg("DB_AUTO_MIGRATE is enabled: development only, use ./cmd/migrate for real schema changes")
		if err := db.AutoMigrate(&entity.User{}, &entity.Session{}); err != nil {
			log.Fatal().Err(err).Msg("auto-migrate failed")
		}
	}

	userRepo := dbrepo.NewUserRepository(db)
	userUC := usecase.NewUserUseCase(userRepo)
	val := validator.New()

	accessTTL := durationOr(cfg.JWT.AccessTTL, usecase.DefaultAccessTTL)
	authCfg := &auth.Config{
		Secret:   cfg.JWT.Secret,
		Issuer:   cfg.JWT.Issuer,
		Audience: cfg.JWT.Audience,
		Expiry:   accessTTL,
	}
	if len(authCfg.Secret) < 32 {
		log.Warn().Int("bytes", len(authCfg.Secret)).
			Msg("JWT_SECRET is shorter than 32 bytes: token signing will fail")
	}
	authUC := usecase.NewAuthUseCase(userRepo, userUC, dbrepo.NewSessionRepository(db), authCfg, usecase.AuthOptions{
		AccessTTL:   accessTTL,
		RefreshTTL:  durationOr(cfg.JWT.RefreshTTL, usecase.DefaultRefreshTTL),
		GracePeriod: durationOr(cfg.JWT.RefreshGrace, usecase.DefaultRefreshGrace),
	})

	workspaceRepo := dbrepo.NewWorkspaceRepository(db)

	// Realtime: pg_notify after a committed push wakes SSE subscribers; clients
	// still pull with their own cursor, so a lost notification is harmless.
	notifier := realtime.NewNotifier(sqlDB)
	realtimeCtx, stopRealtime := context.WithCancel(context.Background())
	listener, err := realtime.NewListenerFromConfig(realtimeCtx, &cfg.DB)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to start realtime listener")
	}
	presence := realtime.NewPresenceStore(sqlDB)

	workspaceUC := usecase.WithChangeNotifier(usecase.NewWorkspaceUseCase(workspaceRepo), notifier)
	inviteUC := usecase.NewInviteUseCase(dbrepo.NewInviteRepository(db), workspaceRepo)

	monitorRepo := dbrepo.NewMonitorRepository(db)
	monitorExecutor := monitor.NewExecutor(
		monitorRepo,
		monitor.NewCommandRunnerFromEnv(),
		monitor.NewWebhookNotifier(),
		time.Now,
	)
	monitorUC := usecase.NewMonitorUseCase(workspaceRepo, monitorRepo, monitorExecutor)

	startTime := time.Now()
	userHandler := handler.NewUserHandler(userUC, val)
	authHandler := handler.NewAuthHandler(authUC, val)
	workspaceHandler := handler.NewWorkspaceHandler(workspaceUC)
	inviteHandler := handler.NewInviteHandler(inviteUC)
	realtimeHandler := handler.NewRealtimeHandler(workspaceUC, listener, presence)
	monitorHandler := handler.NewMonitorHandler(monitorUC)
	healthHandler := handler.NewHealthHandler(sqlDB, startTime, version)

	app := fiber.New(fiber.Config{
		AppName:     cfg.App.Name,
		BodyLimit:   4 * 1024 * 1024,
		ReadTimeout: 10 * time.Second,
		// 0 disables the write deadline: SSE streams stay open until the
		// client disconnects (heartbeats keep intermediaries alive).
		WriteTimeout:          0,
		IdleTimeout:           120 * time.Second,
		DisableStartupMessage: true,
		ErrorHandler: func(c *fiber.Ctx, err error) error {
			code := fiber.StatusInternalServerError
			if e, ok := err.(*fiber.Error); ok {
				code = e.Code
			}
			return c.Status(code).JSON(fiber.Map{
				"status":  false,
				"message": http.StatusText(code),
			})
		},
	})

	app.Use(mw.Recover())
	app.Use(mw.RequestID())
	app.Use(mw.SecurityHeaders())
	app.Use(mw.RateLimiter())
	app.Use(mw.RequestLogger())
	app.Use(mw.CORS(cfg.App.CORSAllowedOrigins))
	app.Use(obsProvider.HTTPMiddleware())
	app.Use(observability.MetricsMiddleware())
	// Full request/response journal → api.log (after obs so trace_id is available).
	app.Use(mw.AccessLog(mw.AccessLogOptions{
		BodyMaxBytes:  cfg.Log.BodyMaxBytes,
		CaptureBodies: cfg.Log.HTTPBodies,
	}))

	route.Setup(app, userHandler, authHandler, workspaceHandler, inviteHandler, realtimeHandler, monitorHandler, healthHandler, authCfg, cfg.App.Env)

	go func() {
		addr := ":" + cfg.App.Port
		log.Info().Str("addr", addr).Msg("http server listening")
		if err := app.Listen(addr); err != nil {
			log.Fatal().Err(err).Msg("http server error")
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, os.Interrupt, syscall.SIGTERM)
	<-quit

	log.Info().Msg("shutdown signal received")

	shutCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := app.ShutdownWithContext(shutCtx); err != nil {
		log.Error().Err(err).Msg("http server forced shutdown")
	}

	if err := obsProvider.Shutdown(shutCtx); err != nil {
		log.Error().Err(err).Msg("observability provider shutdown error")
	}
	stopRealtime()
	_ = listener.Close()
	_ = notifier.Close()
	sqlDB.Close() //nolint:errcheck

	log.Info().Msg("shutdown complete")
}

// durationOr parses a Go duration string, falling back to def when unset or
// invalid so a typo in an optional TTL never prevents the API from booting.
func durationOr(raw string, def time.Duration) time.Duration {
	if raw == "" {
		return def
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		log.Warn().Str("value", raw).Dur("fallback", def).Msg("invalid duration config")
		return def
	}
	return d
}
