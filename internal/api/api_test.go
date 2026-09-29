package api_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BeFeast/RehearseKit/internal/api"
	"github.com/BeFeast/RehearseKit/internal/auth"
	"github.com/BeFeast/RehearseKit/internal/auth/googleid/googleidtest"
	"github.com/BeFeast/RehearseKit/internal/config"
	"github.com/BeFeast/RehearseKit/internal/db/dbtest"
	"github.com/BeFeast/RehearseKit/internal/jobs"
)

func TestMain(m *testing.M) {
	// Request logs would drown the test output.
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	os.Exit(m.Run())
}

type env struct {
	t       *testing.T
	pool    *pgxpool.Pool
	srv     *api.Server
	ts      *httptest.Server
	dataDir string
	cfg     config.Config
}

func newEnv(t *testing.T, mutate func(*config.Config)) *env {
	t.Helper()
	pool := dbtest.Pool(t)
	cfg := config.Defaults()
	cfg.DataDir = t.TempDir()
	cfg.MaxUploadBytes = 2 << 20 // 2 MiB keeps the too-large test cheap
	if mutate != nil {
		mutate(&cfg)
	}
	srv, err := api.New(cfg, pool)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	srv.RunBroker(ctx)
	select {
	case <-srv.Broker().Ready():
	case <-time.After(5 * time.Second):
		t.Fatal("broker not ready")
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(func() { ts.Close(); cancel() })
	return &env{t: t, pool: pool, srv: srv, ts: ts, dataDir: cfg.DataDir, cfg: cfg}
}

// client returns an http.Client with its own cookie jar.
func (e *env) client() *http.Client {
	jar, _ := cookiejar.New(nil)
	return &http.Client{Jar: jar, Timeout: 30 * time.Second}
}

func (e *env) do(c *http.Client, method, path string, body any, headers map[string]string) (*http.Response, []byte) {
	e.t.Helper()
	var rdr io.Reader
	ct := ""
	switch b := body.(type) {
	case nil:
	case []byte:
		rdr = bytes.NewReader(b)
	case string:
		rdr = strings.NewReader(b)
	default:
		buf, _ := json.Marshal(b)
		rdr = bytes.NewReader(buf)
		ct = "application/json"
	}
	req, err := http.NewRequest(method, e.ts.URL+path, rdr)
	if err != nil {
		e.t.Fatal(err)
	}
	if ct != "" {
		req.Header.Set("Content-Type", ct)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := c.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp, out
}

func (e *env) createUser(email, status string) *auth.User {
	e.t.Helper()
	u, err := auth.NewStore(e.pool).CreateUser(context.Background(), auth.CreateUserParams{
		Email: email, Name: "Test", Password: "password123", Provider: auth.ProviderPassword, Role: auth.RoleUser, Status: status,
	})
	if err != nil {
		e.t.Fatal(err)
	}
	return u
}

func (e *env) login(c *http.Client, email string) {
	e.t.Helper()
	resp, body := e.do(c, "POST", "/api/v1/auth/login", map[string]string{"email": email, "password": "password123"}, nil)
	if resp.StatusCode != 200 {
		e.t.Fatalf("login %s: %d %s", email, resp.StatusCode, body)
	}
}

// upload POSTs a multipart job with a synthetic mp3 payload of n bytes.
func (e *env) upload(c *http.Client, n int, fields map[string]string) (*http.Response, []byte, []byte) {
	e.t.Helper()
	payload := make([]byte, max(n, 0))
	for i := range payload {
		payload[i] = byte(i * 31)
	}
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for k, v := range fields {
		_ = mw.WriteField(k, v)
	}
	if n >= 0 {
		fw, _ := mw.CreateFormFile("file", "My Song.mp3")
		_, _ = fw.Write(payload)
	}
	_ = mw.Close()
	resp, body := e.do(c, "POST", "/api/v1/jobs", buf.Bytes(), map[string]string{"Content-Type": mw.FormDataContentType()})
	return resp, body, payload
}

func decodeJob(t *testing.T, body []byte) jobs.Job {
	t.Helper()
	var j jobs.Job
	if err := json.Unmarshal(body, &j); err != nil {
		t.Fatalf("decode job: %v: %s", err, body)
	}
	return j
}

func errCode(body []byte) string {
	var e struct {
		Code string `json:"code"`
	}
	_ = json.Unmarshal(body, &e)
	return e.Code
}

func TestHealthConfigAndSPA(t *testing.T) {
	e := newEnv(t, func(c *config.Config) { c.GoogleClientID = "gid" })
	c := e.client()
	resp, body := e.do(c, "GET", "/healthz", nil, nil)
	if resp.StatusCode != 200 || !strings.Contains(string(body), `"ok"`) {
		t.Errorf("healthz %d %s", resp.StatusCode, body)
	}
	resp, _ = e.do(c, "GET", "/readyz", nil, nil)
	if resp.StatusCode != 200 {
		t.Errorf("readyz %d", resp.StatusCode)
	}
	resp, body = e.do(c, "GET", "/api/v1/config", nil, nil)
	var cfg map[string]any
	if err := json.Unmarshal(body, &cfg); err != nil || resp.StatusCode != 200 {
		t.Fatalf("config %d %s", resp.StatusCode, body)
	}
	if cfg["google_client_id"] != "gid" || cfg["google_sign_in"] != true || cfg["anon_retention_hours"].(float64) != 24 || cfg["job_retention_days"].(float64) != 7 || !strings.Contains(string(body), `"qualities":[{"id":"high","label":"Standard","model":"SCNet XL","stems":4},{"id":"hifi","label":"Plus HiFi","model":"RoFormer vocals + SCNet XL","stems":4}]`) {
		t.Errorf("config: %v", cfg)
	}
	resp, body = e.do(c, "GET", "/api/v1/nope", nil, nil)
	if resp.StatusCode != 404 || errCode(body) != "not_found" {
		t.Errorf("api 404: %d %s", resp.StatusCode, body)
	}
	// Client id set but the token is garbage: the verifier answers, not a 501.
	resp, body = e.do(c, "POST", "/api/v1/auth/google", map[string]string{"credential": "x"}, nil)
	if resp.StatusCode != 401 || errCode(body) != "invalid_google_token" {
		t.Errorf("google garbage: %d %s", resp.StatusCode, body)
	}

	resp, body = e.do(c, "GET", "/", nil, nil)
	if resp.StatusCode != 200 || !strings.Contains(string(body), "RehearseKit") || resp.Header.Get("Cross-Origin-Opener-Policy") != "" {
		t.Errorf("spa root: %d COOP=%q", resp.StatusCode, resp.Header.Get("Cross-Origin-Opener-Policy"))
	}
	resp, body = e.do(c, "GET", "/jobs/abc", nil, nil)
	if resp.StatusCode != 200 || !strings.Contains(string(body), "RehearseKit") {
		t.Errorf("spa fallback: %d", resp.StatusCode)
	}
	if resp.Header.Get("Cross-Origin-Opener-Policy") != "same-origin" || resp.Header.Get("Cross-Origin-Embedder-Policy") != "credentialless" {
		t.Errorf("COOP/COEP on /jobs/{id}: %v", resp.Header)
	}
	// The list is where the Google popup opens, so it must not be isolated.
	resp, _ = e.do(c, "GET", "/jobs", nil, nil)
	if resp.StatusCode != 200 || resp.Header.Get("Cross-Origin-Opener-Policy") != "" || resp.Header.Get("Cross-Origin-Embedder-Policy") != "" {
		t.Errorf("COOP/COEP on /jobs list: %d %v", resp.StatusCode, resp.Header)
	}
}

func TestAuthFlow(t *testing.T) {
	e := newEnv(t, nil)
	e.createUser("active@example.com", auth.StatusActive)
	e.createUser("pending@example.com", auth.StatusPending)
	e.createUser("inactive@example.com", auth.StatusInactive)
	c := e.client()

	resp, body := e.do(c, "GET", "/api/v1/auth/me", nil, nil)
	if resp.StatusCode != 401 || errCode(body) != "unauthorized" {
		t.Errorf("me anon: %d %s", resp.StatusCode, body)
	}
	resp, body = e.do(c, "POST", "/api/v1/auth/login", map[string]string{"email": "pending@example.com", "password": "password123"}, nil)
	if resp.StatusCode != 403 || errCode(body) != "pending_approval" {
		t.Errorf("pending login: %d %s", resp.StatusCode, body)
	}
	resp, body = e.do(c, "POST", "/api/v1/auth/login", map[string]string{"email": "inactive@example.com", "password": "password123"}, nil)
	if resp.StatusCode != 403 || errCode(body) != "account_inactive" {
		t.Errorf("inactive login: %d %s", resp.StatusCode, body)
	}
	resp, body = e.do(c, "POST", "/api/v1/auth/login", map[string]string{"email": "active@example.com", "password": "nope"}, nil)
	if resp.StatusCode != 401 || errCode(body) != "invalid_credentials" {
		t.Errorf("bad password: %d %s", resp.StatusCode, body)
	}
	resp, body = e.do(c, "POST", "/api/v1/auth/login", map[string]string{"email": "ghost@example.com", "password": "nope"}, nil)
	if resp.StatusCode != 401 {
		t.Errorf("unknown user: %d %s", resp.StatusCode, body)
	}

	resp, body = e.do(c, "POST", "/api/v1/auth/login", map[string]string{"email": "Active@Example.com", "password": "password123"}, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("login: %d %s", resp.StatusCode, body)
	}
	var cookie *http.Cookie
	for _, ck := range resp.Cookies() {
		if ck.Name == auth.CookieName {
			cookie = ck
		}
	}
	if cookie == nil || !cookie.HttpOnly || cookie.SameSite != http.SameSiteLaxMode || cookie.Secure || cookie.Path != "/" {
		t.Fatalf("cookie: %+v", cookie)
	}
	if d := time.Until(cookie.Expires); d < 29*24*time.Hour || d > 31*24*time.Hour {
		t.Errorf("cookie expiry %v", d)
	}
	if strings.Contains(string(body), "password_hash") {
		t.Error("password hash leaked in login response")
	}
	resp, body = e.do(c, "GET", "/api/v1/auth/me", nil, nil)
	if resp.StatusCode != 200 || !strings.Contains(string(body), `"email":"active@example.com"`) {
		t.Errorf("me: %d %s", resp.StatusCode, body)
	}
	resp, _ = e.do(c, "POST", "/api/v1/auth/logout", nil, nil)
	if resp.StatusCode != 204 {
		t.Errorf("logout: %d", resp.StatusCode)
	}
	resp, _ = e.do(c, "GET", "/api/v1/auth/me", nil, nil)
	if resp.StatusCode != 401 {
		t.Errorf("me after logout: %d", resp.StatusCode)
	}

	// A forged cookie is ignored.
	c2 := e.client()
	resp, _ = e.do(c2, "GET", "/api/v1/auth/me", nil, map[string]string{"Cookie": auth.CookieName + "=AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"})
	if resp.StatusCode != 401 {
		t.Errorf("forged cookie: %d", resp.StatusCode)
	}

	// Secure flag follows X-Forwarded-Proto.
	resp, _ = e.do(c2, "POST", "/api/v1/auth/login", map[string]string{"email": "active@example.com", "password": "password123"}, map[string]string{"X-Forwarded-Proto": "https"})
	for _, ck := range resp.Cookies() {
		if ck.Name == auth.CookieName && !ck.Secure {
			t.Error("cookie not Secure behind https proxy")
		}
	}

	// Register creates a pending account; login is refused until approval.
	resp, body = e.do(c2, "POST", "/api/v1/auth/register", map[string]string{"email": "new@example.com", "password": "password123", "name": "New"}, nil)
	if resp.StatusCode != 201 || !strings.Contains(string(body), `"status":"pending"`) {
		t.Errorf("register: %d %s", resp.StatusCode, body)
	}
	resp, body = e.do(c2, "POST", "/api/v1/auth/register", map[string]string{"email": "new@example.com", "password": "password123"}, nil)
	if resp.StatusCode != 409 || errCode(body) != "email_taken" {
		t.Errorf("register dup: %d %s", resp.StatusCode, body)
	}
	resp, body = e.do(c2, "POST", "/api/v1/auth/register", map[string]string{"email": "short@example.com", "password": "short"}, nil)
	if resp.StatusCode != 400 || errCode(body) != "weak_password" {
		t.Errorf("register weak: %d %s", resp.StatusCode, body)
	}
}

func TestAdminUsers(t *testing.T) {
	e := newEnv(t, nil)
	admin, err := auth.NewStore(e.pool).UpsertAdmin(context.Background(), "admin@example.com", "password123")
	if err != nil {
		t.Fatal(err)
	}
	pending := e.createUser("pending@example.com", auth.StatusPending)
	user := e.client()
	e.createUser("user@example.com", auth.StatusActive)
	e.login(user, "user@example.com")
	resp, body := e.do(user, "GET", "/api/v1/admin/users", nil, nil)
	if resp.StatusCode != 403 || errCode(body) != "forbidden" {
		t.Errorf("non-admin list: %d %s", resp.StatusCode, body)
	}

	c := e.client()
	e.login(c, "admin@example.com")
	resp, body = e.do(c, "GET", "/api/v1/admin/users?status=pending", nil, nil)
	if resp.StatusCode != 200 || !strings.Contains(string(body), `"total":1`) {
		t.Errorf("list pending: %d %s", resp.StatusCode, body)
	}
	resp, body = e.do(c, "POST", "/api/v1/admin/users/"+pending.ID+"/approve", nil, nil)
	if resp.StatusCode != 200 || !strings.Contains(string(body), `"status":"active"`) {
		t.Errorf("approve: %d %s", resp.StatusCode, body)
	}
	e.login(e.client(), "pending@example.com") // now succeeds
	resp, body = e.do(c, "POST", "/api/v1/admin/users/"+admin.ID+"/deactivate", nil, nil)
	if resp.StatusCode != 409 || errCode(body) != "last_admin" {
		t.Errorf("deactivate last admin: %d %s", resp.StatusCode, body)
	}
	resp, body = e.do(c, "POST", "/api/v1/admin/users/"+admin.ID+"/role", map[string]string{"role": "user"}, nil)
	if resp.StatusCode != 409 {
		t.Errorf("demote last admin: %d %s", resp.StatusCode, body)
	}
	resp, body = e.do(c, "POST", "/api/v1/admin/users/"+pending.ID+"/role", map[string]string{"role": "admin"}, nil)
	if resp.StatusCode != 200 || !strings.Contains(string(body), `"role":"admin"`) {
		t.Errorf("promote: %d %s", resp.StatusCode, body)
	}
	resp, _ = e.do(c, "POST", "/api/v1/admin/users/"+admin.ID+"/role", map[string]string{"role": "user"}, nil)
	if resp.StatusCode != 200 {
		t.Errorf("demote with another admin present: %d", resp.StatusCode)
	}
}

func TestJobLifecycle(t *testing.T) {
	e := newEnv(t, nil)
	e.createUser("owner@example.com", auth.StatusActive)
	e.createUser("other@example.com", auth.StatusActive)
	c := e.client()
	e.login(c, "owner@example.com")

	resp, body, payload := e.upload(c, 300_000, map[string]string{"quality": "high"})
	if resp.StatusCode != 201 {
		t.Fatalf("create: %d %s", resp.StatusCode, body)
	}
	j := decodeJob(t, body)
	if j.Status != "pending" || j.Quality != "high" || j.ProjectName != "My Song" || j.InputType != "upload" || j.SourceFilename == nil || *j.SourceFilename != "My Song.mp3" || j.OwnerID == nil || j.ClaimToken != "" {
		t.Errorf("created job: %+v", j)
	}
	if d := time.Until(j.ExpiresAt); d < 6*24*time.Hour || d > 8*24*time.Hour {
		t.Errorf("owner retention: %v", d)
	}
	stored, err := os.ReadFile(filepath.Join(e.dataDir, "jobs", j.ID, "source.mp3"))
	if err != nil || !bytes.Equal(stored, payload) {
		t.Fatalf("stored upload mismatch: %v (%d bytes)", err, len(stored))
	}

	resp, body = e.do(c, "GET", "/api/v1/jobs", nil, nil)
	if resp.StatusCode != 200 || !strings.Contains(string(body), `"total":1`) || !strings.Contains(string(body), j.ID) {
		t.Errorf("list: %d %s", resp.StatusCode, body)
	}
	resp, body = e.do(c, "GET", "/api/v1/jobs?status=completed", nil, nil)
	if resp.StatusCode != 200 || !strings.Contains(string(body), `"total":0`) {
		t.Errorf("list completed: %d %s", resp.StatusCode, body)
	}
	resp, body = e.do(c, "GET", "/api/v1/jobs?status=weird", nil, nil)
	if resp.StatusCode != 400 {
		t.Errorf("list bad status: %d %s", resp.StatusCode, body)
	}
	resp, body = e.do(c, "GET", "/api/v1/jobs/"+j.ID, nil, nil)
	if resp.StatusCode != 200 || decodeJob(t, body).ID != j.ID {
		t.Errorf("get: %d %s", resp.StatusCode, body)
	}

	// Other users and anonymous visitors cannot see an owned job.
	other := e.client()
	e.login(other, "other@example.com")
	resp, _ = e.do(other, "GET", "/api/v1/jobs/"+j.ID, nil, nil)
	if resp.StatusCode != 404 {
		t.Errorf("other user get: %d", resp.StatusCode)
	}
	resp, _ = e.do(other, "GET", "/api/v1/jobs", nil, nil)
	if !strings.Contains(string(mustBody(e, other, "/api/v1/jobs")), `"total":0`) {
		t.Errorf("other user list leaks")
	}
	anon := e.client()
	resp, _ = e.do(anon, "GET", "/api/v1/jobs/"+j.ID, nil, nil)
	if resp.StatusCode != 401 {
		t.Errorf("anon get owned: %d", resp.StatusCode)
	}
	resp, _ = e.do(anon, "GET", "/api/v1/jobs", nil, nil)
	if resp.StatusCode != 401 {
		t.Errorf("anon list: %d", resp.StatusCode)
	}
	resp, _ = e.do(anon, "POST", "/api/v1/jobs/"+j.ID+"/cancel", nil, nil)
	if resp.StatusCode != 401 {
		t.Errorf("anon cancel: %d", resp.StatusCode)
	}

	resp, body = e.do(c, "POST", "/api/v1/jobs/"+j.ID+"/cancel", nil, nil)
	if resp.StatusCode != 200 || decodeJob(t, body).Status != "cancelled" {
		t.Errorf("cancel: %d %s", resp.StatusCode, body)
	}
	resp, body = e.do(c, "POST", "/api/v1/jobs/"+j.ID+"/cancel", nil, nil)
	if resp.StatusCode != 409 || errCode(body) != "already_finished" {
		t.Errorf("cancel twice: %d %s", resp.StatusCode, body)
	}
	resp, _ = e.do(c, "GET", "/api/v1/jobs?status=failed", nil, nil)
	if !strings.Contains(string(mustBody(e, c, "/api/v1/jobs?status=failed")), `"total":1`) {
		t.Errorf("cancelled job not under failed filter")
	}
	resp, _ = e.do(other, "DELETE", "/api/v1/jobs/"+j.ID, nil, nil)
	if resp.StatusCode != 404 {
		t.Errorf("other delete: %d", resp.StatusCode)
	}
	resp, _ = e.do(c, "DELETE", "/api/v1/jobs/"+j.ID, nil, nil)
	if resp.StatusCode != 204 {
		t.Errorf("delete: %d", resp.StatusCode)
	}
	if _, err := os.Stat(filepath.Join(e.dataDir, "jobs", j.ID)); !os.IsNotExist(err) {
		t.Error("job directory survived delete")
	}
	resp, _ = e.do(c, "GET", "/api/v1/jobs/"+j.ID, nil, nil)
	if resp.StatusCode != 404 {
		t.Errorf("get after delete: %d", resp.StatusCode)
	}
	var events int
	_ = e.pool.QueryRow(context.Background(), `SELECT count(*) FROM job_events WHERE job_id = $1`, j.ID).Scan(&events)
	if events != 0 {
		t.Errorf("%d events survived delete", events)
	}
}

func mustBody(e *env, c *http.Client, path string) []byte {
	_, body := e.do(c, "GET", path, nil, nil)
	return body
}

func TestJobCreateValidation(t *testing.T) {
	e := newEnv(t, nil)
	c := e.client()
	resp, body, _ := e.upload(c, -1, map[string]string{"project_name": "x"})
	if resp.StatusCode != 400 || errCode(body) != "missing_input" {
		t.Errorf("no input: %d %s", resp.StatusCode, body)
	}
	resp, body, _ = e.upload(c, 10, map[string]string{"input_url": "https://youtu.be/abc"})
	if resp.StatusCode != 400 || errCode(body) != "ambiguous_input" {
		t.Errorf("both inputs: %d %s", resp.StatusCode, body)
	}
	resp, body, _ = e.upload(c, 10, map[string]string{"quality": "ultra"})
	if resp.StatusCode != 400 || errCode(body) != "invalid_quality" {
		t.Errorf("bad quality: %d %s", resp.StatusCode, body)
	}
	resp, body, _ = e.upload(c, 0, nil)
	if resp.StatusCode != 400 || errCode(body) != "empty_file" {
		t.Errorf("empty file: %d %s", resp.StatusCode, body)
	}
	resp, body, _ = e.upload(c, 3<<20, nil)
	if resp.StatusCode != 413 || errCode(body) != "too_large" {
		t.Errorf("too large: %d %s", resp.StatusCode, body)
	}
	resp, body, _ = e.upload(c, -1, map[string]string{"input_url": "https://example.com/x.mp3"})
	if resp.StatusCode != 400 || errCode(body) != "invalid_url" {
		t.Errorf("non-youtube url: %d %s", resp.StatusCode, body)
	}
	resp, body, _ = e.upload(c, -1, map[string]string{"input_url": "https://www.youtube.com/watch?v=abc", "project_name": "Tube"})
	if resp.StatusCode != 201 {
		t.Fatalf("youtube: %d %s", resp.StatusCode, body)
	}
	j := decodeJob(t, body)
	if j.InputType != "youtube" || j.InputURL == nil || j.ProjectName != "Tube" || j.ClaimToken == "" || j.Quality != "high" || j.Stack != "public" || j.Model != "scnet_xl_ihf" {
		t.Errorf("youtube job: %+v", j)
	}
	// Long multi-byte names are cut on a rune boundary, never mid-rune.
	cjk := strings.Repeat("音", 80) // 240 bytes
	resp, body, _ = e.upload(c, 10, map[string]string{"project_name": cjk})
	if resp.StatusCode != 201 {
		t.Fatalf("cjk name: %d %s", resp.StatusCode, body)
	}
	if got := decodeJob(t, body).ProjectName; got != strings.Repeat("音", 66) {
		t.Errorf("cjk name truncated to %q (%d bytes)", got, len(got))
	}
	resp, body = e.do(c, "POST", "/api/v1/jobs", "not multipart", map[string]string{"Content-Type": "application/json"})
	if resp.StatusCode != 400 || errCode(body) != "invalid_multipart" {
		t.Errorf("json body: %d %s", resp.StatusCode, body)
	}
	entries, _ := os.ReadDir(filepath.Join(e.dataDir, "jobs"))
	if len(entries) != 1 { // only the cjk job; every rejected upload was cleaned up
		t.Errorf("rejected uploads left %d job dirs behind", len(entries)-1)
	}
}

func TestTranscribeGate(t *testing.T) {
	e := newEnv(t, func(c *config.Config) {
		c.TranscribeEmails = []string{"Owner@Example.com", "public@example.com"}
		c.InternalEmails = []string{"owner@example.com"}
	})
	anon := e.client()
	resp, body, _ := e.upload(anon, 10, map[string]string{"transcribe": "1", "quality": "high6"})
	if resp.StatusCode != 403 || errCode(body) != "transcribe_not_allowed" {
		t.Errorf("anonymous: %d %s", resp.StatusCode, body)
	}
	e.createUser("other@example.com", auth.StatusActive)
	other := e.client()
	e.login(other, "other@example.com")
	resp, body, _ = e.upload(other, 10, map[string]string{"transcribe": "true", "quality": "high6"})
	if resp.StatusCode != 403 || errCode(body) != "transcribe_not_allowed" {
		t.Errorf("not allow-listed: %d %s", resp.StatusCode, body)
	}
	_, body = e.do(other, "GET", "/api/v1/auth/me", nil, nil)
	if !strings.Contains(string(body), `"features":[]`) {
		t.Errorf("me without features: %s", body)
	}
	// On the transcribe allowlist but on the public stack: the adapters are
	// research-licensed, so no transcription and no feature flag.
	e.createUser("public@example.com", auth.StatusActive)
	pub := e.client()
	e.login(pub, "public@example.com")
	resp, body, _ = e.upload(pub, 10, map[string]string{"transcribe": "1", "quality": "hifi"})
	if resp.StatusCode != 403 || errCode(body) != "transcribe_not_allowed" {
		t.Errorf("public stack: %d %s", resp.StatusCode, body)
	}
	_, body = e.do(pub, "GET", "/api/v1/auth/me", nil, nil)
	if !strings.Contains(string(body), `"features":[]`) {
		t.Errorf("public stack features: %s", body)
	}
	e.createUser("owner@example.com", auth.StatusActive)
	owner := e.client()
	resp, body = e.do(owner, "POST", "/api/v1/auth/login", map[string]string{"email": "owner@example.com", "password": "password123"}, nil)
	if resp.StatusCode != 200 || !strings.Contains(string(body), `"features":["transcribe","internal"]`) {
		t.Fatalf("login features: %d %s", resp.StatusCode, body)
	}
	_, body = e.do(owner, "GET", "/api/v1/auth/me", nil, nil)
	if !strings.Contains(string(body), `"features":["transcribe","internal"]`) {
		t.Errorf("me features: %s", body)
	}
	resp, body, _ = e.upload(owner, 10, map[string]string{"transcribe": "1", "quality": "high"})
	if resp.StatusCode != 400 || errCode(body) != "transcribe_requires_high6" {
		t.Errorf("high: %d %s", resp.StatusCode, body)
	}
	resp, body, _ = e.upload(owner, 10, map[string]string{"transcribe": "1", "quality": "high6"})
	if resp.StatusCode != 201 {
		t.Fatalf("owner high6: %d %s", resp.StatusCode, body)
	}
	j := decodeJob(t, body)
	if !j.Transcribe || j.Quality != "high6" {
		t.Errorf("job: %+v", j)
	}
	_, body = e.do(owner, "GET", "/api/v1/jobs/"+j.ID, nil, nil)
	if !strings.Contains(string(body), `"transcribe":true`) {
		t.Errorf("get job: %s", body)
	}
	resp, body, _ = e.upload(owner, 10, map[string]string{"quality": "high6"})
	if resp.StatusCode != 201 || decodeJob(t, body).Transcribe {
		t.Errorf("without the flag: %d %s", resp.StatusCode, body)
	}
	// Internal 6-stem HiFi (Kim + BS-RoFormer SW) carries guitar and piano too.
	resp, body, _ = e.upload(owner, 10, map[string]string{"transcribe": "1", "quality": "hifi"})
	if resp.StatusCode != 201 {
		t.Fatalf("owner hifi: %d %s", resp.StatusCode, body)
	}
	if j := decodeJob(t, body); !j.Transcribe || j.Model != "kim+bs_rofo_sw" || j.Stack != "internal" {
		t.Errorf("hifi job: %+v", j)
	}
	entries, _ := os.ReadDir(filepath.Join(e.dataDir, "jobs"))
	if len(entries) != 3 {
		t.Errorf("rejected uploads left job dirs behind: %d", len(entries))
	}
}

// Entitlement picks the model stack server-side: anonymous and ordinary
// accounts get the public stack only, whatever quality they ask for.
func TestModelStacks(t *testing.T) {
	e := newEnv(t, func(c *config.Config) { c.InternalEmails = []string{"Friend@Example.com"} })
	anon := e.client()
	for _, q := range []string{"fast", "high6"} {
		resp, body, _ := e.upload(anon, 10, map[string]string{"quality": q})
		if resp.StatusCode != 403 || errCode(body) != "quality_not_available" {
			t.Errorf("anonymous %s: %d %s", q, resp.StatusCode, body)
		}
	}
	e.createUser("user@example.com", auth.StatusActive)
	user := e.client()
	e.login(user, "user@example.com")
	for q, model := range map[string]string{"high": "scnet_xl_ihf", "hifi": "kim+scnet_xl_ihf"} {
		for name, c := range map[string]*http.Client{"anonymous": anon, "user": user} {
			resp, body, _ := e.upload(c, 10, map[string]string{"quality": q})
			if resp.StatusCode != 201 {
				t.Fatalf("%s %s: %d %s", name, q, resp.StatusCode, body)
			}
			if j := decodeJob(t, body); j.Stack != "public" || j.Model != model {
				t.Errorf("%s %s: stack %s model %s, want public %s", name, q, j.Stack, j.Model, model)
			}
		}
	}
	resp, body, _ := e.upload(user, 10, map[string]string{"quality": "fast"})
	if resp.StatusCode != 403 || errCode(body) != "quality_not_available" {
		t.Errorf("user fast: %d %s", resp.StatusCode, body)
	}

	e.createUser("friend@example.com", auth.StatusActive)
	friend := e.client()
	e.login(friend, "friend@example.com")
	for q, model := range map[string]string{"fast": "htdemucs", "high": "htdemucs_ft", "high6": "htdemucs_6s", "hifi": "kim+bs_rofo_sw"} {
		resp, body, _ := e.upload(friend, 10, map[string]string{"quality": q})
		if resp.StatusCode != 201 {
			t.Fatalf("friend %s: %d %s", q, resp.StatusCode, body)
		}
		if j := decodeJob(t, body); j.Stack != "internal" || j.Model != model {
			t.Errorf("friend %s: stack %s model %s, want internal %s", q, j.Stack, j.Model, model)
		}
	}
	_, body = e.do(friend, "GET", "/api/v1/config", nil, nil)
	if !strings.Contains(string(body), `{"id":"fast"`) || !strings.Contains(string(body), `{"id":"hifi","label":"Plus HiFi","model":"RoFormer vocals + BS-RoFormer SW","stems":6}`) {
		t.Errorf("internal config: %s", body)
	}
	_, body = e.do(friend, "GET", "/api/v1/auth/me", nil, nil)
	if !strings.Contains(string(body), `"features":["internal"]`) {
		t.Errorf("internal features: %s", body)
	}
	_, body = e.do(user, "GET", "/api/v1/config", nil, nil)
	if strings.Contains(string(body), `"fast"`) || strings.Contains(string(body), "Demucs") {
		t.Errorf("public config leaks internal presets: %s", body)
	}

	// The database refuses a public job on internal weights even if code
	// ever tried to write one.
	_, err := e.pool.Exec(context.Background(), `UPDATE jobs SET model = 'htdemucs_ft' WHERE stack = 'public'`)
	if err == nil || !strings.Contains(err.Error(), "jobs_public_model_check") {
		t.Errorf("public job on Demucs accepted by the database: %v", err)
	}
}

func TestAnonymousJobAndClaim(t *testing.T) {
	e := newEnv(t, nil)
	e.createUser("owner@example.com", auth.StatusActive)
	e.createUser("thief@example.com", auth.StatusActive)
	anon := e.client()

	resp, body, _ := e.upload(anon, 1000, nil)
	if resp.StatusCode != 201 {
		t.Fatalf("anon create: %d %s", resp.StatusCode, body)
	}
	j := decodeJob(t, body)
	if j.ClaimToken == "" || j.OwnerID != nil {
		t.Fatalf("anon job: %+v", j)
	}
	if d := time.Until(j.ExpiresAt); d < 23*time.Hour || d > 25*time.Hour {
		t.Errorf("anon retention: %v", d)
	}
	var hashLen int
	_ = e.pool.QueryRow(context.Background(), `SELECT length(claim_token_hash) FROM jobs WHERE id = $1`, j.ID).Scan(&hashLen)
	if hashLen != 32 {
		t.Errorf("claim_token_hash length %d, want 32 (sha-256)", hashLen)
	}
	// The token never comes back on GET.
	resp, body = e.do(anon, "GET", "/api/v1/jobs/"+j.ID, nil, nil)
	if resp.StatusCode != 200 || strings.Contains(string(body), "claim_token") {
		t.Errorf("anon get: %d %s", resp.StatusCode, body)
	}
	// Anyone with the link may read while it lives; only the token holder may write.
	stranger := e.client()
	resp, _ = e.do(stranger, "GET", "/api/v1/jobs/"+j.ID, nil, nil)
	if resp.StatusCode != 200 {
		t.Errorf("stranger get: %d", resp.StatusCode)
	}
	resp, body = e.do(stranger, "POST", "/api/v1/jobs/"+j.ID+"/cancel", nil, nil)
	if resp.StatusCode != 403 || errCode(body) != "claim_token_required" {
		t.Errorf("stranger cancel: %d %s", resp.StatusCode, body)
	}
	resp, _ = e.do(stranger, "DELETE", "/api/v1/jobs/"+j.ID, nil, map[string]string{"X-Claim-Token": "wrong"})
	if resp.StatusCode != 403 {
		t.Errorf("wrong token delete: %d", resp.StatusCode)
	}

	// Claim needs a session and the right token.
	resp, _ = e.do(anon, "POST", "/api/v1/jobs/"+j.ID+"/claim", map[string]string{"claim_token": j.ClaimToken}, nil)
	if resp.StatusCode != 401 {
		t.Errorf("claim anon: %d", resp.StatusCode)
	}
	thief := e.client()
	e.login(thief, "thief@example.com")
	resp, body = e.do(thief, "POST", "/api/v1/jobs/"+j.ID+"/claim", map[string]string{"claim_token": "nope"}, nil)
	if resp.StatusCode != 403 || errCode(body) != "invalid_claim_token" {
		t.Errorf("claim bad token: %d %s", resp.StatusCode, body)
	}
	owner := e.client()
	e.login(owner, "owner@example.com")
	resp, body = e.do(owner, "POST", "/api/v1/jobs/"+j.ID+"/claim", map[string]string{"claim_token": j.ClaimToken}, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("claim: %d %s", resp.StatusCode, body)
	}
	claimed := decodeJob(t, body)
	if claimed.OwnerID == nil || time.Until(claimed.ExpiresAt) < 6*24*time.Hour {
		t.Errorf("claimed job: %+v", claimed)
	}
	resp, body = e.do(thief, "POST", "/api/v1/jobs/"+j.ID+"/claim", map[string]string{"claim_token": j.ClaimToken}, nil)
	if resp.StatusCode != 409 || errCode(body) != "already_claimed" {
		t.Errorf("claim twice: %d %s", resp.StatusCode, body)
	}
	// After the claim the link is private and the old token is dead.
	resp, _ = e.do(stranger, "GET", "/api/v1/jobs/"+j.ID, nil, map[string]string{"X-Claim-Token": j.ClaimToken})
	if resp.StatusCode != 401 {
		t.Errorf("stranger after claim: %d", resp.StatusCode)
	}
	if !strings.Contains(string(mustBody(e, owner, "/api/v1/jobs")), j.ID) {
		t.Error("claimed job missing from owner's list")
	}

	// Expired anonymous jobs answer 410 for link visitors, but the token holder still gets in.
	resp, body, _ = e.upload(anon, 10, nil)
	j2 := decodeJob(t, body)
	if _, err := e.pool.Exec(context.Background(), `UPDATE jobs SET expires_at = now() - interval '1 minute' WHERE id = $1`, j2.ID); err != nil {
		t.Fatal(err)
	}
	resp, body = e.do(stranger, "GET", "/api/v1/jobs/"+j2.ID, nil, nil)
	if resp.StatusCode != 410 || errCode(body) != "expired" {
		t.Errorf("expired: %d %s", resp.StatusCode, body)
	}
	resp, _ = e.do(stranger, "GET", "/api/v1/jobs/"+j2.ID, nil, map[string]string{"X-Claim-Token": j2.ClaimToken})
	if resp.StatusCode != 200 {
		t.Errorf("expired with token: %d", resp.StatusCode)
	}
	resp, _ = e.do(stranger, "DELETE", "/api/v1/jobs/"+j2.ID, nil, map[string]string{"X-Claim-Token": j2.ClaimToken})
	if resp.StatusCode != 204 {
		t.Errorf("token delete: %d", resp.StatusCode)
	}
}

// --- stems: ported from stemd/server_test.go ---

func (e *env) stemJob(c *http.Client) (jobs.Job, []byte) {
	e.t.Helper()
	resp, body, _ := e.upload(c, 10, nil)
	if resp.StatusCode != 201 {
		e.t.Fatalf("create: %d %s", resp.StatusCode, body)
	}
	j := decodeJob(e.t, body)
	dir := filepath.Join(e.dataDir, "jobs", j.ID)
	wav := make([]byte, 100_000)
	for i := range wav {
		wav[i] = byte(i * 7)
	}
	for _, sub := range []string{"stems", "peaks"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			e.t.Fatal(err)
		}
	}
	for _, name := range []string{"vocals.wav", "guitar.wav"} {
		if err := os.WriteFile(filepath.Join(dir, "stems", name), wav, 0o644); err != nil {
			e.t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "peaks", "vocals.pk"), wav[:5000], 0o644); err != nil {
		e.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "stems", "notes.txt"), []byte("x"), 0o644); err != nil {
		e.t.Fatal(err)
	}
	store := jobs.NewStore(e.pool)
	peaks := "peaks/vocals.pk"
	if err := store.UpsertStem(context.Background(), j.ID, jobs.Stem{Name: "vocals", Bytes: 100000, Frames: 16659, SampleRate: 48000, BitDepth: 24, Channels: 2}, "stems/vocals.wav", &peaks); err != nil {
		e.t.Fatal(err)
	}
	if err := store.UpsertStem(context.Background(), j.ID, jobs.Stem{Name: "guitar", Bytes: 100000, Frames: 16659, SampleRate: 48000, BitDepth: 24, Channels: 2}, "stems/guitar.wav", nil); err != nil {
		e.t.Fatal(err)
	}
	return j, wav
}

func TestStemsFullFileAndManifest(t *testing.T) {
	e := newEnv(t, nil)
	c := e.client()
	j, wav := e.stemJob(c)
	base := "/api/v1/jobs/" + j.ID

	resp, body := e.do(c, "GET", base, nil, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("manifest: %d %s", resp.StatusCode, body)
	}
	m := decodeJob(t, body)
	if len(m.Stems) != 2 || m.Stems[0].Name != "guitar" || m.Stems[1].Name != "vocals" || m.Stems[1].Bytes != 100000 {
		t.Fatalf("manifest stems: %+v", m.Stems)
	}
	if m.Stems[1].StreamURL != base+"/stems/vocals" || m.Stems[1].PeaksURL == nil || *m.Stems[1].PeaksURL != base+"/stems/vocals/peaks" || m.Stems[0].PeaksURL != nil {
		t.Errorf("manifest urls: %+v", m.Stems)
	}

	resp, body = e.do(c, "GET", base+"/stems/vocals", nil, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	h := resp.Header
	if h.Get("Accept-Ranges") != "bytes" || h.Get("Content-Type") != "audio/wav" || h.Get("Content-Length") != "100000" {
		t.Errorf("headers: %v", h)
	}
	if !strings.HasPrefix(h.Get("Content-Disposition"), "inline") || h.Get("Cross-Origin-Resource-Policy") != "cross-origin" || !strings.HasPrefix(h.Get("Cache-Control"), "private") {
		t.Errorf("player headers: %v", h)
	}
	if !bytes.Equal(body, wav) {
		t.Error("body mismatch")
	}
	resp, body = e.do(c, "GET", base+"/stems/vocals/peaks", map[string]string{}, map[string]string{"Range": "bytes=0-9"})
	if resp.StatusCode != 206 || resp.Header.Get("Content-Type") != "application/octet-stream" || len(body) != 10 || !bytes.Equal(body, wav[:10]) {
		t.Errorf("peaks range: %d %v %d", resp.StatusCode, resp.Header, len(body))
	}
	resp, _ = e.do(c, "GET", base+"/stems/guitar/peaks", nil, nil)
	if resp.StatusCode != 404 {
		t.Errorf("missing peaks: %d", resp.StatusCode)
	}
}

func TestStemsPartialContent(t *testing.T) {
	e := newEnv(t, nil)
	c := e.client()
	j, wav := e.stemJob(c)
	url := "/api/v1/jobs/" + j.ID + "/stems/vocals"
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
	for _, tc := range cases {
		resp, body := e.do(c, "GET", url, nil, map[string]string{"Range": tc.rng})
		if resp.StatusCode != 206 {
			t.Errorf("%s: status %d", tc.rng, resp.StatusCode)
			continue
		}
		wantCR := fmt.Sprintf("bytes %d-%d/100000", tc.start, tc.end)
		if got := resp.Header.Get("Content-Range"); got != wantCR {
			t.Errorf("%s: Content-Range=%q want %q", tc.rng, got, wantCR)
		}
		if got := resp.Header.Get("Content-Length"); got != strconv.Itoa(tc.end-tc.start+1) {
			t.Errorf("%s: Content-Length=%q", tc.rng, got)
		}
		if !bytes.Equal(body, wav[tc.start:tc.end+1]) {
			t.Errorf("%s: body mismatch (len %d)", tc.rng, len(body))
		}
	}
}

func TestStemsUnsatisfiableHeadAndValidation(t *testing.T) {
	e := newEnv(t, nil)
	c := e.client()
	j, _ := e.stemJob(c)
	base := "/api/v1/jobs/" + j.ID
	for _, rng := range []string{"bytes=100000-", "bytes=200000-300000"} {
		resp, _ := e.do(c, "GET", base+"/stems/vocals", nil, map[string]string{"Range": rng})
		if resp.StatusCode != 416 || resp.Header.Get("Content-Range") != "bytes */100000" {
			t.Errorf("%s: status %d Content-Range=%q", rng, resp.StatusCode, resp.Header.Get("Content-Range"))
		}
	}
	resp, _ := e.do(c, "GET", base+"/stems/vocals", nil, map[string]string{"Range": "bytes=abc"})
	if resp.StatusCode != 200 && resp.StatusCode != 416 {
		t.Errorf("malformed range: %d", resp.StatusCode)
	}
	resp, body := e.do(c, "HEAD", base+"/stems/vocals", nil, nil)
	if resp.StatusCode != 200 || resp.Header.Get("Content-Length") != "100000" || len(body) != 0 {
		t.Errorf("HEAD: %d len=%s body=%d", resp.StatusCode, resp.Header.Get("Content-Length"), len(body))
	}
	for _, target := range []string{
		"/api/v1/jobs/not-a-uuid/stems/vocals",
		base + "/stems/Vocals",
		base + "/stems/vocals.wav",
		base + "/stems/notes.txt",
		base + "/stems/drums",
		base + "/stems/..%2Fsource.mp3",
	} {
		resp, _ := e.do(c, "GET", target, nil, nil)
		if resp.StatusCode == 200 {
			t.Errorf("%s: served a file", target)
		}
	}
	// Access rules apply to stems too.
	e.createUser("owner@example.com", auth.StatusActive)
	owner := e.client()
	e.login(owner, "owner@example.com")
	resp, _ = e.do(owner, "POST", base+"/claim", map[string]string{"claim_token": j.ClaimToken}, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("claim: %d", resp.StatusCode)
	}
	resp, _ = e.do(c, "GET", base+"/stems/vocals", nil, nil)
	if resp.StatusCode != 401 {
		t.Errorf("stem of owned job without session: %d", resp.StatusCode)
	}
	resp, _ = e.do(owner, "GET", base+"/stems/vocals", nil, map[string]string{"Range": "bytes=0-1"})
	if resp.StatusCode != 206 {
		t.Errorf("owner stem: %d", resp.StatusCode)
	}
}

func TestCORS(t *testing.T) {
	e := newEnv(t, func(c *config.Config) { c.CORSOrigins = []string{"http://localhost:5173"} })
	c := e.client()
	j, _ := e.stemJob(c)
	url := "/api/v1/jobs/" + j.ID + "/stems/vocals"
	resp, _ := e.do(c, "GET", url, nil, map[string]string{"Origin": "http://localhost:5173", "Range": "bytes=0-9"})
	if resp.Header.Get("Access-Control-Allow-Origin") != "http://localhost:5173" || resp.Header.Get("Access-Control-Allow-Credentials") != "true" {
		t.Errorf("ACAO headers: %v", resp.Header)
	}
	if !strings.Contains(resp.Header.Get("Access-Control-Expose-Headers"), "Content-Range") {
		t.Errorf("expose=%q", resp.Header.Get("Access-Control-Expose-Headers"))
	}
	resp, _ = e.do(c, "GET", url, nil, map[string]string{"Origin": "http://evil.example"})
	if resp.Header.Get("Access-Control-Allow-Origin") != "" {
		t.Errorf("unexpected ACAO for evil origin")
	}
	resp, _ = e.do(c, "OPTIONS", url, nil, map[string]string{"Origin": "http://localhost:5173", "Access-Control-Request-Method": "GET", "Access-Control-Request-Headers": "range"})
	if resp.StatusCode != 204 || !strings.Contains(resp.Header.Get("Access-Control-Allow-Headers"), "Range") || !strings.Contains(resp.Header.Get("Access-Control-Allow-Headers"), "X-Claim-Token") {
		t.Errorf("preflight: %d %v", resp.StatusCode, resp.Header)
	}

	// Default: no CORS headers at all.
	e2 := newEnv(t, nil)
	resp, _ = e2.do(e2.client(), "GET", "/healthz", nil, map[string]string{"Origin": "http://localhost:5173"})
	if resp.Header.Get("Access-Control-Allow-Origin") != "" {
		t.Error("CORS headers present without RK_CORS_ORIGINS")
	}
}

// --- SSE ---

type sseEvent struct {
	ID    int64
	Event string
	Data  struct {
		Status   string `json:"status"`
		Progress int    `json:"progress"`
		Message  string `json:"message"`
		At       string `json:"at"`
	}
}

// readSSE parses events until the stream ends or maxEvents arrive.
func readSSE(t *testing.T, body io.Reader, maxEvents int, deadline time.Duration) (events []sseEvent, comments int, ended bool) {
	t.Helper()
	sc := bufio.NewScanner(body)
	var mu sync.Mutex
	done := make(chan struct{})
	go func() {
		defer close(done)
		var cur sseEvent
		for sc.Scan() {
			line := sc.Text()
			mu.Lock()
			switch {
			case line == "":
				if cur.Event != "" {
					events = append(events, cur)
					cur = sseEvent{}
					if len(events) >= maxEvents {
						mu.Unlock()
						return
					}
				}
			case strings.HasPrefix(line, ":"):
				comments++
			case strings.HasPrefix(line, "id: "):
				cur.ID, _ = strconv.ParseInt(line[4:], 10, 64)
			case strings.HasPrefix(line, "event: "):
				cur.Event = line[7:]
			case strings.HasPrefix(line, "data: "):
				if err := json.Unmarshal([]byte(line[6:]), &cur.Data); err != nil {
					t.Errorf("bad data line %q: %v", line, err)
				}
			}
			mu.Unlock()
		}
		mu.Lock()
		ended = true
		mu.Unlock()
	}()
	select {
	case <-done:
	case <-time.After(deadline):
		t.Log("readSSE: deadline reached")
	}
	mu.Lock()
	defer mu.Unlock()
	return append([]sseEvent(nil), events...), comments, ended
}

func TestSSEReplayAndLive(t *testing.T) {
	e := newEnv(t, nil)
	c := e.client()
	resp, body, _ := e.upload(c, 10, nil)
	if resp.StatusCode != 201 {
		t.Fatalf("create: %d %s", resp.StatusCode, body)
	}
	j := decodeJob(t, body)
	ctx := context.Background()
	for _, tr := range []struct {
		status string
		prog   int16
	}{{"converting", 5}, {"analyzing", 20}} {
		if err := jobs.Transition(ctx, e.pool, j.ID, tr.status, tr.prog, tr.status+"..."); err != nil {
			t.Fatal(err)
		}
	}
	all, err := jobs.NewStore(e.pool).Events(ctx, j.ID, 0)
	if err != nil || len(all) != 3 {
		t.Fatalf("events: %v %d", err, len(all))
	}

	// Replay everything after the pending event, then receive a live one.
	req, _ := http.NewRequest("GET", e.ts.URL+"/api/v1/jobs/"+j.ID+"/events", nil)
	req.Header.Set("Last-Event-ID", strconv.FormatInt(all[0].ID, 10))
	stream, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Body.Close()
	if stream.StatusCode != 200 || !strings.HasPrefix(stream.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("stream: %d %v", stream.StatusCode, stream.Header)
	}
	replay, _, _ := readSSE(t, stream.Body, 2, 5*time.Second)
	if len(replay) != 2 || replay[0].ID != all[1].ID || replay[0].Data.Status != "converting" || replay[0].Data.Progress != 5 ||
		replay[1].ID != all[2].ID || replay[1].Data.Status != "analyzing" || replay[1].Event != "status" || replay[1].Data.At == "" {
		t.Fatalf("replay: %+v", replay)
	}

	// Live: a transition after the stream is open must arrive via NOTIFY,
	// and the terminal status must end the stream.
	go func() {
		time.Sleep(200 * time.Millisecond)
		_ = jobs.Transition(ctx, e.pool, j.ID, "separating", 50, "demucs")
		time.Sleep(100 * time.Millisecond)
		_ = jobs.Transition(ctx, e.pool, j.ID, "completed", 100, "done")
	}()
	live, _, ended := readSSE(t, stream.Body, 10, 10*time.Second)
	if len(live) != 2 || live[0].Data.Status != "separating" || live[1].Data.Status != "completed" || live[1].Data.Message != "done" {
		t.Fatalf("live: %+v", live)
	}
	if !ended {
		t.Error("stream did not end after terminal status")
	}

	// A fresh connection with no Last-Event-ID replays the full history and ends.
	stream2, err := http.Get(e.ts.URL + "/api/v1/jobs/" + j.ID + "/events")
	if err != nil {
		t.Fatal(err)
	}
	defer stream2.Body.Close()
	hist, _, ended := readSSE(t, stream2.Body, 10, 5*time.Second)
	if len(hist) != 5 || hist[0].Data.Status != "pending" || hist[4].Data.Status != "completed" || !ended {
		t.Errorf("history: %d events ended=%v", len(hist), ended)
	}
	// Client that already has the terminal event gets an empty, closed stream.
	req3, _ := http.NewRequest("GET", e.ts.URL+"/api/v1/jobs/"+j.ID+"/events", nil)
	req3.Header.Set("Last-Event-ID", strconv.FormatInt(hist[4].ID, 10))
	stream3, err := http.DefaultClient.Do(req3)
	if err != nil {
		t.Fatal(err)
	}
	defer stream3.Body.Close()
	none, _, ended := readSSE(t, stream3.Body, 10, 5*time.Second)
	if len(none) != 0 || !ended {
		t.Errorf("caught-up client: %d events ended=%v", len(none), ended)
	}
	// Access rules apply.
	e.createUser("owner@example.com", auth.StatusActive)
	owner := e.client()
	e.login(owner, "owner@example.com")
	resp, _ = e.do(owner, "POST", "/api/v1/jobs/"+j.ID+"/claim", map[string]string{"claim_token": j.ClaimToken}, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("claim: %d", resp.StatusCode)
	}
	resp, _ = e.do(e.client(), "GET", "/api/v1/jobs/"+j.ID+"/events", nil, nil)
	if resp.StatusCode != 401 {
		t.Errorf("events of owned job anonymously: %d", resp.StatusCode)
	}
}

func TestSSEHeartbeat(t *testing.T) {
	old := jobs.HeartbeatInterval
	jobs.HeartbeatInterval = 100 * time.Millisecond
	t.Cleanup(func() { jobs.HeartbeatInterval = old })
	e := newEnv(t, nil)
	resp, body, _ := e.upload(e.client(), 10, nil)
	if resp.StatusCode != 201 {
		t.Fatalf("create: %d %s", resp.StatusCode, body)
	}
	j := decodeJob(t, body)
	stream, err := http.Get(e.ts.URL + "/api/v1/jobs/" + j.ID + "/events")
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Body.Close()
	_, comments, _ := readSSE(t, stream.Body, 100, 600*time.Millisecond)
	if comments < 2 {
		t.Errorf("got %d heartbeat comments in 600ms, want >= 2", comments)
	}
}

func TestGoogleSignInDisabled(t *testing.T) {
	e := newEnv(t, nil)
	c := e.client()
	resp, body := e.do(c, "GET", "/api/v1/config", nil, nil)
	var cfg map[string]any
	if err := json.Unmarshal(body, &cfg); err != nil || resp.StatusCode != 200 {
		t.Fatalf("config %d %s", resp.StatusCode, body)
	}
	if cfg["google_sign_in"] != false || cfg["google_client_id"] != "" {
		t.Errorf("config: %v", cfg)
	}
	resp, body = e.do(c, "POST", "/api/v1/auth/google", map[string]string{"credential": "x"}, nil)
	if resp.StatusCode != 501 || errCode(body) != "google_not_configured" {
		t.Errorf("google disabled: %d %s", resp.StatusCode, body)
	}
}

func TestGoogleSignIn(t *testing.T) {
	const clientID = "rk-test.apps.googleusercontent.com"
	iss := googleidtest.New(t)
	e := newEnv(t, func(c *config.Config) {
		c.GoogleClientID = clientID
		c.GoogleJWKSURL = iss.URL()
	})
	store := auth.NewStore(e.pool)
	ctx := context.Background()
	token := func(email, sub string, mutate func(map[string]any)) string {
		cl := googleidtest.Claims(clientID, email)
		cl["sub"] = sub
		if mutate != nil {
			mutate(cl)
		}
		return iss.Sign(t, "kid-1", cl)
	}
	post := func(c *http.Client, tok string, headers map[string]string) (*http.Response, []byte) {
		return e.do(c, "POST", "/api/v1/auth/google", map[string]string{"credential": tok}, headers)
	}
	hasSession := func(resp *http.Response) bool {
		for _, ck := range resp.Cookies() {
			if ck.Name == auth.CookieName && ck.Value != "" {
				return true
			}
		}
		return false
	}

	t.Run("bad request bodies", func(t *testing.T) {
		c := e.client()
		resp, body := e.do(c, "POST", "/api/v1/auth/google", map[string]string{"id_token": "x"}, nil)
		if resp.StatusCode != 400 || errCode(body) != "invalid_json" {
			t.Errorf("legacy field: %d %s", resp.StatusCode, body)
		}
		resp, body = e.do(c, "POST", "/api/v1/auth/google", map[string]string{"credential": ""}, nil)
		if resp.StatusCode != 400 || errCode(body) != "invalid_json" {
			t.Errorf("empty credential: %d %s", resp.StatusCode, body)
		}
	})

	t.Run("token rejections", func(t *testing.T) {
		c := e.client()
		cases := map[string]struct {
			tok    string
			status int
			code   string
		}{
			"expired":     {token("x@example.com", "s1", func(m map[string]any) { m["exp"] = time.Now().Add(-time.Hour).Unix() }), 401, "invalid_google_token"},
			"wrong aud":   {iss.Sign(t, "kid-1", googleidtest.Claims("other", "x@example.com")), 401, "invalid_google_token"},
			"wrong iss":   {token("x@example.com", "s1", func(m map[string]any) { m["iss"] = "https://example.com" }), 401, "invalid_google_token"},
			"foreign key": {googleidtest.SignWith(t, googleidtest.ForeignKey(t), "kid-1", "RS256", googleidtest.Claims(clientID, "x@example.com")), 401, "invalid_google_token"},
			"unverified":  {token("x@example.com", "s1", func(m map[string]any) { m["email_verified"] = false }), 403, "email_not_verified"},
			"no email":    {token("", "s1", nil), 401, "invalid_google_token"},
		}
		for name, tc := range cases {
			resp, body := post(c, tc.tok, nil)
			if resp.StatusCode != tc.status || errCode(body) != tc.code {
				t.Errorf("%s: %d %s", name, resp.StatusCode, body)
			}
			if hasSession(resp) {
				t.Errorf("%s: session issued", name)
			}
		}
		if _, err := store.UserByEmail(ctx, "x@example.com"); !errors.Is(err, auth.ErrNotFound) {
			t.Errorf("rejected token created a user: %v", err)
		}
	})

	t.Run("new user is created pending", func(t *testing.T) {
		c := e.client()
		resp, body := post(c, token("New.Person@Example.com", "sub-new", nil), nil)
		if resp.StatusCode != 403 || errCode(body) != "pending_approval" {
			t.Fatalf("first sign-in: %d %s", resp.StatusCode, body)
		}
		if hasSession(resp) {
			t.Error("pending user got a session")
		}
		var denied struct {
			Code string     `json:"code"`
			User *auth.User `json:"user"`
		}
		if err := json.Unmarshal(body, &denied); err != nil || denied.User == nil {
			t.Fatalf("body: %s", body)
		}
		if denied.User.Email != "new.person@example.com" || denied.User.Status != auth.StatusPending ||
			denied.User.Provider != auth.ProviderGoogle || denied.User.Role != auth.RoleUser ||
			denied.User.Name != "Test User" || denied.User.AvatarURL == nil {
			t.Errorf("user summary: %+v", denied.User)
		}
		u, err := store.UserByEmail(ctx, "new.person@example.com")
		if err != nil {
			t.Fatal(err)
		}
		if u.GoogleSub == nil || *u.GoogleSub != "sub-new" || u.PasswordHash != nil {
			t.Errorf("stored user: sub=%v hash=%v", u.GoogleSub, u.PasswordHash)
		}
		// Second attempt while still pending: same answer, no duplicate.
		resp, body = post(c, token("new.person@example.com", "sub-new", nil), nil)
		if resp.StatusCode != 403 || errCode(body) != "pending_approval" {
			t.Fatalf("second sign-in: %d %s", resp.StatusCode, body)
		}
		if _, total, _ := store.ListUsers(ctx, auth.ListUsersParams{Query: "new.person"}); total != 1 {
			t.Errorf("users named new.person: %d", total)
		}
		resp, _ = e.do(c, "GET", "/api/v1/auth/me", nil, nil)
		if resp.StatusCode != 401 {
			t.Errorf("me while pending: %d", resp.StatusCode)
		}

		// Admin approves; the next Google sign-in issues a session.
		if _, err := store.SetUserStatus(ctx, u.ID, auth.StatusActive); err != nil {
			t.Fatal(err)
		}
		resp, body = post(c, token("new.person@example.com", "sub-new", nil), map[string]string{"X-Forwarded-Proto": "https"})
		if resp.StatusCode != 200 || !hasSession(resp) {
			t.Fatalf("approved sign-in: %d %s", resp.StatusCode, body)
		}
		for _, ck := range resp.Cookies() {
			if ck.Name == auth.CookieName && (!ck.Secure || !ck.HttpOnly) {
				t.Errorf("cookie flags: %+v", ck)
			}
		}
		resp, body = e.do(c, "GET", "/api/v1/auth/me", nil, nil)
		if resp.StatusCode != 200 || !strings.Contains(string(body), "new.person@example.com") {
			t.Errorf("me: %d %s", resp.StatusCode, body)
		}
	})

	t.Run("existing active password user links and signs in", func(t *testing.T) {
		e.createUser("active@example.com", auth.StatusActive)
		c := e.client()
		resp, body := post(c, token("Active@Example.com", "sub-active", func(m map[string]any) {
			m["name"] = "Google Name"
			m["picture"] = "https://lh3.googleusercontent.com/new"
		}), nil)
		if resp.StatusCode != 200 || !hasSession(resp) {
			t.Fatalf("sign-in: %d %s", resp.StatusCode, body)
		}
		u, err := store.UserByEmail(ctx, "active@example.com")
		if err != nil {
			t.Fatal(err)
		}
		// Provider and password stay; sub and avatar are recorded; the
		// existing name is not overwritten.
		if u.Provider != auth.ProviderPassword || u.PasswordHash == nil || u.GoogleSub == nil || *u.GoogleSub != "sub-active" ||
			u.AvatarURL == nil || *u.AvatarURL != "https://lh3.googleusercontent.com/new" || u.Name != "Test" {
			t.Errorf("linked user: %+v sub=%v avatar=%v", u, u.GoogleSub, u.AvatarURL)
		}
		// Password login keeps working after linking.
		e.login(e.client(), "active@example.com")

		// A later token for the same sub with a changed Google email still
		// maps to this account.
		resp, body = post(e.client(), token("renamed@example.com", "sub-active", nil), nil)
		if resp.StatusCode != 200 || !strings.Contains(string(body), `"email":"active@example.com"`) {
			t.Errorf("sub match after email change: %d %s", resp.StatusCode, body)
		}
		if _, err := store.UserByEmail(ctx, "renamed@example.com"); !errors.Is(err, auth.ErrNotFound) {
			t.Errorf("email change created a second account: %v", err)
		}
	})

	t.Run("inactive user is refused", func(t *testing.T) {
		e.createUser("inactive@example.com", auth.StatusInactive)
		c := e.client()
		resp, body := post(c, token("inactive@example.com", "sub-inactive", nil), nil)
		if resp.StatusCode != 403 || errCode(body) != "account_inactive" || hasSession(resp) {
			t.Fatalf("inactive: %d %s", resp.StatusCode, body)
		}
	})

	t.Run("existing pending password user stays pending", func(t *testing.T) {
		e.createUser("pending@example.com", auth.StatusPending)
		c := e.client()
		resp, body := post(c, token("pending@example.com", "sub-pending", nil), nil)
		if resp.StatusCode != 403 || errCode(body) != "pending_approval" || hasSession(resp) {
			t.Fatalf("pending: %d %s", resp.StatusCode, body)
		}
		if !strings.Contains(string(body), `"user":{`) {
			t.Errorf("no user summary: %s", body)
		}
	})
}

// --- drum editor: access rules on an owned transcribe job ---

func TestDrumEditsAccess(t *testing.T) {
	e := newEnv(t, func(c *config.Config) {
		c.TranscribeEmails = []string{"owner@example.com"}
		c.InternalEmails = []string{"owner@example.com"}
	})
	e.createUser("owner@example.com", auth.StatusActive)
	e.createUser("other@example.com", auth.StatusActive)
	owner, other, anon := e.client(), e.client(), e.client()
	e.login(owner, "owner@example.com")
	e.login(other, "other@example.com")
	resp, body, _ := e.upload(owner, 10, map[string]string{"transcribe": "1", "quality": "high6"})
	if resp.StatusCode != 201 {
		t.Fatalf("create: %d %s", resp.StatusCode, body)
	}
	j := decodeJob(t, body)
	ctx := context.Background()
	for _, st := range []string{"converting", "completed"} {
		if err := jobs.Transition(ctx, e.pool, j.ID, st, 0, st); err != nil {
			t.Fatal(err)
		}
	}
	if err := jobs.NewStore(e.pool).SetAudioInfo(ctx, j.ID, jobs.AudioInfo{DurationSeconds: 4, SampleRate: 48000, Channels: 2}); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(e.dataDir, "jobs", j.ID)
	if err := os.MkdirAll(filepath.Join(dir, "notes"), 0o755); err != nil {
		t.Fatal(err)
	}
	analysis := `{"version":1,"grid":null,"grid_error":"too few beats","sections":null,"instruments":{"drums":{"status":"ok","adapter":"adtof","model":"adtof_frame_rnn","notes":2}}}`
	notes := `{"stem":"drums","adapter":"adtof","model":"adtof_frame_rnn","notes":[{"onset":0.5,"offset":0.6,"pitch":36,"velocity":0.8},{"onset":1,"offset":1.1,"pitch":38,"velocity":0.9}]}`
	_ = os.WriteFile(filepath.Join(dir, "analysis.json"), []byte(analysis), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "notes", "drums.json"), []byte(notes), 0o644)
	base := "/api/v1/jobs/" + j.ID

	for _, tc := range []struct {
		name string
		c    *http.Client
		path string
		want int
		code string
	}{
		{"owner analysis", owner, "/analysis", 200, ""},
		{"owner notes", owner, "/notes/drums", 200, ""},
		{"owner edits", owner, "/drums/edits", 200, ""},
		{"other analysis", other, "/analysis", 404, "not_found"},
		{"other edits", other, "/drums/edits", 404, "not_found"},
		{"anon analysis", anon, "/analysis", 401, "unauthorized"},
		{"anon edits", anon, "/drums/edits", 401, "unauthorized"},
	} {
		resp, body := e.do(tc.c, "GET", base+tc.path, nil, nil)
		if resp.StatusCode != tc.want || (tc.code != "" && errCode(body) != tc.code) {
			t.Errorf("%s: %d %s", tc.name, resp.StatusCode, body)
		}
	}
	put := map[string]any{"base_rev": 0, "events": []map[string]any{{"id": "u1", "art": "kick", "t": 0.25, "vel": 0.5, "src": "manual"}}}
	if resp, body := e.do(other, "PUT", base+"/drums/edits", put, nil); resp.StatusCode != 404 {
		t.Errorf("other put: %d %s", resp.StatusCode, body)
	}
	if resp, body := e.do(anon, "PUT", base+"/drums/edits", put, nil); resp.StatusCode != 401 {
		t.Errorf("anon put: %d %s", resp.StatusCode, body)
	}
	resp, body = e.do(owner, "PUT", base+"/drums/edits", put, nil)
	if resp.StatusCode != 200 || !strings.Contains(string(body), `"edit_rev":1`) {
		t.Fatalf("owner put: %d %s", resp.StatusCode, body)
	}
	resp, body = e.do(owner, "GET", base+"/drums/edits", nil, nil)
	if resp.StatusCode != 200 || !strings.Contains(string(body), `"edit_rev":1`) || !strings.Contains(string(body), `"grid":null`) {
		t.Fatalf("owner get after put: %d %s", resp.StatusCode, body)
	}
	// Deleting the job removes the edit revision with everything else.
	if resp, _ := e.do(owner, "DELETE", base, nil, nil); resp.StatusCode != 204 {
		t.Fatalf("delete: %d", resp.StatusCode)
	}
	if _, err := os.Stat(filepath.Join(dir, "edits", "drums.json")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("edits survived delete: %v", err)
	}
}
