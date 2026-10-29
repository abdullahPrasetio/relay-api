// Command migrate applies the embedded SQL migrations to the configured
// database. It is the only supported way to change the schema outside local
// development (DB_AUTO_MIGRATE stays dev-only).
//
// Usage:
//
//	go run ./cmd/migrate -dir up                 # apply all pending
//	go run ./cmd/migrate -dir up -steps 1        # apply one step
//	go run ./cmd/migrate -dir down               # revert one step
//	go run ./cmd/migrate -dir version            # show version + dirty flag
//	go run ./cmd/migrate -dir force -version 3   # clear a dirty state
package main

import (
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/url"

	gomysql "github.com/go-sql-driver/mysql"
	"github.com/golang-migrate/migrate/v4"
	migratedb "github.com/golang-migrate/migrate/v4/database"
	migratemysql "github.com/golang-migrate/migrate/v4/database/mysql"
	migratepostgres "github.com/golang-migrate/migrate/v4/database/postgres"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/rs/zerolog/log"

	"github.com/abdullahPrasetio/relay-api/config"
	"github.com/abdullahPrasetio/relay-api/migrations"
)

func main() {
	dir := flag.String("dir", "up", "action: up, down, version, force")
	steps := flag.Int("steps", 0, "migrations to apply/revert; 0 = all for up, 1 for down")
	target := flag.Int("version", -1, "target version for -dir force")
	flag.Parse()

	cfg, err := config.Load()
	if err != nil {
		log.Fatal().Err(err).Msg("failed to load config")
	}

	driver, dsn, err := buildDSN(&cfg.DB)
	if err != nil {
		log.Fatal().Err(err).Msg("invalid database config")
	}

	m, db, err := newMigrator(driver, dsn)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to initialise migrator")
	}
	defer func() { _ = db.Close() }()

	if err := run(m, *dir, *steps, *target); err != nil {
		log.Fatal().Err(err).Str("driver", driver).Msg("migration failed")
	}
}

// buildDSN returns the database/sql driver name and DSN for the configured
// driver. Credentials are escaped properly for both engines.
func buildDSN(cfg *config.DBConfig) (string, string, error) {
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
			User:     url.UserPassword(cfg.User, cfg.Password),
			Host:     net.JoinHostPort(cfg.Host, port),
			Path:     "/" + cfg.Name,
			RawQuery: url.Values{"sslmode": {sslmode}}.Encode(),
		}
		return "postgres", u.String(), nil

	case "mysql":
		port := cfg.Port
		if port == "" {
			port = "3306"
		}
		c := gomysql.NewConfig()
		c.User = cfg.User
		c.Passwd = cfg.Password
		c.Net = "tcp"
		c.Addr = net.JoinHostPort(cfg.Host, port)
		c.DBName = cfg.Name
		c.ParseTime = true
		c.MultiStatements = true // required by the golang-migrate mysql driver
		return "mysql", c.FormatDSN(), nil

	default:
		return "", "", fmt.Errorf("unsupported DB driver %q (want postgres or mysql)", cfg.Driver)
	}
}

// newMigrator wires the embedded source to a live database handle. The
// returned *sql.DB must be closed by the caller.
func newMigrator(driver, dsn string) (*migrate.Migrate, *sql.DB, error) {
	src, err := iofs.New(migrations.FS, ".")
	if err != nil {
		return nil, nil, fmt.Errorf("load embedded migrations: %w", err)
	}

	db, err := sql.Open(driver, dsn)
	if err != nil {
		return nil, nil, fmt.Errorf("open database: %w", err)
	}
	// One connection is enough for migrations and avoids competing advisory
	// locks when several instances start at the same time.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, nil, fmt.Errorf("database unreachable: %w", err)
	}

	var dm migratedb.Driver
	switch driver {
	case "postgres":
		dm, err = migratepostgres.WithInstance(db, &migratepostgres.Config{})
	case "mysql":
		dm, err = migratemysql.WithInstance(db, &migratemysql.Config{})
	default:
		err = fmt.Errorf("unsupported driver %q", driver)
	}
	if err != nil {
		_ = db.Close()
		return nil, nil, fmt.Errorf("init migrate driver: %w", err)
	}

	m, err := migrate.NewWithInstance("iofs", src, driver, dm)
	if err != nil {
		_ = db.Close()
		return nil, nil, fmt.Errorf("init migrator: %w", err)
	}
	return m, db, nil
}

func run(m *migrate.Migrate, dir string, steps, target int) error {
	if err := exec(m, dir, steps, target); err != nil {
		return withDirtyHint(m, err)
	}
	return nil
}

func exec(m *migrate.Migrate, dir string, steps, target int) error {
	switch dir {
	case "up":
		if steps > 0 {
			return report("up", m.Steps(steps))
		}
		return report("up", m.Up())

	case "down":
		n := steps
		if n <= 0 {
			n = 1
		}
		return report("down", m.Steps(-n))

	case "version":
		v, dirty, err := m.Version()
		if errors.Is(err, migrate.ErrNilVersion) {
			fmt.Println("version: none")
			return nil
		}
		if err != nil {
			return err
		}
		fmt.Printf("version: %d (dirty=%t)\n", v, dirty)
		return nil

	case "force":
		if target < 0 {
			return errors.New("-version is required for -dir force")
		}
		return report("force", m.Force(target))

	default:
		return fmt.Errorf("unknown -dir %q (want up, down, version, force)", dir)
	}
}

// report prints the outcome and passes the error through so the caller can
// decide how to handle it (ErrNoChange is a success for every action).
func report(action string, err error) error {
	if err == nil {
		fmt.Printf("%s: done\n", action)
		return nil
	}
	if errors.Is(err, migrate.ErrNoChange) {
		fmt.Printf("%s: no change\n", action)
		return nil
	}
	return err
}

// withDirtyHint explains how to recover from a half-applied migration, which
// leaves the schema_migrations row dirty and blocks every later run.
func withDirtyHint(m *migrate.Migrate, err error) error {
	v, dirty, verr := m.Version()
	if verr != nil || !dirty {
		return err
	}
	return fmt.Errorf("%w\ndatabase is dirty at version %d: repair the failed migration, then run: go run ./cmd/migrate -dir force -version %d", err, v, v)
}
