package scaler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Offer is a rentable machine from `vastai search offers`.
type Offer struct {
	ID          int64
	GPU         string
	DPH         float64 // $/hour, total
	Geo         string
	Reliability float64
	DirectPorts int
	InetDown    float64
	GPURAM      float64 // MB
}

// Instance is one row of `vastai show instances`.
type Instance struct {
	ID            int64
	Status        string // actual_status: loading, running, exited, ...
	StatusMsg     string
	Label         string
	Image         string
	GPU           string
	DPH           float64
	StartDate     time.Time
	SSHHost       string // proxy host (sshN.vast.ai)
	SSHPort       int
	PublicIP      string // direct address
	DirectSSHPort int    // host port mapped to 22/tcp, 0 when unknown
}

// Running reports whether the container is up.
func (i Instance) Running() bool { return i.Status == "running" }

// SSHEndpoints lists ways to reach the instance's sshd, direct first.
func (i Instance) SSHEndpoints() []Endpoint {
	var eps []Endpoint
	if i.PublicIP != "" && i.DirectSSHPort > 0 {
		eps = append(eps, Endpoint{Host: i.PublicIP, Port: i.DirectSSHPort})
	}
	if i.SSHHost != "" && i.SSHPort > 0 {
		eps = append(eps, Endpoint{Host: i.SSHHost, Port: i.SSHPort})
	}
	return eps
}

// RentSpec is what a new instance is created with.
type RentSpec struct {
	Image   string
	Login   string // docker login arguments for a private registry, may be empty
	Label   string
	Env     string // vast --env string: "-e K=V -e K2=V2"
	OnStart string // on-start script contents
	DiskGB  int
}

// Vast is the subset of vast.ai the scaler uses. The production
// implementation shells out to the `vastai` CLI; tests use a fake.
type Vast interface {
	// SearchOffers returns rentable offers matching query, cheapest first.
	SearchOffers(ctx context.Context, query string) ([]Offer, error)
	// CreateInstance rents offerID and returns the new instance (contract) id.
	CreateInstance(ctx context.Context, offerID int64, spec RentSpec) (int64, error)
	// ShowInstances lists the account's instances.
	ShowInstances(ctx context.Context) ([]Instance, error)
	// DestroyInstance stops billing and deletes the instance.
	DestroyInstance(ctx context.Context, id int64) error
	// Credit returns the account credit in dollars.
	Credit(ctx context.Context) (float64, error)
}

// CLI drives the `vastai` command with --raw JSON output.
type CLI struct {
	Bin     string        // default "vastai"
	APIKey  string        // passed as --api-key when set; otherwise the CLI's own key file is used
	Timeout time.Duration // per call; default 90 s
}

func (c *CLI) run(ctx context.Context, args ...string) ([]byte, error) {
	bin := c.Bin
	if bin == "" {
		bin = "vastai"
	}
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = 90 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	full := append([]string{}, args...)
	full = append(full, "--raw")
	if c.APIKey != "" {
		full = append(full, "--api-key", c.APIKey)
	}
	cmd := exec.CommandContext(ctx, bin, full...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = strings.TrimSpace(stdout.String())
		}
		return nil, fmt.Errorf("vastai %s: %w: %s", strings.Join(args[:min(2, len(args))], " "), err, truncate(msg, 400))
	}
	return bytes.TrimSpace(stdout.Bytes()), nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// SearchOffers implements Vast.
func (c *CLI) SearchOffers(ctx context.Context, query string) ([]Offer, error) {
	out, err := c.run(ctx, "search", "offers", query, "-o", "dph+")
	if err != nil {
		return nil, err
	}
	var rows []struct {
		ID          int64   `json:"id"`
		GPUName     string  `json:"gpu_name"`
		DPH         float64 `json:"dph_total"`
		Geo         string  `json:"geolocation"`
		Reliability float64 `json:"reliability2"`
		DirectPorts int     `json:"direct_port_count"`
		InetDown    float64 `json:"inet_down"`
		GPURAM      float64 `json:"gpu_ram"`
		Rentable    *bool   `json:"rentable"`
	}
	if err := json.Unmarshal(out, &rows); err != nil {
		return nil, fmt.Errorf("vastai search offers: parse: %w", err)
	}
	offers := make([]Offer, 0, len(rows))
	for _, r := range rows {
		if r.Rentable != nil && !*r.Rentable {
			continue
		}
		offers = append(offers, Offer{ID: r.ID, GPU: r.GPUName, DPH: r.DPH, Geo: r.Geo, Reliability: r.Reliability, DirectPorts: r.DirectPorts, InetDown: r.InetDown, GPURAM: r.GPURAM})
	}
	sort.SliceStable(offers, func(i, j int) bool { return offers[i].DPH < offers[j].DPH })
	return offers, nil
}

// CreateInstance implements Vast.
func (c *CLI) CreateInstance(ctx context.Context, offerID int64, spec RentSpec) (int64, error) {
	args := []string{"create", "instance", strconv.FormatInt(offerID, 10),
		"--image", spec.Image, "--ssh", "--direct", "--cancel-unavail"}
	if spec.DiskGB > 0 {
		args = append(args, "--disk", strconv.Itoa(spec.DiskGB))
	}
	if spec.Login != "" {
		args = append(args, "--login", spec.Login)
	}
	if spec.Label != "" {
		args = append(args, "--label", spec.Label)
	}
	if spec.Env != "" {
		args = append(args, "--env", spec.Env)
	}
	if spec.OnStart != "" {
		args = append(args, "--onstart-cmd", spec.OnStart)
	}
	out, err := c.run(ctx, args...)
	if err != nil {
		return 0, err
	}
	var resp struct {
		Success     bool   `json:"success"`
		NewContract int64  `json:"new_contract"`
		Msg         string `json:"msg"`
		Error       string `json:"error"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		return 0, fmt.Errorf("vastai create instance: parse %q: %w", truncate(string(out), 200), err)
	}
	if !resp.Success || resp.NewContract == 0 {
		return 0, fmt.Errorf("vastai create instance: %s %s", resp.Error, resp.Msg)
	}
	return resp.NewContract, nil
}

// ShowInstances implements Vast.
func (c *CLI) ShowInstances(ctx context.Context) ([]Instance, error) {
	out, err := c.run(ctx, "show", "instances")
	if err != nil {
		return nil, err
	}
	var rows []struct {
		ID           int64   `json:"id"`
		ActualStatus string  `json:"actual_status"`
		StatusMsg    string  `json:"status_msg"`
		Label        string  `json:"label"`
		Image        string  `json:"image_uuid"`
		GPUName      string  `json:"gpu_name"`
		DPH          float64 `json:"dph_total"`
		StartDate    float64 `json:"start_date"`
		SSHHost      string  `json:"ssh_host"`
		SSHPort      int     `json:"ssh_port"`
		PublicIP     string  `json:"public_ipaddr"`
		Ports        map[string][]struct {
			HostPort string `json:"HostPort"`
		} `json:"ports"`
	}
	if err := json.Unmarshal(out, &rows); err != nil {
		return nil, fmt.Errorf("vastai show instances: parse: %w", err)
	}
	var list []Instance
	for _, r := range rows {
		in := Instance{ID: r.ID, Status: r.ActualStatus, StatusMsg: r.StatusMsg, Label: r.Label, Image: r.Image,
			GPU: r.GPUName, DPH: r.DPH, SSHHost: r.SSHHost, SSHPort: r.SSHPort, PublicIP: r.PublicIP}
		if r.StartDate > 0 {
			in.StartDate = time.Unix(int64(r.StartDate), 0)
		}
		for _, m := range r.Ports["22/tcp"] {
			if p, err := strconv.Atoi(m.HostPort); err == nil && p > 0 {
				in.DirectSSHPort = p
				break
			}
		}
		list = append(list, in)
	}
	return list, nil
}

// DestroyInstance implements Vast.
func (c *CLI) DestroyInstance(ctx context.Context, id int64) error {
	out, err := c.run(ctx, "destroy", "instance", strconv.FormatInt(id, 10))
	if err != nil {
		return err
	}
	var resp struct {
		Success bool   `json:"success"`
		Msg     string `json:"msg"`
	}
	if json.Unmarshal(out, &resp) == nil && !resp.Success {
		return fmt.Errorf("vastai destroy instance %d: %s", id, resp.Msg)
	}
	return nil
}

// Credit implements Vast.
func (c *CLI) Credit(ctx context.Context) (float64, error) {
	out, err := c.run(ctx, "show", "user")
	if err != nil {
		return 0, err
	}
	var u struct {
		Credit *float64 `json:"credit"`
	}
	if err := json.Unmarshal(out, &u); err != nil {
		return 0, fmt.Errorf("vastai show user: parse: %w", err)
	}
	if u.Credit == nil {
		return 0, errors.New("vastai show user: no credit field")
	}
	return *u.Credit, nil
}
