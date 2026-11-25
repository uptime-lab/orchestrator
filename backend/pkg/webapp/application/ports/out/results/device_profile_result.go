package results

import (
	"time"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/device_profile"
	nodeDomain "github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
)

// Device-profile API shapes. The catalog result carries everything the picker
// and the generated forms need, so a new board in the registry appears in the
// UI with no frontend change (the workflow-block registry precedent).

// DeviceProfileCatalogResult is GET /api/v1/device-profiles.
type DeviceProfileCatalogResult struct {
	Boards  []DeviceProfileBoardResult  `json:"boards"`
	Modules []DeviceProfileModuleResult `json:"modules"`
}

type DeviceProfileBoardResult struct {
	ID          string                       `json:"id"`
	Name        string                       `json:"name"`
	Description string                       `json:"description,omitempty"`
	Fits        []string                     `json:"fits,omitempty"`
	Variants    []DeviceProfileVariantResult `json:"variants,omitempty"`
	Fixed       []DeviceProfileOptionDef     `json:"fixed,omitempty"`
	Ports       []DeviceProfilePortResult    `json:"ports,omitempty"`
	// CompatibleModules maps module id -> the board's first port that carries
	// every function the module needs. Computed HERE with the domain's
	// ModuleFitsPort so the pinout dialect has one implementation — the UI
	// filters its module options by this map instead of re-deriving pin
	// functions client-side.
	CompatibleModules map[string]string `json:"compatible_modules,omitempty"`
	// LED: what the board's identify wiring makes possible — the Organization
	// page renders LED controls only for boards that can honor them.
	LED *DeviceProfileBoardLEDResult `json:"led,omitempty"`
}

// DeviceProfileBoardLEDResult is the board-level LED capability summary.
type DeviceProfileBoardLEDResult struct {
	StealthAvailable bool `json:"stealth_available"`
	DigitalLEDs      int  `json:"digital_leds"`
}

type DeviceProfileVariantResult struct {
	ID          string                   `json:"id"`
	Name        string                   `json:"name"`
	Notes       string                   `json:"notes,omitempty"`
	Peripherals []DeviceProfileOptionDef `json:"peripherals,omitempty"`
}

// DeviceProfileOptionDef describes a peripheral or module as the UI renders
// it: what it is, what boot config it needs, and what it grants.
type DeviceProfileOptionDef struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Description  string   `json:"description,omitempty"`
	Overlays     []string `json:"overlays,omitempty"`
	Capabilities []string `json:"capabilities,omitempty"`
	// HasProbe: a scan can confirm this option (drives the ladder's last rung).
	HasProbe bool `json:"has_probe"`
}

type DeviceProfilePortResult struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Pinout      string   `json:"pinout"`
	PinFrom     int      `json:"pin_from"`
	PinTo       int      `json:"pin_to"`
	SharedBuses []string `json:"shared_buses,omitempty"`
}

type DeviceProfileModuleResult struct {
	DeviceProfileOptionDef
	Needs      []string `json:"needs"`
	I2CAddress string   `json:"i2c_address,omitempty"`
}

func optionDefFromPeripheral(p device_profile.Peripheral) DeviceProfileOptionDef {
	return DeviceProfileOptionDef{
		ID: p.ID, Name: p.Name,
		Overlays: p.Overlays, Capabilities: p.Capabilities, HasProbe: !p.Probe.IsZero(),
	}
}

// NewDeviceProfileCatalogResult converts the loaded registry.
func NewDeviceProfileCatalogResult(c *device_profile.Catalog) *DeviceProfileCatalogResult {
	out := &DeviceProfileCatalogResult{
		Boards:  make([]DeviceProfileBoardResult, 0, len(c.Boards)),
		Modules: make([]DeviceProfileModuleResult, 0, len(c.Modules)),
	}
	for _, b := range c.Boards {
		board := DeviceProfileBoardResult{
			ID: b.ID, Name: b.Name, Description: b.Description, Fits: b.Fits,
		}
		for _, v := range b.Variants {
			variant := DeviceProfileVariantResult{ID: v.ID, Name: v.Name, Notes: v.Notes}
			for _, p := range v.Peripherals {
				variant.Peripherals = append(variant.Peripherals, optionDefFromPeripheral(p))
			}
			board.Variants = append(board.Variants, variant)
		}
		for _, p := range b.Fixed {
			board.Fixed = append(board.Fixed, optionDefFromPeripheral(p))
		}
		for _, p := range b.Ports {
			board.Ports = append(board.Ports, DeviceProfilePortResult{
				ID: p.ID, Name: p.Name, Pinout: p.Pinout,
				PinFrom: p.PinFrom, PinTo: p.PinTo, SharedBuses: p.SharedBuses,
			})
		}
		if b.Identify.StealthGPIO > 0 || b.Identify.DigitalLEDs > 0 {
			board.LED = &DeviceProfileBoardLEDResult{
				StealthAvailable: b.Identify.StealthGPIO > 0,
				DigitalLEDs:      b.Identify.DigitalLEDs,
			}
		}
		for _, m := range c.Modules {
			for _, p := range b.Ports {
				if _, ok := device_profile.ModuleFitsPort(m, p); ok {
					if board.CompatibleModules == nil {
						board.CompatibleModules = make(map[string]string)
					}
					board.CompatibleModules[m.ID] = p.ID
					break
				}
			}
		}
		out.Boards = append(out.Boards, board)
	}
	for _, m := range c.Modules {
		out.Modules = append(out.Modules, DeviceProfileModuleResult{
			DeviceProfileOptionDef: DeviceProfileOptionDef{
				ID: m.ID, Name: m.Name, Description: m.Description,
				Overlays: m.Overlays, Capabilities: m.Capabilities, HasProbe: !m.Probe.IsZero(),
			},
			Needs:      m.Needs,
			I2CAddress: m.I2CAddress,
		})
	}
	return out
}

// NodeDeviceProfileResult is GET /api/v1/nodes/:id/device-profile.
type NodeDeviceProfileResult struct {
	Assigned bool `json:"assigned"`
	// Source: node | ou | root_ou | account (present when assigned).
	Source    string                       `json:"source,omitempty"`
	ProfileID string                       `json:"profile_id,omitempty"`
	VariantID string                       `json:"variant_id,omitempty"`
	Modules   []NodeDeviceProfileModuleRef `json:"modules,omitempty"`
	Options   []NodeDeviceProfileOption    `json:"options,omitempty"`
	// Capabilities the effective equipage grants; gates UI sections.
	Capabilities []string `json:"capabilities,omitempty"`
	DesiredHash  string   `json:"desired_hash,omitempty"`
	AppliedHash  string   `json:"applied_hash,omitempty"`
	// CompatibleProfiles: board profile ids the picker may offer this node.
	CompatibleProfiles []string `json:"compatible_profiles"`
	BoardKind          string   `json:"board_kind,omitempty"`
	// HAT fields the last scan reported (spec-compliant HAT ID EEPROM).
	HAT       map[string]string `json:"hat,omitempty"`
	ScannedAt *time.Time        `json:"scanned_at,omitempty"`
	// RebootPending: the applied boot config still needs the reboot that
	// activates it. ABSENT means the platform cannot tell (nothing applied, an
	// edit outstanding, or an agent that reports no uptime) — a client must
	// then keep offering the reboot, never hide it. Deliberately a pointer with
	// omitempty so false and unknown stay distinguishable.
	RebootPending *bool `json:"reboot_pending,omitempty"`
}

type NodeDeviceProfileModuleRef struct {
	ModuleID string `json:"module_id"`
	PortID   string `json:"port_id"`
}

type NodeDeviceProfileOption struct {
	Kind         string   `json:"kind"` // fixed | variant | module
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	PortID       string   `json:"port_id,omitempty"`
	Status       string   `json:"status"` // declared | update_pending | applied | confirmed | not_confirmed
	Capabilities []string `json:"capabilities,omitempty"`
}

// NewNodeDeviceProfileResult converts the resolved state.
func NewNodeDeviceProfileResult(s *nodeDomain.DeviceProfileState) *NodeDeviceProfileResult {
	out := &NodeDeviceProfileResult{
		Assigned:           s.Assigned,
		CompatibleProfiles: s.CompatibleProfiles,
		BoardKind:          s.BoardKind,
	}
	if out.CompatibleProfiles == nil {
		out.CompatibleProfiles = []string{}
	}
	if s.Scan != nil {
		out.HAT = s.Scan.HAT
		at := s.Scan.At
		out.ScannedAt = &at
	}
	if !s.Assigned {
		return out
	}
	out.Source = s.Source
	out.ProfileID = s.Equipage.ProfileID
	out.VariantID = s.Equipage.VariantID
	out.Capabilities = s.Capabilities
	out.DesiredHash = s.DesiredHash
	out.AppliedHash = s.AppliedHash
	out.RebootPending = s.RebootPending
	for _, m := range s.Equipage.Modules {
		out.Modules = append(out.Modules, NodeDeviceProfileModuleRef{ModuleID: m.ModuleID, PortID: m.PortID})
	}
	for _, o := range s.Options {
		out.Options = append(out.Options, NodeDeviceProfileOption{
			Kind: string(o.Kind), ID: o.ID, Name: o.Name, PortID: o.PortID,
			Status: string(o.Status), Capabilities: o.Capabilities,
		})
	}
	return out
}

// DeviceProfilePreviewResult is GET /api/v1/nodes/:id/device-profile/preview —
// the confirm dialog's exact content.
type DeviceProfilePreviewResult struct {
	Path       string   `json:"path"`
	Marker     string   `json:"marker"`
	Content    string   `json:"content"`
	Hash       string   `json:"hash"`
	SetupKinds []string `json:"setup_kinds,omitempty"`
	UpToDate   bool     `json:"up_to_date"`
	// RebootRequired is always true for config.txt changes: the platform never
	// reboots on its own (the overclock rule).
	RebootRequired bool `json:"reboot_required"`
}

// NewDeviceProfilePreviewResult converts the render.
func NewDeviceProfilePreviewResult(p *nodeDomain.DeviceProfilePreview) *DeviceProfilePreviewResult {
	return &DeviceProfilePreviewResult{
		Path: p.Path, Marker: p.Marker, Content: p.Content, Hash: p.Hash,
		SetupKinds: p.SetupKinds, UpToDate: p.UpToDate, RebootRequired: true,
	}
}

// DeviceProfileScanResultDTO is the scan poll response.
type DeviceProfileScanResultDTO struct {
	Status string   `json:"status"` // running | completed | failed
	Detail string   `json:"detail,omitempty"`
	Paths  []string `json:"paths,omitempty"`
	// RTCNames are the registered RTC drivers ("rtc-ds1307 1-0068",
	// "rpi-rtc ..."), the facts RTC module probes match against.
	RTCNames []string `json:"rtc_names,omitempty"`
	// ClockSourceRTC names the RTC the kernel took the system clock FROM
	// (hctosys). Presence and being the clock source are different facts.
	ClockSourceRTC string            `json:"clock_source_rtc,omitempty"`
	HAT            map[string]string `json:"hat,omitempty"`
}

// NewDeviceProfileScanResult converts a poll outcome.
func NewDeviceProfileScanResult(s *nodeDomain.DeviceProfileScanStatus) *DeviceProfileScanResultDTO {
	out := &DeviceProfileScanResultDTO{Status: s.Status, Detail: s.Detail}
	if s.Scan != nil {
		out.Paths = s.Scan.Paths
		out.RTCNames = s.Scan.RTCNames
		out.ClockSourceRTC = s.Scan.ClockSourceRTC
		out.HAT = s.Scan.HAT
	}
	return out
}
