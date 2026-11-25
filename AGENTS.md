# AGENTS.md

This file provides guidance to coding assistants working in this repository.

> **Budget: 150 000 characters.** Past it the harness truncates and the guidance silently
> stops arriving — check `wc -c AGENTS.md` before committing an addition. What earns space
> here is a rule an agent must not violate or a trap already paid for. Design detail belongs
> in `docs/*.md`, and a fact the code, a registry or `psql` can answer belongs there —
> several sections say "read the map, not this list" precisely because their copies rotted.

## Working conventions

- **Language**: everything written into the repository is **English only** — code, comments,
  commit messages, pull requests and all documentation.
- **Branches**: every feature on its own branch. Never commit feature work directly to `main`.
- **Commits, merges and pushes happen only when the person you are working with says so.**
  One approval covers the work in front of you, not the next batch.
- **No dangling work**: before ending a session, check `git status`,
  `git branch --no-merged main` and `git log origin/main..HEAD`, and report what is
  outstanding rather than leaving it behind.
- **User-facing copy follows `docs/COPY-STYLE.md`** — that file is the authority.
- **Docs are part of "done", and stale docs are a defect.** Update the entry where it LIVES,
  not a summary header. **Grep for claims your change just falsified** — search for the
  property you changed ("X has no retention"), not for the file you edited. **A documented
  acceptance is only as good as the thing it depends on**: ask what an accepted risk RESTS
  ON, not whether it is written down — a well-argued acceptance can be silently invalidated
  by one variable in a compose file. **Re-measure before repeating any description here.**
  This file has been wrong about its own state repeatedly; read the code or query the
  database, never paraphrase the doc you are about to update.
- **Docker images are built on the host that runs them, never on a developer machine**; Go
  and TypeScript builds and tests run locally as usual.
- **Feature work must respect the capability split** (`GET /api/v1/server/capabilities`):
  SSH-adopt exists only where the backend shares a network with the fleet; the EEPROM
  signer, netboot and SD provisioning exist wherever they are configured. The endpoint is
  **JWT-authed**, so a bare curl's 401 reads like a missing feature — ask it with a token.
- **Local, unversioned context lives in a `*.local.md` file** in the repository root —
  gitignored, never committed, never quoted in committed files or messages. Credentials and
  host facts belong there and nowhere else.

## Publishing discipline

This repository is public, and so is everything that reaches it: commits, branch names,
tags, pull-request and issue text, screenshots, Actions logs and pasted test output.
Three rules, none of which a checker can fully enforce:

- **Commit messages, branch names, tag annotations and pull-request text carry no deploy
  state** — not which install runs what, not when something was rolled out, not whether a
  host is healthy.
- **They carry no install or tenant facts** — no device serial, address, hostname, account,
  person or key id from any live system, no log line pasted from one, and nothing from a
  developer machine's environment (home-directory paths, local hostnames).
- **They never describe an unfixed vulnerability.** A commit message describes a fix after
  it is deployed, never before; the mechanics of a weakness are written down only once the
  fix is out. A security report goes through the private channel in `SECURITY.md`, never an
  issue.

The same policy governs code comments: **a comment states the property and the reason,
never the incident, the install, the date or the tenant.** "Fails closed on a read error
because a timeout once issued a session" is right; the date, the host and a finding id are
not.

**The publication guard runs in CI on every pull request, including one from a fork and
one that touches only documentation** — it lives in `.github/workflows/guard.yml`, which has
no path filter, while the test workflow `ci.yml` is path-filtered and skips a docs-only
change; never move the guard back behind a path filter (`tools/publication-guard/`,
documented in its README; no secret, no network).
`check_public_shape.py` flags identifier SHAPES — device serials, private and CGNAT
addresses, vendor-prefixed MAC addresses, home-directory paths, mailboxes outside the
example domains, internal hostnames, the phrasing of a private engineering journal (finding
ids, numbered backlog entries, audit-round references, dated measurements), files over
1 MiB and executables; `check_proto_copies.sh` asserts the generated protocol code is
byte-identical in its three locations; `check_doc_citations.py` asserts every cited
`docs/<NAME>.md` exists; `check_licence_boundary.py` asserts the licence map;
`check_workflows.py` asserts no workflow runs on a self-hosted runner or on
`pull_request_target` / `workflow_run`. One more required check, `private-identifier-guard`,
runs only in the merge queue and on push to `main` (its inputs are environment secrets), so a
contributor cannot run it locally: a clean run here is necessary, not sufficient. A documented false positive is silenced on its own line with
`publication-guard: allow <rule>`; every opt-out is counted, and one without a reason beside
it is a review finding, not a fix.

## Licence boundary

The tree holds two licences, decided per directory; `LICENSE` is the human-readable table
and **`REUSE.toml` is the map** — when they disagree, the map wins and the table is the
defect. `agent/`, `proto/` and every directory under `tools/` are Apache-2.0, as is the generated protocol code wherever it is copied and the public-key
files in any trust store; everything else (`backend/`, `frontend/`, `eeprom-signer/`,
`postman/`, `config/`, `docs/`, the root files) is the Fair Core License, which converts
each version to Apache-2.0 two years after its release.

**Code flows one way.** Code may move from the Apache directories into the FCL ones freely;
code is never copied from an FCL directory into an Apache directory except by the
licensor, and `check_licence_boundary.py` is how the tree says so in CI. A verbatim copy
that must exist in both (the URL-credential redaction, written in the backend and copied
into the agent) is declared Apache-2.0 at its source in `REUSE.toml` so that a change can
flow either way. A file's own `SPDX-License-Identifier` header never disagrees with the map.

The licence-key functionality the FCL Limitations clause protects is the **device
entitlement**: the backend's verification of the signed entitlement file
(`backend/pkg/webapp/domain/entitlement`) and the instance-wide admitted-device count in the
node repository. The signer that issues files (`tools/entitlement-signer`) is Apache-2.0 and
outside that boundary; a file counts only if signed by a key the backend embeds. Do not build a second mechanism that gates a feature on payment, and do not
remove or weaken that one.

## What this is

Orchestrator manages clusters of nodes (bare-metal/VMs). Four deployable components plus a
shared protocol:

- **`backend/`** — Go control plane: Echo HTTP API, bidirectional gRPC for agents, WebSocket
  push to the frontend. Module `github.com/uptime-lab/orchestrator/backend`.
- **`agent/`** — Go daemon on each managed node: gRPC client, executes commands, reports
  heartbeats and hardware fingerprint. Two binaries — `cmd/agent`
  (`orchestrator-agent.service`) and the telemetry-only `cmd/watcher`
  (`orchestrator-watcher.service`, `agent_kind=watcher`; the retired wire value `observer`
  is normalized on receipt and must not be reused); both ship as GoReleaser `.deb`s.
  Module `agent`.
- **`frontend/`** — React + TypeScript + Vite SPA.
- **`eeprom-signer/`** — the per-device bootloader signing service the backend calls.
- **`proto/`** — single source of truth for the gRPC contract. Generated Go is **copied into
  both** `agent/` and `backend/` (each keeps its own `.../grpc/proto/`), so **always run
  `make proto/generate` after editing `orchestrator.proto`** — the modules do not share it.

`backend` and `agent` are **separate Go modules** with separate `go.mod` files — there is
no workspace. A third, `tools/agent-signer`, signs agent artifacts (`docs/ARTIFACT-SIGNING.md`).
Read the `go.mod` files for Go and toolchain versions; a copy here rots at every bump.
What a release consists of, where each artefact comes from and how it is verified is
`docs/RELEASE-INVENTORY.md`. Supported boards: `docs/SUPPORTED-HARDWARE.md`.

## Common commands

The root `Makefile` dispatches into the per-module Makefiles via `make backend/<cmd>`,
`make agent/<cmd>`, `make proto/<cmd>`. `make help` and each module's `help` target list
what exists.

```bash
make db/up            # local Postgres via Docker (host port 5434, container 5432)
make db/down
make db/shell         # psql -U postgres -d orchestrator

make backend/run      # ./cmd/server — listens on whatever your gitignored
                      # config_local/config.yaml says (see Configuration & ports)
make agent/run
make backend/build
make agent/build-linux

make test             # backend, agent and tools/entitlement-signer unit tests
make test/integration # brings up the local Postgres, creates a scratch DB; the
                      # DSN-gated tests SKIP under plain `make test`
go test -run TestName ./pkg/...        # a single test
make backend/test-coverage             # coverage.out + HTML

make backend/lint     # golangci-lint must be installed
make backend/fmt

make proto/tools      # protoc-gen-go + protoc-gen-go-grpc (protoc is system-installed)
make proto/generate   # regenerates AND copies into agent/ and backend/

make backend/tools    # swag CLI
make backend/docs     # REQUIRED after changing any HTTP handler — see below

make agent/deb-arm    # GoReleaser snapshot -> dist/*_linux_arm64.deb
```

Frontend (inside `frontend/`; pnpm, Node >=22): `pnpm i`, `pnpm run dev`, `pnpm run build`,
`pnpm run lint` (max-warnings 0), `pnpm run typecheck`, `pnpm run test` (vitest).

## Go architecture (backend + agent)

Both Go modules follow **Clean / Hexagonal architecture**; the authoritative style guide is
`docs/CODE-STYLE.md` — read them before writing Go.

- **Layers**: `domain/` (pure) → `application/` (use cases, port interfaces) → `infrastructure/`
  (HTTP, gRPC, GORM, hardware); dependencies point inward only. Agent code that is
  platform-specific uses build tags, one file per platform (`command_handler_linux.go`).
- **Backend is CQRS**: Commands in `application/ports/in/commands/<name>/`, Queries in
  `.../queries/<name>/`, response DTOs as Results in `application/ports/out/results/`, one per
  file — never a result type in the commands package. **A `Validate()` method is the
  doctrine, NOT the current state**: most command and query packages define none and **no
  `echo.Validator` is installed anywhere**, so the `validate:` tags several commands carry
  never execute. Write a `Validate()` for a new command AND call it from the service. **For a
  partial update prefer `*string` fields** (`NodeUpdateCommand`, `SecretUpdateCommand`): an
  omitted field then means "unchanged" structurally, where a `Validate()` leaves the next
  author one forgotten call away from blanking a value — such as the email, which sign-in
  and password reset resolve by.
- **Ports/adapters**: named interfaces in `ports/`, never anonymous; GORM implementations in
  `infrastructure/database/sql/` assert `var _ repository.XRepository = (*XRepository)(nil)`,
  provide a `Setup()` migration method, and convert via `FromDomain()`/`ToDomain()`.
  **Dependency injection only** — `NewXService/Handler/Repository`, no globals; panic, don't
  nil-check, on a missing dep.
- **Errors**: shared `Err*` in `app_errors`, wrapped with `%w`, checked with `errors.Is`;
  `gorm.ErrRecordNotFound` converts to a domain error; handlers answer through the `results`
  helpers. **A TYPED refusal declares its own status**: implement
  `results.OperatorFacingError` (`HTTPStatus()` + `ErrorCode()`) and `SendInternalServerError`
  answers it as itself at every handler, including ones written later; a per-handler
  `errors.As` arm is invisible when missing, and the refusal then flattens into a 500 that
  discards the remedy. Implementing the interface is ALSO the author's statement that the
  message is safe to show; an ordinary error is never echoed, since it can carry SQL text and
  paths. Two live exceptions: the generic command route KEEPS its own arms, because its
  clients parse the legacy `{"error": <code>, "message": …}` body; and the typed error must
  ride the batch result's `Err` (`NodeCommandResultItem.FailureError()` — rebuilding it as a
  string destroys the type). A plain-400 refusal is wrapped in `app_errors.OperatorRefusal`
  **at the refusal site**: mappers echo `err.Error()` only for that marker or a domain
  sentinel, never through a catch-all. `app_errors.InvalidRequest`/`StateConflict` (400/409
  with a code) are the typed form, answered by every handler with no arm.
- **An `Update` that `Select`s a column list persists exactly that list**: the lists are
  package vars held to every model column by `update_columns_test.go` — add a column to
  the list or name it there as left alone.
- **Logging**: `log/slog`, scoped per component with `logger.With("service", "...")`.

### Backend wiring (`backend/cmd/server/main.go`)

The composition root, and the ordering matters: reset stale node connection states, start the
**gRPC server first** with a temporary node service, then HTTP/WebSocket, then wire the real
services back in via setters — a service the gRPC layer needs gets a setter, not a wider
constructor.

### Backend domains (each a full CQRS slice)

**Read `ls backend/pkg/webapp/domain/` for the list.** Handlers authorize **per action** via
`authz.Action*` — a new route needs an action, not just the auth middleware. Stated checkably:
**every route that reads or mutates tenant data needs an action; a static catalog does not**
(`GET /workflow-blocks` serves `workflow_block.Registry()` verbatim). The doctrine is not
universally in force, so follow it for new routes rather than inferring coverage from
existing ones; `GET /nodes/favorites` carries none deliberately (comment at its
registration). `rolesCovering` satisfies an account-level (nil-target) action only with an
**account-wide** binding, except the catalog reads in `authz.IsAnyScopeRead`, which any
binding carrying the action satisfies — so an OU-scoped grant yields a working session, while
the OU LIST stays narrowed by `AllowedOUs`. Widening that set is a product decision. Mutations
keep the strict rule; per-unit OU routes authorize in-handler against the unit.

The notable slices:

- **workflows / playbooks** — review and design: `docs/PLAYBOOK-SUBSYSTEM-REVIEW.md`. UI is the
  **Operations** nav section (route `/workflows`; no `/os-images` route, not even a redirect);
  API and DB keep `workflow-templates`. **`domain/workflow_block` is the source of truth for
  step types** — kind, params, ports, defaults, privilege — and drives the palette, the
  generated forms and save-time validation, so adding a block needs no frontend change. The
  rules that break something silently if undone:
  - `steps` is derived from the `graph` jsonb (`workflow_template/graph.go` → `Validate`);
    legacy rows get a synthesized graph on read. **Do not add a "reject non-chain graphs"
    check** — every branching playbook already saved would break.
  - **On the full-document PUT an omitted `graph` means UNCHANGED, never delete.** A NULL is
    unrecoverable: the synthesized replacement is a bare chain with no condition, join, wait,
    policy or schedule trigger, `schedule_cron` recomputes to `""`, and the playbook stops
    firing permanently with nothing reporting it.
  - A row that HAS a graph ignores submitted `steps`, so `Service.Update` re-runs the
    privileged-authoring gate on the **MERGED** document — a merge that makes the stored
    document differ from the submission invalidates every check written against it.
  - **Feeding secret material into an unprivileged block is a privileged AUTHORING act**
    (`WorkflowGraph.SecretFedUnprivilegedBlocks`): a `control.condition` on `{{secrets.x}}`
    with `contains` reads the value character by character through the branch that ran. The
    run API also omits such a block's state and everything downstream for a caller without
    `secret.read_value` — `completed` vs `skipped` is the same fact — and the node's command
    list and by-id read drop those blocks' commands the same way (`WithoutHiddenRunCommands`);
    their `node_command_*` frames go to `secret.read_value` holders only, by the same function.
  - **`server.secret_upsert` is bound to the run's node** (`secret.Service.UpsertForNode`):
    a new secret, or one delivered to that node alone; a wider one by name is refused, never
    shadowed. Server-block writes are audited as `runActor()` (starter, else snapshot author).
  - **`ParamSpec.Default` is applied once, by `BlockDefinition.ApplyDefaults` in `startBlock`,
    for every executor kind.** Never re-implement a default at a consumption site; an omission
    is a missing key, a null or an empty string, never a zero or a false.
  - **`server.reboot_to_recovery` calls the platform's ONE-SHOT service, never
    set_boot_policy + reboot** — the one-shot restores the previous boot target on netbooted
    reconnect, so a run dying mid-capture does not strand the device. `control.wait_for_node`'s
    `managed_os` is "online and NOT `IsRecoveryBootstrap`", never a boot-device test.
  - **`system.apply_watchdog` is server-executed on purpose**: an agent block would skip the
    domain bounds, the stored desired state and the config hash. `apply_access_config` is a
    node_command slug with **no** block.
  - **A cron expression is validated at SAVE** (`domain/cronexpr`, shared with the scheduler):
    an out-of-range field is an ERROR, because "never matches" is indistinguishable from a
    schedule whose time has not come. Dialect is crontab(5)'s. **`POST /api/v1/schedule/preview`**
    (authed, no action) runs `cronexpr` on the SERVER so the editor never becomes a second
    implementation, and answers **200 with `valid:false`** because it is called while the
    operator types. `never_fires` is its own field: `0 0 30 2 *` is valid and does nothing.
  - OU scope is a SET (`workflow_template_ous`; the legacy `ou_id` is backfilled once in
    `Setup()` and never read again); availability and targeting are a UNION over it.
    Authoring and running are authorized unit by unit and EVERY unit must allow it; a playbook
    spanning several units pins NO OU on its execution (nil reaches account-wide holders only,
    which is narrower; naming an arbitrary member widens); and **the OU-delete guard must
    count the JOIN table** — `DeleteIfUnreferenced` is the live guard, `CountByOUID` has no
    production caller, and guarding on the legacy column lets the 2nd..Nth unit of a scope be
    deleted, leaving a ghost unit `validateOUs` refuses forever. Re-targeting is its own route
    (`PUT /workflow-templates/:id/organizational-unit`, optional `expected_updated_at`, 409),
    because the full-document PUT re-sends every field and a drag would revert another
    operator's save.
  - Executions pin a `template_snapshot` at start. **"Active" = `completed_at IS NULL`** (a
    transient `partially_failed` with a live node counts) — what the active cap counts AND
    what retention sweeps; in-flight execution nodes are re-enqueued on backend restart. The
    **scale guards** are the `WORKFLOW_*` block in `config.go`: the running-NODE cap, not the
    execution count, decides whether one tenant can own the worker pool, checked against the
    **requested** count as well as the current total, so the 429 lands before an execution
    row exists. `integration.http_request`'s `timeout_seconds` is clamped at **execution**,
    not at save, so the clamp covers playbooks saved before it existed.
  - **The scheduler's per-minute scan is TWO phases**: `ListScheduledCandidates` (a
    four-column projection over the denormalized `schedule_cron`, **bounded per account in
    SQL**), then `GetByIDs` for the minute's matches, so most ticks read no graph jsonb. The
    cap must bind in the QUERY — applied in the caller it bounds what FIRES while the tick
    still streams everything the tenant owns — and the scan asks for **cap+1** so it can log
    an over-cap account. A candidate is a CLAIM; `stillDue` re-checks it against the graph.
  - **A panic in a graph step is contained per execution node** (`processNodeGuarded`) and
    the node is FAILED through the same settler cancel uses, or the restart re-enqueue turns
    an escaping panic into an unbounded crash loop. `withNodeClaim` must release via
    **`defer`**, and the settle path recovers around **itself**, since it runs from a defer
    where a second panic escapes the first.
  - Two other settlers (`services/workflow_execution/abandon.go`), both idempotent and neither
    rewriting a finished outcome: **deleting a node settles the runs targeting it**
    (`AbandonNodeExecutions`, via `SetExecutionAbandoner`; the only other settler runs on
    RECONNECT, which a deleted node never does), and `POST /workflow-executions/:id/cancel`
    (`playbook.run`, not `run_privileged` — a cancel only stops work). Both settle the block's
    command at ANY stage, `in_progress` included (`FailCommandOfStoppedRun`; the send path's
    `MarkNodeCommandFailed` skips it), release the node and fail the block in `graph_state`; the
    device is not told — there is no kill — and its late report is refused as terminal.
  - **A settle must also stop work already in flight, and that takes TWO guards.**
    `UpdateActive` (atomic `SELECT … FOR UPDATE` + status re-check) stops a stale worker copy
    resurrecting a settled run; and `startBlock` re-reads the stored row before it starts ANY
    block, so a cancelled run cannot dispatch one more `erase_drive`/`install_os`/`reboot` on
    its way out — a discarded database write does not un-send a device command. Before EVERY
    block, because a list of "blocks known to park" is one somebody has to remember to
    extend. It fails OPEN on a read error and treats a vanished row as settled.
- **`organizational_unit`** — 2-level OU tree; node policies resolve **node → OU → root →
  account default → default** with the source reported, booleans tri-state `PolicyFlag`. A
  node whose `ou_id` points at a deleted unit resolves as **unassigned with a warning**, never
  an error — as an error it reaches HTTP-boot as a 404 for `boot.sig`, and a network-first
  node with a stale reference can never boot again. Delete and assignment are serialized
  against each other (`OUMutationRepository`). **A PUT on a unit or on the account policy can
  dispatch to the FLEET**: changing `ssh_password_auth`, or reparenting a unit, enqueues
  `apply_access_config` for every affected node. The affected set is "does the inheritance
  chain REACH this level", never "which level decides", or a unit set back to `inherit`
  pushes nothing exactly when its nodes start following the account; it fires on a
  **changed** value, never a supplied one (the full-document PUT re-sends everything); and it
  runs **detached** (`context.WithoutCancel`, bounded). A reparent pushes at all because
  access entries resolve as a UNION across levels. Design: `docs/ORGANIZATION-PAGE-PLAN.md`.
- **`access_entry`** — declarative shell access: a named OS user (ssh keys, `sudo`, optional
  password, optional certificate principals) or a trusted external CA — a trust anchor only,
  principals living on the USER. Scope is an OU set XOR a node XOR account-wide XOR
  unassigned (the safe default: it applies to no node); resolution is a **union** node → OU →
  root → account, the nearest level overriding a username *entirely*, shadowed entries
  reported. Lifecycle is **derived, never stored**: no keys → user LOCKED via `chage -E`,
  home kept (`usermod -L` alone does NOT block key or certificate logins); keys back →
  unlocked; keys swapped → user untouched; no password → `sudo` is NOPASSWD, else it prompts.
  **A password reaches the device only as a crypt(3) `$6$` hash**
  (`crypto.HashPasswordForShadow` → the agent's `chpasswd -e`; `install_os` sends no
  credentials at all); the proto's retired `password` field (5) **must never be reused**, and
  no cleartext-password path may return to the agent. **A multi-line ssh key saves and fails
  at both applies**: `ssh_key.ValidateOpenSSHFormat` validates only the first non-empty line,
  the agent's `validateAuthorizedKeyLine` refuses the whole value. `apply_access_config`
  reconciles wholesale into the root-owned `/etc/ssh/authorized_keys.d/%u`, never
  `~/.ssh/authorized_keys`; ownership is bounded by the `orchestrator-managed` group and
  pre-existing users are never auto-adopted. A node awaiting approval takes no commands: an
  access push to it is a SKIP, not a failure, and `ApproveNode` converges access and
  utilities itself (`convergeOnApproval`) — nothing else re-sends them before the agent
  reconnects. A config that would leave no working login is
  **refused twice**, at dispatch (`AccessConfig.WouldLockOutAllAccess`) and on arrival — the
  agent asking against the MANAGEABLE set — but not at SAVE time. Keys go on and off per key
  (`POST/DELETE /:id/keys/:keyId`); **never a whole-list PUT, which reintroduces silent
  replacement**. `apply_access_config_offline` (not queueable, device-exclusive) mounts the
  target disk from a netbooted agent and writes the resolved config into the installed
  rootfs; `POST|GET /nodes/:id/os-users/scan` inventories the REAL OS accounts through a
  fixed read-only `run_script`, returning a server-parsed result, never raw output.
- **`node_file` / tracked files** — `node_tracked_files` is the identity, `node_files` the
  version log, encrypted at rest and marked by the `content_enc` COLUMN — never a byte
  prefix, since a tracked file can itself start with `encv1:`. Path policy is
  `domain/node_file/paths.go`, quotas `config.NodeFileConfig`. The per-version
  **`MaxContentBytes` bounds a gRPC FRAME, not storage**; **the path travels as a query/body
  field, never a route segment**, since `/files/*` and `/files/*/versions` cannot coexist in
  Echo, and the collision shows empty content in the editor; content comes
  from `GET /files/content` at `ActionNodeCredentials` (list and versions are metadata only),
  the two platform `config.txt` paths carved down to `ActionNodeRead`; and every file
  operation is refused while netbooted. **A track/refresh read is recorded at its result
  settle** (`CompleteTrackedFileRead`), never only by the client's poll: the read carries
  the reserved payload key `results.TrackFileForKey`, written from a context only
  `ReadNodeFile` sets — never from params, so a generic or playbook `read_file` tracks
  nothing — and stripped from every response.
- **node watchdog** — `nodes.watchdog_config` (jsonb desired) + `watchdog_applied_hash` /
  `watchdog_disarm_reason`; presets and their operator wording live in `domain/node`. Also an
  inheritable OU/account policy as ONE block (`ResolveWatchdogConfig`) with three rules: the
  OU/account save **dispatches nothing**; a declaration is always an ARMED supervision (a
  disabled submission clears to inherit), while a node's own disabled config shields it from
  inheritance; and the sidebar renders an inherited config as a proposal with an explicit arm
  action. The agent renders `/etc/watchdog.conf` from typed fields, refuses on-device any
  config that would reboot-loop the node, and disarms after three restarts. **A disarm
  carries its source**, and the backend ignores its own `PlatformDisarmReason` for agents
  predating the field, or every deliberate disarm resurfaces on the next agent start as a
  device self-disarm. The status scan is gated on `ActionNodeStatusCheck` because it
  dispatches a command and flips the node to `maintenance`. **Nothing may arm a device without
  a human present.**
- **node LED control** — per-node `nodes.led_config` + `led_applied_hash`, plus an inheritable
  OU/account policy as ONE block (`GetLED` reports `desired_source`). Blue is reserved for
  identify, which preempts the ambient loop and whose pause must release the driver handle
  synchronously; board wiring lives in the device-profile registry. **The OU save dispatches
  nothing** — a device gets LED state only through a per-node apply.
- **`utility` / Utilities** (`docs/UTILITIES-PLAN.md`, `docs/UTILITIES-RESEARCH.md`) —
  OU-assignable software roles from the embedded registry `infrastructure/utility_registry`
  (the catalog), applied by one wholesale slug with a per-utility ladder in
  `node_utility_states`. The applied hash is written at settle from its **DISPATCH payload**,
  and a marker `applied` records the rung **regardless of the command's overall status**, so
  one failed sibling does not redden converged ones; scope is account-wide XOR OU set
  (`utility_assignment_ous`, whose JOIN the OU delete guard must count) XOR node XOR
  unassigned, tie-freedom being the SERVICE's fail-closed overlap check, not an index;
  unassign RETAINS and dispatches nothing; **a `depends_on` is IMPLIED at resolution**
  (`implyDependencies`: no assignment row, `implied_by` in the effective view; an explicit
  assignment still wins, and `Catalog.Validate` refuses a dependency whose required param has
  no default); params resolve `{{secrets.*}}`/`{{node.attributes.*}}` per node **before
  hashing**, failing rather than emptying and refusing sensitive secrets. Utilities and OU
  policies stay separate families — read the research doc before proposing a merge. **Kiosk
  releases are identified by a mutable image TAG by decision**, which RESTS ON the image
  repository being single-owner; whoever pins must migrate the stored assignment values with
  the catalog, or a stored `image` outside `enum_values` fails every apply.
- **node favorites** — per-user favourite nodes, a personal view with no policy semantics;
  service+repo+handler only, no `domain/` package.
- **`node_attention` / the Status section** — two account-wide reads, both views over nodes
  and therefore both on `ActionNodeRead` with `AllowedOUs` narrowing IN the handler:
  `node.read` is not an any-scope read, so a route-level `RequireAction` would refuse every
  OU-scoped member. `GET /fleet/attention` resolves the whole account in ONE pass
  (`services/node_attention`) and calls the pure `domain/node_attention.Evaluate` per node.
  **A check whose input failed to load is named in `omitted`, never answered** — an
  unreachable signer must not make a fleet read as current; **an Item carries a kind and raw
  values, never a sentence**, so the wording stays translatable; and the kind inventory ships
  in the response. `GET /fleet/activity` is the fleet command log; it serves **no payload and
  no result on purpose** — those resolve `secret.read_value`, which writes a
  `secret.values_read` audit row, so a paged log would stamp the trail once per page load.
  Its visibility set is nil-versus-empty: an empty slice means "this caller may see no node".
  **A firmware VERSION hash does not identify the SoC**: the BCM2711 and BCM2712 images of
  the SAME rpi-eeprom release carry the same release commit, so the freshness check must look
  the base up BY SoC; an unrecognised board must yield no verdict rather than a default
  family. **Fixing from the page adds NO backend surface**: the row buttons and the group
  actions dispatch the endpoints the node sidebar already uses. The catalog is
  `frontend/src/entities/fleet/remedies.ts`; a condition with no honest one-click answer has
  no entry, and `reboots: true` makes the confirmation say so. Only `update_agent` has a real
  batch endpoint (`POST /nodes/commands`); the rest fan out per node, bounded.
- **`account_export`** — `GET /api/v1/account/export` returns the account as one JSON
  document, built as a **composition of reads the same caller can already perform**, so it
  inherits their scoping. It carries **no credential material at all** (`has_*` booleans),
  and **a section that cannot be read is named in `omitted`** while the export still
  succeeds — an empty array and an unreadable one are otherwise the same JSON. Gated on
  `ActionAccountExport`, held by no role below super_admin. Image FILES are listed with
  checksums, each fetched from `GET /images/:id/download`.
- **`account_deletion_request`** — **it files a request; it deletes nothing.** The erasure is
  an instance-administration task outside this API, which owns the table inventory and the
  purge tripwire; a second implementation means two inventories that drift into a HALF-purged
  account. One open request per account, enforced by a **unique index**; the row is transient
  and the audit trail is the durable record — but the customer's free-text reason is
  **never** copied into the audit detail. Because the purge lives outside this tree, **a new
  account-scoped table is a contract change for whatever performs the purge** — name it in
  the pull request.
- **`entitlement` / the device cap** (`docs/ENTITLEMENT.md`) — the licence-key functionality
  named above. Rules that break silently if undone: the count is `node.IsAdmitted` and its SQL
  twin `CountAdmittedNodes` (approved, not `ready_for_adoption`/`flashing`); **the cap bounds
  the TRANSITION into admitted, never row creation** — no creating path creates an admitted
  row outside the gate. The node-token first connect, the master-token placeholder reconcile,
  `ApproveNode` and the restore of a node admitted at deletion write through the locked admission (count of every other row + the write in one transaction
  under one `pg_advisory_xact_lock`); a refusal writes nothing, so the row admits later; any
  new write that admits a row must use the same gate. A lock or count that fails (a statement
  or lock timeout included) while a cap applies REFUSES with nothing written — typed 503
  `device_cap_unverified`, or `ResourceExhausted` + the fixed `device cap could not be checked;
  retry shortly`, which the agent retries through its reconnect backoff — never a write outside
  the lock; cap 0 never counts; reconnects never reach the gate. A failed read
  of the stored FILES never lifts the cap — the last complete resolution answers, else the
  boot file, else the free cap, uncached. **The free cap
  is a FLOOR**: a signed cap of 1–24 verifies and `EffectiveCap` clamps it to 25 (the signer
  refuses to issue one); 0 stays unlimited. The refusal is
  the typed 409 `device_cap_reached`, or `ResourceExhausted` + the fixed `device cap reached`
  over gRPC, on which the agent stays up and holds ~15 min (jittered) before asking again,
  one WARN. Only the instance operator (the oldest account) may apply a file or read its
  terms; other accounts' super_admins get a typed 403 and a summary. The per-account
  limit (`ACCOUNT_DEVICE_LIMIT`) rides the SAME locked gate (`repository.AdmissionLimits`);
  its override columns are written only outside this tree.
- **`node_tombstone` / deleted nodes** — a deleted node is never told, so it retries forever
  with an identity nothing recognises. `node_tombstones` keeps what it WAS, including
  `unique_token` and the `boot_to_os` override; it is **not a soft delete** (the node is gone
  from every list, query and count), and its routes are account-scoped. The tombstone write
  is the ONE part of `node.Service.Delete` that is not best-effort; a restore reuses the
  **original id and token** and **restores** `boot_to_os`; **a restore is never an
  uncounted admission**: a node `was_admitted` at deletion comes back admitted only through
  the locked admission (typed 409 `device_cap_reached` at the cap; the tombstone stays
  restorable), any other with its recorded approval and pre-connect status, and a legacy
  NULL as a placeholder; `adoption_method` decides whether
  a restore returns the DEVICE (`eeprom` yes, `file` only while the adoption file survives) —
  and **only while the serial is still free**: a tombstone's serial is invisible to
  `ListByExactSerial`, so anyone may bind it meanwhile. `RestoreDeleted` refuses that case
  with a typed 409 `serial_bound_elsewhere`, and a lookup FAILURE is a plain error, never
  that refusal — restoring the row without its fingerprint is a green restore that strands
  the device. **A live node of the SAME account holding the full serial, or its guarded
  low word (a browser re-flash's stamp), refuses too** (the board re-adopted as a new node;
  two live rows on one serial let the boot lookup pick the stale one): typed 409 `serial_bound_in_account` naming that node, asked BEFORE the
  guard's own-account exemption; the list carries it as `serial_bound_in_account` (id,
  name; `restorable: false`) via `SerialHoldersInAccount`, and `serial_bound_elsewhere`
  stays cross-account only. `ListDeleted` marks cross-account rows through the `SerialBindingScopes` projection
  (fail-open), never `ListByExactSerial` (it decrypts another tenant's credentials per row)
  and never `guardSerialBinding` (it writes an anomaly per refusal); the predicate is
  `node.SerialConflictsAcrossAccounts`. The copy claims a SERIAL binding, never that the
  device moved. Deleted nodes are grey rows IN the node table (`DeletedNodeCard`; filter
  semantics in `entities/node/statusFilter.ts`), with Restore/Forget in `SidebarDeletedNode`,
  which hands over to the live node's panel under the SAME id after a restore. **A
  never-admitted binding is a LEASE** (`QuotaConfig.SerialLeaseDays`; the hourly sweep clears
  the fingerprint — row kept, audited as the system — of an awaiting-first-connect or
  pending-approval node with no life; an admitted node — `admittedScope`, which includes a
  node restored from an admitted tombstone and still awaiting its device — is never touched).
  **The global HTTP-boot lookup weighs exact AND suffix candidates together**
  (`node.ResolveBootCandidate`): a short serial is not unique, so the node whose EEPROM
  readback (`boot_http_path`) names the requested path wins, and a set still spanning
  accounts refuses; across accounts a row that is not admitted never wins while another
  account has an admitted node matching the serial, and no readback wins over another
  account's admitted node whose readback is unknown (both refuse instead); that readback
  also exempts the node from the suffix-hit strike. **The readback is the agent's own
  report**, so it counts only when it names the node's own serial or its low word, and
  what keeps a foreign row off a device's path is the bind guard: `guardSerialBinding`
  refuses, on every bind path, a serial whose LOW WORD collides with another account's
  full serial or short stamp (short against short stays allowed). An ambiguous lookup
  answers `ErrHardwareSerialAmbiguous` and is not struck.
  **`GET /nodes/adoption-refusals` is a PROJECTION of the tenant's own
  `serial_binding_conflict` anomaly rows**, not a table. HTTP-boot's enumeration guard
  **exempts a deleted node's serial**, or a stranded device slowly bans its own site's IP.
  **`DELETE /nodes/:id` refuses a CONNECTED node with 409 unless `?force=true`** — online,
  maintenance OR restarting (`node.IsInConnectedState`; online alone misses a node wedged
  mid-install). A forced delete dispatches `release_identity` best-effort; the tombstone
  records `identity_release_requested`, **requested, not confirmed** (the terminal status
  arrives after the row is gone), so that DTO field must not gain `omitempty`; the gRPC
  settle stamps `identity_released_at` when the device REPORTS success — a claim, audited as
  `node.identity_released` by the system actor with `reported_by=device`. The tombstone also
  records `eeprom_bake_confirmed` (`BootloaderFlashedAt` set — the `HTTP_HOST` heuristic
  behind `adoption_method=eeprom` only says the EEPROM points at a platform) and
  `boot_order_netboots_first`. **`release_identity` is cosmetic on a baked Pi running an agent
  below its floor**: an older `ParseAdoptionFile` fell back to the EEPROM blob when the disk
  file was missing. The current agent leaves a `.released` marker beside the removed file and
  refuses the fallback while it exists; a new adoption file clears it.
- **reboot-to-recovery** — a one-shot netboot: force `boot_to_os` to netboot, reboot, restore
  the saved target when the node reconnects reporting `current_boot_device == http`
  (`recovery_revert_pending` / `recovery_prev_boot_to_os`). **Refused (409) before anything
  is written** when the node is not online or cannot reach `http` boot
  (`BootOrderCanReturnToNetboot`, unless netbooted now, or `BootHostMismatch` — checked in
  the service, so the playbook block is refused too); an undelivered reboot puts the
  saved target back — a pin nothing will complete is an override nobody asked for.
  **While an `install_os` is unsettled, HTTP-boot serves recovery whatever `boot_to_os`
  says**, and every write of `failed` to one forces netboot (the disk may hold a partial
  image), as does every write of `success` to an `erase_drive` on a node in recovery (the
  boot disk may now be empty): `node_command.Service.setStatus` is the package's only status
  writer and applies both, pinned by a structural test; a delete mid-install records netboot on the tombstone. It
  applies the capture rule too: a `capture_image` ending in any terminal state but success
  fails its image record if still `capturing` (a stream in flight settles its own).
- **`secret`** — account-scoped secrets (`env|credential|opaque`) encrypted at rest; material
  embedded in jsonb carries the `encv1:` token prefix. A `sensitive` secret is write-only and
  the true→false transition is **forbidden** (recreate instead); the name is **unique per
  account**, being the `{{secrets.<name>}}` identifier (no `}`, checked at create and rename
  only); an env value carries no line break; an attachment's subject is a secret
  XOR a tag, targeting an OU XOR a node, and a **tag attachment has dynamic membership**;
  precedence is node > OU > root, direct beating tag within a level; detach is `DELETE
  /secret-tags/:tag/attachments/:attachmentId` and **nothing is registered on the collection
  path**, so a script written against the collection gets a 404 and the detach looks broken
  when it is the call that is wrong. A `secret_tags` row is only a colour override.
- **node attributes** — per-node key/value pairs **backed by secrets, no new table**: an
  `env` secret named `attr:<nodeID>:<key>` attached to that one node, delivered by key into
  the root-only `/etc/orchestrator/env` from agent 0.8.5 on (an older agent writes the login
  environment, `/etc/environment`) and resolvable as `{{node.attributes.<key>}}`, surviving a hardware swap since
  the attachment is to the logical node id. **They spend the per-account secret quota**, so a
  large fleet consumes it without anyone opening the Secrets page.
- **provisioning** — `POST /api/v1/provisioning/sd` mints a master token and streams a ZIP
  boot bundle; `PROVISIONING_SD_PAYLOAD_DIR` points at the extracted boot-partition dir.
- **one-time adoption code** — the per-node install link carries an opaque `ac_<hex>` (1 h
  TTL, single-use, SHA-256 at rest, re-mint revokes), traded at public rate-limited
  `POST /api/v1/adopt/exchange`, which answers a **uniform 404 on any failure** — no oracle.
  `GET /uninstall.sh` is the counterpart, public and tokenless because the device may already
  be off the account. **The order inside it is the feature**: `watchdog.service` reboots the
  machine when the agent stops touching its liveness file, so the disarm precedes the agent
  stop, and a test enforces it — but only for the watchdog the platform configured (its
  drop-ins or marked `watchdog.conf`); an owner's own watchdog is left running. It never
  deletes an OS user, and `--purge-access` is refused when no account that can still log in
  (not expired, not nologin) has its own `~/.ssh/authorized_keys` — the one login the
  platform never writes. **Both device scripts share one package-lock wait** (`packageLockShell`: 45 min,
  holder named from `/proc/locks`, never handed to `apt-get` on a timeout), and **neither
  reports success it did not see**: `install.sh` checks `dpkg-query` and the first
  connection, which counts only once the server has accepted it — a frame from the server,
  or 40 s with no refusal, since the server holds a refused stream up to 30 s — and each
  refusal has its own exit status (header of `installScript`); `uninstall.sh` exits 8 while
  a package is still installed. **An `ac_` code is exchanged after the package install**, so
  a failed preflight or lock wait leaves it unused.
  Their tests run the rendered sections under `sh` with stub tools on `PATH`. **Whatever reverse proxy sits in front needs an explicit route for it** or an SPA
  catch-all serves `index.html` into a root shell.
- **`image` pipeline** (`docs/OS-IMAGES.md`) — upload, compress to the deploy format,
  install, capture; quotas are the `IMAGE_*` block in `config.go`. **An upload and a
  compression are mutually exclusive through ONE column**, `compression_status`
  (`image.IsBusy`) — two "is this busy" fields is how a caller checks only one. The
  `uploading` claim is taken BEFORE the first byte is stored (a same-extension re-upload
  writes exactly the key a compression is reading) and released on a **detached context**, a
  multi-GB upload most often ending with the client vanishing. `GET /images/:id/download` is
  on `ActionImageManage`, **not** `ActionImageRead` (read reaches `RoleViewer`, and a capture
  is the whole disk), and its audit row is written at the MINT, a signed URL outliving its
  request. Capture uploads go to top-level **`PUT /capture/:id`**, no JWT and no auth
  middleware, authenticated by an `?exp=&sig=` HMAC capability — **so it needs its own route
  at any reverse proxy**. **The Ubuntu networkd first-boot race has three separate answers in
  the agent** — install-path cure, runtime backstop, liveness withdrawal — and mistaking one
  for another wastes a debugging session; nothing rescues a node already wedged. Read
  `docs/OS-IMAGES.md` → First-boot reliability first.
- **bootloader / EEPROM** (`docs/NETWORK-FIRST.md`) — `nodes.bootloader_version` /
  `_timestamp` have **one writer: the agent**, over authenticated gRPC; the httpboot handler
  **logs the ROM's User-Agent version and never persists it**, that request being bound to the
  device by a public serial alone (the acceptance behind that path is
  `docs/HTTPBOOT-AUTH-RESEARCH.md`, and it rests on the boot failsafe staying OFF —
  `Config.Problems()` warns whenever it is armed). `POST /nodes/:id/provision-eeprom` doubles
  as "update bootloader" and **is the only way to dispatch `update_boot_order`**, the slug
  needing the `pkg_exp`/`pkg_sig` capability minted by the per-serial bake: dispatched bare,
  the agent falls back to a SHARED image and overwrites the device's embedded HTTP_PATH and
  adoption token, a green run that costs the node its ability to netboot; the generic command
  API and the playbook block refuse it, naming the right route. **Both EEPROM commands PIN
  the node at dispatch** (`rebootingSlugs`) — a claim that they MIGHT take it down:
  `update_boot_order` does NOT reboot on the path that has `rpi-eeprom-update` (it answers
  "Reboot required …" and an operator has to ask for one), its recovery-package path DOES,
  and `enable_eeprom` only really reboots a CM4. The pin drops only on an **explicit**
  `"false"` in `RebootingResultKey`: absence means unknown, because the agent cannot be
  trusted about a reboot it has yet to perform. **A pin also records WHICH command imposed
  it** (`nodes.pinned_by_slug`): the UI says "Working" for a pin no reboot was asked for and
  keeps "Restarting" for `reboot`. **The package capability names the NODE, never just the
  serial** (`cdnsign.SignEEPROMPackage`: account + node ride inside `sig` under the HMAC, so
  the agent's URL shape is unchanged) and the signer writes `by-serial/<serial>/<node_id>/`,
  the only directory `ServeEepromPackage` serves — a short serial is shared by unrelated
  devices, so a serial-bound capability can open another account's node credential. Deploy
  the signer before the backend; the ROM never fetches this route. **A node's package is
  rebuilt only when no flash may still be fetching it**: the agent reads the manifest and
  the files as separate requests and no two builds are byte-identical, so `eeprom_provision`
  serializes per node and refuses a `restarting` node BEFORE the bake, and the signer
  publishes by rename, one build per directory. **`DefaultBootToOSOn` needs an OS to point
  at**: a node in recovery keeps inheriting unless its most recent disk write
  (`install_os`/`erase_drive`) is a successful `install_os`.
- **overclock** — a named preset resolved into a managed marker block in
  `/boot/firmware/config.txt` via `patch_file`; `stock` reverts, and a board with no catalog
  returns empty rather than an error. **A reboot is required and the platform never reboots
  by itself** — a property of `patch_file`; `node_command.CausesReboot` is the checkable list.
- **device profiles** (`docs/DEVICE-PROFILES.md`) — declared carrier-board hardware and
  modules, inheritable node→OU→account as ONE block, ladder `declared → applied → confirmed →
  working` (the top rung means the OS is USING the hardware). The registry is embedded YAML in
  `infrastructure/device_profile_registry`, validated in go test AND fatally at boot. The
  apply is its OWN slug, never patch_file+run_script, whose shared supersede key would let a
  queued overclock and a queued profile apply retire each other; the applied hash travels IN
  the dispatch payload and the settle records it verbatim, never a fresh render; the scan is
  ONE fixed script, matched by exact string equality and refused while netbooted. `identify`
  is the sole member of `node_command.nonDisruptiveSlugs`: it moves the node's status not at
  all, so a lost terminal report cannot hold a node in maintenance over a blinking LED.

## gRPC / agent-platform protocol

Contract: `proto/orchestrator.proto` (design: `GRPC_README.md`). One long-lived bidirectional
stream per node (`EstablishStream`): the agent sends `ConnectionInit` (fingerprint + version +
`agent_kind` + `agent_instance_id`, a per-process id that lets the reconnect sweep tell a
stream blip from an agent restart), `Heartbeat` (30 s) and `StatusUpdate`; the platform sends
`Command` with a correlation UUID, and there are no acks. **After editing the proto run
`make proto/generate`** — `agent/` and `backend/` keep separate generated copies, and CI
asserts the copies are identical.

**Two binaries**: `cmd/agent` (`agent_kind=full`) and the telemetry-only `cmd/watcher`, which
links no command-execution code and runs unprivileged. Empty kind = full, so watcher→full
needs a kind-reporting agent or a manual `nodes.agent_kind` reset; full→watcher needs the
watchdog disarmed first. Every command to a watcher is refused (`WatcherNodeError`, 409)
except `apply_access_config_offline`, and the terminal too — so a watcher node is managed
only from recovery, and **getting it there is not an API call** (`reboot-to-recovery`
dispatches `reboot`, which the same gate refuses): flip `boot_to_os` plus a physical
power-cycle. Hence installing Watcher is gated on `node.BootOrderCanReturnToNetboot`
(`WatcherInstallUnsafeError`): a boot order that cannot reach `http` leaves a telemetry feed
with no way home.

**Never call `Stream.Send` on a registered connection — use `StreamConnection.Send`**
(`infrastructure/grpc/stream_writer.go`: one writer goroutine per connection, a bounded
frame buffer, a short enqueue grace, then a failure carrying a reason; a queueable command
stays registered and is redelivered). grpc-go forbids concurrent `SendMsg` and does not
detect it; two writers (dispatch + web terminal) kill the process. Exactly ONE direct
`stream.Send` is legitimate: the master-token identity handover, which runs before the
connection and its writer exist.

**Canonicalise both ids immediately after `uuid.Parse`, before any branch** — it accepts five
spellings, and the registry, the displacement and the stream cap key on the string while
dispatch uses the canonical form, so one `unique_token` otherwise buys unbounded streams past
the cap. Registration DISPLACES the previous connection and must `Stop()` + cancel it by
hand; the monitor's cleanup deletes a registry entry only while it is still the connection
it swept, or it evicts the NEW stream and leaves a live node that `IsNodeConnected` says is
absent, forever.

Post-handshake bounds are per NODE, never per account (`postauth_limits.go`, defaults in
`config.QuotaConfig`): `AGENT_MAX_STREAMS_PER_NODE` refuses the NEW stream with
`ResourceExhausted` — a fleet-wide bound is the one shape that can take a whole account
offline at once — while `AGENT_STREAM_RATE_LIMIT_RPS`/`_BURST` *pace* and never drop inbound
frames, because a dropped `CommandStatusUpdate` leaves a `node_command` in_progress until
the abandonment sweep. Terminal frames are exempt from the pace: they carry PTY bytes to a
browser and touch no database.

**The platform ends a stream only by RETURNING from `EstablishStream`** — `grpc-go`'s
`stream.Recv` observes no context, so cancelling one never unblocks it. The receive runs on
**its own goroutine** feeding a channel the message loop selects on alongside `ctx.Done()`:
without it the pre-auth reaper cancels a context nothing watches, a silent peer parks the
handler forever, its deferred `releasePreAuth` never runs, and a few hundred credential-less
streams exhaust the gate permanently on an internet-facing port. The loop
returns deliberately in exactly two places: a heartbeat whose `UpdateHeartbeat` returns
`ErrNodeNotFound` (key it on the row being GONE, never on the write failing, or a transient
database error drops every agent at once), and a terminal `success` for `release_identity`
(a FAILED release keeps the stream — the device still holds a credential that works).

**Pre-auth streams are bounded at the edge and, where the proxy speaks PROXY protocol v2,
per SOURCE in the backend.** `AGENT_GRPC_PROXY_PROTOCOL=require` makes `peer.FromContext`
the device's address, and `AGENT_MAX_PREAUTH_STREAMS_PER_SOURCE` bounds one address to a
fraction of the global `AGENT_MAX_PREAUTH_STREAMS`. The per-source gate is ARMED ONLY under
`require` — under `off` every peer is the proxy and a per-source key would BE the fleet-wide
refusal; there is deliberately NO `optional` mode, since a header honoured from any peer is
forgeable into a victim site's bucket; and switching it on is TWO-SIDED — backend first, then
the proxy; a rollback is the reverse, since an old backend behind a header-sending proxy
reads the header as the HTTP/2 preface. The proxy's gRPC service must never gain a health
check that dials without the header.

**An oversized frame kills the STREAM, not the command, so bound every payload at the
source.** `maxGRPCMessageBytes` is symmetric in `infrastructure/grpc/server.go` and the
agent's `platform_client.go` — grpc-go's own defaults are asymmetric — but it is only a
backstop for older agents: the real bound is the agent's `handlers.MaxResultOutputBytes` per
output value. The largest legitimate frame is a node-file body of `MaxContentBytes`.

**Agent connect**: the first connect backs off in process, bounded to `initialConnectWindow`,
then exits so systemd re-reads the adoption file and recycles wedged internal state;
returning on the first failure was a 10-second restart loop. `wireCommandHandler` runs
BEFORE the dial, or a node whose first attempts failed comes up with `install_os` device
validation unconfigured. Once connected the process does NOT exit (`StartReconnectionLoop`
retries in a goroutine), so the identity handover must call `AdoptReconnectIdentity`.
Identity comes from the adoption file: `node_id`+`unique_token` XOR `master_token`, plus
`account_id`, `platform_url`, `grpc_endpoint`. **Agent identity is a generation-aware
snapshot**: `AdoptReconnectIdentity` and `Connect` advance `identityGeneration` under `c.mu`;
`reconnect` snapshots (request, URL, generation) under the lock and re-checks right before
presenting, substituting the current identity if superseded — an older reconnect can never
republish the shared enrollment token over adopted node credentials.

**Master-token connect must not hand over an ADMITTED node's identity on serial alone**
(`ConnectWithMasterToken`). A node that is approved, past `AwaitingFirstConnect` and holds a
MAC-bearing fingerprint is matched by the WHOLE fingerprint — exact stored-vs-submitted
serial AND MAC — plus `!IsNodeConnected`; either refusal answers `ErrNodeNotFound`, writes
NOTHING to the row, and records the account-scoped `master_token_identity_mismatch` anomaly.
The excluded set (placeholders, restored tombstones, pending, offline same-fingerprint
reimages) is what keeps WebUSB, tombstone restore and master-SD reimage working; a genuine
board swap is Delete-then-Restore. The account-scoped `GetByHardwareSerial` suffix fallback
carries the same minimum-length floor as the global lookup. **A new anomaly kind is a
cross-module contract**: instance-administration tooling labels kinds by name, so name a
new kind in the pull request.

**`StreamConnection.OUID` always equals the last COMMITTED `nodes.ou_id`, never a caller's
argument**: `Server.refreshConnectionScope` re-reads the row under the connection's own
`scopeMu` (never `Server.mu`, which must not wait on the database) and runs from
`UpdateNodeOU` (via `SetNodeScopeUpdater`) after the assignment's commit AND once more at
registration AFTER the connection is published — so an assignment that commits
mid-registration and two assignments whose updates land out of order both converge on the
row. **A failed read FAILS CLOSED**: the scope narrows to unassigned (account-wide viewers
only — nothing widens), the failure is logged, and the read is retried at
`scopeRefreshRetryDelay`; keeping the previous unit would be exactly the stale audience a
move must stop feeding. Without any of it a moved node keeps feeding its old unit's metrics
viewers until it reconnects.

## API documentation is mandatory

Changing an HTTP handler means updating both (a rule of `docs/CODE-STYLE.md`):

1. **Swagger annotations** on the handler method (`@Summary`, `@Tags`, `@Param`, `@Success`,
   `@Failure`, `@Security`, `@Router`), then `make backend/docs`.
2. **Postman collections** in `postman/` — three (Auth/Users/Accounts, Members/Authz/Roles,
   Nodes) plus the environment.

**The Swagger half is ENFORCED by a test, so it cannot silently drift**:
`backend/pkg/webapp/infrastructure/http_server/routes_documented_test.go` builds the REAL
router (`NewServer` + `RegisterReadiness` + `SetupRoutes` with stub services — nothing is
called, only registered) and fails in both directions: a registered route with no `@Router`,
and a documented route the server does not serve. It reads the **generated**
`backend/docs/swagger.json`, so forgetting `make backend/docs` fails the test — an annotation
nobody generated is not published. It goes through `NewServer` and `RegisterReadiness`
deliberately: `/health` and `/ready` are registered there, not in `SetupRoutes`, and they are
named functions because `swag` cannot read an annotation off a closure.
Deliberately-undocumented routes live in `nonAPIRoutes`, each with its reason inline; adding
to that map is a decision, not a way to silence the test. The conditional route families
(CDN, http-boot, terminal) are forced ON in `routesTestConfig`, so a route cannot drop out
of the check by being configured off. Path and operation counts live in
`backend/docs/swagger.json` — count them there.

**Postman is caught up but NOT enforced.** The items are hand-curated and carry test scripts
a generator would flatten — do not regenerate them wholesale. `PUT /capture/{id}` is
deliberately absent: it is authenticated by an HMAC capability a client cannot mint.

## Frontend architecture (`frontend/`)

Feature-Sliced–style layers (outer may import inner, never the reverse): `routes/` →
`pages/` → `widgets/` → `entities/` → `shared/`. Stack: Vite + React 19 + TS, **TanStack
Router**, **TanStack Query** (server state), **Effector** (client state), React Hook Form +
Yup, Tailwind, i18next. The i18n resources are TYPED, so `tsc` fails on a key that does not
exist in the locale file, and there is exactly ONE locale. **Never publish a completion count
here without an AST-level check** — a text scan cannot see copy in `defaultValue` props, in a
`const` above the component, or in an error fallback. Deliberate non-copy stays in code:
wire constants, strings byte-compared against server data, hardware vocabulary, shell
snippets, and two strings pinned by `workflowColumns.test.ts`. Two traps for new strings: a
literal containing `{{...}}` must travel as an interpolation VALUE, never verbatim into
`en.json` (i18next would interpolate it away), and `graphUtils.REDACTED_PARAM_PLACEHOLDER`
must byte-match the backend's `workflow_template.RedactedParamPlaceholder`. Each domain
concept is an `entities/` slice — `ls frontend/src/entities/` is the list.

- A single shared **`widgets/ouRail`** hosts the OU tree on every list page: filter by `?ou=`,
  drag an entity onto a unit to assign it (node=membership, playbook=scope,
  secret/tag=attachment, SSH key=OU provisioning key, token=adoption OU, OS image=OU default
  install image). The rails are **filter-only**; drops are gated by each page's own condition
  (`alwaysAcceptDrops`, on Nodes the Select mode). **Drag is never the only way to assign a
  node**: the Nodes toolbar's Select mode carries an explicit "Assign to unit" menu, and the
  node sidebar's Overview has a Unit row with Move. Unit management (`useOUManagement`) lives
  on the **Organization** page (`/organization`), whose rail is permanently in edit mode for
  `ou.manage` holders; selecting a row IS the unit's editor (Policies and Contents,
  `?tab=contents`). `/ou` was removed, not redirected.
- The playbook canvas editor is `pages/workflowEditor` (routes `/workflows/new` and
  `/workflows/$templateId/edit`, full-page) on `@xyflow/react`.
- **Runtime config is four keys, not three**: `VITE_API_URL`, `VITE_WEBSOCKET_URL`,
  `VITE_ESSENTIALS_API_ONLY` and `VITE_TERMS_URL`. In dev they come from `.env`; in the
  container the **unprefixed** names are substituted into `public/config.js`.
- The node table is driven by a column model (`entities/node/ui/nodeList/nodeColumns.tsx`):
  container-query priority visibility and user-hidden columns; sorting and paging state live
  in `pages/home`. `/ui-samples` renders all styled components (dev builds only).
- **The axios `baseURL` is `/api` and call paths are `/api/v1/...`, so the browser requests
  `/api/api/v1/...`** and the reverse proxy in front strips one. Curl
  `http://host/api/api/v1/<x>` (expect 401), or the backend directly on `/api/v1/...`; a
  plain `curl /api/v1/...` through the proxy gives a misleading 404.
- **The frontend `build` is `tsc -b && vite build` — tsc FIRST, and that is not a bug to
  fix.** `frontend/src/routeTree.gen.ts` is **committed** and `@tanstack/router-plugin`
  regenerates it during `vite build`, after the typecheck that reads it. Keep `pnpm run
  lint` clean; it lints `src` only. The remaining `react/no-array-index-key` uses carry a
  disable line WITH its reason: the list is positional (a boot order), so the index IS the
  identity.
- **The terminal panel needs a stable callback** — an inline arrow loops the WebSocket
  reconnect.

## Configuration & ports

**`ApplyEnvironment` in `backend/pkg/webapp/application/config/config.go` is the variable
list; `getDefaultConfig()` right above it is the default list. Read them.** YAML loads from
`CONFIG_PATH` and **environment overrides YAML**; credentials are read `<VAR>_FILE` first
(`secretEnv`), and a named file that cannot be READ is treated as unset rather than falling
back to a stale environment copy. `config/config.yaml.example` and `backend/README.md`'s
table are partial and say so; neither carries anything quota-, storage- or
security-relevant.

**Ports can differ between the example config and the one a local run loads, and the
mismatch reads as a broken build.** `config/config.yaml.example` uses HTTP 8080 / gRPC 50051 /
Postgres 5434 (the host port `make db/up` publishes), and 8080/50051 are also the no-YAML
fallbacks — but the default `CONFIG_PATH` is `config_local/config.yaml`, which is
**gitignored**, so `make backend/run` listens on whatever that file says and curling the
example's port can get connection refused.

**`APP_ENV` polarity is inverted on purpose**: only `development|dev|local|test` opt OUT of
fail-closed validation — unset, misspelled or anything else counts as **production** and
rejects insecure defaults (`Config.IsProduction`), including a weak `JWT_SECRET_KEY` or
`SECRETS_ENCRYPTION_KEY`. `Problems()` computes the same list in every environment and is
logged as warnings outside production, so a development install still gets the regression
signal; **do not "fix" that by flipping a plain-HTTP lab install to production** — it would
rightly refuse to boot. **`CDN_SIGNING_KEY` is MANDATORY in production**: unset or blank is a
`Problems()` entry, and one of the secret checks that refuse a development boot too, unless
`ALLOW_INSECURE_DEV_SECRETS=true` (only then does the JWT-derived fallback serve). A deploy script may generate the value (it agrees with nothing outside the host),
but the file must exist before a compose that declares it as a secret starts.

**Two families of guard, and the split is the rule worth knowing.** A ceiling on what a
tenant costs its own operator (`IMAGE_MAX_*`, `NODE_FILE_MAX_*`) defaults to **0 =
unlimited**: that is the operator's call. Anything that decides whether one tenant degrades
everybody else — the per-account API rate limit, the node / seat / secret counts, the
workflow caps, the invite and capture windows, the agent-stream bounds — defaults **ARMED**,
so it is live on every install whether or not its compose mentions it. Read the numbers from
`getDefaultConfig()`; do not quote them here. **Count and byte caps are different questions
and the bytes usually bind first**; refusals are **409, not 429** — the caller's next step is
to remove something, not to wait; **every count cap fails OPEN** on a query error except the
device cap, which refuses (see `entitlement`); and the
node cap is checked at the row-creating branch of master-token adoption, never at the top,
because above that point is an EXISTING node reconnecting and refusing those takes a fleet
offline.
**The per-account API limiter must stay composed INSIDE `jwtMiddleware`** (`router.go`) —
registered with `e.Use` it runs before `JWTAuth`, finds no account, and silently degrades to
one global bucket shared by every tenant, which looks like it works.
**`MEMBER_MAX_INVITES_PER_DAY` is counted off `audit_events`, not off `account_invites`** —
revoking or re-sending DELETES the invite row, so a cap counted on the working table is
defeated by a revoke-and-retry loop.

**Command retention defaults to 0 = keep forever, on purpose.** The cost of forever on
`NODE_COMMAND_RETENTION_DAYS` is real and named: `node_commands`' payload, result and details
are rewrap targets, so unbounded growth slows every future key rotation — an operator who
wants a bound sets it. **The audit trail defaults to 365 days** (`AUDIT_EVENT_RETENTION_DAYS`,
0 = forever), overridden per account by `accounts.audit_retention_days` — written only by
instance administration outside this tree (`AccountRepository.Update` omits it; a
cross-module contract), exposed read-only on the account. Every positive value is clamped by
`audit_event.EffectiveRetentionDays` to `MinRetentionDays`, the floor that keeps the rows
`MEMBER_MAX_INVITES_PER_DAY` counts — a new reader of `audit_events` with a longer window
raises it. The sweep deletes **one account at a time** (`account_id = ? AND created_at < ?`,
install-default accounts and orphaned ids included), never by excluding the overridden
accounts: a `NOT IN` re-reads their whole longer-kept history every tick.

**Step-up (`middleware.RequireRecentAuth`): the membership test is "does this ADD or REPOINT
a way to sign in", not "does it look like a credential form".** `PUT /api/v1/users/me` is a
profile edit whose body carries the **email**, the identifier password reset resolves by;
`POST /auth/oidc/link` attaches an identity that survives a password change, a passkey
rotation, a TOTP enrolment and a full session revocation. **Removal is deliberately
ungated** — unlinking an identity or deleting a passkey only reduces access, and gating it
would protect an attacker's credential exactly when the victim cannot pass a step-up. The
step-up endpoints themselves are never gated. Grep `recentAuth` in `router.go` for the
current set; `step_up_routes_test.go` drives the routes, because Echo does not expose a
route's middleware chain. **The last-credential guard is `domain/user.SignInMethods`:
password + passkeys + LIVE identities, NOT TOTP** — a second factor cannot start a session.
An unlink is a soft delete (`external_identities.revoked_at`); the sites that must exclude a
revoked row carry the reason in `sql/external_identity_repository.go`. **Anything that MINTS
credentials for an already signed-in caller must prove the caller's SESSION is live** —
`services.SessionProof` (family id + `iat` off the access token): postdates
`PasswordChangedAt`, family in the live inventory, fail closed. `JWTAuth` is stateless, so a
revoked session's access token verifies to its TTL; an account switch minted from that
window would produce a FRESH family that survives "sign out everywhere". Mint inside the
caller's family, never a new one.

**Storage**: `STORAGE_TYPE=auto` (the default) picks S3 when **both** `S3_BUCKET` and
`S3_ENDPOINT` are set, else GCP when its bucket+project are, else local. On an
object-storage install an agent release is published to the BUCKET, not to
`storage/agent-artifacts/` (which silently serves a version-older `latest`); anything that
purges an account needs the same bucket credentials; and `/ready` **replaces** its
free-space check with a bucket reachability check. An upload still spills the whole image to
`TMPDIR` first.

**The invite gate is `SERVER_INVITE_CODE`** — nothing in `backend/pkg` reads an `INVITE_CODE`,
so rotating a variable of that name leaves the real gate untouched. `SERVER_MAX_ACCOUNTS` is
the other half; it is enforced in **`provisionNewOwner`**, the single point where an account
is created, so the OIDC path is covered by construction, and it fails OPEN. At the ceiling
`/auth/signup` answers 403 `registration is currently closed`, with no count and no limit in
the message. `SERVER_OPEN_REGISTRATION` (off by default) admits a new organisation with
no code and wins over a configured code; the OIDC handler's `provisionDecision` applies the
same instance gate ahead of `OIDC_AUTO_PROVISION`, and the ceiling still bounds both. An
open password sign-up is written with `accounts.open_signup_pending_since` set, opens no
session, and is usable for nothing until its owner's address is verified: ONE gate,
`middleware.RequireSettledOpenSignup` inside `jwtMiddleware`, asks
`AuthService.RequireSettledAccount`, which settles the account under the ceiling lock on
its first use after verification. Pending accounts are bounded by their own cap, not the
ceiling, and are never deleted here (no complete account purge exists in this tree). Keep
`OIDC_AUTO_PROVISION` off unless you mean it. Runbook:
`docs/GOOGLE-OAUTH-SETUP.md`.

**Terms of use are a FRONTEND setting (`TERMS_URL`, https only), not a capability** — the
sign-up page holds no token to ask with. The product ships no terms text. A sign-up sends
`terms_accepted` and `AuthService.recordSignupTerms` stores it through the user service's
acknowledgement, which writes the `beta_notice.acknowledge` audit row FIRST (the row IS the
consent record) and the `beta_notice_ack_*` columns after; it never refuses or undoes a
sign-up. `POST /users/me/beta-acknowledgement` and `beta_notice_acknowledged` stay for older
clients. The `beta` names on columns, action string, path and field are stored or wire
names — keep them.

## Gotchas (read before debugging / deploying)

- **A refusal the API answers with a 4xx is not an ERROR.** A handler logs a failed call
  through `results.LogFailure`, which picks WARN for a typed 4xx (`OperatorFacingError`, an
  `OperatorRefusal`, the plain sentinels in `results.clientRefusalSentinels`) and ERROR for
  everything else. Sign-in and sign-up lines carry `services.AddressLogRef`, never the
  typed address, and an unknown address and a wrong password log the same line.

- **`SECRETS_ENCRYPTION_KEY` is rotatable.** New key in `SECRETS_ENCRYPTION_KEY`, old in
  `SECRETS_ENCRYPTION_KEY_OLD` (decrypt-only, comma-separated), one boot with
  `SECRETS_ENCRYPTION_REWRAP=true`; clear both only after the log says `secret rewrap
  completed`. **The inventory is `encryptedTargets()` in
  `infrastructure/database/sql/rewrap.go`, each target carrying BOTH its rewrap and its
  coverage check. Add a new encrypted column there, never to a caller**: two lists drift, and
  the coverage check then says "the old key can be dropped" while queued command payloads
  still need it. A rewrap failure is **logged, not fatal** (fatal crash-loops the install);
  undecryptable rows are **skipped and left verbatim**; and `v1:` marks the **format, not the
  key**, so rotation status comes from trial decryption (`EncryptedWithPrimary`). A sample is
  never "clean", so the boot canary keys off `KeyCoverageHasUnreadable`.
- **Provisioning passwords are encrypted at rest.** The `default_password` columns (`nodes`,
  `organizational_units`, `account_policies` keyed on **`account_id`**, `master_tokens`) are
  AES-GCM, marked by **`default_password_enc`** (0 = legacy plaintext) rather than by the
  `v1:` prefix, which a legacy password could start with. The converting backfill is
  non-fatal and runs **outside `Setup()`**, because every `Setup()` is fatal in `main.go`. A
  value no key can read is reported per level: `provision_connection` refuses, `install_os`
  warns.
- **Migrations** are GORM AutoMigrate (each repo's `Setup()` in `cmd/server/main.go`) on
  backend start — no manual migration step.
- **Commands to offline nodes fail fast unless the slug is queueable**
  (`node_command.IsQueueable` — read the map). Redelivery must run **after** the connection is
  registered in `grpc/server.go`: the dispatch path checks `IsNodeConnected`, so an earlier
  reconnect arm fails every command instantly and silently defeats the queue. A queued
  command counts toward `success_count` but has NOT been applied — read `queued_count` too.
  **Three terminal states besides success and failure are deliberately not failures**:
  `superseded` (a newer queueable command for the same slug+node retired it unsent),
  `expired` (`expires_at` passed while the node was away), `skipped` (a precondition refused
  it on arrival — usually an agent below the slug's floor).
- **A command in_progress is settled by THREE sweeps, and the third judges on a clock.** Two
  are triggered by the node going away (reconnect; the unreachable verdict), so a terminal
  report LOST on a node that stays connected is invisible to both.
  `FailAbandonedInProgressCommands` (five-minute ticker in the gRPC monitor) settles anything
  silent for 30 minutes. **The trap for a new slug**: it EXCLUDES `deviceExclusiveSlugs` in
  the query, because `capture_image` and `install_os` legitimately run for a long time without
  a word. A new long-running slug that is not device-exclusive will be failed at 30 minutes —
  add it to that set, or give it progress reports. The exclusion lives only in SQL (removing
  it compiles and passes every unit test) and is pinned by a DSN-gated test. Its OFFLINE-node
  arm does include them, and counts the 30 minutes on the node's own last sighting as well as
  the command's, so a dropped stream cannot fail a quiet capture that is still running.
- **A successful `install_os` FORGETS the applied markers** — device profile, watchdog, LED and
  the whole utility ladder (`services/node/InvalidateAppliedStateAfterReimage` and the gRPC
  `settleReimage`): the hashes describe a disk the image has just replaced. Access is
  deliberately NOT among them: it self-heals through `accessReapplyNeeded`, which compares
  the last apply against the last install, and two mechanisms for one fact drift apart.
  Nothing re-applies on its own — each of these applies is operator-triggered by design.
- **A netbooted node HOLDS the managed-OS-only slugs** (`node_command.RequiresManagedOS` — read
  the map): the recovery bootstrap is a Buildroot initramfs with no `groupadd`/`usermod`, no
  systemd and no persistent root, so applying there could only produce a spurious FAILED. The
  hold triggers on `CurrentBootDevice() == "http"` even when the node is **online**, and the
  dispatch guard is `nodeCmd.Queueable && RequiresManagedOS(slug)` — so
  **`managedOSOnlySlugs` must stay a SUBSET of `queueableSlugs`** or it silently does nothing.
  Release is in `UpdateSystemInfo`, the only moment the boot mode is fresh.
- **Access config self-heals on every managed-OS report, and the trust direction is the
  trap.** `nodes.access_applied_generation` holds the hash the device confirmed; a mismatch
  against the freshly resolved desired hash re-applies. **The device's report is a CLAIM**:
  the settle accepts it only as a MATCH against a hash the platform resolves itself — an
  agent free to choose that column could suppress the apply carrying a revocation. No
  recorded generation routes to `applyAccessOnFirstConnect`, which fires **once** and only
  for a config that actually GRANTS a login (a keyless user is a LOCKED user). The drift
  branch stands down after **3 consecutive FAILED applies for the SAME hash**
  (`accessDriftReapplyExhausted`); recovery is an entry edit or the manual re-apply endpoint.
- **Anything the agent SLEEPS is clamped, and the clamp runs BEFORE the int32 conversion.**
  `MaxDelaySeconds`/`ClampDelaySeconds` (reboot, shutdown) and
  `MaxBlockSeconds`/`ClampBlockSeconds` (`wait_for_file`, `run_script` timeouts), at the
  payload builder and at the proto build; the agent re-bounds them itself, since a command
  can arrive from an older backend. `int32(someFloat)` out of range is
  architecture-dependent — **arm64 saturates to a 68-year sleep, amd64 wraps negative** — and
  the agent sleeps on its SINGLE command goroutine, so an unbounded value parks that node's
  commands and its terminal while it still looks online.
- **Agent version floors live in `domain/node_command.MinAgentVersion` — read the map.**
  `MeetsMinAgentVersion` is fail-closed: empty, `latest` or unparseable counts as too old. A
  node below the `apply_access_config` / `provision_connection` floor receives no
  access-config changes and cannot be re-provisioned; its queued applies settle `skipped`.
  The floor is re-applied at **redelivery** against the version the node came back on.
  `patch_file`'s floor sits deliberately BELOW the fleet minimum — its job is making the
  fail-closed comparison refuse empty versions. The comparison is `agentpkg.Versions`/`Less`.
  Recovery-mode floors are met by the agent baked INTO the boot image, not by what the CDN
  serves.
- **A plain `reboot` does NOT re-fetch the boot image.** The platform withholds it when
  `boot_to_os` says local — netboot validation goes through `reboot-to-recovery`. A
  `*-recovery` hostname is not proof a node will netboot next time: read `boot_to_os`.
- **A master SD provisioning card accumulates one non-expiring credential per device it has
  provisioned — treat the card as a secret.** `S99agent` seeds `nodes/<serial>.txt` from the
  master blob and starts the agent against THAT; a stale entry turns every later device into
  a fingerprint mismatch, which reads as dead hardware; clear it by deleting that file. A
  per-node card (the Re-adopt download) carries no master token.
- **A guard that asks "is this value plausible" can be satisfied by the wrong value.** The
  recovery bootstrap's clock starts at the IMAGE BUILD DATE — right year, weeks behind — so a
  "year is recent" check called it sane and every certificate read "not yet valid". The time
  sync runs every supervisor iteration and accepts only movement FORWARD.
- **A CDN keeps `latest`, the current release and exactly ONE rollback step**: that step is
  the FLOOR, because re-creating a deleted signed version needs the offline signing key. A
  rollback is **not surfaced in the UI by decision** — the one-click Agent-row update is
  pinned to `latest`; a rollback is `update_agent` with an explicit version through the API.
  `orchestrator-watcher` ships with every release but is absent from `/cdn/agent-versions`:
  the listing regex matches `orchestrator-agent_*` only.
- The **eeprom-signer** refuses to boot without a signer token (`SIGNER_TOKEN_FILE`), runs as
  a non-root uid with keys mounted read-only, and belongs on an internal network reachable
  only by the backend: it initiates no outbound traffic, so egress could only be exfiltration
  of the one key that cannot be rotated without re-flashing every device. The token gates
  **`POST /sign` only**: `GET /health`, `/base` and `/bootcode` stay open. `/sign` validates
  `boot_order` (`^0x[0-9a-fA-F]{1,8}$`) and `http_host` before the signer runs, and `sign.sh`
  `set_var` passes values as DATA through a python3 rewrite, never into a `sed` program.
- **SQL statement logging follows `APP_ENV`.** GORM logs every statement *with interpolated
  parameter values* unless `Config.ProductionLogging` (= `IsProduction()`) is on — don't
  paste a development install's raw backend logs anywhere. `DB_LOG_QUERIES=true` re-enables
  it in production only.
- **Never let the distro auto-update the EEPROM** (`rpi-eeprom-update -a` / its service):
  stock firmware drops the embedded pubkey + per-serial HTTP_PATH and breaks signed netboot.
  `provision` masks the service; flashing goes only through signer-baked images.
- **Credentials reach containers as FILES, not environment.** Docker secrets at
  `/run/secrets/*`; every loader reads `<VAR>_FILE` first. A value in a plain environment
  variable beside a `_FILE` one reaches nothing — silent and security-relevant.

## Health, readiness and config validation

- **`GET /health` is LIVENESS and must stay a static literal.** Composes healthcheck it.
  Repointing them at `/ready` turns a database outage or a full disk into a restart loop,
  which is worse than the outage and takes the logs with it.
- **`GET /ready` is READINESS** — public, coarse, cached 5 s: `SELECT 1` plus free bytes AND
  inodes, **replaced** by a bucket reachability probe where images live in object storage.
  The bucket probe tolerates 3 consecutive misses, the database check none
  (`infrastructure/health/health.go`). This is what an external uptime probe should watch.
- **An ABSENCE cannot be reported by the thing that is absent.** A monitor of CI or of a
  periodic job asserts PRESENCE of a signal (a heartbeat withheld on any error), not
  greenness: silence is the alarm, and one signal then covers the job not running, the
  checker breaking and its host dying.
- **`Config.Validate()` refuses every `Problems()` entry in production and the secret subset
  (`secretProblems()`) everywhere else** unless `ALLOW_INSECURE_DEV_SECRETS=true`; the rest of
  the list is logged as warnings outside production, because an on-prem install may hardcode
  `APP_ENV=development`. The weak-secret rule is `weakSecretMarkers` +
  `minProductionSecretLength` in `config.go`: read it, do not quote it. A YAML file may carry
  a PLACEHOLDER secret while the real one arrives from a secret file — env-over-YAML is what
  saves the boot, so preflight a change against the live config rather than reasoning about
  it.
- **Log correlation**: `infrastructure/logging.ContextHandler` makes any `*Context` log call
  carry `request_id`, `account_id` and `user_id`; a plain `logger.Warn` cannot — prefer the
  `*Context` form for anything worth correlating. `middleware.RequestScope` stamps the
  request id before auth and the JWT middleware the tenant after it; `WithScope` merges.
  **Never log a header map**: `logging.RedactHeaderAttr` backstops the base slog handler, and
  a CORS rejection line carries `has_*` booleans, not `req.Header`.
- **Instance-monitoring POLICY does not live in the backend**: the backend detects and
  records anomalies unconditionally, so for a backend-detected anomaly kind "off" can only
  mean *muted* — a real kill switch belongs in backend env config. The backend never reads
  a settings table owned by tooling outside this tree: that tooling stays isolated, with its
  own narrower access, only while the dependency runs one way.

## Audit trail

**`domain/audit_event/actions.go` is the list of what the trail records — read it.**
`NodeCommandAction(slug)` and `MemberStatusAction(status)` generate whole families. **Nothing
writes a literal.**

- **Write through `application/services/audit.Recorder`**, a required constructor argument
  that panics if missing — including at the EDGE (terminal, image export, account export,
  deletion request). Writes are best-effort: the action already happened, and failing a
  member removal because its audit row would not write turns bookkeeping into an outage. The
  **one** exception is the terms acknowledgement, where the row IS the consent record.
- **The actor is an explicit parameter (`audit.Actor`), never read from the context.**
  `logging.Scope` carries the same ids but exists to decorate log lines and must never be the
  thing that fails; a row silently attributed to "system" because a context value was absent
  is worse than a compile error. `audit.SystemActor(accountID)` is the honest way to say the
  platform did it.
- **Every device command is recorded** at one choke point —
  `node_command.Service.RegisterNodeCommandWithWorkflowNode`, which the HTTP handlers, the
  workflow engine, the cron scheduler and the platform's own self-heal all pass through.
  Action is `node_command.<slug>`. No per-caller writer, and no "destructive" subset: any such
  subset drifts from its own definition.
- **`AuditEvent.Detail` must never carry credential material.** Names, kinds, flags and ids
  yes; a secret value, password, private key, token or command payload never. `audit_events`
  is on **no rewrap list**, so anything copied there outlives a `SECRETS_ENCRYPTION_KEY`
  rotation as unreadable plaintext nobody knows to look for. Per-service tests assert the
  absence directly; keep that habit for any new writer.
- **Read side**: `GET /api/v1/audit-events` on `ActionAuditRead`, granted by no role below
  super_admin (`TestAuditReadIsSuperAdminOnly`), served by `audit.Reader` — a separate type
  from the Recorder, so no writing service gains a read capability. The UI is the **Audit tab
  on the account page**; `widgets/auditTrail/describe.ts` is where a stored action string
  meets the product's words, and an action it does not recognise is quoted rather than
  paraphrased. Deletion is reachable from no request handler, so nothing done through the API
  can erase what it did.

## Security properties (do not silently undo)

Each is a break-site rule; the code carries the comment with the reasoning. They are grouped
by subsystem. Where a rule names an older agent behaviour, the floor in `MinAgentVersion` is
what excludes it.

### Sessions, credentials and sign-in

- **The user's row is the credential-state lock** (`lockUserRow`, `SELECT … FOR UPDATE` in
  `infrastructure/database/sql`). Refresh rotation (`RefreshTokenRepository.Rotate`: claim,
  family-whole check, credential-epoch check, successor insert — ONE transaction), every
  session revocation and the OIDC attachment (`CreateWhileSessionLive`) take it; a password
  write is a single `UPDATE` on that row and queues behind it. Without it a rotation that
  claimed before a revocation inserts an unrevoked successor after it, and a link callback
  persists an identity after the session that asked for it died. **Never add a
  Claim-then-Create or a check-then-insert on these tables outside that lock**, and never
  call `Create` for a rotation — `AuthService.RefreshToken` mints the pair and hands the
  record to `Rotate`. **`Rotate` re-reads the token row UNDER the lock** before classifying —
  from a pre-lock snapshot a concurrent double presentation looks like a plain invalid token
  and revokes nothing; and **every token carries a `jti`** — `iat` is whole seconds, so a
  successor minted within its predecessor's second is the same JWT.
- **Every refresh-token insert goes through `RefreshTokenRepository.Issue`**, never `Create`:
  under `lockUserRow` it re-reads `credential_generation` AND `password_changed_at` and refuses
  when either differs from the snapshot the pair was minted from, and if the family already
  has rows it requires `familyState.live()`. `AuthService.generateTokens` is the single
  caller, so every login path and `SwitchAccount` are covered by construction, and a unit test
  asserts no path bypasses it. That is what makes an administrative reset end a login already
  past password verification. **An administrative password reset performed outside this API
  must advance `credential_generation` in the same UPDATE** — that column is the contract.
- **`users.credential_generation` is READ, not only written**: it rides on every token as the
  `cg` claim and in `SessionProof`, and `Rotate`, `CreateWhileSessionLive` and
  `requireLiveSession` compare it with the row — so a token minted before a sign-in EMAIL
  change is refused like one minted before a password change. **Generations start at 1**
  (`user.NewUser`, column default 1, `UserRepository.Setup` lifts 0 rows once), so a 0 on a
  token means exactly "minted before the claim" — with 0 as the start the predicate protected
  nobody until their first transition. Pending-login tickets carry `cg` too, compared through
  the shared `credentialEpochMoved` — one policy across access tokens, refresh, and both
  ticket kinds.
- **No service writes the whole user row.** `UserRepository.Update` exists for
  registration-time shaping and tests only; `SetPassword`, `UpdateProfile`,
  `MarkEmailVerified` (conditional on the stored address) and `AcknowledgeBetaNotice` write
  named columns, advancing `credential_generation` in the same statement where a credential
  moved. A whole-row write from a copy read before a concurrent reset puts the OLD hash and
  `PasswordChangedAt` back — from a route with no step-up; the integration test keeps that
  rollback as its control. **`UpdateProfile` is a true partial write, decided in SQL**:
  `*string` fields, `COALESCE` for the omitted ones, and "did the email change" is
  `IS DISTINCT FROM` against the STORED value inside the statement, the only place that
  clears `email_verified_at` and advances the generation. **A password reset writes only if
  the address is still the grant's** (`SetPasswordIfEmail`, `WHERE id = ? AND email = ?`;
  stored emails are lowercase by construction; 0 rows → `ErrPasswordResetStale`), so a stale
  mailbox cannot take the account in the check-to-write interval.
- **A passkey, a TOTP confirmation, a password change and a profile change persist under the
  session that asked**: `PasskeyRepository.CreateWhileSessionLive`,
  `TwoFactorRepository.ConfirmPendingWhileSessionLive`,
  `UserRepository.SetPasswordWhileSessionLive` and `UpdateProfileWhileSessionLive` ride the
  same `lockLiveSession` boundary as the OIDC attachment (row lock, generation,
  `PasswordChangedAt`, family live — ONE `familyState.live()` shared with `Issue`), the
  handlers pass `GetSessionProofFromContext`, and a missing proof FAILS CLOSED before the
  ceremony ticket is spent. `SetPassword` without a proof remains for the password RESET,
  which is a mailbox grant, not a session. A password change moves `PasswordChangedAt`, so
  the caller's own pre-change tokens stop being live — the invariant, not a bug. **Any new
  route that mints or enables a way to sign in takes the proof to the repository, not only
  through the step-up middleware** — the middleware checks before the handler, persistence
  happens after.
- **A step-up ticket is issued only to a LIVE session, bound to that session's family, and
  the session is re-proven every time the ticket is presented** (`reauth.Service` →
  `SessionVerifier` = `AuthService.RequireLiveSession`, wired by `SetSessionVerifier`; a
  missing verifier FAILS CLOSED). Bound to the user only, a pre-recovery access token and
  ticket could change the recovered password after "sign out everywhere". A dead session is
  a **401** at the guard and at every confirm endpoint (the SPA signs out — a 403 re-auth
  prompt would be answered by a confirmation the same dead session cannot obtain); a missing
  proof stays the 403 `reauth_required`. There is deliberately NO per-user ticket revocation
  in the reauth store: the live check reads the authoritative state. The OIDC step-up
  captures the proof at `/auth/oidc/reauth` and carries it to the public callback.
- **An OIDC LINK is bound to the session that started it, re-proven right before the identity
  is persisted** (`linkTicket.session`, captured at `POST /auth/oidc/link` behind the step-up;
  `completeLink` asks `RequireLiveSession` and refuses on ANY error). The callback is public
  and holds no token, so the proof travels in the ticket. **Every authenticated start route
  that finishes on a public callback must carry the session proof across** — review that
  seam when adding one.
- **An OIDC sign-in never attaches an identity to an EXISTING account on its own**, verified
  email or not (`LoginOIDC` step 2): a password account goes to `link-existing` (password +
  the factor gate), a passwordless one gets `account_exists`; only an authenticated link or
  that password proof links. **The callback's one-time codes (handoff, pending link) are
  bound to the browser** by the httpOnly `oidc_binding` cookie set on the same redirect;
  `/complete` and `/link-existing` refuse without it and spend the code either way. An OIDC
  invite acceptance goes through `member.Service.AcceptInviteRecord` (seat cap, OU, audit);
  unwired, it refuses.
- **An invitation lives only while its issuer is an active account-wide super_admin**: remove,
  disable and demotion revoke it inside the guarded transaction, and `AcceptInviteRecord`
  re-asks. `ReplaceBindingsGuarded` re-checks the WRITER under the lock and honours
  `expected_binding_ids` (409 `member_bindings_stale`).
- **Password paths**: every path that sets a password refuses more than
  `user.MaxPasswordLength` (72 BYTES, bcrypt's limit) with a 400 before any state change — a
  reset before it claims its link. `Login` and `LinkOIDCWithPassword` sit behind a
  per-ADDRESS brake (`login_throttle.go`, keyed by the address as typed so it reveals no
  existence; attempts counted at ADMISSION, a success refunds): per address+client IP, free
  failures then a doubling delay capped at a minute; per address across IPs, past a higher
  ceiling, IPs that already failed share one slot per 10 s. An IP with no failure for the
  address is never refused, so a stranger cannot hold the owner out. A full map evicts the
  least-braked entry, never fails open. Refusals are 429 `login_throttled`. `/auth/signup` asks the
  admission BEFORE looking the email up. A 2FA lockout is 429 `two_factor_locked`.
- **Refresh reuse has a same-client grace** (`refresh_grace.go`, 10 s): a second presentation
  of a just-rotated token by the same User-Agent+IP gets the SAME successor (after
  `RequireLiveSession`), concurrent ones wait for the first; any other replay still revokes
  the family. The handler must call `RefreshTokenFromDevice`, not `RefreshToken`.
- **The account-selection pre-auth token is single-use and dies with a password change**
  (`preAuthTickets`, the same `ticketStore` the 2FA ticket uses; `SelectAccount` refuses
  `IssuedAt` before `PasswordChangedAt` and spends the ticket LAST, right before minting). A
  backend restart between login and selection costs one retry, never a replay. Both pending
  stores record their USER; `RevokeAllSessions` and `RevokeOtherSessions` end the user's
  pending tickets (a login in progress is never the caller's own session), and
  `CompleteTwoFactor` applies the same `IssuedAt`-vs-`PasswordChangedAt` test.
- **TOTP enrolment fails CLOSED on a read error, and its writes are conditional statements**
  (`CreatePending`: `ON CONFLICT … WHERE confirmed_at IS NULL`; `ConfirmPending`:
  `WHERE confirmed_at IS NULL AND secret = ?`) — otherwise a transient read error replaces an
  enabled factor with a fresh pending secret, and two confirmations both return recovery
  codes. A confirmation against a pending secret that a NEWER enrolment replaced is its own
  refusal, `ErrTwoFactorEnrollmentChanged` (409), not "already enabled".
- **The mandatory-2FA policy proves ENROLMENT, and it is safe to gate on only because the
  login path closes the gap** (`two_factor.Policy.UserHasTwoFactor` counts a passkey):
  `auth.Login` challenges a passkey-holder without TOTP whose ACTIVE memberships include a
  `RequireTwoFactor` account; the ticket then expires unspent, because the passkey login is
  an independent, complete login (`LoginResult.TwoFactorMethods`). **The mandatory check must
  not rest on `ListAccounts`**, which skips an account on ANY read error (right for a picker,
  fail-OPEN here — a pool timeout on the mandatory row issues a session);
  `memberOfMandatoryTwoFactorAccount` skips only `ErrAccountNotFound`. `LinkOIDCWithPassword`
  runs the same gate (`passwordNeedsSecondFactor`), refusing a user with an enrolled factor
  with the typed `ErrOIDCLinkNeedsSecondFactor` (403). Outside a mandatory account the
  password alone still signs in on purpose — recovery codes exist only for TOTP, so forcing
  the passkey everywhere would lock out anyone who lost it; inside a mandatory account that
  lockout IS the policy. Fails CLOSED on the passkey count and the policy read. **The mandate
  is for PASSWORD users**: `UserHasTwoFactor` exempts a member whose `HashedPassword` is
  empty — the federated path never challenges (the IdP owns its MFA). Wired by
  `SetPasswordLookup(userRepo)`; a failed read keeps the old answer (blocks), never exempts.
- **WebAuthn user verification is REQUIRED server-side for every ceremony**
  (`passkey.Service`: `AuthenticatorSelection.UserVerification` in the config AND
  `WithUserVerification(required)` on `BeginDiscoverableLogin`, which `BeginLogin` re-checks
  on the session). go-webauthn checks the UV flag ONLY when the session says required; its
  default `preferred` accepts a UP-only assertion from a roaming key with no PIN as both MFA
  login and step-up.

### Device identity, streams and the terminal

- **The device terminal re-asks THREE questions on its 60 s tick**
  (`terminal.Handler.reauthorize`): re-read the node (current unit, deleted node), enforce
  against the CURRENT unit, and `AuthService.RequireLiveSession` — so logout,
  sign-out-everywhere, password change and OU move end a shell within a minute. The access
  token's `exp` is deliberately NOT re-checked (the browser refreshes inside the family). The
  socket is bounded BEFORE auth and per SOURCE: kill-switch (403), a per-address arrival
  limiter (429), a per-address pending cap (`TERMINAL_MAX_PENDING_HANDSHAKES_PER_IP`, 429)
  asked BEFORE the global one (`TERMINAL_MAX_PENDING_HANDSHAKES`, 503) so the 503 stays a
  platform-wide signal — keyed on `c.RealIP()`, which the server's `IPExtractor` makes
  trustworthy (0 = default, never unlimited); then the auth-frame and input-frame read limits
  — an input frame is ONE gRPC message on the node stream and an oversized one kills it.
- **The account WebSocket resolves MEMBERSHIP at authorize, not just the token**
  (`websockets.MemberAuthorizer`, via `SetMemberAuthorizer`) — fail-closed on a lookup error
  and on no authorizer wired, or a removed member reconnects with a still-valid access token.
  **It reserves its slot BEFORE the upgrade** (`pendingUpgrades` under `m.mu`, counted with
  the unauthorized map so the count never dips; released on a failed upgrade) and
  **authorization publishes only the identical pointer still present in the unauthorized map
  and not closed**; removal is terminal (`Client.Closed()` latched before the removal is
  enqueued). Otherwise N simultaneous unauthenticated upgrades all see room, and a client
  removed during a slow membership check is resurrected into the maps.
- **A pushed frame carries no more than the same member's REST read.** An OU-scoped frame
  whose REST route also demands an action declares it (`Message.RequiringAction`, asked with
  a nil target); a payload the REST read redacts per caller is sent as two variants
  (`WithRedactedVariant`, full only to holders of the action). An account-global frame
  declares the action of its REST read (`NewAccountMessage`), never a looser one.
- **A PTY session has ONE teardown** (`ptySession.teardown`, idempotent): the input channel is
  closed under the session mutex that `enqueue` sends under, so input racing a shell exit is
  refused instead of `send on closed channel`. The read pump reports `TerminalClosed` only
  when its own `teardown()` did the work.
- **A panic in a command handler is reported as a FAILED command by
  `CommandHandler.HandleCommand` itself**, the layer that holds the node identity;
  `platform_client.guarded` is only the backstop for terminal and identity frames. Log-only
  recovery is not enough: the abandonment sweep EXCLUDES the device-exclusive slugs, so a
  swallowed panic mid-`install_os` holds the node in maintenance with the exclusive lock
  until a manual restart.

### SSH adoption (same-network installs only)

- **An ADOPTION never takes a host key on first use, and trust is a server-issued ticket, not
  a stored pin.** A reachable preflight returns `host_key_ticket` — HMAC over account, user,
  host, port, vetted dial address, fingerprint and a short expiry, keyed per PROCESS
  (`domain/ssh_host_key_ticket`; a restart invalidates tickets, and the UI re-probes before
  adopting). `POST /nodes/ssh-adopt` requires exactly one of `host_key_ticket` or
  `host_key_fingerprint`; neither, both, an expired ticket or one bound to another
  account/user/host/port is the typed 400 `host_key_binding_required`, and the dial is always
  strict — the adapter refuses a different key at the callback BEFORE authentication.
  `SSH_ADOPT_HOST_KEY_POLICY` governs PREFLIGHT first contact only (`tofu` marks the result
  `first_contact`; `strict` demands an expectation). A changed key is accepted only through
  `accept_new_host_key: true`, audited with the previous and new keys. The UI keeps the
  trusted key in state SEPARATE from the latest probe (`entities/node/sshHostKeyTrust.ts`): a
  failed probe never clears it.
- With an allowlist the name is resolved ONCE and the vetted IP is dialed; output is capped
  per stream; every command is time-bounded, `NewSession` cancellation-aware and the
  forwarded-agent socket deadlined; slots are reserved BEFORE DNS, key loading or the job row
  (global and per-account, for adoption and for preflight, typed 429); host, username and
  password lengths are bounded on the RAW value with surrounding whitespace refused, and a
  fingerprint is accepted only as exactly `SHA256:` + 43 base64 characters (a prefix check
  lets megabytes into the job row and the audit line). Adoption and preflight are audited
  (`node.ssh_preflight`, `node.ssh_adopt.start/complete/fail`; the ticket itself is never
  written).

### Uploads, images and the CDN

- **The streaming-upload body-read idle deadline is lifted per REQUEST after auth, never by
  path** (`middleware.ReleaseBodyReadDeadline`, called by `UploadFile` post-JWT and
  `CaptureUpload` post-`VerifyCaptureUploadCapability`); the release must set the wrapper
  flag, not just the connection, because every read re-arms. `skipStreamingUpload` covers
  the SIZE cap only.
- **An image upload is ADMITTED before a byte of body is read** (`ImageService.BeginUpload`):
  per-account slot, then global slot, then the byte reservation and the remaining-quota read
  under `quotaMu` (a full quota is the typed 409 `image_quota`), then the account-scoped
  lookup, then `ClaimUpload`; every refusal returns what it took, and `defer lease.Release()`
  covers every exit including the body deadline release, which comes AFTER admission.
  `IMAGE_MAX_CONCURRENT_UPLOADS` / `_PER_ACCOUNT` are ARMED defaults (0 = default, never
  unlimited); refusal is the typed 429 `upload_capacity`, because the caller's next step is to
  wait. `RemainingUploadBytes` subtracts other in-flight reservations, or N quota-sized
  uploads all pass. `PUT /capture/:id` takes no slot on purpose, but **a capture RESERVES its
  stream cap in the admission tracker for as long as it streams** (`reserveCapture`) — the
  stored sums see a capture only at the very end, so two overlapping captures otherwise store
  twice the ceiling, durably.
- **Every upload writer takes ONE quota boundary, `Service.quotaMu`** — `admitCapture`,
  `admitUpload` and `commitUploadedBytes`; lock order is `quotaMu` → `admission.mu`, and
  `lease.Release` never takes `quotaMu`. **The price is one process-wide queue**: `quotaMu`
  is held across the quota SUM reads and the row update (never across storage I/O), so a
  stalled pool stalls every tenant's image admission — accepted for one replica; a second
  replica needs a database lease. **Same-extension image replacement reuses the existing object
  key**; changing that needs a storage-layout migration.
- **xz runs under an explicit memory bound** (`IMAGE_XZ_MEMLIMIT_MB`): `-T0` sizes threads
  from PHYSICAL RAM, not the cgroup, and `--memlimit-decompress` is the HARD refusal of a
  crafted stream. **The bound has a FLOOR, `MinImageXZMemLimitMB`**: below it the encoder
  drops to single-threaded and emits a SINGLE-BLOCK archive — the format the platform itself
  classifies as needing a repack (`TestXZDefaultBoundKeepsTheMultiBlockFormat` asks the real
  binary). **The bound is per PROCESS and a transcode is a decoder + encoder pipeline**, so
  **jobs are bounded by a WEIGHTED memory budget** (`services/image/memory_budget.go`,
  `IMAGE_COMPRESSION_MEMORY_BUDGET_MB`): a job takes its whole weight BEFORE it starts — two
  process limits for a transcode, one for a plain compression — never one process at a time,
  or two transcodes each hold a decoder and wait forever for an encoder. The floor is two
  process limits and there is no "unlimited"; the backend reads its own cgroup limit at boot
  (`config.CgroupMemoryLimitMB`) and logs whether the budget fits. Slot counts bound how MANY
  bodies stage, not how MUCH: `IMAGE_MAX_STAGING_BYTES` bounds the sum of in-flight
  reservations, refusing as the same typed 429. **A compression's output is an upload
  writer too** (`admitCompressionOutput` / `commitCompressedBytes`): reserved under
  `quotaMu`, the stream cut at the reservation, re-checked before the swap. Slots are
  granted fairly across accounts (`compression_queue.go`), a running job has a deadline,
  and `Delete` stops a job, queued or running, before it deletes the row.
- **`/cdn` serves every stored object as an INERT attachment unconditionally**:
  `application/octet-stream`, `nosniff`, `Content-Security-Policy: sandbox`, `attachment`
  named after the object or the sanitized `?filename=`, `private, no-store` (a shared cache
  would outlive the signature); object metadata is never consulted.
  It cannot be conditional: older objects still carry the client's MIME, the SPA's
  CSP/nosniff are headers on the frontend container that a reverse proxy may not add on this
  path, and the refresh token sits in `localStorage`. Agent packages are a separate code path.
- **A workflow HTTP block fails on a body read error and names a deliberate truncation**
  (`outputs.truncated`), instead of completing on a partial 2xx. **`integration.http_request`
  rides ONE shared `http.Transport`** with a bounded idle pool and `IdleConnTimeout`
  (`safe_http.go`; the SSRF dial guard lives on it): a transport per block parks its
  keep-alive connection forever, since neither the request context nor `http.Client.Timeout`
  retires an idle keep-alive. `safe_http_pool_test.go` keeps the leaky shape as its control.

### The agent's filesystem and process boundaries

- **The agent's anchor is a symlink-free component WALK from `/`** (`openDirNoSymlinks`:
  `openat(O_PATH|O_DIRECTORY|O_NOFOLLOW)` per component). The resolved path has been through
  `EvalSymlinks`, so a link at ANY depth means the tree changed after the check and the
  operation is refused by name; `O_NOFOLLOW` on a string open guards only the last component.
  Reads, the download rename and the `patch_file` backup go through the anchor. A handler
  that `MkdirAll`s re-resolves afterwards, and the platform `config.txt` path (exempt from
  `resolveForFileOp`) is canonicalised by `canonicalPlatformConfigPath` — remove either and a
  legitimate symlinked ancestor fails at the anchor for the wrong reason.
- **`extractTar` is an anchored component walk** (`walkAnchored`: `mkdirat` for a missing
  component, `openat(O_CREAT|O_EXCL|O_NOFOLLOW)`, `symlinkat`, `linkat` from an anchored
  source; directory entries included). Reading every `EvalSymlinks` error as "the parent does
  not exist yet" is false when an EXISTING ancestor is a symlink pointing outside — a
  deterministic escape, no race. Never reintroduce a string-path `MkdirAll`/`OpenFile` in
  that loop. **The agent keeps ONE root descriptor from creating the destination through
  extraction AND ownership** (`openOrCreateDirNoSymlinks` → `extractTarInto` →
  `applyOwnerRecursiveAt`); a reopen by path in between guards only the last component.
- **Every archive entry and hardlink source, and every ownership target, passes the
  protected-name policy** (`checkProtectedNames`) before anything is unlinked or created —
  `/etc` is not a protected name, `/etc/shadow` inside an archive aimed at it is. The policy
  refuses a protected PREFIX's own directory node too (`/etc/ssh/authorized_keys.d` chowned to
  an archive's owner makes sshd's StrictModes refuse every platform key), and lists
  `/etc/gshadow` and `/etc/sudoers`. **The owner walk is anchored** (`chownTreeAnchored`: each
  directory opened once with `O_NOFOLLOW`, entries chowned with
  `fchownat(AT_SYMLINK_NOFOLLOW)`, a planted link to a directory never descended), **never
  chowns a directory that governs a protected name** (`governsProtectedPath`, derived from the
  protected lists — never a second literal list — and decided BEFORE the directory's own
  `Fchown`), and **skips, with a WARN, any multi-link file it did not create and any mount
  root** (`statx STATX_ATTR_MOUNT_ROOT`, `st_dev` fallback): a hardlink to `/etc/shadow`
  inside an archive is otherwise an unchecked alias.
- **The anchored staging descriptor is `O_RDWR`** (`createTemp`): `download_file` writes,
  hashes, rewinds and EXTRACTS from the one descriptor; an `O_WRONLY` variant fails every
  `extract=true` download with `EBADF` after the whole archive was fetched
  (`download_extract_linux_test.go` goes through the handler).
- **A download reads cap+1 and fails before publication** on the extra byte
  (`copyDownloadBounded`; an advertised `Content-Length` past the cap is refused before
  reading); a `LimitReader` exhausting silently is a truncated file installed as complete.
  **Extraction finalizes the decoder** (`archiveDecoder.Finish`: a bounded drain after the
  tar end marker, then the decoder's own completion — `xzExecReader` waits for the exit
  status explicitly); the image install loop calls `imageStream.Finish()` at EOF.
- **Device decoding is bounded** (`decoder_bounds.go`): xz-utils with `--memlimit-decompress`
  when present, zstd with a bounded window and memory; the pure-Go xz fallback (recovery
  initramfs only) sits behind a FIRST-block dictionary tripwire — ulikunitz/xz raises its cap
  to whatever each block declares, so that check is not a ceiling. **The xz-utils path must
  ride `xzExecReader`**, never a bare stdout pipe: a refused stream then reads as a clean EOF
  and an empty or truncated archive reports as SUCCESS. **`runWithEnv` is bounded** (combined
  buffer, `WaitDelay`, `Setpgid` + group kill): a grandchild holding the pipe otherwise parks
  the inbound command loop past the timeout. The process-group test records the child PID
  before `exec`.
- **Every one-shot HTTP client in the agent rides `newOneShotTransport`** (`DisableKeepAlives`,
  `IdleConnTimeout`, `MaxIdleConns` 1): a transport discarded after its response parks that
  response's connection forever, and a device that is not rebooted for weeks cannot afford
  one leaked socket per download, signature fetch or EEPROM manifest read. Do not "optimise"
  keep-alive back on for a client that is dropped after one call.
- **Agent logs never carry URL credentials**: `redact.URL` drops userinfo as well as query and
  fragment; `redact.Error` rebuilds a `*url.Error` around the redacted URL with the chain
  intact; both binaries' slog handlers run `redact.SlogAttr` as the call-site-independent
  backstop.

## Board facts worth not re-deriving

`[cm4]` in `config.txt` matches a Compute Module 4 **only** — a Pi 4 B is `[pi4]`, which also
covers CM4/CM4S/Pi 400 — and there is no `[cm5l]` filter at all. The recovery bootstrap's
forced `device_tree=` makes every BCM2712 board call itself a CM5 in `/proc/cpuinfo`, so the
OTP **revision code** is the only identity that survives; the agent reports it raw and
`domain/node.ParseBoardRevision` decodes it. What is supported, and how well, is
`docs/SUPPORTED-HARDWARE.md`.
