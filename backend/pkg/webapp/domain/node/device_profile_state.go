package node

import "github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/device_profile"

// Device-profile view types, in domain for the same reason WatchdogState is:
// the NodeService port returns them, and the port cannot import the service
// package that implements it.

// DeviceProfileState is the resolved picture the API returns: what is
// declared (and by which level), what the device has acknowledged, and what a
// scan confirmed. CompatibleProfiles is computed on the platform, not in the
// frontend — board identity already drifted once across four client-side
// copies.
type DeviceProfileState struct {
	Assigned bool
	Equipage device_profile.Equipage
	// Source is the inheritance level that supplied the equipage
	// (organizational_unit.PolicySource as a string: node|ou|root_ou|account).
	Source  string
	Options []device_profile.Option
	// Capabilities the effective equipage grants (gates UI features).
	Capabilities []string
	DesiredHash  string
	AppliedHash  string
	Scan         *device_profile.ScanResult
	// CompatibleProfiles lists board profile ids whose fits accepts this
	// node's detected board kind — the picker's choices.
	CompatibleProfiles []string
	// BoardKind is the node's detected board ("CM4", ...; empty when unknown).
	BoardKind string
	// RebootPending is whether the applied boot config is still waiting for the
	// reboot that activates it. Nil = the platform cannot tell; a caller must
	// then keep offering the reboot rather than hide it.
	RebootPending *bool
}

// DeviceProfilePreview is the confirm dialog's content: exactly what an apply
// would write and run.
type DeviceProfilePreview struct {
	Path       string
	Marker     string
	Content    string
	Hash       string
	SetupKinds []string
	UpToDate   bool // applied hash already matches this render
}

// DeviceProfileScanStatus is a scan poll outcome.
type DeviceProfileScanStatus struct {
	Status string // running | completed | failed
	Detail string
	Scan   *device_profile.ScanResult
}
