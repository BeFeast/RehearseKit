package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

const testJob = "11111111-2222-4333-8444-555555555555"

func newTestServer(t *testing.T) (*Server, []byte) {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "stems", testJob)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := make([]byte, 100_000)
	for i := range body {
		body[i] = byte(i * 7)
	}
	for _, name := range []string{"vocals.wav", "guitar.wav"} {
		if err := os.WriteFile(filepath.Join(dir, name), body, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	return NewServer(Config{Root: root, AllowedOrigins: []string{"http://localhost:3000"}}, nil), body
}

func do(t *testing.T, s *Server, method, target string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	return rec
}

func TestFullFile(t *testing.T) {
	s, body := newTestServer(t)
	rec := do(t, s, http.MethodGet, "/stems/"+testJob+"/vocals", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	if got := rec.Header().Get("Accept-Ranges"); got != "bytes" {
		t.Errorf("Accept-Ranges=%q", got)
	}
	if got := rec.Header().Get("Content-Type"); got != "audio/wav" {
		t.Errorf("Content-Type=%q", got)
	}
	if got := rec.Header().Get("Content-Length"); got != "100000" {
		t.Errorf("Content-Length=%q", got)
	}
	if !strings.HasPrefix(rec.Header().Get("Content-Disposition"), "inline") {
		t.Errorf("Content-Disposition=%q", rec.Header().Get("Content-Disposition"))
	}
	if got := rec.Header().Get("Cross-Origin-Resource-Policy"); got != "cross-origin" {
		t.Errorf("CORP=%q", got)
	}
	if b, _ := io.ReadAll(rec.Body); string(b) != string(body) {
		t.Error("body mismatch")
	}
}

func TestPartialContent(t *testing.T) {
	s, body := newTestServer(t)
	cases := []struct {
		rng        string
		start, end int
	}{
		{"bytes=0-65535", 0, 65535},
		{"bytes=1000-1999", 1000, 1999},
		{"bytes=99000-", 99000, 99999},
		{"bytes=-500", 99500, 99999},
		{"bytes=0-999999999", 0, 99999},
	}
	for _, c := range cases {
		rec := do(t, s, http.MethodGet, "/stems/"+testJob+"/vocals", map[string]string{"Range": c.rng})
		if rec.Code != http.StatusPartialContent {
			t.Errorf("%s: status %d", c.rng, rec.Code)
			continue
		}
		wantCR := "bytes " + itoa(c.start) + "-" + itoa(c.end) + "/100000"
		if got := rec.Header().Get("Content-Range"); got != wantCR {
			t.Errorf("%s: Content-Range=%q want %q", c.rng, got, wantCR)
		}
		if got := rec.Header().Get("Content-Length"); got != itoa(c.end-c.start+1) {
			t.Errorf("%s: Content-Length=%q", c.rng, got)
		}
		b, _ := io.ReadAll(rec.Body)
		if string(b) != string(body[c.start:c.end+1]) {
			t.Errorf("%s: body mismatch (len %d)", c.rng, len(b))
		}
	}
}

func TestUnsatisfiableRange(t *testing.T) {
	s, _ := newTestServer(t)
	for _, rng := range []string{"bytes=100000-", "bytes=200000-300000"} {
		rec := do(t, s, http.MethodGet, "/stems/"+testJob+"/vocals", map[string]string{"Range": rng})
		if rec.Code != http.StatusRequestedRangeNotSatisfiable {
			t.Errorf("%s: status %d", rng, rec.Code)
		}
		if got := rec.Header().Get("Content-Range"); got != "bytes */100000" {
			t.Errorf("%s: Content-Range=%q", rng, got)
		}
	}
	// Malformed ranges: net/http answers 416; a lenient server may answer 200.
	rec := do(t, s, http.MethodGet, "/stems/"+testJob+"/vocals", map[string]string{"Range": "bytes=abc"})
	if rec.Code != http.StatusOK && rec.Code != http.StatusRequestedRangeNotSatisfiable {
		t.Errorf("malformed: status %d", rec.Code)
	}
}

func TestHead(t *testing.T) {
	s, _ := newTestServer(t)
	rec := do(t, s, http.MethodHead, "/stems/"+testJob+"/vocals", nil)
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Length") != "100000" || rec.Body.Len() != 0 {
		t.Errorf("HEAD: status %d len %s body %d", rec.Code, rec.Header().Get("Content-Length"), rec.Body.Len())
	}
}

func TestCORS(t *testing.T) {
	s, _ := newTestServer(t)
	rec := do(t, s, http.MethodGet, "/stems/"+testJob+"/vocals", map[string]string{"Origin": "http://localhost:3000", "Range": "bytes=0-9"})
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "http://localhost:3000" {
		t.Errorf("ACAO=%q", got)
	}
	if got := rec.Header().Get("Access-Control-Expose-Headers"); !strings.Contains(got, "Content-Range") {
		t.Errorf("expose=%q", got)
	}
	rec = do(t, s, http.MethodGet, "/stems/"+testJob+"/vocals", map[string]string{"Origin": "http://evil.example"})
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("unexpected ACAO=%q", got)
	}
	rec = do(t, s, http.MethodOptions, "/stems/"+testJob+"/vocals", map[string]string{"Origin": "http://localhost:3000", "Access-Control-Request-Method": "GET", "Access-Control-Request-Headers": "range"})
	if rec.Code != http.StatusNoContent || !strings.Contains(rec.Header().Get("Access-Control-Allow-Headers"), "Range") {
		t.Errorf("preflight: status %d headers %q", rec.Code, rec.Header().Get("Access-Control-Allow-Headers"))
	}
	wild := NewServer(Config{Root: s.cfg.Root, AllowedOrigins: []string{"*"}}, nil)
	rec = do(t, wild, http.MethodGet, "/stems/"+testJob+"/vocals", map[string]string{"Origin": "http://anything.example"})
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Errorf("wildcard ACAO=%q", got)
	}
}

func TestManifest(t *testing.T) {
	s, _ := newTestServer(t)
	rec := do(t, s, http.MethodGet, "/stems/"+testJob, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var m Manifest
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatal(err)
	}
	if m.JobID != testJob || len(m.Stems) != 2 || m.Stems[0].Name != "guitar" || m.Stems[1].Name != "vocals" || m.Stems[1].Bytes != 100000 {
		t.Errorf("manifest %+v", m)
	}
	if m.Stems[0].URL != "/stems/"+testJob+"/guitar" {
		t.Errorf("url %q", m.Stems[0].URL)
	}
	rec = do(t, s, http.MethodGet, "/stems/22222222-2222-4333-8444-555555555555", nil)
	if rec.Code != http.StatusNotFound {
		t.Errorf("missing job: status %d", rec.Code)
	}
}

func TestValidation(t *testing.T) {
	s, _ := newTestServer(t)
	for _, target := range []string{
		"/stems/not-a-uuid/vocals",
		"/stems/" + testJob + "/../vocals",
		"/stems/" + testJob + "/Vocals",
		"/stems/" + testJob + "/vocals.wav",
	} {
		rec := do(t, s, http.MethodGet, target, nil)
		if rec.Code != http.StatusBadRequest && rec.Code != http.StatusNotFound && rec.Code != http.StatusMovedPermanently && rec.Code != http.StatusTemporaryRedirect {
			t.Errorf("%s: status %d", target, rec.Code)
		}
		if rec.Code == http.StatusOK {
			t.Errorf("%s: served a file", target)
		}
	}
	rec := do(t, s, http.MethodGet, "/stems/"+testJob+"/drums", nil)
	if rec.Code != http.StatusNotFound {
		t.Errorf("missing stem: status %d", rec.Code)
	}
	rec = do(t, s, http.MethodGet, "/healthz", nil)
	if rec.Code != http.StatusOK {
		t.Errorf("healthz: status %d", rec.Code)
	}
}

func itoa(i int) string { return strconv.Itoa(i) }
