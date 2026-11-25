package eepromsigner

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func signerReturning(t *testing.T, path func(SignRequest) string) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req SignRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		_ = json.NewEncoder(w).Encode(SignResponse{OK: true, Serial: req.Serial, Path: path(req)})
	}))
	t.Cleanup(srv.Close)
	return NewClient(srv.URL, "")
}

// A signer that predates node-scoped packages ignores node_id and writes into
// the shared serial directory, which the download handler never serves. The
// bake must fail there, by name, rather than dispatch a flash nothing can fetch.
func TestSignRefusesASignerThatIgnoredTheNode(t *testing.T) {
	const node = "6f0d2f2e-6a4e-4b8e-9d55-0b1f8f3b8d11"
	old := signerReturning(t, func(r SignRequest) string { return "eeprom/by-serial/" + r.Serial })
	_, err := old.Sign(context.Background(), SignRequest{SoC: "cm4", Serial: "abcdef12", AdoptionB64: "YWRvcHQ=", NodeID: node})
	if err == nil || !strings.Contains(err.Error(), "redeployed") {
		t.Fatalf("stale signer accepted: %v", err)
	}

	current := signerReturning(t, func(r SignRequest) string { return NodeScopedPath(r.Serial, r.NodeID) })
	resp, err := current.Sign(context.Background(), SignRequest{SoC: "cm4", Serial: "abcdef12", AdoptionB64: "YWRvcHQ=", NodeID: node})
	if err != nil || resp.Path != "eeprom/by-serial/abcdef12/"+node {
		t.Fatalf("node-scoped signer: %+v, %v", resp, err)
	}
}

// The adoption blob becomes the UPTIME_TOKEN line of the signed bootloader
// configuration; a line break in it would add configuration lines under the
// platform's signature. Anything but padded base64 of a bounded length is
// refused before the signer is called.
func TestSignRefusesAMalformedAdoptionBlob(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		_ = json.NewEncoder(w).Encode(SignResponse{OK: true})
	}))
	t.Cleanup(srv.Close)
	c := NewClient(srv.URL, "")
	for _, blob := range []string{
		"",
		"YWRvcHQ=\nSIGNED_BOOT=1",
		"YWRv\r\ncHQ=",
		"YWRvcHQ",
		"YW Rv",
		"YW=v",
		strings.Repeat("A", 1540),
	} {
		_, err := c.Sign(context.Background(), SignRequest{SoC: "cm4", Serial: "abcdef12", AdoptionB64: blob})
		if err == nil {
			t.Errorf("adoption blob %q accepted", blob[:min(len(blob), 20)])
		}
	}
	if called {
		t.Fatal("the signer was called with a malformed adoption blob")
	}
	if _, err := c.Sign(context.Background(), SignRequest{SoC: "cm4", Serial: "abcdef12", AdoptionB64: strings.Repeat("QUJD", 384)}); err != nil {
		t.Fatalf("a 1536-character blob was refused: %v", err)
	}
}
