package youtube

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	os.Exit(m.Run())
}

func writeScript(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "yt-dlp")
	if err := os.WriteFile(p, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

// fakeRunner is the injected yt-dlp: fn decides per call, calls counts
// invocations so cache behaviour is observable.
type fakeRunner struct {
	fn    func(ctx context.Context, url string) ([]byte, error)
	calls atomic.Int32
	last  atomic.Value // string
}

func (f *fakeRunner) Run(ctx context.Context, url string) ([]byte, error) {
	f.calls.Add(1)
	f.last.Store(url)
	return f.fn(ctx, url)
}

const goodDump = `{"id":"dQw4w9WgXcQ","title":"Never Gonna Give You Up","channel":"Rick Astley","uploader":"RickAstleyVEVO","duration":212.4,"thumbnail":"https://i.ytimg.com/vi/dQw4w9WgXcQ/maxresdefault.jpg","webpage_url":"https://www.youtube.com/watch?v=dQw4w9WgXcQ","formats":[{"x":1}]}`

func okRunner() *fakeRunner {
	return &fakeRunner{fn: func(context.Context, string) ([]byte, error) { return []byte(goodDump), nil }}
}

type testEnv struct {
	h      *Handlers
	runner *fakeRunner
	clk    *fakeClock
}

func newTestHandlers(t *testing.T, r *fakeRunner, available bool, mutate func(*Options, *HandlerOptions)) *testEnv {
	t.Helper()
	clk := newFakeClock()
	so := Options{Now: clk.now}
	ho := HandlerOptions{Now: clk.now}
	if mutate != nil {
		mutate(&so, &ho)
	}
	return &testEnv{h: NewHandlers(NewService(r, so), available, ho), runner: r, clk: clk}
}

type envelope struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *testEnv) post(t *testing.T, body string, hdr ...string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	mux := http.NewServeMux()
	e.h.Register(mux)
	req := httptest.NewRequest("POST", "/api/v1/youtube/preview", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "203.0.113.5:1234"
	for i := 0; i+1 < len(hdr); i += 2 {
		if hdr[i] == "RemoteAddr" {
			req.RemoteAddr = hdr[i+1]
			continue
		}
		req.Header.Set(hdr[i], hdr[i+1])
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	var out map[string]any
	if rec.Body.Len() > 0 {
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("body %q: %v", rec.Body.String(), err)
		}
	}
	return rec, out
}

func wantError(t *testing.T, rec *httptest.ResponseRecorder, body map[string]any, status int, code string) {
	t.Helper()
	if rec.Code != status || body["code"] != code {
		t.Fatalf("got %d %v, want %d %s", rec.Code, body, status, code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("content-type %q", ct)
	}
	if msg, _ := body["message"].(string); msg == "" {
		t.Fatal("error envelope without message")
	}
}

func TestPreviewSuccess(t *testing.T) {
	e := newTestHandlers(t, okRunner(), true, nil)
	rec, body := e.post(t, `{"url":"https://youtu.be/dQw4w9WgXcQ?t=5&list=PLxyz"}`)
	if rec.Code != 200 {
		t.Fatalf("%d %v", rec.Code, body)
	}
	want := map[string]any{
		"video_id":         "dQw4w9WgXcQ",
		"title":            "Never Gonna Give You Up",
		"channel":          "Rick Astley",
		"duration_seconds": float64(212),
		"thumbnail_url":    "https://i.ytimg.com/vi/dQw4w9WgXcQ/maxresdefault.jpg",
		"webpage_url":      "https://www.youtube.com/watch?v=dQw4w9WgXcQ",
	}
	if len(body) != len(want) {
		t.Fatalf("body has extra/missing keys: %v", body)
	}
	for k, v := range want {
		if body[k] != v {
			t.Fatalf("%s = %v, want %v", k, body[k], v)
		}
	}
	// yt-dlp is invoked with the canonical URL, not the user's string.
	if got := e.runner.last.Load(); got != "https://www.youtube.com/watch?v=dQw4w9WgXcQ" {
		t.Fatalf("yt-dlp url=%v", got)
	}
}

func TestPreviewFallbacks(t *testing.T) {
	r := &fakeRunner{fn: func(context.Context, string) ([]byte, error) {
		return []byte(`{"title":"Untitled","uploader":"someone","duration":null}`), nil
	}}
	e := newTestHandlers(t, r, true, nil)
	rec, body := e.post(t, `{"url":"https://www.youtube.com/shorts/dQw4w9WgXcQ"}`)
	if rec.Code != 200 {
		t.Fatalf("%d %v", rec.Code, body)
	}
	if body["video_id"] != "dQw4w9WgXcQ" || body["channel"] != "someone" || body["duration_seconds"] != float64(0) ||
		body["webpage_url"] != "https://www.youtube.com/watch?v=dQw4w9WgXcQ" || body["thumbnail_url"] != "" {
		t.Fatalf("%v", body)
	}
}

func TestPreviewInvalidRequests(t *testing.T) {
	e := newTestHandlers(t, okRunner(), true, nil)
	cases := []struct {
		body   string
		status int
		code   string
	}{
		{``, 400, "invalid_json"},
		{`{`, 400, "invalid_json"},
		{`{"url":"https://youtu.be/dQw4w9WgXcQ","extra":1}`, 400, "invalid_json"},
		{`{"url":""}`, 400, "invalid_url"},
		{`{"url":"https://vimeo.com/1"}`, 400, "invalid_url"},
		{`{"url":"https://www.youtube.com/@channel"}`, 400, "invalid_url"},
		{`{"url":"https://youtu.be/short"}`, 400, "invalid_url"},
	}
	for _, tc := range cases {
		rec, body := e.post(t, tc.body)
		wantError(t, rec, body, tc.status, tc.code)
	}
	if n := e.runner.calls.Load(); n != 0 {
		t.Fatalf("yt-dlp ran %d times for invalid input", n)
	}
}

func TestPreviewYtdlpFailure(t *testing.T) {
	r := &fakeRunner{fn: func(context.Context, string) ([]byte, error) {
		return nil, &RunError{ExitCode: 1, Message: "Private video. Sign in if you've been granted access to this video"}
	}}
	e := newTestHandlers(t, r, true, nil)
	rec, body := e.post(t, `{"url":"https://www.youtube.com/watch?v=dQw4w9WgXcQ"}`)
	wantError(t, rec, body, 422, "youtube_unavailable")
	if msg := body["message"].(string); !strings.Contains(msg, "Private video") {
		t.Fatalf("message %q should carry yt-dlp's reason", msg)
	}
	// Failures are not cached.
	e.post(t, `{"url":"https://www.youtube.com/watch?v=dQw4w9WgXcQ"}`)
	if n := e.runner.calls.Load(); n != 2 {
		t.Fatalf("calls=%d, failure was cached", n)
	}
}

func TestPreviewGarbageOutput(t *testing.T) {
	r := &fakeRunner{fn: func(context.Context, string) ([]byte, error) { return []byte("not json"), nil }}
	e := newTestHandlers(t, r, true, nil)
	rec, body := e.post(t, `{"url":"https://youtu.be/dQw4w9WgXcQ"}`)
	wantError(t, rec, body, 502, "youtube_error")
}

func TestPreviewTimeout(t *testing.T) {
	r := &fakeRunner{fn: func(ctx context.Context, _ string) ([]byte, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	e := newTestHandlers(t, r, true, func(o *Options, _ *HandlerOptions) { o.Timeout = 50 * time.Millisecond })
	start := time.Now()
	rec, body := e.post(t, `{"url":"https://youtu.be/dQw4w9WgXcQ"}`)
	wantError(t, rec, body, 504, "youtube_timeout")
	if time.Since(start) > 2*time.Second {
		t.Fatal("timeout not enforced")
	}
}

func TestPreviewNotInstalled(t *testing.T) {
	// Not found at startup: 501 before any parsing beyond the JSON body.
	e := newTestHandlers(t, okRunner(), false, nil)
	rec, body := e.post(t, `{"url":"https://youtu.be/dQw4w9WgXcQ"}`)
	wantError(t, rec, body, 501, "youtube_unsupported")
	if e.h.Available() {
		t.Fatal("Available() should be false")
	}
	if e.runner.calls.Load() != 0 {
		t.Fatal("runner invoked while unavailable")
	}

	// Binary vanished after startup: the runner's error maps to 501 too.
	r := &fakeRunner{fn: func(context.Context, string) ([]byte, error) { return nil, ErrNotInstalled }}
	e = newTestHandlers(t, r, true, nil)
	rec, body = e.post(t, `{"url":"https://youtu.be/dQw4w9WgXcQ"}`)
	wantError(t, rec, body, 501, "youtube_unsupported")
}

func TestPreviewCache(t *testing.T) {
	e := newTestHandlers(t, okRunner(), true, nil)
	for _, u := range []string{
		`https://youtu.be/dQw4w9WgXcQ`,
		`https://www.youtube.com/watch?v=dQw4w9WgXcQ&t=1`,
		`https://music.youtube.com/watch?v=dQw4w9WgXcQ`,
	} {
		rec, _ := e.post(t, `{"url":"`+u+`"}`)
		if rec.Code != 200 {
			t.Fatalf("%s: %d", u, rec.Code)
		}
	}
	if n := e.runner.calls.Load(); n != 1 {
		t.Fatalf("calls=%d, want 1 (same video id should hit the cache)", n)
	}
	e.clk.advance(10*time.Minute + time.Second)
	e.post(t, `{"url":"https://youtu.be/dQw4w9WgXcQ"}`)
	if n := e.runner.calls.Load(); n != 2 {
		t.Fatalf("calls=%d, want 2 after TTL", n)
	}
}

func TestPreviewRateLimit(t *testing.T) {
	e := newTestHandlers(t, okRunner(), true, nil)
	body := `{"url":"https://youtu.be/dQw4w9WgXcQ"}`
	for i := 0; i < 10; i++ {
		rec, _ := e.post(t, body)
		if rec.Code != 200 {
			t.Fatalf("request %d: %d", i+1, rec.Code)
		}
	}
	rec, out := e.post(t, body)
	wantError(t, rec, out, 429, "rate_limited")
	if ra := rec.Header().Get("Retry-After"); ra == "" || ra == "0" {
		t.Fatalf("Retry-After=%q", ra)
	}
	// A public peer cannot mint a fresh bucket by spoofing proxy headers.
	rec, out = e.post(t, body, "X-Forwarded-For", "198.51.100.7")
	wantError(t, rec, out, 429, "rate_limited")
	rec, out = e.post(t, body, "X-Real-IP", "198.51.100.8")
	wantError(t, rec, out, 429, "rate_limited")
	// Behind the proxy (private peer) the forwarded client is its own bucket.
	rec, _ = e.post(t, body, "RemoteAddr", "10.0.0.2:5555", "X-Forwarded-For", "198.51.100.7")
	if rec.Code != 200 {
		t.Fatalf("other client behind proxy got %d", rec.Code)
	}
	// A different direct peer is unaffected; a bad URL never costs a token.
	rec, _ = e.post(t, body, "RemoteAddr", "203.0.113.6:1")
	if rec.Code != 200 {
		t.Fatalf("other client got %d", rec.Code)
	}
	e.clk.advance(6 * time.Second)
	rec, _ = e.post(t, body)
	if rec.Code != 200 {
		t.Fatalf("after refill: %d", rec.Code)
	}
	rec, out = e.post(t, `{"url":"nope"}`)
	wantError(t, rec, out, 400, "invalid_url")
}

func TestServiceConcurrencyBound(t *testing.T) {
	var inflight, peak atomic.Int32
	release := make(chan struct{})
	r := &fakeRunner{fn: func(ctx context.Context, _ string) ([]byte, error) {
		n := inflight.Add(1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		defer inflight.Add(-1)
		select {
		case <-release:
			return []byte(goodDump), nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}}
	svc := NewService(r, Options{MaxConcurrent: 2, Timeout: time.Second})
	errc := make(chan error, 6)
	ids := []string{"aaaaaaaaaaa", "bbbbbbbbbbb", "ccccccccccc", "ddddddddddd", "eeeeeeeeeee", "fffffffffff"}
	for _, id := range ids {
		go func() {
			_, err := svc.Preview(context.Background(), id)
			errc <- err
		}()
	}
	time.Sleep(100 * time.Millisecond)
	close(release)
	for range ids {
		if err := <-errc; err != nil {
			t.Fatal(err)
		}
	}
	if p := peak.Load(); p > 2 {
		t.Fatalf("peak concurrency %d > 2", p)
	}
}

func TestServiceTimeoutWaitingForSlot(t *testing.T) {
	block := make(chan struct{})
	defer close(block)
	r := &fakeRunner{fn: func(ctx context.Context, _ string) ([]byte, error) {
		select {
		case <-block:
		case <-ctx.Done():
		}
		return nil, ctx.Err()
	}}
	svc := NewService(r, Options{MaxConcurrent: 1, Timeout: 100 * time.Millisecond})
	go svc.Preview(context.Background(), "aaaaaaaaaaa") //nolint:errcheck
	time.Sleep(20 * time.Millisecond)
	_, err := svc.Preview(context.Background(), "bbbbbbbbbbb")
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("err=%v, want ErrTimeout", err)
	}
}
