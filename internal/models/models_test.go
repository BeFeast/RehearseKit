package models

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// The shipped manifest: every public-stack quality resolves to a recipe
// that loads public checkpoints only, and the internal stack keeps the
// presets it had before the split plus 6-stem HiFi.
func TestManifestPublicClosure(t *testing.T) {
	if len(Qualities(Public)) == 0 {
		t.Fatal("public stack offers nothing")
	}
	for _, q := range Qualities(Public) {
		rc, ok := RecipeFor(Public, q)
		if !ok {
			t.Fatalf("public %s: no recipe", q)
		}
		if rc.Class() != Public {
			t.Errorf("public %s → %s is class %s", q, rc.ID, rc.Class())
		}
		for _, id := range rc.Checkpoints {
			c, _ := CheckpointByID(id)
			if c.Class != Public {
				t.Errorf("public %s → %s loads %s (%s, %s)", q, rc.ID, id, c.Class, c.Licence)
			}
			if strings.Contains(strings.ToLower(c.Licence), "nc") || c.Licence == "unknown" || c.Licence == "research-only" {
				t.Errorf("public %s → %s loads %s under %q", q, rc.ID, id, c.Licence)
			}
		}
		if rc.Engine == EngineDemucs {
			t.Errorf("public %s runs demucs (%s)", q, rc.ID)
		}
		if len(rc.Credits()) == 0 {
			t.Errorf("public %s → %s has no attribution", q, rc.ID)
		}
		for _, c := range rc.Credits() {
			if strings.Contains(c, "internal use") {
				t.Errorf("public %s → %s credits %q", q, rc.ID, c)
			}
		}
	}
	for _, q := range []string{"fast", "high6"} {
		if _, ok := RecipeFor(Public, q); ok {
			t.Errorf("public stack still offers %s", q)
		}
	}
	for q, want := range map[string]int{"fast": 4, "high": 4, "high6": 6, "hifi": 6} {
		rc, ok := RecipeFor(Internal, q)
		if !ok || rc.StemCount != want {
			t.Errorf("internal %s: %+v ok=%v, want %d stems", q, rc, ok, want)
		}
	}
	if rc, _ := RecipeFor(Public, "hifi"); rc.StemCount != 4 {
		t.Errorf("public hifi: %d stems, want 4", rc.StemCount)
	}
}

// Every checkpoint an internal-only model family ships as is classed internal.
func TestManifestClasses(t *testing.T) {
	for _, c := range Checkpoints() {
		internalOnly := strings.HasPrefix(c.ID, "htdemucs") || strings.HasPrefix(c.ID, "bs_rofo_sw") ||
			c.ID == "adtof" || strings.HasPrefix(c.ID, "hfmidi")
		if internalOnly && c.Class != Internal {
			t.Errorf("%s is %s, want internal", c.ID, c.Class)
		}
	}
}

func TestLoadRejectsPublicStackOnInternalWeights(t *testing.T) {
	bad := `{"checkpoints":[{"id":"w","class":"internal","licence":"unknown","path":"w","size":1,"sha256":"` +
		strings.Repeat("0", 64) + `","url":"https://x"}],
		"recipes":[{"id":"r","engine":"msst","stems":4,"checkpoints":["w"]}],
		"stacks":{"public":{"high":"r"}}}`
	if _, err := load([]byte(bad)); err == nil || !strings.Contains(err.Error(), "stack public") {
		t.Fatalf("load accepted a public stack on internal weights: %v", err)
	}
	unpinned := `{"checkpoints":[{"id":"w","class":"public","licence":"MIT","path":"w","size":1,"sha256":"","url":"https://x"}],"recipes":[],"stacks":{}}`
	if _, err := load([]byte(unpinned)); err == nil {
		t.Fatal("load accepted a checkpoint without sha256")
	}
}

func TestCheck(t *testing.T) {
	cases := []struct {
		recipe, job, runner string
		ok                  bool
	}{
		{"scnet_xl_ihf", Public, Public, true},
		{"kim+scnet_xl_ihf", Public, Internal, true},
		{"htdemucs_ft", Internal, Internal, true},
		{"htdemucs_ft", Public, Internal, false}, // a public job never loads research-only weights
		{"kim+bs_rofo_sw", Public, Public, false},
		{"kim+bs_rofo_sw", Internal, Public, false}, // the public image cannot run it either
		{"nope", Internal, Internal, false},
		{"scnet_xl_ihf", "", Public, false},
	}
	for _, c := range cases {
		_, err := Check(c.recipe, c.job, c.runner)
		if (err == nil) != c.ok {
			t.Errorf("Check(%s, job %s, runner %s): err=%v, want ok=%v", c.recipe, c.job, c.runner, err, c.ok)
		}
	}
}

func testRegistry(t *testing.T, files map[string]string) (registry, string) {
	t.Helper()
	dir := t.TempDir()
	var cps []string
	for name, class := range files {
		body := []byte("weights of " + name)
		sum := sha256.Sum256(body)
		cps = append(cps, `{"id":"`+name+`","class":"`+class+`","licence":"x","path":"m/`+name+`","size":`+
			strconv.Itoa(len(body))+`,"sha256":"`+hex.EncodeToString(sum[:])+`","url":"https://x/`+name+`"}`)
		if err := os.MkdirAll(filepath.Join(dir, "m"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "m", name), body, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	m := `{"checkpoints":[` + strings.Join(cps, ",") + `],
		"recipes":[{"id":"pub","engine":"msst","stems":4,"checkpoints":["a"]},
		           {"id":"int","engine":"msst","stems":6,"checkpoints":["a","b"]}],
		"stacks":{"public":{"high":"pub"},"internal":{"hifi":"int"}}}`
	r, err := load([]byte(m))
	if err != nil {
		t.Fatal(err)
	}
	return r, dir
}

func TestScan(t *testing.T) {
	r, dir := testRegistry(t, map[string]string{"a": Public, "b": Internal})

	inv, err := r.scan(dir, Internal)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(inv.Recipes, ",") != "int,pub" {
		t.Errorf("internal runner recipes = %v", inv.Recipes)
	}

	// An internal checkpoint inside a public image is refused outright.
	if _, err := r.scan(dir, Public); err == nil || !strings.Contains(err.Error(), "present on a public runner") {
		t.Errorf("public scan with internal weights: %v", err)
	}
	if err := os.Remove(filepath.Join(dir, "m", "b")); err != nil {
		t.Fatal(err)
	}
	inv, err = r.scan(dir, Public)
	if err != nil || strings.Join(inv.Recipes, ",") != "pub" {
		t.Errorf("public scan: %v %v", inv.Recipes, err)
	}

	// A file whose bytes changed fails verification.
	if err := os.WriteFile(filepath.Join(dir, "m", "a"), []byte("weights of X"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := r.scan(dir, Public); err == nil || !strings.Contains(err.Error(), "sha256") {
		t.Errorf("tampered file: %v", err)
	}
}
