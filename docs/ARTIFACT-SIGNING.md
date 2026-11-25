# Artifact signing — extending the boot-image trust anchor forward

> This is the design record. Which hardware tokens an install trusts is the
> operator's; a fork replaces the key set (see "What a fork must replace").

## Trusted signing keys

Two independent hardware tokens can sign releases; verification everywhere
accepts ANY trusted key (`keys/*.pem` embed globs), so either token suffices:

| Key | Token | Slot / algo | Role |
|---|---|---|---|
| second signing key | a YubiKey | PIV 9c, ECDSA-P384, touch per signature | **primary** |
| first signing key | a YubiKey | PIV 9c, ECDSA-P384, touch per signature | backup (original key) |

Trust stores, each holding the same public PEMs: `signing/trusted-keys/` (source
of truth), `agent/pkg/signing/keys/` (embedded — agent verify) and
`backend/pkg/webapp/infrastructure/signing/keys/` (embedded — `install.sh`
verify, and the server's own check in `--import-agent-release`). Sign with either token:
`tools/agent-signer/agent-signer sign -serial <token serial> -in FILE` (primary first).
**Devices only trust what their embedded key set contains** — which dictates the
**bridge-release rule**: sign a release with a key the FIELDED fleet already
trusts, not merely a key in the repo. Both sides were proven on hardware when
the second key was introduced: an agent that embedded only the first key
REFUSED the artifact signed by the second ("signature does not match any
trusted key", fail-closed, agent kept running), and accepted the same artifact
re-signed by the first. Once every fielded agent embeds both keys, either token
can sign; until then, sign with the key the oldest fielded agent trusts. The
same applies to any future third key: ship it embedded first, sign with it one
fleet-generation later.

## The problem in one sentence

The Raspberry Pi verifies `boot.img` at ROM stage against a public key embedded in the
EEPROM image — a SOFT anchor as shipped: OTP is not fused (`CUSTOMER_KEY_HASH=0`), so the
key is reflashable with physical access, deliberately (the tradeoff and the optional OTP
burn are documented below). When this file was first written, that trust stopped at
`boot.img` — every later artifact fell back to a same-channel SHA256, which protects
against corruption, not against a malicious/MITM'd server. The chain HAS since been
extended to agent artifacts (detached signatures, fail-closed); the table below is the
current state, and the OS-image link is the one that remains different by design.

## Current trust chain

| Link | Transport | Integrity | Authenticity |
|------|-----------|-----------|--------------|
| ROM → `boot.img` | HTTP (ROM can't do HTTPS) | RSA sig `boot.sig` via `rpi-eeprom-digest` | **Yes** — pubkey embedded in EEPROM; netboot RSA-verifies against it. `SIGNED_BOOT` is deliberately **NOT** set (so an unsigned local OS still boots); OTP is **not** fused (`CUSTOMER_KEY_HASH=0`) → the trust key is reflashable, not hardware-locked. See `docs/NETWORK-FIRST.md`. |
| `boot.img` → agent binary | none — nothing is fetched | the agent binary is part of `boot.img`'s rootfs | **Yes, inherited** — the recovery image's `S99agent` runs only the agent baked into the image (its download step was removed), so the binary is covered by the same `boot.sig` the ROM verifies. A new agent reaches recovery only by rebuilding and re-signing `boot.img`. |
| agent → OS image | HTTP or HTTPS | SHA256 in same gRPC command | **Partly** — the checksum is only as trustworthy as the channel carrying it. When the platform URL is https the agent dials gRPC with verifying TLS, so an on-path attacker cannot rewrite it; on an install whose platform URL has no https scheme the gRPC channel is plaintext with no server authentication and MITM swaps both. The digest is also ENFORCED: the agent holds the image head in RAM and never commits it unless the digest matches. |
| agent → `.deb` | HTTP or HTTPS (the server's `/cdn`) | detached `.sig` (ECDSA P-384 over SHA-384) | **Yes** — the agent verifies the package against the key set embedded in its own binary (`agent/pkg/signing`), fail-closed, before installing it: into the target OS during an OS install, and on `update_agent`. The agent doing the checking is either the ROM-verified recovery agent or one that was itself verified when installed. |
| `install.sh` → `.deb` | HTTPS (refused without `--insecure` otherwise) | detached `.sig` | **Yes** — the server bakes its embedded key set (`backend/pkg/webapp/infrastructure/signing/keys/`) into the `install.sh` it serves, and the script checks the package with `openssl dgst -sha384 -verify` before `dpkg -i`. The script itself is as authentic as the HTTPS connection that delivered it. |

## Design principle

Integrity via **signature** is the real requirement; transport encryption (TLS) is a separate
requirement for **confidentiality**. The proven ROM boot path uses HTTP, so for the two
artifact classes that carry their OWN fail-closed signature — `boot.img` (RSA via `boot.sig`)
and the agent artifacts (detached ECDSA `.sig`) — HTTP transport is acceptable and deliberate.

**That thesis does NOT extend to OS images.** They have no independent signature (the signed
manifest was considered and rejected below), so their protection is the gRPC-delivered digest
plus the channel that carries it — which is why production REFUSES a plaintext image URL
without an explicit override, and why the agent holds the image head back until the digest
matches (`docs/OS-IMAGES.md` has the authoritative narrow wording). TLS remains required for
the gRPC control channel. One more caution: newer bootloaders document `HTTP_CACERT_HASH`
(HTTPS boot with a CA embedded in the EEPROM), so "the ROM cannot do HTTPS" is no longer
universally true — untested on the supported boards and firmware, a hardware-in-the-loop
experiment for later, and not a reason to change the proven path today.

## Extending the chain past `boot.img`

The **artifact-signing keys are separate from the boot.img key** so they can rotate
independently; their private halves never leave a hardware token. The public halves
are not a file in the boot image: they are **embedded in the agent binary**
(`//go:embed keys/*.pem` in `agent/pkg/signing`). Since that binary is part of the
ROM-verified `boot.img`, the key set it carries is itself trustworthy — that is what
extends the chain forward: boot.sig authenticates the image, the image carries the
agent, and the agent's embedded keys authenticate every package it installs later.

**Status: the agent-artifact chain is BUILT.** Two hardware-token keypairs
(above), a detached `.sig` per `.deb`, fail-closed verification in the agent
(OS install and `update_agent`) and in `install.sh` — negative-tested on hardware,
where an untrusted signature is refused. The recovery image fetches no agent at
all, so there is no download on the netboot path to verify. (The backend still
registers `/net_install/agent/:file`, which no current recovery image calls.)

### OS-image (`install_os`) payload signing — EXAMINED AND REJECTED. Do not build this.

Two things settled it. The **write-before-verify half was the real defect**, and it
is fixed without any PKI (agent 0.3.5): the agent holds the image head in RAM and
commits it only once the digest matches, so a bad image never becomes bootable. And
the **anti-rollback policy** a signed-manifest design carries would fight this
product's own backup-restore feature — restoring an older capture *is* a deliberate
rollback, and a rule against it breaks the feature it would be protecting.

So the agent trusts the backend for images by design, and that trust boundary is a
closed decision, not deferred work; earlier revisions of this file called it
"deliberately deferred", which read as work waiting to be picked up.

## What still needs TLS (not solved by signing)

Signing authenticates an artifact; it hides nothing. The gRPC command channel
still carries material worth encrypting — environment secrets delivered with
`install_os`/`provision_connection`, and the node's own adoption identity — so TLS
remains a separate requirement wherever the platform URL is not https (a LAN
install serving plain HTTP, for instance). What it no longer carries is a cleartext
password: `install_os`'s `username`/`password`/`ssh_key` fields are RETIRED and
unpopulated, and `provision_connection` sends a crypt(3) SHA-512 hash instead (agent
floor 0.5.0). The two measures stay complementary: signing = "the artifact is
authentic wherever it came from"; TLS = "what travels with the command can't be
sniffed".

## Hardware verification
Confirmed on CM4/CM5: the pubkey is **embedded** in the EEPROM (netboot verifies against it) but
OTP is **not** fused (`CUSTOMER_KEY_HASH=0`) and `SIGNED_BOOT` is deliberately left unset — this is
the intended model (see `docs/NETWORK-FIRST.md`), which keeps the local OS bootable unsigned and the
EEPROM reflashable. **Consequence:** link 1's authenticity rests on the *embedded, reflashable* key,
not a hardware OTP lock — so an attacker with physical/usbboot or managed-OS access could reflash a
different key. Full hardware root-of-trust (OTP fuse + signed managed-OS `boot.img`) remains optional
future hardening, not shipped.

## What a fork must replace

The trust anchor is the key SET, and it lives in three places that must agree
(`tools/publication-guard/check_trust_stores.sh` fails CI when they do not):

- `signing/trusted-keys/` — the source of truth: one public PEM per signing
  token.
- `agent/pkg/signing/keys/` — the copy embedded into the agent binaries
  (`//go:embed keys/*.pem` in `agent/pkg/signing`). This is what a device checks
  a `.deb` against; the recovery image's agent carries it inside the signed
  `boot.img`.
- `backend/pkg/webapp/infrastructure/signing/keys/` — the copy embedded into
  the backend (`//go:embed keys/*.pem` in that package), which the server bakes
  into the `install.sh` it hands out, so the script verifies the agent package
  before `dpkg -i` runs, and verifies against itself when a release is
  imported (`server --import-agent-release`, a mirror of the agent's
  verifier in `signing/verify.go`), refusing to publish an unsigned package.

A fork that signs its own agent releases generates its own keypair(s) on
hardware tokens (PIV slot 9c, ECDSA-P384 — what `tools/agent-signer` expects),
puts the public halves into all three directories, removes the upstream PEMs,
and rebuilds BOTH the agent and the backend. Replacing only the agent copy
leaves the release import and `install.sh` refusing the fork's packages; replacing only the backend
copy leaves fielded agents refusing them. Agents already in the field trust
only what they were built with, so the bridge-release rule above applies to
the fork's first key exactly as it did to the second key here: ship the new
key embedded first, sign with it one fleet-generation later. The agent's
embedded-key test (`TestEmbeddedTrustedKeys`) requires at least one P-384 key
in the set, so an empty directory fails the build's tests rather than
shipping a binary that trusts nothing.

The boot-image key is separate (`docs/NETWORK-FIRST.md`): the EEPROM-embedded
RSA key that `boot.sig` is verified against is held by the eeprom-signer
container, and a fork reaches its devices with its own key through the
per-serial EEPROM bake and the `boot.img` it signs.

## Still open

- **The OTP-burn decision** — genuinely open, and still optional hardening rather
  than a gap (see "Hardware verification" above). It is the only item from this
  document's original plan that is neither built nor rejected.
