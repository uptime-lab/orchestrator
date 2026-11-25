package sql

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/workflow_execution_node"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Idempotent encryption backfill for rows written before at-rest encryption
// covered them: legacy node_commands payload/result values and
// workflow_execution_nodes graph-state outputs stored in the clear. Runs from
// each repository's Setup() at startup, before the servers accept traffic, so
// it cannot race live updates. A row already fully prefixed is left untouched,
// so re-running is free; tables are scanned in id-keyset batches.

const backfillBatchSize = 500

// contentBackfillBatchSize is the batch for sweeps over rows that can be LARGE.
// A tracked file version is capped at 512 KiB, so 500 of them is ~256 MB held at
// once inside a memory-limited container — the batch size that is right for a table of
// small JSON payloads is an OOM on a table of file contents. 25 keeps the same
// sweep under ~13 MB.
const contentBackfillBatchSize = 25

// backfillCommandRow returns re-encrypted payload/result JSON for a legacy row,
// with changed=false when the row is already fully covered. Pure — testable
// without a database.
func backfillCommandRow(payloadJSON *string, resultJSON []byte, details *string, enc commandFieldEncryptor) (newPayload *string, newResult []byte, newDetails *string, changed bool) {
	newPayload, newResult, newDetails = payloadJSON, resultJSON, details
	if enc == nil {
		return newPayload, newResult, newDetails, false
	}

	if payloadJSON != nil && *payloadJSON != "" {
		var payload map[string]interface{}
		if err := json.Unmarshal([]byte(*payloadJSON), &payload); err == nil && hasPlaintextPayloadValue(payload) {
			encrypted, err := encryptCommandPayload(payload, enc, true)
			if err == nil {
				if b, err := json.Marshal(encrypted); err == nil {
					s := string(b)
					newPayload = &s
					changed = true
				}
			}
		}
	}

	if len(resultJSON) > 0 {
		var result map[string]string
		if err := json.Unmarshal(resultJSON, &result); err == nil {
			needs := false
			for _, v := range result {
				if v != "" && !strings.HasPrefix(v, commandCipherPrefix) {
					needs = true
					break
				}
			}
			if needs {
				if encrypted, err := encryptCommandResult(result, enc, true); err == nil {
					if b, err := json.Marshal(encrypted); err == nil {
						newResult = b
						changed = true
					}
				}
			}
		}
	}

	// Details, sealed by the same sweep. Legacy rows carry plaintext with no
	// prefix; preservePrefixed=true leaves ciphertext this package already
	// wrote alone, including ciphertext under a dropped key, which must reach
	// the rewrap machinery verbatim rather than being wrapped twice.
	if details != nil && *details != "" && !strings.HasPrefix(*details, commandCipherPrefix) {
		if sealed, ok := encryptCommandDetails(*details, enc, true); ok {
			newDetails = &sealed
			changed = true
		}
	}

	return newPayload, newResult, newDetails, changed
}

// hasPlaintextPayloadValue reports whether any string anywhere in the payload
// is still stored in the clear. It walks the whole structure rather than a list
// of known keys, so rows written while encryption was a field whitelist — a
// secret resolved into dest_path, a package spec, an http_request field — are
// picked up by this sweep too, not just rows written before
// encryption existed at all.
func hasPlaintextPayloadValue(payload map[string]interface{}) bool {
	for k, v := range payload {
		if commandPlaintextKeys[k] {
			continue
		}
		if valueHasPlaintext(v) {
			return true
		}
	}
	return false
}

func valueHasPlaintext(v interface{}) bool {
	switch val := v.(type) {
	case string:
		return val != "" && !strings.HasPrefix(val, commandCipherPrefix)
	case []interface{}:
		for i := range val {
			if valueHasPlaintext(val[i]) {
				return true
			}
		}
	case map[string]interface{}:
		for _, item := range val {
			if valueHasPlaintext(item) {
				return true
			}
		}
	}
	return false
}

// backfillGraphStateRow returns re-encrypted graph-state JSON for a legacy row,
// with changed=false when every output value is already ciphertext. Pure.
func backfillGraphStateRow(graphState []byte, enc commandFieldEncryptor) (newState []byte, changed bool) {
	if enc == nil || len(graphState) == 0 {
		return graphState, false
	}
	var state map[string]workflow_execution_node.GraphNodeState
	if err := json.Unmarshal(graphState, &state); err != nil {
		return graphState, false
	}
	needs := false
	for _, ns := range state {
		for _, v := range ns.Outputs {
			if v != "" && !strings.HasPrefix(v, commandCipherPrefix) {
				needs = true
				break
			}
		}
		// Error is checked as its own condition, not folded into the loop
		// above: the common legacy shape is an ERROR-ONLY row — a block that
		// failed before producing any output — and a predicate that only walks
		// Outputs returns changed=false for exactly those rows, forever.
		// Being unprefixed, they are also invisible to rewrapGraphState, so
		// this sweep is the only mechanism that can ever seal them.
		if ns.Error != "" && !strings.HasPrefix(ns.Error, commandCipherPrefix) {
			needs = true
		}
		if needs {
			break
		}
	}
	if !needs {
		return graphState, false
	}
	b, err := json.Marshal(encryptGraphStateValues(state, enc, true))
	if err != nil {
		return graphState, false
	}
	return b, true
}

// backfillNodeCommandEncryption sweeps node_commands once, rewriting legacy
// plaintext secret-bearing values to ciphertext.
func backfillNodeCommandEncryption(db *gorm.DB, enc commandFieldEncryptor) error {
	type row struct {
		ID      uuid.UUID
		Payload *string
		Result  []byte
		Details *string
	}
	var last uuid.UUID
	rewritten := 0
	scanned := 0
	for {
		var rows []row
		q := db.Table("node_commands").
			Select("id, payload, result, details").
			Where("payload IS NOT NULL OR result IS NOT NULL OR details IS NOT NULL").
			Where("id > ?", last).
			Order("id ASC").
			Limit(backfillBatchSize).
			Scan(&rows)
		if q.Error != nil {
			return fmt.Errorf("backfill scan failed: %w", q.Error)
		}
		if len(rows) == 0 {
			break
		}
		for _, r := range rows {
			last = r.ID
			newPayload, newResult, newDetails, changed := backfillCommandRow(r.Payload, r.Result, r.Details, enc)
			if !changed {
				continue
			}
			if err := db.Table("node_commands").Where("id = ?", r.ID).
				Updates(map[string]interface{}{"payload": newPayload, "result": newResult, "details": newDetails}).Error; err != nil {
				return fmt.Errorf("backfill update failed for %s: %w", r.ID, err)
			}
			rewritten++
		}
		scanned += len(rows)
		if len(rows) < backfillBatchSize {
			break
		}
	}
	if rewritten > 0 {
		slog.Info("encrypted legacy node-command rows", "rows", rewritten)
	}
	// The cost of this sweep is visible rather than implicit. It walks every
	// command ever issued on every boot, and node_commands has no retention
	// so this number only grows — when it gets uncomfortable the answer
	// is retention on the table, not a quieter sweep.
	if scanned >= backfillBatchSize {
		slog.Info("legacy node-command encryption sweep completed", "rows_scanned", scanned, "rows_rewritten", rewritten)
	}
	return nil
}

// backfillGraphStateEncryption sweeps workflow_execution_nodes once, rewriting
// legacy plaintext block outputs AND error text to ciphertext.
func backfillGraphStateEncryption(db *gorm.DB, enc commandFieldEncryptor) error {
	type row struct {
		ID         uuid.UUID
		GraphState []byte
	}
	var last uuid.UUID
	rewritten := 0
	for {
		var rows []row
		q := db.Table("workflow_execution_nodes").
			Select("id, graph_state").
			Where("graph_state IS NOT NULL").
			Where("id > ?", last).
			Order("id ASC").
			Limit(backfillBatchSize).
			Scan(&rows)
		if q.Error != nil {
			return fmt.Errorf("graph-state backfill scan failed: %w", q.Error)
		}
		if len(rows) == 0 {
			break
		}
		for _, r := range rows {
			last = r.ID
			newState, changed := backfillGraphStateRow(r.GraphState, enc)
			if !changed {
				continue
			}
			if err := db.Table("workflow_execution_nodes").Where("id = ?", r.ID).
				Update("graph_state", newState).Error; err != nil {
				return fmt.Errorf("graph-state backfill update failed for %s: %w", r.ID, err)
			}
			rewritten++
		}
		if len(rows) < backfillBatchSize {
			break
		}
	}
	if rewritten > 0 {
		slog.Info("encrypted legacy workflow graph-state rows", "rows", rewritten)
	}
	return nil
}
