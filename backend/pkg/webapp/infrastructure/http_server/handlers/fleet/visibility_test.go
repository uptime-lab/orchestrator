package fleet

import (
	"testing"

	"github.com/google/uuid"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
)

// The authz narrowing both Status routes share. A node with no unit reaches
// only an account-wide reader — the same line the node list draws, and the one
// an OU-scoped member's whole view depends on.
func TestVisible(t *testing.T) {
	mine, theirs := uuid.New(), uuid.New()
	allowed := map[uuid.UUID]bool{mine: true}
	cases := []struct {
		name     string
		ou       *uuid.UUID
		allowAll bool
		want     bool
	}{
		{name: "account-wide reader sees an unassigned node", ou: nil, allowAll: true, want: true},
		{name: "account-wide reader sees any unit", ou: &theirs, allowAll: true, want: true},
		{name: "scoped reader sees their own unit", ou: &mine, want: true},
		{name: "scoped reader does not see another unit", ou: &theirs},
		{name: "scoped reader does not see an unassigned node", ou: nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := visible(&node.Node{OUID: tc.ou}, tc.allowAll, allowed); got != tc.want {
				t.Errorf("visible = %v, want %v", got, tc.want)
			}
		})
	}
}

// The operator's own ?ou_id= filter, applied on top of the authz one and never
// instead of it. "unassigned" is a literal the node list already accepts.
func TestMatchesOU(t *testing.T) {
	a, b := uuid.New(), uuid.New()
	cases := []struct {
		name       string
		ou         *uuid.UUID
		filter     *uuid.UUID
		unassigned bool
		want       bool
	}{
		{name: "no filter matches everything", ou: &a, want: true},
		{name: "no filter matches an unassigned node", ou: nil, want: true},
		{name: "unit filter matches its unit", ou: &a, filter: &a, want: true},
		{name: "unit filter excludes another unit", ou: &b, filter: &a},
		{name: "unit filter excludes an unassigned node", ou: nil, filter: &a},
		{name: "unassigned filter matches an unassigned node", ou: nil, unassigned: true, want: true},
		{name: "unassigned filter excludes an assigned node", ou: &a, unassigned: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := matchesOU(&node.Node{OUID: tc.ou}, tc.filter, tc.unassigned); got != tc.want {
				t.Errorf("matchesOU = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestParseOUFilter(t *testing.T) {
	id := uuid.New()
	if ou, un, err := parseOUFilter(""); ou != nil || un || err != nil {
		t.Errorf(`parseOUFilter("") = %v/%v/%v, want no narrowing`, ou, un, err)
	}
	if ou, un, err := parseOUFilter("unassigned"); ou != nil || !un || err != nil {
		t.Errorf(`parseOUFilter("unassigned") = %v/%v/%v`, ou, un, err)
	}
	if ou, un, err := parseOUFilter(id.String()); ou == nil || *ou != id || un || err != nil {
		t.Errorf("parseOUFilter(id) = %v/%v/%v", ou, un, err)
	}
	// A typo must be refused, not silently widened to the whole account.
	if _, _, err := parseOUFilter("not-a-unit"); err == nil {
		t.Error("parseOUFilter(garbage) error = nil, want a refusal")
	}
}
