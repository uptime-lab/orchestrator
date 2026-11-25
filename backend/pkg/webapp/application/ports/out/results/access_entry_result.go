package results

import (
	"time"
	"sort"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/repository"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/access_entry"
)

// AccessEntryResult is an access entry in API responses. The password value is
// NEVER returned — only whether one is set (HasPassword). Lifecycle is derived,
// not stored.
type AccessEntryResult struct {
	ID        uuid.UUID `json:"id"`
	AccountID uuid.UUID `json:"account_id"`
	Type      string    `json:"type"`  // user | ca
	Scope     string    `json:"scope"` // node | ou | account | unassigned
	// OUIDs is the set of units the entry lives in (a user can be in several).
	OUIDs          []uuid.UUID `json:"ou_ids"`
	NodeID         *uuid.UUID  `json:"node_id,omitempty"`
	AccountWide    bool        `json:"account_wide"`
	Username       string      `json:"username,omitempty"`
	Sudo           bool        `json:"sudo"`
	HasPassword    bool        `json:"has_password"`
	AdoptUnmanaged bool        `json:"adopt_unmanaged"`
	Principals     []string    `json:"principals,omitempty"`
	KeyIDs         []uuid.UUID `json:"key_ids"`
	Lifecycle      string      `json:"lifecycle,omitempty"` // active | locked (user entries)
	CreatedAt      time.Time       `json:"created_at"`
	UpdatedAt      time.Time       `json:"updated_at"`
}

// NewAccessEntryResult maps a domain entry to its API representation.
func NewAccessEntryResult(e *access_entry.AccessEntry) AccessEntryResult {
	r := AccessEntryResult{
		ID:             e.ID,
		AccountID:      e.AccountID,
		Type:           string(e.Type),
		Scope:          e.TargetScope(),
		OUIDs:          e.OUIDs,
		NodeID:         e.NodeID,
		AccountWide:    e.AccountWide,
		Username:       e.Username,
		Sudo:           e.Sudo,
		HasPassword:    e.Password != nil && *e.Password != "",
		AdoptUnmanaged: e.AdoptUnmanaged,
		Principals:     e.Principals,
		KeyIDs:         e.KeyIDs,
		CreatedAt:      e.CreatedAt,
		UpdatedAt:      e.UpdatedAt,
	}
	if e.Type == access_entry.TypeUser {
		r.Lifecycle = string(e.DesiredState().Lifecycle)
	}
	if r.KeyIDs == nil {
		r.KeyIDs = []uuid.UUID{}
	}
	if r.OUIDs == nil {
		r.OUIDs = []uuid.UUID{}
	}
	return r
}

// NewAccessEntryResults maps a slice of domain entries.
func NewAccessEntryResults(entries []access_entry.AccessEntry) []AccessEntryResult {
	out := make([]AccessEntryResult, len(entries))
	for i := range entries {
		out[i] = NewAccessEntryResult(&entries[i])
	}
	return out
}

// AccessSourceResult reports the level an effective entry (or an overridden
// one) came from.
type AccessSourceResult struct {
	Level   string     `json:"level"` // node | ou | root | account
	OUID    *uuid.UUID `json:"ou_id,omitempty"`
	EntryID uuid.UUID  `json:"entry_id"`
}

// EffectiveAccessEntryResult is a resolved effective entry with provenance. The
// password value is never surfaced (only HasPassword).
type EffectiveAccessEntryResult struct {
	EntryID     uuid.UUID            `json:"entry_id"`
	Type        string               `json:"type"`
	Username    string               `json:"username,omitempty"`
	Sudo        bool                 `json:"sudo"`
	HasPassword bool                 `json:"has_password"`
	Principals  []string             `json:"principals,omitempty"`
	KeyIDs      []uuid.UUID          `json:"key_ids"`
	Lifecycle   string               `json:"lifecycle,omitempty"`
	Source      AccessSourceResult   `json:"source"`
	Overridden  []AccessSourceResult `json:"overridden,omitempty"`
}

// NewEffectiveAccessResults maps resolved entries to their API representation,
// dropping the decrypted password.
func NewEffectiveAccessResults(entries []services.ResolvedAccessEntry) []EffectiveAccessEntryResult {
	out := make([]EffectiveAccessEntryResult, len(entries))
	for i, e := range entries {
		keyIDs := e.KeyIDs
		if keyIDs == nil {
			keyIDs = []uuid.UUID{}
		}
		res := EffectiveAccessEntryResult{
			EntryID:     e.EntryID,
			Type:        e.Type,
			Username:    e.Username,
			Sudo:        e.Sudo,
			HasPassword: e.HasPassword,
			Principals:  e.Principals,
			KeyIDs:      keyIDs,
			Lifecycle:   e.Lifecycle,
			Source:      AccessSourceResult{Level: e.Source.Level, OUID: e.Source.OUID, EntryID: e.Source.EntryID},
		}
		for _, o := range e.Overridden {
			res.Overridden = append(res.Overridden, AccessSourceResult{Level: o.Level, OUID: o.OUID, EntryID: o.EntryID})
		}
		out[i] = res
	}
	return out
}

// AccessBucketCounts is one rail bucket's counts split by entry type, so the
// UI can show the number matching the active tab (Users vs CA Certificates).
type AccessBucketCounts struct {
	Users int64 `json:"users"`
	CAs   int64 `json:"cas"`
}

// AccessOUCountResult is one OU's access-entry counts for the filter rail.
type AccessOUCountResult struct {
	OUID  uuid.UUID          `json:"ou_id"`
	Count AccessBucketCounts `json:"count"`
}

// AccessOUCountsResult is the OU facet for the credentials filter tree.
// Unassigned entries apply to no node; AccountWide entries are the fleet-wide
// default. They are reported separately so the rail can show two buckets.
type AccessOUCountsResult struct {
	OUs         []AccessOUCountResult `json:"ous"`
	Unassigned  AccessBucketCounts    `json:"unassigned"`
	AccountWide AccessBucketCounts    `json:"account_wide"`
	Total       int64                 `json:"total"`
}

// NewAccessOUCountsResult maps per-OU entry counts to the DTO, sorted for a
// stable response.
func NewAccessOUCountsResult(counts map[uuid.UUID]repository.AccessEntryCounts, unassigned, accountWide repository.AccessEntryCounts, total int64) AccessOUCountsResult {
	items := make([]AccessOUCountResult, 0, len(counts))
	for ouID, c := range counts {
		items = append(items, AccessOUCountResult{OUID: ouID, Count: AccessBucketCounts{Users: c.Users, CAs: c.CAs}})
	}
	sort.Slice(items, func(i, j int) bool {
		return items[i].OUID.String() < items[j].OUID.String()
	})
	return AccessOUCountsResult{
		OUs:         items,
		Unassigned:  AccessBucketCounts{Users: unassigned.Users, CAs: unassigned.CAs},
		AccountWide: AccessBucketCounts{Users: accountWide.Users, CAs: accountWide.CAs},
		Total:       total,
	}
}
