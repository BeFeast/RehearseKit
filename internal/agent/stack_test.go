package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BeFeast/RehearseKit/internal/gpu"
	"github.com/BeFeast/RehearseKit/internal/models"
)

// The runner-side lock: a lease is checked against the job's stack, the
// runner's stack and the recipes the runner verified, before anything is
// downloaded or loaded.
func TestCheckLease(t *testing.T) {
	public := &Agent{cfg: Config{Stack: models.Public}, recipes: []string{"kim+scnet_xl_ihf", "scnet_xl_ihf"}}
	internal := &Agent{cfg: Config{Stack: models.Internal},
		recipes: []string{"htdemucs", "htdemucs_6s", "htdemucs_ft", "kim+bs_rofo_sw", "kim+scnet_xl_ihf", "scnet_xl_ihf"}}
	legacy := &Agent{} // pre-registry image: Demucs, no recipe list
	cases := []struct {
		name  string
		a     *Agent
		lease gpu.LeaseResponse
		ok    bool
	}{
		{"public job on public runner", public, gpu.LeaseResponse{Model: "kim+scnet_xl_ihf", Stack: models.Public}, true},
		{"public job never loads Demucs", internal, gpu.LeaseResponse{Model: "htdemucs_ft", Stack: models.Public}, false},
		{"public job never loads SW", internal, gpu.LeaseResponse{Model: "kim+bs_rofo_sw", Stack: models.Public}, false},
		{"public runner cannot take internal weights", public, gpu.LeaseResponse{Model: "htdemucs", Stack: models.Internal}, false},
		{"internal job on internal runner", internal, gpu.LeaseResponse{Model: "kim+bs_rofo_sw", Stack: models.Internal}, true},
		{"public job on internal runner, public recipe", internal, gpu.LeaseResponse{Model: "scnet_xl_ihf", Stack: models.Public}, true},
		{"unknown recipe", internal, gpu.LeaseResponse{Model: "mdx_extra", Stack: models.Internal}, false},
		{"transcribe on a public job", internal, gpu.LeaseResponse{Model: "kim+scnet_xl_ihf", Stack: models.Public, Transcribe: true}, false},
		{"old server, Demucs, legacy runner", legacy, gpu.LeaseResponse{Model: "htdemucs_6s"}, true},
		{"legacy runner cannot run MSST", legacy, gpu.LeaseResponse{Model: "scnet_xl_ihf", Stack: models.Public}, false},
	}
	for _, c := range cases {
		_, err := c.a.checkLease(c.lease)
		if (err == nil) != c.ok {
			t.Errorf("%s: err=%v, want ok=%v", c.name, err, c.ok)
		}
	}
	notInstalled := &Agent{cfg: Config{Stack: models.Public}, recipes: []string{"kim+scnet_xl_ihf"}}
	if _, err := notInstalled.checkLease(gpu.LeaseResponse{Model: "scnet_xl_ihf", Stack: models.Public}); err == nil ||
		!strings.Contains(err.Error(), "not installed") {
		t.Errorf("uninstalled recipe: %v", err)
	}
}

func TestNewScansModels(t *testing.T) {
	base := Config{APIURL: "http://127.0.0.1:18080", Token: "t", ModelsDir: t.TempDir()}

	c := base
	c.Stack = models.Public
	if _, err := New(c); err == nil || !strings.Contains(err.Error(), "no separation recipe") {
		t.Errorf("empty models dir: %v", err)
	}

	c.Transcribe = TranscribeConfig{Enabled: true, ToolsDir: "/x"}
	if _, err := New(c); err == nil || !strings.Contains(err.Error(), "internal") {
		t.Errorf("transcribe on a public runner: %v", err)
	}

	// Any internal checkpoint inside a public image stops the runner.
	ht, _ := models.CheckpointByID("htdemucs")
	p := filepath.Join(base.ModelsDir, filepath.FromSlash(ht.Path))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	c = base
	c.Stack = models.Public
	if _, err := New(c); err == nil || !strings.Contains(err.Error(), "present on a public runner") {
		t.Errorf("internal weights in a public image: %v", err)
	}
	// On an internal runner the same file is checked, and a bad hash fails.
	c.Stack = models.Internal
	if _, err := New(c); err == nil || !strings.Contains(err.Error(), "size") {
		t.Errorf("corrupt checkpoint: %v", err)
	}
}

func TestChildEnvEgressLock(t *testing.T) {
	a := &Agent{}
	if env := a.childEnv(); env != nil {
		t.Errorf("no egress dir: %v", env)
	}
	a.cfg.EgressDir = "/opt/rk/tools/egress"
	env := strings.Join(a.childEnv(), " ")
	if !strings.Contains(env, "RK_EGRESS_LOCK=1") || !strings.Contains(env, "PYTHONPATH=/opt/rk/tools/egress") {
		t.Errorf("childEnv = %s", env)
	}
}
