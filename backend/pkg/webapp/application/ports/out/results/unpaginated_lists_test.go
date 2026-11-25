package results

import (
	"encoding/json"
	"testing"
)

// Three list envelopes carried `limit` and `offset`, set to (len(items), 0) at
// their only call site. A client reading them as a page size and an offset
// pages forever over the same whole list while believing it is paginating —
// which is worse than a list that says it has no paging.
//
// This pins their absence. Re-adding either field means implementing the thing
// it promises, and the shape to implement is the page/per_page/total envelope
// the audit, execution and secret lists already use — not a second convention.
func TestUnpaginatedListsPublishNoPagingFields(t *testing.T) {
	envelopes := map[string]interface{}{
		"ssh keys": NewSSHKeyListResult([]SSHKeyResult{{}}),
		"nodes":    NewNodeListResult([]NodeResult{{}}),
		"images":   NewImageListResult([]ImageResult{{}}),
	}
	for what, envelope := range envelopes {
		raw, err := json.Marshal(envelope)
		if err != nil {
			t.Fatalf("%s: %v", what, err)
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(raw, &fields); err != nil {
			t.Fatalf("%s: %v", what, err)
		}
		for _, field := range []string{"limit", "offset", "page", "per_page"} {
			if _, present := fields[field]; present {
				t.Errorf("the %s list publishes %q while returning everything; either paginate for real "+
					"(page/per_page/total, as the audit and execution lists do) or publish nothing", what, field)
			}
		}
		if _, present := fields["count"]; !present {
			t.Errorf("the %s list publishes no count; an empty list and an absent one are otherwise the same response", what)
		}
	}
}
