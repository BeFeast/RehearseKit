package legacy

import (
	"strings"
	"testing"

	"github.com/BeFeast/RehearseKit/internal/auth"
	"github.com/BeFeast/RehearseKit/internal/jobs"
)

func TestMapStatus(t *testing.T) {
	cases := []struct {
		in, status, errPrefix string
		bad                   bool
	}{
		{in: "COMPLETED", status: jobs.StatusCompleted},
		{in: "completed", status: jobs.StatusCompleted},
		{in: " FAILED ", status: jobs.StatusFailed},
		{in: "CANCELLED", status: jobs.StatusCancelled},
		{in: "PENDING", status: jobs.StatusFailed, errPrefix: "legacy job was PENDING"},
		{in: "CONVERTING", status: jobs.StatusFailed, errPrefix: "legacy job was CONVERTING"},
		{in: "ANALYZING", status: jobs.StatusFailed, errPrefix: "legacy job was ANALYZING"},
		{in: "SEPARATING", status: jobs.StatusFailed, errPrefix: "legacy job was SEPARATING"},
		{in: "FINALIZING", status: jobs.StatusFailed, errPrefix: "legacy job was FINALIZING"},
		{in: "PACKAGING", status: jobs.StatusFailed, errPrefix: "legacy job was PACKAGING"},
		{in: "DONE", bad: true},
		{in: "", bad: true},
	}
	for _, c := range cases {
		status, errText, err := MapStatus(c.in)
		if c.bad {
			if err == nil {
				t.Errorf("MapStatus(%q): expected error, got %q", c.in, status)
			}
			continue
		}
		if err != nil {
			t.Errorf("MapStatus(%q): %v", c.in, err)
			continue
		}
		if status != c.status {
			t.Errorf("MapStatus(%q) = %q, want %q", c.in, status, c.status)
		}
		if !jobs.ValidStatus(status) {
			t.Errorf("MapStatus(%q) produced invalid status %q", c.in, status)
		}
		if c.errPrefix == "" && errText != "" {
			t.Errorf("MapStatus(%q): unexpected error text %q", c.in, errText)
		}
		if c.errPrefix != "" && !strings.HasPrefix(errText, c.errPrefix) {
			t.Errorf("MapStatus(%q): error text %q, want prefix %q", c.in, errText, c.errPrefix)
		}
	}
}

func TestMapQuality(t *testing.T) {
	for in, want := range map[string]string{"fast": jobs.QualityFast, "high": jobs.QualityHigh, "HIGH": jobs.QualityHigh, " fast ": jobs.QualityFast} {
		got, err := MapQuality(in)
		if err != nil || got != want || !jobs.ValidQuality(got) {
			t.Errorf("MapQuality(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"", "high6", "ultra"} {
		if got, err := MapQuality(in); err == nil {
			t.Errorf("MapQuality(%q) = %q, want error", in, got)
		}
	}
}

func TestMapInputType(t *testing.T) {
	for in, want := range map[string]string{"upload": jobs.InputUpload, "youtube": jobs.InputYouTube, "YouTube": jobs.InputYouTube} {
		if got, err := MapInputType(in); err != nil || got != want {
			t.Errorf("MapInputType(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := MapInputType("spotify"); err == nil {
		t.Error("MapInputType(spotify): want error")
	}
}

func TestMapUser(t *testing.T) {
	if MapUserRole(true) != auth.RoleAdmin || MapUserRole(false) != auth.RoleUser {
		t.Error("MapUserRole")
	}
	if MapUserStatus(true) != auth.StatusActive || MapUserStatus(false) != auth.StatusPending {
		t.Error("MapUserStatus")
	}
	google, email, empty := "google", "email", ""
	if MapProvider(&google) != auth.ProviderGoogle {
		t.Error("MapProvider(google)")
	}
	for _, p := range []*string{nil, &email, &empty} {
		if MapProvider(p) != auth.ProviderPassword {
			t.Errorf("MapProvider(%v) want password", p)
		}
	}
}

func TestClampText(t *testing.T) {
	s := strings.Repeat("é", 100) // 2 bytes each
	got := clampText(s, 7)
	if got != strings.Repeat("é", 3) {
		t.Errorf("clampText cut inside a rune: %q", got)
	}
	if clampText("short", 200) != "short" {
		t.Error("clampText should not touch short strings")
	}
}
