package gpu_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/BeFeast/RehearseKit/internal/gpu"
	"github.com/BeFeast/RehearseKit/internal/jobs"
	"github.com/BeFeast/RehearseKit/internal/models"
	"github.com/BeFeast/RehearseKit/internal/pipeline/wavtest"
)

// stackJob creates a job on a stack and walks it to `separating`.
func (e *env) stackJob(stack, quality string) *jobs.Job {
	e.t.Helper()
	id := randomID(e.t)
	j, err := e.jobs.Create(context.Background(), id, jobs.CreateParams{
		ProjectName: stack + " " + quality, InputType: jobs.InputUpload, Quality: quality, Stack: stack,
		ExpiresAt: time.Now().Add(time.Hour),
	})
	if err != nil {
		e.t.Fatal(err)
	}
	dir, _ := e.layout.JobDir(id)
	_ = os.MkdirAll(dir, 0o755)
	if _, err := wavtest.Write(filepath.Join(dir, "source.wav"), wavtest.Options{Seconds: 0.1}); err != nil {
		e.t.Fatal(err)
	}
	for _, st := range []string{jobs.StatusConverting, jobs.StatusAnalyzing, jobs.StatusSeparating} {
		if err := jobs.Transition(context.Background(), e.pool, id, st, jobs.StageStart(st), st); err != nil {
			e.t.Fatal(err)
		}
	}
	return j
}

var (
	publicRunner   = gpu.Capabilities{Stack: models.Public, Recipes: []string{"kim+scnet_xl_ihf", "scnet_xl_ihf"}}
	internalRunner = gpu.Capabilities{Stack: models.Internal,
		Recipes: []string{"htdemucs", "htdemucs_6s", "htdemucs_ft", "kim+bs_rofo_sw", "kim+scnet_xl_ihf", "scnet_xl_ihf"}}
)

// A runner is only offered jobs whose recipe it verified; a public runner
// never gets an internal job, and a pre-registry runner only Demucs jobs.
func TestClaimByRecipeAndStack(t *testing.T) {
	e := newEnv(t, time.Minute)
	ctx := context.Background()
	internalJob := e.stackJob(models.Internal, jobs.QualityHiFi)
	st, err := e.store.QueueStats(ctx)
	if err != nil || st.Waiting != 1 || st.WaitingInternal != 1 {
		t.Fatalf("stats with an internal job: %+v %v", st, err)
	}

	if _, _, err := e.store.Claim(ctx, "pub", publicRunner); !errors.Is(err, gpu.ErrNoJobs) {
		t.Fatalf("public runner claimed an internal job: %v", err)
	}
	// Even a public runner that claims to have the recipe is refused by stack.
	lying := gpu.Capabilities{Stack: models.Public, Recipes: internalRunner.Recipes}
	if _, _, err := e.store.Claim(ctx, "liar", lying); !errors.Is(err, gpu.ErrNoJobs) {
		t.Fatalf("public-stack runner claimed an internal job: %v", err)
	}
	if _, _, err := e.store.Claim(ctx, "old", gpu.Capabilities{}); !errors.Is(err, gpu.ErrNoJobs) {
		t.Fatalf("pre-registry runner claimed an SW job: %v", err)
	}
	l, j, err := e.store.Claim(ctx, "int", internalRunner)
	if err != nil || j.ID != internalJob.ID {
		t.Fatalf("internal runner: %v %v", j, err)
	}
	var reports []gpu.StemReport
	for _, n := range models.SixStems {
		reports = append(reports, gpu.StemReport{Name: n})
	}
	if err := e.store.Complete(ctx, l.ID, "int", reports, nil, nil, nil); err != nil {
		t.Fatalf("complete 6 stems: %v", err)
	}

	publicJob := e.stackJob(models.Public, jobs.QualityHiFi)
	st, _ = e.store.QueueStats(ctx)
	if st.Waiting != 1 || st.WaitingInternal != 0 {
		t.Fatalf("stats with a public job: %+v", st)
	}
	if _, _, err := e.store.Claim(ctx, "old", gpu.Capabilities{}); !errors.Is(err, gpu.ErrNoJobs) {
		t.Fatalf("pre-registry runner claimed a public job: %v", err)
	}
	l, j, err = e.store.Claim(ctx, "pub", publicRunner)
	if err != nil || j.ID != publicJob.ID {
		t.Fatalf("public runner: %v %v", j, err)
	}
	// A public 4-stem HiFi job completes with exactly four stems.
	reports = nil
	for _, n := range models.FourStems {
		reports = append(reports, gpu.StemReport{Name: n})
	}
	if err := e.store.Complete(ctx, l.ID, "pub", reports, nil, nil, nil); err != nil {
		t.Fatalf("complete 4 stems: %v", err)
	}
}

// Over HTTP the lease carries the job's stack and recipe, and the headers
// decide what the runner is offered.
func TestLeaseHeaders(t *testing.T) {
	e := newEnv(t, time.Minute)
	j := e.stackJob(models.Public, jobs.QualityHigh)
	lease := func(recipes, stack string) (int, gpu.LeaseResponse) {
		req, _ := http.NewRequest(http.MethodPost, e.ts.URL+"/api/v1/gpu/lease", bytes.NewReader([]byte(`{"runner_id":"r"}`)))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		if recipes != "" {
			req.Header.Set(gpu.RecipesHeader, recipes)
		}
		if stack != "" {
			req.Header.Set(gpu.StackHeader, stack)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var l gpu.LeaseResponse
		_ = json.NewDecoder(resp.Body).Decode(&l)
		return resp.StatusCode, l
	}
	if code, _ := lease("", ""); code != http.StatusNoContent {
		t.Fatalf("pre-registry runner offered a public job: %d", code)
	}
	if code, _ := lease("kim+scnet_xl_ihf", "public"); code != http.StatusNoContent {
		t.Fatalf("runner without scnet_xl_ihf offered it: %d", code)
	}
	code, l := lease("kim+scnet_xl_ihf,scnet_xl_ihf", "public")
	if code != http.StatusOK || l.JobID != j.ID || l.Model != "scnet_xl_ihf" || l.Stack != models.Public || len(l.Stems) != 4 {
		t.Fatalf("lease: %d %+v", code, l)
	}
}
