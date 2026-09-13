// Package config loads process configuration from the environment exactly once.
// Nothing regulatory lives here — see internal/settings for effective-dated
// regulatory values (plan.md 3.7).
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Environment names.
const (
	EnvDevelopment = "development"
	EnvProduction  = "production"
)

// Config is immutable after Load.
type Config struct {
	AppEnv   string
	HTTPAddr string

	DatabaseURL string

	CORSOrigins        []string
	SecureCookies      bool
	SessionTTL         time.Duration
	SessionIdleTimeout time.Duration

	// TimezoneName is the business timezone. Timestamps are stored UTC; business
	// dates are interpreted here (ARCHITECTURE.md, Time).
	TimezoneName string
	location     *time.Location

	StorageDir   string
	GotenbergURL string

	// Backups. An empty BackupDir means no backups run at all, which the
	// system reports rather than hides.
	BackupDir           string
	BackupRetentionDays int
	BackupHour          int
	BackupMinute        int
	PgDumpPath          string

	// JobPollInterval is how often the background worker looks for due work.
	// A few seconds is right in production; tests shorten it so a queued job
	// does not add seconds to every run.
	JobPollInterval time.Duration

	// SMTP. An empty host means email is simply not available; the UI says so
	// rather than offering a send that cannot work.
	SMTPHost     string
	SMTPPort     int
	SMTPUsername string
	SMTPPassword string
	SMTPFrom     string
	SMTPFromName string
	SMTPStartTLS bool

	BootstrapOwnerEmail    string
	BootstrapOwnerPassword string
	BootstrapOwnerName     string
}

// IsProduction reports whether production-only hardening must apply.
func (c *Config) IsProduction() bool { return c.AppEnv == EnvProduction }

// Location returns the business timezone. This is the single place a UTC
// instant becomes an Asia/Jerusalem business date.
func (c *Config) Location() *time.Location { return c.location }

// Load reads the environment and validates it. It fails fast: a misconfigured
// financial system must not start half-configured.
func Load() (*Config, error) {
	c := &Config{
		AppEnv:                 env("APP_ENV", EnvDevelopment),
		HTTPAddr:               env("HTTP_ADDR", ":8080"),
		DatabaseURL:            env("DATABASE_URL", ""),
		TimezoneName:           env("TIMEZONE", "Asia/Jerusalem"),
		StorageDir:             env("STORAGE_DIR", "./storage"),
		GotenbergURL:           env("GOTENBERG_URL", ""),
		BootstrapOwnerEmail:    strings.TrimSpace(env("BOOTSTRAP_OWNER_EMAIL", "")),
		BootstrapOwnerPassword: env("BOOTSTRAP_OWNER_PASSWORD", ""),
		BootstrapOwnerName:     strings.TrimSpace(env("BOOTSTRAP_OWNER_NAME", "")),
		SMTPHost:               strings.TrimSpace(env("SMTP_HOST", "")),
		SMTPUsername:           env("SMTP_USERNAME", ""),
		SMTPPassword:           env("SMTP_PASSWORD", ""),
		SMTPFrom:               strings.TrimSpace(env("SMTP_FROM", "")),
		SMTPFromName:           strings.TrimSpace(env("SMTP_FROM_NAME", "")),
		BackupDir:              strings.TrimSpace(env("BACKUP_DIR", "")),
		PgDumpPath:             strings.TrimSpace(env("PG_DUMP_PATH", "")),
	}

	if c.DatabaseURL == "" {
		return nil, fmt.Errorf("DATABASE_URL is required")
	}
	if c.AppEnv != EnvDevelopment && c.AppEnv != EnvProduction {
		return nil, fmt.Errorf("APP_ENV must be %q or %q, got %q", EnvDevelopment, EnvProduction, c.AppEnv)
	}

	for _, o := range strings.Split(env("CORS_ORIGINS", ""), ",") {
		if o = strings.TrimSpace(o); o != "" {
			if o == "*" {
				return nil, fmt.Errorf("CORS_ORIGINS must not be \"*\": cookie authentication requires an explicit allow-list")
			}
			c.CORSOrigins = append(c.CORSOrigins, o)
		}
	}

	var err error
	if c.SecureCookies, err = envBool("SECURE_COOKIES", false); err != nil {
		return nil, err
	}
	if c.IsProduction() && !c.SecureCookies {
		return nil, fmt.Errorf("SECURE_COOKIES must be true in production")
	}

	ttlHours, err := envInt("SESSION_TTL_HOURS", 12)
	if err != nil {
		return nil, err
	}
	idleMinutes, err := envInt("SESSION_IDLE_TIMEOUT_MINUTES", 60)
	if err != nil {
		return nil, err
	}
	if ttlHours <= 0 || idleMinutes <= 0 {
		return nil, fmt.Errorf("session lifetimes must be positive")
	}
	c.SessionTTL = time.Duration(ttlHours) * time.Hour
	c.SessionIdleTimeout = time.Duration(idleMinutes) * time.Minute

	if c.location, err = time.LoadLocation(c.TimezoneName); err != nil {
		return nil, fmt.Errorf("load timezone %q: %w", c.TimezoneName, err)
	}

	if c.BackupRetentionDays, err = envInt("BACKUP_RETENTION_DAYS", 30); err != nil {
		return nil, err
	}
	if c.BackupRetentionDays < 1 {
		return nil, fmt.Errorf("BACKUP_RETENTION_DAYS must be at least 1")
	}
	// Written as HH:MM so the operator sets one value, not two.
	backupAt := strings.TrimSpace(env("BACKUP_AT", "02:00"))
	if c.BackupHour, c.BackupMinute, err = parseClock(backupAt); err != nil {
		return nil, fmt.Errorf("BACKUP_AT: %w", err)
	}

	pollMilliseconds, err := envInt("JOB_POLL_INTERVAL_MS", 5000)
	if err != nil {
		return nil, err
	}
	if pollMilliseconds < 50 {
		return nil, fmt.Errorf("JOB_POLL_INTERVAL_MS must be at least 50")
	}
	c.JobPollInterval = time.Duration(pollMilliseconds) * time.Millisecond

	if c.SMTPPort, err = envInt("SMTP_PORT", 587); err != nil {
		return nil, err
	}
	if c.SMTPStartTLS, err = envBool("SMTP_STARTTLS", true); err != nil {
		return nil, err
	}
	// Half-configured email is a deployment mistake worth failing on, rather
	// than discovering when the first document will not send.
	if (c.SMTPHost == "") != (c.SMTPFrom == "") {
		return nil, fmt.Errorf("SMTP_HOST and SMTP_FROM must be set together")
	}

	// A bootstrap owner is all-or-nothing; a half-set pair is a deployment
	// mistake worth failing on rather than silently ignoring.
	if (c.BootstrapOwnerEmail == "") != (c.BootstrapOwnerPassword == "") {
		return nil, fmt.Errorf("BOOTSTRAP_OWNER_EMAIL and BOOTSTRAP_OWNER_PASSWORD must be set together")
	}

	return c, nil
}

// parseClock reads an HH:MM time of day.
func parseClock(value string) (hour, minute int, err error) {
	parsed, err := time.Parse("15:04", value)
	if err != nil {
		return 0, 0, fmt.Errorf("must be HH:MM, got %q", value)
	}
	return parsed.Hour(), parsed.Minute(), nil
}

func env(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return def
}

func envBool(key string, def bool) (bool, error) {
	raw, ok := os.LookupEnv(key)
	if !ok || raw == "" {
		return def, nil
	}
	v, err := strconv.ParseBool(raw)
	if err != nil {
		return false, fmt.Errorf("%s must be a boolean: %w", key, err)
	}
	return v, nil
}

func envInt(key string, def int) (int, error) {
	raw, ok := os.LookupEnv(key)
	if !ok || raw == "" {
		return def, nil
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("%s must be an integer: %w", key, err)
	}
	return v, nil
}
