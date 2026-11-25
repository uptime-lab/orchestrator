// Package node_ssh_adopt drives adopting an already-running device over SSH:
// install the agent, register the node, wait for it to connect and (optionally)
// flash a network-first EEPROM. Preflight is a synchronous non-destructive
// probe; adoption runs asynchronously as a node_adoption_job with live progress.
package node_ssh_adopt

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"path"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/config"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/node_create"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/node_ssh_adopt"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/node_ssh_preflight"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/out/results"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/service"
	portsws "github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/websockets"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/audit"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/eeprom_provision"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/audit_event"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/authz"
	nodeDomain "github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node_adoption_job"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/ssh_host_key_ticket"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/http_server/websockets"
)

// ConnectionKeyResolver returns the decrypted private key of a connection keypair
// for outbound SSH auth. Satisfied by the ssh_key service (GetPrivateKey).
type ConnectionKeyResolver interface {
	GetPrivateKey(ctx context.Context, accountID, keyID uuid.UUID) (string, error)
}

// NodeProvisioner is the subset of the node service the runner needs.
type NodeProvisioner interface {
	Create(ctx context.Context, cmd node_create.NodeCreateCommand) (*nodeDomain.Node, error)
	// CheckDeviceCap reports the device-cap refusal an admission would meet
	// right now (a typed *entitlement.DeviceCapError), or nil. Asked before
	// the job starts, so a device is never installed and tokened for a row
	// that cannot be created; Create still holds the real guard.
	CheckDeviceCap(ctx context.Context, actor audit.Actor) error
	GetAdoptionInfo(ctx context.Context, accountID, nodeID uuid.UUID, platformURL, grpcEndpoint string) (string, error)
	GetByIDAndAccount(ctx context.Context, nodeID, accountID uuid.UUID) (*nodeDomain.Node, error)
	// GetByHardwareSerial resolves an already-registered node by its hardware
	// serial (account-scoped). Returns app_errors.ErrNodeNotFound when none.
	GetByHardwareSerial(ctx context.Context, accountID uuid.UUID, hardwareSerial string) (*nodeDomain.Node, error)
	// StampWebUSBPlaceholder stamps a provisional hardware serial + the
	// awaiting-first-connect flag on a fresh node so later adopt paths (and
	// re-runs after a failure) reconcile into it instead of duplicating it.
	// Returns app_errors.ErrHardwareSerialConflict when the (full) serial is
	// already bound to a node in another account.
	StampWebUSBPlaceholder(ctx context.Context, nodeID, accountID uuid.UUID, serial string) error
	// Delete removes a node record (used to roll back a just-created node when
	// its serial turns out to be owned by another account).
	// Delete takes a force flag; every call here is a rollback of a node created
	// moments ago, which must not be refusable.
	Delete(ctx context.Context, accountID, actorUserID, nodeID uuid.UUID, force bool) error
	// ClearAutoProvisionEEPROMPending disarms the connect-time one-shot EEPROM
	// flash; the SSH runner performs the flash itself.
	ClearAutoProvisionEEPROMPending(ctx context.Context, nodeID, accountID uuid.UUID) error
}

const (
	agentPollInterval     = 3 * time.Second
	systemInfoWaitTimeout = 90 * time.Second
	// adoptionRunTimeout bounds one whole adoption run. SSH commands run on a
	// context that is otherwise never cancelled, so a dead TCP connection
	// mid-command (device power loss during the .deb download) would hang the
	// goroutine forever and permanently burn one of the semaphore slots.
	adoptionRunTimeout = 30 * time.Minute
	// installCommandTimeout bounds the two install-step commands (package
	// download, dpkg/apt). Longer than the per-command timeout on purpose: a
	// real `apt-get install -f` on a slow Pi over a slow uplink takes minutes,
	// and a 60 s blanket would fail a healthy install.
	installCommandTimeout = 10 * time.Minute
	// preflightTimeout bounds one whole preflight. The handler runs it on the
	// request context, which carries no deadline of its own; without this a
	// device that accepts the connection and then goes silent parks the
	// request goroutine until the client gives up.
	preflightTimeout = 5 * time.Minute
	// defaultCommandTimeout applies when SSHAdoptConfig.CommandTimeoutSeconds
	// is unset; getDefaultConfig() carries the same value.
	defaultCommandTimeout = 60 * time.Second
)

// HostResolver resolves a hostname for the adoption allowlist. net.DefaultResolver
// in production; a test substitutes one so the vetted-address rule can be
// checked without touching DNS.
type HostResolver interface {
	LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error)
}

// Service performs SSH-based device adoption.
type Service struct {
	sshClient       service.SSHClient
	keyResolver     ConnectionKeyResolver
	nodeProvisioner NodeProvisioner
	eepromProvision *eeprom_provision.Service
	comms           service.NodeCommunicationService
	jobRepo         repository.NodeAdoptionJobRepository
	ws              websockets.WebsocketClient
	audit           *audit.Recorder
	resolver        HostResolver
	cfg             config.SSHAdoptConfig
	platformURL     string
	grpcEndpoint    string
	logger          *slog.Logger
	// tickets issues and verifies the preflight-to-adopt host-key proof. Its
	// key is per process: a restart invalidates every outstanding ticket,
	// which costs nothing because the UI re-runs the preflight right before
	// it adopts, and an API client just runs it again.
	tickets       *ssh_host_key_ticket.Issuer
	adoptGate     *capacityGate
	preflightGate *capacityGate
}

// NewService constructs the SSH-adopt service. sshClient/keyResolver/audit/cfg/
// logger are always required; the runner dependencies (nodeProvisioner, comms,
// jobRepo, ws) may be nil when only Preflight is used, but StartAdoption then
// errors. The audit recorder is required even for preflight: it offers the
// operator's credentials to a host and accepts that host's key, which is the
// row the trail exists to hold.
func NewService(
	sshClient service.SSHClient,
	keyResolver ConnectionKeyResolver,
	nodeProvisioner NodeProvisioner,
	eepromProvision *eeprom_provision.Service,
	comms service.NodeCommunicationService,
	jobRepo repository.NodeAdoptionJobRepository,
	ws websockets.WebsocketClient,
	auditRecorder *audit.Recorder,
	cfg config.SSHAdoptConfig,
	platformURL, grpcEndpoint string,
	logger *slog.Logger,
) *Service {
	if sshClient == nil {
		panic("node_ssh_adopt: sshClient is required")
	}
	if keyResolver == nil {
		panic("node_ssh_adopt: keyResolver is required")
	}
	if auditRecorder == nil {
		panic("node_ssh_adopt: audit recorder is required")
	}
	tickets, err := ssh_host_key_ticket.NewRandomIssuer(ssh_host_key_ticket.DefaultTTL)
	if err != nil {
		// No randomness means no way to mint a proof nobody else can forge;
		// a boot without one would silently run adoptions unprotected.
		panic("node_ssh_adopt: " + err.Error())
	}
	return &Service{
		sshClient:       sshClient,
		keyResolver:     keyResolver,
		nodeProvisioner: nodeProvisioner,
		eepromProvision: eepromProvision,
		comms:           comms,
		jobRepo:         jobRepo,
		ws:              ws,
		audit:           auditRecorder,
		resolver:        net.DefaultResolver,
		cfg:             cfg,
		platformURL:     platformURL,
		grpcEndpoint:    grpcEndpoint,
		logger:          logger.With("service", "node_ssh_adopt"),
		tickets:         tickets,
		adoptGate:       newCapacityGate(capacityKindAdoption, maxConcurrentAdoptions, maxAdoptionsPerAccount),
		preflightGate:   newCapacityGate(capacityKindPreflight, maxConcurrentPreflights, maxPreflightsPerAccount),
	}
}

// Enabled reports whether SSH adoption is turned on.
func (s *Service) Enabled() bool { return s.cfg.Enabled }

// ResetStranded flips pending/running jobs from a previous run to failed (they
// cannot resume — the SSH session is gone). Called once at startup.
func (s *Service) ResetStranded(ctx context.Context) error {
	if s.jobRepo == nil {
		return nil
	}
	return s.jobRepo.ResetStrandedRunning(ctx)
}

// GetJob returns an adoption job's current state.
func (s *Service) GetJob(ctx context.Context, accountID, jobID uuid.UUID) (*node_adoption_job.Job, error) {
	return s.jobRepo.GetByID(ctx, accountID, jobID)
}

// checkHostAllowed enforces the ssh_adopt.allowed_host_cidrs allowlist. With an
// empty list any host is allowed (single-network installs) and the returned
// dial address is empty: the adapter dials the name. With a list, every
// address the name resolves to must sit inside it, and the FIRST vetted one is
// returned for the adapter to dial verbatim. Resolving here and again in the
// adapter would be two DNS answers, and only the first was checked — a name
// whose second answer points at an internal host walks straight through
// (rebinding). Multi-tenant installs set the list so Preflight cannot be used
// as an internal-network scanner.
func (s *Service) checkHostAllowed(ctx context.Context, host string) (string, error) {
	cidrs := s.cfg.AllowedHostCIDRs
	if len(cidrs) == 0 {
		return "", nil
	}
	nets := make([]*net.IPNet, 0, len(cidrs))
	for _, c := range cidrs {
		_, n, err := net.ParseCIDR(strings.TrimSpace(c))
		if err != nil {
			// Fail closed on a malformed allowlist rather than silently allow.
			return "", fmt.Errorf("server misconfiguration: bad allowed_host_cidrs entry %q", c)
		}
		nets = append(nets, n)
	}
	var ips []net.IP
	if ip := net.ParseIP(host); ip != nil {
		ips = []net.IP{ip}
	} else {
		resolver := s.resolver
		if resolver == nil {
			resolver = net.DefaultResolver
		}
		resolved, err := resolver.LookupIPAddr(ctx, host)
		if err != nil || len(resolved) == 0 {
			return "", fmt.Errorf("cannot resolve host %q to check the adoption allowlist", host)
		}
		for _, r := range resolved {
			ips = append(ips, r.IP)
		}
		// A resolver that rotates answer order (round-robin DNS) would
		// otherwise hand the preflight and the adoption different vetted
		// addresses for the same name, and the host-key ticket — bound to the
		// address the preflight dialed — would refuse every adoption of that
		// name. Pick deterministically: the lowest address wins.
		slices.SortFunc(ips, func(a, b net.IP) int { return bytes.Compare(a.To16(), b.To16()) })
	}
	for _, ip := range ips {
		allowed := false
		for _, n := range nets {
			if n.Contains(ip) {
				allowed = true
				break
			}
		}
		if !allowed {
			return "", fmt.Errorf("host %s is outside the allowed adoption subnets", ip)
		}
	}
	return ips[0].String(), nil
}

// checkHostKeyPolicy fails closed on a misspelled ssh_adopt.host_key_policy:
// "accept anything" must never be what an unknown word means.
func (s *Service) checkHostKeyPolicy() error {
	switch s.hostKeyPolicy() {
	case "tofu", "strict":
		return nil
	default:
		return fmt.Errorf("server misconfiguration: unknown ssh_adopt.host_key_policy %q (want tofu|strict)", s.cfg.HostKeyPolicy)
	}
}

// preflightHostKeyParams decides how the PREFLIGHT connects. The policy governs
// first contact only, because the preflight is the one call that may take a
// key on first use: the adoption never does (adoptHostKeyParams). With an
// expectation the probe connects strictly against it, so a changed key is
// reported before any credential is offered — unless the caller also set
// accept_new_host_key, in which case the probe connects on first use and the
// service records the replacement it observes. Without an expectation "tofu"
// (the default) connects on first use and "strict" refuses before any dial:
// every preflight must then carry a fingerprint obtained out of band. The
// server never holds a fingerprint of its own: managed devices are reimaged and
// change keys, and a stored pin would refuse the legitimate reinstall while
// the request-carried one follows the test the operator just ran.
func (s *Service) preflightHostKeyParams(cmd node_ssh_preflight.SSHPreflightCommand) (policy, known string, err error) {
	if err := s.checkHostKeyPolicy(); err != nil {
		return "", "", err
	}
	if cmd.ExpectedHostKeyFingerprint != "" {
		if cmd.AcceptNewHostKey {
			return "tofu", "", nil
		}
		return "strict", cmd.ExpectedHostKeyFingerprint, nil
	}
	if s.hostKeyPolicy() == "strict" {
		return "", "", &HostKeyFingerprintRequiredError{Field: "expected_host_key_fingerprint"}
	}
	return "tofu", "", nil
}

// adoptHostKeyParams decides how the ADOPTION connects: always strictly,
// against a key the request proves it expects. The proof is a ticket a
// preflight issued for this account, user, host, port and vetted address, or
// a fingerprint the API client knows out of band. Neither, or a ticket that
// does not verify for THIS request, is a typed 400 before any dial: the
// adoption is the call that offers SSH and sudo credentials to whatever
// answers on host:port, so it is never the call that meets a key for the
// first time. The policy setting plays no part here.
func (s *Service) adoptHostKeyParams(cmd node_ssh_adopt.SSHAdoptCommand, dialAddr string) (known, binding string, err error) {
	if err := s.checkHostKeyPolicy(); err != nil {
		return "", "", err
	}
	switch {
	case cmd.HostKeyTicket != "":
		fp, err := s.tickets.Verify(cmd.HostKeyTicket, ssh_host_key_ticket.Binding{
			AccountID: cmd.AuthContext.AccountID,
			UserID:    cmd.AuthContext.UserID,
			Host:      cmd.Host,
			Port:      portOrDefault(cmd.Port),
			DialAddr:  dialAddr,
		})
		if err != nil {
			var refusal *ssh_host_key_ticket.Refusal
			if errors.As(err, &refusal) {
				return "", "", &HostKeyBindingRequiredError{Reason: refusal.Reason}
			}
			return "", "", &HostKeyBindingRequiredError{Reason: err.Error()}
		}
		return fp, "ticket", nil
	case cmd.HostKeyFingerprint != "":
		return cmd.HostKeyFingerprint, "fingerprint", nil
	default:
		return "", "", &HostKeyBindingRequiredError{}
	}
}

// issueTicket mints the preflight's proof for the key it just observed. A
// failure here is a bug (the fingerprint is never empty on a reachable probe),
// logged and answered with no ticket rather than with a broken one.
func (s *Service) issueTicket(cmd node_ssh_preflight.SSHPreflightCommand, dialAddr, fingerprint string) (string, *time.Time) {
	token, expiresAt, err := s.tickets.Issue(ssh_host_key_ticket.Binding{
		AccountID:          cmd.AuthContext.AccountID,
		UserID:             cmd.AuthContext.UserID,
		Host:               cmd.Host,
		Port:               portOrDefault(cmd.Port),
		DialAddr:           dialAddr,
		HostKeyFingerprint: fingerprint,
	})
	if err != nil {
		s.logger.Error("issue host key ticket", "host", cmd.Host, "error", err)
		return "", nil
	}
	return token, &expiresAt
}

// agentSockFor gates forwarded-agent auth and resolves which socket to use.
// Empty return with nil error = the adapter's default (shared SSH_AUTH_SOCK).
// With agent_sock_dir set, each operator gets <dir>/<user-id>.sock — they can
// only authenticate with an agent they forwarded themselves.
func (s *Service) agentSockFor(useAgent bool, userID uuid.UUID) (string, error) {
	if !useAgent {
		return "", nil
	}
	if !s.cfg.AllowAgentAuth {
		return "", fmt.Errorf("forwarded-agent auth is disabled on this server (ssh_adopt.allow_agent_auth)")
	}
	if dir := strings.TrimSpace(s.cfg.AgentSockDir); dir != "" {
		return path.Join(dir, userID.String()+".sock"), nil
	}
	return "", nil
}

// StartAdoption validates the request, persists a pending job, and launches the
// adoption in the background. It returns the created job immediately.
func (s *Service) StartAdoption(ctx context.Context, cmd node_ssh_adopt.SSHAdoptCommand) (*node_adoption_job.Job, error) {
	if !s.cfg.Enabled {
		return nil, ErrSSHAdoptDisabled
	}
	if s.jobRepo == nil || s.nodeProvisioner == nil || s.comms == nil {
		return nil, fmt.Errorf("SSH adoption runner is not fully configured")
	}
	if err := cmd.Validate(); err != nil {
		return nil, err
	}
	// A request with no binding at all is refused before the allowlist lookup:
	// there is nothing to resolve for.
	if cmd.HostKeyTicket == "" && cmd.HostKeyFingerprint == "" {
		if err := s.checkHostKeyPolicy(); err != nil {
			return nil, err
		}
		return nil, &HostKeyBindingRequiredError{}
	}
	// Reserve a slot BEFORE anything that costs the server — the DNS lookup of
	// the allowlist check, the key decryption inside the run — and before
	// anything durable exists. Acquiring inside the run goroutine parked an
	// unbounded queue of pending jobs, each with a row already written and a
	// run timer already ticking while it waited, and nothing the operator could
	// cancel; acquiring after the lookup let a
	// caller spend resolver time on every request that a full house would then
	// refuse. A full house is answered now, as a typed 429, and the request
	// leaves no trace. `handedToRun` flips once the goroutine owns the slot;
	// every earlier return releases it.
	release, err := s.adoptGate.acquire(cmd.AuthContext.AccountID)
	if err != nil {
		return nil, err
	}
	handedToRun := false
	defer func() {
		if !handedToRun {
			release()
		}
	}()

	// The device cap, BEFORE the lookup and the job: an adoption that dialled,
	// installed and tokened a device only to find the row cannot be created
	// leaves a half-adopted machine. Inside the slot, so a flood of starts
	// cannot turn the count into unbounded database work. A new node is the
	// only outcome that costs a slot; reusing an existing row never does, but
	// which it will be is not known until the device has been read, and an
	// install at its cap is better told now than after the install step.
	if err := s.nodeProvisioner.CheckDeviceCap(ctx, audit.UserActor(cmd.AuthContext.AccountID, cmd.AuthContext.UserID)); err != nil {
		return nil, err
	}

	dialAddr, err := s.checkHostAllowed(ctx, cmd.Host)
	if err != nil {
		return nil, err
	}
	// The ticket binds the vetted address, so it is verified after the lookup.
	known, binding, err := s.adoptHostKeyParams(cmd, dialAddr)
	if err != nil {
		return nil, err
	}
	agentSock, err := s.agentSockFor(cmd.UseAgent, cmd.AuthContext.UserID)
	if err != nil {
		return nil, err
	}

	job := node_adoption_job.NewJob(cmd.AuthContext.AccountID, cmd.Host, cmd.Username)
	if err := s.jobRepo.Create(ctx, job); err != nil {
		return nil, fmt.Errorf("create adoption job: %w", err)
	}
	s.audit.RecordDetail(ctx, audit.UserActor(cmd.AuthContext.AccountID, cmd.AuthContext.UserID),
		audit_event.ActionNodeSSHAdoptStart, audit_event.TargetNodeAdoptionJob, job.ID.String(),
		adoptStartDetail(cmd, known, binding))
	s.notify(job)

	conn := connectionPlan{
		params: service.SSHConnParams{
			Host:                    cmd.Host,
			DialAddr:                dialAddr,
			Port:                    cmd.Port,
			Username:                cmd.Username,
			Password:                cmd.Password,
			UseAgent:                cmd.UseAgent,
			AgentSock:               agentSock,
			HostKeyPolicy:           "strict",
			KnownHostKeyFingerprint: known,
			ConnectTimeout:          time.Duration(s.connectTimeout()) * time.Second,
		},
	}
	// Detached context so the run survives the request lifecycle. The slot
	// reserved above is handed to the run, which releases it on exit.
	handedToRun = true
	go s.run(context.Background(), job, cmd, conn, release)
	return job, nil
}

// connectionPlan is what StartAdoption resolved for the run: the dial
// parameters, minus the private key, which is fetched inside the run so it
// lives no longer than the connection needs it.
type connectionPlan struct {
	params service.SSHConnParams
}

// run drives one adoption on the reserved slot, then writes the outcome row.
// The steps live in runSteps so that ONE place records success or failure —
// a per-step writer would be one more list somebody has to extend.
func (s *Service) run(ctx context.Context, job *node_adoption_job.Job, cmd node_ssh_adopt.SSHAdoptCommand, conn connectionPlan, release func()) {
	defer release()
	// The run bound starts once the slot is held, not while waiting for one.
	ctx, cancel := context.WithTimeout(ctx, adoptionRunTimeout)
	defer cancel()

	s.runSteps(ctx, job, cmd, conn)

	actor := audit.UserActor(cmd.AuthContext.AccountID, cmd.AuthContext.UserID)
	// context.Background(): the run context may already be past its deadline
	// (that is one way to fail), and an outcome row that never writes exactly
	// when the run went wrong is the row the trail is for.
	switch job.Status {
	case node_adoption_job.StatusCompleted:
		s.audit.RecordDetail(context.Background(), actor, audit_event.ActionNodeSSHAdoptComplete,
			audit_event.TargetNodeAdoptionJob, job.ID.String(), adoptFinishDetail(job))
	default:
		s.audit.RecordDetail(context.Background(), actor, audit_event.ActionNodeSSHAdoptFail,
			audit_event.TargetNodeAdoptionJob, job.ID.String(), adoptFinishDetail(job))
	}
}

func (s *Service) runSteps(ctx context.Context, job *node_adoption_job.Job, cmd node_ssh_adopt.SSHAdoptCommand, conn connectionPlan) {
	job.Status = node_adoption_job.StatusRunning
	s.persist(ctx, job)

	// --- connect ---
	params := conn.params
	if cmd.AuthSSHKeyID != nil && *cmd.AuthSSHKeyID != uuid.Nil {
		priv, err := s.keyResolver.GetPrivateKey(ctx, cmd.AuthContext.AccountID, *cmd.AuthSSHKeyID)
		if err != nil || priv == "" {
			s.failStep(ctx, job, node_adoption_job.StepConnect, "resolve connection key: "+errStr(err, "no private key"))
			return
		}
		params.PrivateKeyPEM = priv
	}

	job.SetStep(node_adoption_job.StepConnect, node_adoption_job.StepRunning, "")
	s.persist(ctx, job)
	rawSess, err := s.sshClient.Dial(ctx, params)
	if err != nil {
		s.failStep(ctx, job, node_adoption_job.StepConnect, err.Error())
		return
	}
	defer rawSess.Close()
	job.HostKeyFingerprint = rawSess.HostKeyFingerprint()
	job.SetStep(node_adoption_job.StepConnect, node_adoption_job.StepCompleted, rawSess.HostKeyFingerprint())
	s.persist(ctx, job)

	// Every command from here on is bounded (SSHAdoptConfig.CommandTimeoutSeconds);
	// the install step gets its own longer bound below.
	sess := s.bounded(rawSess, s.commandTimeout())

	// --- probe (arch) ---
	if err := s.runStep(ctx, job, node_adoption_job.StepProbe, func() (string, error) {
		r, err := sess.Run(ctx, "uname -m")
		if err != nil {
			return "", err
		}
		arch := strings.TrimSpace(r.Stdout)
		if arch != "aarch64" && arch != "arm64" {
			return "", fmt.Errorf("unsupported architecture %q (agent package is arm64)", arch)
		}
		return arch, nil
	}); err != nil {
		return
	}

	// Read the hardware serial (same source the agent fingerprints from) so we
	// can reconcile an accidental re-adopt of a device that is still registered.
	serial := s.readHardwareSerial(ctx, sess)

	// --- install agent ---
	// Installing the .deb needs no node record, so it runs BEFORE create_node:
	// a failure here (wrong sudo password, CDN down, dpkg error) leaves no
	// orphaned placeholder node behind.
	if err := s.runStep(ctx, job, node_adoption_job.StepInstallAgent, func() (string, error) {
		base := strings.TrimRight(strings.TrimSpace(s.cfg.AllowedCDNBaseURL), "/")
		if base == "" {
			return "", fmt.Errorf("agent CDN base URL is not configured (SSH_ADOPT_CDN_BASE_URL)")
		}
		url := base + "/cdn/agent/latest/arm64/deb"
		// The download and the dpkg/apt run are the two commands that
		// legitimately take minutes on a slow Pi (an `apt-get -f` pulling
		// dependencies over a slow uplink); the blanket command timeout would
		// fail a healthy install, so this step carries its own bound.
		install := s.bounded(rawSess, installCommandTimeout)
		dl := fmt.Sprintf("curl -fsSL %q -o /tmp/orchestrator-agent.deb || wget -qO /tmp/orchestrator-agent.deb %q", url, url)
		if r, err := install.RunSudo(ctx, dl, cmd.SudoPassword); err != nil {
			return "", err
		} else if r.ExitCode != 0 {
			return "", fmt.Errorf("download agent package: %s", firstNonEmpty(r.Stderr, r.Stdout))
		}
		inst := "dpkg -i /tmp/orchestrator-agent.deb || apt-get install -y -f"
		if r, err := install.RunSudo(ctx, inst, cmd.SudoPassword); err != nil {
			return "", err
		} else if r.ExitCode != 0 {
			return "", fmt.Errorf("install agent package: %s", firstNonEmpty(r.Stderr, r.Stdout))
		}
		return "installed from " + url, nil
	}); err != nil {
		return
	}

	// --- create (or reuse) node record ---
	// Created only once the device is confirmed installable and the token is
	// about to be written — so earlier failures never strand a node. If a node
	// with this hardware serial already exists in the account (an accidental
	// re-adopt of a device that was never deleted), reuse it instead of creating
	// a duplicate: the device re-homes to the same record on its next connect.
	var nodeID uuid.UUID
	if err := s.runStep(ctx, job, node_adoption_job.StepCreateNode, func() (string, error) {
		if existing := s.findExistingBySerial(ctx, cmd.AuthContext.AccountID, serial); existing != nil {
			if existing.ApprovalStatus == nodeDomain.ApprovalStatusRejected {
				return "", fmt.Errorf("this device matches rejected node %q (%s) — approve or delete that node first", existing.Name, existing.ID)
			}
			nodeID = existing.ID
			job.NodeID = &existing.ID
			return fmt.Sprintf("reusing existing node %q (%s) — matched hardware serial", existing.Name, existing.ID), nil
		}
		// Registering a new node: these are required here (they are optional at
		// request time because the reuse case above does not need them).
		// Only the NAME is required. The default username/password are this
		// node's optional console login, not the credentials this adoption
		// connects with (those are cmd.Username/cmd.Password above): the device
		// already runs an OS we just logged into, and its managed access comes
		// from access entries once the agent reports in.
		if strings.TrimSpace(cmd.NodeName) == "" {
			return "", fmt.Errorf("this device is not registered yet — a node name is required to create it")
		}
		createCmd := node_create.NodeCreateCommand{
			ID:                    uuid.New(),
			Name:                  cmd.NodeName,
			DefaultUsername:       cmd.DefaultUsername,
			DefaultPassword:       cmd.DefaultPassword,
			ProvisionNetworkFirst: cmd.ProvisionNetworkFirst,
			AuthContext:           cmd.AuthContext,
		}
		if cmd.NodeSSHKeyID != nil {
			createCmd.SSHKeyID = *cmd.NodeSSHKeyID
		}
		n, err := s.nodeProvisioner.Create(ctx, createCmd)
		if err != nil {
			return "", err
		}
		nodeID = n.ID
		job.NodeID = &n.ID
		// Stamp the serial read over SSH so a retry after a later-step failure
		// (or another adopt path) reconciles into this node instead of creating
		// a duplicate; the agent's first connect binds the real fingerprint.
		if serial != "" {
			if err := s.nodeProvisioner.StampWebUSBPlaceholder(ctx, n.ID, cmd.AuthContext.AccountID, serial); err != nil {
				if errors.Is(err, app_errors.ErrHardwareSerialConflict) {
					// Fail the adoption BEFORE the token is written: the device's
					// serial belongs to another tenant, so finishing would strand a
					// forever-rejected agent. Roll back the just-created node.
					// force: rolling back a node that has no device behind it yet.
					if derr := s.nodeProvisioner.Delete(ctx, cmd.AuthContext.AccountID, cmd.AuthContext.UserID, n.ID, true); derr != nil {
						s.logger.Warn("failed to roll back node after serial conflict", "node_id", n.ID, "error", derr)
					}
					return "", fmt.Errorf("this device's hardware serial is already registered to another organization; if you recently acquired this device, ask the previous owner to remove it from their account, or contact support")
				}
				s.logger.Warn("failed to stamp hardware serial on new node", "node_id", n.ID, "error", err)
			}
		}
		// The runner performs the network-first EEPROM flash itself (last step);
		// disarm the connect-time one-shot so the device is not flashed twice
		// (or, if it boots from NVMe, left with a latent surprise reflash).
		if cmd.ProvisionNetworkFirst {
			if err := s.nodeProvisioner.ClearAutoProvisionEEPROMPending(ctx, n.ID, cmd.AuthContext.AccountID); err != nil {
				s.logger.Warn("failed to disarm auto EEPROM provision", "node_id", n.ID, "error", err)
			}
		}
		return n.ID.String(), nil
	}); err != nil {
		return
	}

	// --- write adoption file ---
	if err := s.runStep(ctx, job, node_adoption_job.StepWriteAdoption, func() (string, error) {
		adoptionPath := s.adoptionFilePath()
		if r, err := sess.RunSudo(ctx, "mkdir -p "+shellQuote(path.Dir(adoptionPath)), cmd.SudoPassword); err != nil {
			return "", err
		} else if r.ExitCode != 0 {
			return "", fmt.Errorf("mkdir: %s", r.Stderr)
		}
		blob, err := s.nodeProvisioner.GetAdoptionInfo(ctx, cmd.AuthContext.AccountID, nodeID, s.platformURL, s.grpcEndpoint)
		if err != nil {
			return "", err
		}
		if err := sess.WriteFile(ctx, adoptionPath, []byte(blob+"\n"), 0o600, cmd.SudoPassword); err != nil {
			return "", err
		}
		return adoptionPath, nil
	}); err != nil {
		return
	}

	// --- enable + start service ---
	if err := s.runStep(ctx, job, node_adoption_job.StepStartService, func() (string, error) {
		// restart (not `enable --now`) so a device that already runs an agent
		// re-reads the freshly written adoption file and re-adopts.
		start := "systemctl daemon-reload && systemctl enable orchestrator-agent && systemctl restart orchestrator-agent"
		if r, err := sess.RunSudo(ctx, start, cmd.SudoPassword); err != nil {
			return "", err
		} else if r.ExitCode != 0 {
			return "", fmt.Errorf("start service: %s", firstNonEmpty(r.Stderr, r.Stdout))
		}
		return "orchestrator-agent enabled", nil
	}); err != nil {
		return
	}

	// SSH work is done; the rest is platform-side.
	_ = rawSess.Close()

	// --- wait for the agent to connect ---
	if err := s.runStep(ctx, job, node_adoption_job.StepWaitAgent, func() (string, error) {
		return s.waitForAgent(ctx, nodeID)
	}); err != nil {
		return
	}

	// --- EEPROM (best-effort; adoption already succeeded) ---
	s.maybeFlashEEPROM(ctx, job, cmd, nodeID)

	job.Complete()
	s.persist(ctx, job)
	s.logger.Info("ssh adoption completed", "job_id", job.ID, "node_id", nodeID)
}

// waitForAgent polls until the agent connects over gRPC or the timeout elapses.
func (s *Service) waitForAgent(ctx context.Context, nodeID uuid.UUID) (string, error) {
	deadline := time.Now().Add(time.Duration(s.agentWaitTimeout()) * time.Second)
	for {
		if s.comms.IsNodeConnected(nodeID.String()) {
			return "agent connected", nil
		}
		if time.Now().After(deadline) {
			return "", fmt.Errorf("agent did not connect within %ds", s.agentWaitTimeout())
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(agentPollInterval):
		}
	}
}

// maybeFlashEEPROM flashes a network-first EEPROM if requested. It is best-effort:
// a failure marks the step failed but does NOT fail the job (the node is already
// adopted; the EEPROM can be flashed later from the node's card).
func (s *Service) maybeFlashEEPROM(ctx context.Context, job *node_adoption_job.Job, cmd node_ssh_adopt.SSHAdoptCommand, nodeID uuid.UUID) {
	if !cmd.ProvisionNetworkFirst {
		job.SkipStep(node_adoption_job.StepEEPROM, "not requested")
		s.persist(ctx, job)
		return
	}
	if s.eepromProvision == nil || !s.eepromProvision.Enabled() {
		job.SkipStep(node_adoption_job.StepEEPROM, "EEPROM signer not configured")
		s.persist(ctx, job)
		return
	}
	job.SetStep(node_adoption_job.StepEEPROM, node_adoption_job.StepRunning, "")
	s.persist(ctx, job)

	// The device reports SoC/serial only after the agent sends system info.
	node, err := s.waitForSystemInfo(ctx, cmd.AuthContext.AccountID, nodeID)
	if err != nil {
		job.SetStep(node_adoption_job.StepEEPROM, node_adoption_job.StepFailed, err.Error())
		s.persist(ctx, job)
		return
	}
	if err := s.eepromProvision.ProvisionNetworkFirstEEPROM(ctx, cmd.AuthContext.AccountID, cmd.AuthContext.UserID, node, nodeDomain.NetworkFirstBootOrder, s.platformURL, s.grpcEndpoint); err != nil {
		job.SetStep(node_adoption_job.StepEEPROM, node_adoption_job.StepFailed, err.Error())
		s.persist(ctx, job)
		return
	}
	job.SetStep(node_adoption_job.StepEEPROM, node_adoption_job.StepCompleted, "flash dispatched; device will reboot")
	s.persist(ctx, job)
}

// waitForSystemInfo polls until the node reports enough system info to flash
// (SoC + serial known) or times out.
func (s *Service) waitForSystemInfo(ctx context.Context, accountID, nodeID uuid.UUID) (*nodeDomain.Node, error) {
	deadline := time.Now().Add(systemInfoWaitTimeout)
	for {
		n, err := s.nodeProvisioner.GetByIDAndAccount(ctx, nodeID, accountID)
		if err == nil && n.SoC() != "" && n.GetHardwareSerial() != "" {
			return n, nil
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("node did not report hardware info in time")
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(agentPollInterval):
		}
	}
}

// runStep runs a step, persisting+notifying on running and completion; on error
// it marks the step + job failed and returns the error so the caller stops.
func (s *Service) runStep(ctx context.Context, job *node_adoption_job.Job, key string, fn func() (string, error)) error {
	job.SetStep(key, node_adoption_job.StepRunning, "")
	s.persist(ctx, job)
	msg, err := fn()
	if err != nil {
		s.failStep(ctx, job, key, err.Error())
		return err
	}
	job.SetStep(key, node_adoption_job.StepCompleted, msg)
	s.persist(ctx, job)
	return nil
}

func (s *Service) failStep(ctx context.Context, job *node_adoption_job.Job, key, msg string) {
	job.SetStep(key, node_adoption_job.StepFailed, msg)
	job.Fail(fmt.Sprintf("%s: %s", key, msg))
	s.persist(ctx, job)
	s.logger.Warn("ssh adoption step failed", "job_id", job.ID, "step", key, "error", msg)
}

func (s *Service) persist(ctx context.Context, job *node_adoption_job.Job) {
	if err := s.jobRepo.Update(ctx, job); err != nil {
		s.logger.Error("persist adoption job", "job_id", job.ID, "error", err)
	}
	s.notify(job)
}

func (s *Service) notify(job *node_adoption_job.Job) {
	if s.ws == nil {
		return
	}
	// Account-wide holders of node.ssh_adopt only — the action
	// GET /node-adoption-jobs/:id demands. The frame carries the target host,
	// the SSH username and the host-key fingerprint of a device being adopted,
	// which the REST read refuses to every member below that action.
	//
	// Account-global rather than scoped to the node's OU because a job has no
	// OU of its own and its node does not exist for most of the run. Nothing
	// consumes this frame today (the Add Node panel polls the job instead);
	// when a consumer appears, route it to the operator who started the run.
	msg := portsws.NewAccountMessage(job.AccountID, authz.ActionNodeSSHAdopt, portsws.MessageWrapper{
		Type:    "node_adoption_job_updated",
		Payload: results.NewNodeAdoptionJobResult(job),
	})
	_ = s.ws.Send(msg)
}

func (s *Service) hostKeyPolicy() string {
	if s.cfg.HostKeyPolicy == "" {
		return "tofu"
	}
	return s.cfg.HostKeyPolicy
}

func (s *Service) connectTimeout() int {
	if s.cfg.ConnectTimeoutSeconds <= 0 {
		return 15
	}
	return s.cfg.ConnectTimeoutSeconds
}

func (s *Service) commandTimeout() time.Duration {
	if s.cfg.CommandTimeoutSeconds <= 0 {
		return defaultCommandTimeout
	}
	return time.Duration(s.cfg.CommandTimeoutSeconds) * time.Second
}

// bounded wraps a session so every Run/RunSudo/WriteFile carries its own
// deadline. Wrapping the SESSION rather than each call site is what keeps a
// command added later from being the one that runs unbounded; a step needing
// a different bound wraps the raw session again with its own.
func (s *Service) bounded(sess service.SSHSession, timeout time.Duration) service.SSHSession {
	return &boundedSession{SSHSession: sess, timeout: timeout}
}

type boundedSession struct {
	service.SSHSession
	timeout time.Duration
}

func (b *boundedSession) Run(ctx context.Context, cmd string) (service.SSHRunResult, error) {
	ctx, cancel := context.WithTimeout(ctx, b.timeout)
	defer cancel()
	return b.SSHSession.Run(ctx, cmd)
}

func (b *boundedSession) RunSudo(ctx context.Context, cmd string, sudoPassword string) (service.SSHRunResult, error) {
	ctx, cancel := context.WithTimeout(ctx, b.timeout)
	defer cancel()
	return b.SSHSession.RunSudo(ctx, cmd, sudoPassword)
}

func (b *boundedSession) WriteFile(ctx context.Context, path string, content []byte, mode uint32, sudoPassword string) error {
	ctx, cancel := context.WithTimeout(ctx, b.timeout)
	defer cancel()
	return b.SSHSession.WriteFile(ctx, path, content, mode, sudoPassword)
}

// authKind names HOW a connection authenticated for the audit trail: the
// method and, for a connection key, its id. Never the password, the key
// material or the sudo password — AuditEvent.Detail outlives every key
// rotation as plaintext.
func authKind(password string, keyID *uuid.UUID, useAgent bool) string {
	switch {
	case useAgent:
		return "agent"
	case keyID != nil && *keyID != uuid.Nil:
		return "key:" + keyID.String()
	case password != "":
		return "password"
	}
	return "none"
}

func hostKeyDetail(known string) string {
	if known == "" {
		return "first-use"
	}
	return known
}

// adoptStartDetail names the key the adoption will insist on and where that
// expectation came from ("ticket" or "fingerprint"). The ticket itself is
// never written: it is a bearer proof for the next fifteen minutes.
func adoptStartDetail(cmd node_ssh_adopt.SSHAdoptCommand, known, binding string) string {
	return fmt.Sprintf("host=%s port=%d username=%s auth=%s host_key=%s binding=%s",
		cmd.Host, portOrDefault(cmd.Port), cmd.Username,
		authKind(cmd.Password, cmd.AuthSSHKeyID, cmd.UseAgent), known, binding)
}

func adoptFinishDetail(job *node_adoption_job.Job) string {
	detail := fmt.Sprintf("host=%s host_key=%s", job.Host, firstNonEmpty(job.HostKeyFingerprint, "unobserved"))
	if job.NodeID != nil {
		detail += " node_id=" + job.NodeID.String()
	}
	for _, st := range job.Steps {
		if st.Status == node_adoption_job.StepFailed {
			detail += " step=" + st.Key
			break
		}
	}
	return detail
}

func portOrDefault(port int) int {
	if port == 0 {
		return 22
	}
	return port
}

func (s *Service) agentWaitTimeout() int {
	if s.cfg.AgentWaitTimeoutSeconds <= 0 {
		return 300
	}
	return s.cfg.AgentWaitTimeoutSeconds
}

func (s *Service) adoptionFilePath() string {
	if strings.TrimSpace(s.cfg.AdoptionFilePath) == "" {
		return "/etc/orchestrator/node-adoption.txt"
	}
	return s.cfg.AdoptionFilePath
}

// Preflight runs a non-destructive probe: dial SSH, capture the host-key
// fingerprint, and check arch/sudo/CDN reachability. It never writes to or
// reboots the device. A connection failure is reported in the result (Reachable
// = false), not returned as an error, so the UI can render it.
func (s *Service) Preflight(ctx context.Context, cmd node_ssh_preflight.SSHPreflightCommand) (results.SSHPreflightResult, error) {
	if !s.cfg.Enabled {
		return results.SSHPreflightResult{}, ErrSSHAdoptDisabled
	}
	if err := cmd.Validate(); err != nil {
		return results.SSHPreflightResult{}, err
	}
	policy, known, err := s.preflightHostKeyParams(cmd)
	if err != nil {
		return results.SSHPreflightResult{}, err
	}
	// The slot is taken BEFORE the DNS lookup, the key decryption and the
	// dial: those are what the bound protects, so a refused probe must have
	// spent none of them. The deferred release covers every exit below.
	release, err := s.preflightGate.acquire(cmd.AuthContext.AccountID)
	if err != nil {
		return results.SSHPreflightResult{}, err
	}
	defer release()
	dialAddr, err := s.checkHostAllowed(ctx, cmd.Host)
	if err != nil {
		return results.SSHPreflightResult{}, err
	}
	agentSock, err := s.agentSockFor(cmd.UseAgent, cmd.AuthContext.UserID)
	if err != nil {
		return results.SSHPreflightResult{}, err
	}

	ctx, cancel := context.WithTimeout(ctx, preflightTimeout)
	defer cancel()

	params := service.SSHConnParams{
		Host:                    cmd.Host,
		DialAddr:                dialAddr,
		Port:                    cmd.Port,
		Username:                cmd.Username,
		Password:                cmd.Password,
		UseAgent:                cmd.UseAgent,
		AgentSock:               agentSock,
		HostKeyPolicy:           policy,
		KnownHostKeyFingerprint: known,
		ConnectTimeout:          time.Duration(s.connectTimeout()) * time.Second,
	}
	if cmd.SSHKeyID != nil && *cmd.SSHKeyID != uuid.Nil {
		priv, err := s.keyResolver.GetPrivateKey(ctx, cmd.AuthContext.AccountID, *cmd.SSHKeyID)
		if err != nil {
			return results.SSHPreflightResult{}, fmt.Errorf("resolve connection key: %w", err)
		}
		if priv == "" {
			return results.SSHPreflightResult{}, fmt.Errorf("selected SSH key has no private key (not a connection keypair)")
		}
		params.PrivateKeyPEM = priv
	}

	actor := audit.UserActor(cmd.AuthContext.AccountID, cmd.AuthContext.UserID)
	target := net.JoinHostPort(cmd.Host, fmt.Sprint(portOrDefault(cmd.Port)))
	auth := authKind(cmd.Password, cmd.SSHKeyID, cmd.UseAgent)

	rawSess, err := s.sshClient.Dial(ctx, params)
	if err != nil {
		var mismatch *service.HostKeyMismatchError
		if errors.As(err, &mismatch) {
			// The handshake stopped at the host key, before authentication, so
			// the device got no credential and nothing below was probed. The
			// operator decides whether the new key is the reinstall they
			// expect: re-running the test with accept_new_host_key says so.
			// No ticket: nothing was verified.
			s.audit.RecordDetail(ctx, actor, audit_event.ActionNodeSSHPreflight, audit_event.TargetSSHHost, target,
				fmt.Sprintf("host=%s port=%d username=%s auth=%s host_key=%s expected=%s host_key_changed=true",
					cmd.Host, portOrDefault(cmd.Port), cmd.Username, auth, mismatch.Got, mismatch.Want))
			return results.SSHPreflightResult{
				Reachable:                  false,
				HostKeyChanged:             true,
				HostKeyFingerprint:         mismatch.Got,
				ExpectedHostKeyFingerprint: mismatch.Want,
				Error: fmt.Sprintf("the device's SSH host key changed since the last connection test (was %s, now %s). "+
					"If the device was reinstalled, accept the new key and run the connection test again. "+
					"Otherwise, check that %s is the device you expect before continuing.",
					mismatch.Want, mismatch.Got, cmd.Host),
			}, nil
		}
		return results.SSHPreflightResult{Reachable: false, Error: err.Error()}, nil
	}
	defer rawSess.Close()
	sess := s.bounded(rawSess, s.commandTimeout())
	observed := rawSess.HostKeyFingerprint()

	// How this probe came to trust the key it saw, for the result and the
	// trail. A reset is recorded with BOTH fingerprints: the operator replaced
	// a key on purpose, and the trail must show which one went.
	reset := cmd.AcceptNewHostKey && cmd.ExpectedHostKeyFingerprint != "" && observed != cmd.ExpectedHostKeyFingerprint
	firstContact := cmd.ExpectedHostKeyFingerprint == ""
	// An accept-new-key probe dials TOFU (known == ""), but when the device
	// still presents the expected key nothing was accepted on first use — the
	// trail records the match, not "first-use".
	pinned := known
	if pinned == "" && cmd.ExpectedHostKeyFingerprint != "" && observed == cmd.ExpectedHostKeyFingerprint {
		pinned = cmd.ExpectedHostKeyFingerprint
	}
	detail := fmt.Sprintf("host=%s port=%d username=%s auth=%s host_key=%s pinned=%s",
		cmd.Host, portOrDefault(cmd.Port), cmd.Username, auth, observed, hostKeyDetail(pinned))
	if reset {
		detail = fmt.Sprintf("host=%s port=%d username=%s auth=%s host_key_reset=true previous=%s host_key=%s",
			cmd.Host, portOrDefault(cmd.Port), cmd.Username, auth, cmd.ExpectedHostKeyFingerprint, observed)
	}
	s.audit.RecordDetail(ctx, actor, audit_event.ActionNodeSSHPreflight, audit_event.TargetSSHHost, target, detail)

	ticket, ticketExpiresAt := s.issueTicket(cmd, dialAddr, observed)
	res := results.SSHPreflightResult{
		Reachable:              true,
		HostKeyFingerprint:     observed,
		FirstContact:           firstContact,
		HostKeyReset:           reset,
		HostKeyTicket:          ticket,
		HostKeyTicketExpiresAt: ticketExpiresAt,
	}
	if reset {
		res.ExpectedHostKeyFingerprint = cmd.ExpectedHostKeyFingerprint
	}
	res.Arch = s.runTrimmed(ctx, sess, "uname -m")
	res.ArchSupported = res.Arch == "aarch64" || res.Arch == "arm64"
	res.User = s.runTrimmed(ctx, sess, "whoami")
	res.SudoMode = s.detectSudo(ctx, sess, res.User, cmd.SudoPassword)
	// Warn if this device is already registered (accidental re-adopt): adoption
	// will reuse that node rather than create a duplicate.
	if existing := s.findExistingBySerial(ctx, cmd.AuthContext.AccountID, s.readHardwareSerial(ctx, sess)); existing != nil {
		res.ExistingNodeID = existing.ID.String()
		res.ExistingNodeName = existing.Name
		res.ExistingNodeStatus = string(existing.Status)
	}
	if !res.ArchSupported {
		res.Notes = "the agent Debian package targets arm64/aarch64; this device reports " + res.Arch
	}
	if base := strings.TrimSpace(s.cfg.AllowedCDNBaseURL); base != "" {
		res.CDNChecked = true
		res.CDNReachable = s.checkCDN(ctx, sess, base)
	}
	return res, nil
}

// serialRe matches the 16-hex Raspberry Pi hardware serial the agent fingerprints
// from /proc/cpuinfo, so an SSH read yields the identical value.
var serialRe = regexp.MustCompile(`[0-9a-f]{16}`)

// readHardwareSerial reads the device hardware serial over SSH (best-effort; ""
// when the device is not a Pi or has no serial). /proc/cpuinfo is world-readable,
// so no sudo is needed.
func (s *Service) readHardwareSerial(ctx context.Context, sess service.SSHSession) string {
	r, err := sess.Run(ctx, "grep -m1 -E '^Serial' /proc/cpuinfo")
	if err != nil {
		return ""
	}
	return serialRe.FindString(strings.ToLower(r.Stdout))
}

// findExistingBySerial returns an already-registered node with this hardware
// serial in the account, or nil (no match / lookup unavailable / error).
func (s *Service) findExistingBySerial(ctx context.Context, accountID uuid.UUID, serial string) *nodeDomain.Node {
	if serial == "" || s.nodeProvisioner == nil {
		return nil
	}
	n, err := s.nodeProvisioner.GetByHardwareSerial(ctx, accountID, serial)
	if err != nil || n == nil {
		return nil
	}
	return n
}

func (s *Service) detectSudo(ctx context.Context, sess service.SSHSession, user, sudoPassword string) string {
	if user == "root" {
		return "root"
	}
	if r, err := sess.Run(ctx, "sudo -n true"); err == nil && r.ExitCode == 0 {
		return "nopasswd"
	}
	if sudoPassword != "" {
		// Actually exercise the password so a wrong one is caught here at
		// preflight instead of half-way through adoption on install_agent.
		if r, err := sess.RunSudo(ctx, "true", sudoPassword); err == nil && r.ExitCode == 0 {
			return "password"
		}
		return "bad_password"
	}
	return "none"
}

func (s *Service) checkCDN(ctx context.Context, sess service.SSHSession, base string) bool {
	cmd := fmt.Sprintf("curl -fsS -o /dev/null --max-time 10 %q || wget -q --spider --timeout=10 %q", base, base)
	r, err := sess.Run(ctx, cmd)
	return err == nil && r.ExitCode == 0
}

func (s *Service) runTrimmed(ctx context.Context, sess service.SSHSession, cmd string) string {
	r, err := sess.Run(ctx, cmd)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(r.Stdout)
}

func errStr(err error, fallback string) string {
	if err != nil {
		return err.Error()
	}
	return fallback
}

func firstNonEmpty(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
