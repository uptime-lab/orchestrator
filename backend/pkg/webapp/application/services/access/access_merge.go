package access

import (
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/access_entry"
	"github.com/google/uuid"
)

// accessLevel groups a target's access entries with the provenance label used
// for the resolved source. Entries must already be hydrated (KeyIDs set) and
// decrypted (Password set) by the repository.
type accessLevel struct {
	name    string // node | ou | root | account
	ouID    *uuid.UUID
	entries []access_entry.AccessEntry
}

// mergeEffectiveAccess unions access entries across levels. The levels slice
// must be ordered highest precedence FIRST (node, then OU, then root, then
// account). Within a single username the nearest level wins entirely — the
// YubiKey/break-glass semantics: a break-glass user from the root OU survives a
// child OU defining its own different users, and swapping keys on the child
// does not disturb the parent's entry. Shadowed same-username entries are
// recorded in Overridden so the UI can show them. CA entries do not conflict on
// username; they union and de-duplicate by their single trusted key.
//
// It is a pure function to keep the precedence logic unit-testable, mirroring
// mergeEffectiveSecrets.
func mergeEffectiveAccess(levels []accessLevel) []services.ResolvedAccessEntry {
	var out []services.ResolvedAccessEntry
	userIndex := make(map[string]int) // username -> index into out
	caSeen := make(map[uuid.UUID]int) // trusted CA key id -> index in out (principals union into it)

	for _, level := range levels {
		for i := range level.entries {
			e := &level.entries[i]
			src := services.AccessEntrySource{Level: level.name, OUID: level.ouID, EntryID: e.ID}

			switch e.Type {
			case access_entry.TypeUser:
				if idx, seen := userIndex[e.Username]; seen {
					// A higher-precedence level already owns this username; this
					// lower-precedence entry is shadowed. But a SINGLE entry
					// spanning a parent AND its child OU is loaded at both the
					// "ou" and "root" levels — it does not shadow ITSELF, so skip
					// the self-reference (it would otherwise list itself in
					// Overridden and read as a phantom conflict).
					if out[idx].EntryID != e.ID {
						out[idx].Overridden = append(out[idx].Overridden, src)
					}
					continue
				}
				ds := e.DesiredState()
				userIndex[e.Username] = len(out)
				out = append(out, services.ResolvedAccessEntry{
					EntryID:        e.ID,
					Type:           string(e.Type),
					Username:       e.Username,
					Sudo:           e.Sudo,
					SudoNoPasswd:   ds.SudoNoPasswd,
					AdoptUnmanaged: e.AdoptUnmanaged,
					HasPassword:    ds.HasPassword,
					Password:       derefString(e.Password),
					// Certificate principals accepted to log in as this user
					// (only effective when a CA is trusted in scope).
					Principals: append([]string(nil), e.Principals...),
					KeyIDs:     e.KeyIDs,
					Lifecycle:  string(ds.Lifecycle),
					Source:     src,
				})

			case access_entry.TypeCA:
				if len(e.KeyIDs) == 0 {
					continue
				}
				caKey := e.KeyIDs[0]
				if idx, seen := caSeen[caKey]; seen {
					// The same CA trusted at two levels UNIONS its principals
					// rather than the nearest level winning outright. First-wins
					// silently dropped the other level's principals — a node-level
					// entry naming "bob" would have revoked the root OU's "alice"
					// with nothing in the effective view explaining why.
					for _, pr := range e.Principals {
						if !containsPrincipal(out[idx].Principals, pr) {
							out[idx].Principals = append(out[idx].Principals, pr)
						}
					}
					// Same entry re-seen across parent+child levels is not a
					// distinct override of itself.
					if out[idx].EntryID != e.ID {
						out[idx].Overridden = append(out[idx].Overridden, src)
					}
					continue
				}
				caSeen[caKey] = len(out)
				out = append(out, services.ResolvedAccessEntry{
					EntryID:    e.ID,
					Type:       string(e.Type),
					Principals: append([]string(nil), e.Principals...),
					KeyIDs:     e.KeyIDs,
					Source:     src,
				})
			}
		}
	}
	return out
}

// containsPrincipal reports membership in a principal list.
func containsPrincipal(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

func derefString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
