package logging

import (
	"bytes"
	"log/slog"
	"net/http"
	"strings"
	"testing"
)

// The base handler's ReplaceAttr is the backstop for the call site nobody
// reviewed: any log line that hands a header map to slog gets its credential
// headers replaced before the JSON is written, whatever key the caller chose.

func canaryHeaders() http.Header {
	h := http.Header{}
	h.Set("Authorization", "Bearer CANARY-A")
	h.Set("Proxy-Authorization", "Basic CANARY-D")
	h.Set("Cookie", "oidc_state=CANARY-B; oidc_nonce=CANARY-E")
	h.Set("Set-Cookie", "oidc_link=CANARY-F")
	h.Set("X-Reauth-Ticket", "CANARY-C")
	h.Set("Origin", "https://evil.example")
	h.Set("User-Agent", "canary-agent/1.0")
	return h
}

func canaries() []string {
	return []string{"CANARY-A", "CANARY-B", "CANARY-C", "CANARY-D", "CANARY-E", "CANARY-F"}
}

func TestRedactHeaderAttrHidesCredentialHeadersInHandlerOutput(t *testing.T) {
	var out bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&out, &slog.HandlerOptions{ReplaceAttr: RedactHeaderAttr}))

	logger.Info("request rejected", slog.Any("headers", canaryHeaders()))

	logged := out.String()
	for _, c := range canaries() {
		if strings.Contains(logged, c) {
			t.Errorf("credential header value reached the sink (%s):\n%s", c, logged)
		}
	}
	if !strings.Contains(logged, RedactedHeaderValue) {
		t.Errorf("redacted headers must be marked, not silently dropped:\n%s", logged)
	}
	// Harmless headers keep their values — the line has to stay useful.
	if !strings.Contains(logged, "https://evil.example") || !strings.Contains(logged, "canary-agent/1.0") {
		t.Errorf("non-credential headers must survive redaction:\n%s", logged)
	}
}

func TestRedactHeaderAttrCoversThePlainMapTypeToo(t *testing.T) {
	var out bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&out, &slog.HandlerOptions{ReplaceAttr: RedactHeaderAttr}))

	// A caller that converts http.Header to its underlying type gets the
	// same treatment.
	logger.Info("request rejected", slog.Any("hdr", map[string][]string(canaryHeaders())))

	logged := out.String()
	for _, c := range canaries() {
		if strings.Contains(logged, c) {
			t.Errorf("credential header value reached the sink (%s):\n%s", c, logged)
		}
	}
}

func TestRedactHeaderAttrDoesNotCopyTheCallersMap(t *testing.T) {
	h := canaryHeaders()
	_ = RedactHeaderAttr(nil, slog.Any("headers", h))
	if h.Get("Authorization") != "Bearer CANARY-A" {
		t.Fatal("redaction must work on a copy — the request's own header map is still in use")
	}
}

func TestRedactHeaderAttrLeavesOtherAttrsAlone(t *testing.T) {
	for _, a := range []slog.Attr{
		slog.String("origin", "https://evil.example"),
		slog.Int("status", 403),
		slog.Bool("has_authorization", true),
		slog.Any("payload", struct{ Name string }{"x"}),
	} {
		got := RedactHeaderAttr(nil, a)
		if !got.Equal(a) {
			t.Errorf("attr %v was altered to %v", a, got)
		}
	}
}
