// Package server_info exposes deployment-capability discovery and the
// one-command agent installer. One codebase serves two deployment profiles —
// internet-facing (devices behind NAT; only agent-outbound gRPC works) and
// LAN (backend shares a network with the fleet; SSH adoption, EEPROM
// signing and netboot are available). Capabilities derive from what is actually
// configured, so the UI can hide what this install cannot do.
package server_info

import (
	"context"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/signing"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/config"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/out/results"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/services"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/buildinfo"
)

// Capabilities reports which optional feature groups this install supports.
// Derived from configuration, not stored — a restart with new config changes it.
type Capabilities struct {
	// SSHAdopt: backend can dial devices over SSH (same-network installs only).
	SSHAdopt bool `json:"ssh_adopt"`
	// EEPROMSigner: per-device signed EEPROM baking (WebUSB flasher, provision
	// EEPROM, bootloader updates) is available.
	EEPROMSigner bool `json:"eeprom_signer"`
	// Netboot: the platform serves signed network-boot files (reboot-to-recovery,
	// network-first boot).
	Netboot bool `json:"netboot"`
	// SDProvisioning: downloadable SD/USB boot bundles are available.
	SDProvisioning bool `json:"sd_provisioning"`
	// Terminal: the browser web-SSH terminal is enabled.
	Terminal bool `json:"terminal"`
	// OneCommandAdopt: the curl|sh installer can work here — requires the
	// public agent-package CDN (local storage), which the installer downloads
	// the agent from.
	OneCommandAdopt bool `json:"one_command_adopt"`
	// BootHost is the netboot HTTP_HOST this install serves from; the UI flags a
	// device whose EEPROM points at a different host (moved between installs).
	// Empty when netboot is not configured.
	BootHost string `json:"boot_host,omitempty"`
	// MaxImageUploadSize is the per-image upload cap in bytes (a disk
	// quota); 0/absent = only the global storage ceiling applies.
	MaxImageUploadSize int64 `json:"max_image_upload_size,omitempty"`
	// MaxImagesPerAccount caps how many images an account may have;
	// 0/absent = unlimited.
	MaxImagesPerAccount int `json:"max_images_per_account,omitempty"`
	// AccountErasure: an account-deletion request is carried out by the
	// install's operator console. True only on the hosted install (the
	// kind=hosted entitlement in force); a self-hosted install records the
	// request and has nothing that executes it, and the SPA says so.
	AccountErasure bool `json:"account_erasure"`
	// SerialLeaseDays is how long a device bound to a node that was never
	// admitted (a browser-flashed placeholder, a pending adoption) may stay
	// silent before its serial is released; 0 = never released.
	SerialLeaseDays int `json:"serial_lease_days"`
	// Revision is the commit the backend was built from; omitted when the
	// build recorded none.
	Revision string `json:"revision,omitempty"`
	// Version is the platform release the backend was built for; empty when
	// the build stamped none (a build from source).
	Version string `json:"version"`
}

// SignInOptions is what the tokenless sign-in and sign-up pages need to word
// themselves truthfully. Every field is a fact a visitor could learn by
// trying (asking for a reset link, submitting a sign-up), so serving it
// without a token discloses nothing new; no host, address, count or
// credential is in it.
type SignInOptions struct {
	// EmailDelivery: the install can send mail, so "forgot password",
	// confirmation and invitation mails work. Without it a reset is the
	// operator's, from the server.
	EmailDelivery bool `json:"email_delivery"`
	// Registration is how a sign-up is admitted right now: "first_owner"
	// (no account yet, no code needed), "open" (the operator opened
	// self-registration; no code needed), "invite_code" (a new organisation
	// needs the operator's code) or "closed" (people join by invitation).
	// Omitted when the account count could not be read; the SPA then shows
	// the form as it always did and the registration decides.
	Registration services.RegistrationState `json:"registration,omitempty"`
	// RegistrationReason says why a "closed" registration is closed, only
	// where the page has something to tell the visitor that is not already
	// public: "email_unavailable" — the operator opened registration, but
	// this install cannot send the confirmation mail an open sign-up must be
	// verified with. Omitted otherwise.
	RegistrationReason string `json:"registration_reason,omitempty"`
	// SingleSignOn: the identity-provider sign-in routes (/auth/oidc/*) are
	// served on this install. When false the SPA does not ask
	// GET /auth/oidc/config, which then answers 404. Whether a provider is
	// configured is visible to anyone at that route already.
	SingleSignOn bool `json:"single_sign_on"`
}

// RegistrationReasonEmailUnavailable: open registration is on and closed
// for want of mail (SignInOptions.RegistrationReason).
const RegistrationReasonEmailUnavailable = "email_unavailable"

// RegistrationReporter is the one capability the sign-in options need from
// the auth service.
type RegistrationReporter interface {
	RegistrationState(ctx context.Context) (services.RegistrationState, error)
}

// InstallKindReporter tells the hosted install from a self-hosted one; the
// entitlement service answers it from the file in force.
type InstallKindReporter interface {
	IsHostedInstall(ctx context.Context) bool
}

// registrationCacheTTL bounds how often the public sign-in options read the
// account count. The page is tokenless, so without it every anonymous load
// is a query; the answer only moves when an account is created.
//
// The first-owner answer is never cached: it stops being true the moment the
// first account exists, and that account may be created by another process
// (the installer) this handler hears nothing from. A stale "first_owner"
// sends every sign-up in the window to a refusal, while the reads it costs
// happen only on an install with no account yet.
const registrationCacheTTL = 10 * time.Second

// Handler serves capability discovery and the installer script.
type Handler struct {
	cfg *config.Config
	// agentCDN reports whether the public agent-package CDN route is served
	// (local storage only — the same condition the router uses to register it).
	// The one-command installer depends on it for the deb download.
	agentCDN bool
	logger   *slog.Logger

	registration RegistrationReporter
	installKind  InstallKindReporter
	now          func() time.Time

	singleSignOn bool

	regMu     sync.Mutex
	regState  services.RegistrationState
	regReadAt time.Time
}

// NewHandler constructs the server-info handler.
func NewHandler(cfg *config.Config, agentCDNAvailable bool, logger *slog.Logger) *Handler {
	if cfg == nil {
		panic("server_info: cfg is required")
	}
	return &Handler{cfg: cfg, agentCDN: agentCDNAvailable, logger: logger.With("handler", "server_info"), now: time.Now}
}

// SetRegistration wires the sign-up admission reporter. Without one the
// sign-in options omit the registration state (the SPA's default form).
func (h *Handler) SetRegistration(r RegistrationReporter) {
	if r == nil {
		panic("server_info: registration reporter is nil")
	}
	h.registration = r
}

// SetSingleSignOn records whether the identity-provider sign-in routes are
// registered (a provider was discovered at startup).
func (h *Handler) SetSingleSignOn(on bool) { h.singleSignOn = on }

// SetInstallKind wires the hosted/self-hosted reporter. Without one the
// install reports no erasure executor, the self-hosted answer.
func (h *Handler) SetInstallKind(k InstallKindReporter) {
	if k == nil {
		panic("server_info: install kind reporter is nil")
	}
	h.installKind = k
}

// RegisterRoutes wires the routes. Capabilities are authenticated (they leak
// deployment topology); the installer script is public by design — the secret
// is the adoption token the caller supplies, never the script.
func (h *Handler) RegisterRoutes(e *echo.Echo, jwtMiddleware echo.MiddlewareFunc) {
	e.GET("/api/v1/server/capabilities", h.GetCapabilities, jwtMiddleware)
	// Tokenless: the sign-in and sign-up pages hold no token. See SignInOptions
	// for why nothing in it needs one.
	e.GET("/api/v1/server/sign-in-options", h.GetSignInOptions)
	e.GET("/install.sh", h.GetInstallScript)
	// Uninstall is public for the same reason install is, and for one more: the
	// device it has to run on may already be off the account, so requiring a
	// credential would mean the owner of the hardware cannot get the
	// daemon off it. It carries no platform state and takes no token.
	e.GET("/uninstall.sh", h.GetUninstallScript)
}

// GetCapabilities returns this install's feature capabilities
// @Summary Get server capabilities
// @Description Reports which optional feature groups this deployment supports (SSH adoption, EEPROM signing, netboot, SD provisioning, terminal) and whether an account-deletion request is carried out by the install's operator console (account_erasure, hosted install only). The UI hides what the install cannot do. serial_lease_days is how long a device bound to a never-admitted node may stay silent before its serial is released (0 = never); revision is the commit the backend was built from, omitted when the build recorded none; version is the platform release it was built for, empty for a build from source.
// @Tags server
// @Produce json
// @Success 200 {object} results.APIResponse{data=Capabilities}
// @Security BearerAuth
// @Router /api/v1/server/capabilities [get]
func (h *Handler) GetCapabilities(c echo.Context) error {
	caps := Capabilities{
		SSHAdopt:            h.cfg.SSHAdopt.Enabled,
		EEPROMSigner:        h.cfg.EEPROMSigner.URL != "",
		Netboot:             h.cfg.Provisioning.HTTPBootDir != "",
		SDProvisioning:      h.cfg.Provisioning.SDPayloadDir != "",
		Terminal:            h.cfg.Terminal.Enabled,
		OneCommandAdopt:     h.agentCDN,
		BootHost:            h.cfg.Provisioning.BootHost,
		MaxImageUploadSize:  h.cfg.Image.MaxUploadSize,
		MaxImagesPerAccount: h.cfg.Image.MaxPerAccount,
		SerialLeaseDays:     h.cfg.Quota.SerialLeaseDays,
		Revision:            buildinfo.Revision(),
		Version:             buildinfo.Version(),
	}
	if h.installKind != nil {
		caps.AccountErasure = h.installKind.IsHostedInstall(c.Request().Context())
	}
	return results.SendSuccess(c, http.StatusOK, caps, "server capabilities")
}

// GetSignInOptions returns what the sign-in and sign-up pages need
// @Summary Get sign-in options
// @Description Tokenless. Reports whether this install can send mail (password reset, confirmation and invitation mails) and how a sign-up is admitted right now (first_owner, open, invite_code or closed; omitted when unknown; registration_reason email_unavailable when open registration is closed for want of mail), and whether sign-in through an identity provider is served (single_sign_on). The sign-in and sign-up pages word themselves from it. The provider itself is described by GET /auth/oidc/config, which exists only when single_sign_on is true.
// @Tags server
// @Produce json
// @Success 200 {object} results.APIResponse{data=SignInOptions}
// @Router /api/v1/server/sign-in-options [get]
func (h *Handler) GetSignInOptions(c echo.Context) error {
	opts := SignInOptions{
		EmailDelivery: h.cfg.Email.Configured(),
		Registration:  h.registrationState(c.Request().Context()),
		SingleSignOn:  h.singleSignOn,
	}
	if opts.Registration == services.RegistrationClosed && h.cfg.Server.OpenRegistration && !h.cfg.CanVerifyEmail() {
		opts.RegistrationReason = RegistrationReasonEmailUnavailable
	}
	return results.SendSuccess(c, http.StatusOK, opts, "sign-in options")
}

// registrationState answers from a short cache. A failed read is not
// cached, logged, and answered as unknown (empty).
func (h *Handler) registrationState(ctx context.Context) services.RegistrationState {
	if h.registration == nil {
		return ""
	}
	h.regMu.Lock()
	defer h.regMu.Unlock()
	if h.regState != "" && h.now().Sub(h.regReadAt) < registrationCacheTTL {
		return h.regState
	}
	state, err := h.registration.RegistrationState(ctx)
	if err != nil {
		h.logger.WarnContext(ctx, "reading the registration state for the sign-in page failed", "error", err)
		return ""
	}
	if state == services.RegistrationFirstOwner {
		h.regState = ""
		return state
	}
	h.regState = state
	h.regReadAt = h.now()
	return state
}

// scriptPlatformURL is the origin the device scripts name: the advertised
// URL, or the request's own origin when none is configured. It is
// substituted into an executable script, so only a clean http(s) origin is
// accepted — a hostile Host header cannot smuggle shell syntax.
func (h *Handler) scriptPlatformURL(c echo.Context) (string, bool) {
	base := strings.TrimRight(h.cfg.Server.AdvertisementURL, "/")
	if base == "" {
		// Fall back to the request origin (Echo honors X-Forwarded-Proto behind
		// the reverse proxy).
		base = c.Scheme() + "://" + c.Request().Host
	} else if !strings.Contains(base, "://") {
		// Scheme-less advertisement URLs (bare host/IP) are treated as http by
		// the rest of the platform — mirror that here.
		base = "http://" + base
	}
	u, err := url.Parse(base)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" ||
		u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" ||
		strings.ContainsAny(u.Host, "'\"`$\\;| ") {
		return "", false
	}
	return u.Scheme + "://" + u.Host, true
}

// GetInstallScript serves the one-command agent installer
// @Summary One-command agent installer script
// @Description Shell script that installs the agent and adopts the device: `curl -fsSL https://<platform>/install.sh | sudo ORCHESTRATOR_TOKEN='<adoption-token>' sh`. The token is the base64 adoption blob (per-node adoption file or master-token adoption file) and is self-contained; the script never receives credentials from the server. `--token-file <path>` reads the token from a root-only file instead, so it is not on a command line. A one-time code (ac_...) is exchanged only after the package is installed, and a second run of the same code on a device that already exchanged it finishes with the adoption file written then. Exit statuses are listed in the script's header.
// @Tags server
// @Produce plain
// @Success 200 {string} string "shell script"
// @Router /install.sh [get]
func (h *Handler) GetInstallScript(c echo.Context) error {
	if !h.agentCDN {
		// curl -f turns this into a clean non-zero exit instead of piping an
		// error page into the caller's shell.
		return c.String(http.StatusNotFound, "one-command install is not available on this server (no agent package CDN)\n")
	}
	base, ok := h.scriptPlatformURL(c)
	if !ok {
		return c.String(http.StatusServiceUnavailable, "platform URL is not configured on this server\n")
	}
	script := strings.ReplaceAll(withPackageLockShell(installScript), "__PLATFORM_URL__", base)
	// Inject the trusted agent-signing public keys the script verifies the package
	// against before installing it as root (multi-trust; PEM has no shell-special
	// chars, it lives inside a single-quoted heredoc-free assignment).
	script = strings.ReplaceAll(script, "__AGENT_SIGNING_PUBKEYS__", signing.TrustedPublicKeysPEM())
	return c.Blob(http.StatusOK, "text/x-shellscript; charset=utf-8", []byte(script))
}

// installScript mirrors the SSH-adopt runner's install sequence
// (download deb -> dpkg -> write adoption file 0600 -> enable+restart) as a
// POSIX shell script for devices the backend cannot reach. The token is read
// from env or argument — never fetched from the server — and is the same
// base64(JSON) line the agent's --adoption-file expects.
const installScript = `#!/bin/sh
# Orchestrator agent installer.
#
# Usage:
#   curl -fsSL __PLATFORM_URL__/install.sh | sudo sh -s -- --token '<adoption-token>'
#   curl -fsSL __PLATFORM_URL__/install.sh | sudo ORCHESTRATOR_TOKEN='<adoption-token>' sh
#   curl -fsSL __PLATFORM_URL__/install.sh | sudo sh    # prompts for the token
#   curl -fsSL __PLATFORM_URL__/install.sh | sudo sh -s -- --token-file /root/token
#
# --token-file reads the token from a file (keep it root-only), so a command
# that starts this script in a unit of its own does not carry the token on a
# command line, which the journal records.
#
# The adoption token is issued by the platform (Add Node). It is either a
# one-time code (ac_...), traded here over TLS for the adoption config, or a
# legacy adoption blob (base64 JSON) carrying the config directly.
#
# It reports success only once the agent is installed and the platform has
# accepted its connection. On a freshly installed system it may first wait,
# saying so, for the system's own first upgrade to release the package
# manager. A one-time code is exchanged only after the agent is installed, so
# a run that stops before that leaves the code unused.
# Exit status:
#    0  connected and accepted
#    2  bad arguments or token, not root, or the code exchange failed
#    3  unsupported device (not arm64, no dpkg)
#    4  the agent package could not be downloaded
#    5  the agent is installed but its service is not running
#    6  a plaintext platform URL without --insecure
#    7  the agent was not installed
#    8  the agent runs but has not reached the platform yet
#    9  the platform refused the device: the install is at its device cap
#   10  the package signature is missing or invalid, or openssl is missing;
#       or the platform refused the device: its account is at its device limit
#   11  the platform did not accept the device's credential
#   12  the board is registered to another account on the platform
#   13  the platform refused the device's identity (master token on a board
#       without a unique hardware serial)
#   14  the platform could not check its device limit; the agent keeps asking
set -eu

PLATFORM_URL="__PLATFORM_URL__"
TOKEN="${ORCHESTRATOR_TOKEN:-}"
INSECURE="${ORCHESTRATOR_INSECURE:-}"

while [ $# -gt 0 ]; do
  case "$1" in
    --token) TOKEN="$2"; shift 2 ;;
    --token=*) TOKEN="${1#--token=}"; shift ;;
    --token-file|--token-file=*)
      if [ "$1" = --token-file ]; then
        TOKEN_FILE="${2:-}"
        if [ $# -ge 2 ]; then shift 2; else shift; fi
      else
        TOKEN_FILE="${1#--token-file=}"; shift
      fi
      if [ -z "$TOKEN_FILE" ] || [ ! -r "$TOKEN_FILE" ]; then
        echo "ERROR: cannot read the token file ${TOKEN_FILE:-(none given)}." >&2
        exit 2
      fi
      TOKEN="$(head -n 1 "$TOKEN_FILE" | tr -d '[:space:]')"
      ;;
    --insecure) INSECURE=1; shift ;;
    *) echo "ERROR: unknown argument: $1" >&2; exit 2 ;;
  esac
done

# No token given: prompt on the controlling terminal (keeps the credential out
# of shell history and process argv).
if [ -z "$TOKEN" ] && [ -r /dev/tty ] && [ -w /dev/tty ]; then
  printf 'Adoption token: ' > /dev/tty
  stty -echo < /dev/tty 2>/dev/null || true
  IFS= read -r TOKEN < /dev/tty || true
  stty echo < /dev/tty 2>/dev/null || true
  printf '\n' > /dev/tty
fi
if [ -z "$TOKEN" ]; then
  echo "ERROR: adoption token required (--token argument, ORCHESTRATOR_TOKEN env, or interactive prompt)." >&2
  exit 2
fi
if [ "$(id -u)" != "0" ]; then
  echo "ERROR: must run as root (pipe into 'sudo sh')." >&2
  exit 2
fi

# A one-time code (ac_<hex>) is traded for the adoption blob over TLS, and
# that spends it. So everything that can still stop this run — the device
# checks, the package download, its signature and the package lock — comes
# first, and the exchange happens just before the adoption file is written.
# Only the plaintext refusal is checked here, because it needs no network.
case "$TOKEN" in
  ac_*)
    case "$PLATFORM_URL" in
      https://*) ;;
      *)
        if [ -z "$INSECURE" ]; then
          echo "ERROR: the platform URL is not https; refusing to fetch adoption credentials over plaintext. Re-run with --insecure only if you fully trust this network." >&2
          exit 6
        fi
        ;;
    esac
    ;;
esac

ARCH="$(uname -m)"
case "$ARCH" in
  aarch64|arm64) ;;
  *) echo "ERROR: unsupported architecture '$ARCH' — the agent package targets arm64." >&2; exit 3 ;;
esac
if ! command -v dpkg >/dev/null 2>&1; then
  echo "ERROR: dpkg not found — a Debian-based OS is required." >&2
  exit 3
fi

# check_adoption_blob: the token must be base64(JSON) carrying a
# platform_url, and the package download and all agent traffic ride that URL:
# plaintext means a network attacker can substitute a root-installed package,
# so it is refused unless the operator explicitly opted in (--insecure /
# ORCHESTRATOR_INSECURE=1). $1 and $2 say what to do about each refusal.
check_adoption_blob() {
  DECODED="$(printf '%s' "$TOKEN" | base64 -d 2>/dev/null || true)"
  case "$DECODED" in
    *platform_url*) ;;
    *) echo "ERROR: token does not decode to an adoption blob — $1" >&2; exit 2 ;;
  esac
  case "$DECODED" in
    *'"platform_url":"https'*) ;;
    *)
      if [ -z "$INSECURE" ]; then
        echo "ERROR: the platform URL in the token is not https. $2" >&2
        exit 6
      fi
      echo "WARNING: proceeding over plaintext http (--insecure)." >&2
      ;;
  esac
}
case "$TOKEN" in
  ac_*) ;;
  *) check_adoption_blob "copy it exactly as issued." "Re-run with --insecure only if you fully trust this network." ;;
esac

DEB_URL="$PLATFORM_URL/cdn/agent/latest/arm64/deb"
SIG_URL="$DEB_URL.sig"
TMP_DEB="$(mktemp /tmp/orchestrator-agent.XXXXXX)"
TMP_SIG="$(mktemp /tmp/orchestrator-agent-sig.XXXXXX)"
trap 'rm -f "$TMP_DEB" "$TMP_SIG"' EXIT

# Re-running this command is normal — an operator retries it, pastes it twice, or
# calls it from a script — and it must not redo everything: a multi-MB download,
# a signature verification, and the same package reinstalled as root.
#
# The signature file is fetched FIRST and doubles as the change detector: it is
# ~100 bytes, it is needed anyway, and it differs whenever the package differs.
# If it matches what a previous successful run recorded AND the package is still
# installed, the download, the verification and the dpkg are all skipped.
#
# Note it must be the .sig and not a .sha256 sidecar: the CDN route accepts only
# deb/rpm/tar.gz and their signatures, so a deb.sha256 URL answers 400 — a probe
# built on it would never match and this shortcut would silently never happen.
#
# Leaning on the signature file as a detector is safe because it is only ever a
# detector here, never the authority: the sole path that INSTALLS still verifies
# it against the keys baked into this script. The worst a forged or replayed .sig
# achieves is making this script do nothing.
#
# What is deliberately NOT skipped: writing the adoption file and restarting the
# agent. Both are cheap and both are the point of a re-run — a second run with a
# DIFFERENT token is how a device is moved to another account, and a re-run on a
# wedged agent is how it gets kicked.
SIG_MARKER=/etc/orchestrator/.agent-package-sig
echo "Checking the published agent package..."
SIG_RC=0
if command -v curl >/dev/null 2>&1; then
  curl -fsSL "$SIG_URL" -o "$TMP_SIG" || SIG_RC=$?
else
  wget -qO "$TMP_SIG" "$SIG_URL" || true
fi
# curl exit 60: the server's certificate was not accepted. On a device with no
# battery-backed clock the usual reason is a date in the past. Nothing has
# been installed and a one-time code has not been used yet.
if [ ! -s "$TMP_SIG" ] && [ "$SIG_RC" -eq 60 ]; then
  echo "ERROR: the platform's TLS certificate was not accepted. This device's clock reads $(date -u '+%Y-%m-%d %H:%M UTC'); if that is wrong, set it (or wait for time sync) and run the same command again — nothing was changed and the token has not been used." >&2
  exit 2
fi
if [ ! -s "$TMP_SIG" ]; then
  echo "ERROR: agent package signature unavailable; refusing to install an unsigned agent." >&2
  exit 10
fi

SKIP_PACKAGE=0
if [ -s "$SIG_MARKER" ] &&
   dpkg-query -W -f='${Status}' orchestrator-agent 2>/dev/null | grep -q "install ok installed" &&
   cmp -s "$TMP_SIG" "$SIG_MARKER"; then
  SKIP_PACKAGE=1
fi

if [ "$SKIP_PACKAGE" -eq 1 ]; then
  echo "Agent package already current ($(dpkg-query -W -f='${Version}' orchestrator-agent 2>/dev/null)) — skipping download, verification and install."
else

echo "Downloading agent package..."
if command -v curl >/dev/null 2>&1; then
  curl -fsSL "$DEB_URL" -o "$TMP_DEB" || true
else
  wget -qO "$TMP_DEB" "$DEB_URL" || true
fi
if [ ! -s "$TMP_DEB" ]; then
  echo "ERROR: agent package download failed (is the agent CDN populated on the platform?)." >&2
  exit 4
fi
# Catch non-deb bodies (error pages served with 200, truncated downloads)
# before they reach a root dpkg.
if ! dpkg-deb --info "$TMP_DEB" >/dev/null 2>&1; then
  echo "ERROR: downloaded file is not a valid Debian package — the platform's agent CDN is misconfigured." >&2
  exit 4
fi

# Verify the package signature before installing as root (fail-closed). The signing
# key lives only on a hardware token, never on the server, so a compromised CDN or a
# plaintext MITM cannot forge a valid signature for a malicious package. The
# signature itself was already fetched above, where it also served as the
# change detector.
echo "Verifying agent package signature..."
if ! command -v openssl >/dev/null 2>&1; then
  echo "ERROR: openssl is required to verify the agent package signature." >&2
  exit 10
fi
KEYDIR="$(mktemp -d)"
printf '%s\n' '__AGENT_SIGNING_PUBKEYS__' | awk -v kd="$KEYDIR" 'BEGIN{n=0} /-----BEGIN/{n++} {print > (kd "/k" n ".pem")}'
VERIFIED=0
for k in "$KEYDIR"/k*.pem; do
  if openssl dgst -sha384 -verify "$k" -signature "$TMP_SIG" "$TMP_DEB" >/dev/null 2>&1; then
    VERIFIED=1
    break
  fi
done
rm -rf "$KEYDIR"
if [ "$VERIFIED" -ne 1 ]; then
  echo "ERROR: agent package signature is invalid — refusing to install." >&2
  exit 10
fi
echo "Signature verified."

echo "Installing agent..."
__PACKAGE_LOCK_FUNCTIONS__
# The script never reports success for an agent that is not installed: every
# way out of this block that leaves the package missing exits non-zero and
# says why.
not_installed() {
  echo "ERROR: the agent was NOT installed: $1" >&2
  if [ "$PKG_LOCK_TIMED_OUT" = 1 ]; then
    echo "Nothing was changed on this device. Run the same command again once that process has finished (it is safe to repeat)." >&2
  fi
  exit 7
}
if ! pkg_run dpkg -i "$TMP_DEB"; then
  [ "$PKG_LOCK_TIMED_OUT" = 0 ] || not_installed "$PKG_ERR"
  # A dependency failure: let apt repair it, then the install must actually
  # succeed. apt waits for the lock itself, for what is left of the budget.
  echo "$PKG_ERR" >&2
  echo "Repairing dependencies with apt-get..."
  PKG_LEFT=$((PKG_LOCK_MAX - PKG_LOCK_WAITED))
  [ "$PKG_LEFT" -gt 0 ] || PKG_LEFT=1
  PKG_T0=$(date +%s)
  if ! apt-get -o DPkg::Lock::Timeout="$PKG_LEFT" install -y -f; then
    not_installed "apt-get could not repair the package dependencies (see above)"
  fi
  PKG_LOCK_WAITED=$((PKG_LOCK_WAITED + $(date +%s) - PKG_T0))
  pkg_run dpkg -i "$TMP_DEB" || not_installed "$PKG_ERR"
fi
if ! dpkg-query -W -f='${Status}' orchestrator-agent 2>/dev/null | grep -q "install ok installed"; then
  not_installed "the package manager reports orchestrator-agent as not installed"
fi

# Record the signature of what was actually installed, so the next run can skip
# all of the above. Written only after a VERIFIED package installed successfully:
# a marker recorded any earlier would let a failed install suppress every future
# one.
mkdir -p /etc/orchestrator
cp "$TMP_SIG" "$SIG_MARKER"
chmod 644 "$SIG_MARKER"

fi

# One-time opaque code (ac_<hex>): trade it over TLS for the adoption blob so
# the link never carries account id, topology or a reusable credential. The
# platform URL is baked into this script, so the code alone is enough. This is
# the step that spends the code; nothing before it did.
#
# The device remembers which code it exchanged (a digest, never the code), so
# a second start of the same command — a wrapper the system restarted, or the
# same line run again — finishes with the adoption file written then instead
# of presenting a spent code.
CODE_MARKER=/etc/orchestrator/.adoption-code
CODE_ID=""
case "$TOKEN" in
  ac_*)
    CODE_ID="$(printf '%s' "$TOKEN" | sha256sum 2>/dev/null | cut -d' ' -f1 || true)"
    if [ -n "$CODE_ID" ] && [ -s /etc/orchestrator/node-adoption.txt ] &&
       [ "$(cat "$CODE_MARKER" 2>/dev/null || true)" = "$CODE_ID" ]; then
      echo "This one-time code was already exchanged on this device; using the adoption file written then."
      TOKEN="$(cat /etc/orchestrator/node-adoption.txt)"
      CODE_ID=""
    fi
    ;;
esac
case "$TOKEN" in
  ac_*)
    case "$PLATFORM_URL" in
      https://*) ;;
      *) echo "WARNING: exchanging the adoption code over plaintext http (--insecure)." >&2 ;;
    esac
    echo "Exchanging one-time adoption code..."
    EXCHANGE_BODY='{"code":"'"$TOKEN"'"}'
    # Two candidate paths, because the two supported topologies differ.
    # Where a reverse proxy routes PathPrefix(/api) and strips ONE /api (a
    # proxied install), an external client has to ask for /api/api/v1/... — the
    # same double-prefix the browser sends. Where the backend is reached
    # directly (a LAN install), /api/v1/... is correct. The script cannot know
    # which it is talking to, so it tries the proxied form first and falls back.
    # Trying both leaks nothing: the endpoint answers a uniform 404 for unknown,
    # expired and already-used codes alike.
    for EXCHANGE_PATH in "/api/api/v1/adopt/exchange" "/api/v1/adopt/exchange"; do
      if command -v curl >/dev/null 2>&1; then
        EXCHANGE_RC=0
        TOKEN="$(curl -fsSL -X POST -H 'Content-Type: application/json' -d "$EXCHANGE_BODY" "$PLATFORM_URL$EXCHANGE_PATH" 2>/dev/null)" || EXCHANGE_RC=$?
      else
        TOKEN="$(wget -qO- --header='Content-Type: application/json' --post-data="$EXCHANGE_BODY" "$PLATFORM_URL$EXCHANGE_PATH" 2>/dev/null || true)"
      fi
      [ -n "$TOKEN" ] && break
    done
    # curl exit 60: the certificate was not accepted, so no request was sent
    # and the code is still unused.
    if [ -z "$TOKEN" ] && [ "${EXCHANGE_RC:-0}" -eq 60 ]; then
      echo "ERROR: the platform's TLS certificate was not accepted. This device's clock reads $(date -u '+%Y-%m-%d %H:%M UTC'); if that is wrong, set it (or wait for time sync) and run the same command again — the code has not been used." >&2
      exit 2
    fi
    if [ -z "$TOKEN" ]; then
      echo "ERROR: adoption code exchange failed — the code is invalid, expired, or already used. Generate a fresh install command from the platform. The agent package is installed but has no identity yet; the fresh command finishes the adoption without downloading it again." >&2
      exit 2
    fi
    check_adoption_blob "the code has been used; generate a fresh install command from the platform." "The code has been used: generate a fresh install command, and run it with --insecure only if you fully trust this network."
    ;;
esac

mkdir -p /etc/orchestrator
umask 077
printf '%s\n' "$TOKEN" > /etc/orchestrator/node-adoption.txt
chmod 600 /etc/orchestrator/node-adoption.txt
if [ -n "$CODE_ID" ]; then
  printf '%s\n' "$CODE_ID" > "$CODE_MARKER"
fi

systemctl daemon-reload
systemctl enable orchestrator-agent >/dev/null 2>&1 || true
# restart (not start) so a device that already ran an agent re-reads the new
# adoption file and re-adopts.
START_TS="$(date '+%Y-%m-%d %H:%M:%S')"
systemctl restart orchestrator-agent

# Wait for the agent to actually reach the platform, not just for the unit to
# be alive: success is reported only once the platform has accepted the
# agent's connection. Every other ending is non-zero and says what was seen.
#   5   the agent service is not running
#   8   the agent runs but did not reach the platform within the wait
#   9   the platform refused the device: the install is at its device cap
#   10  the platform refused the device: its account is at its device limit
#   11  the platform did not accept the device's credential
#   12  the board is bound to another account on the platform
#   13  the platform refused the device's identity as malformed or unsupported
#   14  the platform could not check its device limit (it clears on its own)
#
# The evidence is the agent's own log. It logs "gRPC connection established"
# when it has SENT its first message, before the platform answers. A refusal
# arrives later as a receive failure carrying the status code, and the
# platform holds a refused stream for up to 30 seconds before answering, so a
# connection counts as accepted only once a frame from the platform arrived
# (a command, or the device's own identity) or the connection has stood for
# CONNECT_SETTLE seconds with no refusal. Each new connection line starts that
# count again: the agent reconnects after a transient refusal.
CONNECT_WAIT=180
CONNECT_SETTLE=40
echo "Waiting for the agent to connect (up to $((CONNECT_WAIT / 60)) minutes)..."
waited=0
seen_conns=0
open_since=-1
while :; do
  if ! systemctl is-active --quiet orchestrator-agent; then
    echo "ERROR: the agent is installed but its service is not running. Inspect: journalctl -u orchestrator-agent -n 50" >&2
    exit 5
  fi
  AGENT_LOG=$(journalctl -u orchestrator-agent --since "$START_TS" --no-pager 2>/dev/null || true)
  # The last thing that happened to the connection, as "<connections>|<state>|<reason>".
  CONN=$(printf '%s\n' "$AGENT_LOG" | awk '
    /gRPC connection established/ { n++; st = "open"; why = ""; next }
    /msg="command received"/ || /reconnect identity adopted/ { if (st == "open") st = "accepted"; next }
    /the platform refused this device: its account is at its device limit/ { st = "account_limit"; why = ""; next }
    /the platform refused this device: the install is at its device cap/ { st = "cap"; why = ""; next }
    /still refused at the device cap/ { if (st != "account_limit") st = "cap"; why = ""; next }
    /failed to receive message/ && / code = [A-Za-z]+ desc = / {
      r = $0; sub(/.* code = /, "", r)
      code = r; sub(/ .*/, "", code)
      why = r; sub(/^[A-Za-z]+ desc = /, "", why); sub(/"[^"]*$/, "", why)
      st = "refused:" code
    }
    END { printf "%d|%s|%s", n, st, why }')
  CONN_N=${CONN%%|*}
  CONN_REST=${CONN#*|}
  CONN_STATE=${CONN_REST%%|*}
  CONN_WHY=${CONN_REST#*|}
  case "$CONN_STATE" in
    accepted)
      break
      ;;
    open)
      if [ "$CONN_N" != "$seen_conns" ]; then
        seen_conns=$CONN_N
        open_since=$waited
      elif [ $((waited - open_since)) -ge "$CONNECT_SETTLE" ]; then
        break
      fi
      ;;
    cap)
      echo "ERROR: the platform refused this device: the install has reached its device limit. The agent stays installed and asks again on its own; it is admitted once a device is removed on the platform or the limit is raised." >&2
      exit 9
      ;;
    account_limit)
      echo "ERROR: the platform refused this device: your account has reached its device limit. The agent stays installed and asks again on its own; it is admitted once a device is removed from the account or the account's limit is raised." >&2
      exit 10
      ;;
    refused:Unauthenticated)
      echo "ERROR: the platform did not accept this device's credential (\"$CONN_WHY\"). The token has expired or was revoked, or it belongs to a node that was deleted on the platform. Generate a fresh install command there and run it." >&2
      exit 11
      ;;
    refused:PermissionDenied)
      echo "ERROR: the platform refused this device (\"$CONN_WHY\"): this board is registered to another account on the platform. Remove it there first, or adopt it with a command from that account." >&2
      exit 12
      ;;
    refused:InvalidArgument)
      echo "ERROR: the platform refused this device's identity (\"$CONN_WHY\"). Adopting with an account token needs a board with a unique hardware serial; use a per-node install command (from a node created on the platform) instead." >&2
      exit 13
      ;;
  esac
  if [ "$waited" -ge $((CONNECT_WAIT + CONNECT_SETTLE)) ] ||
     { [ "$waited" -ge "$CONNECT_WAIT" ] && [ "$CONN_STATE" != open ]; }; then
    break
  fi
  sleep 3
  waited=$((waited + 3))
done
case "$CONN_STATE" in
  accepted|open)
    echo "OK: the agent has connected to the platform. A device adopted with an account token appears on the Nodes page as pending; approve it there."
    exit 0
    ;;
esac
# An agent that predates the account limit does not hold on that refusal; it
# reconnects, and its last receive failure names it.
if [ "$CONN_STATE" = "refused:ResourceExhausted" ] && [ "$CONN_WHY" = "account device limit reached" ]; then
  echo "ERROR: the platform refused this device: your account has reached its device limit. The agent stays installed and asks again on its own; it is admitted once a device is removed from the account or the account's limit is raised." >&2
  exit 10
fi
if [ "$CONN_STATE" = "refused:ResourceExhausted" ] && [ "$CONN_WHY" = "device cap could not be checked; retry shortly" ]; then
  echo "ERROR: the agent reached the platform, but the platform could not check its device limit, so it has not admitted this device yet. This clears on its own: the agent keeps asking, and there is no need to run the command again. If it persists, the platform's database is not answering." >&2
  exit 14
fi
echo "ERROR: the agent is installed and running, but it has not reached the platform within $((CONNECT_WAIT / 60)) minutes, so the platform has not seen this device yet." >&2
echo "The agent keeps trying on its own. Check that this device can reach $PLATFORM_URL (and the agent port), and read: journalctl -u orchestrator-agent -n 50" >&2
exit 8
`

// GetUninstallScript serves the agent removal script
// @Summary Agent uninstall script
// @Description Shell script that removes the orchestrator agent and everything it wrote from a device: `curl -fsSL https://<platform>/uninstall.sh | sudo sh`. It disarms the watchdog FIRST (an armed watchdog whose liveness file goes stale reboots the machine), then stops and purges the package and its state. SSH access artifacts are left alone unless `--purge-access` is given, and that is refused if it would remove the last way in.
// @Tags server
// @Produce plain
// @Success 200 {string} string "shell script"
// @Router /uninstall.sh [get]
func (h *Handler) GetUninstallScript(c echo.Context) error {
	// Unlike install.sh this needs no platform URL, no CDN and no signing keys:
	// it only removes local state. That is why it has no availability check —
	// an install with no agent CDN can still have agents in the field, and
	// refusing to serve the removal script there would strand exactly the
	// people who need it.
	//
	// The usage lines name the server the script came from, as install.sh's
	// do; a server with no usable address keeps a placeholder there.
	base, ok := h.scriptPlatformURL(c)
	if !ok {
		base = "<platform>"
	}
	script := strings.ReplaceAll(withPackageLockShell(uninstallScript), "__PLATFORM_URL__", base)
	return c.Blob(http.StatusOK, "text/x-shellscript; charset=utf-8", []byte(script))
}

// uninstallScript removes the agent and every file it owns.
//
// The ORDER is the load-bearing part and the reason this is a script rather
// than a paragraph in a runbook. watchdog.service supervises the agent through
// a liveness file (/run/orchestrator-agent/alive); stop the agent while the
// watchdog is armed and the file goes stale, which is precisely the condition
// the watchdog exists to reboot on. So the watchdog is disarmed first. A device
// that rebooted mid-uninstall would in fact recover — the watchdog's
// ExecStartPre gate is the agent binary, so once the package is gone the
// service simply fails to start — but "it recovers after an unexplained reboot"
// is not what an offboarding operator should have to discover.
//
// What it deliberately does NOT remove: the OS users the platform created. A
// user made by provision_connection is usually the operator's own login, and
// deleting it is the one irreversible mistake available here. Only the grants
// (our authorized_keys.d files, our sudoers drop-ins, our sshd config) are
// removable, and only on request.
const uninstallScript = `#!/bin/sh
# Orchestrator agent uninstaller.
#
# Usage:
#   curl -fsSL __PLATFORM_URL__/uninstall.sh | sudo sh
#   curl -fsSL __PLATFORM_URL__/uninstall.sh | sudo sh -s -- --purge-access
#
# Removes the agent (or the telemetry-only watcher), its adoption files and its
# state. The device goes offline on the platform once the agent stops;
# deleting the node there is a separate action, done after this one. A device
# set up for network boot also carries its node credential in the bootloader
# (EEPROM), which this script does not rewrite: it says so at the end, and
# deleting the node on the platform revokes it.
#
# Options:
#   --purge-access  also remove the SSH access the platform configured
#                   (authorized_keys.d entries, sudo grants, sshd drop-ins).
#                   Refused if it would leave no way to log in.
#   --force         skip that refusal. You are stating you have console access.
#   --dry-run       print what would be done and change nothing.
#
# Exit status: 0 done; 7 --purge-access refused; 8 a package is still
# installed (another package operation held the lock for too long, or the
# removal failed) — run it again.
set -eu

PURGE_ACCESS=""
FORCE=""
DRY=""

while [ $# -gt 0 ]; do
  case "$1" in
    --purge-access) PURGE_ACCESS=1; shift ;;
    --force) FORCE=1; shift ;;
    --dry-run) DRY=1; shift ;;
    -h|--help) sed -n '2,20p' "$0" 2>/dev/null || true; exit 0 ;;
    *) echo "ERROR: unknown argument: $1" >&2; exit 2 ;;
  esac
done

if [ "$(id -u)" != "0" ]; then
  echo "ERROR: must run as root (pipe into 'sudo sh')." >&2
  exit 2
fi

run() {
  if [ -n "$DRY" ]; then
    echo "  would run: $*"
  else
    "$@" >/dev/null 2>&1 || true
  fi
}
gone() {
  if [ -n "$DRY" ]; then
    # Not "[ -e ] && echo": under set -e a missing path would end the dry run.
    if [ -e "$1" ]; then echo "  would remove: $1"; fi
  else
    rm -rf "$1"
  fi
}

# 1. Watchdog first. An armed watchdog reboots the machine when the agent stops
#    touching /run/orchestrator-agent/alive, and stopping the agent is the very
#    next step. Disarming is not optional and not last.
#    Only the watchdog the platform configured: its unit drop-in, its system
#    drop-in, its marked /etc/watchdog.conf or the backup it keeps of the
#    file it replaced. A board whose owner runs a watchdog of their own, which
#    knows nothing of the agent, keeps it running.
if [ -e /etc/systemd/system/watchdog.service.d/10-orchestrator.conf ] ||
   [ -e /etc/systemd/system.conf.d/10-orchestrator-watchdog.conf ] ||
   [ -e /etc/watchdog.conf.orchestrator.bak ] ||
   grep -qs 'Managed by orchestrator' /etc/watchdog.conf; then
  echo "Disarming the watchdog the platform configured..."
  run systemctl disable --now watchdog.service
  run systemctl disable --now wd_keepalive.service
else
  echo "No watchdog configured by the platform; leaving the watchdog services as they are."
fi
gone /etc/systemd/system/watchdog.service.d/10-orchestrator.conf
gone /etc/systemd/system.conf.d/10-orchestrator-watchdog.conf
# The agent keeps a one-time backup of whatever /etc/watchdog.conf held before
# it first wrote there. Restoring it is more honest than deleting the file: a
# device that had its own watchdog configuration gets it back.
if [ -f /etc/watchdog.conf.orchestrator.bak ]; then
  if [ -n "$DRY" ]; then
    echo "  would restore: /etc/watchdog.conf from .orchestrator.bak"
  else
    mv -f /etc/watchdog.conf.orchestrator.bak /etc/watchdog.conf
  fi
elif grep -qs 'Managed by orchestrator' /etc/watchdog.conf 2>/dev/null; then
  # Our file with no backup beside it: the device had no watchdog.conf before.
  gone /etc/watchdog.conf
fi

# 2. The agent itself.
echo "Stopping the agent..."
run systemctl disable --now orchestrator-agent.service

__PACKAGE_LOCK_FUNCTIONS__
# Packages this run could not remove. A package left installed is reported
# at the end and makes the run fail: a removal that only looks done is worse
# than one that says it is not.
LEFT_INSTALLED=""
LEFT_REASON=""
# pkg_installed: the package manager still lists the package in any state
# other than removed (a removed package's leftover configuration does not
# count; it carries no program and no service).
pkg_installed() {
  _pkg_status=$(dpkg-query -W -f='${Status}' "$1" 2>/dev/null) || return 1
  case "$_pkg_status" in
    *not-installed|*config-files|'') return 1 ;;
  esac
  return 0
}
remove_package() {
  if [ -n "$DRY" ]; then
    echo "  would run: dpkg --purge $1"
    return 0
  fi
  if ! pkg_run dpkg --purge "$1"; then
    [ "$PKG_LOCK_TIMED_OUT" = 1 ] || pkg_run dpkg --remove "$1" || true
  fi
  if pkg_installed "$1"; then
    LEFT_INSTALLED="$LEFT_INSTALLED $1"
    LEFT_REASON="$PKG_ERR"
    echo "  FAILED: $1 is still installed: $PKG_ERR" >&2
  fi
}

echo "Removing the agent package..."
if command -v dpkg >/dev/null 2>&1 && pkg_installed orchestrator-agent; then
  remove_package orchestrator-agent
else
  echo "  (no orchestrator-agent package installed)"
fi
# Belt and braces for devices where the binary was placed by install_agent
# rather than by the package (the netboot recovery path does exactly that).
gone /usr/bin/orchestrator-agent
gone /etc/systemd/system/orchestrator-agent.service
gone /lib/systemd/system/orchestrator-agent.service

# The telemetry-only watcher is the other form of the agent. It holds a node
# credential of its own and would keep reporting with it.
echo "Stopping and removing the watcher (if installed)..."
run systemctl disable --now orchestrator-watcher.service
if command -v dpkg >/dev/null 2>&1 && pkg_installed orchestrator-watcher; then
  remove_package orchestrator-watcher
fi
gone /usr/bin/orchestrator-watcher
gone /etc/systemd/system/orchestrator-watcher.service
gone /lib/systemd/system/orchestrator-watcher.service

# 3. Identity and state. The adoption file is the device's credential for this
#    account; it is the one thing that must not survive an offboarding.
echo "Removing identity and state..."
gone /etc/orchestrator
gone /etc/orchestrator-watcher
gone /var/lib/orchestrator-agent
gone /run/orchestrator-agent
# An older agent delivered environment secrets as a marked block in
# /etc/environment, where every login session reads them. The block goes
# with the identity; lines an administrator added are left alone. Only a
# well-formed block is removed: exactly one begin line, then exactly one end
# line. Any other arrangement of the markers (one missing, out of order,
# repeated) leaves the file as it is, and the script says so.
ENV_BEGIN='# >>> orchestrator:environment >>>'
ENV_END='# <<< orchestrator:environment <<<'
if [ -f /etc/environment ] && { grep -qxF "$ENV_BEGIN" /etc/environment || grep -qxF "$ENV_END" /etc/environment; }; then
  if ! awk -v b="$ENV_BEGIN" -v e="$ENV_END" '
      $0==b { nb++; if (open) bad=1; open=1 }
      $0==e { ne++; if (!open) bad=1; open=0 }
      END { exit !(nb==1 && ne==1 && !bad) }' /etc/environment; then
    echo "  left /etc/environment unchanged: its orchestrator markers are not one begin line followed by one end line; remove that block by hand"
  elif [ -n "$DRY" ]; then
    echo "  would remove: the orchestrator block in /etc/environment"
  else
    # Rewritten in place (same inode, same mode); the scratch copy holds only
    # the lines that stay.
    ENV_TMP=$(mktemp)
    awk -v b="$ENV_BEGIN" -v e="$ENV_END" '$0==b{skip=1;next} $0==e{skip=0;next} !skip' /etc/environment > "$ENV_TMP" \
      && cat "$ENV_TMP" > /etc/environment
    rm -f "$ENV_TMP"
  fi
fi
run systemctl daemon-reload

# 4. SSH access, only on request.
if [ -n "$PURGE_ACCESS" ]; then
  echo "Checking that removing platform SSH access leaves a way in..."
  # Count logins that survive the removal: an account with keys in its OWN
  # ~/.ssh/authorized_keys, which the platform never writes to or reads, and
  # that can still log in. An account the platform locked keeps its home and
  # that file, but its expiry date has passed and its shell is nologin, so it
  # is not a way in. (A password field of "!" alone does not stop a key login,
  # so it does not disqualify an account.)
  PASSWD_FILE=/etc/passwd
  SHADOW_FILE=/etc/shadow
  SURVIVORS=0
  TODAY=$(( $(date +%s) / 86400 ))
  while IFS=: read -r u_name _ _ _ _ u_home u_shell; do
    [ -n "$u_home" ] || continue
    f="$u_home/.ssh/authorized_keys"
    [ -s "$f" ] || continue
    grep -qs '^[^#]' "$f" || continue
    case "$u_shell" in
      */nologin|*/false) continue ;;
    esac
    u_expire=$(awk -F: -v u="$u_name" '$1 == u { print $8; exit }' "$SHADOW_FILE" 2>/dev/null || true)
    case "$u_expire" in
      ''|*[!0-9]*) ;;
      *) [ "$u_expire" -gt "$TODAY" ] || continue ;;
    esac
    SURVIVORS=$((SURVIVORS + 1))
  done < "$PASSWD_FILE"
  if [ "$SURVIVORS" -eq 0 ] && [ -z "$FORCE" ]; then
    echo "REFUSED: no account that can log in has its own ~/.ssh/authorized_keys, so removing the" >&2
    echo "platform's SSH configuration would leave no way to log in over the network." >&2
    if [ -n "$DRY" ]; then
      echo "Dry run: nothing was removed. A real run would remove the agent and its identity, then stop here and skip only --purge-access." >&2
    elif [ -n "$LEFT_INSTALLED" ]; then
      echo "The agent was stopped and its identity removed, but the package manager still lists:$LEFT_INSTALLED; only --purge-access was skipped. Run this script again to finish." >&2
    else
      echo "The agent and its identity have already been removed; only --purge-access was skipped." >&2
    fi
    echo "Add a key of your own first, or re-run with --force if you have console access." >&2
    exit 7
  fi
  echo "Removing platform SSH access..."
  gone /etc/ssh/authorized_keys.d
  gone /etc/ssh/orchestrator_ca.d
  gone /etc/ssh/orchestrator_trusted_cas.pub
  gone /etc/ssh/auth_principals
  gone /etc/ssh/sshd_config.d/00-orchestrator-access.conf
  gone /etc/ssh/sshd_config.d/00-orchestrator.conf
  for f in /etc/sudoers.d/90-orchestrator-*; do
    [ -e "$f" ] || continue
    gone "$f"
  done
  # Older agents wrote into the main sshd_config between markers instead of a
  # drop-in; strip exactly that block and nothing else.
  if grep -qs 'orchestrator access config' /etc/ssh/sshd_config 2>/dev/null; then
    if [ -n "$DRY" ]; then
      echo "  would strip the orchestrator block from /etc/ssh/sshd_config"
    else
      cp /etc/ssh/sshd_config /etc/ssh/sshd_config.orchestrator-uninstall.bak
      sed -i '/# >>> orchestrator access config >>>/,/# <<< orchestrator access config <<</d' /etc/ssh/sshd_config
    fi
  fi
  # The group is our authority marker, not a login. Removing it leaves the user
  # accounts exactly as they are.
  run groupdel orchestrator-managed
  if [ -z "$DRY" ]; then
    if sshd -t >/dev/null 2>&1; then
      systemctl reload ssh >/dev/null 2>&1 || systemctl reload sshd >/dev/null 2>&1 || true
    else
      echo "WARNING: sshd rejects the configuration after removal; NOT reloading." >&2
      echo "Your current session stays up. Inspect: sshd -t" >&2
    fi
  fi
else
  echo "Leaving SSH access as configured (pass --purge-access to remove it)."
fi

# 5. What stays. A device the platform set up for network boot carries its node
#    credential in the bootloader configuration as well (UPTIME_TOKEN, baked
#    into the signed EEPROM image), and that copy outlives everything above.
#    Rewriting the bootloader is not something to do from an uninstall script,
#    so it is reported instead, together with the action that revokes it.
EEPROM_CRED=""
if command -v rpi-eeprom-config >/dev/null 2>&1 && BOOTCFG=$(rpi-eeprom-config 2>/dev/null); then
  EEPROM_CRED=no
  if printf '%s\n' "$BOOTCFG" | grep -q '^UPTIME_TOKEN='; then EEPROM_CRED=yes; fi
elif command -v vcgencmd >/dev/null 2>&1 && BOOTCFG=$(vcgencmd bootloader_config 2>/dev/null); then
  EEPROM_CRED=no
  if printf '%s\n' "$BOOTCFG" | grep -q '^UPTIME_TOKEN='; then EEPROM_CRED=yes; fi
elif grep -qs 'Raspberry Pi' /proc/device-tree/model; then
  EEPROM_CRED=unknown
fi

# Not touched, and deliberately: OS users the platform created (deleting one is
# usually deleting the operator's own login), and the marker block in
# /boot/firmware/config.txt written by overclock or patch_file, which describes
# how this hardware boots and is not ours to revert.
echo
if [ -n "$DRY" ]; then
  echo "Dry run complete — nothing was changed."
elif [ -n "$LEFT_INSTALLED" ]; then
  echo "NOT DONE: the agent is stopped and its adoption files are deleted from this disk," >&2
  echo "but the package manager still lists:$LEFT_INSTALLED" >&2
  echo "Reason: $LEFT_REASON" >&2
  echo "Run this script again once that has finished (it is safe to repeat)." >&2
else
  echo "Done. The agent is removed, and its adoption files are deleted from this disk."
fi
case "$EEPROM_CRED" in
  yes)
    echo
    echo "NOTE: this device's bootloader (EEPROM) still holds the node credential the"
    echo "platform baked into it (UPTIME_TOKEN in the bootloader configuration), and that"
    echo "credential is still valid. This script does not rewrite the bootloader."
    ;;
  unknown)
    echo
    echo "NOTE: the bootloader configuration of this device could not be read. If the"
    echo "platform set it up for network boot, its bootloader (EEPROM) still holds the"
    echo "node credential (UPTIME_TOKEN), and that credential is still valid. This script"
    echo "does not rewrite the bootloader."
    ;;
esac
if [ "$EEPROM_CRED" = yes ] || [ "$EEPROM_CRED" = unknown ]; then
  echo "To revoke it, delete the node on the platform: the platform refuses the"
  echo "credential of a deleted node. Then forget the deleted node there too: while its"
  echo "record exists, restoring it makes the same credential valid again."
else
  echo "It will stop appearing online on the platform; delete the node there to finish."
fi
if [ -n "$LEFT_INSTALLED" ]; then
  exit 8
fi
exit 0
`
