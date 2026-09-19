package signed

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/BeFeast/RehearseKit/internal/storage"
)

const jobID = "11111111-2222-4333-8444-555555555555"

func TestSignVerify(t *testing.T) {
	s := New([]byte("k"))
	exp := time.Now().Add(time.Hour)
	u, err := s.Sign(http.MethodGet, SourcePath(jobID), exp)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(u)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Path != SourcePath(jobID) {
		t.Fatalf("path %q", parsed.Path)
	}
	if err := s.Verify(http.MethodGet, parsed.Path, parsed.Query()); err != nil {
		t.Fatalf("verify: %v", err)
	}
	// Method is bound.
	if err := s.Verify(http.MethodPut, parsed.Path, parsed.Query()); !errors.Is(err, ErrBadSig) {
		t.Fatalf("PUT with GET signature: %v", err)
	}
	// Path is bound.
	if err := s.Verify(http.MethodGet, StemPath(jobID, "vocals"), parsed.Query()); !errors.Is(err, ErrBadSig) {
		t.Fatalf("other path: %v", err)
	}
	// Tampered expiry.
	q := parsed.Query()
	q.Set("exp", "9999999999")
	if err := s.Verify(http.MethodGet, parsed.Path, q); !errors.Is(err, ErrBadSig) {
		t.Fatalf("tampered exp: %v", err)
	}
	// Different key.
	if err := New([]byte("other")).Verify(http.MethodGet, parsed.Path, parsed.Query()); !errors.Is(err, ErrBadSig) {
		t.Fatalf("other key: %v", err)
	}
	// Missing params.
	if err := s.Verify(http.MethodGet, parsed.Path, url.Values{}); !errors.Is(err, ErrMissing) {
		t.Fatalf("missing: %v", err)
	}
}

func TestVerifyExpired(t *testing.T) {
	s := New([]byte("k"))
	u, _ := s.Sign(http.MethodGet, "/p", time.Now().Add(-time.Second))
	parsed, _ := url.Parse(u)
	if err := s.Verify(http.MethodGet, parsed.Path, parsed.Query()); !errors.Is(err, ErrExpired) {
		t.Fatalf("expired: %v", err)
	}
	// Clock injection: a URL valid "now" is rejected once the clock passes exp.
	s.now = func() time.Time { return time.Now().Add(2 * time.Hour) }
	u, _ = New([]byte("k")).Sign(http.MethodGet, "/p", time.Now().Add(time.Hour))
	parsed, _ = url.Parse(u)
	if err := s.Verify(http.MethodGet, parsed.Path, parsed.Query()); !errors.Is(err, ErrExpired) {
		t.Fatalf("clock-expired: %v", err)
	}
}

func TestNoKey(t *testing.T) {
	s := New(nil)
	if _, err := s.Sign(http.MethodGet, "/p", time.Now()); !errors.Is(err, ErrNoKey) {
		t.Fatal(err)
	}
	if err := s.Verify(http.MethodGet, "/p", url.Values{"exp": {"1"}, "sig": {"x"}}); !errors.Is(err, ErrNoKey) {
		t.Fatal(err)
	}
}

func newServer(t *testing.T) (*Signer, storage.Layout, *httptest.Server) {
	t.Helper()
	layout, err := storage.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s := New([]byte("test-key"))
	mux := http.NewServeMux()
	NewHandlers(s, layout).Register(mux)
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return s, layout, ts
}

func TestSourceDownload(t *testing.T) {
	s, layout, ts := newServer(t)
	dir, _ := layout.JobDir(jobID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "source.wav"), []byte("RIFFdata"), 0o644); err != nil {
		t.Fatal(err)
	}
	u, _ := s.Sign(http.MethodGet, SourcePath(jobID), time.Now().Add(time.Minute))
	resp, err := http.Get(ts.URL + u)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "audio/wav" {
		t.Fatalf("status %d type %q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	// Range works (ServeContent).
	req, _ := http.NewRequest(http.MethodGet, ts.URL+u, nil)
	req.Header.Set("Range", "bytes=4-7")
	resp2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusPartialContent {
		t.Fatalf("range status %d", resp2.StatusCode)
	}
	// Unsigned is refused.
	resp3, _ := http.Get(ts.URL + SourcePath(jobID))
	resp3.Body.Close()
	if resp3.StatusCode != http.StatusForbidden {
		t.Fatalf("unsigned status %d", resp3.StatusCode)
	}
	// Missing file.
	other := "22222222-2222-4333-8444-555555555555"
	u2, _ := s.Sign(http.MethodGet, SourcePath(other), time.Now().Add(time.Minute))
	resp4, _ := http.Get(ts.URL + u2)
	resp4.Body.Close()
	if resp4.StatusCode != http.StatusNotFound {
		t.Fatalf("missing status %d", resp4.StatusCode)
	}
}

func TestStemUpload(t *testing.T) {
	s, layout, ts := newServer(t)
	body := bytes.Repeat([]byte("stem"), 1000)
	u, _ := s.Sign(http.MethodPut, StemPath(jobID, "vocals"), time.Now().Add(time.Minute))
	req, _ := http.NewRequest(http.MethodPut, ts.URL+u, bytes.NewReader(body))
	req.ContentLength = int64(len(body))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status %d", resp.StatusCode)
	}
	var out struct {
		Name   string `json:"name"`
		Bytes  int64  `json:"bytes"`
		SHA256 string `json:"sha256"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	want := sha256.Sum256(body)
	if out.Bytes != int64(len(body)) || out.SHA256 != hex.EncodeToString(want[:]) || out.Name != "vocals" {
		t.Fatalf("response %+v", out)
	}
	path, _ := layout.StemPath(jobID, "vocals")
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, body) {
		t.Fatalf("stored file mismatch: %v", err)
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".part") {
			t.Fatalf("temp file left behind: %s", e.Name())
		}
	}

	// GET signature is not valid for PUT.
	ug, _ := s.Sign(http.MethodGet, StemPath(jobID, "vocals"), time.Now().Add(time.Minute))
	req, _ = http.NewRequest(http.MethodPut, ts.URL+ug, bytes.NewReader(body))
	resp2, _ := http.DefaultClient.Do(req)
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-method status %d", resp2.StatusCode)
	}

	// Content-Length above the cap is refused before reading.
	req, _ = http.NewRequest(http.MethodPut, ts.URL+u, strings.NewReader("x"))
	req.ContentLength = MaxStemBytes + 1
	resp3, err := http.DefaultClient.Do(req)
	if err == nil {
		resp3.Body.Close()
		if resp3.StatusCode != http.StatusRequestEntityTooLarge {
			t.Fatalf("too-large status %d", resp3.StatusCode)
		}
	}

	// Invalid stem name.
	ub, _ := s.Sign(http.MethodPut, StemPath(jobID, "Bad.Name"), time.Now().Add(time.Minute))
	req, _ = http.NewRequest(http.MethodPut, ts.URL+ub, bytes.NewReader(body))
	req.ContentLength = int64(len(body))
	resp4, _ := http.DefaultClient.Do(req)
	resp4.Body.Close()
	if resp4.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad name status %d", resp4.StatusCode)
	}
}
