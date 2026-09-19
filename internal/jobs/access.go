package jobs

import (
	"net/http"
	"time"

	"github.com/BeFeast/RehearseKit/internal/api/respond"
	"github.com/BeFeast/RehearseKit/internal/auth"
)

// ClaimTokenHeader carries an anonymous creator's claim token.
const ClaimTokenHeader = "X-Claim-Token"

// AuthorizeRead decides whether the request may read job j:
//   - owned jobs: only the owner's session (401 without a session, 404 for
//     anyone else so existence is not leaked);
//   - anonymous jobs: anyone with the link until expires_at, or the holder
//     of the claim token at any time before cleanup.
func AuthorizeRead(r *http.Request, j *Job) error {
	u := auth.UserFrom(r.Context())
	if !j.IsAnonymous() {
		if u == nil {
			return respond.ErrUnauthorized
		}
		if u.ID != *j.OwnerID {
			return respond.ErrNotFound
		}
		return nil
	}
	if j.MatchesClaimToken(r.Header.Get(ClaimTokenHeader)) {
		return nil
	}
	if time.Now().After(j.ExpiresAt) {
		return respond.E(http.StatusGone, "expired", "this anonymous job has expired")
	}
	return nil
}

// AuthorizeWrite decides whether the request may cancel or delete job j:
// the owner's session, or the claim-token holder of an unclaimed job.
func AuthorizeWrite(r *http.Request, j *Job) error {
	u := auth.UserFrom(r.Context())
	if !j.IsAnonymous() {
		if u == nil {
			return respond.ErrUnauthorized
		}
		if u.ID != *j.OwnerID {
			return respond.ErrNotFound
		}
		return nil
	}
	if j.MatchesClaimToken(r.Header.Get(ClaimTokenHeader)) {
		return nil
	}
	return respond.E(http.StatusForbidden, "claim_token_required",
		"anonymous jobs can only be changed with the claim token issued at creation")
}
