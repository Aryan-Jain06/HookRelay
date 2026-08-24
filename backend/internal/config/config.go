// Package config loads process configuration from the environment.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// DefaultJWTSecret is the development placeholder. It is a named constant so
// the production guard can refuse to start when it is still in use.
const DefaultJWTSecret = "dev-only-change-me"

// EnvProduction is the ENVIRONMENT value that turns on the production guards.
const EnvProduction = "production"

// Config is the full configuration surface shared by the API and worker binaries.
type Config struct {
	DatabaseURL string
	RedisURL    string

	APIAddr   string
	JWTSecret string

	// CORSAllowOrigin is the single origin permitted to call the API from a
	// browser. "*" is convenient locally and unacceptable in production.
	CORSAllowOrigin string

	// Stream / consumer-group settings.
	StreamName    string
	ConsumerGroup string
	ConsumerName  string

	// Worker pool.
	WorkerCount     int
	DeliveryTimeout time.Duration

	// Scheduler re-enqueues deliveries whose next_attempt_at has passed.
	SchedulerInterval  time.Duration
	SchedulerBatchSize int

	// Reaper reclaims stream entries abandoned by crashed workers.
	ReaperInterval  time.Duration
	ReaperMinIdle   time.Duration
	ReaperBatchSize int

	// AllowPrivateEndpoints lifts the SSRF guard on delivery, permitting
	// loopback and RFC1918 destinations. It exists so the bundled test receiver
	// is reachable in local development and must stay false in production.
	AllowPrivateEndpoints bool

	// MetricsAddr is the listen address for /metrics. Empty disables it. It is
	// separate from APIAddr so metrics are not exposed through the public
	// ingress alongside tenant data.
	MetricsAddr           string
	MetricsSampleInterval time.Duration

	// Growth control. Zero disables each sweep.
	StreamMaxLen       int64
	StreamTrimInterval time.Duration
	AttemptRetention   time.Duration
	RetentionInterval  time.Duration

	// Rate limits. Zero or negative disables the limiter.
	RateLimitPerTenant   float64
	RateLimitTenantBurst int
	RateLimitPerIP       float64
	RateLimitIPBurst     int

	// Circuit breaker.
	BreakerThreshold int
	BreakerCooldown  time.Duration

	// DeliveryMaxAge is an absolute deadline on a delivery's life. It exists
	// because circuit-breaker skips deliberately do not consume the retry
	// budget: without a deadline, deliveries queued for an endpoint that stays
	// down would be deferred forever instead of dead-lettering.
	DeliveryMaxAge time.Duration

	// RetrySchedule is a comma-separated duration list overriding the default
	// 5s,30s,2m,10m,30m,2h,5h backoff. Compressing it lets a test environment
	// exercise the real dead-letter path in seconds instead of hours.
	RetrySchedule string

	Environment string
}

// Load reads configuration from the environment, applying defaults that make a
// local `docker compose up` work with no .env file at all.
func Load() (*Config, error) {
	cfg := &Config{
		DatabaseURL:           env("DATABASE_URL", "postgres://hookrelay:hookrelay@localhost:5432/hookrelay?sslmode=disable"),
		RedisURL:              env("REDIS_URL", "redis://localhost:6379/0"),
		APIAddr:               env("API_ADDR", ":8080"),
		JWTSecret:             env("JWT_SECRET", DefaultJWTSecret),
		CORSAllowOrigin:       env("CORS_ALLOW_ORIGIN", "*"),
		StreamName:            env("STREAM_NAME", "deliveries_stream"),
		ConsumerGroup:         env("CONSUMER_GROUP", "delivery_workers"),
		ConsumerName:          env("CONSUMER_NAME", defaultConsumerName()),
		Environment:           env("ENVIRONMENT", "development"),
		WorkerCount:           envInt("WORKER_COUNT", 8),
		DeliveryTimeout:       envDuration("DELIVERY_TIMEOUT", 10*time.Second),
		SchedulerInterval:     envDuration("SCHEDULER_INTERVAL", time.Second),
		SchedulerBatchSize:    envInt("SCHEDULER_BATCH_SIZE", 500),
		ReaperInterval:        envDuration("REAPER_INTERVAL", 15*time.Second),
		ReaperMinIdle:         envDuration("REAPER_MIN_IDLE", 60*time.Second),
		ReaperBatchSize:       envInt("REAPER_BATCH_SIZE", 200),
		AllowPrivateEndpoints: envBool("ALLOW_PRIVATE_ENDPOINTS", false),
		MetricsAddr:           env("METRICS_ADDR", ":9100"),
		MetricsSampleInterval: envDuration("METRICS_SAMPLE_INTERVAL", 15*time.Second),
		StreamMaxLen:          int64(envInt("STREAM_MAX_LEN", 1_000_000)),
		StreamTrimInterval:    envDuration("STREAM_TRIM_INTERVAL", 10*time.Minute),
		AttemptRetention:      envDuration("ATTEMPT_RETENTION", 720*time.Hour),
		RetentionInterval:     envDuration("RETENTION_INTERVAL", 6*time.Hour),
		RateLimitPerTenant:    envFloat("RATE_LIMIT_PER_TENANT", 200),
		RateLimitTenantBurst:  envInt("RATE_LIMIT_TENANT_BURST", 400),
		// Deliberately low: /auth/login is bcrypt-backed, so cheap attempts are
		// expensive for us and password guessing must not be free.
		RateLimitPerIP:   envFloat("RATE_LIMIT_PER_IP", 5),
		RateLimitIPBurst: envInt("RATE_LIMIT_IP_BURST", 10),
		BreakerThreshold: envInt("BREAKER_THRESHOLD", 20),
		BreakerCooldown:  envDuration("BREAKER_COOLDOWN", 5*time.Minute),
		RetrySchedule:    env("RETRY_SCHEDULE", ""),
		DeliveryMaxAge:   envDuration("DELIVERY_MAX_AGE", 24*time.Hour),
	}
	if cfg.WorkerCount < 1 {
		return nil, fmt.Errorf("WORKER_COUNT must be >= 1, got %d", cfg.WorkerCount)
	}
	if cfg.SchedulerInterval <= 0 {
		return nil, fmt.Errorf("SCHEDULER_INTERVAL must be > 0")
	}
	return cfg, nil
}

func defaultConsumerName() string {
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "worker"
	}
	return fmt.Sprintf("%s-%d", host, os.Getpid())
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envInt(key string, fallback int) int {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return fallback
	}
	return n
}

// envFloat reads a floating-point setting.
func envFloat(key string, fallback float64) float64 {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return fallback
	}
	return f
}

// envBool reads a boolean flag. Only an explicit "true" enables it, so a typo
// leaves a security-relevant default alone rather than flipping it.
func envBool(key string, fallback bool) bool {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return fallback
	}
	return b
}

func envDuration(key string, fallback time.Duration) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return fallback
	}
	return d
}

// ValidateProduction refuses to run with a configuration that is unsafe on a
// public network. It is a no-op outside ENVIRONMENT=production.
//
// Each of these is a mistake that is easy to make, silent at startup, and
// expensive afterwards, so the process fails fast and names every problem at
// once rather than one per restart.
func (c *Config) ValidateProduction() error {
	if c.Environment != EnvProduction {
		return nil
	}
	var problems []string
	if c.JWTSecret == DefaultJWTSecret {
		problems = append(problems, "JWT_SECRET is still the development default: anyone can mint a dashboard token for any tenant (generate one with `openssl rand -base64 48`)")
	}
	if len(c.JWTSecret) < 32 {
		problems = append(problems, fmt.Sprintf("JWT_SECRET is only %d bytes; use at least 32", len(c.JWTSecret)))
	}
	if c.CORSAllowOrigin == "*" {
		problems = append(problems, "CORS_ALLOW_ORIGIN is \"*\": any website could make authenticated dashboard calls from a visitor's browser (set it to the dashboard origin)")
	}
	if c.AllowPrivateEndpoints {
		problems = append(problems, "ALLOW_PRIVATE_ENDPOINTS is enabled: a tenant could register http://169.254.169.254/ and read this instance's cloud credentials (unset it outside local development)")
	}
	if len(problems) == 0 {
		return nil
	}
	return fmt.Errorf("refusing to start in production with an unsafe configuration:\n  - %s", strings.Join(problems, "\n  - "))
}
