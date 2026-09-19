package jobs_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/BeFeast/RehearseKit/internal/db/dbtest"
	"github.com/BeFeast/RehearseKit/internal/jobs"
)

func newJob(t *testing.T, store *jobs.Store, n int) *jobs.Job {
	t.Helper()
	id := fmt.Sprintf("00000000-0000-4000-8000-%012d", n)
	_, hash, err := jobs.NewClaimToken()
	if err != nil {
		t.Fatal(err)
	}
	j, err := store.Create(context.Background(), id, jobs.CreateParams{
		ClaimTokenHash: hash, ProjectName: fmt.Sprintf("job %d", n), InputType: jobs.InputUpload,
		Quality: jobs.QualityFast, ExpiresAt: time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	return j
}

func TestClaimConcurrentGetsDistinctJobs(t *testing.T) {
	pool := dbtest.Pool(t)
	store := jobs.NewStore(pool)
	ctx := context.Background()
	const n = 8
	for i := 1; i <= n; i++ {
		newJob(t, store, i)
	}

	var mu sync.Mutex
	got := map[string]int{}
	var wg sync.WaitGroup
	errs := make(chan error, 2*n)
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				j, err := jobs.Claim(ctx, pool)
				if errors.Is(err, jobs.ErrNoJobs) {
					return
				}
				if err != nil {
					errs <- err
					return
				}
				mu.Lock()
				got[j.ID]++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	if len(got) != n {
		t.Fatalf("claimed %d distinct jobs, want %d", len(got), n)
	}
	for id, c := range got {
		if c != 1 {
			t.Errorf("job %s claimed %d times", id, c)
		}
	}
	var converting int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE status = 'converting' AND started_at IS NOT NULL`).Scan(&converting); err != nil {
		t.Fatal(err)
	}
	if converting != n {
		t.Errorf("%d jobs converting, want %d", converting, n)
	}
	var events int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM job_events WHERE status = 'converting'`).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if events != n {
		t.Errorf("%d converting events, want %d", events, n)
	}
}

func TestClaimOrdersByCreation(t *testing.T) {
	pool := dbtest.Pool(t)
	store := jobs.NewStore(pool)
	ctx := context.Background()
	first := newJob(t, store, 1)
	time.Sleep(5 * time.Millisecond)
	newJob(t, store, 2)
	j, err := jobs.Claim(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	if j.ID != first.ID {
		t.Errorf("claimed %s, want oldest %s", j.ID, first.ID)
	}
}

func TestTransitionWritesEventsAndTerminalState(t *testing.T) {
	pool := dbtest.Pool(t)
	store := jobs.NewStore(pool)
	ctx := context.Background()
	j := newJob(t, store, 1)

	if err := jobs.Transition(ctx, pool, j.ID, jobs.StatusAnalyzing, 20, "tempo"); err != nil {
		t.Fatal(err)
	}
	if err := jobs.Transition(ctx, pool, j.ID, "bogus", 0, ""); !errors.Is(err, jobs.ErrInvalidStatus) {
		t.Errorf("bogus status: %v", err)
	}
	if err := jobs.Transition(ctx, pool, j.ID, jobs.StatusFailed, 150, "demucs exploded"); err != nil {
		t.Fatal(err)
	}
	got, err := store.Get(ctx, j.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != jobs.StatusFailed || got.StageProgress != 100 || got.Error == nil || *got.Error != "demucs exploded" || got.CompletedAt == nil || got.StartedAt == nil {
		t.Errorf("job after failure: %+v", got)
	}
	if err := jobs.Transition(ctx, pool, j.ID, jobs.StatusCompleted, 100, ""); !errors.Is(err, jobs.ErrTerminal) {
		t.Errorf("transition out of failed: %v", err)
	}
	if err := jobs.Cancel(ctx, pool, j.ID); !errors.Is(err, jobs.ErrTerminal) {
		t.Errorf("cancel finished job: %v", err)
	}
	evs, err := store.Events(ctx, j.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 3 || evs[0].Status != jobs.StatusPending || evs[1].Status != jobs.StatusAnalyzing || evs[2].Status != jobs.StatusFailed {
		t.Errorf("events: %+v", evs)
	}
	if err := jobs.Transition(ctx, pool, "00000000-0000-4000-8000-999999999999", jobs.StatusAnalyzing, 0, ""); !errors.Is(err, jobs.ErrNotFound) {
		t.Errorf("unknown job: %v", err)
	}
}

func TestBrokerDeliversNotify(t *testing.T) {
	pool := dbtest.Pool(t)
	store := jobs.NewStore(pool)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b := jobs.NewBroker(pool)
	go b.Run(ctx)
	select {
	case <-b.Ready():
	case <-time.After(5 * time.Second):
		t.Fatal("broker never became ready")
	}
	j := newJob(t, store, 1)
	other := newJob(t, store, 2)
	wake, unsub := b.Subscribe(j.ID)
	defer unsub()
	if err := jobs.Transition(ctx, pool, other.ID, jobs.StatusConverting, 1, ""); err != nil {
		t.Fatal(err)
	}
	select {
	case <-wake:
		t.Fatal("woken by another job's event")
	case <-time.After(200 * time.Millisecond):
	}
	if err := jobs.Transition(ctx, pool, j.ID, jobs.StatusConverting, 1, ""); err != nil {
		t.Fatal(err)
	}
	select {
	case <-wake:
	case <-time.After(5 * time.Second):
		t.Fatal("no wake-up after NOTIFY")
	}
}

func TestListFilters(t *testing.T) {
	pool := dbtest.Pool(t)
	store := jobs.NewStore(pool)
	ctx := context.Background()
	var owner string
	if err := pool.QueryRow(ctx, `INSERT INTO users (id, email, provider, status) VALUES (gen_random_uuid(), 'o@example.com', 'password', 'active') RETURNING id`).Scan(&owner); err != nil {
		t.Fatal(err)
	}
	mk := func(n int, status string) {
		id := fmt.Sprintf("00000000-0000-4000-8000-%012d", n)
		if _, err := store.Create(ctx, id, jobs.CreateParams{OwnerID: &owner, ProjectName: fmt.Sprintf("Song %d", n), InputType: jobs.InputUpload, Quality: jobs.QualityHigh, ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
			t.Fatal(err)
		}
		if status != jobs.StatusPending {
			if err := jobs.Transition(ctx, pool, id, status, 0, ""); err != nil {
				t.Fatal(err)
			}
		}
	}
	mk(1, jobs.StatusPending)
	mk(2, jobs.StatusSeparating)
	mk(3, jobs.StatusCompleted)
	mk(4, jobs.StatusFailed)
	mk(5, jobs.StatusCancelled)
	cases := map[string]int{"all": 5, "": 5, "active": 2, "completed": 1, "failed": 2}
	for status, want := range cases {
		items, total, err := store.List(ctx, owner, jobs.ListFilter{Status: status})
		if err != nil {
			t.Fatalf("%s: %v", status, err)
		}
		if len(items) != want || total != want {
			t.Errorf("status=%q: %d items / %d total, want %d", status, len(items), total, want)
		}
	}
	items, total, err := store.List(ctx, owner, jobs.ListFilter{Query: "song 3"})
	if err != nil || total != 1 || len(items) != 1 || items[0].ProjectName != "Song 3" {
		t.Errorf("q search: %v %d %v", items, total, err)
	}
	items, total, err = store.List(ctx, owner, jobs.ListFilter{Page: 2, PageSize: 2})
	if err != nil || total != 5 || len(items) != 2 {
		t.Errorf("page 2: %d items / %d total %v", len(items), total, err)
	}
	if _, _, err := store.List(ctx, owner, jobs.ListFilter{Status: "nope"}); !errors.Is(err, jobs.ErrInvalidStatus) {
		t.Errorf("bad status: %v", err)
	}
}
