package scaler

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"time"
)

// Endpoint is an ssh address of the instance.
type Endpoint struct {
	Host string
	Port int
}

func (e Endpoint) String() string { return e.Host + ":" + strconv.Itoa(e.Port) }

// TunnelStatus is a snapshot of the supervised ssh process.
type TunnelStatus struct {
	Running  bool      // an ssh process is currently up
	Verified bool      // the instance could reach rk serve through the forward
	Endpoint Endpoint  // endpoint of the current/last attempt
	UpSince  time.Time // when the current ssh process started
	Restarts int       // ssh processes started since Ensure
}

// Tunnel keeps a reverse ssh forward into the instance alive.
type Tunnel interface {
	// Ensure starts supervising a tunnel to one of eps (tried in order,
	// rotating on failure). Calling it again with the same endpoints is a
	// no-op; with different ones the tunnel is restarted.
	Ensure(eps []Endpoint)
	// Stop kills the tunnel and forgets the endpoints.
	Stop()
	// Status reports the current state.
	Status() TunnelStatus
}

// SSHTunnel runs `ssh -N -R RemotePort:Forward` in a restart loop.
type SSHTunnel struct {
	RemotePort     int           // port bound on the instance's loopback
	Forward        string        // host:port that rk serve answers on, from this machine
	User           string        // default root
	KeyFile        string        // optional identity file
	KnownHosts     string        // known_hosts file for accept-new pinning; removed on Stop
	SSHBin         string        // default ssh
	ConnectTimeout time.Duration // default 20 s
	Log            *slog.Logger

	mu     sync.Mutex
	eps    []Endpoint
	cancel context.CancelFunc
	done   chan struct{}
	st     TunnelStatus
}

func sameEndpoints(a, b []Endpoint) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Ensure implements Tunnel.
func (t *SSHTunnel) Ensure(eps []Endpoint) {
	if len(eps) == 0 {
		return
	}
	t.mu.Lock()
	if t.cancel != nil && sameEndpoints(t.eps, eps) {
		t.mu.Unlock()
		return
	}
	t.mu.Unlock()
	t.Stop()
	ctx, cancel := context.WithCancel(context.Background())
	t.mu.Lock()
	t.eps = append([]Endpoint(nil), eps...)
	t.cancel = cancel
	t.done = make(chan struct{})
	t.st = TunnelStatus{}
	done := t.done
	t.mu.Unlock()
	go t.supervise(ctx, eps, done)
}

// Stop implements Tunnel.
func (t *SSHTunnel) Stop() {
	t.mu.Lock()
	cancel, done := t.cancel, t.done
	t.cancel, t.done, t.eps = nil, nil, nil
	t.mu.Unlock()
	if cancel != nil {
		cancel()
		<-done
	}
	if t.KnownHosts != "" {
		_ = os.Remove(t.KnownHosts)
	}
	t.mu.Lock()
	t.st = TunnelStatus{}
	t.mu.Unlock()
}

// Status implements Tunnel.
func (t *SSHTunnel) Status() TunnelStatus {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.st
}

func (t *SSHTunnel) log() *slog.Logger {
	if t.Log != nil {
		return t.Log
	}
	return slog.Default()
}

func (t *SSHTunnel) baseArgs(ep Endpoint) []string {
	timeout := t.ConnectTimeout
	if timeout <= 0 {
		timeout = 20 * time.Second
	}
	user := t.User
	if user == "" {
		user = "root"
	}
	args := []string{
		"-o", "BatchMode=yes",
		"-o", "StrictHostKeyChecking=accept-new",
		"-o", "ServerAliveInterval=15",
		"-o", "ServerAliveCountMax=3",
		"-o", "ConnectTimeout=" + strconv.Itoa(int(timeout.Seconds())),
		"-o", "LogLevel=ERROR",
		"-p", strconv.Itoa(ep.Port),
	}
	if t.KnownHosts != "" {
		args = append(args, "-o", "UserKnownHostsFile="+t.KnownHosts)
	} else {
		args = append(args, "-o", "UserKnownHostsFile=/dev/null")
	}
	if t.KeyFile != "" {
		args = append(args, "-i", t.KeyFile)
	}
	return append(args, user+"@"+ep.Host)
}

func (t *SSHTunnel) supervise(ctx context.Context, eps []Endpoint, done chan struct{}) {
	defer close(done)
	bin := t.SSHBin
	if bin == "" {
		bin = "ssh"
	}
	backoff := 3 * time.Second
	for i := 0; ctx.Err() == nil; i++ {
		ep := eps[i%len(eps)]
		args := append([]string{"-N",
			"-o", "ExitOnForwardFailure=yes",
			"-R", fmt.Sprintf("127.0.0.1:%d:%s", t.RemotePort, t.Forward)}, t.baseArgs(ep)...)
		cmd := exec.CommandContext(ctx, bin, args...)
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		started := time.Now()
		if err := cmd.Start(); err != nil {
			t.log().Error("tunnel: ssh start", "err", err)
			if !sleepCtx(ctx, backoff) {
				return
			}
			continue
		}
		t.mu.Lock()
		t.st = TunnelStatus{Running: true, Endpoint: ep, UpSince: started, Restarts: t.st.Restarts + 1}
		t.mu.Unlock()
		t.log().Info("tunnel: ssh up", "endpoint", ep.String(), "remote_port", t.RemotePort, "forward", t.Forward)

		verifyCtx, stopVerify := context.WithCancel(ctx)
		go t.verify(verifyCtx, bin, ep)
		err := cmd.Wait()
		stopVerify()
		lived := time.Since(started)
		t.mu.Lock()
		t.st.Running, t.st.Verified = false, false
		t.mu.Unlock()
		if ctx.Err() != nil {
			return
		}
		t.log().Warn("tunnel: ssh exited", "endpoint", ep.String(), "after", lived.Round(time.Second), "err", err)
		if lived > 30*time.Second {
			backoff = 3 * time.Second
			i-- // the endpoint worked; keep it
		} else if backoff < time.Minute {
			backoff *= 2
		}
		if !sleepCtx(ctx, backoff) {
			return
		}
	}
}

// verify runs a probe through a second ssh session: curl on the instance
// against the forwarded port. Marks the tunnel Verified once it answers.
func (t *SSHTunnel) verify(ctx context.Context, bin string, ep Endpoint) {
	probe := fmt.Sprintf("curl -sf -o /dev/null -m 10 http://127.0.0.1:%d/healthz", t.RemotePort)
	for attempt := 0; ctx.Err() == nil && attempt < 20; attempt++ {
		if !sleepCtx(ctx, 3*time.Second) {
			return
		}
		cmd := exec.CommandContext(ctx, bin, append(t.baseArgs(ep), probe)...)
		if err := cmd.Run(); err == nil {
			t.mu.Lock()
			if t.st.Running && t.st.Endpoint == ep {
				t.st.Verified = true
			}
			t.mu.Unlock()
			t.log().Info("tunnel: verified from the instance", "endpoint", ep.String(), "after", time.Since(t.st.UpSince).Round(time.Second))
			return
		}
	}
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}
