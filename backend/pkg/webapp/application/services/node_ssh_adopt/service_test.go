package node_ssh_adopt

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/config"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/node_create"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/node_ssh_adopt"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/node_ssh_preflight"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/out/results"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/service"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/audit"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/audit/audittest"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/audit_event"
	nodeDomain "github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node_adoption_job"
	proto "github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/grpc/proto"
)

const (
	serverKey    = "SHA256:serverkeyserverkeyserverkeyserverkeyserverk"
	otherKey     = "SHA256:otherkeyotherkeyotherkeyotherkeyotherkeyoth"
	testPassword = "hunter2-ssh"
	testSudoPW   = "hunter2-sudo"
)

// ---- fakes -----------------------------------------------------------------

// runRecord is one command the fake session saw, with how much of its context
// deadline remained when it arrived — the only way to check from outside that
// the service bounded the call.
type runRecord struct {
	cmd       string
	remaining time.Duration
	deadline  bool
}

type fakeSSHClient struct {
	mu       sync.Mutex
	hostKey  string
	dials    []service.SSHConnParams
	runs     []runRecord
	blockCmd string // a command that blocks until its ctx is done
	// dialErr, when set, fails every Dial with a plain transport error — the
	// device is off, or the network dropped — before any host key is seen.
	dialErr error
	// holdDial, when set, parks every Dial until it is closed (capacity tests).
	holdDial chan struct{}
}

func (f *fakeSSHClient) Dial(ctx context.Context, p service.SSHConnParams) (service.SSHSession, error) {
	f.mu.Lock()
	f.dials = append(f.dials, p)
	dialErr, hold, hostKey := f.dialErr, f.holdDial, f.hostKey
	f.mu.Unlock()
	if hold != nil {
		select {
		case <-hold:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if dialErr != nil {
		return nil, dialErr
	}
	switch p.HostKeyPolicy {
	case "strict":
		if p.KnownHostKeyFingerprint != hostKey {
			return nil, &service.HostKeyMismatchError{Got: hostKey, Want: p.KnownHostKeyFingerprint}
		}
	case "tofu", "":
	default:
		return nil, errors.New("unknown policy " + p.HostKeyPolicy)
	}
	return &fakeSession{client: f, user: p.Username}, nil
}

func (f *fakeSSHClient) Dials() []service.SSHConnParams {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]service.SSHConnParams(nil), f.dials...)
}

func (f *fakeSSHClient) Runs() []runRecord {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]runRecord(nil), f.runs...)
}

func (f *fakeSSHClient) record(ctx context.Context, cmd string) {
	rec := runRecord{cmd: cmd}
	if dl, ok := ctx.Deadline(); ok {
		rec.deadline = true
		rec.remaining = time.Until(dl)
	}
	f.mu.Lock()
	f.runs = append(f.runs, rec)
	f.mu.Unlock()
}

type fakeSession struct {
	client *fakeSSHClient
	user   string
}

func (s *fakeSession) Run(ctx context.Context, cmd string) (service.SSHRunResult, error) {
	s.client.record(ctx, cmd)
	if s.client.blockCmd != "" && strings.Contains(cmd, s.client.blockCmd) {
		<-ctx.Done()
		return service.SSHRunResult{}, ctx.Err()
	}
	switch {
	case cmd == "uname -m":
		return service.SSHRunResult{Stdout: "aarch64\n"}, nil
	case cmd == "whoami":
		return service.SSHRunResult{Stdout: s.user + "\n"}, nil
	case strings.HasPrefix(cmd, "grep -m1 -E '^Serial'"):
		return service.SSHRunResult{Stdout: "Serial\t\t: 10000000deadbeef\n"}, nil
	}
	return service.SSHRunResult{}, nil
}

func (s *fakeSession) RunSudo(ctx context.Context, cmd string, _ string) (service.SSHRunResult, error) {
	s.client.record(ctx, "sudo:"+cmd)
	return service.SSHRunResult{}, nil
}

func (s *fakeSession) WriteFile(ctx context.Context, path string, _ []byte, _ uint32, _ string) error {
	s.client.record(ctx, "write:"+path)
	return nil
}

func (s *fakeSession) HostKeyFingerprint() string { return s.client.currentHostKey() }
func (s *fakeSession) Close() error               { return nil }

func (f *fakeSSHClient) currentHostKey() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.hostKey
}

func (f *fakeSSHClient) setHostKey(key string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.hostKey = key
}

func (f *fakeSSHClient) setDialErr(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.dialErr = err
}

// fakeKeyResolver hands out one private key and counts how often it was asked:
// a capacity refusal must land before the key is decrypted.
type fakeKeyResolver struct{ calls atomic.Int32 }

func (r *fakeKeyResolver) GetPrivateKey(context.Context, uuid.UUID, uuid.UUID) (string, error) {
	r.calls.Add(1)
	return "-----BEGIN OPENSSH PRIVATE KEY-----\nsecretmaterial\n-----END OPENSSH PRIVATE KEY-----", nil
}

type fakeProvisioner struct{}

func (fakeProvisioner) Create(_ context.Context, cmd node_create.NodeCreateCommand) (*nodeDomain.Node, error) {
	return &nodeDomain.Node{ID: cmd.ID, Name: cmd.Name}, nil
}
func (fakeProvisioner) CheckDeviceCap(context.Context, audit.Actor) error { return nil }
func (fakeProvisioner) GetAdoptionInfo(context.Context, uuid.UUID, uuid.UUID, string, string) (string, error) {
	return "adoption-blob", nil
}
func (fakeProvisioner) GetByIDAndAccount(context.Context, uuid.UUID, uuid.UUID) (*nodeDomain.Node, error) {
	return nil, app_errors.ErrNodeNotFound
}
func (fakeProvisioner) GetByHardwareSerial(context.Context, uuid.UUID, string) (*nodeDomain.Node, error) {
	return nil, app_errors.ErrNodeNotFound
}
func (fakeProvisioner) StampWebUSBPlaceholder(context.Context, uuid.UUID, uuid.UUID, string) error {
	return nil
}
func (fakeProvisioner) Delete(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, bool) error {
	return nil
}
func (fakeProvisioner) ClearAutoProvisionEEPROMPending(context.Context, uuid.UUID, uuid.UUID) error {
	return nil
}

type stubComms struct{}

func (stubComms) SendCommand(string, *proto.Command) error { return nil }
func (stubComms) GetConnectedNodes() []string              { return nil }
func (stubComms) IsNodeConnected(string) bool              { return true }

type memJobRepo struct {
	mu   sync.Mutex
	jobs map[uuid.UUID]node_adoption_job.Job
}

func newMemJobRepo() *memJobRepo { return &memJobRepo{jobs: map[uuid.UUID]node_adoption_job.Job{}} }

func (r *memJobRepo) Create(_ context.Context, job *node_adoption_job.Job) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.jobs[job.ID] = *job
	return nil
}
func (r *memJobRepo) Update(_ context.Context, job *node_adoption_job.Job) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.jobs[job.ID] = *job
	return nil
}
func (r *memJobRepo) GetByID(_ context.Context, _ uuid.UUID, id uuid.UUID) (*node_adoption_job.Job, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	j, ok := r.jobs[id]
	if !ok {
		return nil, app_errors.ErrNodeAdoptionJobNotFound
	}
	return &j, nil
}
func (r *memJobRepo) ResetStrandedRunning(context.Context) error { return nil }
func (r *memJobRepo) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.jobs)
}

type fakeResolver struct{ hosts map[string][]string }

func (f fakeResolver) LookupIPAddr(_ context.Context, host string) ([]net.IPAddr, error) {
	ips, ok := f.hosts[host]
	if !ok {
		return nil, errors.New("no such host")
	}
	out := make([]net.IPAddr, 0, len(ips))
	for _, ip := range ips {
		out = append(out, net.IPAddr{IP: net.ParseIP(ip)})
	}
	return out, nil
}

// ---- harness ---------------------------------------------------------------

type harness struct {
	svc    *Service
	ssh    *fakeSSHClient
	keys   *fakeKeyResolver
	jobs   *memJobRepo
	audit  *audittest.Sink
	actor  commands.AuthContext
	config config.SSHAdoptConfig
}

func newHarness(t *testing.T, mutate func(*config.SSHAdoptConfig)) *harness {
	t.Helper()
	cfg := config.SSHAdoptConfig{
		Enabled:                 true,
		AllowedCDNBaseURL:       "http://cdn.test",
		ConnectTimeoutSeconds:   1,
		CommandTimeoutSeconds:   60,
		AgentWaitTimeoutSeconds: 5,
	}
	if mutate != nil {
		mutate(&cfg)
	}
	recorder, sink := audittest.New()
	sshc := &fakeSSHClient{hostKey: serverKey}
	keys := &fakeKeyResolver{}
	jobs := newMemJobRepo()
	svc := NewService(sshc, keys, fakeProvisioner{}, nil, stubComms{}, jobs, nil,
		recorder, cfg, "http://platform.test", "platform.test:50051",
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	return &harness{
		svc:    svc,
		ssh:    sshc,
		keys:   keys,
		jobs:   jobs,
		audit:  sink,
		actor:  commands.NewAuthContext(uuid.New(), uuid.New()),
		config: cfg,
	}
}

// adoptCmd is the API client's honest request: the device's fingerprint,
// known out of band, pins the connection. Tests of the ticket path clear it
// and set HostKeyTicket instead.
func (h *harness) adoptCmd() node_ssh_adopt.SSHAdoptCommand {
	return node_ssh_adopt.SSHAdoptCommand{
		Host:               "10.0.0.3",
		Port:               22,
		Username:           "pi",
		Password:           testPassword,
		SudoPassword:       testSudoPW,
		HostKeyFingerprint: serverKey,
		NodeName:           "bench-1",
		DefaultUsername:    "orchestrator",
		DefaultPassword:    "console-pw",
		AuthContext:        h.actor,
	}
}

// adoptWithTicket is the UI's request: no fingerprint, the preflight's ticket.
func (h *harness) adoptWithTicket(ticket string) node_ssh_adopt.SSHAdoptCommand {
	cmd := h.adoptCmd()
	cmd.HostKeyFingerprint = ""
	cmd.HostKeyTicket = ticket
	return cmd
}

// preflightOK runs a first-contact preflight and returns its result, failing
// the test unless the device was reachable.
func (h *harness) preflightOK(t *testing.T) results.SSHPreflightResult {
	t.Helper()
	res, err := h.svc.Preflight(context.Background(), h.preflightCmd())
	if err != nil {
		t.Fatalf("Preflight: %v", err)
	}
	if !res.Reachable {
		t.Fatalf("Preflight unreachable: %s", res.Error)
	}
	return res
}

func (h *harness) preflightCmd() node_ssh_preflight.SSHPreflightCommand {
	return node_ssh_preflight.SSHPreflightCommand{
		Host:         "10.0.0.3",
		Port:         22,
		Username:     "pi",
		Password:     testPassword,
		SudoPassword: testSudoPW,
		AuthContext:  h.actor,
	}
}

// waitJob polls until the job reaches a terminal status.
func (h *harness) waitJob(t *testing.T, id uuid.UUID) *node_adoption_job.Job {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		j, err := h.jobs.GetByID(context.Background(), h.actor.AccountID, id)
		if err == nil && (j.Status == node_adoption_job.StatusCompleted || j.Status == node_adoption_job.StatusFailed) {
			return j
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("adoption job never reached a terminal status")
	return nil
}

func assertNoCredentialMaterial(t *testing.T, events []audit_event.AuditEvent) {
	t.Helper()
	for _, e := range events {
		for _, secret := range []string{testPassword, testSudoPW, "PRIVATE KEY", "secretmaterial", "console-pw"} {
			if strings.Contains(e.Detail, secret) {
				t.Errorf("audit row %s carries credential material %q in its detail: %q", e.Action, secret, e.Detail)
			}
		}
	}
}

// ---- the vetted address is what gets dialed ---------------------------------

func TestAllowlistDialsTheVettedAddressAndKeepsTheName(t *testing.T) {
	h := newHarness(t, func(c *config.SSHAdoptConfig) { c.AllowedHostCIDRs = []string{"10.0.0.0/8"} })
	h.svc.resolver = fakeResolver{hosts: map[string][]string{"pi.lan": {"10.0.0.7"}}}

	cmd := h.adoptCmd()
	cmd.Host = "pi.lan"
	job, err := h.svc.StartAdoption(context.Background(), cmd)
	if err != nil {
		t.Fatalf("StartAdoption: %v", err)
	}
	if job.Host != "pi.lan" {
		t.Errorf("job.Host = %q, want the name the operator typed", job.Host)
	}
	h.waitJob(t, job.ID)
	dials := h.ssh.Dials()
	if len(dials) != 1 {
		t.Fatalf("dials = %d", len(dials))
	}
	if dials[0].DialAddr != "10.0.0.7" {
		t.Errorf("DialAddr = %q, want the vetted 10.0.0.7", dials[0].DialAddr)
	}
	if dials[0].Host != "pi.lan" {
		t.Errorf("Host = %q, want the name", dials[0].Host)
	}
}

// A name with several answers must vet to the SAME address every time, or the
// host-key ticket (bound to the address the preflight dialed) refuses the
// adoption whenever the resolver rotates its answer order.
func TestAllowlistPicksTheSameAddressWhateverTheResolverOrder(t *testing.T) {
	h := newHarness(t, func(c *config.SSHAdoptConfig) { c.AllowedHostCIDRs = []string{"10.0.0.0/8"} })
	for _, order := range [][]string{{"10.0.0.7", "10.0.0.3"}, {"10.0.0.3", "10.0.0.7"}} {
		h.svc.resolver = fakeResolver{hosts: map[string][]string{"pi.lan": order}}
		got, err := h.svc.checkHostAllowed(context.Background(), "pi.lan")
		if err != nil {
			t.Fatalf("checkHostAllowed(%v): %v", order, err)
		}
		if got != "10.0.0.3" {
			t.Errorf("order %v vetted %q, want the lowest address 10.0.0.3", order, got)
		}
	}
}

func TestAllowlistRefusesAnOutOfRangeNameBeforeAnyDial(t *testing.T) {
	h := newHarness(t, func(c *config.SSHAdoptConfig) { c.AllowedHostCIDRs = []string{"10.0.0.0/8"} })
	h.svc.resolver = fakeResolver{hosts: map[string][]string{"evil.lan": {"8.8.8.8"}}}

	cmd := h.preflightCmd()
	cmd.Host = "evil.lan"
	if _, err := h.svc.Preflight(context.Background(), cmd); err == nil {
		t.Fatal("Preflight accepted a host outside the allowlist")
	}
	if len(h.ssh.Dials()) != 0 {
		t.Error("a dial happened for a refused host")
	}
}

func TestAllowlistWithAnIPLiteralDialsThatLiteral(t *testing.T) {
	h := newHarness(t, func(c *config.SSHAdoptConfig) { c.AllowedHostCIDRs = []string{"10.0.0.0/8"} })
	h.svc.resolver = fakeResolver{} // any lookup would fail: literals must not resolve
	res, err := h.svc.Preflight(context.Background(), h.preflightCmd())
	if err != nil || !res.Reachable {
		t.Fatalf("Preflight = %+v, %v", res, err)
	}
	if d := h.ssh.Dials(); len(d) != 1 || d[0].DialAddr != "10.0.0.3" {
		t.Errorf("dials = %+v", d)
	}
}

func TestNoAllowlistDialsByName(t *testing.T) {
	h := newHarness(t, nil)
	cmd := h.preflightCmd()
	cmd.Host = "pi.lan"
	if _, err := h.svc.Preflight(context.Background(), cmd); err != nil {
		t.Fatal(err)
	}
	if d := h.ssh.Dials(); len(d) != 1 || d[0].DialAddr != "" || d[0].Host != "pi.lan" {
		t.Errorf("dials = %+v, want a plain dial by name", d)
	}
}

// ---- every command is bounded ----------------------------------------------

func TestEveryAdoptCommandCarriesADeadlineAndInstallGetsTheLongerOne(t *testing.T) {
	h := newHarness(t, nil)
	job, err := h.svc.StartAdoption(context.Background(), h.adoptCmd())
	if err != nil {
		t.Fatal(err)
	}
	h.waitJob(t, job.ID)
	runs := h.ssh.Runs()
	if len(runs) == 0 {
		t.Fatal("no commands recorded")
	}
	commandTimeout := time.Duration(h.config.CommandTimeoutSeconds) * time.Second
	for _, r := range runs {
		if !r.deadline {
			t.Errorf("%q ran with no deadline", r.cmd)
			continue
		}
		install := strings.Contains(r.cmd, "dpkg -i") || strings.Contains(r.cmd, "orchestrator-agent.deb")
		switch {
		case install && r.remaining <= commandTimeout:
			t.Errorf("install command %q got %s, want the longer install bound", r.cmd, r.remaining)
		case !install && r.remaining > commandTimeout:
			t.Errorf("%q got %s, want at most the %s command timeout", r.cmd, r.remaining, commandTimeout)
		}
	}
}

func TestPreflightCommandTimeoutUnblocksAStuckCommand(t *testing.T) {
	h := newHarness(t, func(c *config.SSHAdoptConfig) { c.CommandTimeoutSeconds = 1 })
	h.ssh.blockCmd = "uname -m"

	start := time.Now()
	res, err := h.svc.Preflight(context.Background(), h.preflightCmd())
	if err != nil {
		t.Fatalf("Preflight: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 4*time.Second {
		t.Fatalf("Preflight took %s with a 1s command timeout", elapsed)
	}
	if !res.Reachable || res.Arch != "" {
		t.Errorf("result = %+v, want reachable with an empty arch", res)
	}
}
