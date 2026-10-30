package realtime

import (
	"errors"
	"fmt"
	"net"
	"net/url"

	"github.com/abdullahPrasetio/relay-api/config"
)

// ErrInvalidDSN is returned when no usable PostgreSQL DSN can be built.
var ErrInvalidDSN = errors.New("realtime: invalid database DSN")

// DSNFromConfig builds a lib/pq/pgx compatible PostgreSQL URL from the API
// database configuration. Postgres is required: LISTEN/NOTIFY and the presence
// queries are PostgreSQL-only, so a misconfigured MySQL driver fails loudly
// instead of silently disabling realtime.
func DSNFromConfig(cfg *config.DBConfig) (string, error) {
	if cfg == nil {
		return "", fmt.Errorf("%w: nil database config", ErrInvalidDSN)
	}
	if cfg.Host == "" {
		return "", fmt.Errorf("%w: DB_HOST is empty", ErrInvalidDSN)
	}
	if cfg.Name == "" {
		return "", fmt.Errorf("%w: DB_NAME is empty", ErrInvalidDSN)
	}
	if cfg.User == "" {
		return "", fmt.Errorf("%w: DB_USER is empty", ErrInvalidDSN)
	}

	switch cfg.Driver {
	case "postgres", "":
		port := cfg.Port
		if port == "" {
			port = "5432"
		}
		sslmode := cfg.SSLMode
		if sslmode == "" {
			sslmode = "disable"
		}

		u := url.URL{
			Scheme:   "postgres",
			Host:     net.JoinHostPort(cfg.Host, port),
			Path:     "/" + cfg.Name,
			RawQuery: url.Values{"sslmode": {sslmode}}.Encode(),
		}
		if cfg.Password != "" {
			u.User = url.UserPassword(cfg.User, cfg.Password)
		} else {
			u.User = url.User(cfg.User)
		}
		return u.String(), nil
	default:
		return "", fmt.Errorf("%w: pg_notify requires the postgres driver, got %q", ErrInvalidDSN, cfg.Driver)
	}
}
