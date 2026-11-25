# OS images: upload, compression, deployment

How OS images travel from the operator's browser to a node's NVMe, and why the
pipeline looks the way it does. All numbers below were measured on one bench
setup (CM5 on a gigabit network, NVMe behind PCIe Gen 3 in the recovery
environment) with a 4.87 GB Ubuntu 26.04 preinstalled server image.

## Formats

| Upload extension | Accepted | Deployable | Notes |
|---|---|---|---|
| `.img`, `.iso` | yes | yes (written as-is) | raw images |
| `.xz` | yes | yes (streaming decompress) | official vendor images are usually **single-block** — see repack below |
| `.zst` | yes | yes (streaming decompress) | legacy deploy format, still fully supported |
| anything else | rejected at upload | — | the catalog only holds what `install_os` can deploy |

## The deploy format: multi-block xz

Server-side compression (`POST /api/v1/images/:id/compress`) produces
**multi-block xz** (`xz -T0 -6`, under `--memlimit-compress=` from
`IMAGE_XZ_MEMLIMIT_MB` — `-T0` sizes its threads from the
host's physical RAM, not the container's cgroup, so xz now trades threads for
staying inside the bound; decode carries `--memlimit-mt-decompress` and a hard
`--memlimit-decompress` for the same reason). Rationale, from an A/B benchmark of
identical content on real hardware:

| | multi-block xz | zstd -15 |
|---|---|---|
| size (4.87 GB raw) | **1.38 GB** | 1.50 GB |
| `install_os` wall-clock | 26 s | 26 s |
| server-side compression | 5 min | 99 s |

Install speed is identical because the install pipeline is network/disk-bound,
not decoder-bound; multi-block xz decompresses in parallel on the device
(`xz -dc -T0`, one block per core). Single-block archives — which is what
vendors ship — decode on one core (~60–80 MB/s on a CM5) and install ~2.5×
slower, hence the "Repack to multi-block XZ" action for uploaded `.xz` files.
zstd remains as a compressor fallback and for `.zst` transcoding.

Compression runs as a background job with progress (`compression_status`,
`compression_progress` on the image, pushed over the account websocket at most
twice a second). For a raw upload the percentage is how much of it has been
encoded; for a transcode it is an estimate from the expanded bytes, because a
small archive is read in seconds and then expands for minutes, and it stops at
90 until the job finishes rather than claiming to be almost done. A job is
bounded: its expansion by `IMAGE_MAX_DECOMPRESSED_SIZE`, what it stores by the
byte quotas, its run time by `IMAGE_COMPRESSION_TIMEOUT_MINUTES`. Slots are
shared fairly between accounts, and deleting the image stops its job, queued or
running (`docs/CONFIGURATION.md`).
Statuses stranded by a backend restart are reset on startup — a compression to
`failed`, a capture to `capture_failed`.

**Whether the original upload survives compression is per-install.** With
`IMAGE_COMPRESS_REPLACE_ORIGINAL` off (the code default) the image record
points at the compressed file and keeps the original in storage
(`file_storage_info.source_file_path`), so the upload can be re-compressed
later; same-extension repacks preserve the original under `<id>.orig.<ext>`.
An install that pays for object storage may set
**`IMAGE_COMPRESS_REPLACE_ORIGINAL=true`** rather than keep two copies of every
multi-gigabyte image. There, compressing an image **deletes the uploaded
original permanently**, and getting it back means re-uploading it.

When the original has been dropped the record carries no `source_file_path`,
so "already in the deploy format" is recognized instead by the completed
progress marker (`compression_status` empty and `compression_progress` 100,
which a fresh upload resets). Either way a file already in the deploy format is
final and cannot be compressed again, and deleting an image (or re-uploading
its file) removes every stored file the record references.

## Image kinds: os / golden / backup

Every image carries a `kind` (`images.kind`, default `os`):

- **os** — uploaded OS image; never carried a machine identity.
- **golden** — capture with `generalize=true`: machine identity (machine-id,
  SSH host keys, adoption file, cloud-init/DHCP state, hostname) is stripped
  at capture, so it deploys onto any device, which then adopts as a fresh node.
- **backup** — byte-exact capture of one node, identity included. The source
  node is recorded on the image (`source_node_id`/`source_node_name`,
  stamped at capture; the UI shows a kind chip + "Captured from …").

**Restore semantics** (`install_os`): a backup restored onto its **source**
node boots as that same node — nothing is stripped. A backup deployed onto a
**different** node would clone the source's identity, so the platform
automatically sets `strip_identity` on the install command (proto field 11)
and the agent (≥ 0.3.3) generalizes the freshly written system before first
boot. The gate is fail-closed: an older agent refuses the command with a
clear error instead of silently cloning (`MinAgentVersionStripIdentity`).
Legacy captures (pre-`kind`) are backfilled from the old version-field marker
with an unknown source, which counts as "different node" — strip, fail safe.

**Which disk, and when a used one may be overwritten.** `install_os` with
`wipe` (*Overwrite a used disk*) is refused with 409 `recovery_required`
unless the node is running the recovery system: from a running OS the device
refuses only a disk that is mounted or busy, so an unmounted second disk would
be erased. Without `wipe`, installing onto a blank second disk from the running
OS stays allowed — the device refuses any disk that already has partitions. A
playbook block that overwrites must name its image, or the playbook cannot be
activated. `capture_image` with no `target_device` captures the node's only
reported disk and is refused, naming the disks, when it reports several; the
recovery root is RAM, so there is no "disk the node runs from" to default to.

**Disk sizes**: capture shrinks the root filesystem AND the partition-table
entry, so the streamed image is only as big as the data. On install the agent
first relocates the backup GPT to the real end of the target disk
(`sfdisk --relocate gpt-bak-std`, needed whenever the target size differs
from the source), then grows the root partition to fill the disk — so a
capture restores onto smaller and larger disks alike, as long as the data fits.

## Where images are stored

The storage backend is `local`, `gcp` or `s3`, chosen by `STORAGE_TYPE`
(`auto`, the default, picks S3 when both `S3_BUCKET` and `S3_ENDPOINT` are set,
else GCP when its bucket and project are, else local disk). What changes with
the backend, beyond where the bytes land:

- **The device fetches the image from a different place.** On local storage
  `install_os` hands the node a URL on the platform's own CDN route, whose
  access control is the CDN handler's HMAC. On S3 it hands the node a **real
  presigned URL**, valid one hour, straight at the bucket endpoint — the bucket
  itself stays private, the URL carries its own authorization, and the download
  never touches the platform host. Either way the URL must be https unless the
  operator has explicitly accepted plaintext: an OS image is root-equivalent on
  the target device and the only thing vouching for it is a checksum travelling
  in the same command.
- **Compression streams bucket → xz → bucket**, so the host's disk stays flat
  through a multi-gigabyte repack.

## Taking an image back out

`GET /api/v1/images/:id/download` mints a short-lived signed `/cdn` URL for the
stored file and returns it as JSON, together with the recorded file name, byte
count and SHA-256 — the last so the recipient can verify their copy against the
same digest the upload computed. It is what makes an account's images portable.

The download lands under the name the file was uploaded with, carried in an
unsigned `filename` query parameter that `/cdn` turns into a
`Content-Disposition`. Unsigned on purpose: the HMAC binds the storage path, and
widening the signed material would invalidate every URL the agent, `install.sh`
and the EEPROM package fetch already use — for a parameter that only labels a
response whose bytes the holder is entitled to either way. What makes it safe is
`cdnsign.SanitizeFileName`, an allowlist applied where the value enters the
header. Without the parameter the browser would save the file under its storage
key, which is a bare UUID.

## Integrity chain

1. **Upload**: the file is streamed to storage through a SHA-256 hash; the
   declared multipart size must match the stored byte count. Local storage
   writes are atomic (`.tmp` + rename), so failed uploads never truncate an
   existing file; on S3 the equivalent guarantee comes from the object only
   becoming visible when the multipart upload is completed. Re-uploads replace
   the old file only after the new one is stored.
2. **Compression**: the checksum is recomputed over the compressed output.
3. **install_os**: the agent hashes the raw download stream and verifies it
   against the platform checksum; compressed formats additionally carry
   their own internal block checksums (CRC64 / xxhash). After writing, the
   agent runs `e2fsck` on the root filesystem while expanding it.

## Rollback copies in `net_install/` and `agent-artifacts/`

Nothing prunes `net_install/`, and no quota covers it — the `IMAGE_MAX_*`
guards bound tenant image storage, which is a different path (and with object
storage a different backend entirely, since images live in the bucket). So the
rollback copies an operator keeps there are the operator's to bound.

- **The recovery image.** `tools/netboot/sign-boot-image.sh` keeps exactly one
  previous pair, `boot.img.prev` / `boot.sig.prev`; rolling back is moving the
  two back into place and re-extracting the SD payload from the restored image
  (`docs/NETWORK-FIRST.md` → "Serving the recovery image"). Any further copies
  you keep (about 100 MB each, plus a whole `sd-payload/`) are not removed by
  anything.
- **The agent package.** Keep the previous version's `.deb` and `.deb.sig` in
  `agent-artifacts/` beside the current one: a rollback is then `update_agent`
  with that explicit version. A version you deleted can be re-imported from the
  agent's releases (`docs/INSTALL.md` → Upgrading); it cannot be re-signed by
  anyone but the release key holders.

Take a rollback filename from the files that are actually on the host, never
from a document. After any change, verify the netboot chain rather than
assuming it survived: `head -1 boot.sig` equals `sha256sum boot.img`,
`boot.sig` is 3 lines / about 600 bytes, `sd-payload/` was extracted from the
same image, and a rollback agent version still fetches **and** verifies
(`/cdn/agent/<prev>/arm64/deb` plus `.../deb.sig` — `.../sig` answers 400 —
checked with `openssl dgst -sha384`).

## Agent install pipeline

- Decompression is chosen by the download URL's path **extension** — which is
  why a presigned bucket URL works unchanged: signing only adds query
  parameters, and the object keeps the key it was stored under. `.xz` prefers the
  system XZ Utils binary (`xz -dc -T0`; the netboot bootstrap ships one) and
  falls back to a pure-Go decoder; `.zst` uses the built-in multi-core
  klauspost decoder — no system binaries required.
- Device writes are buffered (page cache) with a sync every 256 MB and a
  final sync before the command is reported complete. `O_SYNC` per chunk —
  the previous behavior — capped the whole pipeline at ~28 MB/s; removing it
  brought installs from >3.5 min down to ~26–52 s.
- An interrupted download (platform restart, network blip) is tried up to
  3 times in all, each retry from offset 0; a checksum mismatch is not
  retried.
- After `install_agent`, the boot partition gets: CM4 EEPROM settings in
  `[cm4]`, and a cloud-init `network-config` pinning `dhcp-identifier: mac`
  (see below).

## First-boot reliability

Two failure modes were chased on bench hardware and fixed:

1. **Ubuntu 26.04 netplan/networkd boot race.** `netplan-configure.service`
   can stop `systemd-networkd` before its initial activation finishes; the
   follow-up start is swallowed and the node stays without a network manager
   forever (journal signature: early `eth0: Configuring with
   /run/systemd/network/zzzz-dracut-default.network`, then `Stopping
   systemd-networkd` with no later `Started`, while netplan's generated
   config sits unused). Hits roughly half of boots, on **any** board — CM5 and
   Pi 4 both reproduce it; the same Pi 4 had one clean and one failed install
   hours apart.

   Two things address it, and **the first is the actual cure**:

   - **A persistent enablement symlink** (`ensureNetworkdEnabledOnRoot`,
     `agent/pkg/application/handlers/network_provision.go`) written into the
     target rootfs during `install_agent`. On Ubuntu 26.04 `systemd-networkd`
     has **no persistent enablement at all** — `is-enabled` reports
     `enabled-runtime` from a symlink the netplan generator manufactures into
     `/run` each boot — and the race closes `systemd-networkd.socket` along
     with the service, so nothing is left to activate it. Because
     `netplan-configure.service` is ordered from `sysinit.target` and
     `multi-user.target` comes later, a static `multi-user.target.wants`
     symlink starts networkd *after* the stop that loses it. Refuses to act on
     an image whose network belongs to NetworkManager or ifupdown — two DHCP
     clients on one interface would be worse than the race, and would
     reproduce every boot.
   - **A state-based runtime heal** (`agent/cmd/agent/net_heal.go`), which
     ignores the connection error entirely and decides from host state: no
     default route (a *route*, not an address — `docker0`'s bridge address
     defeats the naive address test) plus `systemd-networkd` not running. Then
     `systemctl reset-failed` + **`start`** (not `restart`: the fault is "not
     running", and restarting a merely-slow host kills an in-flight DHCP).
     Suppressed for the first 150 s of uptime, when NetworkManager is active,
     and when the host can already route.

     **Two constraints make it safe, and both are easy to undo by accident.**
     It is called from `main.go` **only after `agentService.Run` returns**,
     which happens only when the FIRST connect failed — so reaching it proves
     no command can be in flight. Moving it into the reconnect path would let a
     networkd restart land in the middle of an `install_os` streaming several
     GB to NVMe; if it ever moves, it needs an interlock against in-flight
     commands first. And a marker file under `/run`
     (`/run/orchestrator-agent-net-heal`) throttles it to one attempt per
     `healInterval` (3 min). `/run` is tmpfs on purpose: the marker survives the
     agent's own 10 s systemd restarts within a boot, but not a reboot, where
     healing should be free to run again immediately.

   **Remedy proven on a device actually in the wedged state** (a Pi 4, via
   console): `reset-failed` + `start` brought the node back. Until then "one
   restart fixes it" was only a claim in a code comment.

   **Measured — the race no longer reproduces on the configuration that was
   tested.** A hardware-in-the-loop runner drove a CM5 on NVMe through **20**
   full `install_os` → `install_agent` → reboot → reconnect cycles with agent
   0.5.5 and passed **20/20** (install 30 s every iteration, reconnect mean
   52 s). At the ~50%-per-boot rate above, twenty consecutive passes is a
   ~1-in-a-million coincidence, so this is evidence rather than a lucky boot —
   which is exactly why a single clean install was never accepted as one.

   **Scope, because the paragraph above says the race hits "any board".** That
   run is one board and one image; the cure is not board-specific, and the
   boards the platform supports are listed in `docs/SUPPORTED-HARDWARE.md`.
   The install-path cure it exercises — a default netplan plus a persistent
   `systemd-networkd` symlink written into the target rootfs — shipped in
   agent **0.4.8**, so a node below that is not covered by this result at all.
2. **DHCP identity.** The installed OS requests leases with
   `dhcp-identifier: mac` (written to the boot partition `network-config` at
   install), matching the bootloader and the netboot bootstrap, so a device
   presents one DHCP identity across all of its boot environments.

## Troubleshooting: node looks dead after install

The OS is usually up — the agent just can't reach the platform. In order:

1. Don't trust a ping to the node's recovery-time IP: the installed OS may
   hold a different lease. Sweep the subnet and check the neighbor table /
   the switch MAC table for the node's MAC.
2. If the node's agent crashloops on a connection error — `network is
   unreachable` **or** `name resolver error: produced zero addresses` (the
   wording when the platform URL is a hostname) — that's the netplan race.
   Confirm from the console with
   `ip a` (link UP, only a `fe80::` address, no `inet`) and `systemctl
   is-active systemd-networkd` (`inactive`). Recover in place with `sudo
   systemctl reset-failed systemd-networkd && sudo systemctl start
   systemd-networkd`.

   Note the node is up-but-silent, so it accepts no remote reboot and no
   `update_agent` — reaching it needs console, power, or the watchdog below.
   To the platform it is simply "offline", which is indistinguishable from
   powered off.

   **An armed agent-health watchdog DOES now see it, and rebooting is the
   right answer.** The liveness file used to be touched unconditionally from a
   ticker started before the first connect ever ran, so a 10 s crash loop
   refreshed it 12–30× faster than the preset's 120–300 s threshold and the one
   supervisor meant to catch this saw a perfectly healthy agent for two hours.
   `livenessShouldReport`
   (`agent/pkg/application/service/agent_service.go`) now **withholds** the
   report when BOTH hold: this process has never reached the platform, and
   `/proc/net/route` shows no default route. The watchdog then reboots the
   node, and a reboot genuinely fixes it — the netplan/networkd race is a dice
   roll that a fresh boot re-rolls. Both halves of the condition are required:
   withholding merely because the agent is disconnected would reboot every
   armed node in the fleet, repeatedly, during a platform outage the nodes had
   no part in. Once connected the file is always touched, so an established
   node's supervision is unchanged — and on a node with **no watchdog armed**
   (which is most of the fleet: arming is a deliberate per-node operator
   action) none of this does anything at all.
3. For deeper inspection, netboot the node into recovery (set the node's
   `boot_to_os` policy to netboot, power-cycle) and read the installed OS's
   persistent journal from the mounted NVMe via the node web terminal
   (`strings /var/log/journal/*/system.journal` — the bootstrap has no
   journalctl).
4. On a bench network without NTP, fresh-OS clocks can be months off, so journal
   timestamps mislead — correlate by event order, not wall time.

## Troubleshooting: a disk that "failed" — check the power first

**Read the node's Power row before believing anything a disk tells you.** Since
agent 0.5.4 the node Overview carries one, decoded from `vcgencmd get_throttled`,
and the fleet list shows an `UNDER-VOLTAGE` pill. It is deliberately absent
rather than green on non-Pi hardware and on agents below 0.5.4 — an empty row
means "no facts", never "the supply is fine".

The symptom this exists for: `erase_drive` returned
`wipefs: error: /dev/sda: probing initialization failed: No such device or
address`, which reads as a platform bug, and behind it `dd if=/dev/sda bs=512
count=1` and `blkid` both failed with the same ENXIO after
`uas_eh_device_reset_handler` cycles. The disk looked dead. **The node was
powered from a computer's USB port and had browned out** — sticky bits `0x50000`,
i.e. under-voltage HAS occurred — and the disk came back the moment it had a
proper supply. Nothing was wrong with it.

Two things follow for anyone debugging this class of failure:

- **The sticky half of the mask is the useful half.** The live bits read `0x0`
  during that failure even though the board had already sagged; bits 16–19
  record that it happened since boot and clear only on reboot. So the answer is
  usually available *after* the failure, which is when somebody is looking.
- The agent now probes the device before running `wipefs`, so a wedged disk
  reports *"device /dev/sda is not responding (USB transport)"* instead of a tool
  internal. That moves the blame off the platform, but only the power row moves
  it off the disk.

`vcgencmd get_throttled` needs root (`/dev/vcio`) — the agent has it, an
ordinary operator shell does not, so prefix a manual check with `sudo`.
