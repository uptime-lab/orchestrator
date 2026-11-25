package node

import (
	"fmt"
	"sort"
	"strings"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/out/results"
)

// nodeListParams are GET /nodes' optional list controls. Filtering, sorting
// and paging run in the HANDLER, after the authz visibility filter, over the
// account's own rows — bounded by NODE_MAX_PER_ACCOUNT, so the in-memory pass
// costs one bounded query, exactly what the unpaginated endpoint already paid.
// Push this into SQL only when the per-account cap rises past what a single
// query should carry; pushing it down today would mean re-implementing the
// authz OU filter in every repository method that touches nodes.
type nodeListParams struct {
	Search  string
	Sort    string
	Order   string
	Page    int
	PerPage int
	// paginate records whether the caller asked for a page at all: without
	// page/per_page the response keeps the legacy full-list envelope, so
	// existing consumers (and the WS-fed frontend store) keep working.
	paginate bool
}

const (
	nodeListDefaultPerPage = 50
	nodeListMaxPerPage     = 200
)

func (p *nodeListParams) validate() error {
	switch p.Sort {
	case "", "name", "status", "last_seen_at", "created_at":
	default:
		return fmt.Errorf("invalid sort: must be name, status, last_seen_at or created_at")
	}
	switch p.Order {
	case "", "asc", "desc":
	default:
		return fmt.Errorf("invalid order: must be asc or desc")
	}
	if p.paginate {
		if p.Page < 1 {
			p.Page = 1
		}
		if p.PerPage < 1 {
			p.PerPage = nodeListDefaultPerPage
		}
		if p.PerPage > nodeListMaxPerPage {
			p.PerPage = nodeListMaxPerPage
		}
	}
	return nil
}

// matchesSearch is a case-insensitive substring match over the fields an
// operator pastes when hunting a box: name, hostname, IPv4, MAC and the
// hardware serial.
func matchesSearch(n *results.NodeResult, search string) bool {
	if search == "" {
		return true
	}
	needle := strings.ToLower(search)
	candidates := []string{n.Name, n.Status}
	if n.Hostname != nil {
		candidates = append(candidates, *n.Hostname)
	}
	if n.IPv4 != nil {
		candidates = append(candidates, *n.IPv4)
	}
	if n.MacAddress != nil {
		candidates = append(candidates, *n.MacAddress)
	}
	if n.Fingerprint != nil {
		candidates = append(candidates, n.Fingerprint.HardwareSerial)
	}
	for _, c := range candidates {
		if strings.Contains(strings.ToLower(c), needle) {
			return true
		}
	}
	return false
}

// displayName mirrors the frontend's sort key: a node with no name sorts by
// its hostname rather than clumping at the top as "".
func displayName(n *results.NodeResult) string {
	if n.Name != "" {
		return n.Name
	}
	if n.Hostname != nil {
		return *n.Hostname
	}
	return ""
}

func sortNodeResults(nodes []results.NodeResult, sortKey, order string) {
	desc := order == "desc"
	less := func(i, j int) bool {
		a, b := &nodes[i], &nodes[j]
		switch sortKey {
		case "status":
			if a.Status != b.Status {
				return a.Status < b.Status
			}
		case "last_seen_at":
			av, bv := int64(0), int64(0)
			if a.LastSeenAt != nil {
				av = *a.LastSeenAt
			}
			if b.LastSeenAt != nil {
				bv = *b.LastSeenAt
			}
			if av != bv {
				return av < bv
			}
		case "created_at":
			if !a.CreatedAt.Equal(b.CreatedAt) {
				return a.CreatedAt.Before(b.CreatedAt)
			}
		}
		// Name is both the default sort and every other key's tie-break, so
		// the order is total and stable across refetches.
		return strings.ToLower(displayName(a)) < strings.ToLower(displayName(b))
	}
	sort.SliceStable(nodes, func(i, j int) bool {
		if desc {
			return less(j, i)
		}
		return less(i, j)
	})
}

// pageSlice returns the requested page. A page past the end is an empty page,
// not an error: the caller's total tells it where the end is.
func pageSlice(nodes []results.NodeResult, page, perPage int) []results.NodeResult {
	start := (page - 1) * perPage
	if start >= len(nodes) {
		return []results.NodeResult{}
	}
	end := start + perPage
	if end > len(nodes) {
		end = len(nodes)
	}
	return nodes[start:end]
}
