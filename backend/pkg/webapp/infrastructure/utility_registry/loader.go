// Package utility_registry loads the embedded YAML catalog of utilities into
// the pure domain catalog — the device_profile_registry pattern, for the same
// reasons: the registry is DATA shipped inside the binary (its content becomes
// root-level software on live devices, so a new utility goes through git
// review and a backend deploy), the YAML decode lives here because domain
// carries no external deps, and Load is called both from go test (a bad entry
// breaks CI) and fatally from cmd/server/main.go at boot.
package utility_registry

import (
	"bytes"
	"embed"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/utility"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/workflow_block"
)

//go:embed registry/utilities/*.yaml
var registryFS embed.FS

// YAML DTOs, strict-decoded: a typo'd key refuses the entry instead of
// silently dropping its intent.

type utilityYAML struct {
	ID              string      `yaml:"id"`
	Name            string      `yaml:"name"`
	Description     string      `yaml:"description"`
	Details         string      `yaml:"details"`
	Class           string      `yaml:"class"`
	DependsOn       []string    `yaml:"depends_on"`
	ConflictsWith   []string    `yaml:"conflicts_with"`
	RequiresReboot  bool        `yaml:"requires_reboot"`
	MinAgentVersion string      `yaml:"min_agent_version"`
	Params          []paramYAML `yaml:"params"`
}

type paramYAML struct {
	Name        string      `yaml:"name"`
	Label       string      `yaml:"label"`
	Description string      `yaml:"description"`
	Details     string      `yaml:"details"`
	Type        string      `yaml:"type"`
	Required    bool        `yaml:"required"`
	Default     interface{} `yaml:"default"`
	EnumValues  []string    `yaml:"enum_values"`
	Placeholder string      `yaml:"placeholder"`
	Min         *float64    `yaml:"min"`
	Max         *float64    `yaml:"max"`
	Pattern     string      `yaml:"pattern"`
	PatternHint string      `yaml:"pattern_hint"`
}

// Load parses and validates the embedded registry. The returned catalog is
// the process-wide read-only source of truth.
func Load() (*utility.Catalog, error) {
	catalog := &utility.Catalog{}
	if err := eachFile("registry/utilities", func(name string, data []byte) error {
		var u utilityYAML
		if err := strictDecode(data, &u); err != nil {
			return err
		}
		catalog.Utilities = append(catalog.Utilities, toDefinition(u))
		return nil
	}); err != nil {
		return nil, err
	}
	if err := catalog.Validate(); err != nil {
		return nil, fmt.Errorf("utility registry: %w", err)
	}
	return catalog, nil
}

// eachFile walks the embedded directory in sorted order so the catalog is
// stable across builds.
func eachFile(dir string, fn func(name string, data []byte) error) error {
	entries, err := fs.ReadDir(registryFS, dir)
	if err != nil {
		return fmt.Errorf("utility registry: %w", err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".yaml") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	for _, name := range names {
		data, err := registryFS.ReadFile(dir + "/" + name)
		if err != nil {
			return fmt.Errorf("utility registry: %s: %w", name, err)
		}
		if err := fn(name, data); err != nil {
			return fmt.Errorf("utility registry: %s: %w", name, err)
		}
	}
	return nil
}

// strictDecode refuses unknown keys and a second YAML document per file (the
// device-profile loader's rules, for its reasons).
func strictDecode(data []byte, out any) error {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(out); err != nil {
		return err
	}
	if err := dec.Decode(new(any)); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("more than one YAML document in file — one entry per file")
		}
		return err
	}
	return nil
}

func toDefinition(u utilityYAML) utility.Definition {
	def := utility.Definition{
		ID:              u.ID,
		Name:            u.Name,
		Description:     u.Description,
		Details:         u.Details,
		Class:           utility.Class(u.Class),
		DependsOn:       u.DependsOn,
		ConflictsWith:   u.ConflictsWith,
		RequiresReboot:  u.RequiresReboot,
		MinAgentVersion: u.MinAgentVersion,
	}
	for _, p := range u.Params {
		def.Params = append(def.Params, workflow_block.ParamSpec{
			Name:        p.Name,
			Label:       p.Label,
			Description: p.Description,
			Details:     p.Details,
			Type:        workflow_block.ParamType(p.Type),
			Required:    p.Required,
			Default:     p.Default,
			EnumValues:  p.EnumValues,
			Placeholder: p.Placeholder,
			Min:         p.Min,
			Max:         p.Max,
			Pattern:     p.Pattern,
			PatternHint: p.PatternHint,
		})
	}
	return def
}
