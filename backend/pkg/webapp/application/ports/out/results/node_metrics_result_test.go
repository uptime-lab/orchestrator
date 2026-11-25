package results

import (
	"strings"
	"testing"

	proto "github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/grpc/proto"
)

func TestNodeMetricsResultTruncatesADeviceReportedSample(t *testing.T) {
	cores := make([]float64, MaxMetricsCPUCores*10)
	for i := range cores {
		cores[i] = float64(i)
	}
	zones := make([]*proto.ThermalZone, MaxMetricsThermalZones*10)
	for i := range zones {
		zones[i] = &proto.ThermalZone{
			Name:        strings.Repeat("z", MaxThermalZoneNameLength*4),
			TempCelsius: 42,
		}
	}

	out := NewNodeMetricsResult(&proto.MetricsUpdate{
		CpuPercentPerCore: cores,
		Thermal:           zones,
	})

	if len(out.CPUPercentCore) != MaxMetricsCPUCores {
		t.Fatalf("per-core samples: got %d, want %d", len(out.CPUPercentCore), MaxMetricsCPUCores)
	}
	if len(out.Thermal) != MaxMetricsThermalZones {
		t.Fatalf("thermal zones: got %d, want %d", len(out.Thermal), MaxMetricsThermalZones)
	}
	for _, z := range out.Thermal {
		if len(z.Name) > MaxThermalZoneNameLength {
			t.Fatalf("thermal zone name: got %d bytes, want at most %d", len(z.Name), MaxThermalZoneNameLength)
		}
	}
	// The kept samples must be the ones the device sent, not zeroes.
	if out.CPUPercentCore[0] != 0 || out.CPUPercentCore[MaxMetricsCPUCores-1] != float64(MaxMetricsCPUCores-1) {
		t.Fatalf("truncation dropped the wrong end: %v", out.CPUPercentCore[:4])
	}
}

func TestNodeMetricsResultDoesNotAliasTheDecodedFrame(t *testing.T) {
	m := &proto.MetricsUpdate{CpuPercentPerCore: []float64{1, 2, 3, 4}}

	out := NewNodeMetricsResult(m)
	m.CpuPercentPerCore[0] = 99

	if out.CPUPercentCore[0] != 1 {
		t.Fatalf("DTO aliases the decoded proto slice: got %v", out.CPUPercentCore[0])
	}
}

func TestNodeMetricsResultKeepsAnUntruncatedSampleIntact(t *testing.T) {
	m := &proto.MetricsUpdate{
		CpuPercentPerCore: []float64{10, 20, 30, 40},
		Thermal:           []*proto.ThermalZone{{Name: "cpu-thermal", TempCelsius: 51.5}},
	}

	out := NewNodeMetricsResult(m)

	if len(out.CPUPercentCore) != 4 || out.CPUPercentCore[2] != 30 {
		t.Fatalf("per-core samples altered: %v", out.CPUPercentCore)
	}
	if len(out.Thermal) != 1 || out.Thermal[0].Name != "cpu-thermal" || out.Thermal[0].TempCelsius != 51.5 {
		t.Fatalf("thermal zone altered: %+v", out.Thermal)
	}
}
