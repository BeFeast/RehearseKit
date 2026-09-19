// Package googleidtest is a fake Google JWKS endpoint plus a token minter
// for tests of the ID-token flow. It is imported only from _test files.
package googleidtest

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Issuer is a fake Google: one or more RSA keys served as a JWKS over
// httptest, and a Sign method that mints RS256 ID tokens with those keys.
type Issuer struct {
	Server *httptest.Server
	// MaxAge is the Cache-Control max-age (seconds) the JWKS response
	// advertises. Zero omits the header.
	MaxAge int
	// Fetches counts JWKS requests served.
	Fetches atomic.Int64
	// Gate, when non-nil, is called at the start of every JWKS request;
	// tests use it to hold a fetch open.
	Gate func()

	mu   sync.Mutex
	keys map[string]*rsa.PrivateKey
	kids []string
}

// New starts an Issuer with one 2048-bit key ("kid-1") and a 1 h max-age.
func New(t testing.TB) *Issuer {
	t.Helper()
	iss := &Issuer{keys: map[string]*rsa.PrivateKey{}, MaxAge: 3600}
	iss.AddKey(t, "kid-1")
	iss.Server = httptest.NewServer(http.HandlerFunc(iss.serveJWKS))
	t.Cleanup(iss.Server.Close)
	return iss
}

// URL is the JWKS endpoint.
func (i *Issuer) URL() string { return i.Server.URL }

// AddKey generates and publishes a new signing key under kid.
func (i *Issuer) AddKey(t testing.TB, kid string) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	i.keys[kid] = key
	i.kids = append(i.kids, kid)
}

// Claims returns a valid payload for aud that expires in an hour; tests
// override entries before signing.
func Claims(aud, email string) map[string]any {
	now := time.Now()
	return map[string]any{
		"iss":            "https://accounts.google.com",
		"aud":            aud,
		"sub":            "1234567890",
		"email":          email,
		"email_verified": true,
		"name":           "Test User",
		"picture":        "https://lh3.googleusercontent.com/a/photo",
		"iat":            now.Unix(),
		"exp":            now.Add(time.Hour).Unix(),
	}
}

// Sign mints a compact JWT for claims with the key kid (must exist).
func (i *Issuer) Sign(t testing.TB, kid string, claims map[string]any) string {
	t.Helper()
	i.mu.Lock()
	key, ok := i.keys[kid]
	i.mu.Unlock()
	if !ok {
		t.Fatalf("googleidtest: no key %q", kid)
	}
	return SignWith(t, key, kid, "RS256", claims)
}

// SignWith mints a JWT with an arbitrary key/kid/alg header, so tests can
// produce tokens the JWKS does not vouch for.
func SignWith(t testing.TB, key *rsa.PrivateKey, kid, alg string, claims map[string]any) string {
	t.Helper()
	hdr, _ := json.Marshal(map[string]string{"alg": alg, "typ": "JWT", "kid": kid})
	body, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	signing := base64.RawURLEncoding.EncodeToString(hdr) + "." + base64.RawURLEncoding.EncodeToString(body)
	digest := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	return signing + "." + base64.RawURLEncoding.EncodeToString(sig)
}

// ForeignKey returns a fresh key the issuer does not publish.
func ForeignKey(t testing.TB) *rsa.PrivateKey {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func (i *Issuer) serveJWKS(w http.ResponseWriter, _ *http.Request) {
	i.Fetches.Add(1)
	if i.Gate != nil {
		i.Gate()
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	var keys []map[string]string
	for _, kid := range i.kids {
		pub := &i.keys[kid].PublicKey
		keys = append(keys, map[string]string{
			"kty": "RSA", "kid": kid, "use": "sig", "alg": "RS256",
			"n": base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
			"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes()),
		})
	}
	w.Header().Set("Content-Type", "application/json")
	if i.MaxAge > 0 {
		w.Header().Set("Cache-Control", "public, max-age="+strconv.Itoa(i.MaxAge)+", must-revalidate, no-transform")
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"keys": keys})
}
