// Package httpboot implements the network-first HTTP-boot endpoint.
//
// Every managed device is provisioned HTTP-boot-first with an NVMe fallback
// (BOOT_ORDER=0xf167) and fetches its own per-serial path
// (HTTP_PATH=http-boot/{pi_serial_number}). On every boot the ROM requests
// GET /http-boot/<serial>/boot.img (+ boot.sig) and the platform decides, per
// device, whether to hand it the signed RAM bootstrap:
//
//   - serve  -> the device netboots the bootstrap (adopt / install / maintenance)
//   - 404    -> HTTP boot fails, the ROM falls through to NVMe and boots the
//     installed managed OS
//
// The switch is the effective boot_to_os policy (node -> OU -> root). A node
// whose effective boot_to_os == on boots its local OS (404); anything else is
// served the bootstrap. What happens to an UNKNOWN serial (or a lookup/policy
// error) depends on provisioning.http_boot_fail_open: an install that shares a network
// with its devices sets it true so a brand-new device netboots to get adopted;
// the default is fail-CLOSED
// (404), because a public endpoint must not boot arbitrary Pis. See
// docs/NETWORK-FIRST.md.
package httpboot

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	echomw "github.com/labstack/echo/v4/middleware"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/app_errors"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/config"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/node_tombstone"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/organizational_unit"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/cdnsign"
	"golang.org/x/time/rate"
)

// NodeBySerialLookup resolves a node by its globally-unique hardware serial.
// Satisfied by services.NodeService (and the node service concrete type).
type NodeBySerialLookup interface {
	GetByHardwareSerialGlobal(ctx context.Context, hardwareSerial string) (*node.Node, error)
}

// DeletedNodeLookup answers "is this unknown serial a node we deleted?".
//
// Optional dependency (SetDeletedNodeLookup): without it the handler behaves as
// it always did, which is to count a stranded node's boot request as serial
// enumeration. Satisfied by the node service.
type DeletedNodeLookup interface {
	FindDeletedBySerial(ctx context.Context, serial string) *node_tombstone.Tombstone
}

// EffectivePolicyResolver resolves a node's effective inheritable policy
// (node -> OU -> root -> default). Satisfied by the organizational_unit service.
type EffectivePolicyResolver interface {
	ResolveEffectivePolicy(ctx context.Context, accountID, nodeID uuid.UUID) (organizational_unit.EffectivePolicy, error)
}

// NetbootWithholdRegistrar records a WITHHELD netboot attempt (a node that keeps
// re-netbooting without booting its OS) and reports whether the boot-loop failsafe
// should SERVE recovery instead of withholding. Satisfied by services.NodeService.
type NetbootWithholdRegistrar interface {
	RegisterNetbootWithhold(ctx context.Context, n *node.Node, globalThreshold int) (bool, error)
}

// OSInstallInFlightLookup answers "is an OS install to this node's disk still
// unfinished?". Optional dependency (SetOSInstallInFlightLookup); satisfied by
// the node service.
type OSInstallInFlightLookup interface {
	OSInstallInFlight(ctx context.Context, nodeID uuid.UUID) (bool, error)
}

// bootloaderUAPattern extracts the firmware build hash and timestamp from the
// RPi ROM's HTTP-boot User-Agent, e.g.
// "rpiboot/1.0 (e041a0; version:f68405bc timestamp:1783585441)". Parsed for
// DIAGNOSTICS only — see logBootloaderUA for why it is never persisted.
var bootloaderUAPattern = regexp.MustCompile(`version:([0-9a-fA-F]{1,40}) timestamp:(\d{1,19})`)

// allowedFiles are the only files the ROM ever requests over HTTP boot. Keeping
// this an exact whitelist means the :file path parameter can never be used for
// traversal outside the configured boot dir.
var allowedFiles = map[string]bool{
	"boot.img": true,
	"boot.sig": true,
}

// serialRegex bounds the serial to the shape a Raspberry Pi actually emits:
// hex, between the 8-nibble low word a WebUSB harvest can see and the full
// 16-nibble SoC serial (`domain/node.FullSerialLen`, whose own regex is
// `^[0-9a-f]{16}$` — case-insensitive here because this matches the RAW path
// segment, before normalization).
//
// A looser pattern that accepted a ONE-character serial would let the global
// lookup's suffix fallback resolve `f` to the newest node in that hex bucket,
// across accounts. The fallback itself is bounded to >= 8 characters; this
// pattern rejects such a probe before it reaches the database at all.
//
// A Raspberry Pi SoC serial is exactly 16 hex digits. The 8-char floor is
// kept because a WebUSB-provisioned node legitimately stores the low word.
var serialRegex = regexp.MustCompile(`^[0-9a-fA-F]{8,16}$`)

// Handler serves the network-first HTTP-boot decision.
type Handler struct {
	nodeLookup     NodeBySerialLookup
	policyResolver EffectivePolicyResolver
	failsafe       NetbootWithholdRegistrar // optional: nil disables the boot-loop failsafe
	guard          *netbootGuard            // NAT-aware auto-ban on unknown-serial floods
	// deletedLookup recognises the serial of a node this platform deleted
	// (optional; SetDeletedNodeLookup).
	deletedLookup DeletedNodeLookup
	// installLookup reports an unfinished OS install (optional;
	// SetOSInstallInFlightLookup).
	installLookup OSInstallInFlightLookup
	cfg           *config.Config
	logger        *slog.Logger
}

// NewHandler builds the HTTP-boot handler.
func NewHandler(
	nodeLookup NodeBySerialLookup,
	policyResolver EffectivePolicyResolver,
	failsafe NetbootWithholdRegistrar,
	cfg *config.Config,
	logger *slog.Logger,
) *Handler {
	if nodeLookup == nil {
		panic("httpboot.NewHandler: nodeLookup is required")
	}
	if policyResolver == nil {
		panic("httpboot.NewHandler: policyResolver is required")
	}
	if cfg == nil {
		panic("httpboot.NewHandler: cfg is required")
	}
	return &Handler{
		nodeLookup:     nodeLookup,
		policyResolver: policyResolver,
		failsafe:       failsafe,
		guard:          newNetbootGuard(),
		cfg:            cfg,
		logger:         logger.With("handler", "httpboot"),
	}
}

// SetDeletedNodeLookup wires recognition of deleted nodes into the boot path, so
// a stranded device's honest request is not treated as enumeration.
func (h *Handler) SetDeletedNodeLookup(lookup DeletedNodeLookup) {
	h.deletedLookup = lookup
}

// SetOSInstallInFlightLookup wires the install check into the boot decision,
// so a device reset in the middle of writing its disk is served recovery
// instead of being sent to that disk.
func (h *Handler) SetOSInstallInFlightLookup(lookup OSInstallInFlightLookup) {
	h.installLookup = lookup
}

// allowedAgentFiles is the exact whitelist of the /net_install/agent/:file
// route: the raw agent binary (named by `uname -m`, aarch64 on Pi), its
// checksum and its signature. The current recovery image fetches NOTHING here —
// its S99agent runs only the agent baked into the ROM-verified boot.img, the
// download step having been removed — so the route has no consumer in this
// tree and only a recovery image built before that removal would call it; it
// is a candidate for removal, not a trust link to rely on. An
// exact map keeps the :file parameter from traversing outside the agent dir
// and stops the endpoint being a generic file server.
var allowedAgentFiles = map[string]bool{
	"orchestrator-agent-linux-aarch64":        true,
	"orchestrator-agent-linux-aarch64.sha256": true,
	"orchestrator-agent-linux-aarch64.sig":    true,
	"orchestrator-agent-linux-amd64":          true,
	"orchestrator-agent-linux-amd64.sha256":   true,
	"orchestrator-agent-linux-amd64.sig":      true,
}

// RegisterRoutes wires the public (unauthenticated) HTTP-boot routes. The ROM
// cannot authenticate, so these endpoints are intentionally open; they only
// serve the signed bootstrap image (whose authenticity the device's ROM
// verifies against our public key) and the unused agent-binary route below.
func (h *Handler) RegisterRoutes(e *echo.Echo) {
	// Per-IP throttle on the public netboot endpoints. GENEROUS on purpose: a whole
	// cluster (100+ devices) behind one NAT IP can power-cycle together and
	// each device fetches a couple of files, so raw volume from one IP is legitimate.
	// Abuse is caught not by volume but by the netboot GUARD below (volume of
	// UNKNOWN serials). RealIP is trustworthy now (Echo IPExtractor trusts only the
	// proxy), so X-Forwarded-For can't rotate the bucket.
	netbootLimit := echomw.RateLimiterWithConfig(echomw.RateLimiterConfig{
		Store: echomw.NewRateLimiterMemoryStoreWithConfig(echomw.RateLimiterMemoryStoreConfig{
			Rate:      rate.Limit(20),
			Burst:     300,
			ExpiresIn: time.Minute,
		}),
		IdentifierExtractor: func(c echo.Context) (string, error) { return c.RealIP(), nil },
		DenyHandler: func(c echo.Context, _ string, _ error) error {
			return c.NoContent(http.StatusTooManyRequests)
		},
	})
	// Ban gate: an IP that tripped the unknown-serial guard is refused on every
	// netboot route until its ban expires. A legitimate NAT'd cluster only ever
	// requests its own adopted serials, so it never trips the guard.
	banGate := func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			if h.guard.banned(c.RealIP()) {
				return c.NoContent(http.StatusTooManyRequests)
			}
			return next(c)
		}
	}
	e.GET("/http-boot/:serial/:file", h.ServeBootFile, banGate, netbootLimit)
	e.GET("/http-boot/time", h.ServeTime, banGate, netbootLimit)
	e.GET("/net_install/agent/:file", h.ServeAgent, banGate, netbootLimit)
	e.GET("/net_install/eeprom/*", h.ServeEepromPackage, banGate, netbootLimit)
}

// ServeTime returns the current unix time (seconds) as plain text. The RPi
// netboot bootstrap has no RTC and its busybox wget cannot do HTTPS or follow
// the app host's :80->:443 redirect, so it cannot read a Date header to set its
// clock — without a sane clock, TLS to the (https) platform fails and the agent
// never connects. This endpoint lives on the boot host, which is plaintext :80
// with no redirect, and the bootstrap already reached it to fetch boot.img. The
// value is a hint (bounded by the response being over the same channel that
// served the signed boot image); it only needs to be recent enough for TLS.
func (h *Handler) ServeTime(c echo.Context) error {
	c.Response().Header().Set("Cache-Control", "no-store")
	return c.String(http.StatusOK, strconv.FormatInt(time.Now().Unix(), 10))
}

// allowedEepromFiles is the exact set of files a signed per-device EEPROM
// package is made of (plus its checksum manifest). The agent downloads only
// these from <HTTPBootDir>/eeprom/... over the device's platform_url (https on an
// internet-facing install), then flashes them via rpi-eeprom-update.
var allowedEepromFiles = map[string]bool{
	"recovery.bin": true, "pieeprom.upd": true, "pieeprom.sig": true, "SHA256SUMS": true,
}

// hexOrderRe bounds the boot-order path segment of a generic package path.
var hexOrderRe = regexp.MustCompile(`^0x[0-9a-fA-F]{1,8}$`)

// ServeEepromPackage serves the signer's per-device EEPROM packages. The agent
// fetches either the per-device path (by-serial/<serial>/<file>) or the shared
// path (<soc>/<hexorder>/<file>). The layout is validated by shape and the
// filename is whitelisted, so the wildcard can never traverse outside the
// eeprom dir. Public + unauthenticated: the packages are per-device signed
// (the device applies them only if they verify), and a device on an
// internet-facing install fetches them over TLS from its own install.
func (h *Handler) ServeEepromPackage(c echo.Context) error {
	if h.cfg.Provisioning.HTTPBootDir == "" {
		return c.NoContent(http.StatusNotFound)
	}
	sub := strings.TrimPrefix(c.Param("*"), "/")
	parts := strings.Split(sub, "/")
	// Reject traversal / empty segments outright.
	for _, p := range parts {
		if p == "" || p == "." || p == ".." {
			return c.NoContent(http.StatusNotFound)
		}
	}
	// Exactly three segments: <a>/<b>/<file>.
	if len(parts) != 3 || !allowedEepromFiles[parts[2]] {
		return c.NoContent(http.StatusNotFound)
	}
	// dirParts is the package's on-disk directory under <HTTPBootDir>/eeprom/.
	dirParts := []string{parts[0], parts[1]}
	if parts[0] == "by-serial" {
		if !serialRegex.MatchString(parts[1]) {
			return c.NoContent(http.StatusNotFound)
		}
		// The per-device package embeds the node's adoption token (UPTIME_TOKEN).
		// Knowing a serial must NOT be enough to fetch it (that would be a
		// cross-tenant node-hijack primitive), so require a short-lived HMAC
		// capability minted at provision time.
		//
		// The capability names the account AND the node it was minted for, and
		// the file is served from THAT node's directory,
		// by-serial/<serial>/<node>/, never from the serial directory itself. A
		// short serial is not unique and two accounts may both bake for it; a
		// capability bound to the serial path alone would open whichever package
		// sat there last, another account's node credential included. A
		// path-only capability, a forged node, and a node whose
		// package is absent all get the same 404, so the answer says nothing
		// about which nodes exist. Only the agent and the WebUSB flasher fetch
		// this route; the ROM's own per-serial fetch is /http-boot/<serial>/
		// (ServeBootFile) and carries no capability.
		key := cdnsign.DeriveKey(h.cfg.CDNSigningSecret())
		_, nodeID, err := cdnsign.VerifyEEPROMPackage(parts[1], c.QueryParam(cdnsign.ParamExpires), c.QueryParam(cdnsign.ParamSignature), key)
		if err != nil {
			h.logger.Warn("rejected unsigned/expired EEPROM package request", "serial", parts[1])
			return c.NoContent(http.StatusNotFound)
		}
		dirParts = append(dirParts, nodeID.String())
	} else if parts[0] == "cm4" || parts[0] == "cm5" {
		// Generic shared package: no per-device secret, so no capability needed.
		if !hexOrderRe.MatchString(parts[1]) {
			return c.NoContent(http.StatusNotFound)
		}
	} else {
		return c.NoContent(http.StatusNotFound)
	}
	fullPath := filepath.Join(append(append([]string{h.cfg.Provisioning.HTTPBootDir, "eeprom"}, dirParts...), parts[2])...)
	f, err := os.Open(fullPath)
	if err != nil {
		if os.IsNotExist(err) {
			return c.NoContent(http.StatusNotFound)
		}
		h.logger.Error("net_install/eeprom: failed to open file", "path", fullPath, "error", err)
		return c.NoContent(http.StatusInternalServerError)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		h.logger.Error("net_install/eeprom: failed to stat file", "path", fullPath, "error", err)
		return c.NoContent(http.StatusInternalServerError)
	}
	c.Response().Header().Set("Content-Type", "application/octet-stream")
	c.Response().Header().Set("Cache-Control", "no-store")
	c.Response().Status = http.StatusOK
	http.ServeContent(c.Response().Writer, c.Request(), parts[2], info.ModTime(), f)
	return nil
}

// ServeAgent serves the raw agent binary (and its .sha256) the netboot
// bootstrap fetches at runtime, so the boot image never needs re-signing just
// to ship a new agent. The binary is public (also shipped via the deb CDN); the
// bootstrap fetches it from its platform_url — https on an internet-facing
// install, so TLS + the
// baked CA authenticate the source, and a device in any network reaches only
// its own install. Files live in <HTTPBootDir>/agent/.
func (h *Handler) ServeAgent(c echo.Context) error {
	if h.cfg.Provisioning.HTTPBootDir == "" {
		return c.NoContent(http.StatusNotFound)
	}
	file := c.Param("file")
	if !allowedAgentFiles[file] {
		h.logger.Warn("rejected agent download for non-whitelisted file", "file", file)
		return c.NoContent(http.StatusNotFound)
	}
	fullPath := filepath.Join(h.cfg.Provisioning.HTTPBootDir, "agent", file)
	f, err := os.Open(fullPath)
	if err != nil {
		if os.IsNotExist(err) {
			return c.NoContent(http.StatusNotFound)
		}
		h.logger.Error("net_install/agent: failed to open file", "path", fullPath, "error", err)
		return c.NoContent(http.StatusInternalServerError)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		h.logger.Error("net_install/agent: failed to stat file", "path", fullPath, "error", err)
		return c.NoContent(http.StatusInternalServerError)
	}
	c.Response().Header().Set("Content-Type", "application/octet-stream")
	c.Response().Header().Set("Cache-Control", "no-store")
	c.Response().Status = http.StatusOK
	http.ServeContent(c.Response().Writer, c.Request(), file, info.ModTime(), f)
	return nil
}

// ServeBootFile implements the per-serial serve/withhold decision.
func (h *Handler) ServeBootFile(c echo.Context) error {
	if h.cfg.Provisioning.HTTPBootDir == "" {
		// Endpoint not configured -> behave as if nothing is here so devices fall
		// through to their next boot device.
		return c.NoContent(http.StatusNotFound)
	}

	serial := c.Param("serial")
	file := c.Param("file")

	if !allowedFiles[file] {
		return c.NoContent(http.StatusNotFound)
	}
	if !serialRegex.MatchString(serial) {
		h.logger.Warn("rejected http-boot request with malformed serial", "serial", serial)
		h.guard.strike(c.RealIP()) // malformed serials are never legitimate
		return c.NoContent(http.StatusNotFound)
	}

	// Resolve the node. failOpen (pre-adoption on a LAN install) serves on
	// unknown/error; fail-closed (default) withholds so a public :80 never boots a
	// stranger's Pi or leaks topology.
	failOpen := h.cfg.Provisioning.HTTPBootFailOpen
	n, err := h.nodeLookup.GetByHardwareSerialGlobal(c.Request().Context(), serial)
	if err != nil {
		known := errors.Is(err, app_errors.ErrNodeNotFound)
		if !known {
			h.logger.Error("http-boot: node lookup failed", "serial", serial, "error", err, "fail_open", failOpen)
		}
		if failOpen {
			return h.serveFile(c, serial, file) // fail-open: serve unadopted / on error
		}
		// Fail-closed: an UNKNOWN serial (not a transient DB error) is the enumeration/
		// spoofing signal — strike the IP. A legit cluster only requests its own
		// adopted serials, which resolve, so it never strikes here.
		//
		// With ONE exception, and it is not a courtesy: a device whose node was
		// DELETED keeps booting and asking for its own boot.sig forever, because
		// nothing told it to stop. Counting that as enumeration means a stranded
		// node slowly bans the site it sits in — penalising an operator's whole
		// location for a question their own platform provoked. Recognising it also
		// puts the one line in the log that makes the situation diagnosable at all.
		// A serial that matches nodes in more than one account is KNOWN, not
		// probed: refusing it is the safe answer, but striking for it would let
		// one account's row ban the site of another account's device.
		if errors.Is(err, app_errors.ErrHardwareSerialAmbiguous) {
			h.logger.Warn("http-boot: withholding for a serial that matches nodes in more than one account",
				"serial", serial)
			return c.NoContent(http.StatusNotFound)
		}
		if known {
			if deleted := h.findDeleted(c, serial); deleted != nil {
				h.logger.Info("http-boot: withholding for a DELETED node — the device is stranded and will retry forever; restore it (POST /api/v1/nodes/deleted/<id>/restore) or forget it",
					"serial", serial, "node_id", deleted.ID, "account_id", deleted.AccountID,
					"deleted_at", deleted.DeletedAt, "adoption_method", deleted.AdoptionMethod,
					"restorable", deleted.Restorable())
				return c.NoContent(http.StatusNotFound)
			}
			if banned := h.guard.strike(c.RealIP()); banned {
				h.logger.Warn("http-boot: banned IP after repeated unknown-serial requests", "ip", c.RealIP())
			}
		}
		h.logger.Info("http-boot: withholding (fail-closed) for unknown serial or lookup error", "serial", serial)
		return c.NoContent(http.StatusNotFound)
	}

	// A hit resolved by the SUFFIX fallback rather than by an exact match is a
	// device asking for a serial that is not its own, which is not normal
	// traffic, and the ban guard alone would not see it: it strikes only when a
	// lookup FAILS, and a suffix probe succeeds. So it is logged and struck here.
	//
	// Derived rather than reported back from the repository: the exact-match arm
	// compares the whole normalized string, so a stored serial that differs from
	// the requested one can only have come from the fallback. That keeps the
	// repository signature honest — "which query matched" is the handler's
	// question, not the store's.
	//
	// A device whose own EEPROM readback names exactly the requested path is
	// asking for what it was baked with — a WebUSB-flashed node keeps its
	// low-word path after it rebinds to the full serial — so it is not
	// struck: a site of such nodes rebooting together must not ban its own IP.
	//
	// The low word of the node's own serial, asked for while the node's
	// readback is not known, is the same legacy form: an EEPROM baked from a
	// WebUSB harvest carries only the low word, and the readback is unknown
	// whenever the node's last report could not read the bootloader
	// configuration. It is logged as what it is, not as a device asking for someone else's
	// serial, and it is still struck: without the readback nothing tells it
	// apart from a probe for that low word. Any other suffix, or the low word
	// against a readback naming a different path, stays a warning.
	if stored := node.NormalizeSerial(n.GetHardwareSerial()); stored != "" &&
		stored != node.NormalizeSerial(serial) && !n.BootPathRequestsSerial(serial) {
		if node.NormalizeSerial(serial) == node.LowWord(stored) && n.BootHTTPPath() == "" {
			h.logger.Info("http-boot: device asks with the short (low-word) form of its node's serial; its bootloader was baked with the short serial — provisioning the bootloader again bakes the full one",
				"requested_serial", serial, "resolved_serial", stored,
				"node_id", n.ID, "account_id", n.AccountID, "ip", c.RealIP())
		} else {
			h.logger.Warn("http-boot: serial resolved by SUFFIX match, not exactly — a device asking for a serial that is not its own",
				"requested_serial", serial, "resolved_serial", stored,
				"node_id", n.ID, "account_id", n.AccountID, "ip", c.RealIP())
		}
		if banned := h.guard.strike(c.RealIP()); banned {
			h.logger.Warn("http-boot: banned IP after repeated suffix-matched serial requests", "ip", c.RealIP())
		}
	}

	h.logBootloaderUA(c, n)

	if h.shouldBootLocalOS(c, serial, n, failOpen) {
		// A boot request while an OS install to this node is unfinished means
		// the device was reset or lost power mid-write: its disk holds a
		// partial image that may boot far enough to hang, out of the
		// platform's reach. boot_to_os says what to boot once the disk is
		// whole; until the install has settled, recovery is served so the
		// device comes back to the platform. A failed or abandoned install
		// is settled by forcing the node back to netboot, which takes over
		// from here.
		if h.osInstallInFlight(c, n) {
			h.logger.Warn("http-boot: serving recovery, an OS install to this node has not finished",
				"serial", serial, "file", file, "node_id", n.ID)
			return h.serveFile(c, serial, file)
		}
		// Boot-loop failsafe. A crash-looping node keeps re-netbooting: on real
		// hardware the ROM fetches only boot.sig in the withhold case (gets 404,
		// falls through to NVMe) — it never requests boot.img when withheld — so we
		// count boot.sig. Once the failsafe has fired, boot_failsafe_at is recent;
		// serve BOTH files for that one boot so the recovery netboot can complete.
		if h.failsafe != nil {
			if n.BootFailsafeAt != nil && time.Since(*n.BootFailsafeAt) < failsafeServeWindow {
				h.logger.Info("http-boot: serving recovery (boot-loop failsafe in progress)", "serial", serial, "file", file)
				return h.serveFile(c, serial, file)
			}
			if file == "boot.sig" {
				serveRecovery, err := h.failsafe.RegisterNetbootWithhold(c.Request().Context(), n, h.cfg.Provisioning.BootFailsafeThreshold)
				if err != nil {
					// Fail safe toward the normal decision: withhold on error.
					h.logger.Error("http-boot: boot-loop failsafe check failed; withholding", "serial", serial, "error", err)
				} else if serveRecovery {
					return h.serveFile(c, serial, file)
				}
			}
		}
		// Withhold: HTTP boot fails and the ROM falls through to NVMe.
		h.logger.Info("http-boot: withholding bootstrap, device boots local OS", "serial", serial, "file", file)
		return c.NoContent(http.StatusNotFound)
	}

	return h.serveFile(c, serial, file)
}

// findDeleted returns the deleted-node record for a serial, or nil. Nil-safe on
// the optional lookup, and a lookup error is treated as "not a deleted node" —
// the guard must not stop striking because a query failed.
func (h *Handler) findDeleted(c echo.Context, serial string) *node_tombstone.Tombstone {
	if h.deletedLookup == nil {
		return nil
	}
	return h.deletedLookup.FindDeletedBySerial(c.Request().Context(), serial)
}

// osInstallInFlight reports an unfinished OS install for the node. Nil-safe on
// the optional lookup; a lookup error falls back to the normal decision (and
// is logged), the same way a policy-resolution error does.
func (h *Handler) osInstallInFlight(c echo.Context, n *node.Node) bool {
	if h.installLookup == nil {
		return false
	}
	inFlight, err := h.installLookup.OSInstallInFlight(c.Request().Context(), n.ID)
	if err != nil {
		h.logger.Error("http-boot: could not check for an unfinished OS install; applying boot_to_os",
			"node_id", n.ID, "error", err)
		return false
	}
	return inFlight
}

// failsafeServeWindow is how long after the boot-loop failsafe fires we keep
// serving recovery to that node, so the single recovery boot fetches both boot.sig
// and boot.img (and completes the ~20s image download + boot) without the one-shot
// counter reset flipping it back to withholding mid-boot.
const failsafeServeWindow = 3 * time.Minute

// shouldBootLocalOS reports whether the known node should boot its installed OS
// (withhold the bootstrap) rather than netboot. An effective boot_to_os of "on"
// boots locally. On a policy-resolution error, fail-open serves the bootstrap
// while fail-closed withholds — the safer choice for a public
// endpoint is not to netboot on an internal error.
func (h *Handler) shouldBootLocalOS(c echo.Context, serial string, n *node.Node, failOpen bool) bool {
	policy, err := h.policyResolver.ResolveEffectivePolicy(c.Request().Context(), n.AccountID, n.ID)
	if err != nil {
		h.logger.Error("http-boot: policy resolution failed", "serial", serial, "node_id", n.ID, "error", err, "fail_open", failOpen)
		return !failOpen // fail-closed -> withhold (true); fail-open -> serve (false)
	}

	return policy.BootToOS.Value
}

// logBootloaderUA logs the firmware identity the ROM claims in its User-Agent,
// for diagnostics only. It is deliberately NEVER persisted
// (docs/HTTPBOOT-AUTH-RESEARCH.md, Phase 0): the only binding between this
// unauthenticated request and a real device is the serial in the URL, which is
// printed on the hardware — so any persisted value is writable by anyone who
// knows a serial, and the UI would render it as an "Up-to-date" badge. The
// IP-binding gate that used to guard the write was simultaneously too strict
// (a NAT'd cluster's telemetry was dropped every time) and too weak (any
// sibling behind the same NAT passed it). The authoritative writer is the
// AGENT, which reports the running bootloader over its authenticated gRPC
// stream (BootInfo.bootloader_version, preferred unconditionally in
// UpdateSystemInfo) — including from the recovery bootstrap, so a netbooted
// node updates the stored value moments after this request anyway.
func (h *Handler) logBootloaderUA(c echo.Context, n *node.Node) {
	m := bootloaderUAPattern.FindStringSubmatch(c.Request().UserAgent())
	if m == nil {
		return
	}
	h.logger.Debug("http-boot: ROM reports bootloader (diagnostic only, not persisted)",
		"node_id", n.ID, "version", strings.ToLower(m[1]), "ua_timestamp", m[2])
}

// serveFile streams a whitelisted boot file from the configured HTTP-boot dir.
func (h *Handler) serveFile(c echo.Context, serial, file string) error {
	fullPath := filepath.Join(h.cfg.Provisioning.HTTPBootDir, file)

	f, err := os.Open(fullPath)
	if err != nil {
		if os.IsNotExist(err) {
			h.logger.Error("http-boot: configured boot file missing", "path", fullPath)
			return c.NoContent(http.StatusNotFound)
		}
		h.logger.Error("http-boot: failed to open boot file", "path", fullPath, "error", err)
		return c.NoContent(http.StatusInternalServerError)
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		h.logger.Error("http-boot: failed to stat boot file", "path", fullPath, "error", err)
		return c.NoContent(http.StatusInternalServerError)
	}

	h.logger.Info("http-boot: serving bootstrap", "serial", serial, "file", file, "size", info.Size())

	c.Response().Header().Set("Content-Type", "application/octet-stream")
	c.Response().Header().Set("Cache-Control", "no-store")
	c.Response().Status = http.StatusOK

	// http.ServeContent honours Range requests, which the RPi ROM may use when
	// fetching large boot images, and sets Content-Length/Last-Modified for us.
	http.ServeContent(c.Response().Writer, c.Request(), file, info.ModTime(), f)
	return nil
}
