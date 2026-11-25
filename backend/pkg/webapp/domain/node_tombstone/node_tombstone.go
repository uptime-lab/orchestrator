// Package node_tombstone records what a deleted node WAS, so a device that is
// still out there can be taken back.
//
// The problem it solves: deleting a node removes the row and tells the device nothing.
// The device's adoption file still names a node_id that no longer exists, so its
// gRPC ConnectionInit is refused; HTTP-boot refuses an unknown serial
// fail-closed, so a network-first node cannot even reach recovery. It boots its
// installed OS and retries into nothing, indefinitely — powered, on the network,
// invisible to the platform, and with no warning in the delete dialog that this
// is what "Delete" means.
//
// Recovery was possible but only by knowing that the EEPROM package the platform
// baked is also a credential store, and that the audit trail holds the deleted
// node id. Both were undocumented, and neither is discoverable while staring at
// an empty node list. Worse, it worked only for an EEPROM-provisioned device: one
// adopted by install script or SD card keeps its identity in a file on its own
// disk, which the platform cannot read.
//
// A tombstone makes recovery uniform and trivial instead, because the identity a
// restore needs is exactly what the platform is about to throw away. Note what
// this is NOT: it is not a soft delete. The node is gone from every list, every
// query and every count; this is a small separate record of how to accept the
// device back if someone asks.
package node_tombstone

import (
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
)

// SerialHolderNode names the live node of the record's own account that now
// carries the record's hardware serial.
type SerialHolderNode struct {
	ID   uuid.UUID
	Name string
}

// Tombstone is the identity of a deleted node.
//
// ID is the DELETED NODE'S id, not a new one. That is the whole point: the
// device authenticates as that id and holds a token issued for it, so restoring
// under a fresh id would produce a row the device can never match.
type Tombstone struct {
	ID        uuid.UUID
	AccountID uuid.UUID
	// HardwareSerial is how a netbooting device is recognised before it has any
	// identity to present. Empty for a node that never reported a fingerprint (a
	// WebUSB placeholder, or a row created and deleted before first contact) —
	// such a node cannot be recognised at boot, only restored by hand.
	HardwareSerial string
	// UniqueToken is the credential the device still holds. Storing it is not a
	// new exposure: deleting our copy revokes nothing, because the device keeps
	// its own in its adoption file or its EEPROM — it only destroys the platform's
	// ability to accept that device back.
	UniqueToken string
	// UniqueTokenUnreadable: stored ciphertext no configured key can read
	// (node.Node.UniqueTokenUnreadable's twin). A restore with it set would
	// hand the device back a blank credential; the restore path refuses
	// instead. Never persisted.
	UniqueTokenUnreadable bool
	// SerialBoundElsewhere: at read time, this serial is carried by a node in
	// ANOTHER account and by none in this one, so the restore will refuse it and
	// the device this record names is not reachable through it.
	//
	// Computed by ListDeleted, never persisted — UniqueTokenUnreadable's twin.
	// Every other load path leaves it false, which is the safe default: the
	// restore asks the question again for itself rather than trusting a flag
	// resolved at some earlier moment.
	SerialBoundElsewhere bool
	// SerialBoundInAccount: at read time, this serial is carried by a LIVE node
	// of THIS account (the board was adopted again as a new node), so the
	// restore refuses it — two live rows claiming one serial would let the boot
	// lookup pick the stale one. Computed by ListDeleted for a record that is
	// otherwise restorable, never persisted; nil everywhere else.
	SerialBoundInAccount *SerialHolderNode
	Name                 string
	Hostname             string
	// OUID is the unit the node belonged to. It may no longer exist by the time
	// anyone restores; the restore then brings the node back unassigned rather
	// than failing or writing a reference to a unit that is gone.
	OUID *uuid.UUID
	// AdoptionMethod records how the device got its identity, because it decides
	// what a restore is worth. See the constants below.
	AdoptionMethod string
	// EEPROMBakeConfirmed: the platform itself flashed THIS node's identity into
	// the EEPROM (an update_boot_order settled, or a WebUSB flash was committed).
	// AdoptionEEPROM alone says only that the EEPROM points at a platform — a
	// device baked for a previous node id and re-adopted by file reports the
	// same HTTP_HOST — so the restore copy may promise "it rejoins on its own"
	// only when this is set.
	EEPROMBakeConfirmed bool
	// BootOrderNetbootsFirst: whether the boot order the device last reported
	// reaches HTTP boot BEFORE any local disk (domain/node.
	// BootOrderCanReturnToNetboot — the device netboots even with an OS
	// installed), captured at delete time because the row that knew it is
	// gone. nil = never reported. A netboot-first device presents its EEPROM
	// identity on every boot; a disk-first one presents whatever its installed
	// OS holds, and asks the platform for a boot image only when that disk
	// fails to boot.
	BootOrderNetbootsFirst *bool
	// BootToOS is the node's boot override, carried so a restore puts the device
	// back on the boot path it was on.
	//
	// It matters more than it looks: HTTP-boot only SERVES a serial whose node
	// wants to netboot, so a network-first device restored as "boot the local OS"
	// would keep getting a 404 for boot.sig and the restore would appear to have
	// done nothing. Restoring it as netboot unconditionally would be the opposite
	// mistake — it would drag a healthy node into recovery on its next reboot. So
	// it is recorded rather than chosen, including nil, which means "inherit from
	// the OU or the account policy".
	BootToOS *bool
	// WasAdmitted records whether the node was ADMITTED (node.IsAdmitted:
	// approved and out of every pre-connect status) when it was deleted, and
	// ApprovalStatus / Status record the two columns that decided it.
	//
	// They exist because a restore is not allowed to be an admission. The
	// device cap is enforced where a row first becomes admitted — a first
	// connect, an approval — under one lock; a restore that re-created every
	// row approved and offline turned a placeholder that never connected, a
	// pending device and a rejected one into admitted devices with no count,
	// so Add, Delete, Restore grew the fleet past the cap without bound.
	// Recorded, the restore puts a never-admitted row back exactly as it
	// was, and its first connect or approval is the admission like any other.
	//
	// nil = a tombstone written before these were recorded. RestoreState
	// treats it as NOT admitted: the device is then admitted on its next
	// connect, under the lock, which is the fail-safe direction for the cap.
	WasAdmitted    *bool
	ApprovalStatus node.ApprovalStatus
	Status         node.NodeStatus
	// IdentityReleaseRequested records that the platform asked the agent to
	// delete its own adoption file as the node was deleted (a forced delete of an
	// ONLINE node).
	//
	// REQUESTED, not confirmed, and the difference is the whole reason it is a
	// separate field rather than an inference: the command's terminal status
	// arrives after the node row is gone, so nothing here can ever say the device
	// complied. What it is for is keeping the restore offer honest — if the device
	// took the release, its identity is gone from disk and putting the row back
	// brings nothing with it.
	IdentityReleaseRequested bool
	// IdentityReleasedAt is when the device REPORTED that it erased its adoption
	// file — the terminal success of the release command, which arrives after
	// the node row is gone and is therefore stamped here. Reported by the
	// device, never verified: the platform can only repeat what the agent said.
	IdentityReleasedAt *time.Time
	// LastRefusedAt is when a device last presented THIS deleted node's id and
	// token to the platform and was refused — i.e. the device is out there,
	// powered, and still trying. Stamped from the handshake's refusal branch
	// only after the presented token matched the stored one (a device that
	// merely names the id proves nothing), and throttled, because a stranded
	// agent redials about once a second.
	//
	// It says REFUSED, never online: the platform saw a credential, not a
	// device. What it is for is telling the operator that "restore it, then
	// delete it while it is connected" would work right now.
	LastRefusedAt *time.Time
	DeletedAt     time.Time
	DeletedBy     uuid.UUID
	// RestoredAt marks a tombstone that has already been used. Kept rather than
	// deleted: "this node was deleted on the 29th and restored on the 1st" is the
	// question an operator actually asks later.
	RestoredAt *time.Time
}

// Adoption methods. These are about where the DEVICE keeps its identity, which
// is what decides whether a restore brings it back on its own.
const (
	// AdoptionEEPROM: the identity is baked into the device's EEPROM by
	// provision-eeprom. The platform also still holds the package it signed, so
	// this was the one case recoverable before tombstones existed.
	AdoptionEEPROM = "eeprom"
	// AdoptionFile: the identity lives in /etc/orchestrator/node-adoption.txt on
	// the device's own disk (install script, SD card, SSH adopt). The platform
	// cannot read it — which is exactly why recording it here matters.
	AdoptionFile = "file"
	// AdoptionUnknown: no fingerprint was ever reported, so nothing is known
	// about how (or whether) this device holds an identity.
	AdoptionUnknown = "unknown"
)

// Restorable reports whether this tombstone can still be used. A tombstone that
// has already been restored is history, not an offer.
func (t Tombstone) Restorable() bool {
	return t.RestoredAt == nil
}

// CredentialRestorable reports whether the record still carries a device
// credential a restore can hand back: one that decrypted, and is not empty.
// It is the predicate the restore refuses on (node_credential_unreadable), so
// a listing that offers a restore and the restore itself cannot disagree.
func (t Tombstone) CredentialRestorable() bool {
	return !t.UniqueTokenUnreadable && strings.TrimSpace(t.UniqueToken) != ""
}

// RecognisableAtBoot reports whether a netbooting device carrying this serial can
// be identified as a deleted node rather than an unknown one. Used by the
// HTTP-boot handler so a stranded node's honest request is not counted as serial
// enumeration against its own site's IP.
func (t Tombstone) RecognisableAtBoot() bool {
	return t.HardwareSerial != ""
}

// RestoreState is the status and approval a restore re-creates the node
// with, and whether that row is admitted. Admission is decided by
// node.IsAdmitted on the result, never by a separate flag the caller could
// get wrong.
//
//   - Admitted at deletion: approved and offline. A restore returns a device
//     the install already managed, so it is restored admitted; the
//     device reports its real status when it reconnects.
//   - Recorded and NOT admitted: the original approval, and the original
//     status while it is a pre-connect one. A rejected device stays
//     rejected, a pending one pending, a never-connected placeholder stays
//     ready_for_adoption (or flashing). A pending or rejected device that
//     had a live status is restored offline, since nothing is connected to
//     a row that did not exist a moment ago.
//   - Not recorded (a tombstone older than WasAdmitted): approved and
//     ready_for_adoption. Its device's next connect is a first connect and
//     therefore an admission under the cap; restoring it admitted would
//     reopen the bypass for every row written before this field.
func (t *Tombstone) RestoreState() (node.NodeStatus, node.ApprovalStatus) {
	switch {
	case t.WasAdmitted != nil && *t.WasAdmitted:
		return node.StatusOffline, node.ApprovalStatusApproved
	case t.WasAdmitted != nil && knownApproval(t.ApprovalStatus):
		status := node.StatusOffline
		if node.IsPreConnectStatus(t.Status) {
			status = t.Status
		}
		if t.ApprovalStatus == node.ApprovalStatusApproved && !node.IsPreConnectStatus(status) {
			// An approved row recorded as not admitted can only have been in a
			// pre-connect status; anything else is a record this code did not
			// write, and it must not come back admitted.
			status = node.StatusReadyForAdoption
		}
		return status, t.ApprovalStatus
	default:
		return node.StatusReadyForAdoption, node.ApprovalStatusApproved
	}
}

func knownApproval(a node.ApprovalStatus) bool {
	switch a {
	case node.ApprovalStatusApproved, node.ApprovalStatusPendingApproval, node.ApprovalStatusRejected:
		return true
	}
	return false
}
