# Uptime Orchestrator

Network boot and fleet management for Raspberry Pi boards, over the
internet. ("Orchestrator" in the code, the API paths and this repository.)

A board's own bootloader fetches a signed recovery system from the server
over HTTP. It needs no PXE or TFTP server, and the board's drive can be
empty. From recovery the platform
installs an operating system onto the board. From then on it manages who
logs in, what software runs, which secrets the board holds and what it
does on a schedule. When the board's system no longer boots, you set its boot target to
Recovery in the browser and it comes back from the network, with no card
reader and no trip to the rack.

- Hosted service: [app.orchestrator.sh](https://app.orchestrator.sh)
- Website: [orchestrator.sh](https://orchestrator.sh)
- Documentation: [orchestrator.sh/docs](https://orchestrator.sh/docs/)
- Status: public beta. What has and has not been exercised is listed at
  the top of [`docs/INSTALL.md`](docs/INSTALL.md); changes are in
  [`CHANGELOG.md`](CHANGELOG.md).

The hosted service and a self-hosted server run the same code from this
repository.

![The Nodes page: boards with their state, system and agent version](https://orchestrator.sh/docs/img/interface-tour/01-nodes-page.jpg)

A board with a wiped drive coming back into recovery, recorded:
[clip](https://orchestrator.sh/docs/img/how-it-works/v1-broken-os-to-recovery.mp4).

## Who it is for

Anyone who runs Raspberry Pi 4, Pi 5, Compute Module 4 or Compute Module 5
boards and wants to install, reach and repair them without touching them:
one board on a home network or a fleet spread over several sites. A board
needs wired Ethernet (Wi-Fi is not used), a DHCP address and a route to the
server. It connects out, so it can sit behind NAT.

In this file a *board* is the hardware and a *node* is its record on the
server. The limits count *devices*: boards the server has admitted.

## How it differs from the usual approaches

- **A boot server on the board's network (PXE, TFTP).** Here the board
  fetches recovery from a server anywhere on the internet, and nothing is
  installed or changed on its network.
- **A/B partitions and over-the-air updates.** They undo a failed update.
  They do not help when the drive is wiped or corrupted. Network recovery
  does, because it needs nothing from the drive.
- **Configuration management over SSH.** It needs a system that boots and
  a way in to the board. Here the board connects out, and is reachable
  before an OS is installed and after it breaks.
- **Re-flashing a card by hand.** It needs someone at the board.

## Try it

### Hosted

1. Sign up at [app.orchestrator.sh](https://app.orchestrator.sh) with
   Google, or with an email and password.
2. Click **Add node**, generate the one-line command and run it on a board
   with 64-bit Raspberry Pi OS, Debian or Ubuntu. A blank board joins from
   the browser over USB or from a provisioning card instead.
3. Approve the board, then update its bootloader from the node's page so it
   boots from the network.

Boards need outbound access to the service on ports 443 and 50051, and 80
for network boot.

### Self-hosted

On a fresh Ubuntu machine one command installs the server and asks what it
needs to know: whether it is reached on your local network or from the
internet, whether to set up network boot, and the owner's email. It
checks every answer and port before it changes anything. The local-network setup has no certificate, which rules out
flashing from the browser and passkeys ([Security and trust](#security-and-trust)).

```sh
curl -fsSL https://get.orchestrator.sh | sh
```

The address serves [`tools/install/install.sh`](tools/install/install.sh)
from this repository. To read it first, download it with
`curl -fsSL https://get.orchestrator.sh -o install.sh` and run `sh install.sh`.

Requirements, from [`docs/INSTALL.md`](docs/INSTALL.md#what-you-need):

- An x86-64 or arm64 machine or VM with Debian or Ubuntu and 4 GB of RAM,
  plus disk for OS images. A Raspberry Pi 5 with an SSD works as the
  server. The installer installs Docker on Ubuntu; on Debian, install
  Docker first.
- A name or address for the server that will not change: it is written
  into every adopted board and its bootloader. A `.local`
  name is refused because a Pi bootloader cannot resolve it.
- On a local network, ports 80 and 50051 free. From the internet, a public
  DNS name pointing at the machine with ports 80, 443 and 50051 open.
- Loopback ports 8080, 8088 and 50052 free.

[`docs/INSTALL.md`](docs/INSTALL.md) is the
self-hosting guide and the install by hand: the platform, the
compose file, secret bootstrap, first sign-in, adopting the first device,
upgrading, and recovering a lost password. Backup and restore are in
[`docs/BACKUP-RESTORE.md`](docs/BACKUP-RESTORE.md), every configuration
variable the server reads in [`docs/CONFIGURATION.md`](docs/CONFIGURATION.md),
and how to reach the API through the proxy, sign in and pass a
re-authentication in
[`docs/INSTALL.md` → "Using the API"](docs/INSTALL.md#using-the-api).

### Moving between them

A board can move from the hosted service to your own server or back: adopt
it on the new server, update its bootloader there, then delete it on the
old one ([`docs/INSTALL.md`](docs/INSTALL.md#uninstalling), "Moving a
device to another server").

## Supported boards

Raspberry Pi 5, Raspberry Pi 4, Compute Module 5 and Compute Module 4 (eMMC
or Lite), all over wired Ethernet. Carrier-board profiles apply to the
Compute Modules and the Pi 5. The recorded install runs above were made on
a CM4 and a CM5 Lite. Details:
[`docs/SUPPORTED-HARDWARE.md`](docs/SUPPORTED-HARDWARE.md).

## Pricing and limits

No feature is held back for a paid tier. The only limit is the number of
devices.

| | Free | Above that |
|---|---|---|
| Self-hosted | 25 devices per server, counted across all its accounts | A signed entitlement file raises or removes the limit |
| Hosted at app.orchestrator.sh | 10 devices per account | A subscription, by request |

Subscriptions are not on sale yet. During the beta, larger installs are
handled one by one: write to sales@orchestrator.sh, or see
[orchestrator.sh/subscription](https://orchestrator.sh/subscription/).

At the limit every device already admitted keeps connecting,
taking commands, netbooting and updating, whether or not an entitlement has
expired. A new device waits, un-admitted, until a slot is free: remove a
device, or apply an entitlement file under Account → Subscription. A device
refused at its first connect asks again on its own, about every 15
minutes. The free limit is a floor that no file lowers. The full
specification, including how a per-account limit works, is
[`docs/ENTITLEMENT.md`](docs/ENTITLEMENT.md).

## What it does

### Add a board

| The board | How it joins |
|---|---|
| Already runs 64-bit Raspberry Pi OS, Debian or Ubuntu | Run one generated command on it as a user with sudo. The script installs the signed agent and reports when it has connected. |
| Can be connected to a computer over USB in boot mode | Flash its bootloader from the browser (Chrome or Edge, over HTTPS). The node arrives already approved. |
| Is blank and takes a card | Boot it from a provisioning card made from a downloaded bundle. |

A board that joins with a shared token waits as pending until someone
checks its hardware serial and approves it. Approval applies the node's
access and software at once. A fourth way, for a server on the same
network as its boards, is adoption over SSH, off by default
(`SSH_ADOPT_ENABLED`). The install paths, with their API
calls, are in [`docs/INSTALL.md`](docs/INSTALL.md#adopt-a-first-device).

### Network boot over HTTP

Every adopted board gets a bootloader (EEPROM) configuration made for it:
the server's name, the path for its own serial, and the server's public
key. On every boot the bootloader asks the server first. The server either
serves the signed recovery image, which the board loads into RAM, or
withholds it, and the board boots its own drive. If the server cannot be
reached, the board boots its own drive. Nothing changes on the router, and
the server can be on another network or on the internet. Design:
[`docs/NETWORK-FIRST.md`](docs/NETWORK-FIRST.md).

### Install an operating system

Upload an OS image once. The server compresses it into its deploy format,
and the agent in recovery streams it onto the board's drive, checks its
SHA-256 and installs itself into the new system. Set the board's boot
target to its local OS and it boots from its drive. Pipeline:
[`docs/OS-IMAGES.md`](docs/OS-IMAGES.md).

Recorded runs over the internet with an Ubuntu Server image, from approval
to a managed, online board:

| Time | Step |
|---|---|
| T+0 | The board is approved by its serial and its bootloader is flashed. |
| T+2 min | The 98 MB signed recovery system is running from RAM. |
| T+3 min | The OS is written. Ubuntu took 81 seconds on a CM4. |
| T+4 min | The signed agent is installed into the new system. |
| T+6 min | The board boots from its drive and reports in. |

In those runs a CM5 Lite took 5 min 42 s and a CM4 6 min 9 s in total.

### Recovery without A/B partitions

Because a board asks the server first on every boot, it can be brought
back even when its OS is broken beyond booting:

1. Set the board's boot target to Recovery in the browser.
2. A board that cannot boot its drive keeps asking the server, about every
   30 seconds, and takes the recovery system on its next request. In two
   recorded runs a board with a wiped drive was back in recovery 57 and 73
   seconds after the switch, with no power cycle.
3. Open a root terminal in the browser, reinstall the OS or restore a
   backup.

A board that is still running is sent to recovery with one click. That
reboot lasts one boot: the previous boot target comes back when the board
reconnects. Turn on the watchdog (below) so that a board whose system
hangs reboots by itself.

### Day two

**Access**

- Linux users, SSH keys, trusted SSH certificate authorities and sudo, set
  for one board, an organizational unit or the whole account. A
  configuration that would leave no working login is refused.
- A root terminal in the browser, also in recovery.
- Roles from viewer to super admin, granted on the account or on one unit
  ([`docs/ORGANIZATION-PAGE-PLAN.md`](docs/ORGANIZATION-PAGE-PLAN.md)).

**Software**

- Managed software: Docker, a kiosk display, Tailscale or ZeroTier, a
  metrics exporter, security updates. Assign it to a unit and its boards
  keep it installed ([`docs/UTILITIES-PLAN.md`](docs/UTILITIES-PLAN.md)).
- Playbooks: steps run on the boards you pick, by hand or on a cron
  schedule, with retries, timeouts and a result per node
  ([`docs/PLAYBOOK-SUBSYSTEM-REVIEW.md`](docs/PLAYBOOK-SUBSYSTEM-REVIEW.md)).
- Secrets, encrypted at rest
  ([`docs/CONFIGURATION.md`](docs/CONFIGURATION.md#secrets-at-rest)),
  variables and tracked files with versions.

**Resilience**

- Backups of a board's drive, and golden images captured from one board
  and installed on others.
- Watchdog presets that reboot a board when the agent, the OS or a network
  target stops answering.

**Hardware**

- Carrier-board profiles (the Compute Blade included), overclock presets,
  status LEDs and bootloader updates
  ([`docs/DEVICE-PROFILES.md`](docs/DEVICE-PROFILES.md),
  [`docs/DEVICE-COMMANDS.md`](docs/DEVICE-COMMANDS.md)).

**Oversight**

- A page of the boards that need attention, with fixes for many at once.
- An audit trail of every command with its sender.
- An HTTP API for everything the web UI does
  ([`backend/docs/swagger.json`](backend/docs/swagger.json)); a test fails
  when a served route is undocumented.

## How it works

| Part | Where it runs | What it does |
|---|---|---|
| Server | Your machine, or the hosted service | Web UI and API, the device channel, OS image storage |
| EEPROM signer | Beside the server | Prepares and signs each board's bootloader image |
| Bootloader | The board's EEPROM | Holds the server's name and public key, fetches the recovery system |
| Recovery image | The board's RAM | A small Linux system with the agent inside; it does not touch the drive by itself |
| Agent | Recovery, then the installed OS | Connects out to the server over gRPC, runs commands, reports back |

The boot chain, from a blank board:

1. The board's bootloader is written once, from the browser over USB, from
   a provisioning card, or by the agent on a board that already runs Linux.
2. On power-on the board takes an ordinary DHCP address and its bootloader
   asks the server, by name, for the recovery image and its signature over
   HTTP on port 80.
3. The bootloader checks the signature against the key it holds, loads the
   image into RAM and starts it.
4. The agent inside the image connects out to the server and the board
   appears in the browser.
5. On command, the agent streams the chosen OS image onto the drive,
   checks it, and installs itself into the new system.
6. With the board's boot target set to its local OS, the server withholds
   recovery, and the board boots from its drive and reports in from the
   installed OS.

The agent-to-server protocol is described in
[`GRPC_README.md`](GRPC_README.md); `proto/orchestrator.proto` is the
contract.

## Security and trust

**What is signed, and what checks it.** The full chain is
[`docs/ARTIFACT-SIGNING.md`](docs/ARTIFACT-SIGNING.md).

- *Recovery image.* Each server has its own RSA signing key, created when
  network boot is set up and held by the EEPROM signer. Its public half is
  embedded in every bootloader the server prepares, and the bootloader
  refuses a recovery image signed by another key. Because the signature is
  checked, plain HTTP is enough for this step
  ([`docs/NETWORK-FIRST.md`](docs/NETWORK-FIRST.md)).
- *Agent in recovery.* The agent is part of the signed image. Recovery
  downloads no code.
- *Agent packages.* Each `.deb` carries a detached ECDSA P-384 signature
  made on hardware tokens that never release the private key. The agent verifies a package
  against the public keys embedded in its own binary before installing it,
  and refuses an unsigned or untrusted one. The install script and the
  server's release import check the same signatures. The public keys are in
  [`signing/trusted-keys/`](signing/trusted-keys/); **the server does not
  hold the agent signing key**.
- *OS images.* The install command carries the image's SHA-256. The agent
  holds the first part of the image back and writes it only once the digest
  matches, so a drive that fails the check never becomes bootable. The
  digest is not signed: on the unencrypted local-network setup it catches
  corruption, not tampering.

**What the server operator can do to a board.** Adopting a board hands
whoever operates the server root on it, by design: the agent runs
as root and executes what the platform sends. Read the "Trust model" section
of [`SECURITY.md`](SECURITY.md) before adopting hardware to a server you
do not run yourself. The limitations accepted for a self-hosted install are
argued one by one in
[`docs/SECURITY-ACCEPTANCES.md`](docs/SECURITY-ACCEPTANCES.md).

**How the server knows a board.** The bootloader can send only a path,
so the boot request is identified by the board's serial alone, and what it
returns is the same recovery image for every board. The board's own
credential is written to the board when it is adopted: into its bootloader
configuration, where the agent in recovery reads it, and for a board
without network boot into a file on its drive. The agent presents it when
it connects.

**What a stranger can do with the boot URL.** Someone who knows a serial
and the server's address gets the recovery image, which carries no board's
credential, and learns whether that board is set to network boot. Nobody
can substitute another image: the bootloader checks the signature.
`PROVISIONING_BOOT_FAILSAFE_THRESHOLD` makes the server send recovery
after that many boot requests in a row from a board set to its own system,
so that a board whose system cannot boot comes back by itself. Leave it at
`0`, its default, on a server reachable from the internet, because anyone
who knows the serial can make those requests. The reasoning is in
[`docs/SECURITY-ACCEPTANCES.md`](docs/SECURITY-ACCEPTANCES.md) and
[`docs/HTTPBOOT-AUTH-RESEARCH.md`](docs/HTTPBOOT-AUTH-RESEARCH.md).

**Transport.** Boards connect out; nothing has to be opened towards them.
The installer's internet setup obtains a Let's Encrypt certificate, and the
web UI and the agent connection then use TLS. Its local-network setup has
no certificate: both run unencrypted, and it is meant for a network you
trust. Browsers allow USB access and passkeys only on HTTPS pages, so
there a blank board joins from a provisioning card, and sign-in is by
password with two-step verification.

**Sign-in.** Email and password, single sign-on through an OpenID Connect
provider such as Google ([`docs/GOOGLE-OAUTH-SETUP.md`](docs/GOOGLE-OAUTH-SETUP.md)),
passkeys, and two-step verification with an authenticator app and recovery
codes, which an account can make mandatory for its members. Adding a way to
sign in asks for a recent re-authentication. A member sees and ends their
own sessions. An operator resets a lost password or authenticator from the
server's command line
([`docs/INSTALL.md`](docs/INSTALL.md#recover-a-lost-password-or-authenticator)).

**Audit trail.** Every command sent to a board is recorded with its sender, along
with changes to members, access entries, secrets, playbooks and the
entitlement. No API request deletes audit rows; they are kept for a year by
default (`AUDIT_EVENT_RETENTION_DAYS`).

**Reporting a vulnerability.** Use GitHub private vulnerability reporting
on this repository, or write to security@orchestrator.sh. The process,
response times and how fixes reach self-hosted installs are in
[`SECURITY.md`](SECURITY.md).

## Who makes it

Uptime Lab, the company behind the
[Compute Blade](https://www.raspberrypi.com/news/compute-blade-for-the-raspberry-pi-compute-module/),
which raised more than a million dollars on
[Kickstarter](https://www.kickstarter.com/projects/uptimelab/compute-blade)
in 2023. The project was started by Ivan Kuleshov, who designed the
Compute Blade after more than 15 years as a system administrator.

## Licences

- **`agent/`, `proto/` and `tools/`** are under the
  [Apache License 2.0](LICENSES/Apache-2.0.txt).
- **Everything else** (the server, the web UI, the EEPROM signer, the
  documentation) is under the
  [Fair Core License](LICENSES/LicenseRef-FCL-1.0-ALv2.md)
  (`FCL-1.0-ALv2`): use, modify and self-host it for any purpose except
  offering it to others as a competing product or service. Each version
  becomes Apache-2.0 two years after its release. Until then the server is
  *fair source*, not open source in the OSI sense.
- **What the FCL asks you not to change** is the device limit: the check of
  the signed entitlement file and the count of admitted devices.

[`LICENSE`](LICENSE) is the map and names the protected code exactly;
[`REUSE.toml`](REUSE.toml) is the machine-readable form.

Third-party notices are in [`THIRD-PARTY-NOTICES.md`](THIRD-PARTY-NOTICES.md)
and [`NOTICE`](NOTICE). What a release consists of, where each artefact comes
from and under which licence it travels is
[`docs/RELEASE-INVENTORY.md`](docs/RELEASE-INVENTORY.md).

## Components

| Directory | What it is | Licence |
|---|---|---|
| `backend/` | Go control plane: HTTP API (Echo), bidirectional gRPC for agents, WebSocket push to the UI | FCL-1.0-ALv2 |
| `frontend/` | React + TypeScript + Vite single-page application | FCL-1.0-ALv2 |
| `eeprom-signer/` | Signs per-board bootloader (EEPROM) images for signed network boot | FCL-1.0-ALv2 |
| `agent/` | Go daemon on each managed board (`orchestrator-agent`), plus a telemetry-only `orchestrator-watcher` binary | Apache-2.0 |
| `proto/` | The gRPC contract between agent and platform, the single source of truth; generated Go is copied into both Go modules | Apache-2.0 |
| `tools/` | `agent-signer` (signs agent release artefacts against a hardware-held key), `entitlement-signer` (issues device entitlements), `publication-guard` (the checks that keep this tree publishable), `configref` (generates the configuration reference), `notices` (generates the third-party notices), `netboot` (signing and payload tooling for the recovery image) | Apache-2.0 |
| `postman/` | Postman collections for the HTTP API | FCL-1.0-ALv2 |
| `config/` | Example configuration files | FCL-1.0-ALv2 |
| `signing/trusted-keys/` | The public halves of the agent-release signing keys (the same two files are embedded in the agent and the server) | Apache-2.0 |
| `docs/` | Design documents (listed [below](#design-documents)) | FCL-1.0-ALv2 |

`backend` and `agent` are separate Go modules with no shared workspace; the
Go toolchain each needs is in its `go.mod`. The frontend needs Node 22 or
later and pnpm 9.

Deployment tooling for the hosted service lives in a private repository and
is not part of this one.

## The agent mirror

[`uptime-lab/orchestrator-agent`](https://github.com/uptime-lab/orchestrator-agent)
is a read-only source mirror of `agent/`, `proto/` and `tools/agent-signer/`
under Apache-2.0, updated with one commit per agent release from the release
procedure (no CI runs there). Each release's GitHub Release carries the
signed `.deb` packages, their detached signatures and an SBOM, attached by the
maintainers. The agent's Go module path is the bare `agent`, so it is built
from a clone rather than fetched with `go get`. The mirror takes no pull
requests; changes to the agent are made here.

## Development

```bash
make help             # every target, per module

make db/up            # PostgreSQL via Docker for local development
make backend/run      # ./cmd/server — reads config_local/config.yaml (gitignored;
                      # start from config/config.yaml.example)
make agent/run
make test             # Go unit tests for every module

cd frontend
pnpm i && pnpm run dev
```

Three rules the build enforces:

- After changing `proto/orchestrator.proto`, run `make proto/generate`. The
  generated Go is copied into both modules, and CI fails when the copies
  differ.
- After changing any HTTP handler, regenerate the API docs with
  `make backend/docs`; a test fails when a registered route is undocumented
  or a documented route does not exist.
- The frontend build is `tsc -b && vite build`; `pnpm run lint` runs with
  `--max-warnings 0`.

Architecture conventions (Clean/Hexagonal, CQRS in the backend) are in
[`docs/CODE-STYLE.md`](docs/CODE-STYLE.md). Everything else a contributor
needs (setup, branches, pull requests, the publishing rules and the
contributor licence agreement) is in [`CONTRIBUTING.md`](CONTRIBUTING.md).

## Design documents

The documents under `docs/` are design records: they explain why a subsystem
is shaped as it is and what must not be undone.

| Document | Subject |
|---|---|
| [`SUPPORTED-HARDWARE.md`](docs/SUPPORTED-HARDWARE.md) | The supported boards |
| [`NETWORK-FIRST.md`](docs/NETWORK-FIRST.md) | Network-first provisioning, signed netboot and the per-device bootloader |
| [`HTTPBOOT-AUTH-RESEARCH.md`](docs/HTTPBOOT-AUTH-RESEARCH.md) | What the HTTP boot path can and cannot authenticate, and the accepted limitation |
| [`OS-IMAGES.md`](docs/OS-IMAGES.md) | The image pipeline: upload, compression, install, capture, first-boot reliability |
| [`DEVICE-PROFILES.md`](docs/DEVICE-PROFILES.md) | Declared carrier-board hardware and the apply/confirm ladder |
| [`UTILITIES-PLAN.md`](docs/UTILITIES-PLAN.md), [`UTILITIES-RESEARCH.md`](docs/UTILITIES-RESEARCH.md) | Curated software roles and why they are not OU policies |
| [`PLAYBOOK-SUBSYSTEM-REVIEW.md`](docs/PLAYBOOK-SUBSYSTEM-REVIEW.md) | The playbook engine: graph model, scheduler, scale guards |
| [`ORGANIZATION-PAGE-PLAN.md`](docs/ORGANIZATION-PAGE-PLAN.md) | The organizational-unit tree and its page |
| [`ARTIFACT-SIGNING.md`](docs/ARTIFACT-SIGNING.md) | How releases are signed and verified, and what a fork must replace |
| [`SECURITY-ACCEPTANCES.md`](docs/SECURITY-ACCEPTANCES.md) | The known and accepted limitations of a self-hosted install |
| [`GOOGLE-OAUTH-SETUP.md`](docs/GOOGLE-OAUTH-SETUP.md) | Setting up OpenID Connect sign-in |
| [`COPY-STYLE.md`](docs/COPY-STYLE.md) | The style guide for user-facing copy |
| [`CODE-STYLE.md`](docs/CODE-STYLE.md) | Code style and structure for the Go modules |
| [`RELEASE-INVENTORY.md`](docs/RELEASE-INVENTORY.md) | What a release consists of and how each artefact is verified |
| [`ENTITLEMENT.md`](docs/ENTITLEMENT.md) | The device count and the entitlement file |
| [`INSTALL.md`](docs/INSTALL.md), [`CONFIGURATION.md`](docs/CONFIGURATION.md), [`BACKUP-RESTORE.md`](docs/BACKUP-RESTORE.md) | Self-hosting, the configuration reference, backup and restore |
| [`DEVICE-COMMANDS.md`](docs/DEVICE-COMMANDS.md) | Identify, hostname, access entries and tracked files, with the API calls |

## Contributing

Issues are welcome. A pull request can be opened, and waits until the
contributor licence agreement is published. The setup, the
conventions and the rules that keep this tree free of anything from a live
install are in [`CONTRIBUTING.md`](CONTRIBUTING.md); conduct is governed by
[`CODE_OF_CONDUCT.md`](CODE_OF_CONDUCT.md).

## Trademarks

"Uptime Orchestrator", the Uptime Lab name and the product logos are trade
marks used by Uptime Lab Inc. (EU application pending); the licences grant no
right to use them beyond identifying the origin of the software. What is
allowed without asking and what needs permission is in
[`TRADEMARKS.md`](TRADEMARKS.md). Raspberry Pi is a trademark of Raspberry Pi
Ltd, used here only to name the hardware the software runs on.
