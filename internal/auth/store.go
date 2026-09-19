// Package auth implements cookie sessions, argon2id passwords, the user
// approval model and the /api/v1/auth handlers.
package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// User roles and statuses (CHECK constraints in the users table).
const (
	RoleUser  = "user"
	RoleAdmin = "admin"

	StatusPending  = "pending"
	StatusActive   = "active"
	StatusInactive = "inactive"

	ProviderPassword = "password"
	ProviderGoogle   = "google"
)

// SessionTTL is the fixed lifetime of a session cookie.
const SessionTTL = 30 * 24 * time.Hour

// User is a row of the users table.
type User struct {
	ID           string     `json:"id"`
	Email        string     `json:"email"`
	Name         string     `json:"name"`
	AvatarURL    *string    `json:"avatar_url"`
	Provider     string     `json:"provider"`
	PasswordHash *string    `json:"-"`
	GoogleSub    *string    `json:"-"`
	Role         string     `json:"role"`
	Status       string     `json:"status"`
	CreatedAt    time.Time  `json:"created_at"`
	LastLoginAt  *time.Time `json:"last_login_at"`
}

// IsAdmin reports whether the user has the admin role.
func (u *User) IsAdmin() bool { return u != nil && u.Role == RoleAdmin }

// Session is a row of the sessions table with the raw id.
type Session struct {
	ID        []byte
	UserID    string
	CreatedAt time.Time
	ExpiresAt time.Time
	UserAgent string
}

// Token is the cookie form of the session id.
func (s *Session) Token() string { return base64.RawURLEncoding.EncodeToString(s.ID) }

// Store persists users and sessions.
type Store struct {
	pool *pgxpool.Pool
}

// NewStore wraps a pool.
func NewStore(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

// Errors returned by Store.
var (
	ErrNotFound      = errors.New("auth: not found")
	ErrEmailTaken    = errors.New("auth: email already registered")
	ErrLastAdmin     = errors.New("auth: cannot demote or deactivate the last admin")
	ErrInvalidStatus = errors.New("auth: invalid status")
)

const userColumns = `id, email, name, avatar_url, provider, password_hash, google_sub, role, status, created_at, last_login_at`

func scanUser(row pgx.Row) (*User, error) {
	var u User
	err := row.Scan(&u.ID, &u.Email, &u.Name, &u.AvatarURL, &u.Provider, &u.PasswordHash, &u.GoogleSub, &u.Role, &u.Status, &u.CreatedAt, &u.LastLoginAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &u, nil
}

// NormalizeEmail trims and lower-cases an email address.
func NormalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

// CreateUserParams describes a new user.
type CreateUserParams struct {
	Email     string
	Name      string
	Password  string // plaintext; hashed here. Empty for non-password providers.
	Provider  string
	Role      string
	Status    string
	AvatarURL string // optional
	GoogleSub string // optional; set for ProviderGoogle
}

// CreateUser inserts a user. Returns ErrEmailTaken on duplicate email.
func (s *Store) CreateUser(ctx context.Context, p CreateUserParams) (*User, error) {
	email := NormalizeEmail(p.Email)
	if email == "" || !strings.Contains(email, "@") {
		return nil, fmt.Errorf("auth: invalid email %q", p.Email)
	}
	var hash *string
	if p.Provider == ProviderPassword {
		if p.Password == "" {
			return nil, errors.New("auth: password required")
		}
		h, err := HashPassword(p.Password)
		if err != nil {
			return nil, err
		}
		hash = &h
	}
	if p.Role == "" {
		p.Role = RoleUser
	}
	if p.Status == "" {
		p.Status = StatusPending
	}
	row := s.pool.QueryRow(ctx, `INSERT INTO users (id, email, name, avatar_url, provider, password_hash, google_sub, role, status)
		VALUES (gen_random_uuid(), $1, $2, NULLIF($3, ''), $4, $5, NULLIF($6, ''), $7, $8)
		ON CONFLICT (email) DO NOTHING
		RETURNING `+userColumns, email, p.Name, p.AvatarURL, p.Provider, hash, p.GoogleSub, p.Role, p.Status)
	u, err := scanUser(row)
	if errors.Is(err, ErrNotFound) {
		return nil, ErrEmailTaken
	}
	return u, err
}

// UserByGoogleSub looks a user up by the Google account id recorded at
// their first Google sign-in.
func (s *Store) UserByGoogleSub(ctx context.Context, sub string) (*User, error) {
	if sub == "" {
		return nil, ErrNotFound
	}
	return scanUser(s.pool.QueryRow(ctx, `SELECT `+userColumns+` FROM users WHERE google_sub = $1`, sub))
}

// GoogleProfile is what a verified ID token tells us about the account.
type GoogleProfile struct {
	Sub       string
	Name      string
	AvatarURL string
}

// LinkGoogle records a Google sign-in on an existing user: stores the
// account id, refreshes the avatar and fills in an empty name. The
// provider and password hash are left alone so a password account keeps
// working with either method.
func (s *Store) LinkGoogle(ctx context.Context, id string, p GoogleProfile) (*User, error) {
	if p.Sub == "" {
		return nil, errors.New("auth: google sub required")
	}
	return scanUser(s.pool.QueryRow(ctx, `UPDATE users SET
			google_sub = $2,
			avatar_url = COALESCE(NULLIF($3, ''), avatar_url),
			name = CASE WHEN name = '' THEN $4 ELSE name END
		WHERE id = $1 RETURNING `+userColumns, id, p.Sub, clampText(p.AvatarURL, 2048), clampText(p.Name, 200)))
}

// UpsertAdmin creates or updates a password admin account; used by
// `rk create-admin` to bootstrap a stand.
func (s *Store) UpsertAdmin(ctx context.Context, email, password string) (*User, error) {
	hash, err := HashPassword(password)
	if err != nil {
		return nil, err
	}
	row := s.pool.QueryRow(ctx, `INSERT INTO users (id, email, name, provider, password_hash, role, status)
		VALUES (gen_random_uuid(), $1, '', 'password', $2, 'admin', 'active')
		ON CONFLICT (email) DO UPDATE SET
			provider = 'password', password_hash = EXCLUDED.password_hash,
			role = 'admin', status = 'active'
		RETURNING `+userColumns, NormalizeEmail(email), hash)
	return scanUser(row)
}

// UserByEmail looks a user up by (case-insensitive) email.
func (s *Store) UserByEmail(ctx context.Context, email string) (*User, error) {
	return scanUser(s.pool.QueryRow(ctx, `SELECT `+userColumns+` FROM users WHERE email = $1`, NormalizeEmail(email)))
}

// UserByID looks a user up by id.
func (s *Store) UserByID(ctx context.Context, id string) (*User, error) {
	return scanUser(s.pool.QueryRow(ctx, `SELECT `+userColumns+` FROM users WHERE id = $1`, id))
}

// ListUsersParams filters ListUsers.
type ListUsersParams struct {
	Status   string // "" = all
	Query    string // ILIKE on email/name
	Page     int
	PageSize int
}

// ListUsers returns a page of users and the total count.
func (s *Store) ListUsers(ctx context.Context, p ListUsersParams) ([]*User, int, error) {
	if p.Page < 1 {
		p.Page = 1
	}
	if p.PageSize < 1 || p.PageSize > 100 {
		p.PageSize = 20
	}
	where := `WHERE ($1 = '' OR status = $1) AND ($2 = '' OR email ILIKE '%' || $2 || '%' OR name ILIKE '%' || $2 || '%')`
	var total int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM users `+where, p.Status, p.Query).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.pool.Query(ctx, `SELECT `+userColumns+` FROM users `+where+
		` ORDER BY created_at DESC LIMIT $3 OFFSET $4`, p.Status, p.Query, p.PageSize, (p.Page-1)*p.PageSize)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	users := []*User{}
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, 0, err
		}
		users = append(users, u)
	}
	return users, total, rows.Err()
}

// SetUserStatus changes a user's status, refusing to deactivate the last
// active admin.
func (s *Store) SetUserStatus(ctx context.Context, id, status string) (*User, error) {
	switch status {
	case StatusPending, StatusActive, StatusInactive:
	default:
		return nil, ErrInvalidStatus
	}
	return s.updateGuarded(ctx, id, `status = $2`, status, status != StatusActive)
}

// SetUserRole changes a user's role, refusing to demote the last active admin.
func (s *Store) SetUserRole(ctx context.Context, id, role string) (*User, error) {
	if role != RoleUser && role != RoleAdmin {
		return nil, fmt.Errorf("auth: invalid role %q", role)
	}
	return s.updateGuarded(ctx, id, `role = $2`, role, role != RoleAdmin)
}

// updateGuarded applies `SET <set>` to the user, and when guard is true
// refuses if that user is the only active admin.
func (s *Store) updateGuarded(ctx context.Context, id, set string, val any, guard bool) (*User, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if guard {
		// Lock the admin rows so two concurrent demotions cannot both pass.
		var isActiveAdmin bool
		var others int
		err := tx.QueryRow(ctx, `WITH admins AS (
				SELECT id FROM users WHERE role = 'admin' AND status = 'active' FOR UPDATE)
			SELECT EXISTS (SELECT 1 FROM admins WHERE id = $1),
			       (SELECT count(*) FROM admins WHERE id <> $1)`, id).Scan(&isActiveAdmin, &others)
		if err != nil {
			return nil, err
		}
		if isActiveAdmin && others == 0 {
			return nil, ErrLastAdmin
		}
	}
	u, err := scanUser(tx.QueryRow(ctx, `UPDATE users SET `+set+` WHERE id = $1 RETURNING `+userColumns, id, val))
	if err != nil {
		return nil, err
	}
	return u, tx.Commit(ctx)
}

// CreateSession issues a new session for the user.
func (s *Store) CreateSession(ctx context.Context, userID, userAgent string) (*Session, error) {
	id := make([]byte, 32)
	if _, err := rand.Read(id); err != nil {
		return nil, err
	}
	userAgent = clampText(userAgent, 512)
	sess := &Session{ID: id, UserID: userID, UserAgent: userAgent}
	err := s.pool.QueryRow(ctx, `INSERT INTO sessions (id, user_id, expires_at, user_agent)
		VALUES ($1, $2, now() + $3::interval, $4) RETURNING created_at, expires_at`,
		id, userID, SessionTTL, userAgent).Scan(&sess.CreatedAt, &sess.ExpiresAt)
	if err != nil {
		return nil, err
	}
	_, _ = s.pool.Exec(ctx, `UPDATE users SET last_login_at = now() WHERE id = $1`, userID)
	return sess, nil
}

// SessionUser resolves a cookie token to its user. Expired sessions and
// inactive users yield ErrNotFound.
func (s *Store) SessionUser(ctx context.Context, token string) (*User, error) {
	id, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(id) != 32 {
		return nil, ErrNotFound
	}
	u, err := scanUser(s.pool.QueryRow(ctx, `SELECT `+userColumnsPrefixed("u")+` FROM sessions s
		JOIN users u ON u.id = s.user_id
		WHERE s.id = $1 AND s.expires_at > now() AND u.status = 'active'`, id))
	if err != nil {
		return nil, err
	}
	return u, nil
}

// DeleteSession removes a session by cookie token; unknown tokens are ignored.
func (s *Store) DeleteSession(ctx context.Context, token string) error {
	id, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return nil
	}
	_, err = s.pool.Exec(ctx, `DELETE FROM sessions WHERE id = $1`, id)
	return err
}

// clampText makes s valid UTF-8 and cuts it to at most maxBytes without
// splitting a rune, so it can be stored in a Postgres text column.
func clampText(s string, maxBytes int) string {
	s = strings.ToValidUTF8(s, "�")
	if len(s) <= maxBytes {
		return s
	}
	cut := maxBytes
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

func userColumnsPrefixed(alias string) string {
	cols := strings.Split(userColumns, ", ")
	for i, c := range cols {
		cols[i] = alias + "." + c
	}
	return strings.Join(cols, ", ")
}
