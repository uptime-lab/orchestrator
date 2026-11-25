package node

import (
	"context"

	"github.com/google/uuid"
)

// What the platform believes a device has applied is only true of the disk that
// was there when it applied it. A successful install_os writes a new image over
// that disk, so the marker block in config.txt, the rendered watchdog config,
// the persisted LED state and every installed utility are gone — while the
// recorded hashes still equal the desired ones, so every ladder in the UI reads
// converged and no re-apply is ever offered.
//
// Access is NOT in this list, and deliberately: it already self-heals through
// accessReapplyNeeded, which compares the last successful apply against the last
// successful install_os and re-enqueues on the recovery→OS transition. A second
// mechanism for the same fact is how two answers drift apart.
//
// Nothing here dispatches. Every one of these applies is operator-triggered by
// design (phase 1 device profiles never auto-dispatch, an LED config reaches a
// device only through a per-node apply, and nothing may arm a watchdog without a
// human present), so the honest outcome of a reimage is a ladder that says
// "not applied" — not a fleet of automatic re-applies at 3am.

// InvalidateAppliedStateAfterReimage forgets the per-device applied markers a
// fresh OS image has just invalidated.
//
// Best-effort per column: this runs from a command settle, and failing the
// settle because one marker could not be cleared would leave the command
// unfinished, which is worse than one stale ladder.
func (s *Service) InvalidateAppliedStateAfterReimage(ctx context.Context, accountID, nodeID uuid.UUID) error {
	n, err := s.nodeRepo.GetByID(ctx, accountID, nodeID)
	if err != nil {
		return err
	}
	columns := map[string]interface{}{}
	if n.DeviceProfileAppliedHash != "" {
		columns["device_profile_applied_hash"] = ""
		columns["device_profile_applied_at"] = nil
	}
	if n.WatchdogAppliedHash != "" {
		// The disarm reason goes with it: it describes a watchdog that no longer
		// exists on the device, and leaving it would report the new OS as having
		// disarmed something it never armed.
		columns["watchdog_applied_hash"] = ""
		columns["watchdog_disarm_reason"] = ""
		columns["watchdog_last_refusal"] = ""
	}
	if n.LEDAppliedHash != "" {
		columns["led_applied_hash"] = ""
	}
	if len(columns) == 0 {
		return nil
	}
	if err := s.nodeRepo.UpdateNodeColumns(ctx, accountID, nodeID, columns); err != nil {
		return err
	}
	// Named columns, not a full-row save: this runs from a settle that races the
	// gRPC layer's own writes, including the `restarting` pin.
	if _, cleared := columns["device_profile_applied_hash"]; cleared {
		n.DeviceProfileAppliedHash = ""
		n.DeviceProfileAppliedAt = nil
	}
	if _, cleared := columns["watchdog_applied_hash"]; cleared {
		n.WatchdogAppliedHash = ""
		n.WatchdogDisarmReason = ""
		n.WatchdogLastRefusal = ""
	}
	if _, cleared := columns["led_applied_hash"]; cleared {
		n.LEDAppliedHash = ""
	}
	s.notifyNodeUpdated(*n)
	return nil
}
