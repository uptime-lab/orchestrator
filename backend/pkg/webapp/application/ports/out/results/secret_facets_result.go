package results

import (
	"sort"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/secret"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/secret_tag"
)

// SecretTagCountResult is one tag of the account's vocabulary with its usage,
// its color (explicit override or deterministic default) and — when the tag is
// attached somewhere — its delivery target counts.
type SecretTagCountResult struct {
	Tag       string `json:"tag"`
	Color     string `json:"color"`
	Count     int    `json:"count"`
	OUCount   int    `json:"ou_count"`
	NodeCount int    `json:"node_count"`
}

// SecretTagListResult is the tag facet for the secrets filter rail.
type SecretTagListResult struct {
	Tags  []SecretTagCountResult `json:"tags"`
	Count int                    `json:"count"`
}

// NewSecretTagListResult maps tag usage + attachment target counts + color
// overrides to the DTO, sorted by tag name. Every tag gets a color: its
// explicit override when present, else the deterministic default for its name.
// A tag appears if it is carried by a secret, attached somewhere, OR has an
// explicit color row (a freshly created but not-yet-used tag).
func NewSecretTagListResult(counts map[string]int, attachments map[string]secret.AttachmentCounts, colors map[string]string) SecretTagListResult {
	items := make([]SecretTagCountResult, 0, len(counts))
	seen := make(map[string]bool, len(counts))

	color := func(tag string) string {
		if c, ok := colors[tag]; ok {
			return c
		}
		return secret_tag.DefaultColorFor(tag)
	}

	for tag, count := range counts {
		seen[tag] = true
		entry := SecretTagCountResult{Tag: tag, Color: color(tag), Count: count}
		if a, ok := attachments[tag]; ok {
			entry.OUCount = a.OUs
			entry.NodeCount = a.Nodes
		}
		items = append(items, entry)
	}
	for tag, a := range attachments {
		if !seen[tag] {
			seen[tag] = true
			items = append(items, SecretTagCountResult{Tag: tag, Color: color(tag), OUCount: a.OUs, NodeCount: a.Nodes})
		}
	}
	for tag := range colors {
		if !seen[tag] {
			seen[tag] = true
			items = append(items, SecretTagCountResult{Tag: tag, Color: color(tag)})
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Tag < items[j].Tag })
	return SecretTagListResult{Tags: items, Count: len(items)}
}

// SecretTagResult is a single tag color row (the Tags-tab entity).
type SecretTagResult struct {
	Name  string `json:"name"`
	Color string `json:"color"`
}

// NewSecretTagResult maps a domain tag to the DTO.
func NewSecretTagResult(tag secret_tag.Tag) SecretTagResult {
	return SecretTagResult{Name: tag.Name, Color: tag.Color}
}

// SecretOUCountResult is one OU with its delivered-secret count.
type SecretOUCountResult struct {
	OUID  uuid.UUID `json:"ou_id"`
	Count int       `json:"count"`
}

// SecretOUCountsResult is the OU facet for the secrets filter tree.
type SecretOUCountsResult struct {
	OUs        []SecretOUCountResult `json:"ous"`
	Unattached int64                 `json:"unattached"`
	Total      int64                 `json:"total"`
}

// NewSecretOUCountsResult maps per-OU delivery counts to the DTO.
func NewSecretOUCountsResult(counts map[uuid.UUID]int, unattached, total int64) SecretOUCountsResult {
	items := make([]SecretOUCountResult, 0, len(counts))
	for ouID, count := range counts {
		items = append(items, SecretOUCountResult{OUID: ouID, Count: count})
	}
	sort.Slice(items, func(i, j int) bool {
		return items[i].OUID.String() < items[j].OUID.String()
	})
	return SecretOUCountsResult{OUs: items, Unattached: unattached, Total: total}
}
