// Command rk is the RehearseKit server binary.
//
//	rk serve          run the HTTP API (+ embedded SPA)
//	rk migrate        apply embedded schema migrations
//	rk create-admin   create or reset a password admin account
//	rk worker         (not implemented yet; phase 3)
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/BeFeast/RehearseKit/internal/api"
	"github.com/BeFeast/RehearseKit/internal/auth"
	"github.com/BeFeast/RehearseKit/internal/config"
	"github.com/BeFeast/RehearseKit/internal/db"
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
	case "worker":
		fmt.Fprintln(os.Stderr, "rk worker: not implemented (phase 3)")
		os.Exit(2)
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
  serve          run the HTTP server (RK_LISTEN_ADDR, RK_DATA_DIR, RK_DATABASE_URL, ...)
  migrate        apply schema migrations (RK_DATABASE_URL)
  create-admin   --email <e> --password <p>  create or reset an admin (RK_DATABASE_URL)
  worker         not implemented yet`)
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
