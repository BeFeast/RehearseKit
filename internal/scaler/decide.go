package scaler

import (
	"fmt"
	"time"
)

// Limits are the caps the decision applies.
type Limits struct {
	Idle         time.Duration // destroy after this long with nothing waiting and no active lease
	MaxAge       time.Duration // destroy (and re-rent if needed) after this long regardless
	BootTimeout  time.Duration // destroy an instance that never reaches `running`
	MinCredit    float64       // do not rent below this account credit
	RentCooldown time.Duration // wait after a failed rent before trying again
}

// Observation is everything one tick knows.
type Observation struct {
	Now time.Time

	// Queue.
	QueueOK      bool // Waiting and ActiveLeases are fresh
	Waiting      int
	ActiveLeases int

	// Our instance, if the state file has one.
	HaveInstance bool
	RentedAt     time.Time
	// Listed is vast's view of that instance; nil when vast no longer lists it.
	Listed *Instance

	// Account credit, when it was fetched this tick.
	CreditOK bool
	Credit   float64

	LastRentFailure time.Time
}

// Memory is the little state the decision carries between ticks.
type Memory struct {
	IdleSince time.Time // when waiting and active leases were last seen at zero; zero = not idle
}

// Action is what the tick should do.
type Action int

// Actions.
const (
	ActNone    Action = iota // keep going
	ActRent                  // rent a new instance
	ActDestroy               // destroy our instance
	ActForget                // drop the state: vast no longer lists our instance
)

func (a Action) String() string {
	switch a {
	case ActRent:
		return "rent"
	case ActDestroy:
		return "destroy"
	case ActForget:
		return "forget"
	default:
		return "none"
	}
}

// Decision is Decide's output.
type Decision struct {
	Action Action
	Reason string
	Memory Memory
}

// Decide is the pure scaling policy: at most one instance, rent when jobs
// wait, destroy when idle for Limits.Idle, cap age, never rent below
// MinCredit, back off after a failed rent. It never destroys on stale
// queue data (only on age). Memory is returned unchanged on destroy/forget
// so a failed destroy is retried on the next tick instead of restarting
// the idle timer; the caller clears it once the instance is gone.
func Decide(o Observation, m Memory, l Limits) Decision {
	if o.HaveInstance {
		return decideWithInstance(o, m, l)
	}
	m.IdleSince = time.Time{}
	if !o.QueueOK {
		return Decision{ActNone, "queue unknown", m}
	}
	if o.Waiting == 0 {
		return Decision{ActNone, "nothing waiting", m}
	}
	if !o.LastRentFailure.IsZero() && o.Now.Sub(o.LastRentFailure) < l.RentCooldown {
		return Decision{ActNone, fmt.Sprintf("rent cooldown after failure %s ago", o.Now.Sub(o.LastRentFailure).Round(time.Second)), m}
	}
	if !o.CreditOK {
		return Decision{ActNone, "credit unknown; not renting", m}
	}
	if o.Credit < l.MinCredit {
		return Decision{ActNone, fmt.Sprintf("credit guard: $%.2f < $%.2f", o.Credit, l.MinCredit), m}
	}
	return Decision{ActRent, fmt.Sprintf("%d job(s) waiting", o.Waiting), m}
}

func decideWithInstance(o Observation, m Memory, l Limits) Decision {
	if o.Listed == nil {
		return Decision{ActForget, "instance no longer listed by vast", m}
	}
	age := o.Now.Sub(o.RentedAt)
	if l.MaxAge > 0 && age >= l.MaxAge {
		return Decision{ActDestroy, fmt.Sprintf("max age %s reached (age %s)", l.MaxAge, age.Round(time.Second)), m}
	}
	if !o.Listed.Running() && l.BootTimeout > 0 && age >= l.BootTimeout {
		return Decision{ActDestroy, fmt.Sprintf("not running after %s (status %q: %s)", age.Round(time.Second), o.Listed.Status, o.Listed.StatusMsg), m}
	}
	if !o.QueueOK {
		return Decision{ActNone, "queue unknown; keeping the instance", m}
	}
	if o.Waiting > 0 || o.ActiveLeases > 0 {
		m.IdleSince = time.Time{}
		return Decision{ActNone, fmt.Sprintf("busy: %d waiting, %d active", o.Waiting, o.ActiveLeases), m}
	}
	if m.IdleSince.IsZero() {
		m.IdleSince = o.Now
		return Decision{ActNone, "idle timer started", m}
	}
	if idle := o.Now.Sub(m.IdleSince); idle >= l.Idle {
		return Decision{ActDestroy, fmt.Sprintf("idle for %s", idle.Round(time.Second)), m}
	}
	return Decision{ActNone, fmt.Sprintf("idle for %s of %s", o.Now.Sub(m.IdleSince).Round(time.Second), l.Idle), m}
}
