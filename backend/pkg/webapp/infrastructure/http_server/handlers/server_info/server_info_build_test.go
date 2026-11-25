package server_info

import (
	"log/slog"
	"testing"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/config"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/buildinfo"
)

// The capabilities report the platform version and revision the binary was
// stamped with, and an empty version for a build that stamped none, rather
// than a made-up one. The serial lease is the configured value.
func TestCapabilitiesReportTheBuild(t *testing.T) {
	cfg := &config.Config{}
	cfg.Quota.SerialLeaseDays = 30
	h := NewHandler(cfg, false, slog.Default())

	buildinfo.SetVersion("")
	buildinfo.SetRevision("")
	data := getJSON(t, h.GetCapabilities, "/api/v1/server/capabilities")
	if v, ok := data["version"]; !ok || v != "" {
		t.Errorf("version for an unstamped build = %v (present %v), want an empty string", v, ok)
	}
	if _, ok := data["revision"]; ok {
		t.Errorf("revision present for a build that recorded none: %v", data["revision"])
	}
	if data["serial_lease_days"] != float64(30) {
		t.Errorf("serial_lease_days = %v, want 30", data["serial_lease_days"])
	}

	buildinfo.SetVersion("0.1.1")
	buildinfo.SetRevision("0123456789abcdef")
	t.Cleanup(func() {
		buildinfo.SetVersion("")
		buildinfo.SetRevision("")
	})
	data = getJSON(t, h.GetCapabilities, "/api/v1/server/capabilities")
	if data["version"] != "0.1.1" {
		t.Errorf("version = %v, want the stamped 0.1.1", data["version"])
	}
	if data["revision"] != "0123456789abcdef" {
		t.Errorf("revision = %v, want the stamped revision", data["revision"])
	}
}
