package results

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node_tombstone"
)

// The projection dropped IdentityReleaseRequested, so the deleted-nodes panel
// could only ever offer a plain "Restore" — including for a device the platform
// itself asked to erase its on-disk identity, where a restore returns the row
// and not the device. This asserts the field survives the
// projection AND appears in the JSON, because a field present on the struct but
// tagged `-` would pass a struct-only check and still reach no client.
func TestDeletedNodeResultCarriesIdentityReleaseRequested(t *testing.T) {
	for _, requested := range []bool{true, false} {
		tomb := node_tombstone.Tombstone{
			ID:                       uuid.New(),
			Name:                     "Node-abcd0001",
			HardwareSerial:           "10000000abcd0001",
			AdoptionMethod:           node_tombstone.AdoptionFile,
			IdentityReleaseRequested: requested,
			DeletedAt:                time.Now(),
			DeletedBy:                uuid.New(),
		}

		got := NewDeletedNodeResult(tomb)
		if got.IdentityReleaseRequested != requested {
			t.Fatalf("projection dropped the flag: want %v, got %v", requested, got.IdentityReleaseRequested)
		}

		raw, err := json.Marshal(got)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		var decoded map[string]any
		if err := json.Unmarshal(raw, &decoded); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		// Deliberately NOT omitempty: false is a meaningful answer here ("the
		// device was never asked"), and an absent key is indistinguishable from
		// an older backend that does not report it at all.
		v, present := decoded["identity_release_requested"]
		if !present {
			t.Fatalf("identity_release_requested missing from JSON for requested=%v: %s", requested, raw)
		}
		if v != requested {
			t.Fatalf("identity_release_requested = %v in JSON, want %v", v, requested)
		}
	}
}

// The panel decides whether to offer a restore from this key, so it must reach
// the client for BOTH values. The false case is the regression that matters: an
// `omitempty` added later would silently drop it on every unaffected row, which
// is the same shape as the projection defect this file already guards against.
func TestDeletedNodeResultCarriesSerialBoundElsewhere(t *testing.T) {
	for _, bound := range []bool{true, false} {
		tomb := node_tombstone.Tombstone{
			ID:                   uuid.New(),
			Name:                 "Node-abcd0001",
			HardwareSerial:       "10000000abcd0001",
			AdoptionMethod:       node_tombstone.AdoptionEEPROM,
			SerialBoundElsewhere: bound,
			DeletedAt:            time.Now(),
			DeletedBy:            uuid.New(),
		}

		got := NewDeletedNodeResult(tomb)
		if got.SerialBoundElsewhere != bound {
			t.Fatalf("projection dropped the flag: want %v, got %v", bound, got.SerialBoundElsewhere)
		}

		raw, err := json.Marshal(got)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		var decoded map[string]any
		if err := json.Unmarshal(raw, &decoded); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		v, present := decoded["serial_bound_elsewhere"]
		if !present {
			t.Fatalf("serial_bound_elsewhere missing from JSON for bound=%v: %s", bound, raw)
		}
		if v != bound {
			t.Fatalf("serial_bound_elsewhere = %v, want %v", v, bound)
		}
	}
}

// restorable tells the truth the restore acts on: a record whose device
// credential did not decrypt, or decrypted to nothing, is refused by the
// restore (node_credential_unreadable), so the listing must not offer it.
func TestDeletedNodeResultIsRestorableOnlyWhenTheRestoreWouldBe(t *testing.T) {
	restored := time.Now()
	cases := []struct {
		name string
		tomb node_tombstone.Tombstone
		want bool
	}{
		{"readable credential", node_tombstone.Tombstone{UniqueToken: "tok"}, true},
		{"undecryptable credential", node_tombstone.Tombstone{UniqueTokenUnreadable: true}, false},
		{"empty credential", node_tombstone.Tombstone{UniqueToken: "  "}, false},
		{"already restored", node_tombstone.Tombstone{UniqueToken: "tok", RestoredAt: &restored}, false},
	}
	for _, tc := range cases {
		if got := NewDeletedNodeResult(tc.tomb).Restorable; got != tc.want {
			t.Errorf("%s: restorable = %v, want %v", tc.name, got, tc.want)
		}
	}
}
