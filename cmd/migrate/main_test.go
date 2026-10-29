package main

import (
	"testing"

	gomysql "github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/abdullahPrasetio/relay-api/config"
)

func TestBuildDSNPostgres(t *testing.T) {
	driver, dsn, err := buildDSN(&config.DBConfig{
		Driver:   "postgres",
		Host:     "db.internal",
		Port:     "5432",
		Name:     "relay",
		User:     "app",
		Password: "p@ss/w:rd",
	})
	require.NoError(t, err)
	assert.Equal(t, "postgres", driver)
	assert.Contains(t, dsn, "postgres://app:p%40ss%2Fw%3Ard@db.internal:5432/relay")
	assert.Contains(t, dsn, "sslmode=disable")
}

func TestBuildDSNPostgresDefaults(t *testing.T) {
	driver, dsn, err := buildDSN(&config.DBConfig{
		Host: "localhost",
		Name: "relay",
		User: "app",
	})
	require.NoError(t, err)
	assert.Equal(t, "postgres", driver)
	assert.Contains(t, dsn, "localhost:5432")
	assert.Contains(t, dsn, "sslmode=disable")
}

func TestBuildDSNPostgresSSLMode(t *testing.T) {
	_, dsn, err := buildDSN(&config.DBConfig{
		Driver:  "postgres",
		Host:    "localhost",
		Port:    "5432",
		Name:    "relay",
		User:    "app",
		SSLMode: "require",
	})
	require.NoError(t, err)
	assert.Contains(t, dsn, "sslmode=require")
}

func TestBuildDSNMysql(t *testing.T) {
	driver, dsn, err := buildDSN(&config.DBConfig{
		Driver:   "mysql",
		Host:     "localhost",
		Name:     "relay",
		User:     "app",
		Password: "secret",
	})
	require.NoError(t, err)
	assert.Equal(t, "mysql", driver)

	parsed, err := gomysql.ParseDSN(dsn)
	require.NoError(t, err)
	assert.Equal(t, "app", parsed.User)
	assert.Equal(t, "secret", parsed.Passwd)
	assert.Equal(t, "tcp", parsed.Net)
	assert.Equal(t, "localhost:3306", parsed.Addr)
	assert.Equal(t, "relay", parsed.DBName)
	assert.True(t, parsed.ParseTime)
	assert.True(t, parsed.MultiStatements, "golang-migrate mysql requires multiStatements")
}

func TestBuildDSNUnsupportedDriver(t *testing.T) {
	_, _, err := buildDSN(&config.DBConfig{Driver: "sqlite"})
	require.Error(t, err)
}
