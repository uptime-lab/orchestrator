package sql

import (
	"fmt"
	"log/slog"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node_file"
	"gorm.io/gorm"
)

// Provisioning passwords — nodes, organizational_units, account_policies and
// master_tokens — are encrypted at rest under the same key as every other
// secret. They share this file because they share one contract, and getting the
// four copies subtly out of step is the whole risk.
//
// Storage is marked by a COLUMN (default_password_enc: 0 = legacy plaintext,
// 1 = AES-GCM), not by the "v1:" prefix on the value. Every key writes that same
// prefix, so it identifies the FORMAT and not the key; and a legacy plaintext
// password that happened to start with "v1:" would be misread as ciphertext.
// node_files.content_enc exists for exactly the same reason.
//
// Legacy rows are accepted verbatim on read and converted by the one-shot
// backfill (and by the next write). Nothing here ever overwrites a value it
// could not decrypt: an undecryptable row keeps its bytes so a rotation that
// restores the right key can still recover it.

// provisioningPasswordEncAES is the marker value for an AES-GCM ciphertext.
const provisioningPasswordEncAES = 1

// encryptProvisioningPassword prepares a password for storage, returning the
// value to write and its marker. An empty password is stored as empty plaintext
// (marker 0) — there is nothing to protect and it keeps "unset" queryable.
func encryptProvisioningPassword(enc Encryptor, plaintext string) (string, int, error) {
	if plaintext == "" {
		return "", 0, nil
	}
	ciphertext, err := enc.Encrypt(plaintext)
	if err != nil {
		return "", 0, fmt.Errorf("failed to encrypt provisioning password: %w", err)
	}
	return ciphertext, provisioningPasswordEncAES, nil
}

// decryptProvisioningPassword reverses it, returning the plaintext and whether
// the stored value was UNREADABLE (present, marked encrypted, and decrypting
// with none of the configured keys).
//
// A failure is not an error to the caller, deliberately: these columns are read
// on the node list, the OU tree and every gRPC connect, and failing those over
// one bad row would take the control plane down. The caller gets an empty
// password plus the flag, and the two are read together — an empty password
// means "none set", an empty password with the flag means "set but
// unrecoverable". Only the provisioning dispatch path turns the flag into a
// refusal, which is where it actually matters.
func decryptProvisioningPassword(enc Encryptor, stored string, marker int, subject string) (string, bool) {
	if stored == "" {
		return "", false
	}
	if marker != provisioningPasswordEncAES {
		return stored, false // legacy plaintext, migrated on the next write
	}
	plaintext, err := enc.Decrypt(stored)
	if err != nil {
		// looks_like_envelope separates the two causes an operator has to act on
		// differently: true means a key problem (wrong or dropped
		// SECRETS_ENCRYPTION_KEY), false means the row is marked encrypted but
		// holds something else — what a backend built without this code writes
		// if it is ever deployed over these rows.
		slog.Error("stored provisioning password could not be decrypted with any configured key; treating it as unset",
			"subject", subject,
			"looks_like_envelope", node_file.LooksLikeStoredCiphertext(stored),
			"error", err)
		return "", true
	}
	return plaintext, false
}

// backfillProvisioningPasswords encrypts the rows written before the marker
// column existed, for one table. Idempotent — it only touches rows still marked
// plaintext, so re-running is free and a partial run resumes.
//
// Called separately from Setup() and non-fatally on purpose: every repository's
// Setup() is fatal in main.go, and crash-looping a live install over a hygiene
// backfill is not an acceptable failure mode.
func backfillProvisioningPasswords(db *gorm.DB, enc Encryptor, table, pk string) (int, error) {
	if enc == nil {
		return 0, nil
	}
	type row struct {
		PK  string
		Val string
	}
	converted := 0
	for {
		var rows []row
		if err := db.Table(table).
			Select(fmt.Sprintf("%s::text AS pk, default_password AS val", pk)).
			Where("default_password_enc = 0 AND default_password IS NOT NULL AND default_password <> ''").
			Limit(backfillBatchSize).
			Scan(&rows).Error; err != nil {
			return converted, err
		}
		if len(rows) == 0 {
			return converted, nil
		}
		batchWrote := int64(0)
		for _, rw := range rows {
			ciphertext, err := enc.Encrypt(rw.Val)
			if err != nil {
				return converted, fmt.Errorf("encrypting %s.%s: %w", table, rw.PK, err)
			}
			// The marker is part of the WHERE so a row a concurrent writer already
			// encrypted cannot be double-wrapped by this loop.
			res := db.Table(table).
				Where(fmt.Sprintf("%s::text = ? AND default_password_enc = 0", pk), rw.PK).
				Updates(map[string]interface{}{
					"default_password":     ciphertext,
					"default_password_enc": provisioningPasswordEncAES,
				})
			if res.Error != nil {
				return converted, res.Error
			}
			batchWrote += res.RowsAffected
			converted += int(res.RowsAffected)
		}
		// The loop terminates because every write clears the row's marker and so
		// drops it out of the next SELECT. If a whole batch wrote nothing, that
		// invariant does not hold and the next iteration would fetch the same rows
		// forever — stop and report instead of spinning at startup.
		if batchWrote == 0 {
			return converted, fmt.Errorf("%s: a batch of %d row(s) matched the backfill but none could be updated", table, len(rows))
		}
	}
}
