package cdnsign

import (
	"strings"
	"time"

	"github.com/google/uuid"
)

// The per-device EEPROM package capability.
//
// The package a signer bakes for a node embeds that node's adoption token
// (UPTIME_TOKEN: node_id + unique_token), so whoever downloads it can connect
// AS that node. The capability therefore binds the account and the node it was
// minted for, not only the serial directory: a short (8-hex) serial is not
// unique across devices and is exempt from the cross-account serial guard, so
// two accounts can both bake for the same serial, and a capability bound to the
// serial path alone would open whichever package sat there last, possibly
// another account's credential.
//
// The URL the agent and the browser fetch keeps its shape,
// /net_install/eeprom/by-serial/<serial>/<file>?exp=&sig=, because a released
// agent builds it from the serial and appends the two values verbatim. The
// binding therefore travels INSIDE the sig value — "<account>.<node>.<hmac>",
// URL-safe as is — and the HMAC is the same cdnsign HMAC over the extended
// name "by-serial/<serial>/<account>/<node>". The handler serves the file from
// by-serial/<serial>/<node>/, the directory the signer writes for that node,
// so the object served is always the package of the node the capability names.

const eepromTokenSep = "."

// EEPROMPackageName is the signed name for a node's per-device package.
func EEPROMPackageName(serial string, accountID, nodeID uuid.UUID) string {
	return "by-serial/" + serial + "/" + accountID.String() + "/" + nodeID.String()
}

// SignEEPROMPackage mints the (exp, sig) pair the agent and the WebUSB flasher
// present to download the package baked for nodeID.
func SignEEPROMPackage(serial string, accountID, nodeID uuid.UUID, key []byte, ttl time.Duration) (expStr, sig string) {
	exp, mac := Sign(EEPROMPackageName(serial, accountID, nodeID), key, ttl)
	return exp, accountID.String() + eepromTokenSep + nodeID.String() + eepromTokenSep + mac
}

// VerifyEEPROMPackage checks a capability presented for serial and returns the
// account and node it was minted for. Any malformed, expired, path-only
// (pre-binding) or foreign value is ErrInvalidSignature.
func VerifyEEPROMPackage(serial, expStr, sig string, key []byte) (accountID, nodeID uuid.UUID, err error) {
	parts := strings.Split(sig, eepromTokenSep)
	if len(parts) != 3 {
		return uuid.Nil, uuid.Nil, ErrInvalidSignature
	}
	accountID, aerr := uuid.Parse(parts[0])
	nodeID, nerr := uuid.Parse(parts[1])
	if aerr != nil || nerr != nil || accountID == uuid.Nil || nodeID == uuid.Nil {
		return uuid.Nil, uuid.Nil, ErrInvalidSignature
	}
	// uuid.Parse accepts several spellings; only the canonical one was signed.
	if parts[0] != accountID.String() || parts[1] != nodeID.String() {
		return uuid.Nil, uuid.Nil, ErrInvalidSignature
	}
	if err := Verify(EEPROMPackageName(serial, accountID, nodeID), expStr, parts[2], key); err != nil {
		return uuid.Nil, uuid.Nil, err
	}
	return accountID, nodeID, nil
}
