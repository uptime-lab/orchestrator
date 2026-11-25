// Package utility models OU-assignable software roles (docs/UTILITIES-PLAN.md):
// a Utility is a condition a node HOLDS — installed and configured on every
// eligible node an assignment reaches — where a playbook is a sequence an
// operator RUNS. The catalog of what exists is embedded data loaded by
// infrastructure/utility_registry (the device-profile registry pattern); this
// package holds the typed schema, assignment scoping, per-node resolution and
// the per-utility config hashing the apply/settle ladder is built on.
//
// The agent implements each utility id natively — the registry never ships
// shell (the SetupStep doctrine from device profiles: a data file that
// dispatches root scripts bypasses every domain bound).
package utility

import (
	"errors"
	"fmt"
	"regexp"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/workflow_block"
)

// Sentinel errors, mapped to 4xx by the HTTP layer via errors.Is.
var (
	// ErrUnknownUtility: an assignment references a utility id not in the catalog.
	ErrUnknownUtility = errors.New("unknown utility")
	// ErrScopeConflict: an assignment names more than one target (the scope is
	// account-wide XOR one OU XOR one node XOR unassigned).
	ErrScopeConflict = errors.New("A utility assignment must target exactly one scope: account-wide, organizational units, or a single node.")
	// ErrDuplicateAssignment: the (utility, target) pair already has a row —
	// resolution must never have ties, so at most one assignment of a utility
	// may exist per target.
	ErrDuplicateAssignment = errors.New("this utility is already assigned to this target")
	// ErrDependencyMissing: the utility declares depends_on and the dependency
	// is not part of the node's resolved set.
	ErrDependencyMissing = errors.New("utility dependency is not assigned")
	// ErrInvalidConfig wraps every param-schema refusal so the HTTP layer can
	// map the whole family to 400 with one errors.Is.
	ErrInvalidConfig = errors.New("invalid utility config")
)

// Class names the consent model. Exactly one exists today: "consent_gated"
// was deleted — no catalog entry ever used it and no gating flow
// was ever built, so the constant only pretended a second model existed. If a
// utility ever needs per-node consent for disruptive transitions, the class
// (and the parking/consent flow docs/UTILITIES-PLAN.md §1 argues for) must be
// built together, not re-declared alone.
type Class string

const (
	// ClassReconciled utilities get full automation: fan-out on config change,
	// drift self-heal, first-connect apply, reimage re-apply.
	ClassReconciled Class = "reconciled"
)

// Definition describes one utility in the catalog.
type Definition struct {
	// ID is the stable API identifier ("docker"). The agent keys its native
	// applier on it and refuses ids it does not know.
	ID string
	// Name is the display label ("Docker").
	Name string
	// Description explains what the utility installs, shown in the picker. It
	// carries the operator-relevant consequences (e.g. dockerd's iptables
	// management publishes container ports through host firewalls).
	Description string
	// Details is the optional expanded help behind the description — the
	// tooltip/details tier the UI shows on demand. Empty means the description
	// stands alone.
	Details string
	// Class picks the consent model.
	Class Class
	// DependsOn lists utility ids that must be part of the same resolved set
	// (kiosk depends on docker).
	DependsOn []string
	// ConflictsWith lists utility ids that must NOT be part of the same set.
	ConflictsWith []string
	// RequiresReboot marks a utility whose apply ends in a reboot the operator
	// must consent to. Phase 1 ships none.
	RequiresReboot bool
	// Params is the config schema, in the workflow-block shape on purpose: it
	// drives the same generated forms (CommandInputs) and the same save-time
	// validation, and the catalog endpoint serves it in the same JSON.
	Params []workflow_block.ParamSpec
	// MinAgentVersion optionally raises the floor above the slug's own (a
	// utility whose applier ships in a later agent release than the handler).
	// Empty means the slug floor decides alone.
	MinAgentVersion string
}

// Catalog is the loaded registry: every utility this build knows. Built once
// at boot, validated, then read-only.
type Catalog struct {
	Utilities []Definition
}

// Get returns the definition by id.
func (c *Catalog) Get(id string) (Definition, bool) {
	for _, d := range c.Utilities {
		if d.ID == id {
			return d, true
		}
	}
	return Definition{}, false
}

// Validate enforces the registry rules. Called from go test (a bad entry
// breaks CI) and fatally at boot (a registry the binary cannot trust must not
// serve traffic) — the device-profile registry contract.
func (c *Catalog) Validate() error {
	seen := map[string]bool{}
	for _, d := range c.Utilities {
		if d.ID == "" || d.Name == "" {
			return fmt.Errorf("utility %q: id and name are required", d.ID)
		}
		if seen[d.ID] {
			return fmt.Errorf("utility %q: duplicate id", d.ID)
		}
		seen[d.ID] = true
		if d.Class != ClassReconciled {
			return fmt.Errorf("utility %q: unknown class %q", d.ID, d.Class)
		}
		for _, p := range d.Params {
			if p.Name == "" {
				return fmt.Errorf("utility %q: a param has no name", d.ID)
			}
			// A broken pattern must fail HERE (CI + boot), not on the first
			// operator save it silently refuses.
			if p.Pattern != "" {
				if _, err := regexp.Compile(p.Pattern); err != nil {
					return fmt.Errorf("utility %q: param %q has an invalid pattern: %v", d.ID, p.Name, err)
				}
			}
		}
	}
	// Dependency/conflict references must exist, and a utility must not
	// conflict with its own dependency chain. A dependency is IMPLIED into the
	// set with its schema defaults alone (implyDependencies), so every
	// required parameter of a depends_on target needs a default — otherwise
	// the implied member fails ValidateConfig at apply on every node, with a
	// remedy ("edit the assignment") that names a row nobody created.
	byID := make(map[string]Definition, len(c.Utilities))
	for _, d := range c.Utilities {
		byID[d.ID] = d
	}
	for _, d := range c.Utilities {
		for _, dep := range d.DependsOn {
			if !seen[dep] {
				return fmt.Errorf("utility %q: depends_on %q which does not exist", d.ID, dep)
			}
			for _, p := range byID[dep].Params {
				if p.Required && p.Default == nil {
					return fmt.Errorf("utility %q: depends_on %q whose required param %q has no default, so it cannot be implied", d.ID, dep, p.Name)
				}
			}
		}
		for _, conflict := range d.ConflictsWith {
			if !seen[conflict] {
				return fmt.Errorf("utility %q: conflicts_with %q which does not exist", d.ID, conflict)
			}
			for _, dep := range d.DependsOn {
				if dep == conflict {
					return fmt.Errorf("utility %q: %q is both a dependency and a conflict", d.ID, dep)
				}
			}
		}
	}
	return nil
}

// ValidateConfig checks an assignment's config against the utility's param
// schema: required present, types right, enums enforced. Unknown keys are
// refused — utility params are registry-bounded (the typed-step doctrine
// extended to param value domains), unlike workflow blocks where extras pass
// through to the agent.
func (d Definition) ValidateConfig(config map[string]interface{}) error {
	known := make(map[string]bool, len(d.Params))
	for _, p := range d.Params {
		known[p.Name] = true
	}
	for k := range config {
		if !known[k] {
			return fmt.Errorf("%w: unknown parameter %q", ErrInvalidConfig, k)
		}
	}
	block := workflow_block.BlockDefinition{Params: d.Params}
	if err := block.ValidateParams(block.ApplyDefaults(config)); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidConfig, err)
	}
	return nil
}

// ValidateRanges refuses a number outside its parameter's Min/Max. The agent
// clamps such a value, so without this a saved port of 80 read back as 80
// while the device listened on the bound. It is a SAVE-time check only:
// ValidateConfig also runs at dispatch, where a stored config that predates
// a bound must keep converging (clamped) rather than drop out.
func (d Definition) ValidateRanges(config map[string]interface{}) error {
	for _, p := range d.Params {
		if p.Min == nil && p.Max == nil {
			continue
		}
		raw, ok := config[p.Name]
		if !ok {
			continue
		}
		var v float64
		switch n := raw.(type) {
		case float64:
			v = n
		case int:
			v = float64(n)
		case int64:
			v = float64(n)
		default:
			continue // a type error is ValidateConfig's to report
		}
		if p.Min != nil && v < *p.Min {
			return fmt.Errorf("%w: %s must be at least %v", ErrInvalidConfig, p.Name, *p.Min)
		}
		if p.Max != nil && v > *p.Max {
			return fmt.Errorf("%w: %s must be at most %v", ErrInvalidConfig, p.Name, *p.Max)
		}
	}
	return nil
}

// ResolvedConfig returns the config with the schema defaults applied — the
// single defaulting site, so a consumption site never re-implements a default
// (the ApplyDefaults doctrine).
func (d Definition) ResolvedConfig(config map[string]interface{}) map[string]interface{} {
	block := workflow_block.BlockDefinition{Params: d.Params}
	return block.ApplyDefaults(config)
}
