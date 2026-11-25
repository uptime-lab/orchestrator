# Security acceptances for a self-hosted install

Status: decision record. Every limitation below is known, is in the code on
purpose, and was first accepted before the source was public. Anyone can now
read this code and run it, with their own accounts and their own network, so
each entry is argued again for that world, and either fixed or accepted on
grounds an operator can check.

`SECURITY.md` states the trust model this document assumes: whoever operates
an install, and whoever holds its secrets, has root on every adopted device.
Nothing below is about that operator. It is about what someone who is NOT an
authorised operator can do, or what a member with a narrow role can reach.

Every entry has the same parts:

- **Mechanism**: what the code does, in one paragraph.
- **Original premise**: why it was accepted.
- **Without that premise**: what changes when the source is public and the
  install is someone else's.
- **Attacker**: what they gain and what they need.
- **Control now**: the variable, default or code path that holds today.
- **Fixed / still accepted**: the split.
- **Acceptance**: one sentence to agree or disagree with.

Code is cited by symbol, not by line. Variables are documented in
`docs/CONFIGURATION.md`.

Contents:

1. The per-serial HTTP boot path and the boot-loop failsafe
2. Serial squatting and the short-serial bake
3. The kiosk utility's mutable image tag
4. Development default secrets (closed)
5. The agent channel's edge
6. Operator residuals: the Tailscale auth key, single-replica state, the xz
   memory budget on a small host, an unclaimed install's sign-up state
7. Summary

---

## 1. The per-serial HTTP boot path and the boot-loop failsafe

**Mechanism.** A Raspberry Pi bootloader fetches `boot.img` and `boot.sig`
over plain HTTP from `GET /http-boot/:serial/:file`
(`httpboot.Handler.ServeBootFile`). The ROM can send nothing but a literal
path, so the only identity in the request is the hardware serial, which is
printed on the board. The serial decides whether the recovery image is
served or withheld (the node's effective `boot_to_os`), and every withheld
`boot.sig` request feeds the boot-loop failsafe counter
(`NodeService.RegisterNetbootWithhold`). Past
`PROVISIONING_BOOT_FAILSAFE_THRESHOLD` consecutive withholds the endpoint
serves recovery anyway, so a node whose installed OS cannot boot becomes
reachable. The image itself is generic and integrity-protected: the ROM
verifies `boot.sig` against the public key baked into its EEPROM, so nobody
can substitute an image. The full analysis is
`docs/HTTPBOOT-AUTH-RESEARCH.md`.

**Original premise.** The acceptance was always an install-level rule, not a
secret: an install whose boot host is reachable from the internet, as a
hosted install's is, runs the failsafe off. The closed source added little,
because a device's own EEPROM configuration shows the path shape.

**Without that premise.** Every self-hosted install is now a candidate
internet-facing boot host, and its operator chooses the threshold. The
community compose binds the backend to the loopback interface only; it is
the operator's reverse proxy that serves `/http-boot` on plain port 80
(`docs/INSTALL.md`, HTTP routes), and every install that netboots devices
does so, so the rule applies to it. Public source also gives an attacker the exact
window, threshold and liveness guard.

**Attacker.** With the failsafe **off**, someone who knows a serial and the
install's boot host learns one bit per request: whether that node is set to
netboot (a policy oracle). With the failsafe **armed**, they can also spend
the counter for a node that has been offline for more than five minutes
(`bootFailsafeLiveness`; a heartbeating node is never counted) and divert its
next boot into the recovery image instead of its OS. They need the serial,
network reach to the boot host on port 80, and a node that is already down.
They gain no code execution and no credential: recovery is the platform's own
signed image, and the node shows up in recovery for its operator.

**Control now.**

- `PROVISIONING_BOOT_FAILSAFE_THRESHOLD` defaults to `0` (off) in
  `getDefaultConfig()` and in `docker-compose.community.yml`.
  `TestCommunityComposeKeepsTheBootFailsafeOff`
  (`backend/pkg/webapp/application/config/community_compose_test.go`) reads
  the compose file and fails if that default moves, if
  `PROVISIONING_HTTP_BOOT_FAIL_OPEN` stops defaulting to `false`, or if the
  default would produce the advisory below.
- `Config.Advisories()` logs a warning at every start, in every mode,
  whenever the threshold is above 0. It is deliberately not a boot refusal:
  a LAN install arms it on purpose.
- A node can carry its own threshold (`nodes.boot_failsafe_threshold`, read
  through `node.EffectiveBootFailsafeThreshold`), which wins over the
  variable in both directions. No API writes it; it is set only with `psql`,
  and the advisory above does not see it.
- `PROVISIONING_HTTP_BOOT_FAIL_OPEN=false` (default): an unknown serial gets
  404, never the bootstrap.
- The route carries a per-address rate limit and an unknown-serial ban
  (`netbootGuard`); the short-serial suffix fallback needs at least eight
  characters and strikes on a hit, so probing it is not free.

**Fixed / still accepted.** The default was already off; it is now pinned by
a test, and the compose comment says when and how to arm it. Still accepted:
the boot path is unauthenticated, and the oracle bit is readable. The design
that closes both, a per-device secret in the boot path, is
`docs/HTTPBOOT-AUTH-RESEARCH.md` §5 Phase 2 and is not built. The price of
running the failsafe off is real: a node that cannot boot its OS is rescued
by hand (set `boot_to_os`, or Reboot to recovery), and the watchdog API
reports `recovery_limited` for an armed watchdog on such a node.

**Acceptance.** An install that strangers can reach on port 80 keeps the
boot-loop failsafe off and accepts that a node with a broken OS is recovered
by an operator; an install that strangers cannot reach may arm it.

---

## 2. Serial squatting and the short-serial bake

**Mechanism.** A hardware serial binds to at most one account, first writer
wins (`guardSerialBinding`, `app_errors.ErrHardwareSerialConflict`). No path
proves possession of the device at the FIRST bind: every per-device secret is
minted by the binding itself, so a member who may create nodes can bind the
serial of a device they do not hold. Serials shorter than sixteen
characters, the eight-character form a WebUSB flasher reads, are not guarded
the same way, because unrelated devices legitimately share them, so two
accounts can both bake a signed per-serial bootloader package for the same
short serial. Each baked package carries one node's adoption token, is
stored per node, and is downloaded with a short-lived capability bound to
that node and its account.

**Original premise.** It takes two accounts on one install: an install with
a single account has nobody to bind a serial but that account's own admins.

**Without that premise.** Public source makes the recipe readable, but it
does not open anyone's sign-up. What matters is how many accounts an install
has and who may create them. A self-hosted install from the community compose
has ONE: `SERVER_MAX_ACCOUNTS=1`, and an empty invite code means registration
is closed once the first owner exists (`auth.admitSignup`). On such an
install the only people who can bind a serial are that account's own
admins, who can adopt the device anyway. An install that hosts several
organisations, by invite code or with open registration
(`SERVER_OPEN_REGISTRATION=true`), has accounts that do not know each other;
the controls below are what bounds the matter there.

**Attacker.** On a multi-account install, an admin of account A can bind the
serial of a device that account B has not adopted yet, or of a node B has
deleted, so B's adoption is refused. They need an account on the install with
node-create rights and the victim's serial. They gain denial of adoption, and
through the bake a device that netboots carrying A's adoption token. They
cannot take over a device B has already adopted: the guard protects the
incumbent. They cannot read B's package either: the download capability
opens only the package of the node it was minted for. That binding to the
node and the account is in place.

**Control now.**

- `SERVER_MAX_ACCOUNTS` (community compose `1`) and the closed-by-default
  invite gate, decided inside one locked transaction
  (`AccountRepository.CreateAdmitted`).
- A never-admitted binding is a lease: `NODE_SERIAL_LEASE_DAYS` (default 30)
  clears the fingerprint of a placeholder or pending node with a full serial
  and no sign of life, so holding a squat needs a live device-shaped client.
- Typed refusals instead of silent failure: the bind refusal is recorded and
  shown to the refused account (`GET /api/v1/nodes/adoption-refusals`), and a
  restore whose serial is now bound elsewhere answers 409
  `serial_bound_elsewhere`.
- A serial lookup that would resolve across accounts refuses rather than
  guesses, and the suffix fallback needs at least eight characters.
- The guard covers the eight-character low word in both directions: a full
  serial is refused when another account holds a full serial or a short
  stamp with the same low word, and a short stamp when another account holds
  a full serial ending in it. An EEPROM readback, which the node's own agent
  reports, counts only when it names that node's own serial or low word, and
  never decides a boot against another account's admitted node that reports
  none.
- A baked package is stored per NODE (`by-serial/<serial>/<node_id>/`), and
  its download capability names the account and the node it was minted for
  (`cdnsign.SignEEPROMPackage`); `httpboot.Handler.ServeEepromPackage` serves
  only that node's package and answers every other capability with the same
  404. This, not the serial guard, is what keeps one account's capability
  from opening another account's credential, so a short serial may still be
  baked by more than one account.

**Fixed / still accepted.** Fixed: the package download capability is bound
to the node and the account it was minted for. Still accepted, held out of
reach by the single-account default: no proof of possession for a first
bind, and the unguarded short serial, whose two bakes coexist side by side.
A device netboots with whichever package was flashed into it, so the
squatter's gain stays a device that carries the squatter's adoption token.

On a multi-account install, what holds is the typed refusal shown to the
refused account, the 409 on a restore whose serial is bound elsewhere, the
serial lease, the node-bound package capability, and the eight-character
floor on the short-serial lookup, which refuses rather than guesses across
accounts. What remains accepted there is that one account can bind an
unadopted serial first, which blocks the other account's adoption until the
lease clears it or the operator does. A device already adopted cannot be
taken, and no credential crosses accounts.

**Acceptance.** The first account to bind an unadopted serial holds it. On
an install with several organisations the refused account sees why, and the
binding lasts until the serial lease or the operator clears it.

---

## 3. The kiosk utility's mutable image tag

**Mechanism.** The kiosk utility runs a container image chosen from an
enumeration in the embedded registry
(`backend/pkg/webapp/infrastructure/utility_registry/registry/utilities/kiosk.yaml`).
The entries are tags, not digests, so the device runs whatever the tag points
at when it pulls.

**Original premise.** The image repository has one owner, the same as this
repository's. Retagging it presupposes owning the platform's own release
path, so a digest pin would buy nothing against that owner and would cost a
registry edit and a server release per image release.

**Without that premise.** Publication does not change who can push the
image. It does change who trusts that owner: a self-hosted operator now
trusts the maintainers' registry for this image exactly as they trust it for
the server images themselves, which the community compose pulls by
`PLATFORM_VERSION` tag.

**Attacker.** Someone who controls the image repository can put code on
every kiosk node at its next pull. They need the maintainers' registry
credentials. Whoever has those can already publish a server image.

**Control now.** Only enumerated images are accepted, at save
(`Definition.ValidateConfig`) and again at every apply, so a member cannot point a node at an arbitrary
image. The image is pulled anonymously; there is no credential on the device
to steal.

**Fixed / still accepted.** Accepted unchanged. The acceptance rests on
single ownership of the image repository; a second publisher or a shared
organisation reopens it, and the remedy then is a `tag@sha256:` pin together
with a migration of stored assignment values, since a stored value outside
the enumeration fails every apply.

**Acceptance.** A self-hosted operator who trusts the maintainers' server
images by tag also trusts the kiosk image by tag.

---

## 4. Development default secrets (closed)

**Mechanism.** The repository carries placeholder values for the JWT secret,
the secrets encryption key and the CDN signing key, in
`backend/.env.example` and in example configuration.

**Original premise.** A development mode (`APP_ENV=development`) logged weak
secrets and booted anyway, and a development install was assumed to be
private.

**Without that premise.** A placeholder in a public repository is a published
key. A server booted on it signs tokens anyone can forge and encrypts values
anyone can read.

**Control now. This one is closed.** `Config.Validate()` refuses the three
secret checks (`Config.secretProblems`: a weak, placeholder or short
`JWT_SECRET_KEY` or `SECRETS_ENCRYPTION_KEY`, and an unset or weak
`CDN_SIGNING_KEY`) in EVERY mode, not only production. A development mode
boots on such values only with `ALLOW_INSECURE_DEV_SECRETS=true`, which
production ignores and which is logged either way (`Config.Advisories()`).
The community compose runs in production mode and takes every credential
from files that `server --init-secrets` generates (`docs/INSTALL.md`).

**Fixed / still accepted.** Fixed in the code before this document. Nothing
is accepted.

**Acceptance.** None needed: a published secret cannot start a server unless
its operator says so in a development mode.

---

## 5. The agent channel's edge

**Mechanism.** Devices hold one long-lived gRPC stream to the server's agent
port. Every device credential, command and terminal byte crosses it. The
server refuses to start in production unless the shape of that channel is
DECLARED (`Config.Problems`): TLS at the server
(`GRPC_TLS_CERT_FILE` / `GRPC_TLS_KEY_FILE`), TLS terminated by a proxy in
front (`GRPC_TLS_TERMINATED_EXTERNALLY=true`), or plaintext on a trusted LAN
(`AGENT_GRPC_PLAINTEXT_OK=true`, which is logged as an advisory at every
start). Separately, `AGENT_GRPC_PROXY_PROTOCOL` says whether the proxy sends
a PROXY protocol header, which is what lets the server see each device's
real address.

**Original premise.** An install whose operator also runs the proxy can
have it terminate TLS and send PROXY protocol v2 (`require`), so the
per-source bound on unauthenticated streams sees real addresses.

**Without that premise.** A stranger's proxy may not send the header, and
`require` against a proxy that does not would refuse every device. The
community compose therefore declares `GRPC_TLS_TERMINATED_EXTERNALLY=true`
and defaults `AGENT_GRPC_PROXY_PROTOCOL` to `off`. Two things follow. The TLS
declaration is a statement the server cannot verify. And with `off` every
connection appears to come from the proxy, so the per-source bound
(`AGENT_MAX_PREAUTH_STREAMS_PER_SOURCE`) is inert; it is armed only under
`require`, because under `off` a per-source key would be one bucket for the
whole fleet.

**Attacker.** Against `off`: one source can hold unauthenticated streams up
to the global bound, `AGENT_MAX_PREAUTH_STREAMS` (default 1024), delaying
new device connections. Established streams are untouched, and no stream does
anything before it authenticates. Against a false TLS declaration: an
on-path observer reads device credentials and commands. That needs the
operator to forward the agent port without TLS; the compose publishes it on
loopback only, so it takes a proxy configured that way.

**Control now.** The declaration requirement in `Config.Problems()`; an
unrecognised `AGENT_GRPC_PROXY_PROTOCOL` value is refused, never read as
`off`; the global pre-auth bound and the per-connection stream limit stay
armed in every mode; the server ports bind to loopback in the community
compose. `docs/INSTALL.md` tells the operator to check the agent port's
certificate from a device and to switch to `require` when the proxy can send
the header.

**Fixed / still accepted.** Nothing new is fixed here; the declaration was
added before this document. Still accepted: under `off`, one source can use
the global pre-auth budget; the arrival rate of new connections is not
bounded per source; the TLS declaration is trusted as stated.

**Acceptance.** An install whose proxy cannot send PROXY protocol accepts
that a single source can delay new device connections up to the global
bound, and an operator who declares TLS terminated externally is responsible
for that being true.

---

## 6. Operator residuals

These do not need an outsider. They are about what a member with a narrow
role can read, and about limits that only show on a self-hosted install.

### 6.1 The Tailscale auth key is readable by node viewers

**Mechanism.** The Tailscale utility takes a pre-auth key as a parameter.
Utility parameters are readable at `utility.read`, which the viewer role
holds, through the assignment list and a node's effective utilities; a
literal key also rides the apply command's payload, which `node.read` can
list. A key entered as a reference to a variable, `{{secrets.<name>}}`, is
different: the reads return the reference, the value is readable only at
`secret.read_value` (admin), and the apply marks the resolved payload for
redaction (`Service.resolveParamRefs` sets `ParamsSecretDerived`).

**Original premise.** On an invitation-only install a viewer is a
colleague the account owner invited.

**Without that premise.** Unchanged in kind: a self-hosted install's viewers
are also people its owner invited. Public source only makes the read path
obvious.

**Attacker.** A viewer copies a pre-auth key and joins their own machine to
the operator's tailnet until the key expires or is used up. They need the
viewer role on an account that pasted a reusable key.

**Control now.** The utility's description tells the operator to use a
single-use or short-lived key, and the key's description now tells them to
store it as a variable and enter the reference instead. A single-use key
has already been spent by the device's first login.

**Fixed / still accepted.** Fixed: the advice now sits where the key is
entered. Still accepted: a LITERAL key stays readable by viewers. Hiding it
needs a sensitive-parameter flag in the registry, redaction on every read
path and a merge rule so a full-document update does not blank the stored
key; that is a feature, not a one-line authorisation fix.

**Acceptance.** An operator who pastes a reusable Tailscale key as a literal
accepts that every viewer of that scope can read it; a reference to a
variable, or a single-use key, avoids it.

### 6.2 Single-replica state

**Mechanism.** Several bounds and stores live in the backend process: the
image upload admission and quota boundary (`image.Service.quotaMu` and its
admission tracker, including the capture reservation), the pending sign-in
tickets for two-factor and account selection (`auth` ticket store), the
step-up tickets (`reauth` ticket store), the last-credential lock
(`credlock`), the SSH-adopt host-key ticket key, the per-account API rate
limiter and the terminal admission limits, the gRPC stream registry, the
workflow worker pool and the node monitor. Credential-state transitions,
the device cap and the account ceiling are decided in Postgres under row or
advisory locks and are not in this list.

**Original premise.** One backend process per install is the supported
deployment.

**Without that premise.** A self-hoster may try to scale the backend. With
two replicas behind one proxy: every quota and admission bound becomes per
replica, so the install admits up to twice what it says; the last-credential
lock no longer serialises, so two concurrent removals on different replicas
can lock a user out of their own account; a ticket issued by one replica is
refused by the other (a retry, not a bypass); a device's stream lives on one
replica, so commands sent through the other fail as "not connected"; and both
replicas run the monitor and the workflow workers.

**Attacker.** None from outside. A member gains at most the doubled quota.
The cost is correctness and availability, borne by the operator who scaled.

**Control now.** The community compose runs one backend and names its
container (`container_name: orchestrator-backend`), so `--scale backend=2` cannot
start a second container under that name. `docs/INSTALL.md` lists "one replica" among the
known limitations.

**Fixed / still accepted.** Accepted. A second replica needs database leases
for these stores and a stream-aware dispatch, which is design work.

**Acceptance.** An install runs exactly one backend process, and one that
runs two loses the quota bounds and the last-credential guard without being
told.

### 6.3 The xz memory budget on a small host

**Mechanism.** Uploaded OS images are compressed and transcoded with `xz`.
`xz -T0` sizes its threads from the host's physical memory, not from the
container's limit, so the backend bounds each process
(`IMAGE_XZ_MEMLIMIT_MB`, never below the multi-block floor of 192 MiB, where
xz falls back to a single-block format), the number of jobs
(`IMAGE_MAX_CONCURRENT_COMPRESSIONS`), and the sum of process limits the
running jobs may hold (`IMAGE_COMPRESSION_MEMORY_BUDGET_MB`). A transcode is
a decoder and an encoder, two processes, so it weighs two limits and takes
both before it starts. `--memlimit-decompress` refuses a crafted stream
instead of decoding it. At boot the server reads its own cgroup limit and
warns when budget + 128 MiB does not fit.

**Original premise.** The numbers were chosen for one known container and
host.

**Without that premise.** A self-hosted install may run on a 4 to 8 GB
single-board computer next to Postgres. The question is no longer one host's
arithmetic but whether the defaults a stranger starts with fit.

**Attacker.** A member who may upload images (a super-admin role) can send a
stream whose dictionary exceeds the bound; xz refuses it rather than
expanding it. Several large uploads queue behind the job count and the
budget; they slow each other, they do not exhaust memory.

**Control now.** The community compose sets every term:
`IMAGE_XZ_MEMLIMIT_MB=256`, `IMAGE_COMPRESSION_MEMORY_BUDGET_MB=512` (exactly
one transcode), `IMAGE_MAX_CONCURRENT_COMPRESSIONS=1`, inside
`BACKEND_MEMORY_LIMIT=1G`, which is what the declared 4 GB host is sized for.
`TestCommunityComposeCompressionBudgetFitsTheContainer` (beside the failsafe
test) fails if the xz bound drops below the floor, if the budget would be
clamped, or if budget + 128 MiB exceeds the container limit. For an 8 GB
host: `IMAGE_XZ_MEMLIMIT_MB=512`, `IMAGE_COMPRESSION_MEMORY_BUDGET_MB=1024`,
`BACKEND_MEMORY_LIMIT=2G`, one job at a time; the boot log line "compression
memory budget fits the container" confirms the arithmetic.

**Fixed / still accepted.** Fixed: the budget was unset in the community
compose (its built-in floor happened to fit) and is now stated beside the
other two terms, with the arithmetic in a comment and a test on the defaults.
Still accepted: the bound is per install, so on a multi-account install one
account's compressions can delay another's.

**Acceptance.** A small host compresses one image at a time inside a bounded
budget and is slower for it, rather than risking the backend being killed
for memory.

### 6.4 An unclaimed install says so

**Mechanism.** `GET /api/v1/server/sign-in-options` is tokenless, so the
sign-up page can word itself before anyone signs in. Its `registration`
field reports how a sign-up is admitted right now; on an install with no
account yet it answers `first_owner` — the first sign-up needs no invite
code and becomes the owner (`auth.admitSignup`).

**Original premise.** The installs that existed were claimed before they
were reachable, so the answer was never `first_owner` to a stranger.

**Without that premise.** A fresh self-hosted install reachable before its
operator signs up tells anyone who asks that it is unclaimed.

**Attacker.** Someone who reaches the install in that window can register
first and become its owner, which under the trust model above means root on
every device the install later adopts. The tokenless answer adds a
non-destructive way to find unclaimed installs across many addresses
without registering on any of them. A new install's name usually becomes
public when its reverse proxy obtains a certificate, so the window can be
found.

**Control now.** The install order. `docs/INSTALL.md` tells the operator to
create the first owner right after the first boot, before the port is
reachable from anywhere they do not trust. The answer becomes
`open`, `invite_code` or `closed` once the first account exists, and the admission
is decided under the account-ceiling lock, so two first sign-ups cannot both
become the owner. The one-command installer (`tools/install/install.sh`)
keeps that order itself: it starts the stack without its proxy, creates the
owner through the backend's loopback port, and starts the proxy — and so
requests the certificate — only after that, so an install it makes is never
reachable while it answers `first_owner`.

**Fixed / still accepted.** Fixed for installs the installer makes. Still
accepted: the tokenless `first_owner` answer, and the claim race on an
install set up by hand and exposed before its owner registers. The stronger design is a one-time setup token that the install
prints at first boot and requires for the first sign-up; it is planned, not
built.

**Acceptance.** An install exposed before its operator registers can be
found and claimed by a stranger; the operator registers the owner before
exposing the port.

---

## 7. Summary

| Risk | Outcome | What holds it |
|---|---|---|
| Per-serial HTTP boot path, failsafe | accepted, default pinned | failsafe off by default, test, start-up advisory |
| Serial squatting, short-serial bake | first bind wins; bounded on an install with several organisations | one account by default, closed sign-up, serial lease, node-bound package capability |
| Kiosk mutable tag | accepted | single owner of the image repository |
| Development default secrets | closed | secret checks refuse in every mode |
| Agent channel edge | accepted | declared channel shape, global pre-auth bound |
| Tailscale key readable by viewers | advice added, literal accepted | variable reference, single-use key |
| Single-replica state | accepted | one named backend container |
| xz budget on a small host | fixed in the community compose | stated budget, test |
| Unclaimed install reports `first_owner` | accepted | install order: register the owner before exposing the port; a setup token is planned |
