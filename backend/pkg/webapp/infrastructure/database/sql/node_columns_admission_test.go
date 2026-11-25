package sql

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// UpdateNodeColumns never writes the two columns that decide device-cap
// admission, under ANY spelling GORM's Updates(map) accepts: the column name
// and the struct field name resolve to the same column. Refused before any
// statement, so this needs no database — the connection below is never
// dialled.
func TestUpdateNodeColumnsRefusesTheAdmissionColumnsInEverySpelling(t *testing.T) {
	db, err := gorm.Open(postgres.New(postgres.Config{DSN: "host=127.0.0.1 port=1 user=none dbname=none sslmode=disable"}),
		&gorm.Config{DisableAutomaticPing: true, Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("opening an undialled handle: %v", err)
	}
	repo := NewNodeRepository(db, passthroughEncryptor{})

	cases := []struct {
		key    string
		column string
	}{
		{"status", "status"},
		{"Status", "status"},
		{"approval_status", "approval_status"},
		{"ApprovalStatus", "approval_status"},
	}
	for _, tc := range cases {
		t.Run(tc.key, func(t *testing.T) {
			err := repo.UpdateNodeColumns(context.Background(), uuid.New(), uuid.New(),
				map[string]interface{}{"notes": "x", tc.key: "online"})
			if err == nil || !strings.Contains(err.Error(), "does not write "+tc.column) {
				t.Errorf("UpdateNodeColumns(%q): err = %v, want the refusal naming %s", tc.key, err, tc.column)
			}
		})
	}
}
