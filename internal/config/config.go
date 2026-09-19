// Package config loads the rk runtime configuration from RK_* environment
// variables.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config is the process-wide configuration shared by all rk subcommands.
type Config struct {
	// ListenAddr is the HTTP listen address for `rk serve` (RK_LISTEN_ADDR).
	ListenAddr string
	// DataDir is the storage root; jobs live under DataDir/jobs/<id>/ (RK_DATA_DIR).
	DataDir string
	// DatabaseURL is the Postgres DSN (RK_DATABASE_URL).
	DatabaseURL string
	// CORSOrigins lists allowed browser origins; empty means same-origin only (RK_CORS_ORIGINS).
	CORSOrigins []string
	// JobRetention is how long a signed-in user's job is kept (RK_JOB_RETENTION_DAYS, default 7).
	JobRetention time.Duration
	// AnonRetention is how long an anonymous job is kept (fixed at 24h by the design contract).
	AnonRetention time.Duration
	// MaxUploadBytes caps a multipart upload (RK_MAX_UPLOAD_BYTES, default 1 GiB).
	MaxUploadBytes int64
	// GoogleClientID is reported by /api/v1/config; Google sign-in itself is not implemented yet.
	GoogleClientID string
}

// Defaults returns the configuration used when no RK_* variables are set.
func Defaults() Config {
	return Config{
		ListenAddr:     ":8080",
		DataDir:        "./data",
		JobRetention:   7 * 24 * time.Hour,
		AnonRetention:  24 * time.Hour,
		MaxUploadBytes: 1 << 30,
	}
}

// FromEnv builds a Config from the environment on top of Defaults.
func FromEnv() (Config, error) {
	cfg := Defaults()
	if v := os.Getenv("RK_LISTEN_ADDR"); v != "" {
		cfg.ListenAddr = v
	}
	if v := os.Getenv("RK_DATA_DIR"); v != "" {
		cfg.DataDir = v
	}
	cfg.DatabaseURL = os.Getenv("RK_DATABASE_URL")
	cfg.GoogleClientID = os.Getenv("RK_GOOGLE_CLIENT_ID")
	if v := os.Getenv("RK_CORS_ORIGINS"); v != "" {
		for _, o := range strings.Split(v, ",") {
			if o = strings.TrimSpace(o); o != "" {
				cfg.CORSOrigins = append(cfg.CORSOrigins, o)
			}
		}
	}
	if v := os.Getenv("RK_JOB_RETENTION_DAYS"); v != "" {
		d, err := strconv.Atoi(v)
		if err != nil || d < 1 {
			return cfg, fmt.Errorf("RK_JOB_RETENTION_DAYS: expected a positive integer, got %q", v)
		}
		cfg.JobRetention = time.Duration(d) * 24 * time.Hour
	}
	if v := os.Getenv("RK_MAX_UPLOAD_BYTES"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 1 {
			return cfg, fmt.Errorf("RK_MAX_UPLOAD_BYTES: expected a positive integer, got %q", v)
		}
		cfg.MaxUploadBytes = n
	}
	return cfg, nil
}
