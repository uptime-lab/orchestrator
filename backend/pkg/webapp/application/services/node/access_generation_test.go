package node

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
)

// genFakeRepo is the smallest node repository the generation paths need.
type genFakeRepo struct {
	repository.NodeRepository
	node    *node.Node
	updates int
	// columnWrites counts UpdateNodeColumns calls specifically, so a test can
	// assert that a no-op path writes nothing at all.
	columnWrites int
}

func (r *genFakeRepo) GetByID(_ context.Context, _, _ uuid.UUID) (*node.Node, error) {
	if r.node == nil {
		return nil, errors.New("not found")
	}
	return r.node, nil
}

func (r *genFakeRepo) Update(_ context.Context, n *node.Node) error {
	r.updates++
	r.node = n
	return nil
}

// UpdateNodeColumns mirrors the real repository: it writes NAMED columns, not a
// whole row. The fake applies them to its stored node so the assertions below
// still read the effect — and it fails loudly on a column it does not know,
// because a typo in a column name is silent in the map form and this is the only
// place that can catch it.
func (r *genFakeRepo) UpdateNodeColumns(_ context.Context, _, _ uuid.UUID, columns map[string]interface{}) error {
	r.updates++
	r.columnWrites++
	if r.node == nil {
		return errors.New("not found")
	}
	for name, value := range columns {
		switch name {
		case "access_applied_generation":
			r.node.AccessAppliedGeneration, _ = value.(string)
		case "access_applied_at":
			if value == nil {
				r.node.AccessAppliedAt = nil
				continue
			}
			t, ok := value.(time.Time)
			if !ok {
				return fmt.Errorf("access_applied_at got %T, want time.Time", value)
			}
			r.node.AccessAppliedAt = &t
		case "device_profile_applied_hash":
			r.node.DeviceProfileAppliedHash, _ = value.(string)
		case "device_profile_applied_at":
			if value == nil {
				r.node.DeviceProfileAppliedAt = nil
				continue
			}
			t, ok := value.(time.Time)
			if !ok {
				return fmt.Errorf("device_profile_applied_at got %T, want time.Time", value)
			}
			r.node.DeviceProfileAppliedAt = &t
		case "watchdog_applied_hash":
			r.node.WatchdogAppliedHash, _ = value.(string)
		case "watchdog_disarm_reason":
			r.node.WatchdogDisarmReason, _ = value.(string)
		case "watchdog_last_refusal":
			r.node.WatchdogLastRefusal, _ = value.(string)
		case "led_applied_hash":
			r.node.LEDAppliedHash, _ = value.(string)
		default:
			return fmt.Errorf("unexpected column %q", name)
		}
	}
	return nil
}

// genFakeResolver returns a fixed desired hash, or an error to prove that "we
// could not resolve" never turns into a push.
type genFakeResolver struct {
	hash string
	err  error
	// managed records the password-auth policy the caller folded in, because a
	// hash resolved without it would miss a policy-only change.
	managed bool
	calls   int
	// fullResolves counts the expensive resolve, which this path must not use.
	fullResolves int
}

// The full resolve must NOT be what the generation path calls: it decrypts the
// node's whole secret set and stamps last_used_at, on every managed-OS report
// of every converged node, for a command that carries no secrets. Failing here
// is how that regression gets caught.
func (f *genFakeResolver) ResolveEffectiveProvisioning(_ context.Context, _, _ uuid.UUID) (services.EffectiveProvisioning, error) {
	f.fullResolves++
	return services.EffectiveProvisioning{SSHPasswordAuthManaged: f.managed}, nil
}

func (f *genFakeResolver) ResolvePasswordAuthPolicy(_ context.Context, _, _ uuid.UUID) (bool, bool, error) {
	return true, f.managed, nil
}

func (f *genFakeResolver) ResolveEffectiveAccess(_ context.Context, _, _ uuid.UUID) ([]services.ResolvedAccessEntry, error) {
	return nil, nil
}

func (f *genFakeResolver) ResolveAccessConfig(_ context.Context, _, _ uuid.UUID, managePasswordAuth, _ bool) (services.AccessConfig, error) {
	f.calls++
	if f.err != nil {
		return services.AccessConfig{}, f.err
	}
	if managePasswordAuth != f.managed {
		return services.AccessConfig{}, errors.New("password-auth policy was not folded into the hash")
	}
	return services.AccessConfig{Hash: f.hash}, nil
}

func genService(repo *genFakeRepo, resolver *genFakeResolver) *Service {
	svc := &Service{nodeRepo: repo, logger: slog.Default()}
	if resolver != nil {
		svc.SetAccessGenerationResolvers(resolver, resolver)
	}
	return svc
}

func TestRecordAccessAppliedStoresTheGenerationAndTime(t *testing.T) {
	repo := &genFakeRepo{node: &node.Node{ID: uuid.New()}}
	svc := genService(repo, &genFakeResolver{hash: "abc123", managed: true})

	if err := svc.RecordAccessApplied(context.Background(), uuid.New(), repo.node.ID, "abc123"); err != nil {
		t.Fatalf("record failed: %v", err)
	}
	if repo.node.AccessAppliedGeneration != "abc123" {
		t.Errorf("generation = %q, want abc123", repo.node.AccessAppliedGeneration)
	}
	if repo.node.AccessAppliedAt == nil {
		t.Error("applied time must be stamped: an applied generation with no time cannot be aged out later")
	}
}

func TestRecordAccessAppliedRefusesAGenerationThePlatformDidNotAskFor(t *testing.T) {
	// The attack this closes: an agent reports the hash of a config the operator
	// has not pushed yet — say the same config with a contractor's user removed.
	// Stored, it makes the drift check find applied == desired the moment the
	// operator really removes that user, so the revocation is never dispatched
	// and nothing says so.
	repo := &genFakeRepo{node: &node.Node{ID: uuid.New(), AccessAppliedGeneration: "current"}}
	svc := genService(repo, &genFakeResolver{hash: "desired-now", managed: true})

	if err := svc.RecordAccessApplied(context.Background(), uuid.New(), repo.node.ID, "a-hash-of-the-agent's-choosing"); err != nil {
		t.Fatalf("a refused claim is not an error: %v", err)
	}
	if repo.node.AccessAppliedGeneration != "current" {
		t.Errorf("generation = %q, want the previous value kept: refusing to record must not also erase what was known",
			repo.node.AccessAppliedGeneration)
	}
	if repo.node.AccessAppliedAt != nil {
		t.Error("nothing was recorded, so nothing may be stamped")
	}
}

func TestRecordAccessAppliedStoresThePlatformsOwnString(t *testing.T) {
	// Equal, but not the same bytes the agent sent: what lands in the column is
	// the platform's value. Nothing downstream should ever be able to be fed a
	// string chosen on the device, even one that compares equal today.
	repo := &genFakeRepo{node: &node.Node{ID: uuid.New()}}
	resolver := &genFakeResolver{hash: "deadbeef", managed: true}
	svc := genService(repo, resolver)

	if err := svc.RecordAccessApplied(context.Background(), uuid.New(), repo.node.ID, "deadbeef"); err != nil {
		t.Fatalf("record failed: %v", err)
	}
	if resolver.calls != 1 {
		t.Errorf("the desired hash must be resolved to verify the claim, calls = %d", resolver.calls)
	}
	if repo.node.AccessAppliedGeneration != "deadbeef" {
		t.Errorf("generation = %q, want deadbeef", repo.node.AccessAppliedGeneration)
	}
}

func TestRecordAccessAppliedFailsWhenTheDesiredHashCannotBeResolved(t *testing.T) {
	// Unverifiable is not the same as refused: the caller logs it, and the node
	// keeps whatever it had, so the next managed-OS report re-applies.
	repo := &genFakeRepo{node: &node.Node{ID: uuid.New(), AccessAppliedGeneration: "current"}}
	svc := genService(repo, &genFakeResolver{err: errors.New("database is down")})

	if err := svc.RecordAccessApplied(context.Background(), uuid.New(), repo.node.ID, "abc123"); err == nil {
		t.Fatal("a resolution failure must surface, not silently drop the report")
	}
	if repo.node.AccessAppliedGeneration != "current" {
		t.Errorf("generation = %q, want the previous value kept", repo.node.AccessAppliedGeneration)
	}
}

func TestRecordAccessAppliedRecordsNothingWithoutResolvers(t *testing.T) {
	// SetAccessGenerationResolvers is optional. Without it nothing can verify the
	// claim — and nothing reads the column either, because the self-heal is
	// disabled by the same absence. Writing an unverified value there would be
	// the old behaviour, for no reader.
	repo := &genFakeRepo{node: &node.Node{ID: uuid.New()}}
	svc := genService(repo, nil)

	if err := svc.RecordAccessApplied(context.Background(), uuid.New(), repo.node.ID, "abc123"); err != nil {
		t.Fatalf("record failed: %v", err)
	}
	if repo.node.AccessAppliedGeneration != "" {
		t.Errorf("generation = %q, want nothing recorded", repo.node.AccessAppliedGeneration)
	}
}

func TestRecordAccessAppliedIgnoresAnEmptyGeneration(t *testing.T) {
	// An agent that reports no generation (a non-Linux node, an older build) is
	// not making a claim, and writing "" would silently look like a clear.
	repo := &genFakeRepo{node: &node.Node{ID: uuid.New(), AccessAppliedGeneration: "kept"}}
	svc := genService(repo, nil)

	if err := svc.RecordAccessApplied(context.Background(), uuid.New(), repo.node.ID, ""); err != nil {
		t.Fatalf("record failed: %v", err)
	}
	if repo.node.AccessAppliedGeneration != "kept" || repo.updates != 0 {
		t.Error("an empty generation must change nothing")
	}
}

func TestClearAccessAppliedIsIdempotent(t *testing.T) {
	repo := &genFakeRepo{node: &node.Node{ID: uuid.New()}}
	svc := genService(repo, nil)

	if err := svc.ClearAccessApplied(context.Background(), uuid.New(), repo.node.ID); err != nil {
		t.Fatalf("clear failed: %v", err)
	}
	if repo.updates != 0 {
		t.Error("clearing an already-empty generation must not write")
	}
}

// A node with no recorded generation now takes the FIRST-CONNECT path rather
// than being skipped outright: entries created before a node was adopted used to
// reach it never, because propagation only walks the nodes that exist at the
// time of the edit. The protection that made the old skip necessary moved into
// shouldApplyOnFirstConnect, which is tested below.
func TestReconcileRoutesANodeWithNoGenerationToTheFirstConnectPath(t *testing.T) {
	repo := &genFakeRepo{node: &node.Node{ID: uuid.New()}}
	resolver := &genFakeResolver{hash: "desired"}
	svc := genService(repo, resolver)

	svc.reconcileAccessGeneration(context.Background(), uuid.New(), repo.node.ID, "")

	// With no command service wired the first-connect path stops at "has an
	// apply ever been attempted?", which answers yes when it cannot tell. What
	// matters here is that nothing was pushed.
	if repo.updates != 0 {
		t.Error("the first-connect path must not write to the node")
	}
}

func TestShouldApplyOnFirstConnect(t *testing.T) {
	withKeys := services.AccessConfig{
		Users: []services.AccessConfigUser{{Username: "alice", AuthorizedKeys: []string{"ssh-ed25519 AAAA"}}},
	}

	if !shouldApplyOnFirstConnect(withKeys, false, false) {
		t.Error("a node an entry reaches with a usable key must get its first apply")
	}
	if shouldApplyOnFirstConnect(withKeys, true, false) {
		t.Error("an apply already in flight owns the node")
	}
	// Fires once. Re-enqueueing on every system-info report would turn one
	// permanently refused config into a command every 30 seconds forever.
	if shouldApplyOnFirstConnect(withKeys, false, true) {
		t.Error("a node that has already been sent an apply must not get another")
	}
}

// The config must grant a login, not merely exist. This is the empty-config fear
// reached from the other side: a user with no keys is LOCKED by definition, so a
// config made only of those would make the platform's first ever act on a device
// the locking of an account. An account whose only entry is a keyless "pi" is
// the case this guards.
func TestFirstConnectRequiresAConfigThatGrantsALogin(t *testing.T) {
	cases := map[string]services.AccessConfig{
		"nothing at all":      {},
		"a user with no keys": {Users: []services.AccessConfigUser{{Username: "pi"}}},
		"a CA nobody uses":    {CAs: []services.AccessConfigCA{{}}},
		"principals, no CA":   {Users: []services.AccessConfigUser{{Username: "pi", Principals: []string{"ops"}}}},
	}
	for name, cfg := range cases {
		if shouldApplyOnFirstConnect(cfg, false, false) {
			t.Errorf("%s: must not trigger a first apply", name)
		}
	}

	// A CA plus a user holding principals IS a working login: that is the
	// certificate path, and the agent's own lockout gate counts it the same way.
	withCert := services.AccessConfig{
		Users: []services.AccessConfigUser{{Username: "ops", Principals: []string{"ops"}}},
		CAs:   []services.AccessConfigCA{{}},
	}
	if !shouldApplyOnFirstConnect(withCert, false, false) {
		t.Error("a trusted CA with a principal must count as access worth applying")
	}
}

func TestReconcileDoesNothingWhenConverged(t *testing.T) {
	repo := &genFakeRepo{node: &node.Node{ID: uuid.New()}}
	resolver := &genFakeResolver{hash: "same"}
	svc := genService(repo, resolver)

	svc.reconcileAccessGeneration(context.Background(), uuid.New(), repo.node.ID, "same")

	if repo.updates != 0 {
		t.Error("a converged node must not be touched")
	}
}

func TestReconcileTreatsAResolutionFailureAsUnknown(t *testing.T) {
	// "We could not work out what this node should have" is not grounds for
	// pushing anything — an empty access config means "lock every managed user".
	repo := &genFakeRepo{node: &node.Node{ID: uuid.New()}}
	resolver := &genFakeResolver{err: errors.New("database is down")}
	svc := genService(repo, resolver)

	svc.reconcileAccessGeneration(context.Background(), uuid.New(), repo.node.ID, "applied")

	if repo.updates != 0 {
		t.Error("a resolution failure must not enqueue anything")
	}
}

func TestResolveAccessGenerationFoldsInThePasswordPolicy(t *testing.T) {
	// The policy is resolved first and passed into the config so it lands inside
	// the hash. Resolving it any other way produces a hash that cannot equal the
	// one the device holds, and the node would re-apply on every connect.
	repo := &genFakeRepo{node: &node.Node{ID: uuid.New()}}
	resolver := &genFakeResolver{hash: "h", managed: true}
	svc := genService(repo, resolver)

	got, err := svc.resolveAccessGeneration(context.Background(), uuid.New(), repo.node.ID)
	if err != nil {
		t.Fatalf("resolve failed: %v", err)
	}
	if got != "h" {
		t.Errorf("hash = %q, want h", got)
	}
	// The hash needs two booleans, not the full provisioning
	// resolve. Reaching for the latter here brings back an OU-chain load, a
	// decrypt of every secret attached to the node and a TouchLastUsed UPDATE,
	// on a path that runs for every converged node on every managed-OS report.
	if resolver.fullResolves != 0 {
		t.Errorf("resolveAccessGeneration called ResolveEffectiveProvisioning %d time(s); "+
			"it must use ResolvePasswordAuthPolicy", resolver.fullResolves)
	}
}
