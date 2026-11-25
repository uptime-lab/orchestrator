package node

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
)

// PUT /nodes/:id/watchdog accepts either a named preset or a full
// WatchdogConfig body (the UI's Re-arm re-PUTs the stored desired config).
// The full form works through the embedded struct's promoted JSON fields —
// this pins that binding so a refactor to a named field (which would silently
// stop promoting them) is caught.
func TestWatchdogRequestBindsAFullConfig(t *testing.T) {
	body := `{
		"enabled": true,
		"os_hang": true,
		"watchdog_timeout_sec": 15,
		"agent_health": true,
		"agent_change_sec": 300,
		"network": true,
		"ping_targets": ["10.0.0.1"],
		"ping_count": 3,
		"interval_sec": 10,
		"retry_timeout_sec": 60
	}`
	req := httptest.NewRequest(http.MethodPut, "/", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	c := echo.New().NewContext(req, httptest.NewRecorder())

	var parsed watchdogRequest
	if err := c.Bind(&parsed); err != nil {
		t.Fatalf("bind: %v", err)
	}
	if parsed.Preset != "" {
		t.Errorf("a full-config body must not look like a preset, got %q", parsed.Preset)
	}
	cfg := parsed.WatchdogConfig
	if !cfg.Enabled || !cfg.OSHang || cfg.WatchdogTimeoutSec != 15 ||
		!cfg.AgentHealth || cfg.AgentChangeSec != 300 ||
		!cfg.Network || len(cfg.PingTargets) != 1 || cfg.PingCount != 3 ||
		cfg.IntervalSec != 10 || cfg.RetryTimeoutSec != 60 {
		t.Errorf("full config did not bind: %+v", cfg)
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("the bound config must pass domain validation: %v", err)
	}
}

// The preset form still binds alongside the embedded config fields.
func TestWatchdogRequestBindsAPreset(t *testing.T) {
	req := httptest.NewRequest(http.MethodPut, "/", strings.NewReader(`{"preset": "network", "ping_target": "10.0.0.1"}`))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	c := echo.New().NewContext(req, httptest.NewRecorder())

	var parsed watchdogRequest
	if err := c.Bind(&parsed); err != nil {
		t.Fatalf("bind: %v", err)
	}
	if parsed.Preset != "network" || parsed.PingTarget != "10.0.0.1" {
		t.Errorf("preset form did not bind: %+v", parsed)
	}
}
