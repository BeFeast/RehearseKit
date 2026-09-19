package auth

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/BeFeast/RehearseKit/internal/api/respond"
	"github.com/BeFeast/RehearseKit/internal/auth/googleid"
)

// Handlers serves /api/v1/auth/* and /api/v1/admin/users*.
type Handlers struct {
	store          *Store
	googleVerifier *googleid.Verifier // nil when RK_GOOGLE_CLIENT_ID is unset
}

// NewHandlers builds the auth handlers. google may be nil, in which case
// POST /api/v1/auth/google answers 501 google_not_configured.
func NewHandlers(store *Store, google *googleid.Verifier) *Handlers {
	return &Handlers{store: store, googleVerifier: google}
}

// Register mounts the auth routes on mux. Paths are absolute.
func (h *Handlers) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/auth/login", h.login)
	mux.HandleFunc("POST /api/v1/auth/register", h.register)
	mux.HandleFunc("POST /api/v1/auth/logout", h.logout)
	mux.Handle("GET /api/v1/auth/me", RequireSession(http.HandlerFunc(h.me)))
	mux.HandleFunc("POST /api/v1/auth/google", h.googleSignIn)

	mux.Handle("GET /api/v1/admin/users", RequireAdmin(http.HandlerFunc(h.adminListUsers)))
	mux.Handle("POST /api/v1/admin/users/{id}/approve", RequireAdmin(http.HandlerFunc(h.adminSetStatus(StatusActive))))
	mux.Handle("POST /api/v1/admin/users/{id}/deactivate", RequireAdmin(http.HandlerFunc(h.adminSetStatus(StatusInactive))))
	mux.Handle("POST /api/v1/admin/users/{id}/role", RequireAdmin(http.HandlerFunc(h.adminSetRole)))
}

type credentials struct {
	Email    string `json:"email"`
	Password string `json:"password"`
	Name     string `json:"name,omitempty"`
}

func (h *Handlers) login(w http.ResponseWriter, r *http.Request) {
	var c credentials
	if err := respond.DecodeJSON(r, &c); err != nil {
		respond.Fail(w, err)
		return
	}
	invalid := respond.E(http.StatusUnauthorized, "invalid_credentials", "email or password is incorrect")
	u, err := h.store.UserByEmail(r.Context(), c.Email)
	if errors.Is(err, ErrNotFound) {
		respond.Fail(w, invalid)
		return
	}
	if err != nil {
		respond.Fail(w, err)
		return
	}
	if u.Provider != ProviderPassword || u.PasswordHash == nil {
		respond.Fail(w, invalid)
		return
	}
	ok, err := VerifyPassword(*u.PasswordHash, c.Password)
	if err != nil {
		respond.Fail(w, err)
		return
	}
	if !ok {
		respond.Fail(w, invalid)
		return
	}
	switch u.Status {
	case StatusPending:
		respond.Failf(w, http.StatusForbidden, "pending_approval", "your account is waiting for admin approval")
		return
	case StatusInactive:
		respond.Failf(w, http.StatusForbidden, "account_inactive", "your account has been deactivated")
		return
	}
	sess, err := h.store.CreateSession(r.Context(), u.ID, r.UserAgent())
	if err != nil {
		respond.Fail(w, err)
		return
	}
	SetSessionCookie(w, r, sess)
	respond.JSON(w, http.StatusOK, u)
}

// register creates a password account in the pending state; an admin has to
// approve it before login succeeds. It never issues a session.
func (h *Handlers) register(w http.ResponseWriter, r *http.Request) {
	var c credentials
	if err := respond.DecodeJSON(r, &c); err != nil {
		respond.Fail(w, err)
		return
	}
	email := NormalizeEmail(c.Email)
	if email == "" || !strings.Contains(email, "@") || len(email) > 254 {
		respond.Failf(w, http.StatusBadRequest, "invalid_email", "a valid email address is required")
		return
	}
	if len(c.Password) < 8 || len(c.Password) > 256 {
		respond.Failf(w, http.StatusBadRequest, "weak_password", "password must be between 8 and 256 characters")
		return
	}
	if len(c.Name) > 200 {
		respond.Failf(w, http.StatusBadRequest, "invalid_name", "name is too long")
		return
	}
	u, err := h.store.CreateUser(r.Context(), CreateUserParams{
		Email: email, Name: strings.TrimSpace(c.Name), Password: c.Password,
		Provider: ProviderPassword, Role: RoleUser, Status: StatusPending,
	})
	if errors.Is(err, ErrEmailTaken) {
		respond.Failf(w, http.StatusConflict, "email_taken", "an account with this email already exists")
		return
	}
	if err != nil {
		respond.Fail(w, err)
		return
	}
	respond.JSON(w, http.StatusCreated, u)
}

func (h *Handlers) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(CookieName); err == nil && c.Value != "" {
		if err := h.store.DeleteSession(r.Context(), c.Value); err != nil {
			respond.Fail(w, err)
			return
		}
	}
	ClearSessionCookie(w, r)
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handlers) me(w http.ResponseWriter, r *http.Request) {
	respond.JSON(w, http.StatusOK, UserFrom(r.Context()))
}

// statusDenied is the 403 envelope for pending accounts: the usual
// {"code","message"} plus the user, so the SPA can show who is waiting on
// /pending-approval without a session.
type statusDenied struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	User    *User  `json:"user"`
}

// google signs a user in with a Google Identity Services ID token
// ({"credential": "<jwt>"}, the field name GIS uses). The token is verified
// against Google's JWKS; then the account is matched by Google sub, else by
// email, else created in the pending state. Sessions are only issued to
// active accounts.
func (h *Handlers) googleSignIn(w http.ResponseWriter, r *http.Request) {
	if h.googleVerifier == nil {
		respond.Failf(w, http.StatusNotImplemented, "google_not_configured",
			"Google sign-in is not enabled on this server; use email and password")
		return
	}
	var body struct {
		Credential string `json:"credential"`
	}
	if err := respond.DecodeJSON(r, &body); err != nil {
		respond.Fail(w, err)
		return
	}
	if body.Credential == "" || len(body.Credential) > 8192 {
		respond.Failf(w, http.StatusBadRequest, "invalid_json", "credential is required")
		return
	}
	claims, err := h.googleVerifier.Verify(r.Context(), body.Credential)
	if err != nil {
		var ge *googleid.Error
		switch {
		case errors.Is(err, googleid.ErrEmailNotVerified):
			respond.Failf(w, http.StatusForbidden, "email_not_verified", "your Google account email is not verified")
		case errors.As(err, &ge):
			slog.Debug("google id token rejected", "reason", ge.Reason, "detail", ge.Detail)
			respond.Failf(w, http.StatusUnauthorized, "invalid_google_token", "Google sign-in could not be verified; try again")
		default:
			slog.Warn("google jwks", "err", err)
			respond.Failf(w, http.StatusServiceUnavailable, "google_unavailable", "Google sign-in is temporarily unavailable")
		}
		return
	}
	email := NormalizeEmail(claims.Email)
	if email == "" || !strings.Contains(email, "@") || len(email) > 254 {
		respond.Failf(w, http.StatusUnauthorized, "invalid_google_token", "Google account has no usable email")
		return
	}
	profile := GoogleProfile{Sub: claims.Subject, Name: strings.TrimSpace(claims.Name), AvatarURL: claims.Picture}

	u, err := h.store.UserByGoogleSub(r.Context(), claims.Subject)
	if errors.Is(err, ErrNotFound) {
		u, err = h.store.UserByEmail(r.Context(), email)
	}
	created := false
	if errors.Is(err, ErrNotFound) {
		u, err = h.store.CreateUser(r.Context(), CreateUserParams{
			Email: email, Name: clampText(profile.Name, 200), AvatarURL: clampText(profile.AvatarURL, 2048),
			Provider: ProviderGoogle, GoogleSub: profile.Sub, Role: RoleUser, Status: StatusPending,
		})
		created = err == nil
		if errors.Is(err, ErrEmailTaken) {
			// Lost a race with a concurrent first sign-in or registration.
			u, err = h.store.UserByEmail(r.Context(), email)
		}
	}
	if err != nil {
		respond.Fail(w, err)
		return
	}
	if !created {
		if u, err = h.store.LinkGoogle(r.Context(), u.ID, profile); err != nil {
			respond.Fail(w, err)
			return
		}
	}
	switch u.Status {
	case StatusPending:
		respond.JSON(w, http.StatusForbidden, statusDenied{
			Code: "pending_approval", Message: "your account is waiting for admin approval", User: u,
		})
		return
	case StatusInactive:
		respond.Failf(w, http.StatusForbidden, "account_inactive", "your account has been deactivated")
		return
	}
	sess, err := h.store.CreateSession(r.Context(), u.ID, r.UserAgent())
	if err != nil {
		respond.Fail(w, err)
		return
	}
	SetSessionCookie(w, r, sess)
	respond.JSON(w, http.StatusOK, u)
}

type usersPage struct {
	Items    []*User `json:"items"`
	Page     int     `json:"page"`
	PageSize int     `json:"page_size"`
	Total    int     `json:"total"`
}

func (h *Handlers) adminListUsers(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	p := ListUsersParams{Status: q.Get("status"), Query: strings.TrimSpace(q.Get("q"))}
	p.Page, _ = strconv.Atoi(q.Get("page"))
	p.PageSize, _ = strconv.Atoi(q.Get("page_size"))
	if p.Status == "all" {
		p.Status = ""
	}
	if p.Page < 1 {
		p.Page = 1
	}
	if p.PageSize < 1 || p.PageSize > 100 {
		p.PageSize = 20
	}
	users, total, err := h.store.ListUsers(r.Context(), p)
	if err != nil {
		respond.Fail(w, err)
		return
	}
	respond.JSON(w, http.StatusOK, usersPage{Items: users, Page: p.Page, PageSize: p.PageSize, Total: total})
}

func (h *Handlers) adminSetStatus(status string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u, err := h.store.SetUserStatus(r.Context(), r.PathValue("id"), status)
		h.writeAdminResult(w, u, err)
	}
}

func (h *Handlers) adminSetRole(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Role string `json:"role"`
	}
	if err := respond.DecodeJSON(r, &body); err != nil {
		respond.Fail(w, err)
		return
	}
	if body.Role != RoleUser && body.Role != RoleAdmin {
		respond.Failf(w, http.StatusBadRequest, "invalid_role", "role must be user or admin")
		return
	}
	u, err := h.store.SetUserRole(r.Context(), r.PathValue("id"), body.Role)
	h.writeAdminResult(w, u, err)
}

func (h *Handlers) writeAdminResult(w http.ResponseWriter, u *User, err error) {
	switch {
	case errors.Is(err, ErrNotFound):
		respond.Fail(w, respond.ErrNotFound)
	case errors.Is(err, ErrLastAdmin):
		respond.Failf(w, http.StatusConflict, "last_admin", "the last active admin cannot be demoted or deactivated")
	case err != nil:
		respond.Fail(w, err)
	default:
		respond.JSON(w, http.StatusOK, u)
	}
}
