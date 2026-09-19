package stems_test

import (
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/BeFeast/RehearseKit/internal/db/dbtest"
	"github.com/BeFeast/RehearseKit/internal/jobs"
	"github.com/BeFeast/RehearseKit/internal/stems"
	"github.com/BeFeast/RehearseKit/internal/storage"
)

func TestMain(m *testing.M) {
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	os.Exit(m.Run())
}

func TestPackageFilename(t *testing.T) {
	for in, want := range map[string]string{
		"Song":               "Song-stems.zip",
		"  ../evil/name?  ":  "evil_name-stems.zip",
		"":                   "rehearsekit-stems.zip",
		"Ünïcode / slashes":  "n_code _ slashes-stems.zip",
		"dots.and-dashes_ok": "dots.and-dashes_ok-stems.zip",
	} {
		if got := stems.PackageFilename(in); got != want {
			t.Errorf("PackageFilename(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDownload(t *testing.T) {
	pool := dbtest.Pool(t)
	layout, err := storage.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	store := jobs.NewStore(pool)
	jh := jobs.NewHandlers(store, layout, jobs.NewBroker(pool), jobs.Options{AnonRetention: time.Hour, UserRetention: time.Hour})
	mux := http.NewServeMux()
	sh := stems.NewHandlers(jh, layout)
	sh.Register(mux)
	sh.RegisterDownload(mux)
	ts := httptest.NewServer(mux)
	defer ts.Close()

	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	id := fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
	ctx := context.Background()
	token, hash, _ := jobs.NewClaimToken()
	if _, err := store.Create(ctx, id, jobs.CreateParams{ClaimTokenHash: hash, ProjectName: "My Song", InputType: jobs.InputUpload,
		Quality: jobs.QualityFast, ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	get := func() *http.Response {
		req, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/v1/jobs/"+id+"/download", nil)
		req.Header.Set(jobs.ClaimTokenHeader, token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}
	// Not completed yet → 409.
	resp := get()
	resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("pending download: %d", resp.StatusCode)
	}
	for _, st := range []string{jobs.StatusConverting, jobs.StatusCompleted} {
		if err := jobs.Transition(ctx, pool, id, st, 0, st); err != nil {
			t.Fatal(err)
		}
	}
	// Completed but no file → 404.
	resp = get()
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("missing package: %d", resp.StatusCode)
	}
	dir, _ := layout.JobDir(id)
	_ = os.MkdirAll(dir, 0o755)
	if err := os.WriteFile(filepath.Join(dir, "package.zip"), []byte("PK\x03\x04zip"), 0o644); err != nil {
		t.Fatal(err)
	}
	resp = get()
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || string(body) != "PK\x03\x04zip" {
		t.Fatalf("download: %d %q", resp.StatusCode, body)
	}
	if cd := resp.Header.Get("Content-Disposition"); cd != `attachment; filename="My Song-stems.zip"` {
		t.Fatalf("disposition %q", cd)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/zip" {
		t.Fatalf("content-type %q", ct)
	}
}
