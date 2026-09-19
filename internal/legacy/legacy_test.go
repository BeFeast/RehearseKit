package legacy

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"io"
	"log/slog"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BeFeast/RehearseKit/internal/auth"
	"github.com/BeFeast/RehearseKit/internal/db/dbtest"
	"github.com/BeFeast/RehearseKit/internal/jobs"
	"github.com/BeFeast/RehearseKit/internal/pipeline/peaks"
	"github.com/BeFeast/RehearseKit/internal/storage"
)

// legacySchema is the subset of backend/app/models/{user,job}.py the
// importer reads, as Alembic created it (enums are Postgres enum types).
const legacySchema = `
CREATE TYPE jobstatus AS ENUM ('PENDING','CONVERTING','ANALYZING','SEPARATING','FINALIZING','PACKAGING','COMPLETED','FAILED','CANCELLED');
CREATE TYPE inputtype AS ENUM ('upload','youtube');
CREATE TYPE qualitymode AS ENUM ('fast','high');
CREATE TABLE users (
    id              uuid PRIMARY KEY,
    email           varchar UNIQUE,
    hashed_password varchar,
    full_name       varchar,
    avatar_url      varchar,
    is_admin        boolean NOT NULL DEFAULT false,
    is_active       boolean NOT NULL DEFAULT true,
    oauth_provider  varchar,
    oauth_id        varchar,
    created_at      timestamptz NOT NULL DEFAULT now(),
    last_login_at   timestamptz
);
CREATE TABLE jobs (
    id                uuid PRIMARY KEY,
    status            jobstatus NOT NULL,
    user_id           uuid REFERENCES users (id) ON DELETE SET NULL,
    input_type        inputtype NOT NULL,
    input_url         varchar,
    project_name      varchar NOT NULL,
    quality_mode      qualitymode NOT NULL,
    detected_bpm      double precision,
    manual_bpm        double precision,
    trim_start        double precision,
    trim_end          double precision,
    progress_percent  integer DEFAULT 0,
    error_message     text,
    source_file_path  varchar,
    stems_folder_path varchar,
    package_path      varchar,
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz,
    completed_at      timestamptz
);`

// legacyPool creates a throwaway schema shaped like the legacy database and
// returns a pool whose search_path points at it.
func legacyPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv(dbtest.EnvVar)
	if dsn == "" {
		t.Skipf("%s not set", dbtest.EnvVar)
	}
	ctx := context.Background()
	var buf [6]byte
	if _, err := rand.Read(buf[:]); err != nil {
		t.Fatal(err)
	}
	schema := "rk_legacy_" + hex.EncodeToString(buf[:])
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Close()
		_, _ = admin.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
		admin.Close()
	})
	if _, err := pool.Exec(ctx, legacySchema); err != nil {
		t.Fatalf("legacy schema: %v", err)
	}
	return pool
}

// writeWAV writes a 24-bit stereo 48 kHz PCM WAV of n frames (a sine).
func writeWAV(t *testing.T, path string, n int) {
	t.Helper()
	const ch, rate, bits = 2, 48000, 24
	var data bytes.Buffer
	for i := 0; i < n; i++ {
		for c := 0; c < ch; c++ {
			v := int32(math.Round(math.Sin(float64(i)/50) * 8388607 * 0.8))
			data.Write([]byte{byte(v), byte(v >> 8), byte(v >> 16)})
		}
	}
	var out bytes.Buffer
	out.WriteString("RIFF")
	_ = binary.Write(&out, binary.LittleEndian, uint32(4+8+16+8+data.Len()))
	out.WriteString("WAVEfmt ")
	_ = binary.Write(&out, binary.LittleEndian, uint32(16))
	_ = binary.Write(&out, binary.LittleEndian, uint16(1))
	_ = binary.Write(&out, binary.LittleEndian, uint16(ch))
	_ = binary.Write(&out, binary.LittleEndian, uint32(rate))
	_ = binary.Write(&out, binary.LittleEndian, uint32(rate*ch*bits/8))
	_ = binary.Write(&out, binary.LittleEndian, uint16(ch*bits/8))
	_ = binary.Write(&out, binary.LittleEndian, uint16(bits))
	out.WriteString("data")
	_ = binary.Write(&out, binary.LittleEndian, uint32(data.Len()))
	out.Write(data.Bytes())
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, out.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

var quietLog = slog.New(slog.NewTextHandler(io.Discard, nil))

const (
	uAdmin    = "3246d705-a051-4660-8125-a93b04a46175"
	uGoogle   = "753950b8-8e5b-4904-80db-836289d44dec"
	uInactive = "11111111-1111-4111-8111-111111111111"
	uNoEmail  = "22222222-2222-4222-8222-222222222222"

	jCompleted = "2901fb00-6910-4a1d-b9d6-1e3f38e0a47c"
	jYouTube   = "afede416-8e30-4139-b4dd-0036f34977c5"
	jNoStems   = "63d67992-6a41-46be-b397-259cbc1476a5"
	jInFlight  = "98d40c2e-c010-4cbf-8239-26e6b380bd11"
	jFailed    = "58a07f58-5b00-442d-b797-40c6a5ec07ba"
	jFlac      = "a05f6921-e14a-42c2-8dec-08caa04eb55c"
)

// seedLegacy fills the legacy schema and directory with the fixture set.
// It returns the number of jobs seeded.
func seedLegacy(t *testing.T, legacy *pgxpool.Pool, dir string, withFlac bool) int {
	t.Helper()
	ctx := context.Background()
	t0 := time.Date(2025, 10, 21, 19, 5, 0, 0, time.UTC)
	mustExec := func(sql string, args ...any) {
		t.Helper()
		if _, err := legacy.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	mustExec(`INSERT INTO users (id, email, hashed_password, full_name, is_admin, is_active, oauth_provider, created_at, last_login_at)
		VALUES ($1, 'Admin@Example.com', '$2b$12$legacy', 'Oleg Kossoy', true, true, NULL, $2, $3)`, uAdmin, t0, t0.Add(time.Hour))
	mustExec(`INSERT INTO users (id, email, full_name, avatar_url, is_admin, is_active, oauth_provider, oauth_id, created_at)
		VALUES ($1, 'g@example.com', 'G User', 'https://img.example/g.png', false, true, 'google', '123', $2)`, uGoogle, t0)
	mustExec(`INSERT INTO users (id, email, hashed_password, full_name, is_admin, is_active, created_at)
		VALUES ($1, 'sleepy@example.com', '$2b$12$legacy', 'Sleepy', false, false, $2)`, uInactive, t0)
	mustExec(`INSERT INTO users (id, email, full_name, created_at) VALUES ($1, NULL, 'Ghost', $2)`, uNoEmail, t0)

	job := func(id, status, userID, input, name string, created time.Time, extra map[string]any) {
		t.Helper()
		var uid *string
		if userID != "" {
			uid = &userID
		}
		var completed *time.Time
		if status == "COMPLETED" {
			c := created.Add(time.Minute)
			completed = &c
		}
		mustExec(`INSERT INTO jobs (id, status, user_id, input_type, input_url, project_name, quality_mode, detected_bpm,
			progress_percent, error_message, source_file_path, stems_folder_path, package_path, created_at, completed_at)
			VALUES ($1, $2::text::jobstatus, $3, $4::text::inputtype, $5, $6, 'high', $7, $8, $9, $10, $11, $12, $13, $14)`,
			id, status, uid, input, extra["input_url"], name, extra["bpm"], extra["progress"], extra["error"],
			extra["source"], "stems/"+id, id+".zip", created, completed)
	}
	job(jCompleted, "COMPLETED", uAdmin, "upload", "Kiko Loureiro - Enfermo", t0,
		map[string]any{"bpm": 128.5, "progress": 100, "source": "uploads/" + jCompleted + "_source.wav"})
	job(jYouTube, "COMPLETED", uGoogle, "youtube", "Queen - Bohemian Rhapsody", t0.Add(24*time.Hour),
		map[string]any{"progress": 100, "input_url": "https://www.youtube.com/watch?v=fJ9rUzIMcZQ"})
	job(jNoStems, "COMPLETED", uInactive, "upload", "Missing Other", t0.Add(48*time.Hour),
		map[string]any{"progress": 100, "source": "uploads/" + jNoStems + "_source.wav"})
	job(jInFlight, "SEPARATING", uNoEmail, "upload", "Interrupted", t0.Add(72*time.Hour),
		map[string]any{"progress": 55})
	job(jFailed, "FAILED", uAdmin, "upload", "Broken", t0.Add(96*time.Hour),
		map[string]any{"progress": 30, "error": "demucs crashed"})
	n := 5

	for _, id := range []string{jCompleted, jYouTube} {
		for i, name := range RequiredStems {
			writeWAV(t, filepath.Join(dir, "stems", id, name+".wav"), 1000+i*10)
		}
	}
	for _, name := range []string{"vocals", "drums", "bass"} {
		writeWAV(t, filepath.Join(dir, "stems", jNoStems, name+".wav"), 500)
	}
	writeWAV(t, filepath.Join(dir, "uploads", jCompleted+"_source.wav"), 2000)
	writeWAV(t, filepath.Join(dir, "uploads", jNoStems+"_source.wav"), 700)

	if withFlac {
		job(jFlac, "COMPLETED", uGoogle, "upload", "Flac Source", t0.Add(120*time.Hour),
			map[string]any{"progress": 100, "source": "uploads/" + jFlac + "_source.flac"})
		for _, name := range RequiredStems {
			writeWAV(t, filepath.Join(dir, "stems", jFlac, name+".wav"), 800)
		}
		wav := filepath.Join(dir, "tmp-src.wav")
		writeWAV(t, wav, 3000)
		flac := filepath.Join(dir, "uploads", jFlac+"_source.flac")
		if out, err := exec.Command("ffmpeg", "-v", "error", "-y", "-i", wav, flac).CombinedOutput(); err != nil {
			t.Fatalf("ffmpeg: %v: %s", err, out)
		}
		_ = os.Remove(wav)
		n++
	}
	return n
}

func TestImport(t *testing.T) {
	target := dbtest.Pool(t)
	legacy := legacyPool(t)
	ctx := context.Background()
	withFlac := ffmpegAvailable()
	legacyDir := t.TempDir()
	nJobs := seedLegacy(t, legacy, legacyDir, withFlac)

	// The v2 admin exists before the import with a different id and a
	// working password; the import must merge into it.
	admin, err := auth.NewStore(target).UpsertAdmin(ctx, "admin@example.com", "correct-horse-battery")
	if err != nil {
		t.Fatal(err)
	}
	layout, err := storage.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	opts := Options{LegacyDir: legacyDir, Retention: 7 * 24 * time.Hour, Log: quietLog}

	// Dry run: full report, nothing written.
	dry := opts
	dry.DryRun = true
	sum, err := Run(ctx, target, legacy, layout, dry)
	if err != nil {
		t.Fatal(err)
	}
	if !sum.DryRun || sum.Users.Imported != 2 || sum.Users.Merged != 1 || sum.Users.Skipped != 1 {
		t.Fatalf("dry-run users: %+v", sum.Users)
	}
	if sum.Jobs.Imported != nJobs || sum.Jobs.Errors != 0 || sum.Jobs.ImportedCompleted != nJobs-3 || sum.Jobs.ImportedFailed != 3 {
		t.Fatalf("dry-run jobs: %+v", sum.Jobs)
	}
	var count int
	if err := target.QueryRow(ctx, `SELECT count(*) FROM jobs`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("dry run wrote jobs: %d %v", count, err)
	}
	if err := target.QueryRow(ctx, `SELECT count(*) FROM users`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("dry run wrote users: %d %v", count, err)
	}
	if entries, _ := os.ReadDir(filepath.Join(layout.Root, "jobs")); len(entries) != 0 {
		t.Fatalf("dry run wrote files: %v", entries)
	}

	// Real run.
	sum, err = Run(ctx, target, legacy, layout, opts)
	if err != nil {
		t.Fatal(err)
	}
	if sum.Users.Imported != 2 || sum.Users.Merged != 1 || sum.Users.Skipped != 1 || sum.Users.Existing != 0 {
		t.Fatalf("users: %+v", sum.Users)
	}
	if sum.Jobs.Imported != nJobs || sum.Jobs.Errors != 0 || sum.Jobs.Existing != 0 {
		t.Fatalf("jobs: %+v %+v", sum.Jobs, sum.JobResults)
	}
	wantStems := 8
	wantPeaks := 8 + 2 // + source.pk of jCompleted and of jNoStems (its source is kept)
	if withFlac {
		wantStems += 4
		wantPeaks += 4 + 1
	}
	if sum.Stems != wantStems || sum.Peaks != wantPeaks {
		t.Fatalf("stems=%d peaks=%d, want %d/%d: %+v", sum.Stems, sum.Peaks, wantStems, wantPeaks, sum.JobResults)
	}
	if sum.BytesCopied <= 0 {
		t.Fatal("bytes_copied not counted")
	}

	// Users.
	users := auth.NewStore(target)
	a, err := users.UserByEmail(ctx, "admin@example.com")
	if err != nil || a.ID != admin.ID || a.PasswordHash == nil || a.Role != auth.RoleAdmin || a.Name != "Oleg Kossoy" {
		t.Fatalf("admin merge: %+v %v", a, err)
	}
	if a.LastLoginAt == nil {
		t.Fatal("admin merge: last_login_at not carried over")
	}
	g, err := users.UserByID(ctx, uGoogle)
	if err != nil || g.Provider != auth.ProviderGoogle || g.Role != auth.RoleUser || g.Status != auth.StatusActive ||
		g.PasswordHash != nil || g.AvatarURL == nil || *g.AvatarURL != "https://img.example/g.png" || g.Email != "g@example.com" {
		t.Fatalf("google user: %+v %v", g, err)
	}
	s, err := users.UserByID(ctx, uInactive)
	if err != nil || s.Provider != auth.ProviderPassword || s.Status != auth.StatusPending || s.PasswordHash != nil {
		t.Fatalf("inactive user: %+v %v", s, err)
	}
	if _, err := users.UserByID(ctx, uNoEmail); err == nil {
		t.Fatal("user without email must not be imported")
	}

	// Jobs.
	js := jobs.NewStore(target)
	j, err := js.Get(ctx, jCompleted)
	if err != nil {
		t.Fatal(err)
	}
	if j.Status != jobs.StatusCompleted || j.OwnerID == nil || *j.OwnerID != admin.ID || j.Quality != jobs.QualityHigh ||
		j.InputType != jobs.InputUpload || j.ProjectName != "Kiko Loureiro - Enfermo" || j.StageProgress != 100 || j.Error != nil {
		t.Fatalf("completed job: %+v", j)
	}
	if j.DetectedBPM == nil || *j.DetectedBPM != 128.5 {
		t.Fatalf("bpm: %v", j.DetectedBPM)
	}
	if !j.ExpiresAt.Equal(j.CreatedAt.Add(7*24*time.Hour)) || j.CompletedAt == nil {
		t.Fatalf("expires_at/completed_at: %v %v %v", j.CreatedAt, j.ExpiresAt, j.CompletedAt)
	}
	if j.SampleRate == nil || *j.SampleRate != 48000 || j.Channels == nil || *j.Channels != 2 || j.DurationSeconds == nil || *j.DurationSeconds != 0.021 {
		t.Fatalf("audio facts: %v %v %v", j.SampleRate, j.Channels, j.DurationSeconds)
	}
	if len(j.Stems) != 4 {
		t.Fatalf("stems: %+v", j.Stems)
	}
	for _, st := range j.Stems {
		want := int64(1000)
		switch st.Name {
		case "drums":
			want = 1010
		case "bass":
			want = 1020
		case "other":
			want = 1030
		}
		if st.Frames != want || st.SampleRate != 48000 || st.BitDepth != 24 || st.Channels != 2 || st.Bytes != 44+want*6 || st.PeaksURL == nil {
			t.Fatalf("stem %s: %+v", st.Name, st)
		}
		p, _ := layout.StemPath(jCompleted, st.Name)
		if fi, err := os.Stat(p); err != nil || fi.Size() != st.Bytes {
			t.Fatalf("stem file %s: %v", p, err)
		}
		pk, _ := layout.PeaksPath(jCompleted, st.Name)
		checkPeaks(t, pk, uint64(want))
	}
	dir, _ := layout.JobDir(jCompleted)
	if _, err := os.Stat(filepath.Join(dir, "source.wav")); err != nil {
		t.Fatalf("source.wav: %v", err)
	}
	checkPeaks(t, filepath.Join(dir, "peaks", "source.pk"), 2000)
	var path, peaksPath string
	if err := target.QueryRow(ctx, `SELECT path, peaks_path FROM stems WHERE job_id = $1 AND name = 'vocals'`, jCompleted).Scan(&path, &peaksPath); err != nil {
		t.Fatal(err)
	}
	if path != "stems/vocals.wav" || peaksPath != "peaks/vocals.pk" {
		t.Fatalf("stem paths: %q %q", path, peaksPath)
	}
	events, err := js.Events(ctx, jCompleted, 0)
	if err != nil || len(events) != 1 || events[0].Message != EventMessage || events[0].Status != jobs.StatusCompleted || events[0].Progress != 100 {
		t.Fatalf("events: %+v %v", events, err)
	}

	y, err := js.Get(ctx, jYouTube)
	if err != nil || y.Status != jobs.StatusCompleted || y.InputType != jobs.InputYouTube || y.InputURL == nil ||
		y.OwnerID == nil || *y.OwnerID != uGoogle || len(y.Stems) != 4 {
		t.Fatalf("youtube job: %+v %v", y, err)
	}
	ydir, _ := layout.JobDir(jYouTube)
	if _, err := os.Stat(filepath.Join(ydir, "peaks", "source.pk")); err == nil {
		t.Fatal("youtube job has no source; source.pk must not exist")
	}

	m, err := js.Get(ctx, jNoStems)
	if err != nil || m.Status != jobs.StatusFailed || m.Error == nil || *m.Error != ErrLegacyStemsMissing || len(m.Stems) != 0 {
		t.Fatalf("missing-stems job: %+v %v", m, err)
	}
	mdir, _ := layout.JobDir(jNoStems)
	if _, err := os.Stat(filepath.Join(mdir, "source.wav")); err != nil {
		t.Fatalf("failed job keeps its source: %v", err)
	}
	if _, err := os.Stat(filepath.Join(mdir, "stems")); err == nil {
		t.Fatal("failed job must not get partial stems")
	}

	f, err := js.Get(ctx, jInFlight)
	if err != nil || f.Status != jobs.StatusFailed || f.Error == nil || *f.Error != "legacy job was SEPARATING at import time" ||
		f.OwnerID != nil || f.StageProgress != 55 || f.CompletedAt == nil {
		t.Fatalf("in-flight job: %+v %v", f, err)
	}
	b, err := js.Get(ctx, jFailed)
	if err != nil || b.Status != jobs.StatusFailed || b.Error == nil || *b.Error != "demucs crashed" || b.StageProgress != 30 {
		t.Fatalf("failed job: %+v %v", b, err)
	}
	if withFlac {
		fl, err := js.Get(ctx, jFlac)
		if err != nil || fl.Status != jobs.StatusCompleted || len(fl.Stems) != 4 {
			t.Fatalf("flac job: %+v %v", fl, err)
		}
		fdir, _ := layout.JobDir(jFlac)
		if _, err := os.Stat(filepath.Join(fdir, "source.flac")); err != nil {
			t.Fatal(err)
		}
		checkPeaks(t, filepath.Join(fdir, "peaks", "source.pk"), 3000)
		if _, err := os.Stat(filepath.Join(fdir, "peaks", "source-decode.tmp.wav")); err == nil {
			t.Fatal("ffmpeg temp file left behind")
		}
	}

	// Second run: everything already there.
	again, err := Run(ctx, target, legacy, layout, opts)
	if err != nil {
		t.Fatal(err)
	}
	if again.Users.Imported != 0 || again.Users.Existing != 2 || again.Users.Merged != 1 || again.Users.Skipped != 1 {
		t.Fatalf("rerun users: %+v", again.Users)
	}
	if again.Jobs.Imported != 0 || again.Jobs.Existing != nJobs || again.Jobs.Errors != 0 || again.Stems != 0 || again.BytesCopied != 0 {
		t.Fatalf("rerun jobs: %+v", again.Jobs)
	}
	if err := target.QueryRow(ctx, `SELECT count(*) FROM job_events`).Scan(&count); err != nil || count != nJobs {
		t.Fatalf("rerun added events: %d %v", count, err)
	}
}

func TestImportHardlink(t *testing.T) {
	target := dbtest.Pool(t)
	legacy := legacyPool(t)
	ctx := context.Background()
	// Same temp root so the link can succeed.
	root := t.TempDir()
	legacyDir := filepath.Join(root, "legacy")
	seedLegacy(t, legacy, legacyDir, false)
	layout, err := storage.New(filepath.Join(root, "data"))
	if err != nil {
		t.Fatal(err)
	}
	sum, err := Run(ctx, target, legacy, layout, Options{LegacyDir: legacyDir, Retention: time.Hour, Hardlink: true, Log: quietLog})
	if err != nil {
		t.Fatal(err)
	}
	if sum.HardlinkFallbacks != 0 || sum.Stems != 8 {
		t.Fatalf("hardlink run: %+v", sum)
	}
	dst, _ := layout.StemPath(jCompleted, "vocals")
	a, err := os.Stat(dst)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.Stat(filepath.Join(legacyDir, "stems", jCompleted, "vocals.wav"))
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(a, b) {
		t.Fatal("stem was copied, not linked")
	}
}

func checkPeaks(t *testing.T, path string, frames uint64) {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("peaks %s: %v", path, err)
	}
	defer f.Close()
	pk, err := peaks.Read(f)
	if err != nil {
		t.Fatalf("peaks %s: %v", path, err)
	}
	if pk.Frames != frames || pk.Channels != 2 || pk.SampleRate != 48000 || len(pk.Stages) != len(peaks.DefaultShifts) {
		t.Fatalf("peaks %s: frames=%d channels=%d rate=%d stages=%d", path, pk.Frames, pk.Channels, pk.SampleRate, len(pk.Stages))
	}
}
