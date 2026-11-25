# Network-first boot

How managed Raspberry Pi devices boot, get onboarded, and are controlled by the platform.

**Boards.** Which boards are supported is stated in `docs/SUPPORTED-HARDWARE.md`: the
Raspberry Pi 4, the Raspberry Pi 5, the Compute Module 4 and the Compute Module 5. The
design was written for the Compute Modules and the text below often names them; what it
says of the CM4 applies to the Pi 4 (BCM2711) and what it says of the CM5 to the Pi 5
(BCM2712). The per-board device trees live in the recovery image and the revision-code
decoding in `domain/node/board_revision.go`.

The model: every device is **HTTP-boot first with a local (NVMe) fallback**. On each
boot the ROM asks the platform for its bootstrap image; the platform **serves** it
(→ device netboots the RAM bootstrap: adopt / install / maintenance) or **withholds**
it (→ ROM falls through to NVMe and boots the installed OS). PXE-style central control,
no `kexec`, resilient when the server is down.

---

## The bootloader (EEPROM) configuration

One per-device EEPROM, flashed at onboarding:

```
BOOT_ORDER=0xf1467                      # http -> nvme -> usb -> sd -> restart (http-first)
HTTP_HOST=<platform>                    # e.g. 192.0.2.10
HTTP_PATH=http-boot/<REAL-SERIAL>       # the device's actual serial, baked in
UPTIME_TOKEN=<base64 adoption blob>     # how/where to adopt (see "Adoption token")
PCIE_PROBE=1                            # NVMe detection
# our RSA public key is EMBEDDED (rpi-eeprom-config --pubkey)
# NOTE: SIGNED_BOOT is deliberately NOT set
```

**Key insight (the crux of the whole design):** embed our public key but do **NOT**
enable `SIGNED_BOOT`. Then:

- **Netboot** always verifies `boot.sig` — against our **embedded** pubkey → our signed
  bootstrap boots. (RPi5/CM5 network boot *always* RSA-verifies; there is no unsigned
  netboot.)
- **Local NVMe boot** is **not** enforced (SIGNED_BOOT unset), so a stock OS on NVMe
  (which ships no `boot.sig`) boots normally via loose files.

Secure netboot **and** an unsigned stock OS, unattended, with no OS `boot.img` signing.

Why not the obvious alternatives:
- `SIGNED_BOOT=1` → forces the local NVMe OS to also present a signed `boot.img`; stock
  Ubuntu has none → `secure-boot → Loading boot.img → Error 6`, won't boot locally.
- `SIGNED_BOOT=0` **without** our pubkey → netboot still verifies `boot.sig` against the
  built-in RPi key → our signature fails (`rsa-verify fail 0x4380`, or `Invalid RSA
  signature size 0` for a hash-only sig).
- `{pi_serial_number}` is **not** a firmware substitution token — `HTTP_PATH` is a literal
  path; a device with `HTTP_PATH=http-boot/{pi_serial_number}` requests
  `/http-boot/%7Bpi_serial_number%7D/...` verbatim. So each device must carry its **real**
  serial in `HTTP_PATH` (per-device EEPROM). Serial substitution exists only for TFTP.

`SIGNED_BOOT` is a single global EEPROM flag (no "signed netboot + loose local boot"); our
setup is the *soft* variant (key in the EEPROM config, `CUSTOMER_KEY_HASH=0` / not OTP →
reflashable via usbboot). For comparison, standard `rpi-imager` HTTP boot works with no
customer setup because RPi's own net-install image is signed with RPi's built-in key.

---

## Serve/withhold control — the `boot_to_os` policy

The netboot endpoint decides per device from the effective **`boot_to_os`** policy
(node → OU → root → account default, resolved by
`organizational_unit.ResolveEffectivePolicy`):

| `boot_to_os` | endpoint | result |
|---|---|---|
| off / inherit (default) | serves `boot.img` (200) | device **netboots** the bootstrap |
| **on** | 404 (withhold) | http fails → ROM → **NVMe → USB** → boots the installed OS |
| **on**, but the boot-loop failsafe fired | serves `boot.img` (200) **despite `on`** | device netboots into recovery so an operator can reach it |
| **on**, but an `install_os` to the node has not settled | serves `boot.img` (200) **despite `on`** | a device reset mid-write netboots into recovery instead of booting a partial disk |

An `install_os` that ends `failed` — reported by the agent, or settled by the platform
because the node stopped answering or came back as a new agent process — forces the
node's `boot_to_os` off, so the partial disk is not booted after the install has
settled either; set it back once an install has succeeded. An `erase_drive` that
succeeds on a node running the recovery system does the same — the disk it erased may be
the one the node boots, and the reboot-to-recovery one-shot would otherwise have put
*Local OS* back — so the node stays on recovery until an install succeeds or the target is
set. (An erase from a running OS changes no target: the device refuses to erase the disk
it runs from.) What the server cannot do is
reach a device whose ROM never asks it: with the failsafe off, a board that booted a
partial disk before the platform could serve it recovery needs a power cycle.

So **"go to the local OS" = set `boot_to_os = on`**; "netboot for install/maintenance" =
off. The order is a **baked default, changed only by re-flashing a signer-generated
package** — never by editing the EEPROM config on the device (see the boot-order gotcha
below, which is what "never change it" has always meant). Do NOT confuse with
`network_first` (that flag only triggers the auto-provision-on-approve; it does not affect
serving).

USB boot applies to **every** supported board — Pi 4, Pi 5, CM4 and CM5 all boot a
USB disk, and installing to an external SSD is a normal choice on any of them. The digit
sits after NVMe so a board booting from NVMe is unaffected, while everything else reaches
its disk instead of falling through to the provisioning SD card the operator was
told to remove. There is no SATA digit in `BOOT_ORDER`: a SATA SSD boots through a
USB-SATA bridge and is covered by the USB digit; a PCIe SATA HBA is not bootable by the
ROM at all, which boots NVMe only over PCIe.

**One-shot recovery boot** (`POST /api/v1/nodes/:id/reboot-to-recovery`, UI: "Reboot to
recovery"): the backend saves the node's current `boot_to_os` override, forces it to
netboot, reboots the device, and restores the saved target as soon as the node reconnects
reporting `current_boot_device == http` — so the NEXT reboot boots the local OS again. The
restore fires only on an http report (a premature disk-booted system-info push cannot
revert early); if the netboot never happens, the forced override stays visible in the
Policy tab. It is refused with a 409, before anything is changed, when the node is not
online (`node_unavailable`) or when it is not netbooted now and its reported boot order
does not reach `http` before a local disk (`recovery_unreachable`, the same test the
watcher install uses), or when its bootloader fetches from a different boot server (also
`recovery_unreachable`; the route and the playbook block are refused alike); a reboot that cannot be dispatched puts the saved target back. While netbooted, agent ≥0.2.4 names the host `rpi-<serial-tail>-recovery` and
the UI shows the node as violet RECOVERY instead of ONLINE.

**The boot-loop failsafe is the third outcome, and the table above is wrong without
it.** A node whose installed OS cannot boot keeps re-netbooting and asking for the
same file; nothing on the device can report that, and with `boot_to_os = on` the
platform would withhold forever. So the handler COUNTS the withheld attempts and,
past `PROVISIONING_BOOT_FAILSAFE_THRESHOLD` consecutive ones, serves recovery
anyway (`RegisterNetbootWithhold` → `httpboot.Handler.ServeBootFile`). Details
that matter when reading a node's behaviour:

- It counts **`boot.sig` only** — on real hardware the ROM asks for just that file
  in the withhold case, gets a 404 and falls through to NVMe; it never requests
  `boot.img` when withheld.
- Once it fires, both files are served for a **3-minute window**
  (`failsafeServeWindow`) so the one recovery boot can complete, and the counter is
  one-shot-reset to 0 so the NEXT boot withholds again and gives the OS another
  chance. `nodes.boot_failsafe_at` records it; a confirmed OS boot clears it, and
  `POST /api/v1/nodes/:id/clear-boot-failsafe` is the operator acknowledge.
- A node with a **recent heartbeat is never counted** — it is not crash-looping,
  and that also stops a spoofed serial diverting a healthy node into recovery.
- **The threshold is an install decision, and the two postures differ on purpose: a
  LAN install whose boot host strangers cannot reach may arm it; an internet-facing
  install runs 0 (off), which is also the default in the code and the community
  compose.** `docs/HTTPBOOT-AUTH-RESEARCH.md` accepts the unauthenticated
  per-serial boot path only while the failsafe is off, since anyone who knows a serial
  can otherwise divert an already-offline node's next boot; `Config.Advisories()` warns
  at every start whenever it is armed. The price of running it off is that a node which
  cannot boot its OS is not rescued automatically — that is an operator action there. A
  per-node override (`nodes.boot_failsafe_threshold`) wins; 0 disables.

Endpoint: `GET /http-boot/:serial/:file` (file ∈ {boot.img, boot.sig}), unauthenticated
(the ROM can't auth). Looks the node up by hardware serial across accounts, resolves
`boot_to_os`, serves the file from `PROVISIONING_HTTP_BOOT_DIR` or returns 404.

**It is not the only public boot route, and one of the others is easy to lose.** Four
routes are registered unauthenticated, all behind the same per-IP rate limiter and
unknown-serial ban gate: `/http-boot/:serial/:file`, **`GET /http-boot/time`**,
`GET /net_install/agent/:file` (an exact filename allowlist — the agent binary plus its
`.sha256` and `.sig` — so the route cannot be walked as a general file server) and
`GET /net_install/eeprom/*`. `/http-boot/time` is the plaintext clock-sync endpoint: the
bootstrap has no RTC and its busybox `wget` can neither speak HTTPS nor follow the app
host's :80→:443 redirect, so without it the clock stays at the image build date and every
certificate the platform presents reads "not yet valid". Tighten the netboot routes in
the reverse proxy or a firewall without carrying `/http-boot/time` along and SD/netboot
adoption against an HTTPS install fails looking exactly like a PKI problem.

**Unknown serial or lookup/policy error → depends on
`PROVISIONING_HTTP_BOOT_FAIL_OPEN`, and the two postures differ.** The default, in the
code and in the community compose, is **`false`**: an unrecognised serial gets 404 and a
brand-new device cannot netboot its way in — SD provisioning is the entry path, and an
internet-facing install keeps it that way. Fail-open is an opt-in for an on-prem/LAN
install whose boot host strangers cannot reach. Check the install before reasoning about
what a new device will do.

**One unknown serial is deliberately NOT struck: a node this platform deleted.**
Deleting a node tells the device nothing, so it asks for its own `boot.sig` on
every boot forever, and counting that as enumeration let one stranded node slowly
ban **the IP of the site it sits in**. The handler checks `node_tombstones` first
(`SetDeletedNodeLookup`); it still withholds — the row is gone, there is nothing
to serve — but it logs what happened and whether the record is still restorable.
The exemption covers only serials this install actually deleted, so it is not a
hole in the guard.

---

## Adoption token (`UPTIME_TOKEN`) — two-phase, node-specific in the field

`UPTIME_TOKEN` is base64(JSON) telling the bootstrap where/how to adopt. Two phases:

1. **Card (phase 1):** the SD/USB provisioning bundle (Nodes → Add node → *Master
   image* → *Download SD Image*) carries a **master-token**
   adoption (`node_id=0` + `master_token` + `platform_url` + `grpc_endpoint`). Reusable
   across devices; used only for the *first* adopt. It has a TTL (default 7 days) — mint a
   fresh one per onboarding batch; expiry only affects onboarding, not fielded devices.
2. **Per-device EEPROM (phase 2):** when the node is approved / provisioned, the backend
   bakes a **node-specific** adoption (`node_id` + the node's `unique_token`, via
   `nodeService.GetAdoptionInfo`) into the EEPROM `UPTIME_TOKEN`, alongside the real serial
   in `HTTP_PATH`. So fielded devices carry their **own** token, not the account master
   token — extraction impersonates only that one node.

`GetAdoptionInfo` already returns base64(JSON); the signer bakes it verbatim (the agent
decodes once). Do NOT re-encode (a double-base64 encoding makes `UPTIME_TOKEN`
unparseable).

---

## Lifecycle (unattended)

1. **Onboard:** Nodes → Add node → *Master image* → *Download SD Image* → unzip to a
   **FAT32 SD** (USB works too but the ROM's USB stack is picky — prefer SD). Insert into
   the device → it boots the Buildroot bootstrap from the card → `S99agent` seeds a
   per-device `nodes/<serial>.txt` from the master blob and starts the agent against
   **that** file → **adopts** (pending). The ZIP still contains `node-adoption.txt` (the master blob) and the bootstrap still
   reads it, but nothing writes it any more — see "The provisioning card is reusable".
2. **Approve:** with effective `network_first = on`, approving auto-provisions the EEPROM:
   backend → signer generates the per-device pieeprom (real serial in `HTTP_PATH`, our
   pubkey, no SIGNED_BOOT, `0xf1467`, node-specific `UPTIME_TOKEN`) → agent flashes it via the
   recovery.bin path → reboot → device netboots per-serial. Remove the card.
   (Or trigger explicitly: `POST /api/v1/nodes/:id/provision-eeprom`.)
   Only the recovery system on a provisioning card is flashed at approval
   (`IsRecoveryBootstrap` and booted from SD). A device that runs its own
   operating system — the one-command adoption, from NVMe or from an SD card —
   keeps its bootloader until an explicit provision, which also sets its boot
   target; flashing it at approval would send its every reboot into recovery.
   The approval's flash pins the node to `restarting`, and a provision asked for
   while that pin holds is refused (409 `node_unavailable`) before anything is
   baked — see "One build per node at a time" below.
3. **Install:** `install_os` (pick an image) → `provision_connection` (optional) →
   `install_agent` (version **dropdown**, default `latest`). Install OS from the UI sends
   `wipe`, so it overwrites a disk that already holds partitions itself; no separate
   `erase_drive` comes first. Without `wipe`, `install_os` refuses a partitioned disk.
4. **Go managed:** set `boot_to_os = on` → next boot the endpoint 404s → device boots the
   installed OS from NVMe → the OS agent connects → **managed online**.
   An explicit `provision-eeprom` sets `boot_to_os = on` for you when the node's
   target is still unset (inherit) **and** the node has an OS to boot: it reported
   from an installed OS, or the most recent command that wrote its disk was a
   successful `install_os` (an `erase_drive` after it leaves nothing to boot). A node
   in the recovery system with no such install on record — one just adopted from a
   card, or one whose disk was erased since — keeps
   inheriting, so the flash's reboot brings it back to recovery rather than to a
   withheld netboot and an empty (or foreign) disk. Retention prunes old command
   rows, so a node whose install record is gone errs the same way: towards
   recovery, which an operator can always leave by setting the target.
5. **Reinstall / maintenance:** set `boot_to_os = off` → next boot serves the bootstrap →
   device netboots. No EEPROM change, no boot-order change, no kexec.

### What `provision_connection` writes into the target OS rootfs
- `/etc/orchestrator/env` — env secrets (effective env set, node→OU→root), 0600
  root:root, one `KEY="value"` per line (double-quoted, with `\`, `"`, `$` and the backtick escaped, the form systemd and a shell read alike); an empty set removes the file. Readable by
  root only and loaded by nothing automatically: they are **not** in any login
  session. A service loads them with `EnvironmentFile=/etc/orchestrator/env`; a root
  shell with `set -a; . /etc/orchestrator/env; set +a`. `run_script` and playbook
  scripts run from the agent's own environment and never saw them; a script that
  needs one sources the file the same way. Older agents wrote them into a marked
  block of `/etc/environment`, which pam_env exported into every session; each
  provision, and the agent's first start, removes that block and the copies of the
  keys it names, keeping the operator's own lines. A file without that block is
  never touched, and one whose markers are broken is left as it is, with a
  warning in the provision's result instead of a failure.
- user + password + SSH key + an sshd drop-in enabling password auth.
- `/etc/default/rpi-eeprom-update` (`RPI_EEPROM_USE_FLASHROM=1`,
  `CM4_ENABLE_RPI_EEPROM_UPDATE=1`) — lets the managed OS reflash the EEPROM without a
  manual `enable_eeprom`.
- masks `rpi-eeprom-update.service` (symlink to /dev/null) — the distro's unattended
  stock-firmware update would drop our embedded pubkey / per-serial `HTTP_PATH` and break
  signed netboot; only platform-signed images may touch the EEPROM.
- `/etc/netplan/99-orchestrator.yaml` (DHCP on `e*`, renderer networkd) — some images ship
  no netplan, leaving eth0 unmanaged with no IPv4 → the agent can never reach the platform.

  It is written from `install_agent`, the one command every install passes through —
  not only from `provision_connection`, which is OPTIONAL (the three UI buttons are
  independent and nothing chains them).

  Be honest about what it buys: on an Ubuntu preinstalled image cloud-init's
  `50-cloud-init.yaml` matches the same interface and sorts first, so networkd applies
  *that* one and this netdef is never used. Its value is that `/etc/netplan` is non-empty
  when the generator runs on the first boot, which on a stock image it otherwise is not.
- **A persistent `multi-user.target.wants/systemd-networkd.service` symlink** — the actual
  fix for the netplan/networkd boot race. On Ubuntu 26.04 networkd has no persistent
  enablement, and the race closes its socket too, so nothing can bring it back. Written by
  `ensureNetworkdEnabledOnRoot`, gated on networkd genuinely being the image's renderer.
  Full write-up: `docs/OS-IMAGES.md` → First-boot reliability.

---

## Components

- **`eeprom-signer/`** — isolated container, the ONLY holder of the private signing key.
  `POST /sign {soc, serial, node_id, http_host, adoption_b64, boot_order}` → bakes a
  per-device pieeprom (embeds our pubkey, **no SIGNED_BOOT**) into
  `net_install/eeprom/by-serial/<serial>/<node_id>/` (into `by-serial/<serial>/` itself
  only for a caller that names no node; the backend always names one). Mounts key + rpi-eeprom toolchain + firmware
  bases; internal network only. Picks cm4 (firmware-2711) / cm5 (firmware-2712) by SoC.
- **Backend** — `infrastructure/eepromsigner` client (`EEPROM_SIGNER_URL`); on approve
  (network_first on + node running the recovery system from a provisioning card) or via `POST /nodes/:id/provision-eeprom`,
  builds the node-specific adoption + calls the signer + sends `update_boot_order` so the
  agent flashes. `GET /cdn/agent-versions` lists installable agent versions for the UI
  dropdown. `GET /http-boot/:serial/:file` is the serve/withhold endpoint.
  Config: `provisioning.http_boot_dir` (`PROVISIONING_HTTP_BOOT_DIR`),
  `eeprom_signer.url` (`EEPROM_SIGNER_URL`).
- **Agent** — every EEPROM change requests the per-device package as
  `/net_install/eeprom/by-serial/<serial>/<file>` with the command's download capability
  (serial from `/proc/cpuinfo`), which the backend answers from that node's own
  `by-serial/<serial>/<node_id>/` directory. A command that carries the capability refuses
  to fall back to the shared `<soc>/<boot_order>` package when the per-device one is
  missing or baked with another order; only a command without one uses the shared
  package. It flashes the exact signed image: on the bootstrap
  via recovery.bin on the SD/USB boot partition, on a managed OS via
  `rpi-eeprom-update -d -f`. The agent never writes the EEPROM config on-device
  (no `rpi-eeprom-config --apply`). `provision_connection` writes
  env/creds/sshd/rpi-eeprom-update/netplan into the target rootfs.
- **`/net_install` must be routed to the BACKEND**, so a per-device EEPROM package is
  served only against its `?exp=&sig=` capability, and the ban gate and netboot rate
  limiter apply. The capability names the account and the NODE it was minted for
  (`cdnsign.SignEEPROMPackage`; the ids ride inside `sig`, so the URL shape a released
  agent builds is unchanged), and the handler serves the file from that node's own
  directory, never from the serial directory: a short serial is shared by unrelated
  devices, so a capability bound only to the serial would open another account's
  package and the node credential inside it. Deploy the signer before the backend. `/http-boot/*` goes to the backend for the same reason, and the RPi
  boots over HTTP:80, not HTTPS. What is served under the prefix is only
  `agent/<allowlisted file>` and `eeprom/<allowlisted file>`; the bare
  `/net_install/boot.img`, `/net_install/boot.sig` and directory listings 404, and no
  device ever used them (the ROM takes its image from `/http-boot/<serial>/…`).

  Routing `/net_install` to a static file server instead (an nginx sidecar serving the
  directory) hands every by-serial package to anyone who knows the path, because a
  static server cannot check the capability — a vulnerability rather than a quirk, and
  why the route belongs in the reverse proxy's configuration pointing at the backend.
- **Corresponding source — `GET /source/`.** The recovery image is a Buildroot
  output that aggregates GPL-2.0 and GPL-3.0 software (the kernel, BusyBox, the
  toolchain's libc and more), and every install hands it to devices over HTTP
  boot and inside the SD provisioning bundle. GPLv2 section 3 obliges whoever
  distributes such a binary to offer its source, and serving it from the same
  host — "equivalent access" — is the form that needs no written offer to track.
  So the backend serves `SOURCE_OFFER_DIR` at `/source/` (a plain-text listing
  with sizes and the digests from `SHA256SUMS`) and `/source/<name>` (one file,
  as an inert attachment, resumable by Range), tokenless, per-IP rate limited
  like the other public root routes, and with no authorization action, because
  the route reads no tenant data. What goes in the directory is the source
  bundle for the image's GPL **base** and the toolchain source snapshot, both
  named in the `SOURCE-OFFER.txt` the image build writes into its FAT root
  (the release procedure keeps the two in step): the base, not the agent release, is
  the unit of the offer, because a rebake only swaps the agent into the image.
  The UI links the route where the image leaves the platform — the SD bundle
  download and the reboot into recovery. With the variable unset the route
  answers 404 with a notice, which is right for an install whose devices are
  all its own; an install that serves the image to anyone else must set it.
  The route needs its own line in the reverse proxy like `/install.sh` does,
  or the SPA catch-all answers the URL in the image's notice with HTML.

---

## Serving the recovery image (operator procedure)

Network boot and the SD bundle need three things on the server, and an install
produces all three itself: the recovery image signed with **its own** key, the
SD payload extracted from that same image, and the EEPROM build directory the
signer bakes from. The paths below are the community compose's
(`docs/INSTALL.md`), relative to the directory holding `docker-compose.community.yml`;
the scripts live in `tools/netboot/` and take other paths as arguments.
`docs/INSTALL.md` makes `data/eeprom-keys` and `data/net_install` owned by
uid 1000, so the commands that write there run under `sudo`.

**1. The signing keypair** (once per install). The public half is baked into every
device's bootloader by the signer; the private half signs `boot.img` and every
per-device EEPROM package.

```sh
sudo openssl genrsa -out data/eeprom-keys/privkey.pem 2048
sudo openssl rsa -in data/eeprom-keys/privkey.pem -pubout -out data/eeprom-keys/pubkey.pem
sudo chown -R 1000:1000 data/eeprom-keys && sudo chmod 0400 data/eeprom-keys/privkey.pem
```

Losing the private key means no rebuilt server can ever sign a boot image the
devices already provisioned accept; escrow it (`docs/BACKUP-RESTORE.md`).

**2. The EEPROM build directory.** `eeprom-signer/sign.sh` reads
`/eeprom-build/rpi-eeprom/firmware-2711/latest` (CM4, Pi 4) and
`…/firmware-2712/latest` (CM5, Pi 5) from a clone of the Raspberry Pi
`rpi-eeprom` repository, and runs `/eeprom-build/tools/rpi-eeprom-config` and
`/eeprom-build/tools/rpi-eeprom-digest`. A bare clone at `data/eeprom-build`
is not that layout (the signer answers `no pieeprom base`).
`tools/netboot/update-eeprom-firmware.sh` produces it — the first run clones,
later runs fast-forward the clone and refresh the two tools:

```sh
tools/netboot/update-eeprom-firmware.sh data/eeprom-build
```

Run it periodically (a daily timer or cron entry is enough). A stale clone is
silent: the node's Bootloader section compares devices against the old base and
offers no update. The script never flashes a device.

**3. The signed image.** `boot.img` is built from the separate
`orchestrator-boot-image` repository (its README covers the build, the notices
inside the image and the corresponding-source bundle). The ROM verifies
`boot.sig`, a **three-line text file** produced by `rpi-eeprom-digest`:

```
<sha256 hex of boot.img>
ts: <unix time>
rsa2048: <hex RSA-2048 signature over the image bytes>
```

`openssl dgst -sign … -out boot.sig` computes the right signature and writes it
in a shape every device rejects. `tools/netboot/sign-boot-image.sh` signs a copy
beside the live image with `data/eeprom-keys/privkey.pem`, checks the three
lines, verifies the signature against `pubkey.pem`, keeps the previous pair as
`boot.img.prev`/`boot.sig.prev`, and renames the new pair into place:

```sh
sudo tools/netboot/sign-boot-image.sh /path/to/new/boot.img data/net_install
```

It needs `openssl`, `xxd` and `sha256sum` on the host. Never edit the live
`boot.img` in place: between the edit and the new signature every device that
netboots fails verification and falls through its boot order.

**4. The SD payload**, from the image just installed, every time `boot.img`
changes — otherwise an SD bundle and a network boot hand a device two
different recovery systems:

```sh
sudo tools/netboot/extract-boot-payload.sh data/net_install/boot.img data/net_install/sd-payload
```

It needs `mtools` (`mcopy`) and no mount; `sudo` only because the target
directory belongs to uid 1000. The backend reads the
result through `PROVISIONING_SD_PAYLOAD_DIR`.

**5. Corresponding source**, if the install hands the image to anyone else's
devices: place the archives the image's `SOURCE-OFFER.txt` names in
`data/source-offer/` and set `SOURCE_OFFER_DIR` (`docs/INSTALL.md`, "Network
boot").

The backend mounts `data/net_install` read-only; the only writer under it is the
signer, into `eeprom/`. The files above only need to be readable by uid 1000.
`sign-boot-image.sh` reads the 0400 private key and writes into
`data/net_install`, so it runs as root (`sudo`, as above) or as uid 1000.

---

## Hardware findings / lessons (why the design is what it is)

- **RPi5/CM5 network boot ALWAYS RSA-verifies `boot.img`.** No unsigned netboot. It verifies
  against the RPi built-in key and/or the embedded customer key. → we embed our pubkey.
- **`SIGNED_BOOT` is global + all-or-nothing.** Enabling it for secure netboot also blocks an
  unsigned local OS. → embed the key but leave SIGNED_BOOT unset.
- **`HTTP_PATH` is literal** (no `{pi_serial_number}`). → bake each device's real serial.
- **Boot-order gotcha — never rebuild the EEPROM config on-device.**
  `rpi-eeprom-config --apply` rebuilds the EEPROM from the *stock* pieeprom → **drops our
  embedded pubkey** (and may swap the bootloader firmware) → netboot signature breaks. In
  this model the boot order is baked once (`0xf1467`) and control is serve/withhold, so it
  never needs changing **on the device**. Changing the platform's default changes what a
  fresh package bakes, which is the supported path; already-provisioned nodes keep their
  old order until `provision-eeprom` runs again. The agent contains no `--apply` path at
  all: every EEPROM
  change (boot order, adoption token) downloads a signer-generated per-device package and
  flashes the exact image — via `rpi-eeprom-update -d -f` on a managed OS, via the
  recovery.bin staging path on the bootstrap. The UI matches: manual boot-order
  editing is gone, and the node sidebar's Bootloader section renders the device's
  current order read-only (`bootloaderSection.tsx` — the prop is commented "for
  display only"). Changing it goes through `provision-eeprom`.
- **A purely-netbooted device can't self-reflash its EEPROM** — the ROM honors `recovery.bin`
  only from SD/USB, not NVMe. So flash the target EEPROM at onboarding (on the SD bootstrap)
  or via usbboot; don't rely on reflashing a running netbooted device.

---

## Recovery runbook — unbrick / reflash via usbboot

Always-available lever (needs physical access + a host):
1. Put the device in bootloader/usbboot mode (nRPIBOOT / no bootable media), connect USB. It
   enumerates as `BCM2711 Boot` (CM4) / `BCM2712D0 Boot` (CM5).
2. `git clone --recurse-submodules https://github.com/raspberrypi/usbboot && cd usbboot && make`
   (needs libusb + pkg-config).
3. **Reset to factory default:** the bundled `recovery/` dir is CM4-ready; for CM4
   `cd recovery && ./update-pieeprom.sh && cd .. && ./rpiboot -d recovery` (add
   `recovery_reboot=1` to `recovery/config.txt`). For CM5 use `bootcode5.bin` =
   `firmware/2712/recovery.bin`.
4. **Flash our per-device EEPROM:** put `recovery.bin` + `pieeprom.upd` + `pieeprom.sig` from
   the node's own package directory, `net_install/eeprom/by-serial/<serial>/<node id>/` (the
   node's `id` as `GET /api/v1/nodes` reports it; if the directory is missing, bake one
   with `POST /api/v1/nodes/:id/provision-eeprom` or the node's **Update bootloader**),
   into a recovery dir → `./rpiboot -d <dir>` → prints
   `"EEPROM_UPDATE": "success"`. Files lying directly under `by-serial/<serial>/` are left
   from the older per-serial layout: a short serial is shared by unrelated devices, so they
   may carry another node's credential or that of the node's deleted predecessor — never
   flash them.

---

## Bootloader lifecycle & EEPROM hygiene

Everything below exists because of one failure mode: a reused provisioning SD card
carries another device's staged EEPROM package and the ROM flashes the board with the
wrong identity (`HTTP_PATH` of a different serial) before Linux even starts — the device
then netboots forever regardless of its own `boot_to_os`.

### The provisioning card is reusable — with two guards
The ZIP from Nodes → Add node → *Master image* is universal (bootstrap boot files + an
account-wide adoption token). It became device-specific in **two** independent ways, and the second
is the one that reads as dead hardware.

**Guard 1 — the staged EEPROM package.** The EEPROM flash stages
`recovery.bin + pieeprom.upd + pieeprom.sig` (per-device!) onto the card's boot partition
and nothing cleaned it up. On startup the agent now scans all FAT boot partitions and
deletes packages that are **foreign** (pieeprom baked for another serial — compared
against `/proc/cpuinfo`), **spent** (`RECOVERY.000`), or **staged on NVMe** (the ROM never
runs recovery.bin from NVMe). A package staged for THIS device on SD/USB is left alone. A
foreign *armed* package logs a loud warning: the local EEPROM may already carry the other
node's identity — re-run **Update bootloader** (`provision-eeprom`).

**Guard 2 — the adoption file itself.** After the master-token handshake the platform
hands the device its identity and the agent **rewrites the adoption file it was started
with** — which, on a master card, converted that card into node #1's card. Every later
device then presented node #1's credential with its own fingerprint, the platform
refused the handshake, and the board simply never appeared while retrying about once a
second: indistinguishable from dead hardware. Removing the rewrite
was not an option — it is what stops the shared enrolment token from becoming a node's
permanent credential, and an expired shared token would strand a node for good — so the
files were split instead. `S99agent` seeds `nodes/<serial>.txt` from the master blob and
starts the agent against that; `node-adoption.txt` is now written by nothing.

Two consequences follow, and the first one makes a master card a secret: it **accumulates
one non-expiring credential per device it has ever provisioned**, so it must not be left in
a drawer or handed on the way a card carrying only a TTL-bounded account token could be. A
stale entry is cleared by deleting that device's `nodes/<serial>.txt`. A per-node card (the
Re-adopt download) carries no master token and is unaffected by either guard.

### Where an EEPROM update is possible
- **Managed OS (Ubuntu on NVMe):** always — `rpi-eeprom-update -d -f <signed image>` via
  flashrom, immediate, no SD. The flash does **not** reboot the node; a reboot is required
  for the new bootloader to take effect.
- **The recovery system (netbooted or from an SD card), and an OS without
  `rpi-eeprom-update`:** the package is staged on the SD/USB boot partition and the node
  **reboots at once**, so the ROM's recovery.bin applies it. Which path runs is the
  agent's choice by whether `rpi-eeprom-update` is installed, not by the boot medium.
- **Purely netbooted, no SD/USB:** impossible — the command now FAILS with an explanation
  (it used to stage onto NVMe and report a success the ROM would never apply). The UI hides
  the update action in this state.

### Updating the bootloader / changing the boot order
`POST /nodes/:id/provision-eeprom` accepts an optional `{boot_order: [...]}`; the
default is network-first `http,nvme,usb_msd,sd,restart` = `0xf1467`. The signer always
bakes from its **latest** firmware base, so the same call is also "update the
bootloader". The agent verifies the baked `BOOT_ORDER` of the downloaded per-device
package against the requested order, so a stale package is never flashed silently: a
command carrying the download capability fails and names `provision-eeprom` as the cure
(see "Agent" under Components). Never flash devices in bulk on
unreliable power: an interrupted flashrom write bricks the EEPROM until usbboot recovery.

**One build per node at a time.** A node's package directory holds one build, and
the agent fetches it as separate requests — `SHA256SUMS` (once as the probe that
picks the per-device package, once more with the set), then `recovery.bin`,
`pieeprom.upd`, `pieeprom.sig` — and checks every file against the manifest it
fetched. Two builds are never byte-identical (the signature files carry the
signing time, and the config signature is embedded in the image), so a rebuild
between those requests is an integrity failure on the device. Hence:
`eeprom_provision` serializes bake and dispatch per node, and refuses a node that a
dispatched command has pinned to `restarting` **before** asking the signer — the
dispatch refuses that state anyway, and refusing it only after the bake is what let
a refused request overwrite a package in flight. The signer builds into a
`.stage.*` directory inside the package directory and renames the files into place,
manifest last, one build per directory at a time, so a reader never sees a torn
file and a failed build leaves the previous package whole; consistency across a
reader's separate requests is the platform's rule above, not the signer's.

**WARNING — never copy a boot order out of a document into an explicit
`boot_order`.** A stale order is silently accepted and flashed. The pre-USB
`http,nvme,sd,restart` (`0xf167`) in particular strips USB boot from the device,
which is the exact fall-through-to-the-provisioning-SD-card failure the USB digit
was added to prevent. Omit the field and take the platform default, or read the
device's current order first.

**`update_boot_order` is refused when dispatched bare.** The slug is still reachable
from the generic command API and from a playbook block, and both are refused by
`requireEEPROMCapability` (`services/node/service.go`) unless the command carries the
`pkg_exp`/`pkg_sig` download capability — which is minted only after
`eeprom_provision` has baked and SIGNED an image for this device's serial, so its
presence is the proof that the bake happened. Without it the agent cannot fetch the
per-serial image and falls back to a SHARED one, overwriting the device's embedded
`HTTP_PATH` and `UPTIME_TOKEN`: a green run that costs the node its ability to boot
from the network at all. The refusal names the route that does it properly —
`POST /api/v1/nodes/:id/provision-eeprom` with the desired `boot_order`, which
prepares and signs that image first.

### Bootloader identity & freshness
- `bootloader_version`/`bootloader_timestamp` have exactly ONE writer: the **agent**,
  reporting `vcgencmd bootloader_version` over its authenticated gRPC stream
  (`BootInfo.bootloader_version/_timestamp`), including from the recovery bootstrap.
  The ROM's HTTP-boot User-Agent is parsed and **logged only, never persisted**
  (`logBootloaderUA`; reasoning in `docs/HTTPBOOT-AUTH-RESEARCH.md` Phase 0).
- Signer `GET /base` reports the firmware base per SoC; the backend proxies it as
  `GET /api/v1/eeprom/base-firmware` (5-min cache) and the UI shows **Up to date /
  Update available** on the node's Bootloader section (Advanced tab).
- The base is only as current as the signer's rpi-eeprom clone, so keep it current:
  run `tools/netboot/update-eeprom-firmware.sh` periodically ("Serving the recovery
  image" above). It fast-forwards only and never flashes devices — flashing stays an
  explicit operator action.
- CM4 (2711) firmware releases lag CM5 (2712) by weeks. Ask the signer
  (`GET /base`, proxied as `GET /api/v1/eeprom/base-firmware`) for the current base
  per SoC rather than reading a date out of a document.
