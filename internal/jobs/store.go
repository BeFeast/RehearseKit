package jobs

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Errors returned by Store.
var (
	ErrNotFound      = errors.New("jobs: not found")
	ErrNoJobs        = errors.New("jobs: queue is empty")
	ErrTerminal      = errors.New("jobs: job already finished")
	ErrAlreadyOwned  = errors.New("jobs: job already has an owner")
	ErrBadClaimToken = errors.New("jobs: claim token does not match")
	ErrInvalidStatus = errors.New("jobs: invalid status")
)

// Store runs the job queries.
type Store struct {
	pool *pgxpool.Pool
}

// NewStore wraps a pool.
func NewStore(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

// Pool exposes the underlying pool (for the LISTEN broker).
func (s *Store) Pool() *pgxpool.Pool { return s.pool }

const jobColumns = `id, owner_id, claim_token_hash, project_name, input_type, input_url, source_filename, quality,
	status, stage_progress, error, detected_bpm, duration_seconds, sample_rate, channels,
	created_at, started_at, completed_at, expires_at`

func scanJob(row pgx.Row) (*Job, error) {
	var j Job
	err := row.Scan(&j.ID, &j.OwnerID, &j.claimTokenHash, &j.ProjectName, &j.InputType, &j.InputURL, &j.SourceFilename,
		&j.Quality, &j.Status, &j.StageProgress, &j.Error, &j.DetectedBPM, &j.DurationSeconds, &j.SampleRate, &j.Channels,
		&j.CreatedAt, &j.StartedAt, &j.CompletedAt, &j.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	j.Stems = []Stem{}
	return &j, nil
}

// NewClaimToken returns a random token and its SHA-256, which is what gets
// stored. The token is shown to the anonymous creator exactly once.
func NewClaimToken() (token string, hash []byte, err error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", nil, err
	}
	token = base64.RawURLEncoding.EncodeToString(raw)
	return token, HashClaimToken(token), nil
}

// HashClaimToken returns the stored form of a claim token.
func HashClaimToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

// MatchesClaimToken reports whether token hashes to the job's stored hash.
func (j *Job) MatchesClaimToken(token string) bool {
	if token == "" || len(j.claimTokenHash) == 0 {
		return false
	}
	return subtle.ConstantTimeCompare(HashClaimToken(token), j.claimTokenHash) == 1
}

// CreateParams describes a new job.
type CreateParams struct {
	OwnerID        *string
	ClaimTokenHash []byte // for anonymous jobs
	ProjectName    string
	InputType      string
	InputURL       *string
	SourceFilename *string
	Quality        string
	ExpiresAt      time.Time
}

// Create inserts a pending job (with the id chosen by the caller so the
// upload can be stored first) and its first job_event.
func (s *Store) Create(ctx context.Context, id string, p CreateParams) (*Job, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	j, err := scanJob(tx.QueryRow(ctx, `INSERT INTO jobs
		(id, owner_id, claim_token_hash, project_name, input_type, input_url, source_filename, quality, status, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, 'pending', $9)
		RETURNING `+jobColumns,
		id, p.OwnerID, p.ClaimTokenHash, p.ProjectName, p.InputType, p.InputURL, p.SourceFilename, p.Quality, p.ExpiresAt))
	if err != nil {
		return nil, err
	}
	if err := emit(ctx, tx, id, StatusPending, 0, StatusMessage(StatusPending, 0)); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return j, nil
}

// Get loads a job with its stems.
func (s *Store) Get(ctx context.Context, id string) (*Job, error) {
	j, err := scanJob(s.pool.QueryRow(ctx, `SELECT `+jobColumns+` FROM jobs WHERE id = $1`, id))
	if err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `SELECT name, bytes, frames, sample_rate, bit_depth, channels, peaks_path
		FROM stems WHERE job_id = $1 ORDER BY name`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var st Stem
		var peaksPath *string
		if err := rows.Scan(&st.Name, &st.Bytes, &st.Frames, &st.SampleRate, &st.BitDepth, &st.Channels, &peaksPath); err != nil {
			return nil, err
		}
		st.StreamURL = StemStreamURL(id, st.Name)
		if peaksPath != nil {
			u := StemPeaksURL(id, st.Name)
			st.PeaksURL = &u
		}
		j.Stems = append(j.Stems, st)
	}
	return j, rows.Err()
}

// ListFilter selects which of an owner's jobs to list.
type ListFilter struct {
	Status   string // all | active | completed | failed
	Query    string // substring of project_name (case-insensitive)
	Page     int
	PageSize int
}

// List returns one page of the owner's jobs, newest first, plus the total.
func (s *Store) List(ctx context.Context, ownerID string, f ListFilter) ([]*Job, int, error) {
	var statuses []string
	switch f.Status {
	case "", "all":
	case "active":
		statuses = []string{StatusPending, StatusConverting, StatusAnalyzing, StatusSeparating, StatusFinalizing, StatusPackaging}
	case "completed":
		statuses = []string{StatusCompleted}
	case "failed":
		statuses = []string{StatusFailed, StatusCancelled}
	default:
		return nil, 0, ErrInvalidStatus
	}
	if f.Page < 1 {
		f.Page = 1
	}
	if f.PageSize < 1 || f.PageSize > 100 {
		f.PageSize = 20
	}
	where := `WHERE owner_id = $1 AND ($2::text[] IS NULL OR status = ANY($2)) AND ($3 = '' OR project_name ILIKE '%' || $3 || '%')`
	var total int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM jobs `+where, ownerID, statuses, f.Query).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.pool.Query(ctx, `SELECT `+jobColumns+` FROM jobs `+where+
		` ORDER BY created_at DESC, id LIMIT $4 OFFSET $5`, ownerID, statuses, f.Query, f.PageSize, (f.Page-1)*f.PageSize)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []*Job{}
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, j)
	}
	return out, total, rows.Err()
}

// Delete removes the job row (stems/events cascade). Callers remove files.
func (s *Store) Delete(ctx context.Context, id string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM jobs WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// Claim (the HTTP sense) attaches an anonymous job to a user after
// verifying the claim token, and extends its retention.
func (s *Store) ClaimForUser(ctx context.Context, id, token, userID string, retention time.Duration) (*Job, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	j, err := scanJob(tx.QueryRow(ctx, `SELECT `+jobColumns+` FROM jobs WHERE id = $1 FOR UPDATE`, id))
	if err != nil {
		return nil, err
	}
	if !j.IsAnonymous() {
		return nil, ErrAlreadyOwned
	}
	if !j.MatchesClaimToken(token) {
		return nil, ErrBadClaimToken
	}
	j, err = scanJob(tx.QueryRow(ctx, `UPDATE jobs SET owner_id = $2, claim_token_hash = NULL,
		expires_at = greatest(expires_at, now() + $3::interval)
		WHERE id = $1 RETURNING `+jobColumns, id, userID, retention))
	if err != nil {
		return nil, err
	}
	return j, tx.Commit(ctx)
}

// Events returns the job's events with id > afterID in order.
func (s *Store) Events(ctx context.Context, jobID string, afterID int64) ([]Event, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, job_id, status, progress, message, at FROM job_events
		WHERE job_id = $1 AND id > $2 ORDER BY id`, jobID, afterID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Event
	for rows.Next() {
		var e Event
		if err := rows.Scan(&e.ID, &e.JobID, &e.Status, &e.Progress, &e.Message, &e.At); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// UpsertStem records a stem row (used by the worker and by tests).
func (s *Store) UpsertStem(ctx context.Context, jobID string, st Stem, path string, peaksPath *string) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO stems (job_id, name, path, bytes, frames, sample_rate, bit_depth, channels, peaks_path)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		ON CONFLICT (job_id, name) DO UPDATE SET path = EXCLUDED.path, bytes = EXCLUDED.bytes, frames = EXCLUDED.frames,
			sample_rate = EXCLUDED.sample_rate, bit_depth = EXCLUDED.bit_depth, channels = EXCLUDED.channels, peaks_path = EXCLUDED.peaks_path`,
		jobID, st.Name, path, st.Bytes, st.Frames, st.SampleRate, st.BitDepth, st.Channels, peaksPath)
	return err
}

// emit writes a job_event and notifies listeners inside tx.
func emit(ctx context.Context, tx pgx.Tx, jobID, status string, progress int16, message string) error {
	if _, err := tx.Exec(ctx, `INSERT INTO job_events (job_id, status, progress, message) VALUES ($1, $2, $3, $4)`,
		jobID, status, progress, message); err != nil {
		return fmt.Errorf("job_events: %w", err)
	}
	if _, err := tx.Exec(ctx, `SELECT pg_notify($1, $2)`, NotifyChannel, jobID); err != nil {
		return fmt.Errorf("notify: %w", err)
	}
	return nil
}
