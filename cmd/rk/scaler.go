package main

import (
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/BeFeast/RehearseKit/internal/db"
	"github.com/BeFeast/RehearseKit/internal/gpu"
	"github.com/BeFeast/RehearseKit/internal/scaler"
)

func runGPUScaler(args []string) error {
	fs := flag.NewFlagSet("gpu-scaler", flag.ExitOnError)
	apiURL := fs.String("api", os.Getenv("RK_API_URL"), "base URL of rk serve as seen from this host (or RK_API_URL)")
	token := fs.String("token", os.Getenv("RK_RUNNER_TOKEN"), "runner token (or RK_RUNNER_TOKEN)")
	dbURL := fs.String("db", os.Getenv("RK_DATABASE_URL"), "Postgres DSN for reading the queue when the server predates /api/v1/gpu/queue (or RK_DATABASE_URL; optional)")
	interval := fs.Duration("interval", envDuration("RK_SCALER_INTERVAL", 30*time.Second), "tick (or RK_SCALER_INTERVAL)")
	idle := fs.Duration("idle", envDuration("RK_SCALER_IDLE", 10*time.Minute), "destroy after the queue has been empty this long (or RK_SCALER_IDLE)")
	maxAge := fs.Duration("max-age", envDuration("RK_SCALER_MAX_AGE", 6*time.Hour), "destroy an instance older than this (or RK_SCALER_MAX_AGE)")
	bootTimeout := fs.Duration("boot-timeout", envDuration("RK_SCALER_BOOT_TIMEOUT", 15*time.Minute), "destroy an instance that is not running after this (or RK_SCALER_BOOT_TIMEOUT)")
	cooldown := fs.Duration("rent-cooldown", envDuration("RK_SCALER_RENT_COOLDOWN", 2*time.Minute), "wait after a failed rent (or RK_SCALER_RENT_COOLDOWN)")
	minCredit := fs.Float64("min-credit", envFloat("RK_SCALER_MIN_CREDIT", 5), "do not rent when the vast.ai credit is below this many dollars (or RK_SCALER_MIN_CREDIT)")
	image := fs.String("image", envOr("RK_SCALER_IMAGE", "ghcr.io/kossoy/rk-gpu-runner:latest"), "runner image (or RK_SCALER_IMAGE)")
	login := fs.String("docker-login", os.Getenv("RK_SCALER_DOCKER_LOGIN"), "vast --login string for the image registry (or RK_SCALER_DOCKER_LOGIN; default: derived from docker config.json)")
	dockerConfig := fs.String("docker-config", os.Getenv("RK_SCALER_DOCKER_CONFIG"), "docker config.json to take the registry login from (or RK_SCALER_DOCKER_CONFIG; default ~/.docker/config.json)")
	offerQuery := fs.String("offer-query", envOr("RK_SCALER_OFFER_QUERY", scaler.DefaultOfferQuery), "vastai search offers query (or RK_SCALER_OFFER_QUERY)")
	disk := fs.Int("disk", envInt("RK_SCALER_DISK", 30), "instance disk in GB (or RK_SCALER_DISK)")
	label := fs.String("label", envOr("RK_SCALER_LABEL", scaler.DefaultLabel), "label on rented instances (or RK_SCALER_LABEL)")
	runnerEnv := fs.String("runner-env", os.Getenv("RK_SCALER_RUNNER_ENV"), "extra -e K=V for the runner container, e.g. \"-e RK_DEMUCS_ARGS=--segment 7\" (or RK_SCALER_RUNNER_ENV)")
	tunnelPort := fs.Int("tunnel-port", envInt("RK_SCALER_TUNNEL_PORT", 18080), "port bound on the instance loopback for the reverse tunnel (or RK_SCALER_TUNNEL_PORT)")
	tunnelTarget := fs.String("tunnel-target", os.Getenv("RK_SCALER_TUNNEL_TARGET"), "host:port the tunnel forwards to (or RK_SCALER_TUNNEL_TARGET; default: host of --api)")
	noTunnel := fs.Bool("no-tunnel", os.Getenv("RK_SCALER_NO_TUNNEL") == "1", "the runner reaches --api directly; do not open ssh (or RK_SCALER_NO_TUNNEL=1)")
	sshKey := fs.String("ssh-key", os.Getenv("RK_SCALER_SSH_KEY"), "identity file for the tunnel (or RK_SCALER_SSH_KEY)")
	stateDir := fs.String("state-dir", os.Getenv("RK_SCALER_STATE_DIR"), "state directory (or RK_SCALER_STATE_DIR; default $XDG_STATE_HOME/rk-gpu-scaler)")
	vastBin := fs.String("vastai", envOr("RK_SCALER_VASTAI", "vastai"), "vastai CLI (or RK_SCALER_VASTAI)")
	vastKey := fs.String("vast-api-key", os.Getenv("VAST_API_KEY"), "vast.ai API key (or VAST_API_KEY; default: the CLI's own key file)")
	vastKeyFile := fs.String("vast-api-key-file", os.Getenv("VAST_API_KEY_FILE"), "file holding the vast.ai API key (or VAST_API_KEY_FILE)")
	once := fs.Bool("once", false, "run one tick and exit")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *apiURL == "" || *token == "" {
		return errors.New("RK_API_URL and RK_RUNNER_TOKEN are required")
	}
	u, err := url.Parse(*apiURL)
	if err != nil || u.Host == "" {
		return fmt.Errorf("RK_API_URL %q is not a URL", *apiURL)
	}
	if *tunnelTarget == "" {
		host, port := u.Hostname(), u.Port()
		if port == "" {
			port = "80"
			if u.Scheme == "https" {
				port = "443"
			}
		}
		*tunnelTarget = host + ":" + port
	}
	if *stateDir == "" {
		base := os.Getenv("XDG_STATE_HOME")
		if base == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				return err
			}
			base = filepath.Join(home, ".local", "state")
		}
		*stateDir = filepath.Join(base, "rk-gpu-scaler")
	}
	if *login == "" {
		l, err := scaler.LoginFromDockerConfig(*dockerConfig, *image)
		if err != nil {
			slog.Warn("gpu-scaler: no registry login; the image must be public", "err", err)
		} else {
			*login = l
		}
	}
	if *vastKey == "" && *vastKeyFile != "" {
		b, err := os.ReadFile(*vastKeyFile)
		if err != nil {
			return err
		}
		*vastKey = strings.TrimSpace(string(b))
	}

	ctx, cancel := signalContext()
	defer cancel()

	var queue scaler.Queue = &scaler.APIQueue{URL: *apiURL, Token: *token}
	if *dbURL != "" {
		pool, err := db.Connect(ctx, *dbURL)
		if err != nil {
			return err
		}
		defer pool.Close()
		queue = &scaler.FallbackQueue{API: queue, DB: scaler.NewDBQueue(pool, gpu.DefaultMaxFailures)}
	}
	var tunnel scaler.Tunnel
	if !*noTunnel {
		tunnel = &scaler.SSHTunnel{
			RemotePort: *tunnelPort, Forward: *tunnelTarget, KeyFile: *sshKey,
			KnownHosts: filepath.Join(*stateDir, "known_hosts"),
		}
	}
	s, err := scaler.New(scaler.Config{
		Interval: *interval,
		Limits:   scaler.Limits{Idle: *idle, MaxAge: *maxAge, BootTimeout: *bootTimeout, MinCredit: *minCredit, RentCooldown: *cooldown},
		Image:    *image, Login: *login, OfferQuery: *offerQuery, DiskGB: *disk, Label: *label, RunnerEnv: *runnerEnv,
		TunnelPort: *tunnelPort, Token: *token, StateDir: *stateDir,
	}, &scaler.CLI{Bin: *vastBin, APIKey: *vastKey}, queue, tunnel)
	if err != nil {
		return err
	}
	if *once {
		s.Tick(ctx)
		return nil
	}
	return s.Run(ctx)
}

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func envFloat(key string, def float64) float64 {
	if v := os.Getenv(key); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
	}
	return def
}
