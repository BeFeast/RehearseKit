// Package legacy imports users and jobs from the FastAPI/Celery RehearseKit
// deployment (backend/app/models) into the rk schema and storage layout.
//
// The legacy database is only ever read (the connection forces
// default_transaction_read_only). Files are taken from the legacy storage
// root, whose layout was
//
//	stems/<job_id>/{vocals,drums,bass,other}.wav
//	uploads/<job_id>_source.<ext>
//	<job_id>.zip
//
// and land in the rk layout (see internal/storage). The importer is
// idempotent: users are matched by email (an existing rk account is merged,
// not duplicated) and jobs whose id already exists are skipped.
package legacy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BeFeast/RehearseKit/internal/auth"
	"github.com/BeFeast/RehearseKit/internal/jobs"
	"github.com/BeFeast/RehearseKit/internal/pipeline/peaks"
	"github.com/BeFeast/RehearseKit/internal/storage"
)

// EventMessage is written to job_events for every imported job.
const EventMessage = "imported from legacy"

// Options controls one import run.
type Options struct {
	// LegacyDir is the legacy storage root (stems/, uploads/).
	LegacyDir string
	// DryRun reports what would happen without writing files or rows.
	DryRun bool
	// Hardlink links stems and sources instead of copying; when the link
	// fails (different mount, read-only bind) the file is copied instead.
	Hardlink bool
	// Retention is added to created_at to get expires_at.
	Retention time.Duration
	// Log receives per-job progress; nil means slog.Default().
	Log *slog.Logger
}

// Connect opens a read-only pool to the legacy database.
func Connect(ctx context.Context, dsn string) (*pgxpool.Pool, error) {
	if dsn == "" {
		return nil, errors.New("legacy database URL is empty")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("legacy dsn: %w", err)
	}
	cfg.ConnConfig.RuntimeParams["default_transaction_read_only"] = "on"
	cfg.MaxConns = 2
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("legacy connect: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("legacy ping: %w", err)
	}
	return pool, nil
}

// UserResult is the outcome for one legacy user.
type UserResult struct {
	LegacyID string `json:"legacy_id"`
	Email    string `json:"email,omitempty"`
	// Action is imported, merged (matched an existing rk account by
	// email), existing (same id already present) or skipped.
	Action string `json:"action"`
	// ID is the rk user id the legacy id maps to (empty when skipped).
	ID     string `json:"id,omitempty"`
	Reason string `json:"reason,omitempty"`
}

// JobResult is the outcome for one legacy job.
type JobResult struct {
	ID string `json:"id"`
	// Action is imported, existing (id already present) or error.
	Action string `json:"action"`
	// Status is the rk status the job was (or would be) written with.
	Status string `json:"status,omitempty"`
	Error  string `json:"error,omitempty"`
	Stems  int    `json:"stems"`
	Source string `json:"source,omitempty"`
	// SourcePeaks reports whether peaks/source.pk was written. In a dry
	// run it is a prediction: the source parses as a supported WAV, or
	// ffmpeg is on PATH to decode it.
	SourcePeaks bool  `json:"source_peaks"`
	Bytes       int64 `json:"bytes"`
}

// Summary is the JSON report printed by `rk import-legacy`.
type Summary struct {
	DryRun bool `json:"dry_run"`
	Users  struct {
		Imported int `json:"imported"`
		Merged   int `json:"merged"`
		Existing int `json:"existing"`
		Skipped  int `json:"skipped"`
	} `json:"users"`
	Jobs struct {
		Imported          int `json:"imported"`
		ImportedCompleted int `json:"imported_completed"`
		ImportedFailed    int `json:"imported_failed"`
		Existing          int `json:"existing"`
		Errors            int `json:"errors"`
	} `json:"jobs"`
	Stems             int          `json:"stems"`
	Peaks             int          `json:"peaks"`
	BytesCopied       int64        `json:"bytes_copied"`
	HardlinkFallbacks int          `json:"hardlink_fallbacks"`
	UserResults       []UserResult `json:"user_results"`
	JobResults        []JobResult  `json:"job_results"`
}

type legacyUser struct {
	ID            string
	Email         *string
	FullName      *string
	AvatarURL     *string
	IsAdmin       bool
	IsActive      bool
	OAuthProvider *string
	CreatedAt     time.Time
	LastLoginAt   *time.Time
}

type legacyJob struct {
	ID             string
	Status         string
	InputType      string
	InputURL       *string
	ProjectName    string
	Quality        string
	DetectedBPM    *float64
	Progress       *int32
	ErrorMessage   *string
	SourceFilePath *string
	UserID         *string
	CreatedAt      time.Time
	CompletedAt    *time.Time
}

type stemFile struct {
	Name string
	Src  string
	jobs.Stem
}

// Importer runs one import.
type Importer struct {
	target *pgxpool.Pool
	legacy *pgxpool.Pool
	layout storage.Layout
	opts   Options
	log    *slog.Logger
	// userIDs maps legacy user ids to rk user ids.
	userIDs map[string]string
	sum     *Summary
}

// Run imports everything from legacy into target and returns the summary.
// Per-job failures are recorded in the summary and do not abort the run;
// only setup errors (queries against either database) are returned.
func Run(ctx context.Context, target, legacy *pgxpool.Pool, layout storage.Layout, opts Options) (*Summary, error) {
	if opts.LegacyDir == "" {
		return nil, errors.New("legacy dir is required")
	}
	abs, err := filepath.Abs(opts.LegacyDir)
	if err != nil {
		return nil, err
	}
	if st, err := os.Stat(abs); err != nil || !st.IsDir() {
		return nil, fmt.Errorf("legacy dir %s is not a directory", abs)
	}
	opts.LegacyDir = abs
	if opts.Retention <= 0 {
		return nil, errors.New("retention must be positive")
	}
	if opts.Log == nil {
		opts.Log = slog.Default()
	}
	im := &Importer{
		target:  target,
		legacy:  legacy,
		layout:  layout,
		opts:    opts,
		log:     opts.Log,
		userIDs: map[string]string{},
		sum:     &Summary{DryRun: opts.DryRun},
	}
	im.sum.UserResults = []UserResult{}
	im.sum.JobResults = []JobResult{}
	if err := im.importUsers(ctx); err != nil {
		return nil, err
	}
	if err := im.importJobs(ctx); err != nil {
		return nil, err
	}
	return im.sum, nil
}

// --- users -----------------------------------------------------------------

func (im *Importer) importUsers(ctx context.Context) error {
	rows, err := im.legacy.Query(ctx, `SELECT id::text, email, full_name, avatar_url, is_admin, is_active,
		oauth_provider, created_at, last_login_at FROM users ORDER BY created_at, id`)
	if err != nil {
		return fmt.Errorf("legacy users: %w", err)
	}
	var users []legacyUser
	for rows.Next() {
		var u legacyUser
		if err := rows.Scan(&u.ID, &u.Email, &u.FullName, &u.AvatarURL, &u.IsAdmin, &u.IsActive,
			&u.OAuthProvider, &u.CreatedAt, &u.LastLoginAt); err != nil {
			rows.Close()
			return fmt.Errorf("legacy users: %w", err)
		}
		users = append(users, u)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("legacy users: %w", err)
	}
	for _, u := range users {
		res, err := im.importUser(ctx, u)
		if err != nil {
			return fmt.Errorf("user %s: %w", u.ID, err)
		}
		im.sum.UserResults = append(im.sum.UserResults, res)
		switch res.Action {
		case "imported":
			im.sum.Users.Imported++
		case "merged":
			im.sum.Users.Merged++
		case "existing":
			im.sum.Users.Existing++
		default:
			im.sum.Users.Skipped++
		}
		if res.ID != "" {
			im.userIDs[u.ID] = res.ID
		}
		im.log.Info("user", "legacy_id", u.ID, "email", res.Email, "action", res.Action, "reason", res.Reason)
	}
	return nil
}

func (im *Importer) importUser(ctx context.Context, u legacyUser) (UserResult, error) {
	res := UserResult{LegacyID: u.ID}
	if u.Email == nil {
		res.Action, res.Reason = "skipped", "no email"
		return res, nil
	}
	email := auth.NormalizeEmail(*u.Email)
	res.Email = email
	if email == "" || !strings.Contains(email, "@") {
		res.Action, res.Reason = "skipped", "invalid email"
		return res, nil
	}
	name := ""
	if u.FullName != nil {
		name = strings.TrimSpace(*u.FullName)
	}
	avatar := u.AvatarURL
	if avatar != nil && strings.TrimSpace(*avatar) == "" {
		avatar = nil
	}

	// Same id already there (a previous run): nothing to do.
	var existingID string
	err := im.target.QueryRow(ctx, `SELECT id::text FROM users WHERE id = $1`, u.ID).Scan(&existingID)
	if err == nil {
		res.Action, res.ID = "existing", existingID
		return res, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return res, err
	}

	// Same email under another id (e.g. the admin created by `rk
	// create-admin`): merge into that account, keep its credentials.
	err = im.target.QueryRow(ctx, `SELECT id::text FROM users WHERE email = $1`, email).Scan(&existingID)
	if err == nil {
		res.Action, res.ID = "merged", existingID
		res.Reason = "matched existing account by email; credentials, role and status kept"
		if im.opts.DryRun {
			return res, nil
		}
		_, err = im.target.Exec(ctx, `UPDATE users SET
			name = CASE WHEN name = '' THEN $2 ELSE name END,
			avatar_url = COALESCE(NULLIF(avatar_url, ''), $3),
			last_login_at = GREATEST(last_login_at, $4),
			created_at = LEAST(created_at, $5)
			WHERE id = $1`, existingID, name, avatar, u.LastLoginAt, u.CreatedAt)
		return res, err
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return res, err
	}

	res.Action, res.ID = "imported", u.ID
	provider := MapProvider(u.OAuthProvider)
	if provider == auth.ProviderPassword {
		res.Reason = "password not migrated (legacy bcrypt); needs reset"
	}
	if im.opts.DryRun {
		return res, nil
	}
	_, err = im.target.Exec(ctx, `INSERT INTO users
		(id, email, name, avatar_url, provider, password_hash, role, status, created_at, last_login_at)
		VALUES ($1, $2, $3, $4, $5, NULL, $6, $7, $8, $9)`,
		u.ID, email, name, avatar, provider, MapUserRole(u.IsAdmin), MapUserStatus(u.IsActive), u.CreatedAt, u.LastLoginAt)
	return res, err
}

// --- jobs ------------------------------------------------------------------

func (im *Importer) importJobs(ctx context.Context) error {
	rows, err := im.legacy.Query(ctx, `SELECT id::text, status::text, input_type::text, input_url, project_name,
		quality_mode::text, detected_bpm, progress_percent, error_message, source_file_path, user_id::text,
		created_at, completed_at FROM jobs ORDER BY created_at, id`)
	if err != nil {
		return fmt.Errorf("legacy jobs: %w", err)
	}
	var list []legacyJob
	for rows.Next() {
		var j legacyJob
		if err := rows.Scan(&j.ID, &j.Status, &j.InputType, &j.InputURL, &j.ProjectName, &j.Quality,
			&j.DetectedBPM, &j.Progress, &j.ErrorMessage, &j.SourceFilePath, &j.UserID,
			&j.CreatedAt, &j.CompletedAt); err != nil {
			rows.Close()
			return fmt.Errorf("legacy jobs: %w", err)
		}
		list = append(list, j)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("legacy jobs: %w", err)
	}
	for _, j := range list {
		if err := ctx.Err(); err != nil {
			return err
		}
		res := im.importJob(ctx, j)
		im.sum.JobResults = append(im.sum.JobResults, res)
		switch res.Action {
		case "imported":
			im.sum.Jobs.Imported++
			if res.Status == jobs.StatusCompleted {
				im.sum.Jobs.ImportedCompleted++
			} else if res.Status == jobs.StatusFailed {
				im.sum.Jobs.ImportedFailed++
			}
			im.sum.Stems += res.Stems
			im.sum.Peaks += res.Stems
			if res.SourcePeaks {
				im.sum.Peaks++
			}
			im.sum.BytesCopied += res.Bytes
			im.log.Info("job", "id", j.ID, "action", res.Action, "status", res.Status, "stems", res.Stems,
				"source", res.Source, "bytes", res.Bytes, "error", res.Error)
		case "existing":
			im.sum.Jobs.Existing++
			im.log.Info("job", "id", j.ID, "action", res.Action)
		default:
			im.sum.Jobs.Errors++
			im.log.Error("job", "id", j.ID, "action", res.Action, "error", res.Error)
		}
	}
	return nil
}

func (im *Importer) importJob(ctx context.Context, j legacyJob) JobResult {
	res := JobResult{ID: j.ID}
	fail := func(err error) JobResult {
		res.Action, res.Error = "error", err.Error()
		return res
	}
	if !storage.ValidJobID(j.ID) {
		return fail(fmt.Errorf("invalid job id %q", j.ID))
	}
	var exists bool
	if err := im.target.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM jobs WHERE id = $1)`, j.ID).Scan(&exists); err != nil {
		return fail(err)
	}
	if exists {
		res.Action = "existing"
		return res
	}

	status, errText, err := MapStatus(j.Status)
	if err != nil {
		return fail(err)
	}
	quality, err := MapQuality(j.Quality)
	if err != nil {
		return fail(err)
	}
	inputType, err := MapInputType(j.InputType)
	if err != nil {
		return fail(err)
	}
	if errText == "" && j.ErrorMessage != nil && strings.TrimSpace(*j.ErrorMessage) != "" {
		errText = strings.TrimSpace(*j.ErrorMessage)
	}

	// Stems: a completed job must have all four on disk.
	var stems []stemFile
	if status == jobs.StatusCompleted {
		stems, err = im.legacyStems(j.ID)
		if err != nil {
			status, errText, stems = jobs.StatusFailed, err.Error(), nil
		}
	}
	src, srcExt := im.legacySource(j)
	if src != "" {
		res.Source = "source." + srcExt
	}
	res.Status, res.Error, res.Stems = status, errText, len(stems)
	for _, s := range stems {
		res.Bytes += s.Bytes
	}
	if src != "" {
		if st, err := os.Stat(src); err == nil {
			res.Bytes += st.Size()
		}
	}
	res.SourcePeaks = src != "" && sourcePeaksPossible(src)
	res.Action = "imported"
	if im.opts.DryRun {
		return res
	}

	// Files first, then one transaction; on any failure the job dir is
	// removed so a re-run starts clean.
	jobDir, err := im.layout.JobDir(j.ID)
	if err != nil {
		return fail(err)
	}
	cleanup := func(err error) JobResult {
		_ = os.RemoveAll(jobDir)
		return fail(err)
	}
	if err := os.MkdirAll(filepath.Join(jobDir, "peaks"), 0o755); err != nil {
		return cleanup(err)
	}
	var stemRows []struct {
		jobs.Stem
		Path, Peaks string
	}
	for _, s := range stems {
		dst, err := im.layout.StemPath(j.ID, s.Name)
		if err != nil {
			return cleanup(err)
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return cleanup(err)
		}
		if err := im.place(s.Src, dst); err != nil {
			return cleanup(fmt.Errorf("stem %s: %w", s.Name, err))
		}
		pk, err := im.layout.PeaksPath(j.ID, s.Name)
		if err != nil {
			return cleanup(err)
		}
		if err := writePeaks(dst, pk); err != nil {
			return cleanup(fmt.Errorf("peaks %s: %w", s.Name, err))
		}
		stemRows = append(stemRows, struct {
			jobs.Stem
			Path, Peaks string
		}{s.Stem, "stems/" + s.Name + ".wav", "peaks/" + s.Name + ".pk"})
	}
	res.SourcePeaks = false
	if src != "" {
		dst := filepath.Join(jobDir, "source."+srcExt)
		if err := im.place(src, dst); err != nil {
			return cleanup(fmt.Errorf("source: %w", err))
		}
		pk, err := im.layout.PeaksPath(j.ID, "source")
		if err != nil {
			return cleanup(err)
		}
		if err := writeSourcePeaks(ctx, dst, pk); err != nil {
			// Not fatal: the stems are what the player needs.
			im.log.Warn("source peaks skipped", "id", j.ID, "err", err)
		} else {
			res.SourcePeaks = true
		}
	}

	// Job-level audio facts come from the first stem (all four share them).
	var duration *float64
	var sampleRate *int32
	var channels *int16
	if len(stems) > 0 {
		s := stems[0]
		d := math.Round(float64(s.Frames)/float64(s.SampleRate)*1000) / 1000
		duration, sampleRate, channels = &d, &s.SampleRate, &s.Channels
	}
	var progress int16
	if status == jobs.StatusCompleted {
		progress = 100
	} else if j.Progress != nil {
		progress = int16(min(max(*j.Progress, 0), 100))
	}
	var owner *string
	if j.UserID != nil {
		if id, ok := im.userIDs[*j.UserID]; ok {
			owner = &id
		} else {
			im.log.Warn("job owner not imported; importing as anonymous", "id", j.ID, "legacy_user_id", *j.UserID)
		}
	}
	var inputURL *string
	if j.InputURL != nil && strings.TrimSpace(*j.InputURL) != "" {
		inputURL = j.InputURL
	}
	var errCol *string
	if errText != "" {
		errCol = &errText
	}
	completedAt := j.CompletedAt
	if status != jobs.StatusCompleted && completedAt == nil {
		completedAt = &j.CreatedAt
	}
	expiresAt := j.CreatedAt.Add(im.opts.Retention)

	tx, err := im.target.Begin(ctx)
	if err != nil {
		return cleanup(err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if _, err := tx.Exec(ctx, `INSERT INTO jobs
		(id, owner_id, project_name, input_type, input_url, quality, status, stage_progress, error,
		 detected_bpm, duration_seconds, sample_rate, channels, created_at, completed_at, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)`,
		j.ID, owner, clampText(strings.TrimSpace(j.ProjectName), 200), inputType, inputURL, quality, status, progress, errCol,
		j.DetectedBPM, duration, sampleRate, channels, j.CreatedAt, completedAt, expiresAt); err != nil {
		return cleanup(fmt.Errorf("insert job: %w", err))
	}
	for _, s := range stemRows {
		if _, err := tx.Exec(ctx, `INSERT INTO stems (job_id, name, path, bytes, frames, sample_rate, bit_depth, channels, peaks_path)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
			j.ID, s.Name, s.Path, s.Bytes, s.Frames, s.SampleRate, s.BitDepth, s.Channels, s.Peaks); err != nil {
			return cleanup(fmt.Errorf("insert stem %s: %w", s.Name, err))
		}
	}
	if _, err := tx.Exec(ctx, `INSERT INTO job_events (job_id, status, progress, message) VALUES ($1, $2, $3, $4)`,
		j.ID, status, progress, EventMessage); err != nil {
		return cleanup(fmt.Errorf("insert event: %w", err))
	}
	if err := tx.Commit(ctx); err != nil {
		return cleanup(err)
	}
	return res
}

// legacyStems locates and inspects the four stems of a legacy job.
func (im *Importer) legacyStems(id string) ([]stemFile, error) {
	dir := filepath.Join(im.opts.LegacyDir, "stems", id)
	var out []stemFile
	var missing []string
	for _, name := range RequiredStems {
		p := filepath.Join(dir, name+".wav")
		st, err := os.Stat(p)
		if err != nil || st.IsDir() {
			missing = append(missing, name)
			continue
		}
		hdr, err := wavHeader(p)
		if err != nil {
			return nil, fmt.Errorf("legacy stem unreadable: %s: %w", name, err)
		}
		hdr.Name = name
		hdr.Bytes = st.Size()
		out = append(out, stemFile{Name: name, Src: p, Stem: hdr})
	}
	if len(missing) > 0 {
		return nil, errors.New(ErrLegacyStemsMissing)
	}
	return out, nil
}

// legacySource finds uploads/<id>_source.<ext>, preferring the file named
// in source_file_path. It returns the path and the normalised extension, or
// "" when there is no usable source.
func (im *Importer) legacySource(j legacyJob) (string, string) {
	var candidates []string
	if j.SourceFilePath != nil && strings.TrimSpace(*j.SourceFilePath) != "" {
		candidates = append(candidates, filepath.Join(im.opts.LegacyDir, "uploads", filepath.Base(*j.SourceFilePath)))
	}
	matches, _ := filepath.Glob(filepath.Join(im.opts.LegacyDir, "uploads", j.ID+"_source.*"))
	sort.Strings(matches)
	candidates = append(candidates, matches...)
	for _, c := range candidates {
		st, err := os.Stat(c)
		if err != nil || st.IsDir() {
			continue
		}
		ext, ok := storage.SourceExt(c)
		if !ok {
			continue
		}
		return c, ext
	}
	return "", ""
}

// wavHeader reads the stems-table facts from a WAV file.
func wavHeader(path string) (jobs.Stem, error) {
	f, err := os.Open(path)
	if err != nil {
		return jobs.Stem{}, err
	}
	defer f.Close()
	dec, err := peaks.OpenWAV(f)
	if err != nil {
		return jobs.Stem{}, err
	}
	if dec.Frames > math.MaxInt64 {
		return jobs.Stem{}, errors.New("wav: frame count overflow")
	}
	return jobs.Stem{
		Frames:     int64(dec.Frames),
		SampleRate: int32(dec.SampleRate),
		BitDepth:   int16(dec.BitDepth),
		Channels:   int16(dec.Channels),
	}, nil
}

// place puts src at dst by hardlink (when requested and possible) or copy.
// An existing dst is replaced.
func (im *Importer) place(src, dst string) error {
	if im.opts.Hardlink {
		if err := os.Remove(dst); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		err := os.Link(src, dst)
		if err == nil {
			return nil
		}
		im.sum.HardlinkFallbacks++
		im.log.Warn("hardlink failed; copying", "src", src, "err", err)
	}
	return copyFile(src, dst)
}

// copyFile copies src to dst via a temp file and rename.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".tmp"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dst)
}

// writePeaks builds the default pyramid for wavPath and writes it to pkPath.
func writePeaks(wavPath, pkPath string) error {
	f, err := os.Open(wavPath)
	if err != nil {
		return err
	}
	defer f.Close()
	pk, err := peaks.Build(f, nil)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(pkPath), 0o755); err != nil {
		return err
	}
	tmp := pkPath + ".tmp"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if err := peaks.Write(out, pk); err != nil {
		out.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, pkPath)
}

// writeSourcePeaks handles a source that may not be a (supported) WAV: it
// tries the file directly, then decodes through ffmpeg into a temporary
// 16-bit WAV next to the source.
func writeSourcePeaks(ctx context.Context, srcPath, pkPath string) error {
	direct := writePeaks(srcPath, pkPath)
	if direct == nil {
		return nil
	}
	if !ffmpegAvailable() {
		return fmt.Errorf("%w (ffmpeg not available for decoding)", direct)
	}
	tmp := filepath.Join(filepath.Dir(pkPath), "source-decode.tmp.wav")
	defer os.Remove(tmp)
	cmd := exec.CommandContext(ctx, "ffmpeg", "-v", "error", "-y", "-i", srcPath, "-vn", "-f", "wav", "-c:a", "pcm_s16le", tmp)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("ffmpeg: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return writePeaks(tmp, pkPath)
}

// sourcePeaksPossible predicts whether writeSourcePeaks can succeed for
// src without decoding it: the header parses as a supported WAV, or ffmpeg
// is available to transcode it.
func sourcePeaksPossible(src string) bool {
	if _, err := wavHeader(src); err == nil {
		return true
	}
	return ffmpegAvailable()
}

func ffmpegAvailable() bool {
	_, err := exec.LookPath("ffmpeg")
	return err == nil
}

// clampText truncates s to maxBytes on a rune boundary.
func clampText(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	cut := maxBytes
	for cut > 0 && !isRuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

func isRuneStart(b byte) bool { return b&0xC0 != 0x80 }
