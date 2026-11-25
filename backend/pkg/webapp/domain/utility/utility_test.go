package utility

import (
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/workflow_block"
)

func testCatalog() *Catalog {
	return &Catalog{Utilities: []Definition{
		{
			ID: "docker", Name: "Docker", Class: ClassReconciled,
			Params: []workflow_block.ParamSpec{
				{Name: "log_max_size", Type: workflow_block.ParamString, Default: "10m"},
				{Name: "channel", Type: workflow_block.ParamEnum, EnumValues: []string{"distro"}, Default: "distro"},
			},
		},
		{ID: "kiosk", Name: "Kiosk", Class: ClassReconciled, DependsOn: []string{"docker"}},
	}}
}

func TestResolveNearestLevelWins(t *testing.T) {
	c := testCatalog()
	nodeID := uuid.New()
	ownOU := uuid.New()
	parentOU := uuid.New()

	assignments := []Assignment{
		{ID: uuid.New(), UtilityID: "docker", AccountWide: true, Enabled: true,
			Config: map[string]interface{}{"log_max_size": "50m"}},
		{ID: uuid.New(), UtilityID: "docker", OUIDs: []uuid.UUID{parentOU}, Enabled: true,
			Config: map[string]interface{}{"log_max_size": "20m"}},
		{ID: uuid.New(), UtilityID: "docker", OUIDs: []uuid.UUID{ownOU}, Enabled: true,
			Config: map[string]interface{}{"log_max_size": "5m"}},
	}
	set := ResolveForNode(c, assignments, nodeID, []uuid.UUID{ownOU, parentOU})
	if len(set) != 1 {
		t.Fatalf("expected 1 resolved utility, got %d", len(set))
	}
	r := set[0]
	if r.Source != ScopeOU || r.SourceOUID == nil || *r.SourceOUID != ownOU {
		t.Fatalf("expected the node's own OU to win, got source=%s", r.Source)
	}
	// The whole block wins: log_max_size from the winner, channel from the
	// schema default — never a field merge across levels.
	if r.Config["log_max_size"] != "5m" || r.Config["channel"] != "distro" {
		t.Fatalf("unexpected config: %v", r.Config)
	}
}

func TestResolveNodeBeatsOU(t *testing.T) {
	c := testCatalog()
	nodeID := uuid.New()
	ownOU := uuid.New()
	assignments := []Assignment{
		{ID: uuid.New(), UtilityID: "docker", OUIDs: []uuid.UUID{ownOU}, Enabled: true},
		{ID: uuid.New(), UtilityID: "docker", NodeID: &nodeID, Enabled: true,
			Config: map[string]interface{}{"log_max_size": "1m"}},
	}
	set := ResolveForNode(c, assignments, nodeID, []uuid.UUID{ownOU})
	if len(set) != 1 || set[0].Source != ScopeNode {
		t.Fatalf("expected the node-level assignment to win: %+v", set)
	}
}

func TestResolveSkipsDisabledUnassignedAndForeign(t *testing.T) {
	c := testCatalog()
	nodeID := uuid.New()
	otherNode := uuid.New()
	foreignOU := uuid.New()
	assignments := []Assignment{
		{ID: uuid.New(), UtilityID: "docker", AccountWide: true, Enabled: false},
		{ID: uuid.New(), UtilityID: "docker", Enabled: true},                                // unassigned
		{ID: uuid.New(), UtilityID: "docker", NodeID: &otherNode, Enabled: true},            // other node
		{ID: uuid.New(), UtilityID: "docker", OUIDs: []uuid.UUID{foreignOU}, Enabled: true}, // OU off-chain
	}
	if set := ResolveForNode(c, assignments, nodeID, nil); len(set) != 0 {
		t.Fatalf("expected empty set, got %+v", set)
	}
}

func TestResolveSkipsUnknownCatalogID(t *testing.T) {
	c := testCatalog()
	nodeID := uuid.New()
	assignments := []Assignment{
		{ID: uuid.New(), UtilityID: "vanished", AccountWide: true, Enabled: true},
	}
	if set := ResolveForNode(c, assignments, nodeID, nil); len(set) != 0 {
		t.Fatalf("a row whose id left the registry must be skipped, got %+v", set)
	}
}

func TestHashChangesWithConfigAndIsCanonical(t *testing.T) {
	a := HashConfig("docker", map[string]interface{}{"a": "1", "b": "2"})
	b := HashConfig("docker", map[string]interface{}{"b": "2", "a": "1"})
	if a != b {
		t.Fatal("hash must not depend on key order")
	}
	if a == HashConfig("docker", map[string]interface{}{"a": "1", "b": "3"}) {
		t.Fatal("hash must change with a value")
	}
	if a == HashConfig("kiosk", map[string]interface{}{"a": "1", "b": "2"}) {
		t.Fatal("hash must change with the utility id")
	}
}

func TestSetConstraints(t *testing.T) {
	c := testCatalog()
	kiosk, _ := c.Get("kiosk")
	docker, _ := c.Get("docker")
	if err := CheckSetConstraints([]Resolved{{Definition: kiosk}}); err == nil {
		t.Fatal("kiosk without docker must fail the dependency check")
	}
	if err := CheckSetConstraints([]Resolved{{Definition: kiosk}, {Definition: docker}}); err != nil {
		t.Fatalf("kiosk with docker must pass: %v", err)
	}
}

func TestValidateScopeXOR(t *testing.T) {
	id := uuid.New()
	bad := Assignment{AccountWide: true, OUIDs: []uuid.UUID{id}}
	if err := bad.ValidateScope(); err == nil {
		t.Fatal("two targets must be refused")
	}
	if err := (&Assignment{}).ValidateScope(); err != nil {
		t.Fatal("unassigned (no target) is valid")
	}
}

func TestValidateConfigBoundsUnknownKeys(t *testing.T) {
	c := testCatalog()
	docker, _ := c.Get("docker")
	if err := docker.ValidateConfig(map[string]interface{}{"nope": "x"}); err == nil {
		t.Fatal("unknown params must be refused — utility params are registry-bounded")
	}
	if err := docker.ValidateConfig(map[string]interface{}{"channel": "edge"}); err == nil {
		t.Fatal("enum violation must be refused")
	}
	if err := docker.ValidateConfig(map[string]interface{}{"log_max_size": "5m"}); err != nil {
		t.Fatalf("valid config refused: %v", err)
	}
}

func TestLadderStatus(t *testing.T) {
	s := &NodeState{DesiredHash: "h1"}
	if s.Status() != StatusDeclared {
		t.Fatal("no applied hash => declared")
	}
	s.AppliedHash = "h0"
	if s.Status() != StatusUpdatePending {
		t.Fatal("stale applied hash => update_pending")
	}
	s.AppliedHash = "h1"
	if s.Status() != StatusApplied {
		t.Fatal("matching hash => applied")
	}
	s.LastError = "failed: apt exploded"
	if s.Status() != StatusFailed {
		t.Fatal("last error => failed")
	}
}

func TestCatalogValidate(t *testing.T) {
	bad := &Catalog{Utilities: []Definition{{ID: "a", Name: "A", Class: "wat"}}}
	if err := bad.Validate(); err == nil {
		t.Fatal("unknown class must fail validation")
	}
	dup := &Catalog{Utilities: []Definition{
		{ID: "a", Name: "A", Class: ClassReconciled},
		{ID: "a", Name: "A2", Class: ClassReconciled},
	}}
	if err := dup.Validate(); err == nil {
		t.Fatal("duplicate id must fail validation")
	}
	dangling := &Catalog{Utilities: []Definition{
		{ID: "a", Name: "A", Class: ClassReconciled, DependsOn: []string{"ghost"}},
	}}
	if err := dangling.Validate(); err == nil {
		t.Fatal("dangling depends_on must fail validation")
	}
	// An implied dependency carries defaults alone, so a dependency with a
	// required param and no default would fail at apply on every node.
	incomplete := &Catalog{Utilities: []Definition{
		{ID: "runtime", Name: "Runtime", Class: ClassReconciled,
			Params: []workflow_block.ParamSpec{{Name: "socket", Type: workflow_block.ParamString, Required: true}}},
		{ID: "app", Name: "App", Class: ClassReconciled, DependsOn: []string{"runtime"}},
	}}
	if err := incomplete.Validate(); err == nil || !strings.Contains(err.Error(), "cannot be implied") {
		t.Fatalf("a dependency that needs a required param without a default must be refused, got %v", err)
	}
	if err := testCatalog().Validate(); err != nil {
		t.Fatalf("good catalog refused: %v", err)
	}
}

func TestSortByDependencies(t *testing.T) {
	// Names chosen so alphabetical order is WRONG: "aaa" depends on "zzz".
	set := []Resolved{
		{Definition: Definition{ID: "aaa", DependsOn: []string{"zzz"}}},
		{Definition: Definition{ID: "zzz"}},
	}
	sorted := SortByDependencies(set)
	if sorted[0].Definition.ID != "zzz" || sorted[1].Definition.ID != "aaa" {
		t.Fatalf("dependency must precede dependent, got %s then %s",
			sorted[0].Definition.ID, sorted[1].Definition.ID)
	}
	// A dependency OUTSIDE the set does not reorder anything.
	solo := SortByDependencies([]Resolved{{Definition: Definition{ID: "kiosk", DependsOn: []string{"docker"}}}})
	if len(solo) != 1 {
		t.Fatal("sort must not drop entries")
	}
}

func TestResolveOUSetSpansLevelsWithoutSelfShadow(t *testing.T) {
	c := testCatalog()
	nodeID := uuid.New()
	ownOU := uuid.New()
	parentOU := uuid.New()
	// One assignment spans BOTH the node's own OU and its parent: it must win
	// at the own-OU level and provenance must name the WINNING unit — with a
	// set scope "which OU won" is a per-node fact.
	assignments := []Assignment{
		{ID: uuid.New(), UtilityID: "docker", OUIDs: []uuid.UUID{parentOU, ownOU}, Enabled: true},
	}
	set := ResolveForNode(c, assignments, nodeID, []uuid.UUID{ownOU, parentOU})
	if len(set) != 1 {
		t.Fatalf("expected 1 resolved utility, got %d", len(set))
	}
	if set[0].SourceOUID == nil || *set[0].SourceOUID != ownOU {
		t.Fatalf("provenance must name the winning (nearest) unit")
	}
}

// A depends_on is a property of the utility that declares it: a unit with
// only kiosk assigned resolves docker too, with docker's defaults and the
// kiosk assignment's provenance, and no assignment row of its own.
func TestResolveImpliesDependencies(t *testing.T) {
	c := testCatalog()
	nodeID := uuid.New()
	ownOU := uuid.New()
	kioskRow := uuid.New()
	set := ResolveForNode(c, []Assignment{
		{ID: kioskRow, UtilityID: "kiosk", OUIDs: []uuid.UUID{ownOU}, Enabled: true},
	}, nodeID, []uuid.UUID{ownOU})
	if len(set) != 2 || set[0].Definition.ID != "docker" || set[1].Definition.ID != "kiosk" {
		t.Fatalf("expected docker implied before kiosk, got %+v", set)
	}
	docker := set[0]
	if len(docker.ImpliedBy) != 1 || docker.ImpliedBy[0] != "kiosk" {
		t.Fatalf("docker must record what implied it, got %v", docker.ImpliedBy)
	}
	if docker.AssignmentID != uuid.Nil {
		t.Fatal("an implied utility has no assignment row")
	}
	if docker.Source != ScopeOU || docker.SourceOUID == nil || *docker.SourceOUID != ownOU {
		t.Fatalf("an implied utility carries the dependant's provenance, got %s/%v", docker.Source, docker.SourceOUID)
	}
	if docker.Config["log_max_size"] != "10m" || docker.Config["channel"] != "distro" {
		t.Fatalf("an implied utility carries its schema defaults, got %v", docker.Config)
	}
	if docker.ConfigHash != HashConfig("docker", docker.Config) {
		t.Fatal("an implied utility hashes like an assigned one")
	}
	if len(set[1].ImpliedBy) != 0 || set[1].AssignmentID != kioskRow {
		t.Fatalf("the dependant itself is not implied: %+v", set[1])
	}
	if err := CheckSetConstraints(set); err != nil {
		t.Fatalf("an implied set satisfies its own constraints: %v", err)
	}

	// An explicit docker assignment wins over the implication: its config
	// applies and nothing is marked implied.
	set = ResolveForNode(c, []Assignment{
		{ID: kioskRow, UtilityID: "kiosk", OUIDs: []uuid.UUID{ownOU}, Enabled: true},
		{ID: uuid.New(), UtilityID: "docker", AccountWide: true, Enabled: true,
			Config: map[string]interface{}{"log_max_size": "50m"}},
	}, nodeID, []uuid.UUID{ownOU})
	if len(set) != 2 || len(set[0].ImpliedBy) != 0 || set[0].Config["log_max_size"] != "50m" || set[0].Source != ScopeAccount {
		t.Fatalf("an assigned dependency must not be replaced by the implied one: %+v", set[0])
	}

	// Unassigning the dependant takes the implied member with it.
	if set := ResolveForNode(c, nil, nodeID, []uuid.UUID{ownOU}); len(set) != 0 {
		t.Fatalf("nothing assigned resolves nothing, got %+v", set)
	}
}

// A number outside its declared range is refused at save. The agent clamps
// it, so a saved listen port of 80 read back as 80 while the device listened
// on the lower bound, with no error anywhere.
func TestValidateRangesRefusesOutOfBoundNumbers(t *testing.T) {
	lo, hi := 1024.0, 65535.0
	def := Definition{ID: "exporter", Params: []workflow_block.ParamSpec{
		{Name: "listen_port", Type: workflow_block.ParamInt, Min: &lo, Max: &hi},
	}}
	for _, v := range []float64{80, 70000} {
		if err := def.ValidateRanges(map[string]interface{}{"listen_port": v}); !errors.Is(err, ErrInvalidConfig) {
			t.Errorf("listen_port=%v: err = %v, want ErrInvalidConfig", v, err)
		}
	}
	if err := def.ValidateRanges(map[string]interface{}{"listen_port": 9100.0}); err != nil {
		t.Errorf("an in-range port was refused: %v", err)
	}
	if err := def.ValidateRanges(map[string]interface{}{}); err != nil {
		t.Errorf("an absent param was refused: %v", err)
	}
}
