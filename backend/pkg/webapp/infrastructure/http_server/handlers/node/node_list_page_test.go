package node

import (
	"testing"
	"time"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/out/results"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
)

func strPtr(s string) *string { return &s }

func sampleNodes() []results.NodeResult {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	seen := int64(100)
	return []results.NodeResult{
		{Name: "beta", Status: "online", IPv4: strPtr("192.0.2.11"), CreatedAt: t0.Add(2 * time.Hour)},
		{Name: "", Hostname: strPtr("alpha-host"), Status: "offline", CreatedAt: t0.Add(time.Hour), LastSeenAt: &seen},
		{Name: "Gamma", Status: "online", Fingerprint: &node.HardwareFingerprint{HardwareSerial: "SERIAL42"}, CreatedAt: t0},
	}
}

func namesOf(nodes []results.NodeResult) []string {
	out := make([]string, 0, len(nodes))
	for i := range nodes {
		out = append(out, displayName(&nodes[i]))
	}
	return out
}

// The default order must match what the frontend has always shown: by display
// name (name, falling back to hostname), case-insensitively — so switching the
// fetch to pages cannot reshuffle the list an operator is looking at.
func TestSortNodeResultsDefaultIsDisplayName(t *testing.T) {
	nodes := sampleNodes()
	sortNodeResults(nodes, "", "")
	got := namesOf(nodes)
	want := []string{"alpha-host", "beta", "Gamma"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order = %v, want %v", got, want)
		}
	}
}

func TestSortNodeResultsDescReverses(t *testing.T) {
	nodes := sampleNodes()
	sortNodeResults(nodes, "", "desc")
	if got := namesOf(nodes); got[0] != "Gamma" {
		t.Fatalf("desc order = %v, want Gamma first", got)
	}
}

func TestSortNodeResultsByCreatedAt(t *testing.T) {
	nodes := sampleNodes()
	sortNodeResults(nodes, "created_at", "asc")
	if got := namesOf(nodes); got[0] != "Gamma" || got[2] != "beta" {
		t.Fatalf("created_at order = %v", got)
	}
}

// A nil last_seen_at sorts as zero — never seen sorts before every node that
// has been — rather than panicking or interleaving arbitrarily.
func TestSortNodeResultsByLastSeenHandlesNil(t *testing.T) {
	nodes := sampleNodes()
	sortNodeResults(nodes, "last_seen_at", "desc")
	if got := namesOf(nodes); got[0] != "alpha-host" {
		t.Fatalf("last_seen desc = %v, want alpha-host first", got)
	}
}

func TestMatchesSearchCoversPastedIdentifiers(t *testing.T) {
	nodes := sampleNodes()
	cases := []struct {
		search string
		want   string
	}{
		{"BETA", "beta"},           // name, case-insensitive
		{"alpha", "alpha-host"},    // hostname
		{"192.0.2.11", "beta"},   // IPv4
		{"serial42", "Gamma"},      // hardware serial
	}
	for _, tc := range cases {
		var hits []string
		for i := range nodes {
			if matchesSearch(&nodes[i], tc.search) {
				hits = append(hits, displayName(&nodes[i]))
			}
		}
		if len(hits) != 1 || hits[0] != tc.want {
			t.Errorf("search %q hit %v, want exactly [%s]", tc.search, hits, tc.want)
		}
	}
}

func TestPageSliceBounds(t *testing.T) {
	nodes := sampleNodes()
	if got := pageSlice(nodes, 1, 2); len(got) != 2 {
		t.Fatalf("page 1 size = %d, want 2", len(got))
	}
	if got := pageSlice(nodes, 2, 2); len(got) != 1 {
		t.Fatalf("page 2 size = %d, want 1", len(got))
	}
	// A page past the end is an empty page, not an error: total tells the
	// caller where the end is.
	if got := pageSlice(nodes, 9, 2); len(got) != 0 {
		t.Fatalf("page past end = %d items, want 0", len(got))
	}
}

func TestNodeListParamsValidate(t *testing.T) {
	p := nodeListParams{Sort: "cpu"}
	if err := p.validate(); err == nil {
		t.Fatal("unknown sort must refuse")
	}
	p = nodeListParams{Order: "sideways"}
	if err := p.validate(); err == nil {
		t.Fatal("unknown order must refuse")
	}
	// Out-of-range paging is clamped, not refused: the params are UI state.
	p = nodeListParams{Page: -3, PerPage: 100000, paginate: true}
	if err := p.validate(); err != nil {
		t.Fatalf("clamped params must pass: %v", err)
	}
	if p.Page != 1 || p.PerPage != nodeListMaxPerPage {
		t.Fatalf("clamp = page %d per_page %d", p.Page, p.PerPage)
	}
}
