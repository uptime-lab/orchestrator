package node

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
)

// adoptionRefusalListLimit bounds the read. The set is already bounded by
// (serials genuinely held elsewhere) × (this account) — the guard records
// nothing for a serial nobody holds — so this is a page size, not a cap that
// decides anything.
const adoptionRefusalListLimit = 100

// ListAdoptionRefusals returns the devices this account's install flow has
// turned away because their serial is bound to another account.
//
// The source is the platform's own refusal record (instance_anomalies, kind
// serial_binding_conflict, account_id = this account), projected to the fields
// a tenant may see. Two facts are DERIVED here rather than stored, so they can
// never go stale: whether the device is still retrying (Live, a clock rule in
// the domain) and whether the serial is still bound elsewhere (asked of the
// nodes table through the same projection the Deleted Nodes panel uses, never
// through ListByExactSerial, which decrypts foreign rows).
//
// A serial this account now holds itself is dropped: the device got in, and a
// record that says otherwise is the panel lying in the other direction.
func (s *Service) ListAdoptionRefusals(ctx context.Context, accountID uuid.UUID) ([]node.AdoptionRefusal, error) {
	if s.anomalyRepo == nil {
		return []node.AdoptionRefusal{}, nil
	}
	rows, err := s.anomalyRepo.ListSerialBindingRefusals(ctx, accountID, adoptionRefusalListLimit)
	if err != nil {
		return nil, fmt.Errorf("listing adoption refusals: %w", err)
	}
	if len(rows) == 0 {
		return []node.AdoptionRefusal{}, nil
	}
	serials := make([]string, 0, len(rows))
	for _, row := range rows {
		serials = append(serials, node.NormalizeSerial(row.HardwareSerial))
	}
	scopes, err := s.nodeRepo.SerialBindingScopes(ctx, accountID, serials)
	if err != nil {
		return nil, fmt.Errorf("resolving adoption refusal bindings: %w", err)
	}
	now := time.Now()
	out := make([]node.AdoptionRefusal, 0, len(rows))
	for _, row := range rows {
		serial := node.NormalizeSerial(row.HardwareSerial)
		scope := scopes[serial]
		if scope.InAccount {
			continue
		}
		out = append(out, node.AdoptionRefusal{
			HardwareSerial:      serial,
			FirstSeenAt:         row.FirstSeenAt,
			LastSeenAt:          row.LastSeenAt,
			Occurrences:         row.OccurrenceCount,
			Live:                node.AdoptionRefusalIsLive(row.LastSeenAt, now),
			StillBoundElsewhere: node.SerialConflictsAcrossAccounts(serial, scope.InAccount, scope.InAnotherAccount),
		})
	}
	return out, nil
}
