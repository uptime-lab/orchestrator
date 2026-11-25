package node

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// RecordBootloaderFlashed stamps the settle time of a successful EEPROM flash
// (update_boot_order), called by the gRPC settle hook on the command's
// terminal success. The stamp is a device fact, not view state: it is what
// keeps "flashed, reboot still owed" visible after the panel closes and in
// every later session, until LastBootAt moves past it.
func (s *Service) RecordBootloaderFlashed(ctx context.Context, accountID, nodeID uuid.UUID) error {
	n, err := s.nodeRepo.GetByID(ctx, accountID, nodeID)
	if err != nil {
		return err
	}
	// Named column, not a full-row save: a full-row save writes back every other
	// column from the read above and can undo a concurrent update.
	flashedAt := time.Now()
	if err := s.nodeRepo.UpdateNodeColumns(ctx, accountID, nodeID, map[string]interface{}{
		"bootloader_flashed_at": flashedAt,
	}); err != nil {
		return err
	}
	n.BootloaderFlashedAt = &flashedAt
	s.notifyNodeUpdated(*n)
	return nil
}
