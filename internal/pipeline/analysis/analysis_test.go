package analysis

import (
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	r, err := Parse([]byte(`{"version":1,"grid":{"beats":[0.5,1.0,1.5],"downbeats":[0.5],"source":"beat_this"},
		"sections":null,"instruments":{"guitar":{"status":"ok","adapter":"hf_midi","notes":12},"drums":{"status":"failed","reason":"timeout"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if r.Grid == nil || len(r.Grid.Beats) != 3 || r.Instruments["drums"].Status != StatusFailed {
		t.Fatalf("%+v", r)
	}
	if _, err := Parse([]byte(`{"version":2}`)); err == nil {
		t.Fatal("wrong version accepted")
	}
	if _, err := Parse([]byte(`{"version":1,"grid":{"beats":[1,0.5]}}`)); err == nil {
		t.Fatal("descending beats accepted")
	}
	if _, err := Parse([]byte(`{"version":1,"instruments":{"x":{"status":"weird"}}}`)); err == nil {
		t.Fatal("bad status accepted")
	}
	r, err = Parse([]byte(`{"version":1,"grid":null,"grid_error":"too few beats","instruments":{}}`))
	if err != nil || r.Grid != nil || !strings.Contains(r.GridError, "few") {
		t.Fatalf("null grid: %v %+v", err, r)
	}
}

func TestParseNotes(t *testing.T) {
	n, err := ParseNotes([]byte(`{"stem":"bass","notes":[{"onset":2,"offset":2.5,"pitch":40,"velocity":0.8},{"onset":1,"offset":1.2,"pitch":43,"velocity":1}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if n.Notes[0].Onset != 1 {
		t.Fatal("notes should be sorted by onset")
	}
	for _, bad := range []string{
		`{"notes":[{"onset":1,"offset":0.5,"pitch":40,"velocity":0.5}]}`,
		`{"notes":[{"onset":1,"offset":2,"pitch":200,"velocity":0.5}]}`,
		`{"notes":[{"onset":1,"offset":2,"pitch":40,"velocity":1.5}]}`,
	} {
		if _, err := ParseNotes([]byte(bad)); err == nil {
			t.Fatalf("accepted %s", bad)
		}
	}
}
