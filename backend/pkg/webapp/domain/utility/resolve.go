package utility

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/google/uuid"
)

// Resolved is one utility of a node's effective set: the winning assignment,
// its defaulted config and the per-utility hash the apply/settle ladder keys
// on.
type Resolved struct {
	Definition   Definition
	AssignmentID uuid.UUID
	// Source names the level the winning assignment sits on ("node", "ou",
	// "account") — provenance for the effective view.
	Source Scope
	// SourceOUID is set when Source is "ou" (which unit won).
	SourceOUID *uuid.UUID
	// Config has the schema defaults applied.
	Config map[string]interface{}
	// ConfigHash is the per-utility desired hash (see HashConfig).
	ConfigHash string
	// ParamsSecretDerived reports that Config holds values substituted from a
	// {{secrets.*}} / {{node.attributes.*}} reference. It is set only by the
	// per-node resolution (resolveParamRefs), never stored, and it exists so the
	// dispatch can mark the payload for redaction — a resolved value is
	// indistinguishable from a literal one once it is in the map.
	ParamsSecretDerived bool
	// ImpliedBy lists the utilities whose depends_on brought this one into the
	// set without an assignment of its own (implyDependencies). Empty for a
	// utility that resolves from an assignment; AssignmentID is nil when set.
	ImpliedBy []string
}

// ResolveForNode picks, per utility id, the NEAREST assignment: node > the
// node's own OU > its parent OU > account-wide. The winner brings its WHOLE
// config block — half a declaration is not a meaningful state, so levels are
// never field-merged (the device-profile one-block rule). Disabled and
// unassigned rows never apply. Utilities whose id is not in the catalog are
// skipped (a registry that no longer ships an id must not fail every node
// holding an old row); the caller logs them.
//
// ouChain is the node's OU lineage nearest-first (own OU, then parent) — the
// 2-level tree makes it at most two entries. The result is sorted by utility
// id so the union hash and the dispatch payload are deterministic.
func ResolveForNode(catalog *Catalog, assignments []Assignment, nodeID uuid.UUID, ouChain []uuid.UUID) []Resolved {
	type candidate struct {
		a        Assignment
		distance int // 0 = node, 1..len(ouChain) = OU levels, big = account
		// wonOU is the chain unit that matched (provenance for the effective
		// view) — with a SET scope "which OU won" is a per-node fact, not a
		// column on the assignment.
		wonOU *uuid.UUID
	}
	best := map[string]candidate{}
	accountDistance := len(ouChain) + 1
	for _, a := range assignments {
		if !a.Enabled {
			continue
		}
		var distance int
		var wonOU *uuid.UUID
		switch a.Scope() {
		case ScopeNode:
			if *a.NodeID != nodeID {
				continue
			}
			distance = 0
		case ScopeOU:
			// The nearest chain position ANY unit of the set matches: an
			// assignment spanning the node's own OU and its parent counts as
			// the own-OU level (a set does not shadow itself — the
			// access-entry EntryID lesson).
			distance = -1
			for i, ou := range ouChain {
				for _, target := range a.OUIDs {
					if target == ou {
						distance = i + 1
						matched := ou
						wonOU = &matched
						break
					}
				}
				if distance >= 0 {
					break
				}
			}
			if distance < 0 {
				continue
			}
		case ScopeAccount:
			distance = accountDistance
		default: // unassigned applies to nothing
			continue
		}
		if cur, ok := best[a.UtilityID]; !ok || distance < cur.distance {
			best[a.UtilityID] = candidate{a: a, distance: distance, wonOU: wonOU}
		}
	}

	out := make([]Resolved, 0, len(best))
	for id, c := range best {
		def, ok := catalog.Get(id)
		if !ok {
			continue
		}
		config := def.ResolvedConfig(c.a.Config)
		r := Resolved{
			Definition:   def,
			AssignmentID: c.a.ID,
			Source:       c.a.Scope(),
			Config:       config,
			ConfigHash:   HashConfig(id, config),
		}
		if c.a.Scope() == ScopeOU {
			r.SourceOUID = c.wonOU
		}
		out = append(out, r)
	}
	out = implyDependencies(catalog, out)
	sort.Slice(out, func(i, j int) bool { return out[i].Definition.ID < out[j].Definition.ID })
	return out
}

// implyDependencies adds every depends_on the resolved set does not already
// carry, with the dependency's schema defaults and the DEPENDANT's provenance.
//
// A dependency is a property of the utility that declares it, not a second
// thing the operator has to remember: a unit with only kiosk assigned used to
// be refused at apply, per node, with "kiosk needs docker" and
// nothing in the UI pointing at the missing row. Implied at resolution rather
// than written as a second assignment row on purpose — a row outlives the
// dependant's unassignment and can be edited into another scope, while an
// implied member simply leaves the set when its dependant does and, like every
// unassign, dispatches nothing (the software stays installed). An explicit
// assignment of the dependency still wins: it is present already, so nothing
// is implied and its own config applies.
//
// Provenance is the dependant's (Source, SourceOUID) so the effective view can
// still say which level put it there. Runs to a fixpoint so a dependency's own
// depends_on is honoured; a cycle cannot reach here (Catalog.Validate refuses
// one), and Catalog.Validate also guarantees an implied utility is complete
// with defaults alone.
func implyDependencies(catalog *Catalog, set []Resolved) []Resolved {
	present := make(map[string]int, len(set))
	for i, r := range set {
		present[r.Definition.ID] = i
	}
	for i := 0; i < len(set); i++ {
		dependant := set[i]
		for _, dep := range dependant.Definition.DependsOn {
			if j, ok := present[dep]; ok {
				if len(set[j].ImpliedBy) > 0 {
					set[j].ImpliedBy = append(set[j].ImpliedBy, dependant.Definition.ID)
				}
				continue
			}
			def, ok := catalog.Get(dep)
			if !ok {
				continue // CheckSetConstraints names it
			}
			config := def.ResolvedConfig(map[string]interface{}{})
			set = append(set, Resolved{
				Definition: def,
				Source:     dependant.Source,
				SourceOUID: dependant.SourceOUID,
				Config:     config,
				ConfigHash: HashConfig(dep, config),
				ImpliedBy:  []string{dependant.Definition.ID},
			})
			present[dep] = len(set) - 1
		}
	}
	return set
}

// SortByDependencies orders a resolved set so every utility's depends_on
// precede it (stable within a rank: id order). Today's set happens to sort
// correctly alphabetically (docker < kiosk) — this makes it a property
// instead of luck. The order is computed at PAYLOAD BUILD on the platform:
// the wire carries no dependency graph, and the agent applies in payload
// order, so this is the one place the ordering can live without a proto
// change. Cycles cannot reach here (Catalog.Validate refuses a dependency
// that is also a conflict, and CheckSetConstraints runs first); a cycle
// would fall back to id order rather than loop.
func SortByDependencies(set []Resolved) []Resolved {
	rank := map[string]int{}
	var rankOf func(id string, depth int) int
	byID := map[string]Resolved{}
	for _, r := range set {
		byID[r.Definition.ID] = r
	}
	rankOf = func(id string, depth int) int {
		if r, ok := rank[id]; ok {
			return r
		}
		if depth > len(set) { // cycle guard: never loop, fall back flat
			return 0
		}
		max := 0
		for _, dep := range byID[id].Definition.DependsOn {
			if _, present := byID[dep]; !present {
				continue
			}
			if r := rankOf(dep, depth+1) + 1; r > max {
				max = r
			}
		}
		rank[id] = max
		return max
	}
	out := append([]Resolved(nil), set...)
	sort.SliceStable(out, func(i, j int) bool {
		ri, rj := rankOf(out[i].Definition.ID, 0), rankOf(out[j].Definition.ID, 0)
		if ri != rj {
			return ri < rj
		}
		return out[i].Definition.ID < out[j].Definition.ID
	})
	return out
}

// CheckSetConstraints verifies depends_on / conflicts_with over a RESOLVED
// set. Run at apply time, per node — an OU assignment legitimately coexists
// with a node-level dependency, so the constraint is only checkable against
// what a specific node actually resolves to (the device-profile fits-check
// reasoning). Since implyDependencies a missing depends_on can only mean a
// catalog id that no longer exists; the check stays as the safety net.
func CheckSetConstraints(set []Resolved) error {
	present := make(map[string]bool, len(set))
	for _, r := range set {
		present[r.Definition.ID] = true
	}
	for _, r := range set {
		for _, dep := range r.Definition.DependsOn {
			if !present[dep] {
				return fmt.Errorf("%w: %s needs %s", ErrDependencyMissing, r.Definition.ID, dep)
			}
		}
		for _, conflict := range r.Definition.ConflictsWith {
			if present[conflict] {
				return fmt.Errorf("%s conflicts with %s in the same set", r.Definition.ID, conflict)
			}
		}
	}
	return nil
}

// HashConfig computes the per-utility desired hash: sha256 over the utility id
// and the canonical JSON of its defaulted config (encoding/json sorts map
// keys, which is what makes the encoding canonical). This is the value the
// dispatch payload carries per utility and the settle records verbatim from
// its OWN copy — never from the agent's echo (the access-generation rule).
func HashConfig(utilityID string, config map[string]interface{}) string {
	raw, err := json.Marshal(config)
	if err != nil {
		// A config that cannot marshal cannot have been stored; hash the
		// failure deterministically rather than panic.
		raw = []byte("unmarshalable")
	}
	sum := sha256.Sum256(append([]byte(utilityID+"\n"), raw...))
	return hex.EncodeToString(sum[:])
}

// UnionHash identifies a whole resolved set — the dispatch/supersede identity.
// Per-utility convergence is tracked by the per-utility hashes; the union
// exists so a newer queued set retires an older unsent one.
func UnionHash(set []Resolved) string {
	lines := make([]string, 0, len(set))
	for _, r := range set {
		lines = append(lines, r.Definition.ID+":"+r.ConfigHash)
	}
	sort.Strings(lines)
	h := sha256.New()
	for _, l := range lines {
		h.Write([]byte(l))
		h.Write([]byte("\n"))
	}
	return hex.EncodeToString(h.Sum(nil))
}
