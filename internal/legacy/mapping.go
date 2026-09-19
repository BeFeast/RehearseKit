package legacy

import (
	"fmt"
	"strings"

	"github.com/BeFeast/RehearseKit/internal/auth"
	"github.com/BeFeast/RehearseKit/internal/jobs"
)

// ErrLegacyStemsMissing is the jobs.error text for a legacy COMPLETED job
// whose four stem WAVs are not all present in the legacy directory.
const ErrLegacyStemsMissing = "legacy stems missing"

// RequiredStems are the stems every legacy (htdemucs 4-stem) job produced.
var RequiredStems = []string{"vocals", "drums", "bass", "other"}

// MapStatus converts a legacy jobstatus enum value (upper case) to the new
// lower-case status. Terminal statuses map one to one. A job that was still
// in flight when the legacy stack was frozen cannot be resumed by the new
// worker (its Celery state is gone), so it is imported as failed with an
// explanatory error; the second return value carries that error text and is
// empty otherwise.
func MapStatus(legacy string) (status string, errText string, err error) {
	switch strings.ToUpper(strings.TrimSpace(legacy)) {
	case "COMPLETED":
		return jobs.StatusCompleted, "", nil
	case "FAILED":
		return jobs.StatusFailed, "", nil
	case "CANCELLED":
		return jobs.StatusCancelled, "", nil
	case "PENDING", "CONVERTING", "ANALYZING", "SEPARATING", "FINALIZING", "PACKAGING":
		return jobs.StatusFailed, "legacy job was " + strings.ToUpper(strings.TrimSpace(legacy)) + " at import time", nil
	}
	return "", "", fmt.Errorf("unknown legacy status %q", legacy)
}

// MapQuality converts a legacy qualitymode to the new quality preset.
func MapQuality(legacy string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(legacy)) {
	case "fast":
		return jobs.QualityFast, nil
	case "high":
		return jobs.QualityHigh, nil
	}
	return "", fmt.Errorf("unknown legacy quality %q", legacy)
}

// MapInputType validates a legacy inputtype; the values are unchanged.
func MapInputType(legacy string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(legacy)) {
	case jobs.InputUpload:
		return jobs.InputUpload, nil
	case jobs.InputYouTube:
		return jobs.InputYouTube, nil
	}
	return "", fmt.Errorf("unknown legacy input type %q", legacy)
}

// MapUserRole returns admin for legacy is_admin users, user otherwise.
func MapUserRole(isAdmin bool) string {
	if isAdmin {
		return auth.RoleAdmin
	}
	return auth.RoleUser
}

// MapUserStatus returns active for legacy is_active users; everyone else
// lands in pending so an admin has to approve them before they can sign in.
func MapUserStatus(isActive bool) string {
	if isActive {
		return auth.StatusActive
	}
	return auth.StatusPending
}

// MapProvider converts the legacy oauth_provider column. Google accounts
// stay Google; everything else (NULL, "email", unknown providers) becomes a
// password account. Legacy password hashes are bcrypt and the new stack
// verifies argon2id only, so they are never migrated: the returned account
// has no password until an admin resets it.
func MapProvider(oauthProvider *string) string {
	if oauthProvider != nil && strings.EqualFold(strings.TrimSpace(*oauthProvider), auth.ProviderGoogle) {
		return auth.ProviderGoogle
	}
	return auth.ProviderPassword
}
