package agent

import (
	"testing"

	"github.com/BeFeast/RehearseKit/internal/gpu"
)

func TestRebaseURL(t *testing.T) {
	got, err := rebaseURL("http://127.0.0.1:18080", "https://rk.example.com/api/v1/signed/jobs/abc/source?exp=1&sig=x%2By")
	if err != nil || got != "http://127.0.0.1:18080/api/v1/signed/jobs/abc/source?exp=1&sig=x%2By" {
		t.Fatalf("%q %v", got, err)
	}
	if got, err := rebaseURL("http://127.0.0.1:18080", "https://rk.example.com/p"); err != nil || got != "http://127.0.0.1:18080/p" {
		t.Fatalf("no query: %q %v", got, err)
	}
	if _, err := rebaseURL("http://127.0.0.1:18080", "https://rk.example.com"); err == nil {
		t.Fatal("URL without a path accepted")
	}
}

func TestNewValidatesSignedURLBase(t *testing.T) {
	base := Config{APIURL: "http://127.0.0.1:18080", Token: "t"}
	for _, bad := range []string{"127.0.0.1:18080", "not a url", "/relative"} {
		c := base
		c.SignedURLBase = bad
		if _, err := New(c); err == nil {
			t.Errorf("SignedURLBase %q accepted", bad)
		}
	}
	c := base
	c.SignedURLBase = "http://127.0.0.1:18080/"
	a, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	lease := gpu.LeaseResponse{
		SourceURL:  "https://rk.example.com/api/v1/signed/jobs/j/source?exp=9&sig=s",
		UploadURLs: map[string]string{"vocals": "https://rk.example.com/api/v1/signed/jobs/j/stems/vocals?exp=9&sig=v"},
	}
	if err := a.rebase(&lease); err != nil {
		t.Fatal(err)
	}
	if lease.SourceURL != "http://127.0.0.1:18080/api/v1/signed/jobs/j/source?exp=9&sig=s" ||
		lease.UploadURLs["vocals"] != "http://127.0.0.1:18080/api/v1/signed/jobs/j/stems/vocals?exp=9&sig=v" {
		t.Fatalf("rebased lease %+v", lease)
	}
	// Loopback API URL: rebased onto it by default.
	a2, _ := New(base)
	l2 := gpu.LeaseResponse{SourceURL: "https://rk.example.com/x?exp=1&sig=s"}
	_ = a2.rebase(&l2)
	if l2.SourceURL != "http://127.0.0.1:18080/x?exp=1&sig=s" {
		t.Fatalf("loopback default: %q", l2.SourceURL)
	}
	// Public API URL: left alone unless asked.
	pub := Config{APIURL: "https://rk.example.com", Token: "t"}
	a3, _ := New(pub)
	l3 := gpu.LeaseResponse{SourceURL: "https://cdn.example.com/x"}
	_ = a3.rebase(&l3)
	if l3.SourceURL != "https://cdn.example.com/x" {
		t.Fatal("rebased a public API URL without being asked")
	}
	pub.RebaseSignedURLs = true
	a4, _ := New(pub)
	l4 := gpu.LeaseResponse{SourceURL: "https://cdn.example.com/x"}
	_ = a4.rebase(&l4)
	if l4.SourceURL != "https://rk.example.com/x" {
		t.Fatalf("explicit rebase: %q", l4.SourceURL)
	}
}

func TestAPIIsLoopback(t *testing.T) {
	for api, want := range map[string]bool{
		"http://127.0.0.1:18080": true, "http://localhost:8080": true, "http://[::1]:8080": true,
		"http://10.10.0.15:30080": false, "https://rk.example.com": false, "": false,
	} {
		if got := apiIsLoopback(api); got != want {
			t.Errorf("apiIsLoopback(%q) = %v", api, got)
		}
	}
}
