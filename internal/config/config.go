// Package config loads the rk runtime configuration from RK_* environment
// variables.
package config

import (
	"crypto/sha256"
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
	// GoogleClientID is the OAuth client id ID tokens must be issued for
	// (RK_GOOGLE_CLIENT_ID). Empty disables Google sign-in.
	GoogleClientID string
	// GoogleJWKSURL overrides Google's certificate endpoint; empty means the
	// real one. Not read from the environment; tests set it.
	GoogleJWKSURL string

	// PublicURL is the externally reachable base URL of `rk serve`, used to
	// build absolute signed URLs for GPU runners (RK_PUBLIC_URL). When empty
	// the URL is derived from the lease request's Host header.
	PublicURL string
	// RunnerToken authenticates GPU runners on /api/v1/gpu/* (RK_RUNNER_TOKEN).
	// Empty disables the GPU lease API.
	RunnerToken string
	// SigningKey is the HMAC key for signed URLs (RK_SIGNING_KEY). Empty
	// derives a key from RunnerToken.
	SigningKey []byte
	// LeaseTTL is how long a GPU lease lives without a heartbeat (RK_GPU_LEASE_TTL, default 10m).
	LeaseTTL time.Duration
	// SignedURLTTL is the lifetime of signed source/stem URLs (RK_SIGNED_URL_TTL, default 2h).
	SignedURLTTL time.Duration

	// Worker settings.

	// MaxDuration rejects sources longer than this (RK_MAX_DURATION_SECONDS, default 1800).
	MaxDuration time.Duration
	// LocalDemucs makes the worker run `python -m demucs` itself instead of
	// waiting for a GPU runner (RK_LOCAL_DEMUCS=1; development only).
	LocalDemucs bool
	// Python is the interpreter used for tools/tempo/tempo.py and local demucs (RK_PYTHON, default python3).
	Python string
	// ToolsDir holds tempo/tempo.py (RK_TOOLS_DIR, default ./tools).
	ToolsDir string
	// TempoCmd overrides the tempo analyser command entirely (RK_TEMPO_CMD; tests).
	TempoCmd string
	// GPUWaitTimeout bounds how long a job waits for a GPU runner (RK_GPU_WAIT_TIMEOUT, default 3h).
	GPUWaitTimeout time.Duration
	// DemucsDevice is passed to demucs as -d (RK_DEMUCS_DEVICE; default cuda for gpu-agent, cpu for local mode).
	DemucsDevice string
	// WorkerSlots is how many jobs the worker drives through the CPU stages
	// (converting, analyzing) at once (RK_WORKER_SLOTS, default 2).
	WorkerSlots int
}

// Defaults returns the configuration used when no RK_* variables are set.
func Defaults() Config {
	return Config{
		ListenAddr:     ":8080",
		DataDir:        "./data",
		JobRetention:   7 * 24 * time.Hour,
		AnonRetention:  24 * time.Hour,
		MaxUploadBytes: 1 << 30,
		LeaseTTL:       10 * time.Minute,
		SignedURLTTL:   2 * time.Hour,
		MaxDuration:    30 * time.Minute,
		Python:         "python3",
		ToolsDir:       "./tools",
		GPUWaitTimeout: 3 * time.Hour,
		WorkerSlots:    2,
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
	cfg.PublicURL = strings.TrimRight(os.Getenv("RK_PUBLIC_URL"), "/")
	cfg.RunnerToken = os.Getenv("RK_RUNNER_TOKEN")
	if v := os.Getenv("RK_SIGNING_KEY"); v != "" {
		cfg.SigningKey = []byte(v)
	} else if cfg.RunnerToken != "" {
		cfg.SigningKey = DeriveSigningKey(cfg.RunnerToken)
	}
	if v := os.Getenv("RK_PYTHON"); v != "" {
		cfg.Python = v
	}
	if v := os.Getenv("RK_TOOLS_DIR"); v != "" {
		cfg.ToolsDir = v
	}
	cfg.TempoCmd = os.Getenv("RK_TEMPO_CMD")
	cfg.LocalDemucs = os.Getenv("RK_LOCAL_DEMUCS") == "1"
	cfg.DemucsDevice = os.Getenv("RK_DEMUCS_DEVICE")
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
	if v := os.Getenv("RK_WORKER_SLOTS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			return cfg, fmt.Errorf("RK_WORKER_SLOTS: expected a positive integer, got %q", v)
		}
		cfg.WorkerSlots = n
	}
	if v := os.Getenv("RK_MAX_DURATION_SECONDS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			return cfg, fmt.Errorf("RK_MAX_DURATION_SECONDS: expected a positive integer, got %q", v)
		}
		cfg.MaxDuration = time.Duration(n) * time.Second
	}
	for _, d := range []struct {
		env string
		dst *time.Duration
	}{
		{"RK_GPU_LEASE_TTL", &cfg.LeaseTTL},
		{"RK_SIGNED_URL_TTL", &cfg.SignedURLTTL},
		{"RK_GPU_WAIT_TIMEOUT", &cfg.GPUWaitTimeout},
	} {
		if v := os.Getenv(d.env); v != "" {
			dur, err := time.ParseDuration(v)
			if err != nil || dur <= 0 {
				return cfg, fmt.Errorf("%s: expected a positive duration, got %q", d.env, v)
			}
			*d.dst = dur
		}
	}
	return cfg, nil
}

// DeriveSigningKey returns the signed-URL key used when RK_SIGNING_KEY is
// unset: a hash of the runner token, so one secret configures both.
func DeriveSigningKey(runnerToken string) []byte {
	sum := sha256.Sum256([]byte("rk-signing:" + runnerToken))
	return sum[:]
}
