package access

import (
	"testing"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/access_entry"
	"github.com/google/uuid"
)

func userEntry(username string, keyID *uuid.UUID) access_entry.AccessEntry {
	e := access_entry.AccessEntry{ID: uuid.New(), Type: access_entry.TypeUser, Username: username}
	if keyID != nil {
		e.KeyIDs = []uuid.UUID{*keyID}
	}
	return e
}

// Union across levels: distinct usernames from different levels all survive.
func TestMergeEffectiveAccess_Union(t *testing.T) {
	k := uuid.New()
	levels := []accessLevel{
		{name: "node", entries: []access_entry.AccessEntry{userEntry("alice", &k)}},
		{name: "root", entries: []access_entry.AccessEntry{userEntry("breakglass", &k)}},
	}
	got := mergeEffectiveAccess(levels)
	if len(got) != 2 {
		t.Fatalf("expected both users to survive, got %d", len(got))
	}
	names := map[string]bool{}
	for _, r := range got {
		names[r.Username] = true
	}
	if !names["alice"] || !names["breakglass"] {
		t.Fatalf("union lost a user: %+v", names)
	}
}

// Nearest level wins entirely for a shared username; the shadowed entry is
// recorded in Overridden.
func TestMergeEffectiveAccess_Override(t *testing.T) {
	nodeKey := uuid.New()
	rootKey := uuid.New()
	nodeE := userEntry("alice", &nodeKey)
	rootE := userEntry("alice", &rootKey)
	levels := []accessLevel{
		{name: "node", entries: []access_entry.AccessEntry{nodeE}},
		{name: "root", entries: []access_entry.AccessEntry{rootE}},
	}
	got := mergeEffectiveAccess(levels)
	if len(got) != 1 {
		t.Fatalf("expected one merged alice, got %d", len(got))
	}
	if got[0].KeyIDs[0] != nodeKey {
		t.Fatalf("node level should win, got key %v", got[0].KeyIDs[0])
	}
	if len(got[0].Overridden) != 1 || got[0].Overridden[0].Level != "root" {
		t.Fatalf("shadowed root entry should be in Overridden, got %+v", got[0].Overridden)
	}
	if got[0].Overridden[0].EntryID != rootE.ID {
		t.Fatalf("overridden provenance should point at the root entry")
	}
}

// Empty key set derives to locked; a key makes it active.
func TestMergeEffectiveAccess_Lifecycle(t *testing.T) {
	k := uuid.New()
	levels := []accessLevel{{name: "ou", entries: []access_entry.AccessEntry{
		userEntry("locked", nil),
		userEntry("active", &k),
	}}}
	got := mergeEffectiveAccess(levels)
	byName := map[string]string{}
	for _, r := range got {
		byName[r.Username] = r.Lifecycle
	}
	if byName["locked"] != string(access_entry.LifecycleLocked) {
		t.Fatalf("empty key set should be locked, got %s", byName["locked"])
	}
	if byName["active"] != string(access_entry.LifecycleActive) {
		t.Fatalf("keyed user should be active, got %s", byName["active"])
	}
}

// CA entries union and de-duplicate by their trusted key.
func TestMergeEffectiveAccess_CADedup(t *testing.T) {
	caKey := uuid.New()
	ca1 := access_entry.AccessEntry{ID: uuid.New(), Type: access_entry.TypeCA, Principals: []string{"deploy"}, KeyIDs: []uuid.UUID{caKey}}
	ca2 := access_entry.AccessEntry{ID: uuid.New(), Type: access_entry.TypeCA, Principals: []string{"deploy"}, KeyIDs: []uuid.UUID{caKey}}
	levels := []accessLevel{
		{name: "node", entries: []access_entry.AccessEntry{ca1}},
		{name: "root", entries: []access_entry.AccessEntry{ca2}},
	}
	got := mergeEffectiveAccess(levels)
	if len(got) != 1 {
		t.Fatalf("expected CA entries de-duplicated by key, got %d", len(got))
	}
}

// The same CA trusted at two levels must union its principals. First-wins meant
// a node-level entry naming "bob" silently revoked the root OU's "alice" — the
// certificate stopped authenticating with nothing in the effective view saying
// why.
func TestMergeUnionsPrincipalsForTheSameCA(t *testing.T) {
	caKey := uuid.New()
	nodeEntry := access_entry.AccessEntry{
		ID: uuid.New(), Type: access_entry.TypeCA,
		KeyIDs: []uuid.UUID{caKey}, Principals: []string{"bob"},
	}
	rootEntry := access_entry.AccessEntry{
		ID: uuid.New(), Type: access_entry.TypeCA,
		KeyIDs: []uuid.UUID{caKey}, Principals: []string{"alice"},
	}

	out := mergeEffectiveAccess([]accessLevel{
		{name: "node", entries: []access_entry.AccessEntry{nodeEntry}},
		{name: "root", entries: []access_entry.AccessEntry{rootEntry}},
	})

	var ca *services.ResolvedAccessEntry
	for i := range out {
		if out[i].Type == string(access_entry.TypeCA) {
			ca = &out[i]
		}
	}
	if ca == nil {
		t.Fatal("CA entry missing from the merged set")
	}
	if len(ca.Principals) != 2 {
		t.Fatalf("principals = %v, want both bob and alice", ca.Principals)
	}
	found := map[string]bool{}
	for _, p := range ca.Principals {
		found[p] = true
	}
	if !found["bob"] || !found["alice"] {
		t.Fatalf("principals = %v, want bob and alice", ca.Principals)
	}
	if len(ca.Overridden) == 0 {
		t.Fatal("the shadowed level should be recorded so the UI can explain the merge")
	}
}
