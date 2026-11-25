package node

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/audit"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/audit_event"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/entitlement"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
)

// DeviceCapResolver answers how many devices this install may admit right
// now — the device entitlement's effective cap (0 = unlimited) — and where a
// refusal should send the operator. The entitlement service implements it;
// it is named here, narrowly, so this service never imports that one.
type DeviceCapResolver interface {
	EffectiveDeviceCap(ctx context.Context) (cap int, subscriptionURL string)
}

// InstanceOperatorResolver answers whether an account operates this install.
// The entitlement service implements it next to DeviceCapResolver; a resolver
// that does not is treated as "not the operator" (the refusal then shows the
// tenant view), which is the side that discloses nothing.
type InstanceOperatorResolver interface {
	IsInstanceOperator(ctx context.Context, accountID uuid.UUID) bool
}

// SetDeviceCapResolver wires the device entitlement into admission.
func (s *Service) SetDeviceCapResolver(r DeviceCapResolver) { s.deviceCapResolver = r }

// AccountDeviceLimitResolver answers the per-account device limit in force
// for one account (Limit 0 = none). An error means the limit could not be
// established, and the admission is refused as unverified — a read that
// fails never lifts a limit. The entitlement service implements it.
type AccountDeviceLimitResolver interface {
	AccountDeviceLimit(ctx context.Context, accountID uuid.UUID) (entitlement.AccountLimit, error)
}

// SetAccountDeviceLimitResolver wires the per-account device limit into
// admission. Unwired, no account limit applies.
func (s *Service) SetAccountDeviceLimitResolver(r AccountDeviceLimitResolver) {
	s.accountLimitResolver = r
}

// accountLimit is the per-account limit admission checks for accountID.
func (s *Service) accountLimit(ctx context.Context, accountID uuid.UUID) (entitlement.AccountLimit, error) {
	if s.accountLimitResolver == nil || accountID == uuid.Nil {
		return entitlement.AccountLimit{}, nil
	}
	return s.accountLimitResolver.AccountDeviceLimit(ctx, accountID)
}

// deviceCapPolicy is the cap every admission path checks against. With no
// resolver wired the FREE cap applies — never "unlimited": a service built
// without the entitlement is a service that forgot it, and the public promise
// is a bound, not an absence of one.
func (s *Service) deviceCapPolicy(ctx context.Context) (int, string) {
	if s.deviceCapResolver == nil {
		return entitlement.FreeDeviceCap, entitlement.DefaultSubscriptionURL
	}
	cap, url := s.deviceCapResolver.EffectiveDeviceCap(ctx)
	if url == "" {
		url = entitlement.DefaultSubscriptionURL
	}
	return cap, url
}

// admit inserts a new node under the device cap: the COURTESY check on the
// row-creating paths (the operator's create, the browser flash, the SSH
// adoption and the master-token handshake). None of them creates an
// admitted row, so this refuses early what admitTransition would refuse
// later; the bound itself is admitTransition. A tombstone restore of a node
// that was admitted when deleted comes through here too, and for it this is
// the bound: the row it inserts is admitted, so the deletion's freed slot
// must still be free. A restore of a node that was not admitted puts back a
// non-admitted row whose own first connect or approval is the admission, and
// a hardware replace creates no row; neither comes here.
//
// refusalKey is empty on a path an operator drives, and names the device on
// a path a device drives (see underDeviceCap).
func (s *Service) admit(ctx context.Context, n *node.Node, actor audit.Actor, refusalKey string) error {
	return s.underDeviceCap(ctx, n.ID, n.AccountID, actor, refusalKey, func(limits repository.AdmissionLimits) (repository.DeviceCapAdmission, error) {
		return s.nodeRepo.CreateUnderDeviceCap(ctx, n, limits)
	})
}

// admitTransition writes n, whose stored state is not admitted and whose new
// state is, under the device cap — the ADMISSION the cap bounds: the first
// connect out of a pre-connect status and the placeholder reconcile. A
// refusal writes nothing, so the row stays un-admitted and can be admitted
// once a slot is free. Both are device-driven, so the refusal record is
// throttled per node.
func (s *Service) admitTransition(ctx context.Context, n *node.Node, actor audit.Actor) error {
	return s.underDeviceCap(ctx, n.ID, n.AccountID, actor, "node:"+n.ID.String(), func(limits repository.AdmissionLimits) (repository.DeviceCapAdmission, error) {
		return s.nodeRepo.UpdateUnderDeviceCap(ctx, n, limits)
	})
}

const (
	// deviceCapRefusalRecordInterval is how often one device's refusal is
	// logged and audited. A refused device asks again on its own — every
	// ~15 minutes on a current agent, about once a minute on an older one —
	// so an unthrottled record would grow by tens of thousands of identical
	// rows a day, which would otherwise fill the audit trail within any
	// retention window.
	deviceCapRefusalRecordInterval = time.Hour
	// deviceCapRefusalRecordCapacity bounds the throttle's memory: the key
	// is a node id (a first connect) or a master-token id (a new master-token
	// device) — never anything the device can mint, so the map grows with
	// the install, not with what a caller invents. Past it the map is
	// cleared, which only lets one more record through per key.
	deviceCapRefusalRecordCapacity = 10_000
)

// refusalThrottle answers "may this key's refusal be recorded again now" at
// most once per interval. The zero value is ready to use.
type refusalThrottle struct {
	mu   sync.Mutex
	last map[string]time.Time
}

func (t *refusalThrottle) allow(key string, now time.Time) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if last, ok := t.last[key]; ok && now.Sub(last) < deviceCapRefusalRecordInterval {
		return false
	}
	if t.last == nil || len(t.last) >= deviceCapRefusalRecordCapacity {
		t.last = map[string]time.Time{}
	}
	t.last[key] = now
	return true
}

// underDeviceCap runs one admission write under the effective cap and the
// account's own limit, so the refusal's log and audit row live here once for
// every path. A refusal on a device-driven path (refusalKey non-empty) is
// logged and audited once per device per deviceCapRefusalRecordInterval; an
// operator's refusal is recorded every time.
//
// A cap that could not be checked (*entitlement.DeviceCapUnverifiedError:
// the lock or the count failed, or the account's limit could not be read;
// nothing was written) is refused too, never admitted unchecked. It is an
// ERROR line — an install that cannot count its own fleet is a fault —
// throttled like the refusal on a device-driven path, and no audit row: no
// limit was reached, and the trail would only record the database being
// unreachable, usually by failing to write.
func (s *Service) underDeviceCap(ctx context.Context, nodeID, accountID uuid.UUID, actor audit.Actor, refusalKey string,
	write func(limits repository.AdmissionLimits) (repository.DeviceCapAdmission, error)) error {
	cap, url := s.deviceCapPolicy(ctx)
	var err error
	accountLimit, lerr := s.accountLimit(ctx, accountID)
	if lerr != nil {
		err = &entitlement.DeviceCapUnverifiedError{Cap: cap, Cause: fmt.Errorf("reading the account's device limit: %w", lerr)}
	} else {
		_, err = write(repository.AdmissionLimits{InstanceCap: cap, AccountID: accountID, Account: accountLimit})
	}
	var unverified *entitlement.DeviceCapUnverifiedError
	if errors.As(err, &unverified) {
		if refusalKey == "" || s.capUnverifiedRecords.allow(refusalKey, time.Now()) {
			s.logger.ErrorContext(ctx, "refusing node admission: the device cap could not be checked",
				"node_id", nodeID, "account_id", accountID, "cap", cap, "error", unverified.Cause)
		}
		return unverified
	}
	var capErr *entitlement.DeviceCapError
	if errors.As(err, &capErr) {
		capErr.SubscriptionURL = url
		s.scopeDeviceCapError(ctx, capErr, accountID)
		if refusalKey == "" || s.capRefusalRecords.allow(refusalKey, time.Now()) {
			s.logger.WarnContext(ctx, "refusing node admission: the install is at its device cap",
				"node_id", nodeID, "account_id", accountID, "cap", capErr.Cap, "count", capErr.Count)
			s.recordDeviceCapReached(ctx, actor, capErr)
		}
		return capErr
	}
	var limitErr *entitlement.AccountDeviceLimitError
	if errors.As(err, &limitErr) {
		if refusalKey == "" || s.accountLimitRefusalRecords.allow(refusalKey, time.Now()) {
			s.logger.WarnContext(ctx, "refusing node admission: the account is at its device limit",
				"node_id", nodeID, "account_id", accountID, "limit", limitErr.Limit,
				"account_devices", limitErr.Count, "plan", limitErr.Plan)
			s.recordAccountDeviceLimitReached(ctx, actor, limitErr)
		}
		return limitErr
	}
	return err
}

// isDeviceCapRefusal reports whether err is an admission refused by the
// device cap or the account's limit: reached, or not checkable.
// underDeviceCap has already logged it (throttled on a device-driven path),
// so a caller does not log it again.
func isDeviceCapRefusal(err error) bool {
	return errors.As(err, new(*entitlement.DeviceCapError)) ||
		errors.As(err, new(*entitlement.AccountDeviceLimitError)) ||
		errors.As(err, new(*entitlement.DeviceCapUnverifiedError))
}

// CheckDeviceCap answers whether an admission would be refused right now,
// without admitting anything: *entitlement.DeviceCapError at the cap,
// *entitlement.AccountDeviceLimitError at the account's limit,
// *entitlement.DeviceCapUnverifiedError when a count or the account's limit
// cannot be read while a limit applies. The SSH adoption asks it BEFORE
// starting its job: the job dials the device, installs the agent and writes
// a token, and finding out at the end that the row cannot be created would
// leave a half-adopted machine. The job's own create still runs under the
// lock, so this check is advice, not the guard.
func (s *Service) CheckDeviceCap(ctx context.Context, actor audit.Actor) error {
	cap, url := s.deviceCapPolicy(ctx)
	if !entitlement.Unlimited(cap) {
		count, err := s.nodeRepo.CountAdmittedNodes(ctx)
		if err != nil {
			// Refused, as the admission itself refuses: an adoption started now
			// would end in a create that cannot be checked either.
			s.logger.ErrorContext(ctx, "refusing adoption: the device cap could not be checked",
				"account_id", actor.AccountID, "cap", cap, "error", err)
			return &entitlement.DeviceCapUnverifiedError{Cap: cap, Cause: err}
		}
		if count >= int64(cap) {
			capErr := &entitlement.DeviceCapError{Cap: cap, Count: count, SubscriptionURL: url}
			s.scopeDeviceCapError(ctx, capErr, actor.AccountID)
			s.logger.WarnContext(ctx, "refusing adoption: the install is at its device cap",
				"account_id", actor.AccountID, "cap", cap, "count", count)
			s.recordDeviceCapReached(ctx, actor, capErr)
			return capErr
		}
	}
	limit, err := s.accountLimit(ctx, actor.AccountID)
	if err != nil {
		s.logger.ErrorContext(ctx, "refusing adoption: the account's device limit could not be read",
			"account_id", actor.AccountID, "error", err)
		return &entitlement.DeviceCapUnverifiedError{Cap: cap, Cause: err}
	}
	if !limit.Applies() {
		return nil
	}
	own, err := s.nodeRepo.CountAdmittedNodesForAccount(ctx, actor.AccountID)
	if err != nil {
		s.logger.ErrorContext(ctx, "refusing adoption: the account's admitted devices could not be counted",
			"account_id", actor.AccountID, "error", err)
		return &entitlement.DeviceCapUnverifiedError{Cap: cap, Cause: err}
	}
	if own >= int64(limit.Limit) {
		limitErr := &entitlement.AccountDeviceLimitError{AccountID: actor.AccountID, Limit: limit.Limit,
			Count: own, Plan: limit.Plan, Until: limit.Until, Contact: limit.Contact}
		s.logger.WarnContext(ctx, "refusing adoption: the account is at its device limit",
			"account_id", actor.AccountID, "limit", limit.Limit, "account_devices", own)
		s.recordAccountDeviceLimitReached(ctx, actor, limitErr)
		return limitErr
	}
	return nil
}

// scopeDeviceCapError says whose refusal this is, so the HTTP answer can show
// a tenant its OWN admitted count instead of the install's. Both reads fail
// toward the tenant view: an operator check
// that cannot answer is "not the operator", and a count that cannot be read
// is left out rather than shown as zero.
func (s *Service) scopeDeviceCapError(ctx context.Context, capErr *entitlement.DeviceCapError, accountID uuid.UUID) {
	capErr.AccountID = accountID
	if op, ok := s.deviceCapResolver.(InstanceOperatorResolver); ok && accountID != uuid.Nil {
		capErr.InstanceOperator = op.IsInstanceOperator(ctx, accountID)
	}
	if accountID == uuid.Nil {
		return
	}
	count, err := s.nodeRepo.CountAdmittedNodesForAccount(ctx, accountID)
	if err != nil {
		s.logger.ErrorContext(ctx, "counting the account's admitted nodes for a device-cap refusal failed",
			"account_id", accountID, "error", err)
		return
	}
	capErr.AccountDevices = count
	capErr.AccountDevicesKnown = true
}

// recordDeviceCapReached writes the refusal to the trail of the refused
// account, so the detail is scoped exactly as the HTTP answer is (call it
// after scopeDeviceCapError): the instance operator's row carries the
// install-wide count, any other account's row carries the cap and its own
// admitted count and never the install's.
func (s *Service) recordDeviceCapReached(ctx context.Context, actor audit.Actor, capErr *entitlement.DeviceCapError) {
	if s.audit == nil {
		return
	}
	detail := fmt.Sprintf("cap=%d", capErr.Cap)
	if capErr.InstanceOperator {
		detail += fmt.Sprintf(" count=%d", capErr.Count)
	}
	if capErr.AccountDevicesKnown {
		detail += fmt.Sprintf(" account_devices=%d", capErr.AccountDevices)
	}
	s.audit.RecordDetail(ctx, actor, audit_event.ActionEntitlementDeviceCapReached,
		audit_event.TargetEntitlement, "", detail)
}

// recordAccountDeviceLimitReached writes a refusal at the account's own
// limit to that account's trail. Every number in it is the account's own.
func (s *Service) recordAccountDeviceLimitReached(ctx context.Context, actor audit.Actor, e *entitlement.AccountDeviceLimitError) {
	if s.audit == nil {
		return
	}
	detail := fmt.Sprintf("limit=%d account_devices=%d plan=%s", e.Limit, e.Count, e.Plan)
	if e.Until != nil {
		detail += " until=" + e.Until.UTC().Format(time.RFC3339)
	}
	s.audit.RecordDetail(ctx, actor, audit_event.ActionEntitlementAccountDeviceLimitReached,
		audit_event.TargetEntitlement, "", detail)
}
