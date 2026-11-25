package sql

// BackfillUniqueTokenEncryption encrypts the device tokens stored before the
// marker column existed. Idempotent; wired outside Setup() and non-fatally in
// main.go, the provisioning-password precedent.
func (r *NodeRepository) BackfillUniqueTokenEncryption() (int, error) {
	return backfillDeviceTokens(r.db, r.enc, "nodes", "id")
}

// BackfillUniqueTokenEncryption is the tombstone twin.
func (r *NodeTombstoneRepository) BackfillUniqueTokenEncryption() (int, error) {
	return backfillDeviceTokens(r.db, r.enc, "node_tombstones", "id")
}
