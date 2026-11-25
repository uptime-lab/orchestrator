package sql

import (
	"fmt"
	"log/slog"

	"gorm.io/gorm"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node_file"
)

// deviceTokenEncAES marks a unique_token column value as AES-GCM ciphertext.
// The provisioning-password rules apply verbatim (a COLUMN marker, never a
// value prefix; the converting backfill runs outside Setup and non-fatally),
// with one semantic difference the decrypt path must carry: a token is a
// device CREDENTIAL, not an operator convenience. An unreadable password
// degrades to "unset" and provisioning warns; an unreadable token means the
// device can never authenticate again until the key returns, so the
// unreadable flag must also keep every full-row write from replacing the
// stored ciphertext with an encrypted empty string.
const deviceTokenEncAES = 1

// encryptDeviceToken prepares a unique_token for storage. Empty stays empty
// plaintext (marker 0): "no token" must remain queryable, and encrypting ""
// would also collide with the column's unique index which multiple empty
// ciphertexts do not hit today only because empties are rare.
func encryptDeviceToken(enc Encryptor, plaintext string) (string, int, error) {
	if plaintext == "" {
		return "", 0, nil
	}
	ciphertext, err := enc.Encrypt(plaintext)
	if err != nil {
		return "", 0, fmt.Errorf("failed to encrypt device token: %w", err)
	}
	return ciphertext, deviceTokenEncAES, nil
}

// decryptDeviceToken reverses it, returning the plaintext and whether the
// stored value is UNREADABLE. Not an error to the caller for the same reason
// the password twin is not: node rows are read on every list and every gRPC
// connect, and failing those over one bad row takes the control plane down.
// An empty token with the flag means "set but unrecoverable": the connect
// path refuses an empty or unreadable token outright (a constant-time compare
// of two empty strings would be equal), which is the honest outcome of a lost
// key.
func decryptDeviceToken(enc Encryptor, stored string, marker int, subject string) (string, bool) {
	if stored == "" {
		return "", false
	}
	if marker != deviceTokenEncAES {
		return stored, false // legacy plaintext, migrated by the backfill
	}
	plaintext, err := enc.Decrypt(stored)
	if err != nil {
		slog.Error("stored device token could not be decrypted with any configured key; the device cannot authenticate until the key returns",
			"subject", subject,
			"looks_like_envelope", node_file.LooksLikeStoredCiphertext(stored),
			"error", err)
		return "", true
	}
	return plaintext, false
}

// backfillDeviceTokens encrypts the unique_token values stored before the
// marker column existed, for one table. Idempotent; called outside Setup()
// and non-fatally, the provisioning-password precedent.
func backfillDeviceTokens(db *gorm.DB, enc Encryptor, table, pk string) (int, error) {
	converted := 0
	for {
		var rows []struct {
			PK    string
			Token string
		}
		err := db.Table(table).
			Select(fmt.Sprintf("%s as pk, unique_token as token", pk)).
			Where("unique_token <> '' AND unique_token_enc = 0").
			Limit(backfillBatchSize).
			Scan(&rows).Error
		if err != nil {
			return converted, err
		}
		if len(rows) == 0 {
			return converted, nil
		}
		for _, row := range rows {
			ciphertext, marker, err := encryptDeviceToken(enc, row.Token)
			if err != nil {
				return converted, err
			}
			if err := db.Table(table).
				Where(fmt.Sprintf("%s = ?", pk), row.PK).
				Updates(map[string]interface{}{
					"unique_token":     ciphertext,
					"unique_token_enc": marker,
				}).Error; err != nil {
				return converted, err
			}
			converted++
		}
	}
}
