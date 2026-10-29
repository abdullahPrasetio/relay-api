package database

import (
	"database/sql"
	"fmt"
	"time"

	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"github.com/abdullahPrasetio/relay-api/config"
)

func NewConnection(cfg *config.DBConfig) (*gorm.DB, error) {
	dialector, err := buildDialector(cfg)
	if err != nil {
		return nil, err
	}

	db, err := gorm.Open(dialector, &gorm.Config{
		Logger:                 gormlogger.Default.LogMode(gormlogger.Silent),
		PrepareStmt:            true,
		SkipDefaultTransaction: true,
	})
	if err != nil {
		return nil, fmt.Errorf("opening db connection: %w", err)
	}

	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("getting underlying sql.DB: %w", err)
	}

	if err := configurePool(sqlDB, cfg); err != nil {
		return nil, err
	}

	return db, nil
}

func buildDialector(cfg *config.DBConfig) (gorm.Dialector, error) {
	if cfg.Host == "" {
		return nil, fmt.Errorf("DB_HOST is required but not set")
	}
	if cfg.Name == "" {
		return nil, fmt.Errorf("DB_NAME is required but not set")
	}
	if cfg.User == "" {
		return nil, fmt.Errorf("DB_USER is required but not set")
	}

	switch cfg.Driver {
	case "mysql":
		tls := "false"
		if cfg.SSLMode == "require" {
			tls = "true"
		}
		port := cfg.Port
		if port == "" {
			port = "3306"
		}
		dsn := fmt.Sprintf(
			"%s:%s@tcp(%s:%s)/%s?charset=utf8mb4&parseTime=True&loc=UTC&tls=%s",
			cfg.User, cfg.Password, cfg.Host, port, cfg.Name, tls,
		)
		return mysql.Open(dsn), nil

	case "postgres", "":
		sslMode := cfg.SSLMode
		if sslMode == "" {
			sslMode = "disable"
		}
		port := cfg.Port
		if port == "" {
			port = "5432"
		}
		dsn := fmt.Sprintf(
			"postgres://%s:%s@%s:%s/%s?sslmode=%s&TimeZone=UTC",
			cfg.User, cfg.Password, cfg.Host, port, cfg.Name, sslMode,
		)
		return postgres.Open(dsn), nil

	default:
		return nil, fmt.Errorf("unsupported DB driver: %q", cfg.Driver)
	}
}

func configurePool(sqlDB *sql.DB, cfg *config.DBConfig) error {
	if cfg.MaxOpenConns > 0 {
		sqlDB.SetMaxOpenConns(cfg.MaxOpenConns)
	}
	if cfg.MaxIdleConns > 0 {
		sqlDB.SetMaxIdleConns(cfg.MaxIdleConns)
	}
	if cfg.ConnMaxLife != "" {
		d, err := time.ParseDuration(cfg.ConnMaxLife)
		if err != nil {
			return fmt.Errorf("invalid conn_max_life %q: %w", cfg.ConnMaxLife, err)
		}
		sqlDB.SetConnMaxLifetime(d)
	}
	return nil
}
