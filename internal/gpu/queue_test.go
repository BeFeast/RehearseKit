package gpu_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/BeFeast/RehearseKit/internal/gpu"
	"github.com/BeFeast/RehearseKit/internal/jobs"
)

func TestQueueStats(t *testing.T) {
	e := newEnv(t, time.Minute)
	ctx := context.Background()

	get := func(tok string) (*http.Response, gpu.QueueStats) {
		t.Helper()
		req, _ := http.NewRequest(http.MethodGet, e.ts.URL+"/api/v1/gpu/queue", nil)
		if tok != "" {
			req.Header.Set("Authorization", "Bearer "+tok)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var st gpu.QueueStats
		_ = json.NewDecoder(resp.Body).Decode(&st)
		return resp, st
	}

	if resp, _ := get(""); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no token: %d", resp.StatusCode)
	}
	if resp, st := get(token); resp.StatusCode != http.StatusOK || st.Waiting != 0 || st.ActiveLeases != 0 || st.OldestWaitingAt != nil {
		t.Fatalf("empty queue: %d %+v", resp.StatusCode, st)
	}

	j1 := e.separatingJob(1, jobs.QualityFast)
	j2 := e.separatingJob(2, jobs.QualityFast)
	st, err := e.store.QueueStats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if st.Waiting != 2 || st.ActiveLeases != 0 || st.OldestWaitingAt == nil {
		t.Fatalf("two waiting: %+v", st)
	}

	// A claimed job leaves the waiting set and shows as an active lease.
	l, _, err := e.store.Claim(ctx, "r1", gpu.Capabilities{})
	if err != nil {
		t.Fatal(err)
	}
	if _, st = get(token); st.Waiting != 1 || st.ActiveLeases != 1 {
		t.Fatalf("after claim: %+v", st)
	}
	// A failed lease puts the job back into the waiting set.
	if _, err := e.store.Fail(ctx, l.ID, "r1", "boom"); err != nil {
		t.Fatal(err)
	}
	if _, st = get(token); st.Waiting != 2 || st.ActiveLeases != 0 {
		t.Fatalf("after fail: %+v", st)
	}
	// Jobs that used up their attempts are not waiting.
	e.store.MaxFailures = 1
	if _, st = get(token); st.Waiting != 1 || st.ActiveLeases != 0 {
		t.Fatalf("with attempts exhausted for %s: %+v (other %s)", j1.ID, st, j2.ID)
	}
}
