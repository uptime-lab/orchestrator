package entitlement

import (
	"bytes"
	"context"
	"crypto"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/in/commands/entitlement_apply"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services/audit/audittest"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/audit_event"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/entitlement"
)

type memRepo struct {
	repository.InstanceEntitlementRepository
	rows    []entitlement.Record
	listErr error
}

func (r *memRepo) Create(_ context.Context, rec *entitlement.Record) error {
	r.rows = append(r.rows, *rec)
	return nil
}

func (r *memRepo) ListNewest(_ context.Context, limit int) ([]entitlement.Record, error) {
	if r.listErr != nil {
		return nil, r.listErr
	}
	out := append([]entitlement.Record(nil), r.rows...)
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

type fixedCount struct {
	n   int64
	err error
}

func (c fixedCount) CountAdmittedNodes(context.Context) (int64, error) { return c.n, c.err }

// CountAdmittedNodesForAccount answers a tenth of the install, so a test
// can tell the two counts apart.
func (c fixedCount) CountAdmittedNodesForAccount(context.Context, uuid.UUID) (int64, error) {
	return c.n / 10, c.err
}

// fixedOperator names the instance operator account, or fails the lookup.
type fixedOperator struct {
	id  uuid.UUID
	err error
}

func (o *fixedOperator) InstanceOperatorAccountID(context.Context) (uuid.UUID, error) {
	return o.id, o.err
}

type harness struct {
	svc      *Service
	repo     *memRepo
	audit    *audittest.Sink
	pub      ed25519.PublicKey
	priv     ed25519.PrivateKey
	clock    time.Time
	operator *fixedOperator
}

func newHarness(t *testing.T, bootDocument string) *harness {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	repo := &memRepo{}
	recorder, sink := audittest.New()
	operator := &fixedOperator{id: uuid.New()}
	svc := NewService(repo, fixedCount{n: 100}, operator, bootDocument, "", recorder, slog.New(slog.NewTextHandler(io.Discard, nil)))
	h := &harness{svc: svc, repo: repo, audit: sink, pub: pub, priv: priv,
		clock: time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC), operator: operator}
	svc.now = func() time.Time { return h.clock }
	svc.verify = func(form string) (*entitlement.Document, error) {
		return entitlement.VerifyWith(form, []crypto.PublicKey{pub})
	}
	return h
}

func (h *harness) sign(t *testing.T, mutate func(*entitlement.Document)) string {
	t.Helper()
	doc := entitlement.Document{
		Version:    entitlement.Version,
		CustomerID: "customer-1",
		DeviceCap:  100,
		IssuedAt:   time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		ExpiresAt:  time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC),
		Kind:       entitlement.KindEvaluation,
		GraceDays:  30,
	}
	if mutate != nil {
		mutate(&doc)
	}
	form, err := entitlement.Sign(doc, h.priv)
	if err != nil {
		t.Fatal(err)
	}
	return form
}

// apply pastes as the instance operator.
func (h *harness) apply(t *testing.T, form string) (Snapshot, error) {
	t.Helper()
	return h.applyAs(t, form, h.operator.id)
}

func (h *harness) applyAs(t *testing.T, form string, accountID uuid.UUID) (Snapshot, error) {
	t.Helper()
	return h.svc.Apply(context.Background(), entitlement_apply.EntitlementApplyCommand{
		Document:    form,
		AuthContext: commands.NewAuthContext(accountID, uuid.New()),
	})
}

// On an install hosting several accounts, each has its own super_admin; a
// file pasted by any of them would change what every account may admit.
// Only the operating account may apply one — refused before the file is
// read, nothing stored, nothing in force changed.
func TestApplyFromAnotherAccountIsRefused(t *testing.T) {
	h := newHarness(t, "")
	form := h.sign(t, func(d *entitlement.Document) {
		d.ExpiresAt = h.clock.AddDate(1, 0, 0)
	})

	_, err := h.applyAs(t, form, uuid.New())
	var notOperator *entitlement.NotInstanceOperatorError
	if !errors.As(err, &notOperator) {
		t.Fatalf("a tenant's paste: err = %v, want *entitlement.NotInstanceOperatorError", err)
	}
	if notOperator.HTTPStatus() != 403 || notOperator.ErrorCode() != entitlement.NotInstanceOperatorErrorCode {
		t.Errorf("refusal answers %d %q", notOperator.HTTPStatus(), notOperator.ErrorCode())
	}
	if len(h.repo.rows) != 0 {
		t.Errorf("a refused paste stored %d row(s)", len(h.repo.rows))
	}
	if _, ok := h.audit.Find(audit_event.ActionEntitlementApply); ok {
		t.Error("a refused paste wrote an apply audit row")
	}

	// And it fails CLOSED: a lookup that errors is not the operator.
	h.operator.err = errors.New("database is unhappy")
	if _, err := h.apply(t, form); !errors.As(err, &notOperator) {
		t.Errorf("with the operator lookup failing: err = %v, want the refusal", err)
	}
	h.operator.err = nil
	if _, err := h.apply(t, form); err != nil {
		t.Fatalf("the operator's own paste: %v", err)
	}
}

// A hosted install started without its file (or on a lapsed one) holds more
// devices than the cap now in force: the fleet keeps running, every new
// admission is refused. The boot log says so once, with both numbers.
func TestBootWarnsWhenTheFleetExceedsTheCap(t *testing.T) {
	h := newHarness(t, "") // 100 admitted, no file: the free cap
	var buf bytes.Buffer
	h.svc.logger = slog.New(slog.NewTextHandler(&buf, nil))
	h.svc.LogBootState(context.Background())
	want := fmt.Sprintf("already admits 100 devices and its effective cap is %d", entitlement.FreeDeviceCap)
	if n := strings.Count(buf.String(), want); n != 1 {
		t.Errorf("%d boot lines say %q, want exactly 1:\n%s", n, want, buf.String())
	}
	if !strings.Contains(buf.String(), "level=WARN") {
		t.Errorf("the over-cap line is not a WARN:\n%s", buf.String())
	}

	// Under an unlimited hosted file the same fleet is no finding.
	form := h.sign(t, func(d *entitlement.Document) {
		d.DeviceCap = 0
		d.Kind = entitlement.KindHosted
		d.ExpiresAt = h.clock.AddDate(1, 0, 0)
	})
	if _, err := h.apply(t, form); err != nil {
		t.Fatal(err)
	}
	buf.Reset()
	h.svc.LogBootState(context.Background())
	if strings.Contains(buf.String(), "already admits") {
		t.Errorf("an unlimited entitlement still warned:\n%s", buf.String())
	}
}

// A build that still trusts the development signing key says so at boot,
// once, with the fingerprint; a build with the production key says nothing.
func TestBootWarnsWhenTheBuildTrustsTheDevelopmentKey(t *testing.T) {
	for _, dev := range []bool{true, false} {
		h := newHarness(t, "")
		var buf bytes.Buffer
		h.svc.logger = slog.New(slog.NewTextHandler(&buf, nil))
		h.svc.embedsDevelopmentKey = func() bool { return dev }
		h.svc.LogBootState(context.Background())
		n := strings.Count(buf.String(), "DEVELOPMENT entitlement-signing key")
		switch {
		case dev && (n != 1 || !strings.Contains(buf.String(), entitlement.DevelopmentKeyFingerprint)):
			t.Errorf("development key: %d warning(s), want one naming the fingerprint:\n%s", n, buf.String())
		case !dev && n != 0:
			t.Errorf("production key: %d development-key warning(s):\n%s", n, buf.String())
		}
	}
}

// The newest valid file wins by its signed issue date, so a file dated far
// ahead (a signing slip) would outrank every later one until it lapsed. A
// file issued more than a day in the future is refused on paste and not a
// candidate on resolution; a day ahead is an honest clock difference.
func TestAFileIssuedInTheFutureIsRefused(t *testing.T) {
	h := newHarness(t, "")
	future := h.sign(t, func(d *entitlement.Document) {
		d.IssuedAt = h.clock.Add(48 * time.Hour)
		d.ExpiresAt = h.clock.AddDate(1, 0, 0)
	})
	_, err := h.apply(t, future)
	if !errors.Is(err, entitlement.ErrIssuedInFuture) || !IsRejected(err) {
		t.Fatalf("paste of a file issued two days ahead: err = %v, want the typed rejection wrapping ErrIssuedInFuture", err)
	}
	if len(h.repo.rows) != 0 {
		t.Error("the refused file was stored")
	}

	// Stored anyway (an older build accepted it): not a candidate.
	h.repo.rows = append(h.repo.rows, entitlement.Record{ID: uuid.New(), Document: future, AppliedAt: h.clock})
	snap := h.svc.Current(context.Background())
	if snap.Document != nil || len(snap.Rejections) != 1 {
		t.Errorf("a stored future-dated file resolved as %+v", snap)
	}

	h2 := newHarness(t, "")
	nearly := h2.sign(t, func(d *entitlement.Document) {
		d.IssuedAt = h2.clock.Add(23 * time.Hour)
		d.ExpiresAt = h2.clock.AddDate(1, 0, 0)
	})
	if _, err := h2.apply(t, nearly); err != nil {
		t.Errorf("a file 23 hours ahead was refused: %v", err)
	}
}

// A rejection is shown to the operator, about files any account may have
// offered: it names the source and the reason, never a customer id.
func TestRejectionsCarryNoCustomerID(t *testing.T) {
	h := newHarness(t, "")
	h.repo.rows = append(h.repo.rows, entitlement.Record{ID: uuid.New(), Document: "entl1.bm90.c2ln",
		CustomerID: "someone-elses-customer", AppliedAt: h.clock})
	snap := h.svc.Current(context.Background())
	if len(snap.Rejections) != 1 {
		t.Fatalf("rejections = %+v, want the one bad row", snap.Rejections)
	}
	if strings.Contains(snap.Rejections[0].Reason, "someone-elses-customer") {
		t.Errorf("rejection reason %q carries a customer id", snap.Rejections[0].Reason)
	}
}

func TestNoFileIsTheFreeCap(t *testing.T) {
	h := newHarness(t, "")
	snap := h.svc.Current(context.Background())
	if snap.State != StateFree || snap.EffectiveCap != entitlement.FreeDeviceCap || snap.Source != SourceNone {
		t.Errorf("snapshot = %+v", snap)
	}
	if snap.AdmittedCount != 100 {
		t.Errorf("admitted = %d, want the counter's 100", snap.AdmittedCount)
	}
	cap, url := h.svc.EffectiveDeviceCap(context.Background())
	if cap != entitlement.FreeDeviceCap || url != entitlement.DefaultSubscriptionURL {
		t.Errorf("cap %d url %q", cap, url)
	}
}

// CurrentFor adds the caller's own count beside the install's; Current
// alone asks about no account and says so with -1.
func TestCurrentForCarriesTheAccountCount(t *testing.T) {
	h := newHarness(t, "")
	if snap := h.svc.Current(context.Background()); snap.AccountAdmittedCount != -1 {
		t.Errorf("Current: account count = %d, want -1", snap.AccountAdmittedCount)
	}
	snap := h.svc.CurrentFor(context.Background(), uuid.New())
	if snap.AccountAdmittedCount != 10 || snap.AdmittedCount != 100 {
		t.Errorf("CurrentFor: account %d install %d, want 10 and 100", snap.AccountAdmittedCount, snap.AdmittedCount)
	}
}

// A trial that ran out: the state says so, the file stays on the card, and
// the only thing that changes is the cap the NEXT admission meets. Nothing
// here withdraws a device, a command or anything else — the banner is the
// whole consequence.
func TestTrialExpiryBannerOnly(t *testing.T) {
	h := newHarness(t, "")
	if _, err := h.apply(t, h.sign(t, nil)); err != nil { // expires 2026-04-01, 30 days grace
		t.Fatal(err)
	}

	h.clock = time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC)
	if snap := h.svc.Current(context.Background()); snap.State != StateActive || snap.EffectiveCap != 100 {
		t.Errorf("before expiry: %s cap %d", snap.State, snap.EffectiveCap)
	}

	h.svc.invalidate()
	h.clock = time.Date(2026, 4, 15, 0, 0, 0, 0, time.UTC)
	if snap := h.svc.Current(context.Background()); snap.State != StateGrace || snap.EffectiveCap != 100 {
		t.Errorf("in grace: %s cap %d", snap.State, snap.EffectiveCap)
	}

	h.svc.invalidate()
	h.clock = time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	snap := h.svc.Current(context.Background())
	if snap.State != StateLapsed {
		t.Errorf("after grace: state %s, want lapsed", snap.State)
	}
	if snap.EffectiveCap != entitlement.FreeDeviceCap {
		t.Errorf("after grace: cap %d, want the free cap", snap.EffectiveCap)
	}
	if snap.Document == nil || snap.Document.CustomerID != "customer-1" {
		t.Error("the lapsed file left the card; it must stay so the operator sees what ran out")
	}
	if snap.AdmittedCount != 100 {
		t.Errorf("the fleet count changed on lapse: %d", snap.AdmittedCount)
	}
	if cap, _ := h.svc.EffectiveDeviceCap(context.Background()); cap != entitlement.FreeDeviceCap {
		t.Errorf("admission cap after lapse = %d", cap)
	}
}

// The cache must never freeze a lapse: the file in force is cached, the
// phase is computed at every read.
func TestCachedResolutionStillLapsesOnTheClock(t *testing.T) {
	h := newHarness(t, "")
	if _, err := h.apply(t, h.sign(t, nil)); err != nil {
		t.Fatal(err)
	}
	h.svc.invalidate()
	h.clock = time.Date(2026, 4, 30, 23, 59, 50, 0, time.UTC) // ten seconds before the grace ends
	if cap, _ := h.svc.EffectiveDeviceCap(context.Background()); cap != 100 {
		t.Fatalf("grace cap = %d", cap)
	}
	// Twenty seconds later: past the grace, inside the cache TTL. The table
	// must not be read again, and the cap must still fall.
	h.repo.listErr = errors.New("must not be read again within the TTL")
	h.clock = h.clock.Add(20 * time.Second)
	if cap, _ := h.svc.EffectiveDeviceCap(context.Background()); cap != entitlement.FreeDeviceCap {
		t.Errorf("cap inside the cache window after the grace ended = %d, want the free cap", cap)
	}
}

// Newest issued wins among the files that have not lapsed; a lapsed file
// never beats a live one however recent, and re-pasting an old file cannot
// demote a newer one because the order is the SIGNED issue date.
func TestNewestValidFileWins(t *testing.T) {
	h := newHarness(t, "")
	h.clock = time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	subscription := h.sign(t, func(d *entitlement.Document) {
		d.CustomerID = "subscription"
		d.Kind = entitlement.KindSubscription
		d.DeviceCap = 500
		d.IssuedAt = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
		d.ExpiresAt = time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	})
	shortTrial := h.sign(t, func(d *entitlement.Document) {
		d.CustomerID = "trial"
		d.DeviceCap = 1000
		d.IssuedAt = time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
		d.ExpiresAt = time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
		d.GraceDays = 0
	})
	upgrade := h.sign(t, func(d *entitlement.Document) {
		d.CustomerID = "upgrade"
		d.Kind = entitlement.KindSubscription
		d.DeviceCap = 0
		d.IssuedAt = time.Date(2026, 4, 15, 0, 0, 0, 0, time.UTC)
		d.ExpiresAt = time.Date(2027, 6, 1, 0, 0, 0, 0, time.UTC)
	})

	// Pasted in the WRONG order: the upgrade first, the subscription last.
	for _, form := range []string{upgrade, subscription} {
		if _, err := h.apply(t, form); err != nil {
			t.Fatal(err)
		}
	}
	if snap := h.svc.Current(context.Background()); snap.Document.CustomerID != "upgrade" || snap.EffectiveCap != 0 {
		t.Errorf("file in force = %s cap %d, want the upgrade (newest issued) unlimited", snap.Document.CustomerID, snap.EffectiveCap)
	}

	// A lapsed trial issued between them changes nothing.
	if _, err := h.apply(t, shortTrial); err != nil {
		t.Fatal(err)
	}
	if snap := h.svc.Current(context.Background()); snap.Document.CustomerID != "upgrade" {
		t.Errorf("a lapsed file displaced a live one: %s", snap.Document.CustomerID)
	}
}

// Every lapsed: the newest of them is shown, so the card names what ran out.
func TestAllLapsedShowsTheNewest(t *testing.T) {
	h := newHarness(t, "")
	h.clock = time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	older := h.sign(t, func(d *entitlement.Document) { d.CustomerID = "older" })
	newer := h.sign(t, func(d *entitlement.Document) {
		d.CustomerID = "newer"
		d.IssuedAt = time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	})
	for _, form := range []string{newer, older} {
		if _, err := h.apply(t, form); err != nil {
			t.Fatal(err)
		}
	}
	snap := h.svc.Current(context.Background())
	if snap.State != StateLapsed || snap.Document.CustomerID != "newer" {
		t.Errorf("snapshot = %s %q", snap.State, snap.Document.CustomerID)
	}
}

// A boot file that does not verify: the install starts on the free cap, the
// card says the file was rejected, and the reason is on the snapshot for
// the boot log. Never a refusal to boot.
func TestRejectedBootFileIsLoudAndFallsToTheFreeCap(t *testing.T) {
	h := newHarness(t, "entl1.bm90LWEtcGF5bG9hZA.AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA")
	snap := h.svc.Current(context.Background())
	if snap.State != StateRejected || snap.EffectiveCap != entitlement.FreeDeviceCap {
		t.Errorf("snapshot = %s cap %d", snap.State, snap.EffectiveCap)
	}
	if len(snap.Rejections) != 1 || snap.Rejections[0].Source != SourceFile {
		t.Errorf("rejections = %+v", snap.Rejections)
	}
	// A valid paste then takes over, and the boot rejection stays visible.
	if _, err := h.apply(t, h.sign(t, func(d *entitlement.Document) {
		d.ExpiresAt = time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	})); err != nil {
		t.Fatal(err)
	}
	snap = h.svc.Current(context.Background())
	if snap.State != StateActive || snap.Source != SourceDatabase {
		t.Errorf("after a valid paste: %s from %s", snap.State, snap.Source)
	}
	if len(snap.Rejections) != 1 {
		t.Errorf("the boot rejection went silent once a paste succeeded: %+v", snap.Rejections)
	}
}

// The boot file wins over nothing in the table and loses to a newer paste —
// it is a candidate like any other, not an override.
func TestBootFileIsACandidate(t *testing.T) {
	seed := newHarness(t, "")
	boot := seed.sign(t, func(d *entitlement.Document) {
		d.CustomerID = "boot"
		d.ExpiresAt = time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	})
	h := newHarness(t, boot)
	h.priv, h.pub = seed.priv, seed.pub
	h.svc.verify = seed.svc.verify
	if snap := h.svc.Current(context.Background()); snap.Source != SourceFile || snap.Document.CustomerID != "boot" {
		t.Fatalf("boot file not in force: %+v", snap)
	}
	if _, err := h.apply(t, h.sign(t, func(d *entitlement.Document) {
		d.CustomerID = "pasted"
		d.IssuedAt = time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
		d.ExpiresAt = time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	})); err != nil {
		t.Fatal(err)
	}
	if snap := h.svc.Current(context.Background()); snap.Source != SourceDatabase || snap.Document.CustomerID != "pasted" {
		t.Errorf("a newer paste did not win over the boot file: %+v", snap)
	}
}

// A failed read of the stored files never lifts the cap. With nothing read
// before and no boot file, that one request is bounded by the free cap; the
// failure is not cached, so the very next read sees the applied file again.
func TestUnreadableTableIsNotCachedAsTheFreeCap(t *testing.T) {
	h := newHarness(t, "")
	h.repo.rows = append(h.repo.rows, entitlement.Record{ID: uuid.New(), Document: h.sign(t, func(d *entitlement.Document) {
		d.ExpiresAt = time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	})})
	h.repo.listErr = errors.New("database is unhappy")
	if cap, _ := h.svc.EffectiveDeviceCap(context.Background()); cap != entitlement.FreeDeviceCap {
		t.Errorf("cap on a failed first read = %d, want the free cap %d (a failed read never lifts the cap)", cap, entitlement.FreeDeviceCap)
	}
	h.repo.listErr = nil
	if cap, _ := h.svc.EffectiveDeviceCap(context.Background()); cap != 100 {
		t.Errorf("cap right after the failed read = %d, want the applied file's 100: the failure was cached", cap)
	}
}

// With nothing read before, a failed read is bounded by what can be
// established without the table: a boot file that verifies answers with its
// own cap, never with "unlimited".
func TestUnreadableTableFirstReadIsBoundedByTheBootFile(t *testing.T) {
	seed := newHarness(t, "")
	boot := seed.sign(t, func(d *entitlement.Document) {
		d.CustomerID = "boot"
		d.DeviceCap = 60
		d.ExpiresAt = time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	})
	h := newHarness(t, boot)
	h.svc.verify = seed.svc.verify
	h.repo.listErr = errors.New("database is unhappy")
	if cap, _ := h.svc.EffectiveDeviceCap(context.Background()); cap != 60 {
		t.Errorf("cap on a failed first read with a boot file = %d, want the boot file's 60", cap)
	}
}

// Once a resolution has succeeded, a later failed read answers with it,
// past the TTL, instead of the free cap — and the card says the same.
func TestUnreadableTableKeepsTheLastResolution(t *testing.T) {
	h := newHarness(t, "")
	if _, err := h.apply(t, h.sign(t, func(d *entitlement.Document) {
		d.ExpiresAt = time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	})); err != nil {
		t.Fatal(err)
	}
	h.clock = h.clock.Add(2 * cacheTTL)
	h.repo.listErr = errors.New("database is unhappy")
	if cap, _ := h.svc.EffectiveDeviceCap(context.Background()); cap != 100 {
		t.Errorf("cap on a failed read after a good one = %d, want the last resolution's 100", cap)
	}
	if snap := h.svc.Current(context.Background()); snap.State != StateActive || snap.EffectiveCap != 100 {
		t.Errorf("card on a failed read = state %q cap %d, want the last resolution (active, 100)", snap.State, snap.EffectiveCap)
	}
}

func TestApplyRefusesAFileThatDoesNotVerifyAndStoresNothing(t *testing.T) {
	h := newHarness(t, "")
	other := newHarness(t, "")
	_, err := h.apply(t, other.sign(t, nil)) // signed by a key this install does not trust
	var rejected *entitlement.RejectedError
	if !errors.As(err, &rejected) {
		t.Fatalf("err = %v, want *entitlement.RejectedError", err)
	}
	if !errors.Is(err, entitlement.ErrSignatureInvalid) {
		t.Errorf("the refusal does not carry the reason: %v", err)
	}
	if rejected.HTTPStatus() != 400 || rejected.ErrorCode() != entitlement.RejectedErrorCode {
		t.Errorf("status %d code %q", rejected.HTTPStatus(), rejected.ErrorCode())
	}
	if len(h.repo.rows) != 0 {
		t.Errorf("a refused file was stored")
	}
	if _, ok := h.audit.Find(audit_event.ActionEntitlementApply); ok {
		t.Error("a refused file was audited as applied")
	}
	if _, err := h.apply(t, "   "); !errors.As(err, &rejected) {
		t.Errorf("a blank paste: %v", err)
	}
}

func TestApplyStoresAuditsAndTakesEffectAtOnce(t *testing.T) {
	h := newHarness(t, "")
	h.clock = time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	if snap := h.svc.Current(context.Background()); snap.State != StateFree {
		t.Fatalf("precondition: %s", snap.State)
	}
	snap, err := h.apply(t, h.sign(t, func(d *entitlement.Document) { d.Note = "ninety days" }))
	if err != nil {
		t.Fatal(err)
	}
	if snap.State != StateActive || snap.EffectiveCap != 100 || snap.Document.Note != "ninety days" {
		t.Errorf("snapshot after apply = %+v", snap)
	}
	if len(h.repo.rows) != 1 || h.repo.rows[0].CustomerID != "customer-1" || h.repo.rows[0].Document == "" {
		t.Errorf("stored rows = %+v", h.repo.rows)
	}
	ev, ok := h.audit.Find(audit_event.ActionEntitlementApply)
	if !ok {
		t.Fatal("no audit row")
	}
	if ev.TargetID != "customer-1" || ev.TargetType != audit_event.TargetEntitlement {
		t.Errorf("audit target = %s %s", ev.TargetType, ev.TargetID)
	}
	// The detail carries the terms, never the file.
	if len(ev.Detail) == 0 || len(ev.Detail) > 300 || containsForm(ev.Detail) {
		t.Errorf("audit detail = %q", ev.Detail)
	}
	// Admission sees it without waiting for the cache.
	if cap, _ := h.svc.EffectiveDeviceCap(context.Background()); cap != 100 {
		t.Errorf("admission cap right after apply = %d", cap)
	}
}

func containsForm(s string) bool {
	for i := 0; i+len(entitlement.FormPrefix) <= len(s); i++ {
		if s[i:i+len(entitlement.FormPrefix)] == entitlement.FormPrefix {
			return true
		}
	}
	return false
}

// The free cap is a floor: a genuine file signing a positive cap below it is
// accepted and enforced as the free cap, in force and on the card, while 0
// stays unlimited. Expiry and grace are unchanged — once lapsed every file
// falls to the free cap for new admissions.
func TestSignedCapBelowTheFreeCapIsEnforcedAsTheFloor(t *testing.T) {
	phases := []struct {
		name  string
		at    time.Time
		state State
	}{
		{"active", time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC), StateActive},
		{"grace", time.Date(2026, 4, 15, 0, 0, 0, 0, time.UTC), StateGrace},
		{"lapsed", time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC), StateLapsed},
	}
	for _, signed := range []int{0, 1, 24, 25, 26} {
		for _, ph := range phases {
			h := newHarness(t, "")
			h.clock = time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
			if _, err := h.apply(t, h.sign(t, func(d *entitlement.Document) { d.DeviceCap = signed })); err != nil {
				t.Fatalf("cap %d: a genuine file must be accepted: %v", signed, err)
			}
			h.svc.invalidate()
			h.clock = ph.at

			want := signed
			if ph.state == StateLapsed || (signed > 0 && signed < entitlement.FreeDeviceCap) {
				want = entitlement.FreeDeviceCap
			}
			below := signed > 0 && signed < entitlement.FreeDeviceCap

			snap := h.svc.Current(context.Background())
			if snap.State != ph.state || snap.EffectiveCap != want {
				t.Errorf("cap %d %s: state %s cap %d, want %s cap %d", signed, ph.name, snap.State, snap.EffectiveCap, ph.state, want)
			}
			if snap.SignedCapBelowFloor != below {
				t.Errorf("cap %d %s: SignedCapBelowFloor %v, want %v", signed, ph.name, snap.SignedCapBelowFloor, below)
			}
			if snap.Document == nil || snap.Document.DeviceCap != signed {
				t.Errorf("cap %d %s: the card must keep the signed value", signed, ph.name)
			}
			if cap, _ := h.svc.EffectiveDeviceCap(context.Background()); cap != want {
				t.Errorf("cap %d %s: admission cap %d, want %d", signed, ph.name, cap, want)
			}
		}
	}
}

// The clamp is said once per file, not once per cache refresh, and a file
// at or above the floor says nothing.
func TestBelowFloorFileWarnsOnce(t *testing.T) {
	h := newHarness(t, "")
	var buf bytes.Buffer
	h.svc.logger = slog.New(slog.NewTextHandler(&buf, nil))
	h.clock = time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	if _, err := h.apply(t, h.sign(t, func(d *entitlement.Document) { d.DeviceCap = 10 })); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		h.svc.invalidate()
		h.svc.EffectiveDeviceCap(context.Background())
	}
	h.svc.LogBootState(context.Background())
	want := fmt.Sprintf("signs a device cap of 10, below the free cap of %d", entitlement.FreeDeviceCap)
	if n := strings.Count(buf.String(), want); n != 1 {
		t.Errorf("%d lines say %q, want exactly 1:\n%s", n, want, buf.String())
	}
	if !strings.Contains(buf.String(), "signed_device_cap=10") {
		t.Errorf("the boot line does not name the signed cap:\n%s", buf.String())
	}

	h2 := newHarness(t, "")
	buf.Reset()
	h2.svc.logger = slog.New(slog.NewTextHandler(&buf, nil))
	h2.clock = time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	if _, err := h2.apply(t, h2.sign(t, func(d *entitlement.Document) { d.DeviceCap = 25 })); err != nil {
		t.Fatal(err)
	}
	h2.svc.LogBootState(context.Background())
	if strings.Contains(buf.String(), "below the free cap") {
		t.Errorf("a file at the floor warned:\n%s", buf.String())
	}
}

// IsHostedInstall is what the SPA words the account-erasure request by: only
// the hosted install has an operator console that performs one. It follows
// the KIND of the file in force, so a lapsed hosted file still says hosted,
// and an install with no file, or any other kind, is self-hosted.
func TestIsHostedInstallFollowsTheKindInForce(t *testing.T) {
	ctx := context.Background()

	none := newHarness(t, "")
	if none.svc.IsHostedInstall(ctx) {
		t.Fatal("an install with no entitlement file reported itself hosted")
	}

	eval := newHarness(t, "")
	eval.svc.bootDocument = eval.sign(t, func(d *entitlement.Document) {
		d.ExpiresAt = eval.clock.AddDate(1, 0, 0)
	})
	if eval.svc.IsHostedInstall(ctx) {
		t.Fatal("an evaluation file reported the install hosted")
	}

	hosted := newHarness(t, "")
	hosted.svc.bootDocument = hosted.sign(t, func(d *entitlement.Document) {
		d.Kind = entitlement.KindHosted
		d.GraceDays = 0
	}) // expired before the harness clock: lapsed
	if !hosted.svc.IsHostedInstall(ctx) {
		t.Fatal("a lapsed hosted file stopped reporting the install hosted")
	}
}

// A valid file is stored whatever its dates, but only the newest non-lapsed
// file is in force: the paste answer says which file was pasted and whether
// it took effect, so an older or lapsed paste is never silently a no-op.
func TestApplyNamesThePastedFileAndWhetherItIsInForce(t *testing.T) {
	h := newHarness(t, "")
	h.clock = time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	current := h.sign(t, func(d *entitlement.Document) {
		d.CustomerID = "current"
		d.DeviceCap = 500
		d.IssuedAt = time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
		d.ExpiresAt = time.Date(2027, 4, 1, 0, 0, 0, 0, time.UTC)
	})
	older := h.sign(t, func(d *entitlement.Document) {
		d.CustomerID = "older"
		d.DeviceCap = 50
		d.IssuedAt = time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
		d.ExpiresAt = time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)
	})
	lapsed := h.sign(t, func(d *entitlement.Document) {
		d.CustomerID = "lapsed"
		d.DeviceCap = 70
		d.IssuedAt = time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
		d.ExpiresAt = time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)
		d.GraceDays = 0
	})
	inGrace := h.sign(t, func(d *entitlement.Document) {
		d.CustomerID = "grace"
		d.IssuedAt = time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC)
		d.ExpiresAt = time.Date(2026, 4, 15, 0, 0, 0, 0, time.UTC)
		d.GraceDays = 30
	})

	cases := []struct {
		name      string
		form      string
		wantState State
		inForce   bool
		wantCap   int
	}{
		{"the first file takes effect", current, StateActive, true, 500},
		{"an older active file is stored, not in force", older, StateActive, false, 50},
		{"a lapsed file is stored, not in force", lapsed, StateLapsed, false, 70},
		{"an older file in grace is stored, not in force", inGrace, StateGrace, false, 100},
		{"re-pasting the file in force says it is in force", current, StateActive, true, 500},
	}
	for _, tc := range cases {
		snap, err := h.apply(t, tc.form)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if snap.Document == nil || snap.Document.CustomerID != "current" {
			t.Fatalf("%s: the file in force moved: %+v", tc.name, snap.Document)
		}
		a := snap.Applied
		if a == nil || a.Document == nil {
			t.Fatalf("%s: the answer does not name the pasted file", tc.name)
		}
		if a.InForce != tc.inForce || a.State != tc.wantState || a.Document.DeviceCap != tc.wantCap {
			t.Errorf("%s: applied = in_force %v state %s cap %d, want %v %s %d",
				tc.name, a.InForce, a.State, a.Document.DeviceCap, tc.inForce, tc.wantState, tc.wantCap)
		}
	}
	if len(h.repo.rows) != len(cases) {
		t.Errorf("stored %d rows, want every valid paste (%d)", len(h.repo.rows), len(cases))
	}
	// A read is not a paste.
	if snap := h.svc.Current(context.Background()); snap.Applied != nil {
		t.Errorf("a read carries applied: %+v", snap.Applied)
	}
}

// A paste of the same file the boot handed in is the file in force.
func TestApplyOfTheBootFileIsInForce(t *testing.T) {
	h := newHarness(t, "")
	h.clock = time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	form := h.sign(t, nil)
	h.svc.bootDocument = form
	snap, err := h.apply(t, form)
	if err != nil {
		t.Fatal(err)
	}
	if snap.Applied == nil || !snap.Applied.InForce || snap.Applied.State != StateActive {
		t.Errorf("applied = %+v, want in force and active", snap.Applied)
	}
}
