// Package scaler is `rk gpu-scaler`: an on-demand launcher for one vast.ai
// GPU runner. It watches the separation queue, rents the cheapest matching
// offer with the runner image when jobs wait, keeps a reverse ssh tunnel
// into the instance so the runner reaches `rk serve` on a LAN, and destroys
// the instance once the queue has been empty for a while. Caps: one
// instance, a maximum age, a credit floor.
package scaler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Config for the scaler.
type Config struct {
	Interval time.Duration // tick; default 30 s
	Limits   Limits

	Image      string // runner image
	Login      string // vast --login for a private registry
	OfferQuery string // vastai search offers query
	DiskGB     int
	Label      string // label on instances we create; also how orphans are recognised
	RunnerEnv  string // extra "-e K=V" for the runner container

	TunnelPort int    // port bound on the instance loopback; the runner's RK_API_URL
	Token      string // RK_RUNNER_TOKEN handed to the runner

	StateDir string
}

// DefaultOfferQuery is the vast.ai search used unless RK_SCALER_OFFER_QUERY is set.
const DefaultOfferQuery = "gpu_ram>=12 num_gpus=1 dph<0.35 reliability>0.95 inet_down>200 disk_space>=30 cuda_vers>=12.1 rentable=true"

// DefaultLabel marks instances created by the scaler.
const DefaultLabel = "rk-gpu-scaler"

// destroyGrace is how long after a destroy an instance that vast still
// lists is treated as "destroy again" rather than as a foreign orphan or an
// adoptable leftover (vast's listing lags, and a destroy can be refused).
const destroyGrace = time.Hour

// StateInstance is the instance we are paying for.
type StateInstance struct {
	ID        int64     `json:"id"`
	OfferID   int64     `json:"offer_id,omitempty"`
	GPU       string    `json:"gpu,omitempty"`
	Geo       string    `json:"geo,omitempty"`
	DPH       float64   `json:"dph,omitempty"`
	RentedAt  time.Time `json:"rented_at"`
	RunningAt time.Time `json:"running_at,omitzero"`
	TunnelAt  time.Time `json:"tunnel_at,omitzero"`
	FirstWork time.Time `json:"first_work_at,omitzero"`
	Adopted   bool      `json:"adopted,omitempty"`
}

// Record is a finished instance kept for the log.
type Record struct {
	StateInstance
	DestroyedAt time.Time `json:"destroyed_at"`
	Reason      string    `json:"reason"`
	EstCostUSD  float64   `json:"est_cost_usd"`
}

// State is persisted after every change so a restart reattaches (or destroys
// an orphan) instead of paying for a forgotten instance.
type State struct {
	Instance        *StateInstance `json:"instance,omitempty"`
	IdleSince       time.Time      `json:"idle_since,omitzero"`
	LastRentFailure time.Time      `json:"last_rent_failure,omitzero"`
	History         []Record       `json:"history,omitempty"`
	UpdatedAt       time.Time      `json:"updated_at"`
}

// Scaler runs the loop.
type Scaler struct {
	cfg    Config
	vast   Vast
	queue  Queue
	tunnel Tunnel
	log    *slog.Logger
	now    func() time.Time

	state State
}

// New builds a scaler. tunnel may be nil when the runner can reach the API
// directly (RK_API_URL public), in which case no ssh is started.
func New(cfg Config, v Vast, q Queue, t Tunnel) (*Scaler, error) {
	if cfg.Interval <= 0 {
		cfg.Interval = 30 * time.Second
	}
	if cfg.Limits.Idle <= 0 {
		cfg.Limits.Idle = 10 * time.Minute
	}
	if cfg.Limits.MaxAge <= 0 {
		cfg.Limits.MaxAge = 6 * time.Hour
	}
	if cfg.Limits.BootTimeout <= 0 {
		cfg.Limits.BootTimeout = 15 * time.Minute
	}
	if cfg.Limits.RentCooldown <= 0 {
		cfg.Limits.RentCooldown = 2 * time.Minute
	}
	if cfg.Image == "" {
		return nil, errors.New("scaler: image is required")
	}
	if cfg.OfferQuery == "" {
		cfg.OfferQuery = DefaultOfferQuery
	}
	if cfg.Label == "" {
		cfg.Label = DefaultLabel
	}
	if cfg.DiskGB <= 0 {
		cfg.DiskGB = 30
	}
	if cfg.TunnelPort <= 0 {
		cfg.TunnelPort = 18080
	}
	if cfg.Token == "" {
		return nil, errors.New("scaler: runner token is required")
	}
	if cfg.StateDir == "" {
		return nil, errors.New("scaler: state dir is required")
	}
	if err := os.MkdirAll(cfg.StateDir, 0o700); err != nil {
		return nil, err
	}
	s := &Scaler{cfg: cfg, vast: v, queue: q, tunnel: t, log: slog.Default(), now: time.Now}
	if err := s.loadState(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Scaler) statePath() string { return filepath.Join(s.cfg.StateDir, "state.json") }

func (s *Scaler) loadState() error {
	b, err := os.ReadFile(s.statePath())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, &s.state); err != nil {
		return fmt.Errorf("scaler: state file %s: %w", s.statePath(), err)
	}
	return nil
}

func (s *Scaler) saveState() {
	s.state.UpdatedAt = s.now()
	if n := len(s.state.History); n > 50 {
		s.state.History = s.state.History[n-50:]
	}
	b, err := json.MarshalIndent(s.state, "", "  ")
	if err != nil {
		s.log.Error("scaler: encode state", "err", err)
		return
	}
	tmp := s.statePath() + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		s.log.Error("scaler: write state", "err", err)
		return
	}
	if err := os.Rename(tmp, s.statePath()); err != nil {
		s.log.Error("scaler: write state", "err", err)
	}
}

// State returns a copy of the persisted state (tests, status output).
func (s *Scaler) State() State { return s.state }

// Run ticks until ctx is cancelled. The tunnel is stopped on return; the
// instance is left running so a restart of the scaler reattaches to it.
func (s *Scaler) Run(ctx context.Context) error {
	s.log.Info("rk gpu-scaler", "interval", s.cfg.Interval, "idle", s.cfg.Limits.Idle, "max_age", s.cfg.Limits.MaxAge,
		"min_credit", s.cfg.Limits.MinCredit, "image", s.cfg.Image, "label", s.cfg.Label, "state", s.statePath())
	if s.state.Instance != nil {
		s.log.Info("scaler: reattaching to instance from state", "instance", s.state.Instance.ID, "rented_at", s.state.Instance.RentedAt)
	}
	defer func() {
		if s.tunnel != nil {
			s.tunnel.Stop()
		}
	}()
	for {
		s.Tick(ctx)
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(s.cfg.Interval):
		}
	}
}

// Tick runs one observe → decide → act cycle.
func (s *Scaler) Tick(ctx context.Context) {
	now := s.now()
	o := Observation{Now: now, HaveInstance: s.state.Instance != nil, LastRentFailure: s.state.LastRentFailure}

	st, err := s.queue.Stats(ctx)
	if err != nil {
		s.log.Error("scaler: queue", "err", err)
	} else {
		o.QueueOK, o.Waiting, o.ActiveLeases = true, st.Waiting, st.ActiveLeases
	}

	instances, err := s.vast.ShowInstances(ctx)
	if err != nil {
		// Without vast's view we can neither rent nor safely destroy.
		s.log.Error("scaler: show instances", "err", err)
		return
	}
	s.reconcile(ctx, instances)
	if s.state.Instance != nil {
		o.HaveInstance, o.RentedAt = true, s.state.Instance.RentedAt
		for i := range instances {
			if instances[i].ID == s.state.Instance.ID {
				o.Listed = &instances[i]
			}
		}
		s.track(o.Listed, st.Waiting, st.ActiveLeases, o.QueueOK)
	}
	if !o.HaveInstance && o.QueueOK && o.Waiting > 0 {
		credit, err := s.vast.Credit(ctx)
		if err != nil {
			s.log.Error("scaler: credit", "err", err)
		} else {
			o.CreditOK, o.Credit = true, credit
		}
	}

	d := Decide(o, Memory{IdleSince: s.state.IdleSince}, s.cfg.Limits)
	if d.Memory.IdleSince != s.state.IdleSince {
		s.state.IdleSince = d.Memory.IdleSince
		s.saveState()
	}
	attrs := []any{"waiting", o.Waiting, "active", o.ActiveLeases, "queue_ok", o.QueueOK, "reason", d.Reason}
	if s.state.Instance != nil {
		attrs = append(attrs, "instance", s.state.Instance.ID, "age", now.Sub(s.state.Instance.RentedAt).Round(time.Second))
		if o.Listed != nil {
			attrs = append(attrs, "status", o.Listed.Status)
		}
		if s.tunnel != nil {
			ts := s.tunnel.Status()
			attrs = append(attrs, "tunnel_up", ts.Running, "tunnel_ok", ts.Verified)
		}
	}
	if d.Action != ActNone {
		s.log.Info("scaler: "+d.Action.String(), attrs...)
	} else {
		s.log.Debug("scaler: tick", attrs...)
	}

	switch d.Action {
	case ActRent:
		s.rent(ctx, o)
	case ActDestroy:
		s.destroy(ctx, d.Reason)
	case ActForget:
		s.forget(d.Reason)
	}
	s.ensureTunnel(o.Listed)
}

// recentlyDestroyed reports whether id was destroyed within destroyGrace.
func (s *Scaler) recentlyDestroyed(id int64) bool {
	now := s.now()
	for i := len(s.state.History) - 1; i >= 0; i-- {
		r := s.state.History[i]
		if r.ID == id && now.Sub(r.DestroyedAt) < destroyGrace {
			return true
		}
	}
	return false
}

// reconcile handles instances carrying our label that the state does not
// know. One we destroyed recently but vast still lists is destroyed again.
// Otherwise, with no instance in the state, a single one is adopted (a lost
// state file must not leave a GPU billing); anything else is an orphan and
// is destroyed. Instances without our label are never touched.
func (s *Scaler) reconcile(ctx context.Context, instances []Instance) {
	var ours []Instance
	for _, in := range instances {
		if in.Label != s.cfg.Label {
			continue
		}
		if s.recentlyDestroyed(in.ID) && (s.state.Instance == nil || in.ID != s.state.Instance.ID) {
			s.log.Warn("scaler: destroyed instance is still listed; destroying again", "instance", in.ID, "status", in.Status)
			if err := s.vast.DestroyInstance(ctx, in.ID); err != nil {
				s.log.Error("scaler: destroy again", "instance", in.ID, "err", err)
			}
			continue
		}
		ours = append(ours, in)
	}
	if s.state.Instance == nil && len(ours) == 1 {
		in := ours[0]
		s.log.Warn("scaler: adopting instance with our label that is not in the state file", "instance", in.ID, "status", in.Status)
		rented := in.StartDate
		if rented.IsZero() {
			rented = s.now()
		}
		s.state.Instance = &StateInstance{ID: in.ID, GPU: in.GPU, DPH: in.DPH, RentedAt: rented, Adopted: true}
		s.saveState()
		return
	}
	for _, in := range ours {
		if s.state.Instance != nil && in.ID == s.state.Instance.ID {
			continue
		}
		s.log.Warn("scaler: destroying orphan instance with our label", "instance", in.ID, "status", in.Status)
		if err := s.vast.DestroyInstance(ctx, in.ID); err != nil {
			s.log.Error("scaler: destroy orphan", "instance", in.ID, "err", err)
		}
	}
}

// track records boot / tunnel / first-work timestamps for the report.
func (s *Scaler) track(listed *Instance, waiting, active int, queueOK bool) {
	in := s.state.Instance
	changed := false
	if listed != nil && listed.Running() && in.RunningAt.IsZero() {
		in.RunningAt = s.now()
		in.GPU, in.DPH = listed.GPU, listed.DPH
		changed = true
		s.log.Info("scaler: instance running", "instance", in.ID, "gpu", in.GPU, "dph", in.DPH, "boot", in.RunningAt.Sub(in.RentedAt).Round(time.Second), "ssh", listed.SSHEndpoints())
	}
	if s.tunnel != nil && in.TunnelAt.IsZero() && s.tunnel.Status().Verified {
		in.TunnelAt = s.now()
		changed = true
		s.log.Info("scaler: tunnel verified", "instance", in.ID, "since_rent", in.TunnelAt.Sub(in.RentedAt).Round(time.Second))
	}
	if queueOK && active > 0 && in.FirstWork.IsZero() {
		in.FirstWork = s.now()
		changed = true
		s.log.Info("scaler: first lease active", "instance", in.ID, "since_rent", in.FirstWork.Sub(in.RentedAt).Round(time.Second), "waiting", waiting)
	}
	if changed {
		s.saveState()
	}
}

func (s *Scaler) ensureTunnel(listed *Instance) {
	if s.tunnel == nil {
		return
	}
	if s.state.Instance == nil || listed == nil || !listed.Running() {
		if s.state.Instance == nil {
			s.tunnel.Stop()
		}
		return
	}
	s.tunnel.Ensure(listed.SSHEndpoints())
}

// onStart is the script vast runs in the container: wait until the tunnel
// answers on the loopback port, then run the agent (logs in /var/log).
func (s *Scaler) onStart() string {
	return fmt.Sprintf(`X="${VAST_CONTAINERLABEL:-$(hostname)}"; export RK_RUNNER_ID="vast-${X#C.}"; `+
		`nohup bash -c 'until curl -sf -o /dev/null http://127.0.0.1:%d/healthz; do sleep 3; done; exec rk gpu-agent' >> /var/log/rk-gpu-agent.log 2>&1 &`, s.cfg.TunnelPort)
}

func (s *Scaler) runnerEnv() string {
	// RK_SIGNED_URL_BASE: the server builds signed URLs from its public URL,
	// which the runner cannot reach; the agent rebases them onto the tunnel.
	env := fmt.Sprintf("-e RK_API_URL=http://127.0.0.1:%d -e RK_SIGNED_URL_BASE=http://127.0.0.1:%d -e RK_RUNNER_TOKEN=%s", s.cfg.TunnelPort, s.cfg.TunnelPort, s.cfg.Token)
	if extra := strings.TrimSpace(s.cfg.RunnerEnv); extra != "" {
		env += " " + extra
	}
	return env
}

// pickOffer prefers offers with direct ports (the tunnel skips vast's ssh
// proxy) and falls back to the cheapest.
func pickOffer(offers []Offer) (Offer, bool) {
	for _, o := range offers {
		if o.DirectPorts > 0 {
			return o, true
		}
	}
	if len(offers) > 0 {
		return offers[0], true
	}
	return Offer{}, false
}

func (s *Scaler) rent(ctx context.Context, o Observation) {
	fail := func(err error) {
		s.log.Error("scaler: rent failed", "err", err)
		s.state.LastRentFailure = s.now()
		s.saveState()
	}
	offers, err := s.vast.SearchOffers(ctx, s.cfg.OfferQuery)
	if err != nil {
		fail(err)
		return
	}
	offer, ok := pickOffer(offers)
	if !ok {
		fail(fmt.Errorf("no offer matches %q", s.cfg.OfferQuery))
		return
	}
	s.log.Info("scaler: renting", "offer", offer.ID, "gpu", offer.GPU, "dph", offer.DPH, "geo", offer.Geo, "reliability", offer.Reliability, "direct_ports", offer.DirectPorts, "waiting", o.Waiting, "credit", o.Credit)
	id, err := s.vast.CreateInstance(ctx, offer.ID, RentSpec{
		Image: s.cfg.Image, Login: s.cfg.Login, Label: s.cfg.Label, DiskGB: s.cfg.DiskGB,
		Env: s.runnerEnv(), OnStart: s.onStart(),
	})
	if err != nil {
		fail(err)
		return
	}
	// Persist before anything else: a crash here must not orphan the instance.
	s.state.Instance = &StateInstance{ID: id, OfferID: offer.ID, GPU: offer.GPU, Geo: offer.Geo, DPH: offer.DPH, RentedAt: s.now()}
	s.state.IdleSince = time.Time{}
	s.state.LastRentFailure = time.Time{}
	s.saveState()
	s.log.Info("scaler: rented", "instance", id, "offer", offer.ID, "gpu", offer.GPU, "dph", offer.DPH)
}

func (s *Scaler) destroy(ctx context.Context, reason string) {
	in := s.state.Instance
	if in == nil {
		return
	}
	if err := s.vast.DestroyInstance(ctx, in.ID); err != nil {
		// Keep the state; the next tick retries.
		s.log.Error("scaler: destroy failed", "instance", in.ID, "err", err)
		return
	}
	s.close(reason)
	s.log.Info("scaler: destroyed", "instance", in.ID, "reason", reason, "lifetime", s.now().Sub(in.RentedAt).Round(time.Second), "est_cost_usd", fmt.Sprintf("%.4f", s.state.History[len(s.state.History)-1].EstCostUSD))
}

func (s *Scaler) forget(reason string) {
	in := s.state.Instance
	if in == nil {
		return
	}
	s.close(reason)
	s.log.Warn("scaler: forgot instance", "instance", in.ID, "reason", reason)
}

func (s *Scaler) close(reason string) {
	in := s.state.Instance
	now := s.now()
	s.state.History = append(s.state.History, Record{
		StateInstance: *in, DestroyedAt: now, Reason: reason,
		EstCostUSD: in.DPH * now.Sub(in.RentedAt).Hours(),
	})
	s.state.Instance = nil
	s.state.IdleSince = time.Time{}
	s.saveState()
	if s.tunnel != nil {
		s.tunnel.Stop()
	}
}
