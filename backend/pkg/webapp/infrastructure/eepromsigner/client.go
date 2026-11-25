// Package eepromsigner is a thin HTTP client for the isolated eeprom-signer
// service, which produces per-device signed EEPROM images (network-first
// Variant B). The signer holds the private key; the backend only asks it to sign.
package eepromsigner

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// Client calls the eeprom-signer POST /sign endpoint.
type Client struct {
	baseURL string
	token   string
	http    *http.Client
}

// NewClient builds a signer client. baseURL is e.g. http://eeprom-signer:9099.
func NewClient(baseURL, token string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		token:   token,
		// Signing shells out to the rpi-eeprom tools; allow generous headroom.
		http: &http.Client{Timeout: 130 * time.Second},
	}
}

// SignRequest asks the signer to bake a per-device EEPROM image.
type SignRequest struct {
	SoC         string `json:"soc"`          // cm4 | cm5
	Serial      string `json:"serial"`       // baked into HTTP_PATH=http-boot/<serial>
	AdoptionB64 string `json:"adoption_b64"` // -> UPTIME_TOKEN
	HTTPHost    string `json:"http_host,omitempty"`
	BootOrder   string `json:"boot_order,omitempty"`
	// NodeID makes the signer write the package into the node's OWN
	// directory, by-serial/<serial>/<node_id>/, which is the only place the
	// download handler serves a per-device package from (the capability names
	// the node; see cdnsign.SignEEPROMPackage). A short serial is shared by
	// unrelated devices, so a package keyed on the serial alone could be another
	// account's.
	NodeID string `json:"node_id,omitempty"`
}

// NodeScopedPath is where a package baked for nodeID lives, relative to the
// boot-serve root.
func NodeScopedPath(serial, nodeID string) string {
	return "eeprom/by-serial/" + serial + "/" + nodeID
}

// SignResponse is the signer's reply; Path is relative to the boot-serve root.
type SignResponse struct {
	OK        bool     `json:"ok"`
	Serial    string   `json:"serial"`
	SoC       string   `json:"soc"`
	BootOrder string   `json:"boot_order"`
	Path      string   `json:"path"`
	Files     []string `json:"files"`
	Error     string   `json:"error"`
}

// MaxAdoptionB64Len bounds the adoption blob: the bootloader configuration has
// a few kilobytes in all, and the blob the node service builds stays near half
// of this even with maximum-length host names.
const MaxAdoptionB64Len = 1536

// adoptionB64RE is padded standard base64: the alphabet, then at most one
// final padded quantum. Go's base64 decoder skips line breaks, so it is no
// check here.
var adoptionB64RE = regexp.MustCompile(`^(?:[A-Za-z0-9+/]{4})*(?:[A-Za-z0-9+/]{2}==|[A-Za-z0-9+/]{3}=)?$`)

// validAdoptionB64 reports whether blob may become the UPTIME_TOKEN line of
// a signed configuration: a line break in it would add configuration lines
// of the caller's choosing under the platform's signature. The signer checks
// the same shape; this keeps a malformed blob from ever being sent.
func validAdoptionB64(blob string) bool {
	return blob != "" && len(blob) <= MaxAdoptionB64Len && adoptionB64RE.MatchString(blob)
}

// Sign requests a signed per-device EEPROM package. On success the package is
// available under <boot-serve>/<resp.Path>/ for the agent to download.
func (c *Client) Sign(ctx context.Context, req SignRequest) (*SignResponse, error) {
	if !validAdoptionB64(req.AdoptionB64) {
		return nil, fmt.Errorf("adoption blob is not padded base64 of at most %d characters", MaxAdoptionB64Len)
	}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/sign", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("call signer: %w", err)
	}
	defer resp.Body.Close()

	data, _ := io.ReadAll(resp.Body)
	var out SignResponse
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("signer returned status %d: %s", resp.StatusCode, string(data))
	}
	if resp.StatusCode != http.StatusOK || !out.OK {
		return nil, fmt.Errorf("signer error (status %d): %s", resp.StatusCode, out.Error)
	}
	// A signer older than the node-scoped layout ignores node_id and writes
	// into the shared serial directory, which the download handler no longer
	// serves. Fail here, naming the cause, rather than dispatch a flash whose
	// package can never be fetched. Deploy the signer before the backend.
	if req.NodeID != "" && out.Path != NodeScopedPath(req.Serial, req.NodeID) {
		return nil, fmt.Errorf("the EEPROM signer wrote the package to %q, not to the node's own directory; "+
			"it predates node-scoped packages and must be redeployed before this backend can bake", out.Path)
	}
	return &out, nil
}

// BaseFirmware describes the newest bootloader image the signer would bake
// from for one SoC family.
type BaseFirmware struct {
	Version        string `json:"version"`                   // short build hash, e.g. "f68405bc"
	Image          string `json:"image"`                     // base image filename, e.g. "pieeprom-2026-06-29.bin"
	BuildTimestamp int64  `json:"build_timestamp,omitempty"` // firmware build time (unix seconds)
}

// BaseFirmwareVersions returns the signer's current firmware base per SoC
// ("cm4"/"cm5"). A SoC may be absent if the signer has no base for it.
func (c *Client) BaseFirmwareVersions(ctx context.Context) (map[string]BaseFirmware, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/base", nil)
	if err != nil {
		return nil, err
	}
	if c.token != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("call signer: %w", err)
	}
	defer resp.Body.Close()

	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("signer returned status %d: %s", resp.StatusCode, string(data))
	}
	var out map[string]BaseFirmware
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("invalid signer /base response: %w", err)
	}
	return out, nil
}

// Bootcode returns the GENERIC (non-per-device) second-stage recovery.bin for a
// SoC ("cm4"/"cm5") — the bytes a WebUSB flasher pushes to a bare board in
// usbboot mode to harvest its serial before baking a per-serial image.
func (c *Client) Bootcode(ctx context.Context, soc string) ([]byte, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/bootcode?soc="+soc, nil)
	if err != nil {
		return nil, err
	}
	if c.token != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("call signer: %w", err)
	}
	defer resp.Body.Close()

	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("signer returned status %d: %s", resp.StatusCode, string(data))
	}
	return data, nil
}
