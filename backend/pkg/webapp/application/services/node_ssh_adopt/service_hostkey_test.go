package node_ssh_adopt

// Host-key trust is server-enforced. A preflight
// hands the operator a ticket; the adoption must present that ticket or an
// out-of-band fingerprint, and never accepts a key on first use.

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/config"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/node_ssh_adopt"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/out/results"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/audit_event"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node_adoption_job"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/ssh_host_key_ticket"
)

// pinClock replaces the service's ticket issuer with one over a fixed key and
// a settable clock, so expiry can be tested without waiting.
func pinClock(t *testing.T, h *harness) *time.Time {
	t.Helper()
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	key := make([]byte, ssh_host_key_ticket.KeyBytes)
	for i := range key {
		key[i] = 42
	}
	iss, err := ssh_host_key_ticket.NewIssuer(key, ssh_host_key_ticket.DefaultTTL)
	if err != nil {
		t.Fatal(err)
	}
	h.svc.tickets = iss.WithClock(func() time.Time { return now })
	return &now
}

// ---- the preflight issues, the adopt presents -------------------------------

func TestFirstContactPreflightIssuesATicketAndSaysItIsFirstContact(t *testing.T) {
	h := newHarness(t, nil)
	res := h.preflightOK(t)
	if !res.FirstContact {
		t.Error("FirstContact = false on a probe with no expectation")
	}
	if res.HostKeyTicket == "" {
		t.Fatal("no host_key_ticket on a reachable preflight")
	}
	if res.HostKeyTicketExpiresAt == nil || time.Until(*res.HostKeyTicketExpiresAt) < 14*time.Minute {
		t.Errorf("ticket expiry = %v, want about 15 minutes out", res.HostKeyTicketExpiresAt)
	}
	if res.HostKeyFingerprint != serverKey {
		t.Errorf("fingerprint = %q", res.HostKeyFingerprint)
	}
}

func TestMatchingPreflightIsNotFirstContactAndRefreshesTheTicket(t *testing.T) {
	h := newHarness(t, nil)
	cmd := h.preflightCmd()
	cmd.ExpectedHostKeyFingerprint = serverKey
	res, err := h.svc.Preflight(context.Background(), cmd)
	if err != nil || !res.Reachable {
		t.Fatalf("Preflight = %+v, %v", res, err)
	}
	if res.FirstContact {
		t.Error("FirstContact = true on a probe whose expectation matched")
	}
	if res.HostKeyTicket == "" {
		t.Error("a matching probe must refresh the ticket")
	}
	if d := h.ssh.Dials(); len(d) != 1 || d[0].HostKeyPolicy != "strict" || d[0].KnownHostKeyFingerprint != serverKey {
		t.Errorf("dials = %+v, want one strict dial against the expectation", d)
	}
}

func TestAdoptWithTheTicketDialsStrictAgainstTheKeyInsideIt(t *testing.T) {
	h := newHarness(t, nil)
	res := h.preflightOK(t)

	job, err := h.svc.StartAdoption(context.Background(), h.adoptWithTicket(res.HostKeyTicket))
	if err != nil {
		t.Fatalf("StartAdoption: %v", err)
	}
	done := h.waitJob(t, job.ID)
	if done.Status != node_adoption_job.StatusCompleted {
		t.Fatalf("job = %s (%s)", done.Status, done.Error)
	}
	dials := h.ssh.Dials()
	adopt := dials[len(dials)-1]
	if adopt.HostKeyPolicy != "strict" || adopt.KnownHostKeyFingerprint != serverKey {
		t.Errorf("adopt dial = policy %q known %q, want strict against the ticket's key", adopt.HostKeyPolicy, adopt.KnownHostKeyFingerprint)
	}
	start, ok := h.audit.Find(audit_event.ActionNodeSSHAdoptStart)
	if !ok {
		t.Fatalf("no %s audit row; actions = %v", audit_event.ActionNodeSSHAdoptStart, h.audit.Actions())
	}
	for _, want := range []string{"host_key=" + serverKey, "binding=ticket"} {
		if !strings.Contains(start.Detail, want) {
			t.Errorf("start detail %q lacks %q", start.Detail, want)
		}
	}
	if strings.Contains(start.Detail, res.HostKeyTicket) {
		t.Error("the audit detail carries the ticket itself")
	}
	assertNoCredentialMaterial(t, h.audit.Events())
}

func TestAdoptWithAFingerprintIsTheOutOfBandPath(t *testing.T) {
	h := newHarness(t, nil)
	job, err := h.svc.StartAdoption(context.Background(), h.adoptCmd())
	if err != nil {
		t.Fatalf("StartAdoption: %v", err)
	}
	if done := h.waitJob(t, job.ID); done.Status != node_adoption_job.StatusCompleted {
		t.Fatalf("job = %s (%s)", done.Status, done.Error)
	}
	d := h.ssh.Dials()
	if len(d) != 1 || d[0].HostKeyPolicy != "strict" || d[0].KnownHostKeyFingerprint != serverKey {
		t.Errorf("dials = %+v, want one strict dial against the fingerprint", d)
	}
	start, _ := h.audit.Find(audit_event.ActionNodeSSHAdoptStart)
	if !strings.Contains(start.Detail, "binding=fingerprint") {
		t.Errorf("start detail %q lacks binding=fingerprint", start.Detail)
	}
}

func TestAdoptWithWrongFingerprintFailsAtConnectWithoutRunningAnything(t *testing.T) {
	h := newHarness(t, nil)
	cmd := h.adoptCmd()
	cmd.HostKeyFingerprint = otherKey
	job, err := h.svc.StartAdoption(context.Background(), cmd)
	if err != nil {
		t.Fatalf("StartAdoption: %v", err)
	}
	done := h.waitJob(t, job.ID)
	if done.Status != node_adoption_job.StatusFailed || !strings.Contains(done.Error, "host key") {
		t.Fatalf("job = %s (%s), want failed at the host key", done.Status, done.Error)
	}
	if runs := h.ssh.Runs(); len(runs) != 0 {
		t.Errorf("commands ran after a host-key refusal: %v", runs)
	}
	fail, ok := h.audit.Find(audit_event.ActionNodeSSHAdoptFail)
	if !ok || !strings.Contains(fail.Detail, "step=connect") {
		t.Errorf("failure row = %+v, want step=connect", fail)
	}
	assertNoCredentialMaterial(t, h.audit.Events())
}

// ---- the adoption never TOFUs ----------------------------------------------

func TestAdoptWithNeitherTicketNorFingerprintIsRefusedBeforeAnyDial(t *testing.T) {
	for _, policy := range []string{"", "tofu", "strict"} {
		t.Run("policy="+policy, func(t *testing.T) {
			h := newHarness(t, func(c *config.SSHAdoptConfig) { c.HostKeyPolicy = policy })
			cmd := h.adoptCmd()
			cmd.HostKeyFingerprint = ""
			_, err := h.svc.StartAdoption(context.Background(), cmd)
			var required *HostKeyBindingRequiredError
			if !errors.As(err, &required) {
				t.Fatalf("err = %v, want HostKeyBindingRequiredError", err)
			}
			if required.HTTPStatus() != http.StatusBadRequest || required.ErrorCode() != "host_key_binding_required" {
				t.Errorf("status/code = %d/%s", required.HTTPStatus(), required.ErrorCode())
			}
			if len(h.ssh.Dials()) != 0 {
				t.Error("a dial happened despite the refusal")
			}
			if h.jobs.count() != 0 {
				t.Error("a job row was created despite the refusal")
			}
			if _, ok := h.audit.Find(audit_event.ActionNodeSSHAdoptStart); ok {
				t.Error("a start row was written for a refused adoption")
			}
		})
	}
}

// The substitution scenario. Key A is seen; the network drops (a generic failed
// preflight with no fingerprint at all); an attacker with key B now answers
// on host:port. Whatever the client does next, no credential reaches B.
func TestDroppedConnectionThenADifferentKeyNeverReceivesCredentials(t *testing.T) {
	h := newHarness(t, nil)
	first := h.preflightOK(t)

	// The connection drops: a probe that sees no key at all.
	h.ssh.setDialErr(errors.New("dial 10.0.0.3:22: connection refused"))
	dropped, err := h.svc.Preflight(context.Background(), h.preflightCmd())
	if err != nil || dropped.Reachable || dropped.HostKeyChanged || dropped.HostKeyTicket != "" {
		t.Fatalf("dropped probe = %+v, %v; want unreachable with no ticket", dropped, err)
	}

	// Key B takes the address.
	h.ssh.setDialErr(nil)
	h.ssh.setHostKey(otherKey)

	// (1) A retry that still carries the earlier expectation is a change.
	retry := h.preflightCmd()
	retry.ExpectedHostKeyFingerprint = first.HostKeyFingerprint
	res, err := h.svc.Preflight(context.Background(), retry)
	if err != nil || !res.HostKeyChanged || res.HostKeyTicket != "" {
		t.Fatalf("retry = %+v, %v; want host_key_changed and no ticket", res, err)
	}

	// (2) Adopting with the ticket from key A connects strictly against A and
	// fails at the host key, before any command.
	runsBefore := len(h.ssh.Runs())
	job, err := h.svc.StartAdoption(context.Background(), h.adoptWithTicket(first.HostKeyTicket))
	if err != nil {
		t.Fatalf("StartAdoption: %v", err)
	}
	if done := h.waitJob(t, job.ID); done.Status != node_adoption_job.StatusFailed || !strings.Contains(done.Error, "host key") {
		t.Fatalf("job = %s (%s), want failed at the host key", done.Status, done.Error)
	}
	if runs := h.ssh.Runs(); len(runs) != runsBefore {
		t.Errorf("commands ran against the new key: %v", runs[runsBefore:])
	}

	// (3) Adopting with no binding at all is refused without a dial.
	dialsBefore := len(h.ssh.Dials())
	bare := h.adoptCmd()
	bare.HostKeyFingerprint = ""
	var required *HostKeyBindingRequiredError
	if _, err := h.svc.StartAdoption(context.Background(), bare); !errors.As(err, &required) {
		t.Fatalf("bare adopt err = %v, want HostKeyBindingRequiredError", err)
	}
	if len(h.ssh.Dials()) != dialsBefore {
		t.Error("the bare adopt dialed")
	}
}

// ---- ticket binding ----------------------------------------------------------

func TestStaleTicketIsRefusedAndAReplayAfterExpiryToo(t *testing.T) {
	h := newHarness(t, nil)
	now := pinClock(t, h)
	res := h.preflightOK(t)

	// Still good just inside the window.
	*now = now.Add(ssh_host_key_ticket.DefaultTTL - time.Second)
	job, err := h.svc.StartAdoption(context.Background(), h.adoptWithTicket(res.HostKeyTicket))
	if err != nil {
		t.Fatalf("StartAdoption inside the window: %v", err)
	}
	h.waitJob(t, job.ID)

	// Replayed after expiry: refused, and the reason says why.
	*now = now.Add(2 * time.Second)
	_, err = h.svc.StartAdoption(context.Background(), h.adoptWithTicket(res.HostKeyTicket))
	var required *HostKeyBindingRequiredError
	if !errors.As(err, &required) {
		t.Fatalf("err = %v, want HostKeyBindingRequiredError", err)
	}
	if !strings.Contains(required.Error(), "15 minutes") {
		t.Errorf("message %q should say how old is too old", required.Error())
	}
	if h.jobs.count() != 1 {
		t.Errorf("jobs = %d, want only the one from inside the window", h.jobs.count())
	}
}

func TestTicketIsBoundToHostPortAccountAndUser(t *testing.T) {
	h := newHarness(t, nil)
	res := h.preflightOK(t)

	cases := map[string]struct {
		mutate func(cmd *node_ssh_adopt.SSHAdoptCommand)
		want   string
	}{
		"host": {func(c *node_ssh_adopt.SSHAdoptCommand) { c.Host = "10.0.0.4" }, "different host"},
		"port": {func(c *node_ssh_adopt.SSHAdoptCommand) { c.Port = 2222 }, "different port"},
		"account": {func(c *node_ssh_adopt.SSHAdoptCommand) {
			c.AuthContext = commands.NewAuthContext(uuid.New(), h.actor.UserID)
		}, "different account"},
		"user": {func(c *node_ssh_adopt.SSHAdoptCommand) {
			c.AuthContext = commands.NewAuthContext(h.actor.AccountID, uuid.New())
		}, "different user"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			cmd := h.adoptWithTicket(res.HostKeyTicket)
			tc.mutate(&cmd)
			dialsBefore := len(h.ssh.Dials())
			_, err := h.svc.StartAdoption(context.Background(), cmd)
			var required *HostKeyBindingRequiredError
			if !errors.As(err, &required) {
				t.Fatalf("err = %v, want HostKeyBindingRequiredError", err)
			}
			if !strings.Contains(required.Error(), tc.want) {
				t.Errorf("message %q should name the %s", required.Error(), name)
			}
			if len(h.ssh.Dials()) != dialsBefore {
				t.Error("a dial happened despite the refusal")
			}
		})
	}
	if h.jobs.count() != 0 {
		t.Errorf("jobs = %d, want none", h.jobs.count())
	}
}

func TestTicketFromAnotherProcessIsRefused(t *testing.T) {
	a := newHarness(t, nil)
	b := newHarness(t, nil)
	res := a.preflightOK(t)
	cmd := b.adoptWithTicket(res.HostKeyTicket)
	cmd.AuthContext = a.actor
	var required *HostKeyBindingRequiredError
	if _, err := b.svc.StartAdoption(context.Background(), cmd); !errors.As(err, &required) {
		t.Fatalf("err = %v, want HostKeyBindingRequiredError (a restart invalidates tickets)", err)
	}
}

func TestTicketUnderAnAllowlistIsBoundToTheVettedAddress(t *testing.T) {
	h := newHarness(t, func(c *config.SSHAdoptConfig) { c.AllowedHostCIDRs = []string{"10.0.0.0/8"} })
	h.svc.resolver = fakeResolver{hosts: map[string][]string{"pi.lan": {"10.0.0.7"}}}
	pf := h.preflightCmd()
	pf.Host = "pi.lan"
	res, err := h.svc.Preflight(context.Background(), pf)
	if err != nil || !res.Reachable {
		t.Fatalf("Preflight = %+v, %v", res, err)
	}

	// The name now resolves elsewhere inside the allowlist: the ticket was
	// for the address the operator's test actually reached.
	h.svc.resolver = fakeResolver{hosts: map[string][]string{"pi.lan": {"10.0.0.8"}}}
	cmd := h.adoptWithTicket(res.HostKeyTicket)
	cmd.Host = "pi.lan"
	_, err = h.svc.StartAdoption(context.Background(), cmd)
	var required *HostKeyBindingRequiredError
	if !errors.As(err, &required) {
		t.Fatalf("err = %v, want HostKeyBindingRequiredError", err)
	}
	if !strings.Contains(required.Error(), "different address") {
		t.Errorf("message %q should name the address change", required.Error())
	}

	// Same address again: accepted.
	h.svc.resolver = fakeResolver{hosts: map[string][]string{"pi.lan": {"10.0.0.7"}}}
	job, err := h.svc.StartAdoption(context.Background(), cmd)
	if err != nil {
		t.Fatalf("StartAdoption with the vetted address: %v", err)
	}
	if done := h.waitJob(t, job.ID); done.Status != node_adoption_job.StatusCompleted {
		t.Fatalf("job = %s (%s)", done.Status, done.Error)
	}
}

func TestTicketAndFingerprintTogetherAreAValidationError(t *testing.T) {
	h := newHarness(t, nil)
	res := h.preflightOK(t)
	cmd := h.adoptCmd()
	cmd.HostKeyTicket = res.HostKeyTicket
	if err := cmd.Validate(); err == nil || !strings.Contains(err.Error(), "not both") {
		t.Fatalf("Validate = %v, want a not-both refusal", err)
	}
}

// ---- policy governs the PREFLIGHT's first contact only ---------------------

func TestStrictServerRefusesPreflightWithoutFingerprint(t *testing.T) {
	h := newHarness(t, func(c *config.SSHAdoptConfig) { c.HostKeyPolicy = "strict" })
	_, err := h.svc.Preflight(context.Background(), h.preflightCmd())
	var required *HostKeyFingerprintRequiredError
	if !errors.As(err, &required) {
		t.Fatalf("err = %v, want HostKeyFingerprintRequiredError", err)
	}
	if len(h.ssh.Dials()) != 0 {
		t.Error("a dial happened despite the refusal")
	}
}

func TestStrictServerPreflightWithFingerprintIssuesATicketTheAdoptAccepts(t *testing.T) {
	h := newHarness(t, func(c *config.SSHAdoptConfig) { c.HostKeyPolicy = "strict" })
	pf := h.preflightCmd()
	pf.ExpectedHostKeyFingerprint = serverKey
	res, err := h.svc.Preflight(context.Background(), pf)
	if err != nil || !res.Reachable || res.HostKeyTicket == "" {
		t.Fatalf("Preflight = %+v, %v", res, err)
	}
	job, err := h.svc.StartAdoption(context.Background(), h.adoptWithTicket(res.HostKeyTicket))
	if err != nil {
		t.Fatalf("StartAdoption: %v", err)
	}
	if done := h.waitJob(t, job.ID); done.Status != node_adoption_job.StatusCompleted {
		t.Fatalf("job = %s (%s)", done.Status, done.Error)
	}
}

func TestMisspelledPolicyFailsClosed(t *testing.T) {
	h := newHarness(t, func(c *config.SSHAdoptConfig) { c.HostKeyPolicy = "lenient" })
	if _, err := h.svc.Preflight(context.Background(), h.preflightCmd()); err == nil {
		t.Fatal("Preflight ran under an unknown policy")
	}
	if _, err := h.svc.StartAdoption(context.Background(), h.adoptCmd()); err == nil {
		t.Fatal("StartAdoption ran under an unknown policy")
	}
	if len(h.ssh.Dials()) != 0 {
		t.Error("a dial happened under an unknown policy")
	}
}

// ---- host_key_changed and the explicit reset --------------------------------

func TestPreflightWithChangedHostKeyReportsBothAndRunsNothing(t *testing.T) {
	h := newHarness(t, nil)
	cmd := h.preflightCmd()
	cmd.ExpectedHostKeyFingerprint = otherKey

	res, err := h.svc.Preflight(context.Background(), cmd)
	if err != nil {
		t.Fatalf("Preflight: %v", err)
	}
	if !res.HostKeyChanged || res.Reachable || res.HostKeyTicket != "" || res.HostKeyReset {
		t.Fatalf("result = %+v, want host_key_changed with no ticket and no reset", res)
	}
	if res.HostKeyFingerprint != serverKey || res.ExpectedHostKeyFingerprint != otherKey {
		t.Errorf("fingerprints = observed %q expected %q", res.HostKeyFingerprint, res.ExpectedHostKeyFingerprint)
	}
	if res.Error == "" {
		t.Error("Error is empty; the operator needs to be told what to do")
	}
	if runs := h.ssh.Runs(); len(runs) != 0 {
		t.Errorf("commands ran after a host-key refusal: %v", runs)
	}
	row, ok := h.audit.Find(audit_event.ActionNodeSSHPreflight)
	if !ok {
		t.Fatal("no preflight audit row")
	}
	for _, want := range []string{"host_key_changed=true", "host_key=" + serverKey, "expected=" + otherKey} {
		if !strings.Contains(row.Detail, want) {
			t.Errorf("detail %q lacks %q", row.Detail, want)
		}
	}
	if strings.Contains(row.Detail, "host_key_reset") {
		t.Errorf("detail %q records a reset nobody asked for", row.Detail)
	}
}

func TestAcceptNewHostKeyReplacesTheKeyAndIsAuditedWithBoth(t *testing.T) {
	h := newHarness(t, nil)
	cmd := h.preflightCmd()
	cmd.ExpectedHostKeyFingerprint = otherKey
	cmd.AcceptNewHostKey = true

	res, err := h.svc.Preflight(context.Background(), cmd)
	if err != nil {
		t.Fatalf("Preflight: %v", err)
	}
	if !res.Reachable || res.HostKeyChanged || !res.HostKeyReset || res.FirstContact {
		t.Fatalf("result = %+v, want reachable, reset, not first contact", res)
	}
	if res.HostKeyFingerprint != serverKey || res.ExpectedHostKeyFingerprint != otherKey {
		t.Errorf("fingerprints = observed %q replaced %q", res.HostKeyFingerprint, res.ExpectedHostKeyFingerprint)
	}
	if res.HostKeyTicket == "" {
		t.Fatal("no ticket after an accepted reset")
	}
	row, ok := h.audit.Find(audit_event.ActionNodeSSHPreflight)
	if !ok {
		t.Fatal("no preflight audit row")
	}
	for _, want := range []string{"host_key_reset=true", "previous=" + otherKey, "host_key=" + serverKey} {
		if !strings.Contains(row.Detail, want) {
			t.Errorf("detail %q lacks %q", row.Detail, want)
		}
	}
	assertNoCredentialMaterial(t, h.audit.Events())

	// The ticket is for the NEW key: the adoption connects strictly against it.
	job, err := h.svc.StartAdoption(context.Background(), h.adoptWithTicket(res.HostKeyTicket))
	if err != nil {
		t.Fatalf("StartAdoption: %v", err)
	}
	if done := h.waitJob(t, job.ID); done.Status != node_adoption_job.StatusCompleted {
		t.Fatalf("job = %s (%s)", done.Status, done.Error)
	}
	d := h.ssh.Dials()
	if last := d[len(d)-1]; last.KnownHostKeyFingerprint != serverKey {
		t.Errorf("adopt pinned %q, want the accepted %q", last.KnownHostKeyFingerprint, serverKey)
	}
}

func TestAcceptNewHostKeyWhenTheKeyDidNotChangeIsNotAReset(t *testing.T) {
	h := newHarness(t, nil)
	cmd := h.preflightCmd()
	cmd.ExpectedHostKeyFingerprint = serverKey
	cmd.AcceptNewHostKey = true
	res, err := h.svc.Preflight(context.Background(), cmd)
	if err != nil || !res.Reachable {
		t.Fatalf("Preflight = %+v, %v", res, err)
	}
	if res.HostKeyReset {
		t.Error("HostKeyReset = true though the key matched")
	}
	row, _ := h.audit.Find(audit_event.ActionNodeSSHPreflight)
	if strings.Contains(row.Detail, "host_key_reset") {
		t.Errorf("detail %q records a reset that did not happen", row.Detail)
	}
	// The probe dialed TOFU to be able to accept a new key, but the key it
	// met was the expected one: nothing was taken on first use, and the trail
	// must say which key it matched rather than "first-use".
	if strings.Contains(row.Detail, "pinned=first-use") || !strings.Contains(row.Detail, "pinned="+serverKey) {
		t.Errorf("detail %q records first-use for a key that matched the expectation", row.Detail)
	}
}

func TestAcceptNewHostKeyWithoutAnExpectationIsAValidationError(t *testing.T) {
	h := newHarness(t, nil)
	cmd := h.preflightCmd()
	cmd.AcceptNewHostKey = true
	if err := cmd.Validate(); err == nil {
		t.Fatal("Validate accepted accept_new_host_key with nothing to replace")
	}
}

// ---- audit hygiene -----------------------------------------------------------

func TestPreflightIsAuditedWithTheObservedKeyAndNoCredentials(t *testing.T) {
	h := newHarness(t, nil)
	res := h.preflightOK(t)
	row, ok := h.audit.Find(audit_event.ActionNodeSSHPreflight)
	if !ok {
		t.Fatalf("no %s audit row; actions = %v", audit_event.ActionNodeSSHPreflight, h.audit.Actions())
	}
	for _, want := range []string{"host=10.0.0.3", "username=pi", "auth=password", "host_key=" + serverKey, "pinned=first-use"} {
		if !strings.Contains(row.Detail, want) {
			t.Errorf("preflight detail %q lacks %q", row.Detail, want)
		}
	}
	if row.TargetType != audit_event.TargetSSHHost || row.TargetID != "10.0.0.3:22" {
		t.Errorf("target = %s/%s, want ssh_host/10.0.0.3:22", row.TargetType, row.TargetID)
	}
	if strings.Contains(row.Detail, res.HostKeyTicket) {
		t.Error("the audit detail carries the ticket")
	}
	assertNoCredentialMaterial(t, h.audit.Events())
}

func TestPreflightAuthKindNamesTheKeyNotItsMaterial(t *testing.T) {
	h := newHarness(t, nil)
	cmd := h.preflightCmd()
	cmd.Password = ""
	keyID := uuid.New()
	cmd.SSHKeyID = &keyID
	if _, err := h.svc.Preflight(context.Background(), cmd); err != nil {
		t.Fatalf("Preflight: %v", err)
	}
	row, _ := h.audit.Find(audit_event.ActionNodeSSHPreflight)
	if !strings.Contains(row.Detail, "auth=key:"+keyID.String()) {
		t.Errorf("detail %q, want auth=key:<id>", row.Detail)
	}
	assertNoCredentialMaterial(t, h.audit.Events())
}

// ---- the typed refusals reach HTTP as themselves ----------------------------

func TestBindingRequiredAnswers400WithItsCodeThroughTheGenericMapper(t *testing.T) {
	e := echo.New()
	rec := httptest.NewRecorder()
	c := e.NewContext(httptest.NewRequest(http.MethodPost, "/", nil), rec)
	if err := results.SendInternalServerError(c, "failed to start ssh adoption", &HostKeyBindingRequiredError{}); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "host_key_binding_required") {
		t.Errorf("body = %s, want the error code", rec.Body.String())
	}
}
