package results

import (
	"time"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node_tombstone"
)

// DeletedNodeResult is one deleted node an operator may take back.
//
// It deliberately does NOT carry the unique token. The token is what makes the
// restore work, and this endpoint's job is to let someone choose which device to
// restore — not to hand out a credential the device already holds. Same reasoning
// as the write-only shape of secrets.
type DeletedNodeResult struct {
	ID             uuid.UUID  `json:"id"`
	Name           string     `json:"name"`
	Hostname       string     `json:"hostname"`
	HardwareSerial string     `json:"hardware_serial"`
	OUID           *uuid.UUID `json:"ou_id,omitempty"`
	// AdoptionMethod is "eeprom", "file" or "unknown" — what the device would
	// need for a restore to actually bring it back. The UI turns it into the one
	// sentence an operator needs: an EEPROM-provisioned device rejoins on its own,
	// while a device whose identity is a file on its own disk rejoins only if that
	// file is still there.
	AdoptionMethod string `json:"adoption_method"`
	// EEPROMBakeConfirmed: the platform itself flashed THIS node's identity into
	// the EEPROM. Without it, "eeprom" says only that the EEPROM points at a
	// platform, and the UI must not promise that the device rejoins on its own.
	EEPROMBakeConfirmed bool `json:"eeprom_bake_confirmed"`
	// BootOrderNetbootsFirst: whether the device's last reported boot order
	// reaches HTTP boot before any local disk, i.e. it netboots even with an
	// OS installed. Absent when the device never reported one. A disk-first
	// device comes back through whatever its installed OS holds, and reaches
	// the platform's boot path only when that disk fails to boot.
	BootOrderNetbootsFirst *bool `json:"boot_order_netboots_first,omitempty"`
	// IdentityReleaseRequested says the platform asked this device to delete its
	// own adoption file as it was deleted (a forced delete of a connected node).
	//
	// REQUESTED, never confirmed — the command's terminal status arrives after
	// the node row is gone, so nothing can say the device complied. It is carried
	// for one reason: on a node whose identity lives in that file and nowhere
	// else, a restore returns the ROW and not the device, and an offer that says
	// plain "Restore" is a promise the platform cannot keep. Without it here the
	// UI has no way to know.
	IdentityReleaseRequested bool `json:"identity_release_requested"`
	// IdentityReleasedAt: when the device REPORTED erasing its adoption file.
	// Reported, not verified. Absent until the report arrives, and forever for
	// a device that never took the release.
	IdentityReleasedAt *time.Time `json:"identity_released_at,omitempty"`
	// LastRefusedAt: when a device last presented this node's id and matching
	// token and was refused — the device is out there and still trying.
	// REFUSED, never online: the platform saw a credential, not a device.
	// Absent until it happens.
	LastRefusedAt *time.Time `json:"last_refused_at,omitempty"`
	// SerialBoundElsewhere says this record's hardware serial is now carried by a
	// node in another account. The restore refuses outright when it is set
	// (SerialBoundElsewhereError, 409), so this field's job is to stop an
	// operator reaching that refusal — and to stop the panel promising a restore
	// the platform cannot perform.
	//
	// It reports a SERIAL binding and never a device movement. A serial can be
	// bound by an account that never held the hardware, so no copy built on this
	// may claim the device moved.
	//
	// Deliberately NOT omitempty, for IdentityReleaseRequested's reason: false is
	// a real answer, and an absent key is indistinguishable from an older backend.
	SerialBoundElsewhere bool `json:"serial_bound_elsewhere"`
	// SerialBoundInAccount names the live node of THIS account that now carries
	// this record's hardware serial (the board was adopted again as a new
	// node). The restore refuses while it is set (SerialBoundInAccountError,
	// 409 serial_bound_in_account) and Restorable is false; the remedy is to
	// delete that node first. Null when no such node exists — not omitempty,
	// for SerialBoundElsewhere's reason.
	SerialBoundInAccount *DeletedNodeSerialHolder `json:"serial_bound_in_account"`
	DeletedAt            time.Time                `json:"deleted_at"`
	DeletedBy            uuid.UUID                `json:"deleted_by"`
	RestoredAt           *time.Time               `json:"restored_at,omitempty"`
	Restorable           bool                     `json:"restorable"`
}

// DeletedNodeSerialHolder is the caller's own live node that holds a deleted
// node's serial.
type DeletedNodeSerialHolder struct {
	NodeID uuid.UUID `json:"node_id"`
	Name   string    `json:"name"`
}

// NewDeletedNodeResult projects one tombstone.
func NewDeletedNodeResult(t node_tombstone.Tombstone) DeletedNodeResult {
	var holder *DeletedNodeSerialHolder
	if t.SerialBoundInAccount != nil {
		holder = &DeletedNodeSerialHolder{NodeID: t.SerialBoundInAccount.ID, Name: t.SerialBoundInAccount.Name}
	}
	return DeletedNodeResult{
		ID:             t.ID,
		Name:           t.Name,
		Hostname:       t.Hostname,
		HardwareSerial: t.HardwareSerial,
		OUID:           t.OUID,
		AdoptionMethod: t.AdoptionMethod,

		EEPROMBakeConfirmed:    t.EEPROMBakeConfirmed,
		BootOrderNetbootsFirst: t.BootOrderNetbootsFirst,

		IdentityReleaseRequested: t.IdentityReleaseRequested,
		IdentityReleasedAt:       t.IdentityReleasedAt,
		LastRefusedAt:            t.LastRefusedAt,
		SerialBoundElsewhere:     t.SerialBoundElsewhere,
		SerialBoundInAccount:     holder,

		DeletedAt:  t.DeletedAt,
		DeletedBy:  t.DeletedBy,
		RestoredAt: t.RestoredAt,
		Restorable: t.Restorable() && t.CredentialRestorable() && holder == nil,
	}
}

// DeletedNodeListResult is the account's deleted-node records (never nil).
type DeletedNodeListResult struct {
	DeletedNodes []DeletedNodeResult `json:"deleted_nodes"`
}

// NewDeletedNodeListResult projects a list of tombstones.
func NewDeletedNodeListResult(tombstones []node_tombstone.Tombstone) DeletedNodeListResult {
	out := make([]DeletedNodeResult, 0, len(tombstones))
	for _, t := range tombstones {
		out = append(out, NewDeletedNodeResult(t))
	}
	return DeletedNodeListResult{DeletedNodes: out}
}
