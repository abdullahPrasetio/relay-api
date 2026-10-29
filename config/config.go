package config

import (
	"fmt"
	"strings"

	"github.com/joho/godotenv"
	"github.com/spf13/viper"
)

type Config struct {
	App           AppConfig           `mapstructure:"app"`
	DB            DBConfig            `mapstructure:"db"`
	Redis         RedisConfig         `mapstructure:"redis"`
	Kafka         KafkaConfig         `mapstructure:"kafka"`
	RabbitMQ      RabbitMQConfig      `mapstructure:"rabbitmq"`
	Log           LogConfig           `mapstructure:"log"`
	Services      ServiceURLs         `mapstructure:"services"`
	JWT           JWTConfig           `mapstructure:"jwt"`
	Observability ObservabilityConfig `mapstructure:"observability"`
	Health        HealthConfig        `mapstructure:"health"`
	Notification  NotificationConfig  `mapstructure:"notification"`
}

// NotificationConfig groups optional notification add-on settings.
// Both sub-sections are zero-valued when the add-on is not used.
type NotificationConfig struct {
	SMTP     SMTPConfig     `mapstructure:"smtp"`
	Firebase FirebaseConfig `mapstructure:"firebase"`
}

// SMTPConfig holds parameters for the pkg/notification/email add-on.
type SMTPConfig struct {
	Host     string `mapstructure:"host"`     // SMTP_HOST
	Port     int    `mapstructure:"port"`     // SMTP_PORT  (default 587)
	Username string `mapstructure:"username"` // SMTP_USERNAME
	Password string `mapstructure:"password"` // SMTP_PASSWORD
	From     string `mapstructure:"from"`     // SMTP_FROM
	Timeout  string `mapstructure:"timeout"`  // SMTP_TIMEOUT  Go duration (default "10s")
}

// FirebaseConfig holds parameters for the pkg/notification/firebase add-on.
type FirebaseConfig struct {
	// CredentialsJSON is the full JSON content of the Firebase service account key.
	// Set via FIREBASE_CREDENTIALS_JSON (preferred in Kubernetes Secrets).
	CredentialsJSON string `mapstructure:"credentials_json"`
}

// HealthConfig controls health check probe behaviour.
type HealthConfig struct {
	// ProbeTimeout is the per-dependency timeout (DB ping, Redis ping, extras).
	// Env: HEALTH_PROBE_TIMEOUT (Go duration string, e.g. "2s"). Default: "2s".
	ProbeTimeout string `mapstructure:"probe_timeout"`
}

// JWTConfig holds parameters for HS256 token signing and verification.
type JWTConfig struct {
	Secret   string `mapstructure:"secret"`
	Issuer   string `mapstructure:"issuer"`
	Audience string `mapstructure:"audience"`
	Expiry   string `mapstructure:"expiry"`
}

// ObservabilityConfig controls which observability backend is used.
//   - "otel" (default) — OpenTelemetry SDK
//   - "elastic_apm"    — Elastic APM Go agent (reads ELASTIC_APM_* ENV vars natively)
type ObservabilityConfig struct {
	Provider       string `mapstructure:"provider"`
	TracingEnabled bool   `mapstructure:"tracing_enabled"`
	OTLPEndpoint   string `mapstructure:"otlp_endpoint"`
}

type AppConfig struct {
	Env                string `mapstructure:"env"`
	Port               string `mapstructure:"port"`
	Name               string `mapstructure:"name"`
	CORSAllowedOrigins string `mapstructure:"cors_allowed_origins"`
}

type DBConfig struct {
	Driver       string `mapstructure:"driver"`
	Host         string `mapstructure:"host"`
	Port         string `mapstructure:"port"`
	Name         string `mapstructure:"name"`
	User         string `mapstructure:"user"`
	Password     string `mapstructure:"password"`
	MaxOpenConns int    `mapstructure:"max_open_conns"`
	MaxIdleConns int    `mapstructure:"max_idle_conns"`
	ConnMaxLife  string `mapstructure:"conn_max_life"`
	AutoMigrate  bool   `mapstructure:"auto_migrate"`
	SSLMode      string `mapstructure:"sslmode"`
}

type RedisConfig struct {
	URL          string `mapstructure:"url"`
	Password     string `mapstructure:"password"`
	DB           int    `mapstructure:"db"`
	PoolSize     int    `mapstructure:"pool_size"`
	MinIdleConns int    `mapstructure:"min_idle_conns"`
	DialTimeout  string `mapstructure:"dial_timeout"`
	ReadTimeout  string `mapstructure:"read_timeout"`
	WriteTimeout string `mapstructure:"write_timeout"`
	MaxRetries   int    `mapstructure:"max_retries"`
}

type KafkaConfig struct {
	Brokers           string `mapstructure:"brokers"`
	GroupID           string `mapstructure:"group_id"`
	HeartbeatInterval string `mapstructure:"heartbeat_interval"` // KAFKA_HEARTBEAT_INTERVAL default "3s"
	SessionTimeout    string `mapstructure:"session_timeout"`    // KAFKA_SESSION_TIMEOUT    default "30s"
	RebalanceTimeout  string `mapstructure:"rebalance_timeout"`  // KAFKA_REBALANCE_TIMEOUT  default "30s"
}

type RabbitMQConfig struct {
	DSN      string `mapstructure:"dsn"`
	Exchange string `mapstructure:"exchange"`
}

type LogConfig struct {
	Level    string `mapstructure:"level"`
	ToFile   bool   `mapstructure:"to_file"`
	FilePath string `mapstructure:"file_path"`

	// Structured sinks (api/consumer/thirdparty/trace).
	Dir          string `mapstructure:"dir"`            // directory for the 4 sink files (default "logs")
	Rotation     string `mapstructure:"rotation"`       // "size" | "daily"
	MaxAgeDays   int    `mapstructure:"max_age_days"`   // retention for sink files
	BodyMaxBytes int    `mapstructure:"body_max_bytes"` // per-body cap in access/thirdparty logs
	HTTPBodies   bool   `mapstructure:"http_bodies"`    // capture request/response bodies in api.log
}

func Load() (*Config, error) {
	// Load .env silently — missing file is fine (production uses real ENV vars)
	_ = godotenv.Load()

	v := viper.New()

	v.SetConfigName("config")
	v.SetConfigType("yaml")
	v.AddConfigPath("./config")
	v.AddConfigPath(".")

	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()

	bindings := map[string]string{
		"app.env":                       "APP_ENV",
		"app.port":                      "APP_PORT",
		"app.name":                      "APP_NAME",
		"app.cors_allowed_origins":      "APP_CORS_ALLOWED_ORIGINS",
		"db.driver":                     "DB_DRIVER",
		"db.host":                       "DB_HOST",
		"db.port":                       "DB_PORT",
		"db.name":                       "DB_NAME",
		"db.user":                       "DB_USER",
		"db.password":                   "DB_PASSWORD",
		"db.max_open_conns":             "DB_MAX_OPEN_CONNS",
		"db.max_idle_conns":             "DB_MAX_IDLE_CONNS",
		"db.conn_max_life":              "DB_CONN_MAX_LIFE",
		"db.auto_migrate":               "DB_AUTO_MIGRATE",
		"db.sslmode":                    "DB_SSL_MODE",
		"redis.url":            "REDIS_URL",
		"redis.password":       "REDIS_PASSWORD",
		"redis.db":             "REDIS_DB",
		"redis.pool_size":      "REDIS_POOL_SIZE",
		"redis.min_idle_conns": "REDIS_MIN_IDLE_CONNS",
		"redis.dial_timeout":   "REDIS_DIAL_TIMEOUT",
		"redis.read_timeout":   "REDIS_READ_TIMEOUT",
		"redis.write_timeout":  "REDIS_WRITE_TIMEOUT",
		"redis.max_retries":    "REDIS_MAX_RETRIES",
		"kafka.brokers":             "KAFKA_BROKERS",
		"kafka.group_id":            "KAFKA_GROUP_ID",
		"kafka.heartbeat_interval": "KAFKA_HEARTBEAT_INTERVAL",
		"kafka.session_timeout":    "KAFKA_SESSION_TIMEOUT",
		"kafka.rebalance_timeout":  "KAFKA_REBALANCE_TIMEOUT",
		"rabbitmq.dsn":                  "RABBITMQ_DSN",
		"rabbitmq.exchange":             "RABBITMQ_EXCHANGE",
		"log.level":                     "LOG_LEVEL",
		"log.to_file":                   "LOG_TO_FILE",
		"log.file_path":                 "LOG_FILE_PATH",
		"log.dir":                       "LOG_DIR",
		"log.rotation":                  "LOG_ROTATION",
		"log.max_age_days":              "LOG_MAX_AGE_DAYS",
		"log.body_max_bytes":            "LOG_BODY_MAX_BYTES",
		"log.http_bodies":               "LOG_HTTP_BODIES",
		"services.user_url":             "USER_SERVICE_URL",
		"services.order_url":            "ORDER_SERVICE_URL",
		"jwt.secret":                    "JWT_SECRET",
		"jwt.issuer":                    "JWT_ISSUER",
		"jwt.audience":                  "JWT_AUDIENCE",
		"jwt.expiry":                    "JWT_EXPIRY",
		"observability.provider":                    "OBSERVABILITY_PROVIDER",
		"observability.tracing_enabled":             "OTEL_TRACING_ENABLED",
		"observability.otlp_endpoint":               "OTEL_EXPORTER_OTLP_ENDPOINT",
		"health.probe_timeout":                      "HEALTH_PROBE_TIMEOUT",
		"notification.smtp.host":                    "SMTP_HOST",
		"notification.smtp.port":                    "SMTP_PORT",
		"notification.smtp.username":                "SMTP_USERNAME",
		"notification.smtp.password":                "SMTP_PASSWORD",
		"notification.smtp.from":                    "SMTP_FROM",
		"notification.smtp.timeout":                 "SMTP_TIMEOUT",
		"notification.firebase.credentials_json":    "FIREBASE_CREDENTIALS_JSON",
	}
	for key, env := range bindings {
		if err := v.BindEnv(key, env); err != nil {
			return nil, fmt.Errorf("binding env %s: %w", env, err)
		}
	}

	v.SetDefault("app.env", "development")
	v.SetDefault("app.port", "8080")
	v.SetDefault("app.name", "service")
	v.SetDefault("app.cors_allowed_origins", "http://localhost:3000")
	v.SetDefault("db.driver", "postgres")
	v.SetDefault("db.host", "localhost")
	v.SetDefault("db.port", "5432")
	v.SetDefault("db.max_open_conns", 25)
	v.SetDefault("db.max_idle_conns", 5)
	v.SetDefault("db.conn_max_life", "5m")
	v.SetDefault("db.auto_migrate", false)
	v.SetDefault("db.sslmode", "disable")
	v.SetDefault("redis.url", "redis://localhost:6379")
	v.SetDefault("redis.db", 0)
	v.SetDefault("redis.pool_size", 20)
	v.SetDefault("redis.min_idle_conns", 5)
	v.SetDefault("redis.dial_timeout", "5s")
	v.SetDefault("redis.read_timeout", "3s")
	v.SetDefault("redis.write_timeout", "3s")
	v.SetDefault("redis.max_retries", 3)
	v.SetDefault("log.level", "info")
	v.SetDefault("log.to_file", false)
	v.SetDefault("log.file_path", "logs/app.log")
	v.SetDefault("log.dir", "logs")
	v.SetDefault("log.rotation", "size")
	v.SetDefault("log.max_age_days", 30)
	v.SetDefault("log.body_max_bytes", 16384)
	v.SetDefault("log.http_bodies", true)
	v.SetDefault("jwt.issuer", "service")
	v.SetDefault("jwt.audience", "api")
	v.SetDefault("jwt.expiry", "24h")
	v.SetDefault("observability.provider", "otel")
	v.SetDefault("observability.tracing_enabled", false)
	v.SetDefault("health.probe_timeout", "2s")
	v.SetDefault("notification.smtp.port", 587)
	v.SetDefault("notification.smtp.timeout", "10s")
	v.SetDefault("kafka.heartbeat_interval", "3s")
	v.SetDefault("kafka.session_timeout", "30s")
	v.SetDefault("kafka.rebalance_timeout", "30s")

	if err := v.ReadInConfig(); err != nil {
		if _, ok := err.(viper.ConfigFileNotFoundError); !ok {
			return nil, fmt.Errorf("reading config: %w", err)
		}
	}

	var cfg Config
	if err := v.Unmarshal(&cfg); err != nil {
		return nil, fmt.Errorf("unmarshalling config: %w", err)
	}

	return &cfg, nil
}
