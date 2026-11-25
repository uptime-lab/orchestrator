# Utilities — Design Plan

Section numbers here are cited from Go and proto comments — do not renumber.
What is live is the code; this file keeps the reasoning that is expensive to
re-derive and the parts still owed.

## 1. What a Utility is

A **Utility** is a software role a node holds — installed and configured on every
*eligible* node an assignment reaches, and continuously reconciled. The examples
driving the design: Docker, kiosk mode. Read-only root and Kubernetes were part
of the original idea and were deliberately re-homed (§5.3, §5.4).

**The Utility/Playbook line, in one sentence: a playbook is a sequence you run;
a utility is a condition a node holds.** If a candidate "utility" has no
meaningful steady state to reconcile, it is a playbook.

| | Playbook | Utility |
|---|---|---|
| Semantics | Imperative run, point-in-time, pinned `template_snapshot` | Desired state, continuously reconciled |
| Coverage | Nodes selected at start; later joiners untouched | Every **eligible** node the assignment reaches, incl. future joiners and reimaged nodes |
| Failure | Terminal per run | Per-node ladder; drift re-apply with an attempt cap (designed, not built — §3.5) |
| Consent | The run **is** the consent | Assignment confirmation is standing consent; reboot-requiring work on *later* joiners parks pending (§3.6) |

**One utility class exists in the DOMAIN** (`utility.Class`, validated by the
registry loader): **`reconciled`** — full automation: fan-out on config change,
drift self-heal, first-connect apply, reimage re-apply.

A second class, **`consent_gated`**, was designed here (assignment creates a
surfaced pending state per node; only non-disruptive transitions reconcile
automatically; the assignment confirmation dialog is the standing consent for
the *initial* fan-out; a later OU joiner parks in `pending consent` for
anything reboot-requiring rather than rebooting itself) because the platform
doctrine "nothing reboots a device without a human present" would otherwise
contradict the coverage promise. **The constant was DELETED**:
no catalog entry ever used it (all seven ship `class: reconciled`, kiosk
included — it installs as a container and needs no reboot, the exact case the
class was designed around) and none of the gating flow was ever built, so the
declaration only made "untested code" claims possible about code that did not
exist. The consent-gated paragraphs below are kept as the DESIGN to build —
class, parking and consent dialog together — the day a genuinely
reboot-requiring utility enters the catalog.

**"Every eligible node", never "every node."** Some nodes run agents below the
utility's floor that the OU's operator cannot roll forward on their own;
watchers
refuse every command; netbooted nodes hold every managed-OS-only apply. Partial
coverage is the *normal* state of the median OU, so eligibility (agent floor,
`agent_kind`, boot state) is part of the feature's definition: the OU-level
rollup is `eligible / applied / confirmed / pending / skipped(reason)`, never a
bare percentage, and the ou-counts facet counts eligible nodes.

## 2. Decisions taken

Decisions 1 and 5–7 (the name and the top-nav placement; read-only root as an
OU policy rather than a utility; Kubernetes deferred; kiosk as a container) are
either shipped or restated in full where they live — §5.2, §5.3, §5.4.

2. **Scope model is the access-entry one, INCLUDING the OU set** (phase 1
   shipped single-OU): account-wide
   ("all nodes") XOR an **OU set** (`utility_assignment_ous` join; legacy
   `ou_id` backfilled once, never read — the `access_entry_ous` pattern) XOR
   one node XOR **unassigned**. One config legitimately spans several units;
   per-unit differences are separate assignments. Tie-freedom moved from the
   DB index into the SERVICE: no two assignments of one utility may share an
   OU (`checkOUOverlap`, fail-CLOSED — a tie the check could not see is a
   nondeterministic fleet config). The OU delete guard counts the JOIN table,
   node/account-wide levels keep their partial-unique indexes, and a set
   spanning a node's own OU and its parent resolves at the own-OU level
   without shadowing itself (the access-entry EntryID lesson).
3. **Unassign = retain.** Removing an assignment stops managing the software and
   touches nothing on devices; the scan honestly reports e.g. "dockerd running,
   unmanaged". Actual uninstall is a separate, explicitly confirmed action
   (v1.x), treated as disruptive. This closes the accidental-drag blast-radius
   blocker outright.
4. **Params reference variables and node attributes, not raw secrets.** A kiosk
   URL is a *variable* (`sensitive=false` secret) or a per-node attribute:
   `{{secrets.<name>}}` / `{{node.attributes.<key>}}`, resolved at dispatch.
   Per-node kiosk URLs fall out of the existing `attr:<nodeID>:<key>` machinery
   with no new storage. **Implemented** (`resolveParamRefs` in the
   utility service, riding the SAME `ResolveEffectiveProvisioning` interface
   the workflow engine uses): references resolve per node BEFORE hashing, so a
   changed attribute honestly moves the desired hash; an unresolvable
   reference FAILS the node's apply (never substitutes empty); and a
   **sensitive** secret reference is refused outright in v1 — the privileged
   authoring gate (`SecretFedUnprivilegedBlocks` extended) is what would make
   it safe and does not exist for utilities yet. The config jsonb itself stays
   plaintext and off the rewrap list — secret material never enters it.

## 3. Architecture

Modeled on two shipped precedents: **device profiles** for catalog + ladder
(typed embedded registry, fatal boot validation, one-block inheritance, own
slug, `declared → applied → confirmed` closed by a fixed scan) and **access
entries** for convergence (wholesale idempotent reconcile, applied-generation
self-heal, OU-edit fan-out with the reaches-this-level predicate).

### 3.1 Catalog

Shipped. The truth is `infrastructure/utility_registry` — the loader (schema,
fatal boot validation) plus `registry/utilities/*.yaml` (the catalog itself).
One doctrine worth not re-deriving: **typed setup steps, never free-form
shell**, and param value domains bounded in the registry too (the
`system.apply_watchdog` reasoning — a data file that dispatches root scripts
bypasses every domain bound).

### 3.2 Assignment and state

- **`utility_assignments`**: `id, account_id, utility_id, target (account-wide
  XOR ou_id XOR node_id XOR unassigned), config jsonb, enabled`, partial-unique
  index per (utility, target). Per-account row cap (409 on refusal, count fails
  open — the caps doctrine).
- **`node_utility_states`** (a table, not columns on `nodes` — N utilities × per-
  utility state cannot live in columns): per (node, utility) desired hash,
  applied generation (written at settle from the platform's **own dispatched
  hash**, never the agent's claim as given — the access rule; §3.5), consent
  state, confirmed facts from scan, skip reason.
- Both tables are created by backend AutoMigrate and get a section in the
  account export.
  Node delete/restore: utility state dies with the node row; a tombstone restore
  re-converges via the reconcile loop rather than restoring state.
- OU references follow the established rules: delete guard counts this table
  (`DeleteIfUnreferenced`), OU delete vs assignment serialization via
  `OUMutationRepository`, and a dangling `ou_id` resolves as **unassigned with a
  warning, never an error** (the boot.sig 404 lesson).

### 3.3 Resolution

Shipped verbatim — `domain/utility/resolve.go` (`ResolveForNode`) is the
statement of it. In one line: nearest level wins with its **whole config
block**, and eligibility filters dispatch rather than resolution, so an
ineligible node still shows what it *would* get with the skip reason.

### 3.4 Agent-side apply

Shipped verbatim — `agent/pkg/application/handlers/utility_config_linux.go`
plus the slug's entries in `domain/node_command`. Two consequences visible at
neither site: **one wholesale slug** exists because supersede keys are
slug+node, so per-utility dispatches sharing a slug would retire each other
(the defect `apply_device_profile`'s own slug avoids); and commands run inline
on the agent's single stream-read loop, so a multi-minute apt run parks the
node's command channel and web terminal, and an in-flight apply **cannot be
cancelled** — there is no cancel path for node commands. The UI says both.

### 3.5 The generation protocol (the genuinely new engineering)

The access precedent is ONE hash per node; utilities need three things at once
and the design must be written against `access_generation.go`'s code, not its
summary:

- The **union hash** over the consented set exists only as the dispatch/supersede
  key.
- The agent echoes a **per-utility marker map** (`utility.<id>` = `applied`, or
  a failure reason). `SettleApply` takes the RUNG from the marker and the HASH
  from the command's own **dispatch payload** — never from the agent's echo
  (the access-generation rule). *The design said the settle would RE-RESOLVE a
  per-utility hash platform-side and compare; that is not what shipped.* The
  safety property holds either way — a payload hash is the platform's own
  string, so an agent cannot choose the generation it is credited with — but an
  assignment edited while the command sat queued settles the OLDER dispatched
  hash, which the ladder then shows as `update_pending`.
- A marker counts **regardless of the command's overall status**: the wholesale
  command reports failed when any utility fails, and one refused utility must
  not redden converged siblings (a raced verify would otherwise redraw green
  rungs red).
- A partial/refused utility **clears** its applied generation rather than
  recording one (`RecordFailed` blanks `applied_hash` — the access partial-apply
  rule).
- **The drift attempt cap shipped** with the loop that needed it:
  `node_utility_states.drift_failures`, incremented by `RecordFailed`, zeroed
  by `RecordApplied` AND by any `SetDesired` whose source hash differs from
  the stored one. Keyed per (utility_id, desired SOURCE hash) rather than
  derived from command history, because the counter has to survive the
  history window and the source hash is what an edit moves. Recovery is the
  access shape: an assignment edit resets it, and the manual Apply is allowed
  regardless. Cap: `utilityDriftMaxConsecutiveFailures` = 3.

### 3.6 The reconcile loop

**Shipped** (`services/utility/reconcile.go`), as ONE trigger that
covers what the four below describe: `ReconcileNode` runs on every managed-OS
system-info report — the same signal access config self-heals on, and the only
moment the node's real boot mode is fresh. Triggers 2, 3 and 4 collapse into
it because they are the same question asked of the ladder ("does what we
recorded still match what resolves now"): a fresh joiner has no rows, a
reimaged node has no rows either (`install_os` drops the ladder), and an
edited config moves the source hash. Trigger 1's *push*
half — dispatching to the whole affected set the moment an assignment changes
— deliberately did NOT ship: the reconcile pulls per node instead, so a fleet
converges as its nodes report rather than in one fan-out, and the operator's
explicit Apply remains the way to converge a fleet now.

Five properties worth not re-deriving, each of which would be a defect if
undone:

- **Drift is decided WITHOUT resolving secrets.** `ResolveForNode`'s
  `ConfigHash` is exactly the pre-resolution hash the apply records as
  `desired_source_hash`; resolving per report would stamp `last_used_at` on
  every referenced secret at every reconnect, i.e. fake usage.
- **An empty stored source hash is not drift.** Rows written before the column
  existed say nothing, and treating them as changed would re-apply the whole
  fleet once, on the upgrade that introduced the loop.
- **A live apply (registered included) owns the node** — the loop skips. A
  registered command is waiting for exactly this node, and re-dispatching
  would supersede the command this very reconnect is about to deliver. This
  guard fails **CLOSED**, unlike the count caps.
- **The pacing bound applies to the loop**, not just to fan-outs: a rack
  powering back on reports system info for every node at once.
- **The apply carries no user** (`uuid.Nil` → `SystemActor` at the command
  audit choke point): the platform did it, not whoever last touched the
  assignment.
- **The dispatch carries the WHOLE resolved set, not just the drifted
  members.** `apply_utility_config` is wholesale by design — the union hash is
  the supersede key and the agent converges the set as a unit — and the
  agent's appliers are idempotent by construction, so a converged utility
  short-circuits on the device. The real cost is one command per reconcile
  while any member keeps failing, which is exactly what the 3-strike cap
  bounds.

The original four triggers, kept for the reasoning behind each:

1. **Config/assignment change fan-out** — the OU access-config fan-out's shape
   (changed-value trigger, reaches-this-level predicate) but the per-node work
   rides the
   **queued command path**, not the detached walk: one apt install can take 30
   minutes. Consent-gated utilities fan out only what the assignment
   confirmation covered; disruptive transitions park pending per node.
2. **Drift self-heal** on every managed-OS report — recorded ≠ desired
   re-applies, subject to the 3-strike cap. Never fires for consent-gated
   transitions; those surface as pending instead.
3. **Reimage re-apply** — the drift branch alone is blind to `install_os`
   (recorded still EQUALS desired after a wipe; the disk is gone but the
   bookkeeping matches). A utility analog of `reapplyAccessAfterRecovery` /
   `accessReapplyNeeded` — a successful `install_os` postdating the last
   successful apply re-enqueues it — runs at the recovery→OS transition. For
   consent-gated utilities the re-apply parks pending (a reimaged kiosk fleet
   must not schedule 50 unattended reboots).
4. **Guarded first-connect apply** — the access shape, with the `GrantsALogin`
   guard replaced by "never auto-apply a consent-gated or destructive utility on
   first connect."

### 3.7 Fleet pacing

The access-config fan-out is safe because an access apply settles in seconds; a
utility apply is 100–1000× heavier and pins the node to `maintenance` for its
duration. Phase 1 therefore ships with:

- A per-account bound on concurrently in-flight `apply_utility_config` commands
  (`WORKFLOW_MAX_RUNNING_NODES_PER_ACCOUNT` is the precedent and 250 the
  reference number; code-default armed, 0 = off).
- **Canary-first ordering** on any multi-node fan-out (shipped):
  the apply dispatches to ONE node — a connected one when any is — and parks
  the rest; `SettleApply` reads the canary marker off the settled command's
  payload and dispatches the remainder, recomputed from the assignment as it
  is THEN, only on a successful settle. Deliberately not durable: a backend
  restart between canary settle and continuation, or a failed canary, leaves
  the parked nodes parked until the operator re-runs the apply (idempotent —
  a re-run re-canaries, and a converged canary settles cheaply). The marker
  never reaches the agent (the proto builder forwards union_hash and
  utilities alone) and continuation commands carry none, so the fan-out
  cannot cascade.
- A visible warning that a credential revocation queued behind an apt run waits
  for it (single command goroutine).

### 3.8 Authorization, audit, caps

- New actions `utility.read` / `utility.manage`. `utility.manage` is strictly
  more powerful than `playbook.run_privileged` (root software on every node an
  assignment reaches), so: account-wide assignments require an **account-wide**
  binding (`rolesCovering` note), OU-scoped holders assign only within their
  scope, and the sensitive-secret param rule (§2.4) applies at save on the
  merged document (the workflow-templates lesson: a merge invalidates checks
  written against the submission).
- `audit.Recorder` is a required constructor argument; the device command is
  recorded at the existing choke point (`node_command.<slug>`); config jsonb
  never enters `AuditEvent.Detail`.
- Swagger annotations are test-enforced; Postman follows.

## 4. Frontend

Shipped — `frontend/src/entities/utility` and `pages/utilities` are the spec.
One rule the code cannot state for itself: a drop on the rail **creates** an
assignment and immediately opens the config sidebar, because a bare drop must
not leave a half-configured state nothing surfaces.

## 5. Per-utility

### 5.1 Docker — shipped with the chassis

`registry/utilities/docker.yaml` plus the agent's docker applier are the truth.
`utility_registry/loader_test.go` asserts the one thing that must never regress:
`daemon.json` log `max-size`/`max-file` caps **on by default**, because
unbounded json-file logs are the #1 SD killer and a Docker utility that
contradicted the fleet's own SD-wear story would be worse than none.
Compose-file delivery stays a non-goal (§7) — demand for it is the signal for
a Docker param family, not a second utility.

### 5.2 Kiosk — a container on the Docker utility

Shipped as `class: reconciled` (a container install needs no reboot):
`registry/utilities/kiosk.yaml` here, and the image in its own public
repository, `github.com/uptime-lab/orchestrator-images` (`kiosk/`; one
directory and one GHCR package per image, so later custom
images for other utilities go there too). Published as
`ghcr.io/uptime-lab/orchestrator-kiosk:<semver>` by that repository's
workflow on a `kiosk-v<semver>` tag.

A utility whose STORED config no longer validates (a param made required, a
default removed) is DROPPED from the dispatch and its own rung records why;
the rest of the node's set still converges. Refusing the whole node — which
is what shipped first and was corrected — meant one
stale assignment silently blocked every sibling utility on that node, on every
reconcile. The recorded failure also feeds the reconcile loop's 3-strike cap,
so a config nobody fixes stops being retried rather than being retried
invisibly forever.

The `image` param's default must name a PUBLIC package: devices have no
registry-credential mechanism, so a default that lived in a private registry
failed at pull on every device outside the bench where the image had been
hand-loaded, and the default was removed until the package was built by
`orchestrator-images` and published public. It is now
`ghcr.io/uptime-lab/orchestrator-kiosk:<semver>`; bumping it is a one-line
`kiosk.yaml` change per image release. The platform refuses to dispatch an
imageless kiosk config (the generic required-param check in `ApplyToNode`,
reason naming the remedy), and the agent refuses it too for older backends.
Only the GHCR address published by `orchestrator-images` is canonical.

**Docker is implied, not assigned.** `depends_on` is honoured at
resolution: a unit with only kiosk assigned resolves docker too, with docker's
defaults and the kiosk assignment's provenance, no assignment row, and
`implied_by: ["kiosk"]` in the node's effective view ("required by Kiosk
display"). Before that the set was refused at apply, per node, with "kiosk
needs docker" and nothing in the UI pointing at the missing row. An explicit
docker assignment still wins with its own config; unassigning kiosk drops the
implied docker from the set and, like every unassign, dispatches nothing.
`Catalog.Validate` therefore refuses a `depends_on` target with a required
param and no default. **The `image` param is an enum of the platform's own
releases** (`enum_values` in `kiosk.yaml`, newest as default), not free text:
the agent's health probe and screen capture are written against this image's
contract, so an arbitrary image reads as failed even when it draws. A release
of `orchestrator-images` adds its tag to the list.

**Two image rules bought on a Pi 5 with a real HDMI display (before that only
`headless=true` had ever been exercised):** the base is
Debian **trixie**, because bookworm's Mesa 22.3 has no v3d 7.1 support and
the GBM allocator never comes up on a Pi 5 / CM5; and `WLR_BACKENDS` must be
**unset** for DRM output — the image once declared `ENV WLR_BACKENDS=""`,
which wlroots reads as an empty LIST of backends, so cage died at "Failed to
create allocator" on every real display while the container read as Up.
`seatd` runs with `SEATD_VTBOUND=0` because the container has no `/dev/tty*`
and a VT-bound seat never activates without one.

Hardware acceleration from the container — measured expectations, still to
validate on real boards:

- **GPU compositing / WebGL work**: the container carries its own Mesa `v3d`
  userspace talking to the kernel via `/dev/dri`; containerization adds no
  overhead (same kernel, direct device access). This is what makes the UI
  smooth and it is the part that matters for menu/dashboard kiosks.
- **Hardware video decode is per-board**: CM4/Pi 4 expose an H.264 decoder via
  V4L2 (`/dev/video1x`, must be passed in; browser support outside Raspberry Pi
  OS builds is historically fiddly). **Pi 5/CM5 have no H.264 hardware decoder
  at all** (HEVC only) — H.264 is software-decoded there natively too, and the
  CPU handles 1080p. Video-heavy signage is therefore a per-board validation
  item, not a container question. DRM/Widevine content is out of scope.
- Browser engine (Chromium vs WPE/cog) becomes an **image** decision we control
  and can A/B on a bench, not a fleet-wide packaging commitment.

Health: a green systemd/container is not a working kiosk — the scan's kiosk
probe must ask the browser (DevTools over localhost) what URL is actually
rendered; a black-HDMI-with-green-unit is the failure mode. URL changes are
non-disruptive: container env restart (one blink) or DevTools `Page.navigate`
(seamless). Per-node URLs via `{{node.attributes.kiosk_url}}`. Recovery path is
named, not implied: a bench has power-cycle + netboot; on an install running
`PROVISIONING_BOOT_FAILSAFE_THRESHOLD=0` a boot-broken kiosk is a manual
operator action — the catalog description says so before anyone assigns it.
Interaction with RO-root: under overlayroot `/var/lib/docker` lives in tmpfs
(images evaporate at reboot, re-pull needs network) — kiosk+RO requires a
writable data partition/NVMe for `data-root`, expressed as a catalog constraint.

### 5.3 Read-only root — not a utility; an OU policy

`overlayroot` is mature on Ubuntu/Pi and mechanically simple (tmpfs upper over
RO lower; `/boot/firmware` stays RW so config.txt patching survives; enable and
disable each require a reboot — the overlay is mounted in the initramfs, a
running root cannot be converted). The agent tolerates it today: liveness file
and net-heal marker live in `/run` (tmpfs), the adoption file is only read.
Temporary writes (metrics, logs, /tmp) keep working — the upper layer IS a RAM
disk — they just don't persist, which is what temporary means.

What breaks is **intended-persistent** writes: `update_agent` (dpkg),
`apply_access_config` (including key revocation), watchdog config, tracked-file
Apply — each lands in tmpfs, evaporates at reboot, and the platform records a
success whose applied generation then *suppresses* re-apply. Therefore:

- RO is a **`PolicyFlag`** (`inherit|on|off`) in the existing OU policy family
  with a per-node override — maintenance does NOT require moving the node to
  another OU: set the node override to `off`, reboot, work, set back to
  `inherit`, reboot (the `boot_to_os` pattern).
- It is **consent-gated** both ways (reboot on enable and disable), and the
  first-connect guard never applies it to a freshly adopted node.
- **Prerequisite, shipped first and standing alone**: the agent reports overlay
  state as a system-info fact and the platform gates write-shaped commands on
  it, **fail-closed**: agents below the fact's floor display "persistence
  unknown" and refuse the RO policy; the confirmation scan independently
  verifies overlay state rather than trusting the system-info claim (an agent
  free to misreport RW is the revocation-suppression scenario one layer down).
  This gate protects five *existing* features against a hand-configured
  overlayroot node today — it is an open correctness item independent of
  Utilities.
- The un-brick path for credential changes on an RO node exists:
  `apply_access_config_offline` from netboot recovery.

### 5.4 Kubernetes — deferred, concept split

Per-node k3s **install** is easy (binary + unit + service). Cluster
**formation** is ordered and cross-node — designate a server, wait Ready,
distribute a join token, join agents — and no current mechanism expresses that:
the workflow engine walks one graph per node with no cross-node ordering or
role assignment, so the honest revisit path is **new server-side
orchestration**, not "a generated playbook" (`wait_for_node` waits on a node
state, not on another execution's progress). Also: single-server k3s dies with
one SD card; HA needs 3 etcd servers whose write amplification is what RO-root
exists to prevent; 2 GB boards cannot be servers and no per-node capability
filter exists. If demand survives Docker: join tokens have an exact delivery
precedent (`attr:<nodeID>:<key>` node attributes — encrypted, on the rewrap
list), steady-state *membership* could be a utility, formation is its own
design. Until then, Docker absorbs the demand signal.

## 6. Phases

Phases 1–3 (chassis + Docker; convergence; the kiosk container) are shipped or
in flight and are tracked in the issue tracker, not here. What remains:

4. **Overlay awareness → RO policy.** The agent overlay fact + fail-closed
   write gate as its own release (it stands alone); then the RO `PolicyFlag` in
   the OU policy family with per-node override and consent flow.
5. **(Conditional) k3s** — only if demand survives Docker, as
   membership-utility + separate formation design (§5.4).

## 7. Non-goals (v1) and open questions

**Non-goals**: Kubernetes formation; compose-file/workload delivery (playbook
workaround documented); moving agent command execution off the single read loop
(surfaced instead); apt progress streaming; non-Debian distros
(`install_packages` requires apt — utilities are distro-coupled and say so);
per-node capability *filter* at assignment time (constraints check at apply);
Docker Swarm; DRM/Widevine content in kiosks.

**Open questions**:
1. Late-joiner default for consent-gated utilities: the chosen v1 behavior is
   park-pending; revisit whether an explicit "auto-install new nodes incl.
   reboot" opt-in per assignment is wanted once real usage exists.
2. `conflicts_with` at assignment: hard refusal or warning? (Leaning refusal
   for correctness-class conflicts like kiosk+RO-without-writable-data-root.)
3. Kiosk image contents: Chromium vs WPE/cog — an image A/B on a bench in
   phase 3, not a fleet commitment.
