package sql

import (
	"fmt"
	"log/slog"
	"strings"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// rewrapEncryptor is the crypto surface the key-rotation rewrap needs: decrypt
// with any accepted key, re-encrypt under the primary, and test which key a
// ciphertext is already on.
type rewrapEncryptor interface {
	Rewrap(ciphertext string) (string, error)
	EncryptedWithPrimary(ciphertext string) bool
	Encrypt(plaintext string) (string, error)
	Decrypt(ciphertext string) (string, error)
}

// RewrapEncryptedData re-encrypts every encrypted column under the current
// PRIMARY key (SECRETS_ENCRYPTION_KEY), decrypting with the primary or any
// SECRETS_ENCRYPTION_KEY_OLD fallback. Idempotent: a value already readable by
// the primary alone is left untouched, so re-running is free. Runs at startup
// only when SECRETS_ENCRYPTION_REWRAP is set, before the servers accept
// traffic, so it cannot race live writes.
//
// After a successful rewrap the operator can drop the old key
// from SECRETS_ENCRYPTION_KEY_OLD and unset SECRETS_ENCRYPTION_REWRAP.
func RewrapEncryptedData(db *gorm.DB, enc rewrapEncryptor) error {
	targets := encryptedTargets()

	// A rewrap failure must NOT be fatal. The old key stays in the fallback set
	// until the operator removes it, so unrewrapped rows still decrypt and the
	// platform serves correctly; aborting startup instead turns a partial
	// rewrap (one wrong PK column is enough) into a crash-loop. Errors are
	// collected, logged, and reported so the operator can
	// re-run the one-shot after fixing the cause.
	var failures []string
	for _, target := range targets {
		if err := target.Rewrap(db, enc); err != nil {
			slog.Error("rewrap failed for target; leaving it on the old key", "target", target.Label, "error", err)
			failures = append(failures, fmt.Sprintf("%s: %v", target.Label, err))
		}
	}
	if len(failures) > 0 {
		return fmt.Errorf("rewrap incomplete (%d of %d targets failed): %s — KEEP the old key in SECRETS_ENCRYPTION_KEY_OLD and re-run",
			len(failures), len(targets), strings.Join(failures, "; "))
	}
	return nil
}

// encryptedTarget is one place where ciphertext is stored at rest, declared
// together with BOTH operations that must know about it: the rewrap that moves it
// onto the primary key, and the coverage check that answers "can the old key be
// dropped now".
//
// They are one declaration on purpose. They used to be two hand-maintained lists
// in two files and they drifted: the rewrap covered eleven targets and the
// verifier nine, missing node_commands.payload/result and
// workflow_execution_nodes.graph_state. The consequence was not merely an
// incomplete report — a single boot could print "values still depend on an old
// key" and "the old key can be dropped" from the two paths, and the all-clear is
// the one an operator acts on. Dropping the key then leaves queued commands whose
// parameters cannot be decrypted, and decryptCommandPayload DELETES keys it
// cannot read, so such a command is dispatched with fields silently missing
// rather than failing.
type encryptedTarget struct {
	// Label is what both the coverage report and the failure message call it.
	Label string
	// Rewrap moves every value in this target onto the primary key.
	Rewrap func(*gorm.DB, rewrapEncryptor) error
	// Coverage classifies every value in this target by which key can read it.
	Coverage func(*gorm.DB, rewrapEncryptor, CoverageScope) KeyCoverage
}

// encryptedStringColumn is a column holding exactly one ciphertext per row.
//
// extraWhere restricts the set to the rows that ARE ciphertext, for tables that
// also hold legacy plaintext under a marker column. The gate is not cosmetic on
// either side: for the rewrap, a plaintext row reaches Rewrap, fails, and counts
// as skipped, which reports an incomplete rotation forever; for the coverage
// report, it counts as Unreadable, which spends the ENCRYPTION KEY MISMATCH
// alarm — the one signal that catches a restore with the wrong key — on every
// boot of a healthy install.
//
// batch is how many rows one page of a sweep over this column holds. It belongs
// to the column because the right number follows from the SIZE of one value and
// not from the number of rows: a tracked file version is capped at 512 KiB, so a
// 500-row page of node_files.content is hundreds of megabytes, most of a small
// container's memory limit, while 500 secret values are nothing. The literals below are
// unkeyed, so a new column has to state one.
type encryptedStringColumn struct {
	table, pk, col, extraWhere string
	batch                      int
}

// encryptedStringColumns is the whole-value ciphertext inventory: one ciphertext
// per row, rewrapped and classified generically.
func encryptedStringColumns() []encryptedStringColumn {
	return []encryptedStringColumn{
		{"secrets", "id", "value_encrypted", "", backfillBatchSize},
		{"access_entries", "id", "password_encrypted", "", backfillBatchSize},
		{"ssh_keys", "id", "private_key_encrypted", "", backfillBatchSize},
		{"two_factors", "user_id", "secret", "", backfillBatchSize},
		{"nodes", "id", "default_password", "default_password_enc = 1", backfillBatchSize},
		{"organizational_units", "id", "default_password", "default_password_enc = 1", backfillBatchSize},
		{"account_policies", "account_id", "default_password", "default_password_enc = 1", backfillBatchSize},
		{"master_tokens", "id", "default_password", "default_password_enc = 1", backfillBatchSize},
		{"nodes", "id", "unique_token", "unique_token_enc = 1", backfillBatchSize},
		{"node_tombstones", "id", "unique_token", "unique_token_enc = 1", backfillBatchSize},
	}
}

// nodeFilesContentColumn is a whole-value ciphertext like the columns above, but
// it holds tracked-file versions, so it sweeps in the large-row batch and keeps
// its own rewrap: see rewrapNodeFiles.
func nodeFilesContentColumn() encryptedStringColumn {
	return encryptedStringColumn{"node_files", "id", "content", "content_enc = 1", contentBackfillBatchSize}
}

// encryptedTargets is the whole inventory of at-rest ciphertext. Eleven targets.
func encryptedTargets() []encryptedTarget {
	stringCols := encryptedStringColumns()

	targets := make([]encryptedTarget, 0, len(stringCols)+3)
	for _, c := range stringCols {
		c := c
		targets = append(targets, encryptedTarget{
			Label: c.table + "." + c.col,
			Rewrap: func(db *gorm.DB, enc rewrapEncryptor) error {
				return rewrapStringColumn(db, enc, c)
			},
			Coverage: func(db *gorm.DB, enc rewrapEncryptor, scope CoverageScope) KeyCoverage {
				return stringColumnCoverage(db, enc, c, scope)
			},
		})
	}

	nodeFiles := nodeFilesContentColumn()

	return append(targets,
		encryptedTarget{
			Label: "node_files.content",
			Rewrap: func(db *gorm.DB, enc rewrapEncryptor) error {
				return rewrapNodeFiles(db, enc, nodeFiles.batch)
			},
			Coverage: func(db *gorm.DB, enc rewrapEncryptor, scope CoverageScope) KeyCoverage {
				return stringColumnCoverage(db, enc, nodeFiles, scope)
			},
		},
		// JSONB blobs whose secret-bearing VALUES carry the encv1: prefix. Not
		// stringCols: one row holds many tokens, and the surrounding JSON must
		// never be reinterpreted.
		encryptedTarget{
			Label:  "node_commands.payload/result/details",
			Rewrap: rewrapNodeCommands,
			Coverage: func(db *gorm.DB, enc rewrapEncryptor, scope CoverageScope) KeyCoverage {
				return tokenColumnCoverage(db, enc, "node_commands", "id",
					[]string{"payload", "result", "details"},
					"payload IS NOT NULL OR result IS NOT NULL OR details IS NOT NULL", scope)
			},
		},
		encryptedTarget{
			Label:  "workflow_execution_nodes.graph_state",
			Rewrap: rewrapGraphState,
			Coverage: func(db *gorm.DB, enc rewrapEncryptor, scope CoverageScope) KeyCoverage {
				return tokenColumnCoverage(db, enc, "workflow_execution_nodes", "id",
					[]string{"graph_state"}, "graph_state IS NOT NULL", scope)
			},
		},
	)
}

// rewrapStringColumn rewraps one ciphertext-bearing text column, keyset-batched
// by that table's primary key (which is not always "id" — two_factors is keyed
// on user_id) in the column's own page size.
//
// extraWhere narrows the set to the rows that ARE ciphertext, for columns whose
// table also holds legacy plaintext (a marker column says which). Without it
// those rows reach Rewrap, fail, and count as skipped — and a skipped row is
// reported as an incomplete rewrap, which blocks dropping the old key forever.
func rewrapStringColumn(db *gorm.DB, enc rewrapEncryptor, c encryptedStringColumn) error {
	table, pk, col := c.table, c.pk, c.col
	type row struct {
		PK  uuid.UUID
		Val string
	}
	var last uuid.UUID
	rewritten := 0
	skipped := 0
	for {
		var rows []row
		q := db.Table(table).
			Select(fmt.Sprintf("%s AS pk, %s AS val", pk, col)).
			Where(fmt.Sprintf("%s IS NOT NULL AND %s <> ''", col, col)).
			Where(fmt.Sprintf("%s > ?", pk), last).
			Order(fmt.Sprintf("%s ASC", pk)).
			Limit(c.batch)
		if c.extraWhere != "" {
			q = q.Where(c.extraWhere)
		}
		q = q.Scan(&rows)
		if q.Error != nil {
			return q.Error
		}
		if len(rows) == 0 {
			break
		}
		for _, r := range rows {
			last = r.PK
			if enc.EncryptedWithPrimary(r.Val) {
				continue // already on the new key
			}
			newVal, err := enc.Rewrap(r.Val)
			if err != nil {
				// Leave the row on its current key and keep going: one
				// undecryptable value must not stop the rest of the column, and
				// it must never be overwritten with anything.
				slog.Error("skipping row rewrap; it stays on an older key",
					"table", table, "column", col, "pk", r.PK, "error", err)
				skipped++
				continue
			}
			if err := db.Table(table).Where(fmt.Sprintf("%s = ?", pk), r.PK).Update(col, newVal).Error; err != nil {
				return fmt.Errorf("update row %s: %w", r.PK, err)
			}
			rewritten++
		}
		if len(rows) < c.batch {
			break
		}
	}
	if rewritten > 0 {
		slog.Info("rewrapped encrypted column to the new key", "table", table, "column", col, "rows", rewritten)
	}
	if skipped > 0 {
		return fmt.Errorf("%d row(s) in %s.%s could not be rewrapped and remain on an older key", skipped, table, col)
	}
	return nil
}

// rewrapTokensIn re-encrypts every "encv1:<ciphertext>" token in a JSON blob by
// STRING SUBSTITUTION, leaving every other byte untouched.
//
// It deliberately does NOT go through the model round-trip (ToDomain/FromDomain).
// Those decrypt for READING, where an undecryptable value is dropped so callers
// see absence instead of ciphertext — correct for a read, catastrophic for a
// rewrap: it would silently DELETE any value the current keys cannot decrypt and
// then report success. Unmarshalling would also rewrite (or null) a payload whose
// JSON shape the model does not model.
//
// Returns changed=false when nothing needed rewrapping, and an error — with the
// blob UNMODIFIED — if any token fails, so the caller can skip the row and keep
// the old key.
func rewrapTokensIn(blob string, enc rewrapEncryptor) (string, bool, error) {
	tokens := extractCipherTokens(blob)
	if len(tokens) == 0 {
		return blob, false, nil
	}
	// Deduplicate: the same ciphertext can legitimately appear twice.
	seen := make(map[string]string, len(tokens))
	for _, tok := range tokens {
		if _, done := seen[tok]; done {
			continue
		}
		// Strip the storage marker before asking the crypto layer anything: it
		// understands "v1:"-prefixed and bare ciphertext, not "encv1:".
		inner := strings.TrimPrefix(tok, commandCipherPrefix)
		if enc.EncryptedWithPrimary(inner) {
			continue // already on the new key
		}
		fresh, err := enc.Rewrap(inner)
		if err != nil {
			return blob, false, fmt.Errorf("token could not be re-encrypted (leaving the row on its current key): %w", err)
		}
		seen[tok] = commandCipherPrefix + fresh
	}
	if len(seen) == 0 {
		return blob, false, nil
	}
	out := blob
	for old, fresh := range seen {
		out = strings.ReplaceAll(out, old, fresh)
	}
	return out, true, nil
}

// rewrapNodeCommands rewraps the encv1: values in node_commands
// payload/result/details in place, without reinterpreting the surrounding JSON.
func rewrapNodeCommands(db *gorm.DB, enc rewrapEncryptor) error {
	var last uuid.UUID
	rewritten := 0
	skipped := 0
	for {
		var models []NodeCommandModel
		q := db.Where("(payload IS NOT NULL OR result IS NOT NULL OR details IS NOT NULL) AND id > ?", last).
			Order("id ASC").Limit(backfillBatchSize).Find(&models)
		if q.Error != nil {
			return q.Error
		}
		if len(models) == 0 {
			break
		}
		for i := range models {
			m := &models[i]
			last = m.ID

			cols := map[string]interface{}{}
			if m.Payload != nil && *m.Payload != "" {
				out, changed, err := rewrapTokensIn(*m.Payload, enc)
				if err != nil {
					slog.Error("skipping node-command payload rewrap", "command_id", m.ID, "error", err)
					skipped++
					continue
				}
				if changed {
					cols["payload"] = out
				}
			}
			if len(m.Result) > 0 {
				out, changed, err := rewrapTokensIn(string(m.Result), enc)
				if err != nil {
					slog.Error("skipping node-command result rewrap", "command_id", m.ID, "error", err)
					skipped++
					continue
				}
				if changed {
					cols["result"] = []byte(out)
				}
			}
			if m.Details != nil && *m.Details != "" {
				// Details is a whole-value ciphertext, but it carries the same
				// encv1: prefix as the payload's values precisely so this walk
				// finds it — that is why it needed no target of its own.
				out, changed, err := rewrapTokensIn(*m.Details, enc)
				if err != nil {
					slog.Error("skipping node-command details rewrap", "command_id", m.ID, "error", err)
					skipped++
					continue
				}
				if changed {
					cols["details"] = out
				}
			}
			if len(cols) == 0 {
				continue
			}
			// UpdateColumns, not Updates: this is a re-encryption, not a business
			// change, and bumping updated_at would make freshly rewrapped commands
			// look recently active to the abandoned-command reaper.
			if err := db.Model(&NodeCommandModel{}).Where("id = ?", m.ID).
				UpdateColumns(cols).Error; err != nil {
				return fmt.Errorf("update command %s: %w", m.ID, err)
			}
			rewritten++
		}
		if len(models) < backfillBatchSize {
			break
		}
	}
	if rewritten > 0 {
		slog.Info("rewrapped node-command rows to the new key", "rows", rewritten)
	}
	if skipped > 0 {
		// Loud and non-silent: these rows are still on an older key, so the
		// operator must NOT drop it yet.
		return fmt.Errorf("%d node-command row(s) could not be rewrapped and remain on an older key", skipped)
	}
	return nil
}

// rewrapGraphState rewraps encv1: outputs in workflow_execution_nodes.graph_state.
func rewrapGraphState(db *gorm.DB, enc rewrapEncryptor) error {
	var last uuid.UUID
	rewritten := 0
	skipped := 0
	for {
		var models []WorkflowExecutionNodeModel
		q := db.Where("graph_state IS NOT NULL AND id > ?", last).
			Order("id ASC").Limit(backfillBatchSize).Find(&models)
		if q.Error != nil {
			return q.Error
		}
		if len(models) == 0 {
			break
		}
		for i := range models {
			m := &models[i]
			last = m.ID
			out, changed, err := rewrapTokensIn(string(m.GraphState), enc)
			if err != nil {
				slog.Error("skipping graph-state rewrap", "execution_node_id", m.ID, "error", err)
				skipped++
				continue
			}
			if !changed {
				continue
			}
			if err := db.Model(&WorkflowExecutionNodeModel{}).Where("id = ?", m.ID).
				UpdateColumn("graph_state", []byte(out)).Error; err != nil {
				return fmt.Errorf("update exec node %s: %w", m.ID, err)
			}
			rewritten++
		}
		if len(models) < backfillBatchSize {
			break
		}
	}
	if rewritten > 0 {
		slog.Info("rewrapped workflow graph-state rows to the new key", "rows", rewritten)
	}
	if skipped > 0 {
		return fmt.Errorf("%d graph-state row(s) could not be rewrapped and remain on an older key", skipped)
	}
	return nil
}

// extractCipherTokens pulls every "encv1:<base64>" token out of a JSON string
// so the rewrap can test each against the primary key without parsing the
// specific schema.
func extractCipherTokens(s string) []string {
	var out []string
	const marker = commandCipherPrefix // "encv1:"
	for {
		i := strings.Index(s, marker)
		if i < 0 {
			break
		}
		rest := s[i:]
		// A token runs until the next JSON delimiter (quote, comma, brace).
		end := strings.IndexAny(rest, "\",}] \n\t")
		if end < 0 {
			out = append(out, rest)
			break
		}
		out = append(out, rest[:end])
		s = rest[end:]
	}
	return out
}

// rewrapNodeFiles re-encrypts tracked-file content under the primary key.
//
// It cannot use rewrapStringColumn: that one rewraps every non-empty value, and
// node_files legitimately holds plaintext rows (content_enc = 0) that predate
// encryption — feeding those to Rewrap would fail forever and the operator
// could never see a clean run before dropping an old key. Rows this cannot
// decrypt are left verbatim and counted, never overwritten.
//
// batch comes from the column declaration and is the large-row one: a page here
// is file contents, not JSON payloads.
func rewrapNodeFiles(db *gorm.DB, enc rewrapEncryptor, batch int) error {
	type row struct {
		ID  uuid.UUID
		Val string
	}
	var last uuid.UUID
	rewritten, skipped := 0, 0
	for {
		var rows []row
		if err := db.Table("node_files").
			Select("id AS id, content AS val").
			Where("content_enc = 1 AND content <> ''").
			Where("id > ?", last).
			Order("id ASC").
			Limit(batch).
			Scan(&rows).Error; err != nil {
			return err
		}
		if len(rows) == 0 {
			break
		}
		for _, r := range rows {
			last = r.ID
			if enc.EncryptedWithPrimary(r.Val) {
				continue
			}
			next, err := enc.Rewrap(r.Val)
			if err != nil {
				skipped++
				continue
			}
			if err := db.Table("node_files").Where("id = ?", r.ID).
				Update("content", next).Error; err != nil {
				return err
			}
			rewritten++
		}
	}
	slog.Info("rewrap: node_files.content", "rewritten", rewritten, "skipped", skipped)
	if skipped > 0 {
		return fmt.Errorf("%d node_files rows could not be decrypted by any configured key", skipped)
	}
	return nil
}
