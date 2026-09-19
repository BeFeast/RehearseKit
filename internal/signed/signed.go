// Package signed issues and verifies HMAC-signed URLs and serves the two
// endpoints GPU runners use them for: downloading a job's converted source
// and uploading stems.
//
// A signed URL is `<path>?exp=<unix seconds>&sig=<base64url HMAC-SHA256>`
// where the MAC covers `METHOD\n<path>\n<exp>`. The key is RK_SIGNING_KEY
// (or derived from RK_RUNNER_TOKEN). The signature binds the method, so a
// GET URL cannot be replayed as a PUT.
package signed

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/url"
	"strconv"
	"time"
)

// Errors returned by Verify.
var (
	ErrMissing   = errors.New("signed: missing exp or sig")
	ErrExpired   = errors.New("signed: URL has expired")
	ErrBadSig    = errors.New("signed: signature mismatch")
	ErrNoKey     = errors.New("signed: no signing key configured")
	ErrBadExpiry = errors.New("signed: malformed exp")
)

// Signer signs and verifies paths with one HMAC key.
type Signer struct {
	key []byte
	now func() time.Time
}

// New returns a Signer. A nil or empty key yields a Signer whose Sign and
// Verify fail with ErrNoKey.
func New(key []byte) *Signer {
	return &Signer{key: key, now: time.Now}
}

// Enabled reports whether a key is configured.
func (s *Signer) Enabled() bool { return len(s.key) > 0 }

func (s *Signer) mac(method, path string, exp int64) []byte {
	m := hmac.New(sha256.New, s.key)
	m.Write([]byte(method))
	m.Write([]byte{'\n'})
	m.Write([]byte(path))
	m.Write([]byte{'\n'})
	m.Write([]byte(strconv.FormatInt(exp, 10)))
	return m.Sum(nil)
}

// Sign returns path with the exp and sig query parameters appended. path
// must be the request path without query (e.g. /api/v1/signed/jobs/x/source).
func (s *Signer) Sign(method, path string, expires time.Time) (string, error) {
	if !s.Enabled() {
		return "", ErrNoKey
	}
	exp := expires.Unix()
	q := url.Values{}
	q.Set("exp", strconv.FormatInt(exp, 10))
	q.Set("sig", base64.RawURLEncoding.EncodeToString(s.mac(method, path, exp)))
	return path + "?" + q.Encode(), nil
}

// Verify checks the exp and sig parameters against method and path.
func (s *Signer) Verify(method, path string, query url.Values) error {
	if !s.Enabled() {
		return ErrNoKey
	}
	expStr, sig := query.Get("exp"), query.Get("sig")
	if expStr == "" || sig == "" {
		return ErrMissing
	}
	exp, err := strconv.ParseInt(expStr, 10, 64)
	if err != nil {
		return ErrBadExpiry
	}
	want := s.mac(method, path, exp)
	got, err := base64.RawURLEncoding.DecodeString(sig)
	if err != nil || !hmac.Equal(got, want) {
		return ErrBadSig
	}
	if s.now().Unix() > exp {
		return ErrExpired
	}
	return nil
}
