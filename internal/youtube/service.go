package youtube

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"
)

// Preview is the metadata returned to the client.
type Preview struct {
	VideoID         string `json:"video_id"`
	Title           string `json:"title"`
	Channel         string `json:"channel"`
	DurationSeconds int    `json:"duration_seconds"`
	ThumbnailURL    string `json:"thumbnail_url"`
	WebpageURL      string `json:"webpage_url"`
}

// Errors the handler maps to HTTP codes.
var (
	// ErrUnavailable wraps a yt-dlp failure (private, removed, geo-blocked,
	// or an upstream change yt-dlp cannot cope with).
	ErrUnavailable = errors.New("video unavailable")
	// ErrTimeout means yt-dlp did not answer within the configured timeout.
	ErrTimeout = errors.New("yt-dlp timed out")
)

// Options tunes the service; zero values take the defaults.
type Options struct {
	Timeout       time.Duration // per yt-dlp run (default 20s)
	CacheSize     int           // LRU entries (default 100)
	CacheTTL      time.Duration // default 10m
	MaxConcurrent int           // simultaneous yt-dlp processes (default 4)
	Now           func() time.Time
}

// Service resolves previews through a Runner with caching and a bound on
// concurrent yt-dlp processes.
type Service struct {
	runner  Runner
	timeout time.Duration
	cache   *cache
	sem     chan struct{}
}

// NewService builds a Service on top of runner.
func NewService(runner Runner, o Options) *Service {
	if o.Timeout <= 0 {
		o.Timeout = 20 * time.Second
	}
	if o.CacheSize <= 0 {
		o.CacheSize = 100
	}
	if o.CacheTTL <= 0 {
		o.CacheTTL = 10 * time.Minute
	}
	if o.MaxConcurrent <= 0 {
		o.MaxConcurrent = 4
	}
	return &Service{
		runner:  runner,
		timeout: o.Timeout,
		cache:   newCache(o.CacheSize, o.CacheTTL, o.Now),
		sem:     make(chan struct{}, o.MaxConcurrent),
	}
}

// Preview returns the metadata for a video id, from cache when fresh.
func (s *Service) Preview(ctx context.Context, videoID string) (*Preview, error) {
	if p, ok := s.cache.get(videoID); ok {
		return p, nil
	}
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	select {
	case s.sem <- struct{}{}:
		defer func() { <-s.sem }()
	case <-ctx.Done():
		return nil, mapCtxErr(ctx.Err())
	}
	out, err := s.runner.Run(ctx, CanonicalURL(videoID))
	if err != nil {
		var re *RunError
		switch {
		case errors.As(err, &re):
			return nil, fmt.Errorf("%w: %s", ErrUnavailable, re.Message)
		case errors.Is(err, ErrNotInstalled):
			return nil, ErrNotInstalled
		case ctx.Err() != nil:
			return nil, mapCtxErr(ctx.Err())
		default:
			return nil, fmt.Errorf("yt-dlp: %w", err)
		}
	}
	p, err := parseDump(out)
	if err != nil {
		return nil, fmt.Errorf("yt-dlp output: %w", err)
	}
	if p.VideoID == "" {
		p.VideoID = videoID
	}
	if p.WebpageURL == "" {
		p.WebpageURL = CanonicalURL(p.VideoID)
	}
	s.cache.put(videoID, p)
	return p, nil
}

func mapCtxErr(err error) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return ErrTimeout
	}
	return err
}

// dump is the subset of yt-dlp's --dump-single-json we read. Duration is a
// float in some extractors, so it is decoded as a number, not an int.
type dump struct {
	ID         string   `json:"id"`
	Title      string   `json:"title"`
	Channel    string   `json:"channel"`
	Uploader   string   `json:"uploader"`
	Duration   *float64 `json:"duration"`
	Thumbnail  string   `json:"thumbnail"`
	WebpageURL string   `json:"webpage_url"`
}

func parseDump(b []byte) (*Preview, error) {
	var d dump
	if err := json.Unmarshal(b, &d); err != nil {
		return nil, err
	}
	p := &Preview{
		VideoID:      d.ID,
		Title:        d.Title,
		Channel:      d.Channel,
		ThumbnailURL: d.Thumbnail,
		WebpageURL:   d.WebpageURL,
	}
	if p.Channel == "" {
		p.Channel = d.Uploader
	}
	if d.Duration != nil && !math.IsNaN(*d.Duration) && *d.Duration > 0 {
		p.DurationSeconds = int(math.Round(*d.Duration))
	}
	return p, nil
}
