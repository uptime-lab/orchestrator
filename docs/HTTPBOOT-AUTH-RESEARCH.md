# HTTP-boot request authentication — research & recommendation

Status: research document and decision record — "IP-binding is the wrong
control". It is what `Config.Advisories()` points a reader at when the boot
failsafe is armed. It states an open limitation on purpose: the per-serial
boot path is unauthenticated by construction (§6), and the install-level rule
that makes that acceptable is in §2.1. `docs/SECURITY-ACCEPTANCES.md` argues
this acceptance again for a self-hosted install, beside the other accepted
risks.

**Where it stands:** Phase 0 and Phase 1 have SHIPPED (UA persistence removed;
the agent's gRPC report is the single writer of `nodes.bootloader_version`;
the `boot_http_path` readback is stored). **Phase 2 — the per-device path
secret — remains OPEN**; see §6. The design lives here.

Code is cited by **symbol name**, never by line number: the line numbers this
document used to carry all rotted, several outliving the code they pointed at.

---

## 1. The problem, restated against the real topology

The public netboot endpoint `GET /http-boot/:serial/:file`
(`httpboot.Handler.ServeBootFile`) identifies a device by the **serial in the
URL only**. The serial is printed on the hardware and sits in inventories — it
is not a secret. Two things ride on that identification: the per-device
serve/withhold decision (`shouldBootLocalOS`, driven by effective `boot_to_os`)
and the boot-loop failsafe counter.

A third used to: the ROM's User-Agent was persisted as `bootloader_version` /
`bootloader_timestamp` behind an IP-binding gate (request IP == the
agent-reported *private LAN* address `n.IPv4`). That write path has been
**deleted**; `logBootloaderUA` survives and only logs. The authoritative writer
is the agent over its gRPC stream, including from the recovery bootstrap. The
conclusion that removal rests on generalises to everything below:

> **IP-binding is simultaneously too strict (NAT false negatives — a cluster
> behind NAT never matches) and too weak (NAT false positives
> — any sibling behind the same egress IP passed it). It cannot be repaired by
> tuning; the binding has to come from something the device knows or something
> an authenticated channel asserts.**

---

## 2. What the code actually does today (verified paths)

### 2.1 Bundle serving

- Route registration carries a per-IP rate limiter (20 rps, burst 300) and the
  unknown-serial auto-ban guard (`netbootGuard`: 1-minute window, 20 strikes,
  10-minute ban). The guard is NAT-aware *for legitimate clusters* — a cluster
  only ever requests serials that resolve, so it never strikes. Its lock is
  taken by every netboot request, so its work does not grow with the addresses
  seen: expired entries are swept at most once a minute, and it tracks at most
  65 536 addresses — while full, a new address is not tracked (and so not
  banned) until the next sweep, and every address already tracked keeps its
  strikes and its ban. A flood from many addresses, one request each, is not
  what the ban is for.
- Node lookup is **global across accounts** (`GetByHardwareSerialGlobal`), and
  it does not stop at an exact match: for a short request it also collects the
  **low-word suffix matches** (`right(hardware_serial, n) = ?`), because a
  WebUSB-flashed device keeps asking for the 8-hex path baked into its EEPROM
  after its node rebinds to the full serial.
  That fallback is an authentication surface, because **the ban guard strikes
  only when a lookup FAILS — and a suffix probe SUCCEEDS**, so probing was
  invisible and accrued no strikes. Bounded now: the fallback
  requires >= 8 characters, `serialRegex` rejects anything shorter before the
  database is touched, and `ServeBootFile` detects a suffix hit (stored serial
  differs from the requested one), logs it and strikes — unless the node's own
  EEPROM readback (`boot_http_path`, reported over the authenticated stream)
  names exactly the requested path, which is that device's normal boot.
  A short serial is not unique, so an exact short stamp never wins on its own
  across accounts (`node.ResolveBootCandidate`): the node whose readback names
  the path wins, and a set that still spans accounts refuses rather than
  guessing. Collisions are prevented at **bind** time
  (`guardSerialBinding`, `ErrHardwareSerialConflict`; a short-serial WebUSB
  bake is refused when another account holds a full serial ending in it) — the
  one-account-per-serial gate, in place.
- **A DELETED node's serial is exempt from the strike.** Nothing tells a device
  its node row is gone, so it asks for its own `boot.sig` forever; counting that
  as enumeration lets one stranded node slowly ban the site it sits in.
  `findDeleted` recognises the serial and answers 404 with no strike.
- Fail-open vs fail-closed on an unknown serial is profile-driven
  (`Provisioning.HTTPBootFailOpen`): a LAN install may serve unadopted
  devices; an internet-facing install 404s (the withhold-by-default gate, in place).
- **The served file is the same for every serial**: `serveFile` joins
  `HTTPBootDir` with just the filename — the serial decides only *whether* to
  serve. The bundle is generic and deliberately public: `boot.img` holds no
  secrets (adoption identity lives in the EEPROM `UPTIME_TOKEN`), and its
  integrity comes from `boot.sig`, which the ROM RSA-verifies against the
  **EEPROM-embedded public key** (`docs/NETWORK-FIRST.md`).
- **Boot-loop failsafe** counts *withheld* `boot.sig` requests
  (`NodeService.RegisterNetbootWithhold`) and, past a threshold, flips to
  serving recovery for a 3-minute window (`failsafeServeWindow`). Note for
  §4.D: this counter is fed by **unauthenticated** requests.
  **The rule: an install whose boot host is reachable from the internet runs
  the failsafe OFF** (`PROVISIONING_BOOT_FAILSAFE_THRESHOLD=0`). This
  document's acceptance of the unauthenticated per-serial path *depends* on
  the failsafe being off, and an acceptance that rests on a configuration
  value is silently invalidated the moment that value changes — which is why
  `Config.Advisories()` warns at every start when the two disagree. **A LAN
  install may stay armed on purpose**: its boot host is not reachable by
  strangers, and it is where a bricked bench node most needs to recover
  itself.
  **The cost is real and belongs here, not only in a runbook:** with the
  failsafe off, the platform does not rescue a node that cannot boot its OS.
  That is an operator action (flip `boot_to_os`, or Reboot to recovery). If
  that trade stops being acceptable, the fix is not to re-arm it silently — it
  is Option A (authenticate the path), after which the counter can be armed
  again.

### 2.2 How the per-serial HTTP_PATH is minted (the entropy question)

The signer bakes, per device (`eeprom-signer/sign.sh`), `BOOT_ORDER`,
`HTTP_HOST`, `UPTIME_TOKEN=<base64 adoption blob>` and
`HTTP_PATH="${HTTP_PATH_PREFIX}/${SERIAL}"`.

**`HTTP_PATH` today carries zero secret entropy** — the literal `http-boot/`
plus the public hardware serial (the signer's default prefix). It exists
because the ROM does **no path
substitution** — `{pi_serial_number}` is TFTP-only; HTTP_PATH is literal — so
per-device identity *must* be baked in. It was never designed as a credential;
it merely routes.

Two primitives a secret-bearing path would reuse already exist: the EEPROM
already carries a real per-device secret (the `UPTIME_TOKEN` blob embeds the
node's `unique_token`, a UUIDv4 of ~122 bits), and `infrastructure/cdnsign`
(HMAC-SHA256 over path+expiry, keyed by `CDN_SIGNING_KEY` with domain separation; an unset key falls back
to the JWT secret only on a development install, and production refuses to
boot without one)
is the platform's existing answer to "knowing a path must not be enough".

### 2.3 The authenticated channel that already exists

Every managed node holds a long-lived gRPC stream authenticated by
`node_id + unique_token` (validated constant-time in `grpc/server.go`), and the
agent already reports EEPROM-derived facts over it — `BootInfo.boot_http_host`
(agent ≥0.2.19, read via `rpi-eeprom-config`), so the platform can detect a
device pointed at the wrong install. **The pattern "agent reads EEPROM state,
reports it authenticated" is proven and shipped**; `BootInfo.bootloader_version`
is the same pattern and now the only writer of that column.

---

## 3. Hard constraints: what the Pi ROM can and cannot do

These are fixed. The bootloader is ROM + signed EEPROM firmware from Raspberry
Pi; we configure it, we do not program it.

| Capability | Status |
|---|---|
| Plain HTTP GET of `HTTP_PATH/boot.img` + `boot.sig` | Yes — that is *all* it does |
| HTTPS | **CM4 (BCM2711): never.** CM5: only with `HTTP_CACERT_HASH` baked (bootloader ≥2024-04-05), untested here, useless while CM4 is in a fleet. Doctrine: netboot is plaintext :80, trust = RSA `boot.sig` |
| Follow 30x redirects | No — measured on hardware; a reverse proxy that redirects :80 to :443 breaks netboot until the boot routes get a carve-out router that serves them plain |
| Custom headers, cookies, computed query strings, challenge–response, client certs, request signing | **No.** The only device-controlled request content is the literal `HTTP_PATH` and the fixed-format User-Agent |
| Verify the *response* (boot.img against boot.sig, RSA-2048, EEPROM-embedded pubkey; netboot always verifies) | Yes |
| Per-device request identity | Only what is baked into `HTTP_PATH` (literal) + the serial in the UA |

Consequence: **the server can never authenticate the ROM cryptographically at
request time.** The strongest achievable request-time binding is a **bearer
secret in the URL path**, delivered over plaintext HTTP. Anything stronger must
come *out of band* — i.e., over the authenticated gRPC channel, after boot.

---

## 4. Options analysis

### Option A — per-device secret in the boot path (capability URL)

**Idea.** Bake `HTTP_PATH=http-boot/<serial>/<secret>` (or `<serial>-<secret>`)
at signer time; store the secret (hashed) on the node row; the handler requires
it. The serial stays in the path for operability (logs, routing, guard).

**Is today's HTTP_PATH "unguessable enough" to be the binding? No — it contains
no secret at all** (§2.2). The real question is whether entropy can be *added*,
and it can: the ROM treats the path as an opaque literal, `UPTIME_TOKEN` proves
the EEPROM tolerates baked per-device secrets, and the signer already
regenerates the EEPROM per device on `provision-eeprom`. Generate 128 bits with
a CSPRNG at provision time; store only a hash (the `domain/adoption_code`
posture).

**Security properties.**
- Defeats the *remote* poisoner and the *remote* policy oracle outright: knowing
  a serial no longer lets anyone query `boot_to_os` state or feed the failsafe
  counter.
- Defeats the **NAT-sibling** attacker *unless* that sibling can observe the
  netboot traffic. Honest limit: the secret travels **plaintext on the LAN**
  every boot (CM4 cannot do TLS, §3). An attacker with L2 access who can
  ARP-spoof / port-mirror the segment captures it. This is the same channel the
  RPi's own Network Install accepts for millions of devices, and the *bundle*
  remains signature-protected regardless — but as an authentication claim the
  secret is only as private as the site's L2. It raises the bar from
  "knows a serial printed on the case" to "has an active tap on the victim's
  boot-time LAN traffic", which is a real and worthwhile jump, not a
  cryptographic guarantee.
- Rotation is possible remotely: a managed OS re-flashes a re-baked EEPROM via
  `rpi-eeprom-update -d -f` (key rotation was proven exactly this way); a
  purely-netbooted device **cannot** self-reflash, so rotation rides the existing
  provision-eeprom lifecycle rather than getting its own.

**What breaks / migration.** The entire fielded fleet has secretless EEPROMs, so
the handler must accept both shapes per node: `/http-boot/:serial/:file`
(legacy) and `/http-boot/:serial/:secret/:file` (new), keyed on a per-node
"secret provisioned & confirmed" state. Confirmation reuses the `boot_http_host`
readback pattern (§2.3): extend the agent's EEPROM readback with
`boot_http_path` so the backend flips a node to *require* the secret only after
the agent confirms the new path is actually flashed — otherwise a failed flash
strands a device un-netbootable. Until confirmed, the legacy path serves but is
treated as unauthenticated (no failsafe counting, or counted separately).

**Work required.** A node column (+hash), the second route and its per-node
enforcement state, the provision flow plumbing the secret into the signer call,
one env var in `sign.sh`, the agent readback, and a per-device re-bake —
**gradual, fleet-paced; no hardware capability missing.**

**Not a fit for:** telemetry *authority*. A path secret proves "a party that
knows this EEPROM's config" sent the request — good enough to gate a counter,
not the right source of truth for what firmware a device runs when an
authenticated agent can state it directly (which it now does).

### Option B — signed boot requests

**Infeasible, full stop.** The ROM computes nothing per-request: no nonce, no
HMAC, no client TLS, no writable request surface beyond the literal path (§3),
and no firmware hook to add one — we bake config *values* into Raspberry Pi's
signed EEPROM firmware, not code. Any "signed request" design degenerates into
Option A, because a static path *is* the only thing the device can present. The
cryptography here necessarily points the other way: the **response** is signed
(`boot.sig`), and that part already works. Reject and stop revisiting.

### Option D — implications for serving the bundle itself over WAN, and cross-tenant gates

Bundle serving needs **less** authentication than telemetry, not more, because
the trust model was built for an unauthenticatable client. **Confidentiality is
not required** (one shared public bundle; never put per-device data into
`boot.img` — secrets ride the EEPROM `UPTIME_TOKEN` and the HMAC-gated by-serial
packages) and **integrity is already solved** by `boot.sig` against the
EEPROM-baked pubkey, so a WAN MITM can only break the boot into NVMe
fallthrough, never substitute an image.

- **What actually needs protecting** is the *decision metadata*:
  1. **Policy oracle** — for a *known* serial, response code = effective
     `boot_to_os`. The guard only covers unknown and suffix-matched serials.
     Low severity (an attacker learns netboot-vs-local intent), closed by
     Option A's secret.
  2. **Failsafe counter poisoning** — `RegisterNetbootWithhold` increments on
     an unauthenticated withheld `boot.sig` request, so someone who knows a
     serial could pre-trip the threshold and divert the device's next legitimate
     reboot into recovery netboot instead of its OS. Two defences stand in the
     way, and the second is the stronger. The feature is opt-in (threshold 0 =
     off, `Provisioning.BootFailsafeThreshold`, and 0 is what an
     internet-facing install runs). And
     `RegisterNetbootWithhold` **declines to count a withhold at all while the
     node's `LastHeartbeat` is inside `bootFailsafeLiveness` (5 minutes)** — a
     real crash-loop keeps no agent connected, so suppressing the count for a
     recently-alive node costs the feature nothing and means **a healthy node
     cannot be pre-tripped**. What stays reachable is a node that is *already
     offline*, which is exactly what `Config.Advisories()` says. Still an
     unauthenticated write of the same class as the removed telemetry one;
     closed properly by Option A.
- **Cross-tenant gates.** Four principles, all in place, and what to preserve
  when the code above moves. **One account per serial**: a serial binds to at most one account, at
  WRITE time rather than read time (`guardSerialBinding`;
  `GetByHardwareSerialGlobal` refuses a multi-account resolution). **Capability, not serial**:
  by-serial signer output is fetched with a capability, never with the serial
  alone (`cdnsign` HMAC), and the *authorization to re-bake* lives in the
  authenticated, account-scoped `eeprom_provision` path. **Withhold by default**: a public
  endpoint's default on an unknown serial is to withhold (`HTTPBootFailOpen`
  false on an internet-facing install). **Authenticated claims only**: a claim about a device's EEPROM
  is believed only over
  the authenticated channel (`boot_http_host` readback). Option A strengthens
  the capability gate and the agent-reported bootloader strengthens the
  authenticated-claims gate; neither
  weakens any gate.

---

## 5. Recommendation (phased; telemetry and bundle-serving deliberately separated)

**Principle: telemetry authentication and bundle-serving authentication are
different problems with different right answers.** Telemetry moved to the
already-authenticated channel. Bundle serving keeps its "public signed artifact,
per-device *decision*" model and gains a bearer secret (Option A) only to protect
the decision metadata, on the normal EEPROM lifecycle, with no flag-day.

### Phase 1 — agent release (proto + agent + backend)
4. ~~`BootInfo.bootloader_version` (+ build timestamp) reported on connect +
   system-info; backend prefers it unconditionally; UA persistence removed.~~
   **Done.**
5. **`BootInfo.boot_http_path` readback** (companion to `boot_http_host`) —
   **Done**: the field is reported by the agent and stored as
   `node.BootHTTPPath` (the *actually flashed* path). This was the
   prerequisite for Phase 2's safe migration.

### Phase 2 — signer/EEPROM change, fleet-paced (no firmware capability needed)
6. **Path secret** (Option A) — **open.** Per-node enforcement flips to
   *require* only after the agent's `boot_http_path` readback confirms the
   flash; re-bake opportunistically on every `provision-eeprom` / bootloader
   update, so the fleet converges with no dedicated truck-rolls. Once enforced
   per node: policy oracle closed, failsafe counter authenticated, and an
   internet-facing install may arm the failsafe again.
7. **Never** build per-device bundle content or move bundle trust off
   `boot.sig` — plaintext :80 + signature verification is the load-bearing
   design for CM4 and is fine (§3, §4.D).

### Explicitly rejected
- **Signed boot requests (Option B):** no ROM capability, unfixable (§4.B).
- **Repairing IP-binding** (allowlists, CIDR matching, trusting egress IPs):
  still identifies the NAT, not the device.
- **A backend-only correlation window** (commit the UA value only if the node's
  agent connects reporting `current_boot_device == "http"` shortly after): a
  poisoner who *times* the request still wins, and the agent report superseded
  it anyway.

---

## 6. What is still unbuilt (stated plainly, because readers arrive here from a config warning)

One thing this document designs does not exist in the code today. It is not
blocked by hardware; it is simply unbuilt.

1. **The per-device path secret.** `eeprom-signer/sign.sh` still bakes
   `HTTP_PATH="${HTTP_PATH_PREFIX}/${SERIAL}"` — prefix plus public serial,
   **zero entropy**. No node column, no second route shape, no enforcement
   state. Until it ships the per-serial boot path is unauthenticated by
   construction, which is what §2.1's acceptance accepts and what
   `Config.Advisories()` demands be re-argued before the failsafe is armed.
   (The `boot_http_path` readback that Phase 2's safe migration needs has
   since shipped, so only the path secret itself is unbuilt.)
