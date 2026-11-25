package results

// These mirror the domain node scan DTOs for Swagger. The handler's own Go
// package is named `node`, so a `@Success {object} node.X` annotation cannot
// resolve the domain/node types (swag reads `node.` as the self package). The
// repo convention is to reference response shapes as `results.X`; these types
// carry the same JSON shape the handlers actually return.

// OSUserResult is one OS login account from an inventory scan.
type OSUserResult struct {
	Username string `json:"username"`
	UID      int    `json:"uid"`
	GID      int    `json:"gid"`
	Home     string `json:"home"`
	Shell    string `json:"shell"`
	Managed  bool   `json:"managed"`
}

// OSUserScanResult is the polled outcome of an OS-user scan.
type OSUserScanResult struct {
	Status string         `json:"status"`
	Detail string         `json:"detail,omitempty"`
	Users  []OSUserResult `json:"users,omitempty"`
}

// EEPROMConfigEntryResult is one KEY=VALUE line of the live EEPROM config.
type EEPROMConfigEntryResult struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// EEPROMConfigScanResult is the polled outcome of an EEPROM-config scan.
type EEPROMConfigScanResult struct {
	Status  string                    `json:"status"`
	Detail  string                    `json:"detail,omitempty"`
	Entries []EEPROMConfigEntryResult `json:"entries,omitempty"`
}
