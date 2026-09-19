// Package googleid verifies Google Identity Services ID tokens (the JWT the
// browser obtains via GIS and posts to /api/v1/auth/google).
//
// Verification follows Google's documented rules: the RS256 signature is
// checked against the JWKS at https://www.googleapis.com/oauth2/v3/certs,
// `iss` must be accounts.google.com (with or without https://), `aud` must
// equal our OAuth client id, `exp` must be in the future and the email must
// be marked verified. The JWKS is cached for the Cache-Control max-age Google
// sends and refetched when a token names an unknown key id. Only the standard
// library is used.
package googleid

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// DefaultJWKSURL is Google's OAuth2 v3 certificate endpoint.
const DefaultJWKSURL = "https://www.googleapis.com/oauth2/v3/certs"

// Verification failures. All are wrapped in *Error so the handler can map
// them to one 401 without leaking which check failed to the caller.
var (
	ErrMalformed        = errors.New("malformed token")
	ErrAlgorithm        = errors.New("unsupported signing algorithm")
	ErrUnknownKey       = errors.New("unknown signing key")
	ErrSignature        = errors.New("invalid signature")
	ErrIssuer           = errors.New("unexpected issuer")
	ErrAudience         = errors.New("unexpected audience")
	ErrExpired          = errors.New("token expired")
	ErrNotYetValid      = errors.New("token issued in the future")
	ErrEmailNotVerified = errors.New("email not verified")
	ErrMissingClaim     = errors.New("missing claim")
)

// Error wraps a verification failure with the reason.
type Error struct {
	Reason error
	Detail string
}

func (e *Error) Error() string {
	if e.Detail == "" {
		return "googleid: " + e.Reason.Error()
	}
	return "googleid: " + e.Reason.Error() + ": " + e.Detail
}

// Unwrap lets errors.Is match the Err* sentinels.
func (e *Error) Unwrap() error { return e.Reason }

func fail(reason error, detail string) error { return &Error{Reason: reason, Detail: detail} }

// Claims is the subset of the ID token payload rk uses.
type Claims struct {
	Subject       string `json:"sub"`
	Email         string `json:"email"`
	EmailVerified bool   `json:"-"`
	Name          string `json:"name"`
	Picture       string `json:"picture"`
	HostedDomain  string `json:"hd"`
	IssuedAt      time.Time
	ExpiresAt     time.Time
}

// Options configures a Verifier.
type Options struct {
	// ClientID is the OAuth client id the token's `aud` must equal. Required.
	ClientID string
	// JWKSURL overrides DefaultJWKSURL (tests).
	JWKSURL string
	// HTTPClient fetches the JWKS; defaults to a client with a 10 s timeout.
	HTTPClient *http.Client
	// Now overrides the clock (tests).
	Now func() time.Time
	// ClockSkew tolerated on exp/iat. Default 60 s.
	ClockSkew time.Duration
	// MinRefetchInterval throttles refetches caused by unknown key ids so a
	// flood of bogus tokens cannot hammer Google. Default 1 min.
	MinRefetchInterval time.Duration
	// MaxCacheAge caps how long a JWKS is trusted regardless of Cache-Control.
	// Default 24 h.
	MaxCacheAge time.Duration
}

// Verifier validates Google ID tokens against a cached JWKS. Safe for
// concurrent use.
type Verifier struct {
	clientID           string
	jwksURL            string
	http               *http.Client
	now                func() time.Time
	skew               time.Duration
	minRefetchInterval time.Duration
	maxCacheAge        time.Duration

	mu          sync.Mutex
	keys        map[string]*rsa.PublicKey
	expiresAt   time.Time // cache validity end
	lastFetchAt time.Time
}

// New builds a Verifier. It panics if ClientID is empty: the caller must
// not mount the endpoint without a configured client id.
func New(o Options) *Verifier {
	if o.ClientID == "" {
		panic("googleid: ClientID is required")
	}
	v := &Verifier{
		clientID:           o.ClientID,
		jwksURL:            o.JWKSURL,
		http:               o.HTTPClient,
		now:                o.Now,
		skew:               o.ClockSkew,
		minRefetchInterval: o.MinRefetchInterval,
		maxCacheAge:        o.MaxCacheAge,
	}
	if v.jwksURL == "" {
		v.jwksURL = DefaultJWKSURL
	}
	if v.http == nil {
		v.http = &http.Client{Timeout: 10 * time.Second}
	}
	if v.now == nil {
		v.now = time.Now
	}
	if v.skew == 0 {
		v.skew = time.Minute
	}
	if v.minRefetchInterval == 0 {
		v.minRefetchInterval = time.Minute
	}
	if v.maxCacheAge == 0 {
		v.maxCacheAge = 24 * time.Hour
	}
	return v
}

// ClientID returns the configured audience.
func (v *Verifier) ClientID() string { return v.clientID }

type header struct {
	Alg string `json:"alg"`
	Kid string `json:"kid"`
	Typ string `json:"typ"`
}

// rawClaims mirrors the wire form. Google sends email_verified as a JSON
// bool, but older tokens and some libraries used the string "true".
type rawClaims struct {
	Iss           string          `json:"iss"`
	Aud           json.RawMessage `json:"aud"`
	Sub           string          `json:"sub"`
	Exp           int64           `json:"exp"`
	Iat           int64           `json:"iat"`
	Email         string          `json:"email"`
	EmailVerified json.RawMessage `json:"email_verified"`
	Name          string          `json:"name"`
	Picture       string          `json:"picture"`
	Hd            string          `json:"hd"`
}

// Verify checks token and returns its claims. Errors from a failed check are
// *Error; transport errors while fetching the JWKS are returned as-is.
func (v *Verifier) Verify(ctx context.Context, token string) (*Claims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, fail(ErrMalformed, "expected three segments")
	}
	hdrBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, fail(ErrMalformed, "header is not base64url")
	}
	var hdr header
	if err := json.Unmarshal(hdrBytes, &hdr); err != nil {
		return nil, fail(ErrMalformed, "header is not JSON")
	}
	if hdr.Alg != "RS256" {
		return nil, fail(ErrAlgorithm, hdr.Alg)
	}
	if hdr.Kid == "" {
		return nil, fail(ErrMalformed, "header has no kid")
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return nil, fail(ErrMalformed, "signature is not base64url")
	}
	payloadBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, fail(ErrMalformed, "payload is not base64url")
	}

	pub, err := v.key(ctx, hdr.Kid)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(pub, crypto.SHA256, digest[:], sig); err != nil {
		return nil, fail(ErrSignature, "")
	}

	var rc rawClaims
	if err := json.Unmarshal(payloadBytes, &rc); err != nil {
		return nil, fail(ErrMalformed, "payload is not JSON")
	}
	if rc.Iss != "accounts.google.com" && rc.Iss != "https://accounts.google.com" {
		return nil, fail(ErrIssuer, rc.Iss)
	}
	if !audienceContains(rc.Aud, v.clientID) {
		return nil, fail(ErrAudience, "")
	}
	now := v.now()
	if rc.Exp == 0 {
		return nil, fail(ErrMissingClaim, "exp")
	}
	exp := time.Unix(rc.Exp, 0)
	if !now.Before(exp.Add(v.skew)) {
		return nil, fail(ErrExpired, exp.UTC().Format(time.RFC3339))
	}
	var iat time.Time
	if rc.Iat != 0 {
		iat = time.Unix(rc.Iat, 0)
		if iat.After(now.Add(v.skew)) {
			return nil, fail(ErrNotYetValid, iat.UTC().Format(time.RFC3339))
		}
	}
	if rc.Sub == "" {
		return nil, fail(ErrMissingClaim, "sub")
	}
	if rc.Email == "" {
		return nil, fail(ErrMissingClaim, "email")
	}
	if !boolClaim(rc.EmailVerified) {
		return nil, fail(ErrEmailNotVerified, rc.Email)
	}
	return &Claims{
		Subject:       rc.Sub,
		Email:         rc.Email,
		EmailVerified: true,
		Name:          rc.Name,
		Picture:       rc.Picture,
		HostedDomain:  rc.Hd,
		IssuedAt:      iat,
		ExpiresAt:     exp,
	}, nil
}

// audienceContains accepts `aud` as a string or an array of strings.
func audienceContains(raw json.RawMessage, want string) bool {
	if len(raw) == 0 {
		return false
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s == want
	}
	var list []string
	if err := json.Unmarshal(raw, &list); err == nil {
		for _, a := range list {
			if a == want {
				return true
			}
		}
	}
	return false
}

func boolClaim(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return false
	}
	var b bool
	if err := json.Unmarshal(raw, &b); err == nil {
		return b
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s == "true"
	}
	return false
}

// key returns the public key for kid, refetching the JWKS when the cache is
// stale or does not contain kid (subject to the refetch throttle).
func (v *Verifier) key(ctx context.Context, kid string) (*rsa.PublicKey, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	now := v.now()
	if v.keys != nil && now.Before(v.expiresAt) {
		if k, ok := v.keys[kid]; ok {
			return k, nil
		}
		if now.Sub(v.lastFetchAt) < v.minRefetchInterval {
			return nil, fail(ErrUnknownKey, kid)
		}
	}
	if err := v.fetchLocked(ctx); err != nil {
		if v.keys != nil {
			// A fetch failure should not take sign-in down while the
			// previous key set may still be valid.
			if k, ok := v.keys[kid]; ok {
				return k, nil
			}
		}
		return nil, fmt.Errorf("googleid: fetch jwks: %w", err)
	}
	if k, ok := v.keys[kid]; ok {
		return k, nil
	}
	return nil, fail(ErrUnknownKey, kid)
}

type jwk struct {
	Kty string `json:"kty"`
	Kid string `json:"kid"`
	Use string `json:"use"`
	Alg string `json:"alg"`
	N   string `json:"n"`
	E   string `json:"e"`
}

type jwks struct {
	Keys []jwk `json:"keys"`
}

// fetchLocked downloads the JWKS and replaces the cache. Caller holds mu.
func (v *Verifier) fetchLocked(ctx context.Context) error {
	v.lastFetchAt = v.now()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, v.jwksURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := v.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: HTTP %d", v.jwksURL, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	var set jwks
	if err := json.Unmarshal(body, &set); err != nil {
		return fmt.Errorf("decode jwks: %w", err)
	}
	keys := make(map[string]*rsa.PublicKey, len(set.Keys))
	for _, k := range set.Keys {
		if k.Kty != "RSA" || k.Kid == "" || (k.Use != "" && k.Use != "sig") || (k.Alg != "" && k.Alg != "RS256") {
			continue
		}
		pub, err := parseRSA(k)
		if err != nil {
			continue
		}
		keys[k.Kid] = pub
	}
	if len(keys) == 0 {
		return errors.New("jwks contains no usable RS256 keys")
	}
	v.keys = keys
	v.expiresAt = v.now().Add(v.cacheTTL(resp.Header))
	return nil
}

// cacheTTL derives the cache lifetime from Cache-Control max-age minus Age,
// clamped to [1 min, MaxCacheAge]. Without a usable header it falls back to
// one hour.
func (v *Verifier) cacheTTL(h http.Header) time.Duration {
	ttl := time.Hour
	for _, d := range strings.Split(h.Get("Cache-Control"), ",") {
		d = strings.TrimSpace(strings.ToLower(d))
		if val, ok := strings.CutPrefix(d, "max-age="); ok {
			if n, err := strconv.Atoi(strings.TrimSpace(val)); err == nil && n >= 0 {
				ttl = time.Duration(n) * time.Second
			}
		}
	}
	if age, err := strconv.Atoi(strings.TrimSpace(h.Get("Age"))); err == nil && age > 0 {
		ttl -= time.Duration(age) * time.Second
	}
	if ttl < time.Minute {
		ttl = time.Minute
	}
	if ttl > v.maxCacheAge {
		ttl = v.maxCacheAge
	}
	return ttl
}

func parseRSA(k jwk) (*rsa.PublicKey, error) {
	nb, err := base64.RawURLEncoding.DecodeString(k.N)
	if err != nil {
		return nil, fmt.Errorf("n: %w", err)
	}
	eb, err := base64.RawURLEncoding.DecodeString(k.E)
	if err != nil {
		return nil, fmt.Errorf("e: %w", err)
	}
	if len(nb) < 256 || len(eb) == 0 || len(eb) > 4 {
		return nil, errors.New("key size out of range")
	}
	e := 0
	for _, b := range eb {
		e = e<<8 | int(b)
	}
	if e < 3 || e%2 == 0 {
		return nil, errors.New("invalid exponent")
	}
	return &rsa.PublicKey{N: new(big.Int).SetBytes(nb), E: e}, nil
}
