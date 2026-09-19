// Package api wires the HTTP server: routes, middleware (recovery, logging,
// CORS, sessions), the JSON error envelope, health endpoints and the
// embedded SPA.
package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BeFeast/RehearseKit/internal/api/respond"
	"github.com/BeFeast/RehearseKit/internal/auth"
	"github.com/BeFeast/RehearseKit/internal/auth/googleid"
	"github.com/BeFeast/RehearseKit/internal/config"
	"github.com/BeFeast/RehearseKit/internal/gpu"
	"github.com/BeFeast/RehearseKit/internal/jobs"
	"github.com/BeFeast/RehearseKit/internal/signed"
	"github.com/BeFeast/RehearseKit/internal/stems"
	"github.com/BeFeast/RehearseKit/internal/storage"
	"github.com/BeFeast/RehearseKit/internal/youtube"
	"github.com/BeFeast/RehearseKit/web"
)

// Server is the assembled rk HTTP application.
type Server struct {
	cfg     config.Config
	pool    *pgxpool.Pool
	broker  *jobs.Broker
	handler http.Handler
}

// New builds the server. The caller runs Broker via Run or RunBroker.
func New(cfg config.Config, pool *pgxpool.Pool) (*Server, error) {
	layout, err := storage.New(cfg.DataDir)
	if err != nil {
		return nil, fmt.Errorf("data dir: %w", err)
	}
	authStore := auth.NewStore(pool)
	jobStore := jobs.NewStore(pool)
	broker := jobs.NewBroker(pool)
	jobHandlers := jobs.NewHandlers(jobStore, layout, broker, jobs.Options{
		MaxUploadBytes: cfg.MaxUploadBytes,
		AnonRetention:  cfg.AnonRetention,
		UserRetention:  cfg.JobRetention,
	})

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		respond.JSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := pool.Ping(ctx); err != nil {
			respond.Failf(w, http.StatusServiceUnavailable, "db_unavailable", "database ping failed")
			return
		}
		respond.JSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	ytdlp := youtube.LookPath()
	ytHandlers := youtube.NewHandlers(youtube.NewService(ytdlp, youtube.Options{}), ytdlp.Available(), youtube.HandlerOptions{})
	mux.HandleFunc("GET /api/v1/config", func(w http.ResponseWriter, _ *http.Request) {
		respond.JSON(w, http.StatusOK, publicConfig(cfg, ytHandlers.Available()))
	})
	var google *googleid.Verifier
	if cfg.GoogleClientID != "" {
		google = googleid.New(googleid.Options{ClientID: cfg.GoogleClientID, JWKSURL: cfg.GoogleJWKSURL})
	}
	auth.NewHandlers(authStore, google).Register(mux)
	jobHandlers.Register(mux)
	stemHandlers := stems.NewHandlers(jobHandlers, layout)
	stemHandlers.Register(mux)
	stemHandlers.RegisterDownload(mux)
	ytHandlers.Register(mux)
	signer := signed.New(cfg.SigningKey)
	signed.NewHandlers(signer, layout).Register(mux)
	gpu.NewHandlers(gpu.NewStore(pool, cfg.LeaseTTL), signer, layout, gpu.Options{
		RunnerToken: cfg.RunnerToken, PublicURL: cfg.PublicURL, SignedURLTTL: cfg.SignedURLTTL,
	}).Register(mux)
	if cfg.RunnerToken == "" {
		slog.Warn("RK_RUNNER_TOKEN is not set; the GPU runner API is disabled")
	}
	mux.HandleFunc("/api/", func(w http.ResponseWriter, _ *http.Request) {
		respond.Fail(w, respond.ErrNotFound)
	})
	mux.Handle("/", spaHandler(web.Dist()))

	var h http.Handler = mux
	h = authStore.Middleware(h)
	h = corsMiddleware(cfg.CORSOrigins, h)
	h = loggingMiddleware(h)
	h = recoverMiddleware(h)
	return &Server{cfg: cfg, pool: pool, broker: broker, handler: h}, nil
}

// Handler returns the root http.Handler (used by tests).
func (s *Server) Handler() http.Handler { return s.handler }

// RunBroker starts the LISTEN fan-out in the background.
func (s *Server) RunBroker(ctx context.Context) { go s.broker.Run(ctx) }

// Broker exposes the job event broker (tests wait on Ready).
func (s *Server) Broker() *jobs.Broker { return s.broker }

// Run serves until ctx is cancelled, then shuts down gracefully.
func (s *Server) Run(ctx context.Context) error {
	s.RunBroker(ctx)
	srv := &http.Server{
		Addr:              s.cfg.ListenAddr,
		Handler:           s.handler,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		BaseContext:       func(_ net.Listener) context.Context { return ctx },
	}
	errc := make(chan error, 1)
	go func() {
		slog.Info("rk serve", "addr", s.cfg.ListenAddr, "data_dir", s.cfg.DataDir)
		errc <- srv.ListenAndServe()
	}()
	select {
	case err := <-errc:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return srv.Shutdown(sctx)
	}
}

type qualityInfo struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Model string `json:"model"`
	Stems int    `json:"stems"`
}

func publicConfig(cfg config.Config, youtubePreview bool) map[string]any {
	return map[string]any{
		"google_client_id":     cfg.GoogleClientID,
		"google_sign_in":       cfg.GoogleClientID != "",
		"youtube_preview":      youtubePreview,
		"gpu_runner_api":       cfg.RunnerToken != "",
		"max_duration_seconds": int(cfg.MaxDuration.Seconds()),
		"max_upload_bytes":     cfg.MaxUploadBytes,
		"anon_retention_hours": int(cfg.AnonRetention.Hours()),
		"job_retention_days":   int(cfg.JobRetention.Hours() / 24),
		"qualities": []qualityInfo{
			{ID: jobs.QualityFast, Label: "Fast", Model: "htdemucs", Stems: 4},
			{ID: jobs.QualityHigh, Label: "High quality", Model: "htdemucs_ft", Stems: 4},
			{ID: jobs.QualityHigh6, Label: "High quality + guitar/piano", Model: "htdemucs_6s", Stems: 6},
		},
	}
}
