package scaler

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/BeFeast/RehearseKit/internal/gpu"
)

type fakeVast struct {
	offers     []Offer
	instances  []Instance
	credit     float64
	creditErr  error
	createErr  error
	destroyErr error
	showErr    error
	nextID     int64

	created   []RentSpec
	destroyed []int64
	searches  int
}

func (f *fakeVast) SearchOffers(context.Context, string) ([]Offer, error) {
	f.searches++
	return f.offers, nil
}

func (f *fakeVast) CreateInstance(_ context.Context, offerID int64, spec RentSpec) (int64, error) {
	if f.createErr != nil {
		return 0, f.createErr
	}
	f.nextID++
	f.created = append(f.created, spec)
	f.instances = append(f.instances, Instance{ID: f.nextID, Status: "loading", Label: spec.Label, DPH: 0.06, GPU: "RTX 3060",
		SSHHost: "ssh2.vast.ai", SSHPort: 10000 + int(f.nextID), PublicIP: "1.2.3.4", DirectSSHPort: 50000 + int(f.nextID)})
	_ = offerID
	return f.nextID, nil
}

func (f *fakeVast) ShowInstances(context.Context) ([]Instance, error) {
	if f.showErr != nil {
		return nil, f.showErr
	}
	return append([]Instance(nil), f.instances...), nil
}

func (f *fakeVast) DestroyInstance(_ context.Context, id int64) error {
	if f.destroyErr != nil {
		return f.destroyErr
	}
	f.destroyed = append(f.destroyed, id)
	for i, in := range f.instances {
		if in.ID == id {
			f.instances = append(f.instances[:i], f.instances[i+1:]...)
			break
		}
	}
	return nil
}

func (f *fakeVast) Credit(context.Context) (float64, error) { return f.credit, f.creditErr }

func (f *fakeVast) setStatus(id int64, status string) {
	for i := range f.instances {
		if f.instances[i].ID == id {
			f.instances[i].Status = status
		}
	}
}

type fakeQueue struct {
	st  gpu.QueueStats
	err error
}

func (q *fakeQueue) Stats(context.Context) (gpu.QueueStats, error) { return q.st, q.err }

type fakeTunnel struct {
	eps      []Endpoint
	ensures  int
	stops    int
	verified bool
}

func (t *fakeTunnel) Ensure(eps []Endpoint) {
	t.ensures++
	t.eps = eps
}
func (t *fakeTunnel) Stop() { t.stops++; t.eps = nil }
func (t *fakeTunnel) Status() TunnelStatus {
	return TunnelStatus{Running: t.eps != nil, Verified: t.verified && t.eps != nil}
}

type harness struct {
	t     *testing.T
	s     *Scaler
	vast  *fakeVast
	queue *fakeQueue
	tun   *fakeTunnel
	clock time.Time
	dir   string
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	h := &harness{t: t, dir: t.TempDir(), clock: time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)}
	h.vast = &fakeVast{offers: []Offer{{ID: 100, DPH: 0.05}, {ID: 101, DPH: 0.06, DirectPorts: 10, GPU: "RTX 3060"}}, credit: 28,
		instances: []Instance{{ID: 49973633, Status: "running", Label: "someone-elses-project"}}}
	h.queue = &fakeQueue{}
	h.tun = &fakeTunnel{}
	h.reopen()
	return h
}

// reopen builds a fresh Scaler over the same state dir, like a restart.
func (h *harness) reopen() {
	h.t.Helper()
	s, err := New(Config{
		Interval: time.Second,
		Limits:   Limits{Idle: 10 * time.Minute, MaxAge: 6 * time.Hour, BootTimeout: 15 * time.Minute, MinCredit: 5, RentCooldown: 2 * time.Minute},
		Image:    "ghcr.io/example/runner:latest", Login: "-u u -p p ghcr.io", Token: "tok", StateDir: h.dir, TunnelPort: 18080,
	}, h.vast, h.queue, h.tun)
	if err != nil {
		h.t.Fatal(err)
	}
	s.now = func() time.Time { return h.clock }
	s.log = slog.New(slog.NewTextHandler(io.Discard, nil))
	h.s = s
}

func (h *harness) tick(advance time.Duration) {
	h.clock = h.clock.Add(advance)
	h.s.Tick(context.Background())
}

func (h *harness) instanceID() int64 {
	if h.s.state.Instance == nil {
		return 0
	}
	return h.s.state.Instance.ID
}

func TestRentTunnelIdleDestroy(t *testing.T) {
	h := newHarness(t)

	// Empty queue: nothing happens, the foreign instance is untouched.
	h.tick(0)
	if h.instanceID() != 0 || len(h.vast.created) != 0 || len(h.vast.destroyed) != 0 {
		t.Fatalf("idle tick rented/destroyed: %+v", h.vast)
	}

	// A job waits: rent the cheapest offer with direct ports.
	h.queue.st = gpu.QueueStats{Waiting: 1}
	h.tick(30 * time.Second)
	if h.instanceID() != 1 || len(h.vast.created) != 1 {
		t.Fatalf("expected one rented instance, state %+v", h.s.state)
	}
	spec := h.vast.created[0]
	if spec.Image != "ghcr.io/example/runner:latest" || spec.Login == "" || spec.Label != DefaultLabel || spec.DiskGB != 30 {
		t.Fatalf("spec %+v", spec)
	}
	if !strings.Contains(spec.Env, "RK_API_URL=http://127.0.0.1:18080") || !strings.Contains(spec.Env, "RK_SIGNED_URL_BASE=http://127.0.0.1:18080") || !strings.Contains(spec.Env, "RK_RUNNER_TOKEN=tok") {
		t.Fatalf("env %q", spec.Env)
	}
	if !strings.Contains(spec.OnStart, "127.0.0.1:18080/healthz") || !strings.Contains(spec.OnStart, "rk gpu-agent") {
		t.Fatalf("onstart %q", spec.OnStart)
	}
	if h.s.state.Instance.OfferID != 101 {
		t.Fatalf("offer %d, want 101 (direct ports)", h.s.state.Instance.OfferID)
	}
	// State was persisted.
	if _, err := os.Stat(filepath.Join(h.dir, "state.json")); err != nil {
		t.Fatal(err)
	}

	// Still loading: no tunnel yet, nothing else rented.
	h.tick(30 * time.Second)
	if h.tun.ensures != 0 || len(h.vast.created) != 1 {
		t.Fatalf("tunnel ensured while loading (%d) or second rent (%d)", h.tun.ensures, len(h.vast.created))
	}

	// Running: tunnel to the direct endpoint first, proxy as fallback.
	h.vast.setStatus(1, "running")
	h.tick(30 * time.Second)
	if h.tun.ensures != 1 || len(h.tun.eps) != 2 || h.tun.eps[0] != (Endpoint{"1.2.3.4", 50001}) {
		t.Fatalf("tunnel %+v", h.tun)
	}
	if h.s.state.Instance.RunningAt.IsZero() {
		t.Fatal("RunningAt not recorded")
	}
	h.tun.verified = true

	// The runner picked the job up: active lease, nothing waiting.
	h.queue.st = gpu.QueueStats{Waiting: 0, ActiveLeases: 1}
	h.tick(30 * time.Second)
	if h.s.state.Instance.FirstWork.IsZero() || h.s.state.Instance.TunnelAt.IsZero() {
		t.Fatalf("timings not recorded: %+v", h.s.state.Instance)
	}
	if !h.s.state.IdleSince.IsZero() {
		t.Fatal("idle timer running while busy")
	}

	// Queue drained: idle timer starts, instance stays for 10 minutes.
	h.queue.st = gpu.QueueStats{}
	h.tick(30 * time.Second)
	if h.s.state.IdleSince.IsZero() || h.instanceID() != 1 {
		t.Fatalf("idle timer not started: %+v", h.s.state)
	}
	h.tick(9 * time.Minute)
	if h.instanceID() != 1 || len(h.vast.destroyed) != 0 {
		t.Fatal("destroyed before idle timeout")
	}
	h.tick(time.Minute)
	if h.instanceID() != 0 || len(h.vast.destroyed) != 1 || h.vast.destroyed[0] != 1 {
		t.Fatalf("not destroyed after idle: state %+v destroyed %v", h.s.state, h.vast.destroyed)
	}
	if h.tun.stops == 0 || h.tun.eps != nil {
		t.Fatal("tunnel not stopped on destroy")
	}
	if n := len(h.s.state.History); n != 1 || h.s.state.History[0].Reason == "" || h.s.state.History[0].EstCostUSD <= 0 {
		t.Fatalf("history %+v", h.s.state.History)
	}
	// The foreign instance was never touched.
	for _, id := range h.vast.destroyed {
		if id == 49973633 {
			t.Fatal("destroyed a foreign instance")
		}
	}
}

func TestCreditGuardAndCooldown(t *testing.T) {
	h := newHarness(t)
	h.queue.st = gpu.QueueStats{Waiting: 1}
	h.vast.credit = 4
	h.tick(0)
	if len(h.vast.created) != 0 {
		t.Fatal("rented below the credit floor")
	}
	h.vast.credit = 28
	h.vast.createErr = errors.New("no such offer")
	h.tick(30 * time.Second)
	if h.instanceID() != 0 || h.s.state.LastRentFailure.IsZero() {
		t.Fatalf("rent failure not recorded: %+v", h.s.state)
	}
	h.vast.createErr = nil
	searches := h.vast.searches
	h.tick(30 * time.Second)
	if h.vast.searches != searches {
		t.Fatal("searched offers during rent cooldown")
	}
	h.tick(2 * time.Minute)
	if h.instanceID() == 0 {
		t.Fatal("did not rent after the cooldown")
	}
}

func TestMaxAgeReRents(t *testing.T) {
	h := newHarness(t)
	h.queue.st = gpu.QueueStats{Waiting: 1, ActiveLeases: 1}
	h.tick(0)
	h.vast.setStatus(1, "running")
	h.tick(5*time.Hour + 59*time.Minute)
	if h.instanceID() != 1 {
		t.Fatal("destroyed before max age")
	}
	h.tick(time.Minute)
	if h.instanceID() != 0 || len(h.vast.destroyed) != 1 {
		t.Fatalf("max age not enforced: %+v", h.s.state)
	}
	h.tick(30 * time.Second)
	if h.instanceID() != 2 {
		t.Fatalf("did not re-rent for the waiting job: %+v", h.s.state)
	}
}

func TestBootTimeout(t *testing.T) {
	h := newHarness(t)
	h.queue.st = gpu.QueueStats{Waiting: 1}
	h.tick(0)
	h.tick(14 * time.Minute)
	if h.instanceID() != 1 {
		t.Fatal("destroyed before boot timeout")
	}
	h.tick(2 * time.Minute)
	if h.instanceID() != 0 || len(h.vast.destroyed) != 1 {
		t.Fatalf("instance stuck in loading was not destroyed: %+v", h.s.state)
	}
}

func TestRestartReattachesAndDestroysOrphans(t *testing.T) {
	h := newHarness(t)
	h.queue.st = gpu.QueueStats{Waiting: 1}
	h.tick(0)
	h.vast.setStatus(1, "running")
	h.tick(30 * time.Second)

	// A second labelled instance appears (say, a crashed scaler rented it).
	h.vast.instances = append(h.vast.instances, Instance{ID: 77, Status: "running", Label: DefaultLabel})
	// Restart: same state dir, new process.
	h.tun = &fakeTunnel{}
	h.reopen()
	h.queue.st = gpu.QueueStats{ActiveLeases: 1}
	h.tick(30 * time.Second)
	if h.instanceID() != 1 {
		t.Fatalf("did not reattach to instance 1: %+v", h.s.state)
	}
	if len(h.vast.destroyed) != 1 || h.vast.destroyed[0] != 77 {
		t.Fatalf("orphan not destroyed: %v", h.vast.destroyed)
	}
	if h.tun.ensures != 1 {
		t.Fatal("tunnel not re-established after restart")
	}
}

func TestLostStateAdoptsLabelledInstance(t *testing.T) {
	h := newHarness(t)
	h.vast.instances = append(h.vast.instances, Instance{ID: 55, Status: "running", Label: DefaultLabel, StartDate: h.clock.Add(-time.Hour), DPH: 0.07})
	h.queue.st = gpu.QueueStats{}
	h.tick(0)
	in := h.s.state.Instance
	if in == nil || in.ID != 55 || !in.Adopted || !in.RentedAt.Equal(h.clock.Add(-time.Hour)) {
		t.Fatalf("not adopted: %+v", in)
	}
	if len(h.vast.created) != 0 {
		t.Fatal("rented while adopting")
	}
	// Adopted and idle: destroyed after the idle timeout like any other.
	h.tick(10 * time.Minute)
	if h.instanceID() != 0 || len(h.vast.destroyed) != 1 || h.vast.destroyed[0] != 55 {
		t.Fatalf("adopted instance not destroyed when idle: %+v %v", h.s.state, h.vast.destroyed)
	}
}

func TestInstanceGoneIsForgotten(t *testing.T) {
	h := newHarness(t)
	h.queue.st = gpu.QueueStats{Waiting: 1}
	h.tick(0)
	// vast lost it (host went offline and the contract ended).
	h.vast.instances = h.vast.instances[:1]
	h.tick(30 * time.Second)
	if h.instanceID() != 0 || len(h.vast.destroyed) != 0 || len(h.s.state.History) != 1 {
		t.Fatalf("not forgotten: %+v destroyed %v", h.s.state, h.vast.destroyed)
	}
	// And re-rents for the waiting job on the next tick.
	h.tick(30 * time.Second)
	if h.instanceID() != 2 {
		t.Fatalf("did not re-rent: %+v", h.s.state)
	}
}

func TestVastOutageDoesNothing(t *testing.T) {
	h := newHarness(t)
	h.queue.st = gpu.QueueStats{Waiting: 1}
	h.vast.showErr = errors.New("502")
	h.tick(0)
	if len(h.vast.created) != 0 {
		t.Fatal("rented without seeing the instance list")
	}
	h.vast.showErr = nil
	h.tick(30 * time.Second)
	h.vast.setStatus(1, "running")
	h.queue.st = gpu.QueueStats{}
	h.tick(30 * time.Second)
	h.tick(10 * time.Minute) // idle threshold reached...
	if h.instanceID() != 0 {
		t.Fatal("expected idle destroy")
	}
	// ...but a destroy failure keeps the state for a retry.
	h.queue.st = gpu.QueueStats{Waiting: 1}
	h.tick(30 * time.Second)
	h.vast.setStatus(2, "running")
	h.queue.st = gpu.QueueStats{}
	h.vast.destroyErr = errors.New("api down")
	h.tick(30 * time.Second)
	h.tick(10 * time.Minute)
	if h.instanceID() != 2 {
		t.Fatal("state dropped although destroy failed")
	}
	h.vast.destroyErr = nil
	h.tick(30 * time.Second)
	if h.instanceID() != 0 {
		t.Fatal("destroy not retried")
	}
}

func TestQueueErrorKeepsInstance(t *testing.T) {
	h := newHarness(t)
	h.queue.st = gpu.QueueStats{Waiting: 1}
	h.tick(0)
	h.vast.setStatus(1, "running")
	h.queue.st = gpu.QueueStats{}
	h.tick(30 * time.Second) // idle timer starts
	h.queue.err = errors.New("db down")
	h.tick(30 * time.Minute)
	if h.instanceID() != 1 {
		t.Fatal("destroyed on unknown queue")
	}
	h.queue.err = nil
	h.tick(30 * time.Second)
	if h.instanceID() != 0 {
		t.Fatal("idle timer did not resume")
	}
}

func TestFallbackQueue(t *testing.T) {
	api := &fakeQueue{err: ErrNoQueueEndpoint}
	dbq := &fakeQueue{st: gpu.QueueStats{Waiting: 3}}
	q := &FallbackQueue{API: api, DB: dbq}
	st, err := q.Stats(context.Background())
	if err != nil || st.Waiting != 3 || !q.useDB {
		t.Fatalf("fallback: %+v %v useDB=%v", st, err, q.useDB)
	}
	api.err = nil
	api.st = gpu.QueueStats{Waiting: 9}
	if st, _ := q.Stats(context.Background()); st.Waiting != 3 {
		t.Fatal("switched back to the API after falling back")
	}
	noDB := &FallbackQueue{API: &fakeQueue{err: ErrNoQueueEndpoint}}
	if _, err := noDB.Stats(context.Background()); !errors.Is(err, ErrNoQueueEndpoint) {
		t.Fatalf("without DB: %v", err)
	}
}
