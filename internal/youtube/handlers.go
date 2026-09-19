package youtube

import (
	"errors"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/BeFeast/RehearseKit/internal/api/respond"
)

// Handlers serves POST /api/v1/youtube/preview. No session is required:
// anonymous uploads are allowed, so anonymous previews are too; the
// per-IP token bucket is what bounds abuse.
type Handlers struct {
	svc       *Service
	limiter   *limiter
	available bool
}

// HandlerOptions tunes the endpoint; zero values take the defaults.
type HandlerOptions struct {
	RateBurst int           // requests per RatePer per client IP (default 10)
	RatePer   time.Duration // default 1m
	Now       func() time.Time
}

// NewHandlers builds the handlers. available reports whether yt-dlp was
// found; when false the endpoint answers 501 and /config says so.
func NewHandlers(svc *Service, available bool, o HandlerOptions) *Handlers {
	if o.RateBurst <= 0 {
		o.RateBurst = 10
	}
	if o.RatePer <= 0 {
		o.RatePer = time.Minute
	}
	return &Handlers{svc: svc, limiter: newLimiter(o.RateBurst, o.RatePer, o.Now), available: available}
}

// Available reports whether yt-dlp is installed (for /api/v1/config).
func (h *Handlers) Available() bool { return h.available }

// Register mounts the route.
func (h *Handlers) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/youtube/preview", h.preview)
}

type previewRequest struct {
	URL string `json:"url"`
}

func (h *Handlers) preview(w http.ResponseWriter, r *http.Request) {
	var req previewRequest
	if err := respond.DecodeJSON(r, &req); err != nil {
		respond.Fail(w, err)
		return
	}
	if !h.available {
		respond.Failf(w, http.StatusNotImplemented, "youtube_unsupported", "yt-dlp is not installed on this server")
		return
	}
	id, err := ParseVideoID(req.URL)
	if err != nil {
		respond.Failf(w, http.StatusBadRequest, "invalid_url", "url must point to a YouTube video (youtube.com/watch?v=, /shorts/, youtu.be/)")
		return
	}
	if ok, wait := h.limiter.allow(clientIP(r)); !ok {
		secs := int(math.Ceil(wait.Seconds()))
		if secs < 1 {
			secs = 1
		}
		w.Header().Set("Retry-After", strconv.Itoa(secs))
		respond.Failf(w, http.StatusTooManyRequests, "rate_limited", "too many preview requests; retry in "+strconv.Itoa(secs)+"s")
		return
	}
	p, err := h.svc.Preview(r.Context(), id)
	if err != nil {
		switch {
		case errors.Is(err, ErrUnavailable):
			respond.Failf(w, http.StatusUnprocessableEntity, "youtube_unavailable", err.Error())
		case errors.Is(err, ErrNotInstalled):
			respond.Failf(w, http.StatusNotImplemented, "youtube_unsupported", "yt-dlp is not installed on this server")
		case errors.Is(err, ErrTimeout):
			respond.Failf(w, http.StatusGatewayTimeout, "youtube_timeout", "yt-dlp did not answer in time; try again")
		case r.Context().Err() != nil:
			// Client went away; nothing useful to write.
			return
		default:
			slog.Error("youtube preview", "video_id", id, "err", err)
			respond.Failf(w, http.StatusBadGateway, "youtube_error", "could not read video metadata")
		}
		return
	}
	respond.JSON(w, http.StatusOK, p)
}
