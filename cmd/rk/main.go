// Command rk is the RehearseKit server binary.
//
//	rk serve          run the HTTP API (+ embedded SPA, GPU lease API)
//	rk migrate        apply embedded schema migrations
//	rk create-admin   create or reset a password admin account
//	rk import-legacy  copy users/jobs/stems from the FastAPI deployment
//	rk peaks          write peaks/<stem>.pk for a job's stems (dev helper)
//	rk worker         run the CPU pipeline worker and sweepers
//	rk gpu-agent      run the GPU runner loop (on the GPU box)
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/BeFeast/RehearseKit/internal/agent"
	"github.com/BeFeast/RehearseKit/internal/api"
	"github.com/BeFeast/RehearseKit/internal/auth"
	"github.com/BeFeast/RehearseKit/internal/config"
	"github.com/BeFeast/RehearseKit/internal/db"
	"github.com/BeFeast/RehearseKit/internal/legacy"
	"github.com/BeFeast/RehearseKit/internal/pipeline/peaks"
	"github.com/BeFeast/RehearseKit/internal/storage"
	"github.com/BeFeast/RehearseKit/internal/worker"
)

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: logLevel()})))
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "serve":
		err = runServe(os.Args[2:])
	case "migrate":
		err = runMigrate(os.Args[2:])
	case "create-admin":
		err = runCreateAdmin(os.Args[2:])
	case "import-legacy":
		err = runImportLegacy(os.Args[2:])
	case "peaks":
		err = runPeaks(os.Args[2:])
	case "worker":
		err = runWorker(os.Args[2:])
	case "gpu-agent":
		err = runGPUAgent(os.Args[2:])
	case "help", "-h", "--help":
		usage()
		return
	default:
		fmt.Fprintf(os.Stderr, "rk: unknown command %q\n", os.Args[1])
		usage()
		os.Exit(2)
	}
	if err != nil {
		slog.Error("rk "+os.Args[1], "err", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `usage: rk <command> [flags]

commands:
  serve          run the HTTP server (RK_LISTEN_ADDR, RK_DATA_DIR, RK_DATABASE_URL, RK_RUNNER_TOKEN, ...)
  migrate        apply schema migrations (RK_DATABASE_URL)
  create-admin   --email <e> --password <p>  create or reset an admin (RK_DATABASE_URL)
  import-legacy  --legacy-db <dsn> --legacy-dir <path> [--dry-run] [--copy|--hardlink]
                 [--retention-days N]  import the FastAPI deployment (RK_DATABASE_URL, RK_DATA_DIR)
  peaks          <job-id>  build jobs/<id>/peaks/*.pk from jobs/<id>/stems/*.wav (RK_DATA_DIR)
  worker         run the CPU pipeline worker (RK_DATABASE_URL, RK_DATA_DIR, RK_PYTHON, RK_LOCAL_DEMUCS)
  gpu-agent      run the GPU runner (RK_API_URL, RK_RUNNER_TOKEN, RK_RUNNER_ID; --once, --poll, --device)`)
}

func logLevel() slog.Level {
	var l slog.Level
	if err := l.UnmarshalText([]byte(os.Getenv("RK_LOG_LEVEL"))); err != nil {
		return slog.LevelInfo
	}
	return l
}

func signalContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
}

func runServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := config.FromEnv()
	if err != nil {
		return err
	}
	ctx, cancel := signalContext()
	defer cancel()
	pool, err := db.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	srv, err := api.New(cfg, pool)
	if err != nil {
		return err
	}
	return srv.Run(ctx)
}

func runMigrate(args []string) error {
	fs := flag.NewFlagSet("migrate", flag.ExitOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := config.FromEnv()
	if err != nil {
		return err
	}
	ctx, cancel := signalContext()
	defer cancel()
	pool, err := db.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	applied, err := db.Migrate(ctx, pool)
	if err != nil {
		return err
	}
	if len(applied) == 0 {
		slog.Info("migrate: schema is up to date")
	}
	for _, v := range applied {
		slog.Info("migrate: applied", "version", v)
	}
	return nil
}

func runCreateAdmin(args []string) error {
	fs := flag.NewFlagSet("create-admin", flag.ExitOnError)
	email := fs.String("email", os.Getenv("RK_ADMIN_EMAIL"), "admin email (or RK_ADMIN_EMAIL)")
	password := fs.String("password", os.Getenv("RK_ADMIN_PASSWORD"), "admin password (or RK_ADMIN_PASSWORD)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *email == "" || *password == "" {
		return errors.New("--email and --password are required")
	}
	if len(*password) < 8 {
		return errors.New("password must be at least 8 characters")
	}
	cfg, err := config.FromEnv()
	if err != nil {
		return err
	}
	ctx, cancel := signalContext()
	defer cancel()
	pool, err := db.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	u, err := auth.NewStore(pool).UpsertAdmin(ctx, *email, *password)
	if err != nil {
		return err
	}
	slog.Info("admin ready", "id", u.ID, "email", u.Email)
	return nil
}

func runWorker(args []string) error {
	fs := flag.NewFlagSet("worker", flag.ExitOnError)
	poll := fs.Duration("poll", 2*time.Second, "queue polling / cancellation check interval")
	adopt := fs.Bool("adopt", true, "resume jobs left in separating/finalizing/packaging by a previous worker")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := config.FromEnv()
	if err != nil {
		return err
	}
	layout, err := storage.New(cfg.DataDir)
	if err != nil {
		return err
	}
	ctx, cancel := signalContext()
	defer cancel()
	pool, err := db.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	w := worker.New(cfg, pool, layout)
	w.Poll = *poll
	w.Adopt = *adopt
	return w.Run(ctx)
}

func runGPUAgent(args []string) error {
	fs := flag.NewFlagSet("gpu-agent", flag.ExitOnError)
	apiURL := fs.String("api", os.Getenv("RK_API_URL"), "base URL of rk serve (or RK_API_URL)")
	token := fs.String("token", os.Getenv("RK_RUNNER_TOKEN"), "runner token (or RK_RUNNER_TOKEN)")
	runnerID := fs.String("id", os.Getenv("RK_RUNNER_ID"), "runner id shown in leases (or RK_RUNNER_ID; default hostname)")
	python := fs.String("python", envOr("RK_PYTHON", "python3"), "python with demucs installed (or RK_PYTHON)")
	device := fs.String("device", envOr("RK_DEMUCS_DEVICE", "cuda"), "demucs device: cuda or cpu (or RK_DEMUCS_DEVICE)")
	workDir := fs.String("work-dir", os.Getenv("RK_WORK_DIR"), "scratch directory (or RK_WORK_DIR; default $TMPDIR/rk-gpu)")
	poll := fs.Duration("poll", envDuration("RK_POLL_INTERVAL", 5*time.Second), "idle polling interval (or RK_POLL_INTERVAL)")
	once := fs.Bool("once", os.Getenv("RK_ONCE") == "1", "process one job and exit (or RK_ONCE=1)")
	extra := fs.String("demucs-args", os.Getenv("RK_DEMUCS_ARGS"), "extra demucs arguments, space separated (or RK_DEMUCS_ARGS), e.g. \"--segment 7\"")
	if err := fs.Parse(args); err != nil {
		return err
	}
	a, err := agent.New(agent.Config{
		APIURL: *apiURL, Token: *token, RunnerID: *runnerID, Python: *python, Device: *device,
		WorkDir: *workDir, Poll: *poll, Once: *once, DemucsExtra: strings.Fields(*extra),
	})
	if err != nil {
		return err
	}
	ctx, cancel := signalContext()
	defer cancel()
	return a.Run(ctx)
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envDuration(key string, def time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return def
}

func runImportLegacy(args []string) error {
	fs := flag.NewFlagSet("import-legacy", flag.ExitOnError)
	legacyDB := fs.String("legacy-db", "", "legacy Postgres DSN (read-only)")
	legacyDir := fs.String("legacy-dir", "", "legacy storage root containing stems/ and uploads/")
	dryRun := fs.Bool("dry-run", false, "report what would be imported without writing anything")
	doCopy := fs.Bool("copy", false, "copy stems and sources (default)")
	doLink := fs.Bool("hardlink", false, "hardlink stems and sources; falls back to copy per file when linking fails")
	retentionDays := fs.Int("retention-days", 0, "expires_at = created_at + N days (default RK_JOB_RETENTION_DAYS)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *legacyDB == "" || *legacyDir == "" {
		return errors.New("--legacy-db and --legacy-dir are required")
	}
	if *doCopy && *doLink {
		return errors.New("--copy and --hardlink are mutually exclusive")
	}
	if *retentionDays < 0 {
		return errors.New("--retention-days must be positive")
	}
	cfg, err := config.FromEnv()
	if err != nil {
		return err
	}
	retention := cfg.JobRetention
	if *retentionDays > 0 {
		retention = time.Duration(*retentionDays) * 24 * time.Hour
	}
	ctx, cancel := signalContext()
	defer cancel()
	target, err := db.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer target.Close()
	src, err := legacy.Connect(ctx, *legacyDB)
	if err != nil {
		return err
	}
	defer src.Close()
	layout, err := storage.New(cfg.DataDir)
	if err != nil {
		return err
	}
	sum, err := legacy.Run(ctx, target, src, layout, legacy.Options{
		LegacyDir: *legacyDir,
		DryRun:    *dryRun,
		Hardlink:  *doLink,
		Retention: retention,
	})
	if err != nil {
		return err
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(sum); err != nil {
		return err
	}
	if sum.Jobs.Errors > 0 {
		return fmt.Errorf("%d job(s) failed to import", sum.Jobs.Errors)
	}
	return nil
}

func runPeaks(args []string) error {
	fs := flag.NewFlagSet("peaks", flag.ExitOnError)
	dataDir := fs.String("data-dir", envOr("RK_DATA_DIR", "./data"), "storage root (or RK_DATA_DIR)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: rk peaks [--data-dir <dir>] <job-id>")
	}
	id := fs.Arg(0)
	layout, err := storage.New(*dataDir)
	if err != nil {
		return err
	}
	dir, err := layout.JobDir(id)
	if err != nil {
		return err
	}
	entries, err := os.ReadDir(filepath.Join(dir, "stems"))
	if err != nil {
		return err
	}
	var n int
	for _, e := range entries {
		name := strings.TrimSuffix(e.Name(), ".wav")
		if e.IsDir() || name == e.Name() {
			continue
		}
		src, err := layout.StemPath(id, name)
		if err != nil {
			return err
		}
		dst, err := layout.PeaksPath(id, name)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		f, err := os.Open(src)
		if err != nil {
			return err
		}
		pk, err := peaks.Build(f, nil)
		_ = f.Close()
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		out, err := os.Create(dst)
		if err != nil {
			return err
		}
		if err := peaks.Write(out, pk); err != nil {
			_ = out.Close()
			return err
		}
		if err := out.Close(); err != nil {
			return err
		}
		slog.Info("peaks written", "stem", name, "frames", pk.Frames, "sample_rate", pk.SampleRate, "channels", pk.Channels, "path", dst)
		n++
	}
	if n == 0 {
		return errors.New("no stems/*.wav found")
	}
	return nil
}
