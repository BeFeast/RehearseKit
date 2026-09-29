package jobs

import "testing"

func TestStageProgressBands(t *testing.T) {
	cases := []struct {
		status string
		frac   float64
		want   int16
	}{
		{StatusConverting, 0, 0}, {StatusConverting, 1, 14},
		{StatusAnalyzing, 0, 14}, {StatusAnalyzing, 0.5, 21}, {StatusAnalyzing, 1, 28},
		{StatusSeparating, 0, 28}, {StatusSeparating, 0.5, 52}, {StatusSeparating, 1, 76},
		{StatusFinalizing, 0, 76}, {StatusFinalizing, 1, 89},
		{StatusPackaging, 0, 89}, {StatusPackaging, 1, 99},
		{StatusCompleted, 0, 100}, {StatusCompleted, 1, 100},
		{StatusSeparating, -1, 28}, {StatusSeparating, 2, 76},
	}
	for _, c := range cases {
		if got := StageProgress(c.status, c.frac); got != c.want {
			t.Errorf("StageProgress(%s, %v) = %d, want %d", c.status, c.frac, got, c.want)
		}
	}
	// Bands are contiguous.
	order := []string{StatusConverting, StatusAnalyzing, StatusSeparating, StatusFinalizing, StatusPackaging}
	for i := 1; i < len(order); i++ {
		if StageEnd(order[i-1]) != StageStart(order[i]) {
			t.Errorf("%s ends at %d but %s starts at %d", order[i-1], StageEnd(order[i-1]), order[i], StageStart(order[i]))
		}
	}
}

func TestStatusCopyMatchesLegacy(t *testing.T) {
	// Verbatim from frontend/components/job-card.tsx.
	want := map[string]string{
		StatusPending:    "Queued for processing...",
		StatusConverting: "Converting audio to WAV format...",
		StatusAnalyzing:  "Analyzing tempo and detecting BPM...",
		StatusFinalizing: "Embedding metadata into stems...",
		StatusPackaging:  "Creating download package...",
		"bogus":          "Processing...",
	}
	for st, msg := range want {
		if got := StatusMessage(st, 0); got != msg {
			t.Errorf("StatusMessage(%s) = %q, want %q", st, got, msg)
		}
	}
	if StatusMessage(StatusSeparating, 30) != "Loading AI model..." ||
		StatusMessage(StatusSeparating, 40) != "Separating stems with AI (this takes 2-5 minutes)..." ||
		StatusMessage(StatusSeparating, 70) != "Finalizing stem separation..." {
		t.Error("separating messages")
	}
	if StatusDetails(StatusSeparating) != "Separating vocals, drums, bass, and other instruments" || StatusDetails("x") != "" {
		t.Error("details")
	}
}

func TestJobRecipeLegacyRows(t *testing.T) {
	for q, want := range map[string]string{QualityFast: "htdemucs", QualityHigh: "htdemucs_ft", QualityHigh6: "htdemucs_6s", "garbage": "htdemucs"} {
		j := &Job{Quality: q}
		if r := j.Recipe(); r.ID != want {
			t.Errorf("legacy row %s → %s, want %s", q, r.ID, want)
		}
	}
	j := &Job{Quality: QualityHiFi, Stack: "public", Model: "kim+scnet_xl_ihf"}
	if r := j.Recipe(); r.ID != "kim+scnet_xl_ihf" || len(r.Stems()) != 4 {
		t.Errorf("recipe = %+v", r)
	}
}
