package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"
)

// The cross-origin isolation pair (COOP same-origin + COEP credentialless)
// gives the player SharedArrayBuffer but also blocks the Google sign-in
// popup, so it must be sent for the job page only.
func TestSPAIsolationHeadersScopedToJobPage(t *testing.T) {
	dist := fstest.MapFS{
		"index.html":          {Data: []byte("<html>RehearseKit</html>")},
		"assets/app.js":       {Data: []byte("//js")},
		"stream-processor.js": {Data: []byte("//worklet")},
	}
	h := spaHandler(dist)
	cases := []struct {
		path     string
		isolated bool
	}{
		{"/", false},
		{"/index.html", false},
		{"/jobs", false},
		{"/jobs/", false},
		{"/jobs?signin=1&next=%2Fjobs%2Fabc", false},
		{"/jobs/abc", true},
		{"/jobs/aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee", true},
		{"/jobs/abc/", true}, // path.Clean drops the trailing slash
		{"/jobs/abc/stems", false},
		{"/jobsx", false},
		{"/profile", false},
		{"/admin/users", false},
		{"/assets/app.js", false},
		{"/stream-processor.js", false},
	}
	for _, c := range cases {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, c.path, nil))
		if rec.Code != http.StatusOK {
			t.Errorf("%s: status %d", c.path, rec.Code)
		}
		coop := rec.Header().Get("Cross-Origin-Opener-Policy")
		coep := rec.Header().Get("Cross-Origin-Embedder-Policy")
		if c.isolated && (coop != "same-origin" || coep != "credentialless") {
			t.Errorf("%s: want isolation headers, got COOP=%q COEP=%q", c.path, coop, coep)
		}
		if !c.isolated && (coop != "" || coep != "") {
			t.Errorf("%s: unexpected isolation headers COOP=%q COEP=%q", c.path, coop, coep)
		}
	}
}
