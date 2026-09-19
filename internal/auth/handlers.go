package auth

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/BeFeast/RehearseKit/internal/api/respond"
)

// Handlers serves /api/v1/auth/* and /api/v1/admin/users*.
type Handlers struct {
	store *Store
}

// NewHandlers builds the auth handlers.
func NewHandlers(store *Store) *Handlers { return &Handlers{store: store} }

// Register mounts the auth routes on mux. Paths are absolute.
func (h *Handlers) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/auth/login", h.login)
	mux.HandleFunc("POST /api/v1/auth/register", h.register)
	mux.HandleFunc("POST /api/v1/auth/logout", h.logout)
	mux.Handle("GET /api/v1/auth/me", RequireSession(http.HandlerFunc(h.me)))
	mux.HandleFunc("POST /api/v1/auth/google", h.googleNotImplemented)

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

func (h *Handlers) googleNotImplemented(w http.ResponseWriter, _ *http.Request) {
	respond.Failf(w, http.StatusNotImplemented, "not_implemented",
		"Google sign-in is not available in this build; use email and password")
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
