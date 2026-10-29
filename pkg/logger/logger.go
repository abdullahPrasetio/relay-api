package logger

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"gopkg.in/natefinch/lumberjack.v2"
)

type ctxKey int

const requestIDKey ctxKey = iota

func Setup(env, level, filePath string, toFile bool, serviceName string) {
	lvl, err := zerolog.ParseLevel(level)
	if err != nil {
		lvl = zerolog.InfoLevel
	}
	zerolog.SetGlobalLevel(lvl)
	zerolog.TimeFieldFormat = time.RFC3339

	var writers []io.Writer

	if env == "production" {
		writers = append(writers, os.Stdout)
	} else {
		writers = append(writers, zerolog.ConsoleWriter{
			Out:        os.Stdout,
			TimeFormat: time.RFC3339,
		})
	}

	if toFile && filePath != "" {
		if err := os.MkdirAll(filepath.Dir(filePath), 0o750); err == nil {
			writers = append(writers, &lumberjack.Logger{
				Filename:   filePath,
				MaxSize:    100,
				MaxBackups: 7,
				MaxAge:     30,
				Compress:   true,
			})
		}
	}

	mw := io.MultiWriter(writers...)
	log.Logger = zerolog.New(mw).
		With().
		Timestamp().
		Str("service", serviceName).
		Logger()
}

func WithRequestID(ctx context.Context, requestID string) context.Context {
	return context.WithValue(ctx, requestIDKey, requestID)
}

func RequestIDFromContext(ctx context.Context) string {
	if id, ok := ctx.Value(requestIDKey).(string); ok {
		return id
	}
	return ""
}

func FromContext(ctx context.Context) zerolog.Logger {
	if rid := RequestIDFromContext(ctx); rid != "" {
		return log.With().Str("request_id", rid).Logger()
	}
	return log.Logger
}
