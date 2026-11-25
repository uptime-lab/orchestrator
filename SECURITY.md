# Security Policy

Orchestrator manages real hardware: its agents run as root on customer
devices, and the platform holds device credentials, SSH keys and secrets.
We take reports seriously and appreciate the time it takes to make one.

## Trust model

Adopting a device hands whoever operates the Orchestrator instance
root-equivalent control of that device. The agent runs as root and executes
what the platform sends: arbitrary shell commands and scripts, OS installs and
disk erasure, file writes anywhere on the filesystem, boot-order and EEPROM
changes, and OS users with SSH keys and `sudo`. Anyone who can sign in to the
instance with the relevant permissions, and anyone who controls the instance's
database, secrets or host, has that control over every adopted device.

This is the design, not a vulnerability. Adopt devices only to an instance you
trust with root on them, and treat `JWT_SECRET_KEY`, `SECRETS_ENCRYPTION_KEY`
and the EEPROM signing key as equivalent to root on the fleet.

Reports showing a way to obtain that control **without** being an authorised
operator — across tenants, unauthenticated, or from a device to the platform —
are exactly what we want to hear about.

The limitations that are known and accepted, each argued for a self-hosted
install built from this source, are in `docs/SECURITY-ACCEPTANCES.md`; a
report that one of them is worse than it says there is welcome.

## Supported versions

| Component | Supported |
|---|---|
| Platform (server, web UI, eeprom-signer) | the latest `platform-v*` release |
| Agent (`orchestrator-agent`, `orchestrator-watcher`) | the latest `agent-v*` release |
| Recovery image (`boot.img`, built from the separate `uptime-lab/orchestrator-boot-image` repository) | the image built from that repository's current tree carrying the latest agent release; it is versioned by the agent it carries and by its Buildroot base, and a fix ships as a rebuilt image that each install re-signs and serves (`docs/NETWORK-FIRST.md`) |
| SD provisioning bundle | follows the recovery image only once the payload is re-extracted: the server zips the directory `PROVISIONING_SD_PAYLOAD_DIR` names (`data/net_install/sd-payload` in the community compose), which is extracted from `boot.img` by `tools/netboot/extract-boot-payload.sh`. The installer runs it once; after replacing the recovery image, run it again (`sudo tools/netboot/extract-boot-payload.sh data/net_install/boot.img data/net_install/sd-payload`, `docs/NETWORK-FIRST.md` → "Serving the recovery image"), or every card made afterwards carries the old recovery system |
| Per-device bootloader (EEPROM) images | baked on demand by the install's `eeprom-signer` from the Raspberry Pi firmware it holds; supported with the platform release that contains the signer |

Fixes are not backported. A report against an older version is still
welcome; the fix lands in the next release, and the advisory names the first
fixed version.

## Reporting a vulnerability

**Please do not open a public issue, discussion or pull request for a
security problem**, and do not describe an unfixed vulnerability in a commit
message or a pull request.

1. Use **GitHub private vulnerability reporting** on this repository
   ("Report a vulnerability" under the Security tab). It opens a private
   thread between you and the maintainers and becomes the advisory when the
   fix is published.
2. If that is unavailable to you, e-mail **security@orchestrator.sh**. Say
   that you have a report and we will answer with a private channel; do not
   put the details in the first message unless you are comfortable with
   plain e-mail.

Include what you can: the affected component (server API, web UI, agent,
netboot and EEPROM path, signer, protocol), the version or commit,
reproduction steps, and the impact as you understand it. A proof of concept
against your own instance and your own devices is welcome; please do not
test against devices or tenants you do not own, and do not run
denial-of-service tests against anything you do not operate.

## What to expect

- **Acknowledgement within 3 business days** that the report was received
  and is being looked at, and a first assessment of severity and class
  within 10 business days.
- **Coordinated disclosure.** We ask that you keep the details private until
  a fix is released; we keep you informed of progress and agree the
  disclosure date with you. The target is a fix and a published advisory
  within 90 days of the report for anything confirmed; most are much faster.
  If we cannot meet that, we say so and explain.
- **A GitHub Security Advisory** on this repository (for an agent-only issue,
  also on the agent mirror) naming the affected versions, the fixed version
  or commit, and the reporter unless you prefer not to be named. CVE ids
  are requested for remotely exploitable issues.
- No bounty programme exists at this time.

## How fixes reach installs — read this if you self-host

The maintainers operate a hosted service built from this code. When a
vulnerability is **reachable without an operator account** — across
tenants, unauthenticated, from a device to the platform, or through the
netboot/EEPROM chain — the fix is deployed to the hosted service first,
before the fix and its description are published, so that the service is
not exposed by the disclosure. The same applies, with a longer window, to
issues that need an operator account or local access on a device.

**Self-hosted operators learn of a fix when the release ships — there is no
advance notice.** No mechanism reaches a self-hosted install before
publication: there is no customer list to write to, no push channel, and no
version check that phones home. What you get is:

- a published repository security advisory naming the affected and fixed
  versions;
- a release: for the server, the multi-architecture images
  `ghcr.io/uptime-lab/orchestrator/{backend,frontend,eeprom-signer}:<version>`
  built from the `platform-v<version>` tag; for the agent, the signed
  packages of the `agent-v<version>` release (`docs/RELEASE-INVENTORY.md`);
- nothing earlier.

Watch this repository's security advisories and releases (GitHub's "Watch →
Custom → Security alerts / Releases"). To take a server fix, set
`PLATFORM_VERSION` to the fixed release and pull, as described under
"Upgrading" in `docs/INSTALL.md` (take the database backup it asks for
first). A recovery-image fix is a rebuilt `boot.img`: sign it with
`tools/netboot/sign-boot-image.sh` and then re-extract the SD payload from it
with `tools/netboot/extract-boot-payload.sh` — signing alone fixes network
boot and leaves new provisioning cards on the old image
(`docs/NETWORK-FIRST.md` → "Serving the recovery image"). Treat the absence of advance notice as a fact about running this
software yourself, stated here rather than discovered later.

Agent fixes are additionally covered by the agent version floor: a server
may refuse to send certain commands to an agent older than a fixed version,
so an unpatched device degrades rather than stays silently exposed.

## Scope

**In scope** — any of the following in this repository:

- the server (`backend/`): the HTTP API, the WebSocket push, the gRPC
  endpoint agents connect to, authentication and session handling,
  tenant isolation, the encrypted-at-rest secrets, the image pipeline;
- the web UI (`frontend/`);
- the agent (`agent/`): command handling, file operations, archive
  extraction, package verification, the update path (packages are verified
  against public keys embedded in the agent — a bypass of signature
  verification is of particular interest);
- the signer (`eeprom-signer/`) and the per-device bootloader images;
- the protocol (`proto/`) and the way both sides use it;
- the netboot and recovery path end to end, including the recovery image's
  own scripts (reported here; the fix lands in the boot-image repository).

**Out of scope here**:

- the infrastructure of the hosted service — hosts, network, DNS, TLS
  termination, backups, the operations tooling — which is handled under the
  hosted service's own process. A finding in the *application* that you
  observed on the hosted service is in scope and reported the same way;
  please reproduce it on your own instance where you can;
- vulnerabilities in third-party dependencies with no demonstrated effect on
  this software (report them upstream; a dependency bump is welcome as an
  ordinary pull request);
- reports that require a user to be an authorised operator and then do what
  an operator is permitted to do — the trust model above;
- missing rate limits, version disclosure, TLS configuration and similar
  hardening items: welcome, but triaged behind anything touching tenant
  isolation, device credentials, the agent or the netboot/EEPROM chain.

## Severity: the agent runs as root

Because the agent executes as root on every managed device, a flaw that lets
the platform run something the operator did not ask for, or lets anyone who
is not the platform speak to the agent as if they were, is a compromise of
every device the instance manages, not a bug in one service. We rate
accordingly:

- **Critical**: unauthenticated or cross-tenant control of devices or of the
  platform; a device able to act on the platform as another device or as an
  operator; a bypass of agent package signature verification; a forged
  netboot or bootloader image that a device accepts.
- **High**: an authenticated operator exceeding their scope (another unit,
  another account, a device they cannot see); a path by which content the
  platform fetches or extracts on a device escapes the destination it was
  told to use; a credential or secret value reaching a log, an audit row or
  an API response that should not carry it.
- **Medium and below**: everything else, judged by what an attacker gains
  and what they need to start with.

A report that demonstrates one of the first two classes is handled under
the embargo described above whatever the component.

## Safe harbour

We will not pursue or support legal action against anyone who, in good
faith, reports a vulnerability under this policy, tests only against systems
and devices they own or are authorised to test, avoids privacy violations,
data destruction and service disruption, and gives us a reasonable time to
fix the issue before disclosing it.
