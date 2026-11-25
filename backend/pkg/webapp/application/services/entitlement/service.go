// Package entitlement resolves which entitlement file is in force on this
// install and answers the device cap from it. It is the one reader of the
// stored files and of the file handed in at boot; the node service asks it
// through the narrow DeviceCapResolver it implements.
package entitlement

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/entitlement_apply"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/audit"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/audit_event"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/entitlement"
)

// Source says where the file in force came from.
type Source string

const (
	// SourceNone: no valid file anywhere; the free cap applies.
	SourceNone Source = "none"
	// SourceFile: the file handed in at boot (air-gap and bootstrap).
	SourceFile Source = "file"
	// SourceDatabase: a file applied through the API.
	SourceDatabase Source = "database"
)

// State is the card's state, one word.
type State string

const (
	// StateFree: no entitlement; the free cap applies.
	StateFree State = "free"
	// StateActive: a file is in force and before its expiry.
	StateActive State = "active"
	// StateGrace: expired, inside the signed grace; the cap still holds.
	StateGrace State = "grace"
	// StateLapsed: expired and past grace; new admissions fall to the free
	// cap, the fleet is untouched.
	StateLapsed State = "lapsed"
	// StateRejected: a file was offered (at boot or in the table) and none of
	// them verifies against this build's keys. The free cap applies and the
	// card says why.
	StateRejected State = "rejected"
)

// Snapshot is the resolved entitlement at an instant: what the card shows
// and what admission enforces, from one computation.
type Snapshot struct {
	State    State
	Source   Source
	Document *entitlement.Document
	// AppliedAt is when the file in force was pasted; zero for the boot file.
	AppliedAt time.Time
	// EffectiveCap is what admission enforces right now (0 = unlimited).
	EffectiveCap int
	// SignedCapBelowFloor: the file in force signs a positive cap below the
	// free cap. It is accepted and acts as the free cap (the floor), and the
	// card says so, so the signed number and the enforced one never disagree
	// silently.
	SignedCapBelowFloor bool
	// AdmittedCount is the install's admitted device count, or -1 when the
	// count could not be read — the card must not show 0 for "unknown".
	// Instance-wide: it reaches the operator's card only.
	AdmittedCount int64
	// AccountAdmittedCount is the CALLER's account's admitted device count
	// (CurrentFor), -1 when it could not be read or no account was asked
	// about. What a tenant is told about devices is its own fleet, never
	// the install's.
	AccountAdmittedCount int64
	// Rejections names every offered file that did not verify, by source.
	// Non-empty even when another file is in force, so a bad paste or a
	// rotated key is never silent.
	Rejections []Rejection
	FreeCap    int
	// GraceUntil is when the grace ends for the file in force; zero if none.
	GraceUntil      time.Time
	SubscriptionURL string
	// AccountLimit is set when a per-account device limit is in effect for
	// the CALLER's account (CurrentFor): the account's own limit, plan and
	// contact. The card then shows that and nothing of the install's
	// entitlement, which is the operator's licence, not the tenant's.
	AccountLimit *AccountLimitView
	// Applied describes the file a paste just stored; nil on every read
	// that is not the answer to a paste. A valid file is stored even when it
	// does not take effect — an older issue date than a file still active
	// or in grace, or a file already lapsed — so the answer must say which
	// file it was and whether it is the one in force.
	Applied *AppliedFile
}

// AccountLimitView is one account's per-account device limit as its card
// shows it. Known is false when the account's override could not be read;
// the limit is then not shown as a number.
type AccountLimitView struct {
	Limit entitlement.AccountLimit
	Known bool
}

// AppliedFile is the pasted file's own terms and standing at the paste.
type AppliedFile struct {
	Document *entitlement.Document
	// State is the pasted file's own phase: active, grace or lapsed.
	State State
	// InForce: the pasted file is the one the install now runs under.
	InForce bool
}

// Rejection is one offered file that did not verify.
type Rejection struct {
	Source Source
	Reason string
}

// counter is what the card needs from the fleet: the install's admitted
// count and one account's.
type counter interface {
	CountAdmittedNodes(ctx context.Context) (int64, error)
	CountAdmittedNodesForAccount(ctx context.Context, accountID uuid.UUID) (int64, error)
}

// InstanceOperator names the account that operates the install: the oldest
// account (created_at, then id). The entitlement is instance-wide, so on an
// install that hosts several accounts only that account's super_admin may
// apply a file or read its terms.
type InstanceOperator interface {
	InstanceOperatorAccountID(ctx context.Context) (uuid.UUID, error)
}

// Service resolves the entitlement in force. The resolution re-verifies every
// candidate against the embedded keys, so it is cached briefly: the master
// token path asks on every new device, and a batch of devices must not turn
// into a batch of table scans and signature checks.
type Service struct {
	repo            repository.InstanceEntitlementRepository
	nodes           counter
	operator        InstanceOperator
	bootDocument    string
	subscriptionURL string
	audit           *audit.Recorder
	logger          *slog.Logger
	now             func() time.Time
	// verify is entitlement.Verify (the embedded keys); tests substitute a
	// verifier over a key they hold.
	verify func(form string) (*entitlement.Document, error)
	// embedsDevelopmentKey is entitlement.EmbedsDevelopmentKey; tests
	// substitute both answers.
	embedsDevelopmentKey func() bool

	// The per-account device limit (SetAccountDeviceLimit). Off while
	// accountLimitDefault is 0 or no reader is wired.
	accountLimitDefault   int
	accountLimitContact   string
	accountLimitOverrides repository.AccountDeviceLimitReader

	mu       sync.Mutex
	cached   *resolved
	cachedAt time.Time
	// warnedBelowFloor holds the files already reported as clamped to the
	// free cap, so the WARN is said once per file rather than once per
	// cache refresh. Guarded by mu (resolveUncached runs under it); bounded
	// by the candidates an install ever holds.
	warnedBelowFloor map[string]struct{}
}

// resolved is the clock-independent part of a resolution: the file in force
// and the rejections. The phase and the cap are computed from it at each
// read, so a cache never freezes a lapse.
type resolved struct {
	doc *entitlement.Document
	// form is the paste form of doc, so a paste can tell whether it is the
	// file now in force.
	form       string
	source     Source
	appliedAt  time.Time
	rejections []Rejection
	// incomplete: the stored files could not be read, so this resolution
	// saw the boot file alone and may name the wrong file, or none. Never
	// cached; resolve falls back to the last complete resolution, and with
	// none admission is bounded by what it did see (EffectiveDeviceCap).
	incomplete bool
}

const (
	// cacheTTL bounds how stale the file-in-force may be between reads. A
	// paste invalidates it immediately; the TTL only covers a boot file edit
	// (which needs a restart anyway) and the table read itself.
	cacheTTL = 30 * time.Second
	// candidateLimit bounds the table read. One install pastes a handful of
	// files over its life; fifty is far beyond that and still one page.
	candidateLimit = 50
)

// NewService wires the resolver. bootDocument is the paste form handed in at
// boot (empty when none); subscriptionURL may be empty for the default.
func NewService(repo repository.InstanceEntitlementRepository, nodes counter, operator InstanceOperator, bootDocument, subscriptionURL string, auditRecorder *audit.Recorder, logger *slog.Logger) *Service {
	if repo == nil {
		panic("entitlement.Service requires a repository")
	}
	if nodes == nil {
		panic("entitlement.Service requires a node counter")
	}
	if operator == nil {
		panic("entitlement.Service requires the instance operator lookup")
	}
	if auditRecorder == nil {
		panic("entitlement.Service requires an audit recorder")
	}
	if subscriptionURL == "" {
		subscriptionURL = entitlement.DefaultSubscriptionURL
	}
	return &Service{
		repo:            repo,
		nodes:           nodes,
		operator:        operator,
		bootDocument:    bootDocument,
		subscriptionURL: subscriptionURL,
		audit:           auditRecorder,
		logger:          logger.With("service", "entitlement"),
		now:             time.Now,
		verify:          entitlement.Verify,

		embedsDevelopmentKey: entitlement.EmbedsDevelopmentKey,
	}
}

// EffectiveDeviceCap is the DeviceCapResolver the node service admits
// against. When the stored files cannot be read, the last complete
// resolution answers, so a transient failure never refuses an admission an
// applied file allows. With none to fall back on, a failed read never lifts
// the cap: the request is bounded by what can be established without the
// table — the boot file if it verifies, else the free cap — and the failure
// is not cached, so the next request reads the table again. Making the
// table unreadable must not be a way to admit past the cap.
func (s *Service) EffectiveDeviceCap(ctx context.Context) (int, string) {
	r := s.resolve(ctx)
	if r.incomplete {
		s.logger.ErrorContext(ctx, "the applied entitlement files could not be read and no earlier reading is known; "+
			"this request is bounded by the boot file, or the free cap without one", "source", r.source)
	}
	if r.doc == nil {
		return entitlement.FreeDeviceCap, s.subscriptionURL
	}
	return r.doc.EffectiveCap(s.now()), s.subscriptionURL
}

// SetAccountDeviceLimit turns the per-account device limit on: defaultLimit
// (ACCOUNT_DEVICE_LIMIT) for every account without an override in force,
// the override read through overrides, and contact as where to ask for
// more. defaultLimit 0 leaves it off, and then nothing is read: an install
// that does not use the limit behaves exactly as before it existed.
func (s *Service) SetAccountDeviceLimit(defaultLimit int, contact string, overrides repository.AccountDeviceLimitReader) {
	if defaultLimit < 0 {
		defaultLimit = 0
	}
	s.accountLimitDefault = defaultLimit
	s.accountLimitContact = contact
	s.accountLimitOverrides = overrides
}

func (s *Service) accountLimitOn() bool {
	return s.accountLimitDefault > 0 && s.accountLimitOverrides != nil
}

// accountLimitApplies reports whether accountID is bound by the per-account
// limit at all: the limit is on and the account does not operate the
// install. The operator check fails closed (a caller it cannot confirm is a
// tenant), so a failed lookup binds rather than exempts.
func (s *Service) accountLimitApplies(ctx context.Context, accountID uuid.UUID) bool {
	return s.accountLimitOn() && accountID != uuid.Nil && !s.IsInstanceOperator(ctx, accountID)
}

// AccountDeviceLimit is the AccountDeviceLimitResolver admission asks: the
// limit in force for accountID now (Limit 0 = none). The override's end is
// judged on this service's clock, the one the entitlement's expiry is
// judged on, at every call: nothing has to run when a subscription ends.
// An override that cannot be read is an error, and admission refuses as
// unverified — a failed read never lifts a limit.
func (s *Service) AccountDeviceLimit(ctx context.Context, accountID uuid.UUID) (entitlement.AccountLimit, error) {
	if !s.accountLimitApplies(ctx, accountID) {
		return entitlement.AccountLimit{}, nil
	}
	o, err := s.accountLimitOverrides.DeviceLimitOverride(ctx, accountID)
	if err != nil {
		return entitlement.AccountLimit{}, fmt.Errorf("reading the account's device-limit override: %w", err)
	}
	l := entitlement.ResolveAccountLimit(s.accountLimitDefault, o, s.now())
	l.Contact = s.accountLimitContact
	return l, nil
}

// Current is the card: the resolution plus the admitted count.
func (s *Service) Current(ctx context.Context) Snapshot {
	return s.snapshot(ctx, s.resolve(ctx))
}

// snapshot builds the card from one resolution.
func (s *Service) snapshot(ctx context.Context, r resolved) Snapshot {
	now := s.now()
	snap := Snapshot{
		Source:               r.source,
		Document:             r.doc,
		AppliedAt:            r.appliedAt,
		Rejections:           r.rejections,
		FreeCap:              entitlement.FreeDeviceCap,
		EffectiveCap:         entitlement.FreeDeviceCap,
		AdmittedCount:        -1,
		AccountAdmittedCount: -1,
		SubscriptionURL:      s.subscriptionURL,
	}
	switch {
	case r.doc != nil:
		snap.EffectiveCap = r.doc.EffectiveCap(now)
		snap.SignedCapBelowFloor = r.doc.CapBelowFreeFloor()
		snap.GraceUntil = r.doc.GraceUntil()
		snap.State = phaseState(r.doc.PhaseAt(now))
	case len(r.rejections) > 0:
		snap.State = StateRejected
	default:
		snap.State = StateFree
	}
	if count, err := s.nodes.CountAdmittedNodes(ctx); err != nil {
		s.logger.ErrorContext(ctx, "counting admitted nodes for the entitlement card failed", "error", err)
	} else {
		snap.AdmittedCount = count
	}
	return snap
}

// phaseState names a file's phase in the card's words.
func phaseState(p entitlement.Phase) State {
	switch p {
	case entitlement.PhaseActive:
		return StateActive
	case entitlement.PhaseGrace:
		return StateGrace
	default:
		return StateLapsed
	}
}

// CurrentFor is Current plus the admitted count of accountID, the caller's
// own account: the one device number a tenant's card may show.
//
// When a per-account limit binds the account, the snapshot also carries it
// (AccountLimit), and the card shows that instead of the install's terms.
func (s *Service) CurrentFor(ctx context.Context, accountID uuid.UUID) Snapshot {
	snap := s.withAccountCount(ctx, s.Current(ctx), accountID)
	if s.accountLimitApplies(ctx, accountID) {
		view := &AccountLimitView{Limit: entitlement.AccountLimit{Contact: s.accountLimitContact}}
		if l, err := s.AccountDeviceLimit(ctx, accountID); err != nil {
			s.logger.ErrorContext(ctx, "reading the account's device limit for the subscription card failed",
				"account_id", accountID, "error", err)
		} else {
			view.Limit, view.Known = l, true
		}
		snap.AccountLimit = view
	}
	return snap
}

func (s *Service) withAccountCount(ctx context.Context, snap Snapshot, accountID uuid.UUID) Snapshot {
	if count, err := s.nodes.CountAdmittedNodesForAccount(ctx, accountID); err != nil {
		s.logger.ErrorContext(ctx, "counting the account's admitted nodes for the entitlement card failed",
			"account_id", accountID, "error", err)
	} else {
		snap.AccountAdmittedCount = count
	}
	return snap
}

// IsHostedInstall reports whether the file in force is the hosted install's
// own (kind=hosted). It is the one fact the backend holds that tells the
// hosted product from a self-hosted install, and the SPA reads it to word
// what only the hosted product has, such as an operator who performs an
// account erasure. A lapsed hosted file still answers true: the kind says
// what the install is, the phase only what it may admit. No file, or a
// resolution that failed, answers false.
func (s *Service) IsHostedInstall(ctx context.Context) bool {
	r := s.resolve(ctx)
	return r.doc != nil && r.doc.Kind == entitlement.KindHosted
}

// IsInstanceOperator reports whether accountID operates this install. It
// fails CLOSED: an account the lookup cannot confirm is not the operator, so
// a database error costs the operator one retry, never a tenant the file.
func (s *Service) IsInstanceOperator(ctx context.Context, accountID uuid.UUID) bool {
	operatorID, err := s.operator.InstanceOperatorAccountID(ctx)
	if err != nil {
		s.logger.ErrorContext(ctx, "resolving the instance operator account failed; treating the caller as a tenant",
			"account_id", accountID, "error", err)
		return false
	}
	return operatorID == accountID
}

// Apply verifies a pasted file, stores it and makes it the candidate the
// next resolution sees. A file that does not verify is refused with the
// typed 400 and stores nothing; a paste from an account that does not
// operate the install is refused with the typed 403 before anything is read.
func (s *Service) Apply(ctx context.Context, cmd entitlement_apply.EntitlementApplyCommand) (Snapshot, error) {
	if err := cmd.Validate(); err != nil {
		return Snapshot{}, &entitlement.RejectedError{Reason: err}
	}
	if !s.IsInstanceOperator(ctx, cmd.AuthContext.AccountID) {
		s.logger.WarnContext(ctx, "entitlement paste refused: the caller's account does not operate this install",
			"account_id", cmd.AuthContext.AccountID, "user_id", cmd.AuthContext.UserID)
		return Snapshot{}, &entitlement.NotInstanceOperatorError{}
	}
	doc, err := s.accept(cmd.Document)
	if err != nil {
		s.logger.WarnContext(ctx, "entitlement file refused",
			"account_id", cmd.AuthContext.AccountID, "user_id", cmd.AuthContext.UserID, "reason", err)
		return Snapshot{}, &entitlement.RejectedError{Reason: err}
	}
	rec := &entitlement.Record{
		ID:                 uuid.New(),
		Document:           cmd.Document,
		CustomerID:         doc.CustomerID,
		Kind:               doc.Kind,
		DeviceCap:          doc.DeviceCap,
		IssuedAt:           doc.IssuedAt,
		ExpiresAt:          doc.ExpiresAt,
		AppliedAt:          s.now(),
		AppliedByUserID:    cmd.AuthContext.UserID,
		AppliedByAccountID: cmd.AuthContext.AccountID,
	}
	if err := s.repo.Create(ctx, rec); err != nil {
		return Snapshot{}, fmt.Errorf("storing the entitlement: %w", err)
	}
	s.invalidate()
	// The terms, never the file: the document is a bearer value for the cap.
	s.audit.RecordDetail(ctx, audit.UserActor(cmd.AuthContext.AccountID, cmd.AuthContext.UserID),
		audit_event.ActionEntitlementApply, audit_event.TargetEntitlement, doc.CustomerID,
		fmt.Sprintf("kind=%s device_cap=%d issued_at=%s expires_at=%s grace_days=%d",
			doc.Kind, doc.DeviceCap, doc.IssuedAt.Format(time.RFC3339), doc.ExpiresAt.Format(time.RFC3339), doc.GraceDays))
	s.logger.InfoContext(ctx, "entitlement file applied",
		"customer_id", doc.CustomerID, "kind", doc.Kind, "device_cap", doc.DeviceCap,
		"expires_at", doc.ExpiresAt, "account_id", cmd.AuthContext.AccountID, "user_id", cmd.AuthContext.UserID)
	// One resolution answers both "what is in force" and "is it this file",
	// so the two halves of the answer cannot disagree.
	r := s.resolve(ctx)
	snap := s.withAccountCount(ctx, s.snapshot(ctx, r), cmd.AuthContext.AccountID)
	snap.Applied = &AppliedFile{
		Document: doc,
		State:    phaseState(doc.PhaseAt(s.now())),
		InForce:  r.doc != nil && r.form == cmd.Document,
	}
	return snap, nil
}

// LogBootState says at boot, loudly, what the install runs under. A boot
// file that does not verify is an ERROR line and nothing more: the install
// starts on the free cap rather than refusing to boot, because a licensing
// mistake must never take a fleet's control plane down.
func (s *Service) LogBootState(ctx context.Context) {
	if err := entitlement.TrustedKeysError(); err != nil {
		s.logger.ErrorContext(ctx, "the embedded entitlement trust store did not load; every entitlement file will be rejected", "error", err)
	}
	if s.embedsDevelopmentKey() {
		s.logger.WarnContext(ctx, "this build trusts the DEVELOPMENT entitlement-signing key, not the production one: "+
			"it is a development build. A release embeds the production key in "+
			"backend/pkg/webapp/domain/entitlement/keys/ (docs/ENTITLEMENT.md)",
			"key_fingerprint", entitlement.DevelopmentKeyFingerprint)
	}
	if s.accountLimitOn() {
		s.logger.InfoContext(ctx, "per-account device limit",
			"default_account_device_limit", s.accountLimitDefault, "contact_configured", s.accountLimitContact != "")
	}
	snap := s.Current(ctx)
	for _, rej := range snap.Rejections {
		s.logger.ErrorContext(ctx, "an entitlement file was rejected", "source", rej.Source, "reason", rej.Reason)
	}
	attrs := []any{"state", snap.State, "source", snap.Source, "effective_device_cap", snap.EffectiveCap, "free_device_cap", snap.FreeCap}
	if snap.Document != nil {
		attrs = append(attrs, "customer_id", snap.Document.CustomerID, "kind", snap.Document.Kind,
			"expires_at", snap.Document.ExpiresAt, "grace_until", snap.GraceUntil)
		if snap.SignedCapBelowFloor {
			attrs = append(attrs, "signed_device_cap", snap.Document.DeviceCap)
		}
	}
	if snap.AdmittedCount >= 0 {
		attrs = append(attrs, "admitted_devices", snap.AdmittedCount)
	}
	// An install that already holds more devices than its cap keeps every one
	// of them, by design, and refuses every new admission — a misconfigured
	// hosted install (no kind=hosted file, or one that lapsed) looks exactly
	// like that. Say it once, with the numbers, the moment the backend starts.
	if !entitlement.Unlimited(snap.EffectiveCap) && snap.AdmittedCount > int64(snap.EffectiveCap) {
		s.logger.WarnContext(ctx, fmt.Sprintf("device entitlement: this install already admits %d devices and its effective cap is %d. "+
			"The devices keep working; every NEW admission is refused until devices are removed or an entitlement covering them is applied",
			snap.AdmittedCount, snap.EffectiveCap), attrs...)
		return
	}
	if snap.State == StateRejected || snap.State == StateLapsed {
		s.logger.WarnContext(ctx, "device entitlement: new admissions are bounded by the free cap", attrs...)
		return
	}
	s.logger.InfoContext(ctx, "device entitlement", attrs...)
}

// accept is what makes a file a candidate: it verifies against the trusted
// keys and is not issued more than MaxIssueSkew in the future. Applied on
// paste and on every resolution, so a far-future issue date never outranks
// the files issued after it.
func (s *Service) accept(form string) (*entitlement.Document, error) {
	doc, err := s.verify(form)
	if err != nil {
		return nil, err
	}
	if err := doc.CheckIssuedAt(s.now()); err != nil {
		return nil, err
	}
	return doc, nil
}

// invalidate makes the next read resolve again. The cached resolution is
// kept as the fallback for a read that fails, so it is marked stale rather
// than dropped.
func (s *Service) invalidate() {
	s.mu.Lock()
	s.cachedAt = time.Time{}
	s.mu.Unlock()
}

// resolve answers from the cache while it is fresh. A resolution whose table
// read failed is never cached: the last complete one answers instead (kept
// stale, so the next read tries the table again), and with none it is
// returned marked incomplete: the boot file alone, never cached.
func (s *Service) resolve(ctx context.Context) resolved {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cached != nil && !s.cachedAt.IsZero() && s.now().Sub(s.cachedAt) < cacheTTL {
		return *s.cached
	}
	r := s.resolveUncached(ctx)
	if r.incomplete {
		if s.cached != nil {
			return *s.cached
		}
		return r
	}
	s.cached = &r
	s.cachedAt = s.now()
	return r
}

type candidate struct {
	doc       *entitlement.Document
	form      string
	source    Source
	appliedAt time.Time
}

// resolveUncached picks the file in force: among the candidates that verify,
// the newest issued one that has not lapsed; if every one has lapsed, the
// newest issued of those, so the card can say which file ran out. "Newest
// issued" is the SIGNED issue date, never the paste time, so re-pasting an
// old file cannot demote a newer one.
func (s *Service) resolveUncached(ctx context.Context) resolved {
	var out resolved
	var candidates []candidate

	if s.bootDocument != "" {
		if doc, err := s.accept(s.bootDocument); err != nil {
			out.rejections = append(out.rejections, Rejection{Source: SourceFile, Reason: err.Error()})
		} else {
			candidates = append(candidates, candidate{doc: doc, form: s.bootDocument, source: SourceFile})
		}
	}

	records, err := s.repo.ListNewest(ctx, candidateLimit)
	if err != nil {
		// Logged and marked: what follows sees the boot file alone, which a
		// newer stored file may outrank, so this resolution is never cached
		// (see resolve and EffectiveDeviceCap).
		s.logger.ErrorContext(ctx, "reading applied entitlement files failed; resolving without them", "error", err)
		out.incomplete = true
	}
	for i := range records {
		rec := records[i]
		doc, err := s.accept(rec.Document)
		if err != nil {
			// No customer id: the record names who a file was issued to, and
			// a rejection is shown to the operator of an install whose files
			// may have been offered by anyone it hosts.
			out.rejections = append(out.rejections, Rejection{Source: SourceDatabase,
				Reason: fmt.Sprintf("file applied %s: %v", rec.AppliedAt.Format(time.RFC3339), err)})
			continue
		}
		candidates = append(candidates, candidate{doc: doc, form: rec.Document, source: SourceDatabase, appliedAt: rec.AppliedAt})
	}

	now := s.now()
	var best *candidate
	for i := range candidates {
		c := &candidates[i]
		if best == nil {
			best = c
			continue
		}
		bestLapsed := best.doc.PhaseAt(now) == entitlement.PhaseLapsed
		cLapsed := c.doc.PhaseAt(now) == entitlement.PhaseLapsed
		switch {
		case bestLapsed && !cLapsed:
			best = c
		case !bestLapsed && cLapsed:
			// keep best
		case c.doc.IssuedAt.After(best.doc.IssuedAt):
			best = c
		}
	}
	if best != nil {
		out.doc = best.doc
		out.form = best.form
		out.source = best.source
		out.appliedAt = best.appliedAt
		s.warnBelowFloorOnce(ctx, best)
	} else {
		out.source = SourceNone
	}
	return out
}

// warnBelowFloorOnce says, once per file, that the file in force signs a
// positive cap below the free cap and is therefore enforced as the free cap.
// The file is genuine and stays accepted (a refusal would be loud for a
// document that grants nothing worse than the free tier); the WARN is what
// tells its issuer the signed number is not the one in force. Caller holds mu.
func (s *Service) warnBelowFloorOnce(ctx context.Context, c *candidate) {
	if !c.doc.CapBelowFreeFloor() {
		return
	}
	if _, done := s.warnedBelowFloor[c.form]; done {
		return
	}
	if s.warnedBelowFloor == nil {
		s.warnedBelowFloor = map[string]struct{}{}
	}
	s.warnedBelowFloor[c.form] = struct{}{}
	s.logger.WarnContext(ctx, fmt.Sprintf("the entitlement file in force signs a device cap of %d, below the free cap of %d; "+
		"the free cap is a floor, so %d applies", c.doc.DeviceCap, entitlement.FreeDeviceCap, entitlement.FreeDeviceCap),
		"source", c.source, "signed_device_cap", c.doc.DeviceCap, "effective_device_cap_floor", entitlement.FreeDeviceCap)
}

// IsRejected reports whether err is the typed paste refusal.
func IsRejected(err error) bool {
	var rejected *entitlement.RejectedError
	return errors.As(err, &rejected)
}

// IsRefusal reports whether err is one of Apply's typed refusals — a file
// that does not verify, or a caller that does not operate the install —
// rather than a failure of the platform.
func IsRefusal(err error) bool {
	var notOperator *entitlement.NotInstanceOperatorError
	return IsRejected(err) || errors.As(err, &notOperator)
}
