# Utilities — demand research and the policies question

Companion to `docs/UTILITIES-PLAN.md`. This file answers two questions the
code cannot: whether utilities should have become "policies" (§1), and which
software roles are worth building versus deliberately refusing (§2). The
catalog itself is `infrastructure/utility_registry/registry/utilities/*.yaml` —
read that, never a list here.

## 1. Utilities vs policies — keep the families distinct, keep the names

The question is whether "utilities" should widen into (or rename to)
"policies". **Recommendation: no rename, no merge**, on three grounds the code
makes concrete:

- **Different resolution algebras.** Policies field-merge with tri-state
  `inherit|on|off` per knob (`ResolvedFlag`, source provenance, node → OU →
  root → account default). Utilities take the nearest **whole config block**
  (`utility.ResolveForNode`), because half a software declaration is not a
  meaningful state. A merged family must pick one algebra and silently break
  the other.
- **Different apply machinery.** Policies ride existing mechanisms (httpboot
  signing, provisioning fill-in, the access-config fan-out) with no ladder and
  no agent floors. Utilities ride one slug with per-utility hashes, a ladder,
  floors and `depends_on`. Merging forces ladder bookkeeping onto knobs that
  need none.
- **The word is taken.** `/api/v1/account-policy`, `/nodes/:id/policy`,
  `/nodes/:id/effective-policy` already mean something; renaming utilities to
  policies makes two products answer to one word.

**The routing test** (held under load against every candidate below): *does it
install and reconcile SOFTWARE with its own lifecycle (utility), or flip a
knob on substrate the platform already owns (policy)?*

| Example | Lands as | Why |
|---|---|---|
| Kiosk showing a per-OU/per-node URL | Utility (param) | Software role; per-node URL already works via a node-scoped assignment (nearest block wins) |
| Read-only root | Policy (decided, plan §5.3) | A knob on the root mount; tri-state + per-node maintenance override is exactly `PolicyFlag`'s shape |
| Overclock per OU | Deferred; policy-shaped if ever shipped | No software; board-dependent; needs a reboot and the fan-out precedent, not a catalog entry |
| Scheduled reboots | Neither — already exists | `trigger.schedule` playbook + reboot block; point at it, do not rebuild |

## 2. What people actually want from Pi fleets — and what fits

From the demand research (signage/kiosk ecosystems, r/raspberry_pi,
r/selfhosted, fleet-management posts), weighed by user value × audience × fit
with the reconciled-utility model. What that produced and shipped is the
registry; what it produced and did NOT ship is below, and the second list is
the one that keeps its value.

**Good candidates, deliberately later:**

- **AdGuard Home / DNS filtering** — container-class; needs a deliberate
  design for the port-53 host mutation (`DNSStubListener=no`), so it follows
  once kiosk proves the container class.
- **chrony/time_sync** — earns a place only for isolated-network fleets;
  timesyncd covers the common case. Bare timezone is policy-shaped.
- **k3s node membership** — unchanged from plan §5.4; nothing in this research
  moves it.

**Honest non-fits** (popular, and wrong for utilities — write this down before
the first ticket asks):

- **Home Assistant** — wants HAOS owning the OS; a plain container loses
  add-ons and interactive onboarding. Answer: a Docker playbook.
- **NAS / media stacks (OMV, Jellyfin, Plex)** — opinionated stacks, storage
  topology, interactive setup.
- **WireGuard server** — per-peer keypair lifecycle is its own product;
  playbook territory.
- **PiKVM, RetroPie** — own OS images / hobbyist appliances.
- **Watchtower / compose workload delivery** — the v1 non-goal; demand for it
  is the signal for a Docker param family, not a separate utility.
- **Maintenance/reboot windows** — the one genuine future POLICY the research
  produced; parked (doctrine forbids unattended reboots anyway).

## 3. Kiosk container — what survived recipe validation

The recipe itself (a Debian base — trixie since the Pi 5 display findings
below, see `docs/UTILITIES-PLAN.md` §5.2 — seatd + cage under `setpriv`, the DRM
device gid chgrp, `WLR_LIBINPUT_NO_DEVICES=1`, grim + DevTools as the
without-a-monitor probes) shipped and lives in
`github.com/uptime-lab/orchestrator-images` (`kiosk/`). Two findings that are
NOT in that image and would otherwise have to be rediscovered:

- **Traps for a fleet rollout**: devices have no registry-credential
  mechanism, so the image the default param names must be PUBLIC — built by CI
  and published to a public package — before the default works anywhere but a
  bench where the image was hand-loaded. The applier pulls only when the image
  is absent locally, so hand-loaded images (benches, air-gapped sites) keep
  working.
- Physical HDMI output was verified on a Pi 5 with a real display — and the
  headless-only validation had hidden three defects (an empty `WLR_BACKENDS`
  read by wlroots as an empty backend list, seatd's VT binding, bookworm's
  Mesa lacking Pi 5 support); `docs/UTILITIES-PLAN.md` §5.2 carries the rules.

## 4. Chassis lessons worth keeping

- **Verify against the rendered value, not a literal** — unattended-upgrades'
  check compares `apt-config` output to the rendered interval; a check
  hardwired to "1" permanently reddens a valid interval=3 config.
- **Validation stays agent-side** (the docker `registry_mirrors` precedent);
  where a regex tempts, an enum answers (journald sizes).
