package results

import (
	"strings"

	proto "github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/grpc/proto"
)

// Per-sample ceilings on what a device may make the platform fan out. A sample
// is device-reported and arrives every few seconds on a stream the platform
// never rate-limits, and each one is JSON-marshalled to every browser watching
// the account — so the only other bound is the 8 MiB gRPC frame, which is three
// orders of magnitude above any real board. 128 cores and 32 zones are generous
// for the hardware this manages; a sample beyond them is truncated, not refused,
// because dropping it would blind the UI over a field nothing reads closely.
const (
	MaxMetricsCPUCores       = 128
	MaxMetricsThermalZones   = 32
	MaxThermalZoneNameLength = 64
)

// NodeThermalZone is one temperature sensor reading.
type NodeThermalZone struct {
	Name        string  `json:"name"`
	TempCelsius float64 `json:"temp_celsius"`
}

// NodeMetricsResult is the API/WS DTO for a live resource-utilization sample.
type NodeMetricsResult struct {
	Timestamp      int64             `json:"timestamp"`
	CPUPercent     float64           `json:"cpu_percent"`
	CPUPercentCore []float64         `json:"cpu_percent_per_core,omitempty"`
	MemTotalBytes  uint64            `json:"mem_total_bytes"`
	MemUsedBytes   uint64            `json:"mem_used_bytes"`
	MemAvailBytes  uint64            `json:"mem_available_bytes"`
	LoadAvg1       float64           `json:"load_avg_1"`
	LoadAvg5       float64           `json:"load_avg_5"`
	LoadAvg15      float64           `json:"load_avg_15"`
	Thermal        []NodeThermalZone `json:"thermal,omitempty"`
}

// NewNodeMetricsResult maps a proto MetricsUpdate to its DTO, truncated to the
// per-sample ceilings above.
func NewNodeMetricsResult(m *proto.MetricsUpdate) NodeMetricsResult {
	thermal := m.Thermal
	if len(thermal) > MaxMetricsThermalZones {
		thermal = thermal[:MaxMetricsThermalZones]
	}
	zones := make([]NodeThermalZone, 0, len(thermal))
	for _, z := range thermal {
		name := z.Name
		if len(name) > MaxThermalZoneNameLength {
			// A byte cut can split a rune; drop the remains rather than emit
			// replacement characters into the browser's payload.
			name = strings.ToValidUTF8(name[:MaxThermalZoneNameLength], "")
		}
		zones = append(zones, NodeThermalZone{Name: name, TempCelsius: z.TempCelsius})
	}

	// Copied, never aliased: the decoded slice backs the received frame, so
	// retaining it would keep the whole frame alive for as long as the DTO is.
	var perCore []float64
	if n := len(m.CpuPercentPerCore); n > 0 {
		if n > MaxMetricsCPUCores {
			n = MaxMetricsCPUCores
		}
		perCore = make([]float64, n)
		copy(perCore, m.CpuPercentPerCore[:n])
	}

	return NodeMetricsResult{
		Timestamp:      m.Timestamp,
		CPUPercent:     m.CpuPercent,
		CPUPercentCore: perCore,
		MemTotalBytes:  m.MemTotalBytes,
		MemUsedBytes:   m.MemUsedBytes,
		MemAvailBytes:  m.MemAvailableBytes,
		LoadAvg1:       m.LoadAvg_1,
		LoadAvg5:       m.LoadAvg_5,
		LoadAvg15:      m.LoadAvg_15,
		Thermal:        zones,
	}
}

// NodeMetricsEvent is the WebSocket payload pushed to the UI per sample.
type NodeMetricsEvent struct {
	NodeID  string            `json:"node_id"`
	Metrics NodeMetricsResult `json:"metrics"`
}
