package googleid_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/BeFeast/RehearseKit/internal/auth/googleid"
	"github.com/BeFeast/RehearseKit/internal/auth/googleid/googleidtest"
)

const aud = "123-abc.apps.googleusercontent.com"

func newVerifier(t *testing.T, iss *googleidtest.Issuer, mutate func(*googleid.Options)) *googleid.Verifier {
	t.Helper()
	o := googleid.Options{ClientID: aud, JWKSURL: iss.URL(), HTTPClient: iss.Server.Client()}
	if mutate != nil {
		mutate(&o)
	}
	return googleid.New(o)
}

func TestVerifyValid(t *testing.T) {
	iss := googleidtest.New(t)
	v := newVerifier(t, iss, nil)
	claims := googleidtest.Claims(aud, "Someone@Example.com")
	claims["hd"] = "example.com"
	got, err := v.Verify(context.Background(), iss.Sign(t, "kid-1", claims))
	if err != nil {
		t.Fatal(err)
	}
	if got.Subject != "1234567890" || got.Email != "Someone@Example.com" || !got.EmailVerified ||
		got.Name != "Test User" || got.Picture == "" || got.HostedDomain != "example.com" {
		t.Errorf("claims: %+v", got)
	}
	if got.ExpiresAt.Unix() != claims["exp"].(int64) || got.IssuedAt.Unix() != claims["iat"].(int64) {
		t.Errorf("times: %+v", got)
	}

	// Bare issuer and audience-as-array are both accepted.
	claims["iss"] = "accounts.google.com"
	claims["aud"] = []string{"other", aud}
	if _, err := v.Verify(context.Background(), iss.Sign(t, "kid-1", claims)); err != nil {
		t.Errorf("bare iss / aud list: %v", err)
	}
	// email_verified as the legacy string form.
	claims["email_verified"] = "true"
	if _, err := v.Verify(context.Background(), iss.Sign(t, "kid-1", claims)); err != nil {
		t.Errorf(`email_verified "true": %v`, err)
	}
}

func TestVerifyRejections(t *testing.T) {
	iss := googleidtest.New(t)
	v := newVerifier(t, iss, nil)
	ctx := context.Background()
	past := time.Now().Add(-2 * time.Hour)

	cases := []struct {
		name  string
		token func() string
		want  error
	}{
		{"expired", func() string {
			c := googleidtest.Claims(aud, "a@b.c")
			c["exp"] = past.Unix()
			return iss.Sign(t, "kid-1", c)
		}, googleid.ErrExpired},
		{"wrong aud", func() string {
			return iss.Sign(t, "kid-1", googleidtest.Claims("someone-else.apps.googleusercontent.com", "a@b.c"))
		}, googleid.ErrAudience},
		{"aud list without us", func() string {
			c := googleidtest.Claims(aud, "a@b.c")
			c["aud"] = []string{"x", "y"}
			return iss.Sign(t, "kid-1", c)
		}, googleid.ErrAudience},
		{"wrong iss", func() string {
			c := googleidtest.Claims(aud, "a@b.c")
			c["iss"] = "https://evil.example.com"
			return iss.Sign(t, "kid-1", c)
		}, googleid.ErrIssuer},
		{"email not verified (bool)", func() string {
			c := googleidtest.Claims(aud, "a@b.c")
			c["email_verified"] = false
			return iss.Sign(t, "kid-1", c)
		}, googleid.ErrEmailNotVerified},
		{"email not verified (missing)", func() string {
			c := googleidtest.Claims(aud, "a@b.c")
			delete(c, "email_verified")
			return iss.Sign(t, "kid-1", c)
		}, googleid.ErrEmailNotVerified},
		{"no email", func() string {
			c := googleidtest.Claims(aud, "")
			return iss.Sign(t, "kid-1", c)
		}, googleid.ErrMissingClaim},
		{"no sub", func() string {
			c := googleidtest.Claims(aud, "a@b.c")
			delete(c, "sub")
			return iss.Sign(t, "kid-1", c)
		}, googleid.ErrMissingClaim},
		{"no exp", func() string {
			c := googleidtest.Claims(aud, "a@b.c")
			delete(c, "exp")
			return iss.Sign(t, "kid-1", c)
		}, googleid.ErrMissingClaim},
		{"iat in the future", func() string {
			c := googleidtest.Claims(aud, "a@b.c")
			c["iat"] = time.Now().Add(time.Hour).Unix()
			return iss.Sign(t, "kid-1", c)
		}, googleid.ErrNotYetValid},
		{"foreign key, known kid", func() string {
			return googleidtest.SignWith(t, googleidtest.ForeignKey(t), "kid-1", "RS256", googleidtest.Claims(aud, "a@b.c"))
		}, googleid.ErrSignature},
		{"alg none", func() string {
			return googleidtest.SignWith(t, googleidtest.ForeignKey(t), "kid-1", "none", googleidtest.Claims(aud, "a@b.c"))
		}, googleid.ErrAlgorithm},
		{"alg HS256", func() string {
			return googleidtest.SignWith(t, googleidtest.ForeignKey(t), "kid-1", "HS256", googleidtest.Claims(aud, "a@b.c"))
		}, googleid.ErrAlgorithm},
		{"tampered payload", func() string {
			tok := iss.Sign(t, "kid-1", googleidtest.Claims(aud, "a@b.c"))
			parts := strings.Split(tok, ".")
			c := googleidtest.Claims(aud, "admin@b.c")
			other := strings.Split(iss.Sign(t, "kid-1", c), ".")
			return parts[0] + "." + other[1] + "." + parts[2]
		}, googleid.ErrSignature},
		{"garbage", func() string { return "not.a.jwt" }, googleid.ErrMalformed},
		{"two segments", func() string { return "a.b" }, googleid.ErrMalformed},
		{"empty", func() string { return "" }, googleid.ErrMalformed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := v.Verify(ctx, tc.token())
			if !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
			var ge *googleid.Error
			if !errors.As(err, &ge) {
				t.Errorf("error is not *googleid.Error: %T", err)
			}
		})
	}
}

func TestJWKSCacheAndRotation(t *testing.T) {
	iss := googleidtest.New(t)
	now := time.Now()
	clock := func() time.Time { return now }
	v := newVerifier(t, iss, func(o *googleid.Options) {
		o.Now = clock
		o.MinRefetchInterval = time.Minute
	})
	ctx := context.Background()
	tok := func(kid string) string {
		c := googleidtest.Claims(aud, "a@b.c")
		c["iat"], c["exp"] = now.Unix(), now.Add(time.Hour).Unix()
		return iss.Sign(t, kid, c)
	}

	// First verify fetches; second uses the cache (max-age=3600).
	for i := 0; i < 2; i++ {
		if _, err := v.Verify(ctx, tok("kid-1")); err != nil {
			t.Fatal(err)
		}
	}
	if n := iss.Fetches.Load(); n != 1 {
		t.Fatalf("fetches after two verifies: %d", n)
	}

	// Unknown kid inside the throttle window: rejected without a refetch.
	iss.AddKey(t, "kid-2")
	if _, err := v.Verify(ctx, tok("kid-2")); !errors.Is(err, googleid.ErrUnknownKey) {
		t.Fatalf("kid-2 within throttle: %v", err)
	}
	if n := iss.Fetches.Load(); n != 1 {
		t.Fatalf("fetches after throttled unknown kid: %d", n)
	}

	// After the throttle window the unknown kid triggers a refetch and the
	// rotated key verifies.
	now = now.Add(2 * time.Minute)
	if _, err := v.Verify(ctx, tok("kid-2")); err != nil {
		t.Fatalf("kid-2 after rotation: %v", err)
	}
	if n := iss.Fetches.Load(); n != 2 {
		t.Fatalf("fetches after rotation: %d", n)
	}

	// A kid nobody publishes is still rejected after a (throttled-past) refetch.
	now = now.Add(2 * time.Minute)
	if _, err := v.Verify(ctx, foreignTok(t, "ghost", now)); !errors.Is(err, googleid.ErrUnknownKey) {
		t.Fatalf("ghost kid: %v", err)
	}
	if n := iss.Fetches.Load(); n != 3 {
		t.Fatalf("fetches after ghost: %d", n)
	}

	// Once max-age lapses the next verify refetches.
	now = now.Add(2 * time.Hour)
	if _, err := v.Verify(ctx, tok("kid-1")); err != nil {
		t.Fatal(err)
	}
	if n := iss.Fetches.Load(); n != 4 {
		t.Fatalf("fetches after expiry: %d", n)
	}
}

// foreignTok signs with a key the issuer never publishes but stamps kid on it.
func foreignTok(t *testing.T, kid string, now time.Time) string {
	c := googleidtest.Claims(aud, "a@b.c")
	c["iat"], c["exp"] = now.Unix(), now.Add(time.Hour).Unix()
	return googleidtest.SignWith(t, googleidtest.ForeignKey(t), kid, "RS256", c)
}

func TestJWKSFetchFailureKeepsOldKeys(t *testing.T) {
	iss := googleidtest.New(t)
	iss.MaxAge = 60 // minimum clamp
	now := time.Now()
	fail := false
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fail {
			http.Error(w, "boom", http.StatusBadGateway)
			return
		}
		resp, err := iss.Server.Client().Get(iss.URL())
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		defer resp.Body.Close()
		for k, vv := range resp.Header {
			w.Header()[k] = vv
		}
		w.WriteHeader(resp.StatusCode)
		buf := make([]byte, 1<<16)
		n, _ := resp.Body.Read(buf)
		_, _ = w.Write(buf[:n])
	}))
	t.Cleanup(proxy.Close)
	v := googleid.New(googleid.Options{ClientID: aud, JWKSURL: proxy.URL, HTTPClient: proxy.Client(), Now: func() time.Time { return now }})
	tok := func() string {
		c := googleidtest.Claims(aud, "a@b.c")
		c["iat"], c["exp"] = now.Unix(), now.Add(time.Hour).Unix()
		return iss.Sign(t, "kid-1", c)
	}
	if _, err := v.Verify(context.Background(), tok()); err != nil {
		t.Fatal(err)
	}
	// Cache lapses, upstream is down: the previous key set still serves.
	now = now.Add(5 * time.Minute)
	fail = true
	if _, err := v.Verify(context.Background(), tok()); err != nil {
		t.Fatalf("stale keys not used during outage: %v", err)
	}
	// With no cache at all an outage is a plain (non-*Error) failure.
	cold := googleid.New(googleid.Options{ClientID: aud, JWKSURL: proxy.URL, HTTPClient: proxy.Client()})
	_, err := cold.Verify(context.Background(), tok())
	var ge *googleid.Error
	if err == nil || errors.As(err, &ge) {
		t.Fatalf("cold outage: %v", err)
	}
}

func TestNewRequiresClientID(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("no panic")
		}
	}()
	googleid.New(googleid.Options{})
}
