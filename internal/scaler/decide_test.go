package scaler

import (
	"strings"
	"testing"
	"time"
)

func TestDecide(t *testing.T) {
	t0 := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	lim := Limits{Idle: 10 * time.Minute, MaxAge: 6 * time.Hour, BootTimeout: 15 * time.Minute, MinCredit: 5, RentCooldown: 2 * time.Minute}
	running := &Instance{ID: 1, Status: "running"}
	loading := &Instance{ID: 1, Status: "loading", StatusMsg: "pulling image"}

	tests := []struct {
		name   string
		o      Observation
		m      Memory
		action Action
		reason string    // substring
		idle   time.Time // expected Memory.IdleSince
	}{
		// No instance.
		{"nothing waiting", Observation{Now: t0, QueueOK: true}, Memory{}, ActNone, "nothing waiting", time.Time{}},
		{"queue unknown, no instance", Observation{Now: t0}, Memory{}, ActNone, "queue unknown", time.Time{}},
		{"waiting, credit ok → rent", Observation{Now: t0, QueueOK: true, Waiting: 2, CreditOK: true, Credit: 28}, Memory{}, ActRent, "2 job(s) waiting", time.Time{}},
		{"waiting but credit below floor", Observation{Now: t0, QueueOK: true, Waiting: 1, CreditOK: true, Credit: 4.99}, Memory{}, ActNone, "credit guard", time.Time{}},
		{"waiting, credit exactly at floor rents", Observation{Now: t0, QueueOK: true, Waiting: 1, CreditOK: true, Credit: 5}, Memory{}, ActRent, "waiting", time.Time{}},
		{"waiting but credit unknown", Observation{Now: t0, QueueOK: true, Waiting: 1}, Memory{}, ActNone, "credit unknown", time.Time{}},
		{"waiting within rent cooldown", Observation{Now: t0, QueueOK: true, Waiting: 1, CreditOK: true, Credit: 28, LastRentFailure: t0.Add(-time.Minute)}, Memory{}, ActNone, "cooldown", time.Time{}},
		{"waiting after rent cooldown", Observation{Now: t0, QueueOK: true, Waiting: 1, CreditOK: true, Credit: 28, LastRentFailure: t0.Add(-3 * time.Minute)}, Memory{}, ActRent, "waiting", time.Time{}},
		{"active leases elsewhere but nothing waiting", Observation{Now: t0, QueueOK: true, ActiveLeases: 1, CreditOK: true, Credit: 28}, Memory{}, ActNone, "nothing waiting", time.Time{}},
		{"stale idle memory cleared without instance", Observation{Now: t0, QueueOK: true}, Memory{IdleSince: t0.Add(-time.Hour)}, ActNone, "", time.Time{}},

		// With an instance.
		{"instance vanished from vast", Observation{Now: t0, QueueOK: true, HaveInstance: true, RentedAt: t0.Add(-time.Minute)}, Memory{}, ActForget, "no longer listed", time.Time{}},
		{"busy: waiting", Observation{Now: t0, QueueOK: true, Waiting: 1, HaveInstance: true, RentedAt: t0.Add(-time.Minute), Listed: running}, Memory{IdleSince: t0.Add(-time.Minute)}, ActNone, "busy", time.Time{}},
		{"busy: active lease", Observation{Now: t0, QueueOK: true, ActiveLeases: 1, HaveInstance: true, RentedAt: t0.Add(-time.Minute), Listed: running}, Memory{}, ActNone, "busy", time.Time{}},
		{"idle timer starts", Observation{Now: t0, QueueOK: true, HaveInstance: true, RentedAt: t0.Add(-time.Minute), Listed: running}, Memory{}, ActNone, "idle timer started", t0},
		{"idle below threshold", Observation{Now: t0, QueueOK: true, HaveInstance: true, RentedAt: t0.Add(-time.Hour), Listed: running}, Memory{IdleSince: t0.Add(-9 * time.Minute)}, ActNone, "idle for 9m0s of 10m0s", t0.Add(-9 * time.Minute)},
		{"idle at threshold → destroy", Observation{Now: t0, QueueOK: true, HaveInstance: true, RentedAt: t0.Add(-time.Hour), Listed: running}, Memory{IdleSince: t0.Add(-10 * time.Minute)}, ActDestroy, "idle for 10m0s", t0.Add(-10 * time.Minute)},
		{"idle while still booting counts too", Observation{Now: t0, QueueOK: true, HaveInstance: true, RentedAt: t0.Add(-12 * time.Minute), Listed: loading}, Memory{IdleSince: t0.Add(-11 * time.Minute)}, ActDestroy, "idle", t0.Add(-11 * time.Minute)},
		{"queue unknown keeps the instance and the timer", Observation{Now: t0, HaveInstance: true, RentedAt: t0.Add(-time.Hour), Listed: running}, Memory{IdleSince: t0.Add(-time.Hour)}, ActNone, "queue unknown", t0.Add(-time.Hour)},
		{"max age wins over busy", Observation{Now: t0, QueueOK: true, Waiting: 3, ActiveLeases: 1, HaveInstance: true, RentedAt: t0.Add(-6 * time.Hour), Listed: running}, Memory{}, ActDestroy, "max age", time.Time{}},
		{"max age wins over unknown queue", Observation{Now: t0, HaveInstance: true, RentedAt: t0.Add(-7 * time.Hour), Listed: running}, Memory{}, ActDestroy, "max age", time.Time{}},
		{"boot timeout while waiting", Observation{Now: t0, QueueOK: true, Waiting: 1, HaveInstance: true, RentedAt: t0.Add(-16 * time.Minute), Listed: loading}, Memory{}, ActDestroy, "not running after", time.Time{}},
		{"still loading before boot timeout", Observation{Now: t0, QueueOK: true, Waiting: 1, HaveInstance: true, RentedAt: t0.Add(-5 * time.Minute), Listed: loading}, Memory{}, ActNone, "busy", time.Time{}},
		{"low credit never destroys a busy instance", Observation{Now: t0, QueueOK: true, Waiting: 1, HaveInstance: true, RentedAt: t0.Add(-time.Minute), Listed: running, CreditOK: true, Credit: 1}, Memory{}, ActNone, "busy", time.Time{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d := Decide(tc.o, tc.m, lim)
			if d.Action != tc.action {
				t.Fatalf("action %s, want %s (%s)", d.Action, tc.action, d.Reason)
			}
			if !strings.Contains(d.Reason, tc.reason) {
				t.Fatalf("reason %q does not contain %q", d.Reason, tc.reason)
			}
			if !d.Memory.IdleSince.Equal(tc.idle) {
				t.Fatalf("IdleSince %v, want %v", d.Memory.IdleSince, tc.idle)
			}
		})
	}
}

func TestPickOffer(t *testing.T) {
	if _, ok := pickOffer(nil); ok {
		t.Fatal("empty list picked something")
	}
	offers := []Offer{{ID: 1, DPH: 0.05, DirectPorts: 0}, {ID: 2, DPH: 0.06, DirectPorts: 12}, {ID: 3, DPH: 0.07, DirectPorts: 3}}
	if o, _ := pickOffer(offers); o.ID != 2 {
		t.Fatalf("picked %d, want the cheapest with direct ports (2)", o.ID)
	}
	if o, _ := pickOffer(offers[:1]); o.ID != 1 {
		t.Fatalf("picked %d, want the only offer", o.ID)
	}
}

func TestInstanceSSHEndpoints(t *testing.T) {
	in := Instance{SSHHost: "ssh2.vast.ai", SSHPort: 13632, PublicIP: "1.2.3.4", DirectSSHPort: 59330}
	eps := in.SSHEndpoints()
	if len(eps) != 2 || eps[0] != (Endpoint{"1.2.3.4", 59330}) || eps[1] != (Endpoint{"ssh2.vast.ai", 13632}) {
		t.Fatalf("endpoints %v", eps)
	}
	if eps := (Instance{SSHHost: "ssh2.vast.ai", SSHPort: 1}).SSHEndpoints(); len(eps) != 1 {
		t.Fatalf("proxy only: %v", eps)
	}
	if eps := (Instance{}).SSHEndpoints(); len(eps) != 0 {
		t.Fatalf("no addresses: %v", eps)
	}
}
