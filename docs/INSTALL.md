# Installing Uptime Orchestrator

> **A public beta: what has been exercised, and what not yet.** The
> installer has been run on a clean x86-64 Ubuntu host in its public HTTPS
> shape — behind the bundled proxy, with a Let's Encrypt certificate — and
> in the plain-HTTP shape on a local network. On such an install, by this
> page and `docs/BACKUP-RESTORE.md`: the first owner without a mail relay
> and the refused second sign-up, an invitation without mail, the
> operator's password and authenticator reset from the host, the scripted
> dump, a restore into the same install and onto an empty second host, the
> refusal of a database a newer release has migrated, an upgrade between
> releases on the unpacked-tree path, and a Compute Module 4 flashed from
> the browser, booted into recovery over the internet and installed with
> Ubuntu. Not yet exercised: bringing your own reverse proxy, the SD
> provisioning bundle, mail and single sign-on, an upgrade that crosses a
> schema change, and a host whose first user is not uid 1000. "Known
> limitations" at the end lists what the current code makes impossible or
> awkward for a first-time installer.

Uptime Orchestrator is a control plane for a fleet of Raspberry Pi–class
devices: adoption, OS installation over the network, signed network boot,
declarative access and software, and playbooks. This page installs the
**server**; devices join it afterwards.

## The quick way: the installer

On a fresh Ubuntu machine one command does everything this page describes
by hand, and asks what it needs to know:

```sh
curl -fsSL https://get.orchestrator.sh | sh
```

It first asks **how the server will be reached**, because the answer is
written into every device you later adopt:

- **On a local network** — plain HTTP, no certificate, for a home or lab
  network you trust. The installer looks for the name your router gives the
  machine and offers it; a name follows the machine when its address
  changes, so it is the better choice wherever the router hands out names
  (it must be a DNS name: a multicast `.local` name answers on a laptop and
  never in a Raspberry Pi's bootloader, and is refused). Where there is no
  such name, the machine's address is used, and the installer tells you to
  **reserve that address in the router's DHCP settings** — do it before
  adopting a device, or a changed address orphans them all. Reserve it when
  you use the name as well; some routers forget a name with the lease.
- **From the internet** — HTTPS with a Let's Encrypt certificate, for a
  machine a public DNS name already points at, with ports 80, 443 and 50051
  open.

On a local network the address must be one of this machine's own (or a
name every answer of which is one — a router that keeps an old lease's
record for the name would send some devices elsewhere, so such a name is
neither offered nor accepted); an address that leads elsewhere is refused,
with `--skip-address-check` for a NAT or proxy that forwards it here. The
address of a container bridge (`docker0`, `br-*`, `veth*`) is not counted
as this machine's: no device on the network reaches it.

Then it asks whether to set up **network boot** and the email address of the
**owner account**. It checks every answer, the install directory, and every
port the stack needs before it changes anything — before it installs a
package or clones anything. On a machine that already has git and curl,
that includes which release to install and whether its images are
published: a release tag exists as soon as it is pushed, its images only
once the release pipeline has built them, so a newest tag with no images
yet is passed over for the release before it, and a `--version` with none
is refused (a registry that cannot be asked is said, and the install goes
on). Whether an existing checkout is that release is checked after the
missing tools are installed. On a resumed install (below)
the ports are checked once the containers of the interrupted run are
stopped, since those hold them. Then it installs Docker if the machine has
none (Ubuntu; elsewhere it tells you to) — a machine that has Docker but not
its Compose plugin gets only the plugin, from the packaging its Docker came
from (`docker-compose-plugin` beside Docker's own `docker-ce`,
`docker-compose-v2` beside Ubuntu's `docker.io`; any other Docker is refused
with a pointer to the plugin's instructions), and apt is never allowed to
remove a package, so an existing engine is not replaced — clones the release, writes the
credential files and `.env`, and starts the stack **without its proxy**. It
creates the owner account through the backend's loopback port — the first
sign-up owns the server, so nobody can claim it while the installer runs —
and only then starts one of the two reverse proxies bundled in the compose
file. It imports the latest agent release and — for network boot —
generates the signing key, fetches the bootloader firmware, and downloads,
checks and signs the published recovery image. It ends by printing the
address to open and the file holding the owner's generated password
(`owner-password` in the install directory; change the password after
signing in, then delete the file).

Every question is also a flag, so it runs unattended; without a terminal,
a missing answer stops it and names the question. Flags go after `sh -s --`
when the script comes through a pipe, and `--help` lists them:

```sh
curl -fsSL https://get.orchestrator.sh | sh -s -- --help
```

- `--mode lan|public` — how the server is reached.
- `--address NAME_OR_IP` — on a local network, what devices and browsers call it.
- `--skip-address-check` — on a local network, accept an address that is not this machine's own.
- `--domain NAME` — from the internet, the DNS name pointing at this machine.
- `--email ADDRESS` — from the internet, where Let's Encrypt sends expiry notices.
- `--owner-email ADDRESS` — the email the owner account signs in with.
- `--netboot yes|no` — set up network boot and the provisioning card.
- `--recovery-image FILE` — a recovery image you built, instead of the published one.
- `--version X.Y.Z` — the platform release; without it, the latest final release (the repository's latest GitHub Release, or else its newest final `platform-v*` tag).
- `--prerelease` — without `--version`, take the newest release even if it is a release candidate.
- `--agent-version X.Y.Z` — the agent release to import (default: the latest).
- `--dir DIR` — where to install (default: `./orchestrator`).
- `--source DIR` — install in place in an existing checkout of the release instead of cloning it (`--dir` is then ignored); a checkout that is not the release being installed is refused.
- `--yes` — do not ask for confirmation.

It never touches a finished install, nor one it did not make: a directory
holding a `.env` or credential files it did not write is refused, wherever
that `.env` keeps the secrets and the data. An install it started and
did not finish — a failed step, an interrupted run, network boot that could
not be set up — is resumed by running it again with the same answers: the
credentials already written are kept, because the database was created with
them, and the finished steps are skipped. Whenever it rewrites its own
`.env`, a previous one that differs is kept beside it as `.env.bak.<time>`.
Compose names the database and storage volumes after the install
directory's name and keeps them when the containers stop, so a volume an
earlier install in another directory of the same name left behind would be
reused by this one, with credentials that cannot open its database: the
installer refuses such a volume, naming it. Remove it with
`docker volume rm <name>` (after `docker compose down` in the earlier
directory, and "The server" under "Uninstalling" for its data), or run the
installer from that directory.

From the internet, the certificate is the one step the installer cannot
finish alone: Let's Encrypt must reach the machine by its name on port 80.
If HTTPS does not answer within about two minutes, the installer restarts
the proxy once (it asks for a certificate only when it starts), and if that
does not help either it finishes the rest, says what to check — the DNS
name resolving to this machine's public address from outside, port 80 open
to the internet — and exits with an error. Once those are right, run
`docker restart orchestrator-proxy`, or the installer again: on such an
install it only checks the certificate.

The rest of this page is the same install done by hand, with the reasons —
read it to use your own reverse proxy, to change a setting afterwards, to
upgrade, and to know what the installer did.

## Contents

1. [What you need](#what-you-need)
2. [Supported devices](#supported-devices)
3. [Get the files](#get-the-files)
4. [Create the credentials](#create-the-credentials)
5. [Write the environment file](#write-the-environment-file)
6. [Put a reverse proxy in front](#put-a-reverse-proxy-in-front)
7. [Start the stack](#start-the-stack)
8. [The checks a production install refuses to boot on](#the-checks-a-production-install-refuses-to-boot-on)
9. [Create the first owner](#create-the-first-owner)
10. [Import an agent release](#import-an-agent-release)
11. [The entitlement](#the-entitlement)
12. [Adopt a first device](#adopt-a-first-device)
13. [Three things about device commands](#three-things-about-device-commands)
14. [Network boot](#network-boot)
15. [Upgrading](#upgrading)
16. [Recover a lost password or authenticator](#recover-a-lost-password-or-authenticator)
17. [Uninstalling](#uninstalling)
18. [Troubleshooting](#troubleshooting)
19. [Using the API](#using-the-api)
20. [Known limitations](#known-limitations)

## What you need

**Server.** An x86-64 or arm64 virtual machine or host running Debian or
Ubuntu, with Docker Engine and the Docker Compose plugin (`docker compose`,
v2), **4 GB of RAM**, and disk for your OS images (a Raspberry Pi OS image
is a few gigabytes; a captured disk is the size of the device's disk). The
images are published for both architectures. On arm64 the server has run
with adopted devices, and the installer has been run on a Raspberry Pi
Compute Module 5 with an NVMe disk in the local-network shape. Keep OS
images off an SD card.
NAS appliances and LXC containers are out of scope on day one. On Ubuntu
the distribution's own packages are enough (`sudo apt-get install
docker.io docker-compose-v2`, then add your user to the `docker` group and
log in again, or prefix the `docker` commands below with `sudo`); Docker's
own repository works as well. Debian has no `docker-compose-v2` package:
install Docker from Docker's own repository there.

The 4 GB figure comes from the image pipeline: compressing an OS image runs
`xz` under an explicit memory bound, and the compose file sizes that bound for
one job at a time inside a 1 GiB container. Raising `IMAGE_MAX_CONCURRENT_COMPRESSIONS`
multiplies the memory the backend may use — read the `IMAGE_*` rows of
`docs/CONFIGURATION.md` before changing them.

**Network.** A DNS name for the server (`orchestrator.example.com` below), a
TLS certificate for it (Let's Encrypt through your reverse proxy is the usual
answer), and three inbound ports: **443** for the web UI and API, **80** for
Let's Encrypt and for the network-boot path (the device bootloader fetches
over plain HTTP; see below), and **50051** for the agent channel. Devices need
to reach all three.

**Tools on the server.** `git` (the install is a clone of a release tag)
and `curl` for the checks. Network boot and the SD bundle additionally need
`openssl`, `xxd`, `sha256sum` and `mtools` (the signer keypair, signing the
recovery image, extracting the SD payload) — and a recovery image, which
you download prebuilt or build yourself on a separate aarch64 Linux machine
("Network boot" below). The server itself builds nothing:
every server component is pulled as a published image, and the credentials
are generated by the backend image itself.

## Supported devices

Uptime Orchestrator manages the Raspberry Pi 4, the Raspberry Pi 5, the
Compute Module 4 and the Compute Module 5 (eMMC or Lite), running a 64-bit
Linux and reached over Ethernet. Wi-Fi is not used to adopt or install a
device. `docs/SUPPORTED-HARDWARE.md` has the details.

The **agent package is arm64 only**; the install script refuses any other
architecture. A device must run a Debian-based arm64 OS with `systemd` for
the one-command install; the SD bundle and network boot bring their own OS.

## Get the files

Pick the platform release you are installing first: the compose file, the
images and the commands below all name it.

```sh
export PLATFORM_VERSION=<version>   # the release to install (the tag platform-v<version>)
git clone --branch "platform-v$PLATFORM_VERSION" https://github.com/uptime-lab/orchestrator.git
cd orchestrator
```

The exported variable is for the commands on this page that run before
`.env` exists. Compose prefers the shell's value to the one in `.env`, so
once `.env` is written (below), `unset PLATFORM_VERSION` — or a stale export
in a later shell silently runs another release than `.env` names.

Clone the release **tag**, not `main`: the compose file is part of the
release, and a newer one may declare a secret or a setting the images you
run do not know, or the reverse. Downloading `docker-compose.community.yml`
alone from the same tag is enough for a server without network boot and SD
bundles; those need the clone, for the `tools/netboot/*.sh` scripts.

The images are `ghcr.io/uptime-lab/orchestrator/backend`, `…/frontend` and
`…/eeprom-signer`, one tag per platform release (`platform-v<version>` in git,
`<version>` on the image). There is also a `latest` tag, but it exists only
once a final release has been published, and a pre-release such as
`1.2.3-rc1` does not move it. The compose file does not use it: all three
images are pinned to `PLATFORM_VERSION`, and Compose refuses to start until
it is set (in `.env`, below), so the version you run, and every upgrade, is
a change you made.

**Building the images yourself** needs Docker's BuildKit builder, which
`docker build` uses only when the buildx plugin is installed (Docker
Desktop and the `docker-buildx-plugin` package carry it; `docker buildx
version` says whether you have it): the backend and frontend Dockerfiles
read `BUILDPLATFORM`, `TARGETOS` and `TARGETARCH`, which only BuildKit
sets. Each image builds from its own directory, under the name and tag the
compose file pulls, so `up -d` runs it (skip `pull` in "Upgrading" then, or
it fetches the published image over yours). The backend also takes
`--build-arg PLATFORM_VERSION=<version>`, which the web UI shows as the
server version; without it the UI shows only the build revision:

```sh
for svc in backend frontend eeprom-signer; do
  docker buildx build --load \
    --build-arg GIT_REVISION="$(git rev-parse HEAD)" \
    -t "ghcr.io/uptime-lab/orchestrator/$svc:$PLATFORM_VERSION" \
    -f "$svc/Dockerfile" "$svc"
done
```

Create the data directories the compose file bind-mounts. `DATA_PATH`
defaults to `./data`:

```sh
mkdir -p data/postgres data/backend/storage/tmp data/net_install/eeprom \
         data/net_install/sd-payload data/eeprom-keys data/eeprom-build \
         data/source-offer
# The backend and the signer both run as uid 1000 (see below).
sudo chown -R 1000:1000 data/backend data/net_install data/eeprom-keys
```

**One uid.** The backend image's own user is uid 100 / gid 101, but the
compose file runs the backend with `user: "1000:1000"`, and the signer image
runs as uid 1000 itself. So one owner, uid 1000, covers everything either
container writes or reads with restricted modes: the storage volume
(`data/backend/storage`, which also holds the upload staging directory
`tmp/` and the agent packages under `agent-artifacts/`), the signer's output
`data/net_install/eeprom`, the signing keys, and the 0400 credential files
below. **Keep the `user:` line.** Without it the backend runs as 100:101 and
can read none of the credential files — each is 0400 and owned by uid 1000,
and the signer reads the token it shares with the backend as that uid — so
it treats every key as unset and refuses to start. One uid for both
containers is the only supported shape.

## Create the credentials

Credentials reach the containers as **files**, mounted as Docker secrets,
never as environment variables (a container's environment is visible through
`docker inspect` and `/proc`). Every file named in the compose `secrets:`
block must exist before the stack starts. The backend binary writes the whole
set in one step — `--init-secrets` — so run it from the image you are about
to deploy, as the uid the containers read the files with:

```sh
mkdir -p secrets && sudo chown 1000:1000 secrets
docker run --rm --user 1000:1000 -v "$PWD/secrets:/secrets" \
  "ghcr.io/uptime-lab/orchestrator/backend:${PLATFORM_VERSION:?export PLATFORM_VERSION first (Get the files)}" \
  /app/server --init-secrets /secrets
```

`PLATFORM_VERSION` here is the shell variable exported in "Get the files";
the same value goes into `.env` below.

It generates `jwt_secret`, `secrets_encryption_key`, `cdn_signing_key`,
`db_password` and `eeprom_signer_token` (64 hex characters each, the shape the
boot checks accept), creates the optional files empty
(`secrets_encryption_key_old`, `invite_code`, `oidc_client_secret`,
`s3_access_key`, `s3_secret_key`, `smtp_password`, `entitlement` — present so
the compose file can start, empty so nothing is enabled by accident), prints
what it wrote and the
`<VAR>_FILE=…` lines the compose file already sets, and **refuses to run
against a directory that holds any of those files**: it never overwrites, because
a replaced `secrets_encryption_key` is an unreadable database.

The modes are deliberate and the tool sets them: the directory is 0700 and
the files are 0400, so the only reader is their owner. That owner is uid 1000,
which is why the command runs as `--user 1000:1000`: the compose file runs the
backend and the signer as that uid, and Postgres reads its password file as
root. A set created by hand must follow the same rule, or the backend starts
without a database password. A set written by an older image that lacks one
of the optional files is completed by hand: `sudo touch secrets/<name> &&
sudo chown 1000:1000 secrets/<name> && sudo chmod 0400 secrets/<name>`
(numeric ids work whether or not the host has a user with uid 1000).

**Back up `secrets/secrets_encryption_key` now**, before the first boot.
Every stored secret, provisioning password, tracked file and command payload
is encrypted with it, and a database restored without it is permanently
unreadable. `docs/BACKUP-RESTORE.md` says where each key belongs; the
`SECRETS_ENCRYPTION_KEY` row of `docs/CONFIGURATION.md` says what it covers.

## Write the environment file

The compose file reads a `.env` beside it. Two values are required,
`DOMAIN` and `PLATFORM_VERSION` — Compose refuses to start without either;
the rest are overrides with defaults written in the compose file.

```sh
# .env
DOMAIN=orchestrator.example.com
DATA_PATH=./data
PLATFORM_VERSION=<version>       # the release you cloned; no default
# SECRETS_DIR=./secrets
# AGENT_PUBLIC_PORT=50051        # the agent port as devices reach it
# AGENT_GRPC_PROXY_PROTOCOL=off  # `require` once your proxy sends PROXY protocol
# SERVER_MAX_ACCOUNTS=1          # accounts self-registration may create
# SERVER_OPEN_REGISTRATION=false # `true`: anyone may register a new organisation, no code; needs mail (see "Create the first owner")
# ACCOUNT_DEVICE_LIMIT=0         # devices each account may hold; 0 = no per-account limit (see "The entitlement")
# ACCOUNT_DEVICE_LIMIT_CONTACT=  # where an account at its limit writes for more, e.g. an e-mail address
# GRPC_TLS_TERMINATED_EXTERNALLY=true   # `false` + AGENT_GRPC_PLAINTEXT_OK=true for a trusted LAN with no proxy (see "The agent port")
# AGENT_GRPC_PLAINTEXT_OK=false
# IMAGE_ALLOW_PLAINTEXT_URL=false  # `true` only with the plaintext-LAN shape: OS installs over http://
# PUBLIC_SCHEME=https            # `http` with PUBLIC_WS_SCHEME=ws for the plaintext-LAN shape
# PUBLIC_WS_SCHEME=wss
# COMPOSE_PROFILES=              # `proxy-lan` or `proxy-tls` starts a bundled reverse proxy (below)
# ACME_EMAIL=                    # with proxy-tls: where Let's Encrypt sends expiry notices
# PROXY_AGENT_CONNECTIONS_PER_ADDRESS=4096  # bundled proxies: agent connections one address may hold (see "The agent port")
# PROXY_HTTPS_CONNECTIONS_PER_ADDRESS=1024  # with proxy-tls: HTTPS connections one address may hold on port 443 (same section)
# PROXY_HTTP_CONNECTIONS_PER_ADDRESS=1024  # with proxy-lan: HTTP connections one address may hold on port 80 (same section)
# PROVISIONING_BOOT_HOST=        # defaults to DOMAIN; what the bootloader is told to fetch from
# SOURCE_OFFER_DIR=/app/source-offer  # publish the GPL source offer for the recovery image, see "Network boot"
# TERMS_URL=                     # https link to your own terms of use, see below
```

`TERMS_URL` is for an install that lets other people sign up. Set it to an
`https://` page with your terms of use, and the sign-up form requires an
"I agree to the Terms of Use" checkbox linking to it, the account menu links
to it, and the backend stores each acceptance with a timestamp and an audit
event. Anyone who signs in with no stored acceptance (an account made through
an identity provider or an invitation, or one older than the setting) is
asked once, in a dialog that offers to accept or to sign out. Empty, or anything but `https://`, shows nothing. The product ships no
terms of its own. It is read when the frontend container starts, so a change
needs a restart of that container only.

`DOMAIN` is written into every adoption file (`SERVER_ADVERTISEMENT_URL`,
`GRPC_ADVERTISEMENT_URL`), into the install script, and — once you use
network boot — into every device's bootloader. **It is not a value you can
change later without touching every device**; pick the permanent name.

## Put a reverse proxy in front

The compose file publishes the backend and the frontend on the loopback
interface only (`127.0.0.1:8080`, `127.0.0.1:8088`, and the agent port on
`127.0.0.1:50052` — not 50051, which is the port your proxy listens on for
the devices; `AGENT_GRPC_BIND_PORT` moves it). A
reverse proxy of your choice terminates TLS and forwards. If the proxy runs
as a container, join it to the `orchestrator-network` network instead and
forward to `backend:8080`, `frontend:8080` and `backend:50051` by name. That
network is created by the stack's first `up` ("Start the stack"), so
configure the proxy now and start its container after the stack — or create
the network yourself first with `docker network create orchestrator-network`.

**Or use a bundled one.** The compose file carries two Traefik services that
start only when `COMPOSE_PROFILES` in `.env` names them, each with its
routes in `proxy/`: `proxy-tls` is the Traefik example below, ready-made — a
Let's Encrypt certificate for `DOMAIN` on ports 443 and 50051, `/http-boot`
on plain port 80 and everything else there redirected to HTTPS — and
`proxy-lan` is plain HTTP for the trusted-LAN shape of "The agent port".
Both send the PROXY protocol header, so set `AGENT_GRPC_PROXY_PROTOCOL=require`
with either. Both mount the `proxy/` directory and watch their file in it,
so a route file an upgrade replaces reaches the running proxy. The installer
above chooses one of them for you.

### HTTP routes

The SPA calls `<API_URL>/api/v1/…`, where `API_URL` is `https://<DOMAIN>/api`,
so the browser requests `/api/api/v1/…`; the proxy strips **one** `/api` and
the backend sees `/api/v1/…`. The WebSocket works the same way with `/ws`.
Device-facing paths are forwarded unchanged.

| Request path | Forward to | Note |
|---|---|---|
| `/api/*` | backend, with the first `/api` stripped | API, terminal WebSocket (`/api/v1/nodes/:id/terminal`), `/api/health`, `/api/ready` |
| `/ws/*` | backend, with the first `/ws` stripped | the account WebSocket |
| `/cdn/*` | backend | agent packages and signed downloads |
| `/http-boot/*` | backend | **plain HTTP on port 80 too**, see below |
| `/net_install/*` | backend | per-device EEPROM packages; on an HTTPS install over HTTPS only (the agent fetches them from the platform URL); never a static file server (the backend checks a signed capability) |
| `/capture/*` | backend | capture uploads from devices |
| `/install.sh`, `/uninstall.sh` | backend | the one-command installer and its undo |
| `/source/*` | backend | the GPL corresponding-source offer for the recovery image, tokenless (see "Network boot") |
| everything else | frontend | the SPA, which serves `index.html` for unknown paths |

Two properties of the HTTP edge matter:

- **Do not redirect `/http-boot` from port 80 to HTTPS.**
  The Raspberry Pi bootloader fetches its boot image over plain HTTP and
  does not follow a redirect, and the recovery system's clock sync
  (`/http-boot/time`) cannot speak HTTPS. A blanket HTTP→HTTPS redirect,
  which most proxy templates ship, silently breaks network boot while
  everything else works. Serve that prefix on port 80 directly to the
  backend (the files are signed; the device verifies them), and nothing
  else: `/net_install` carries per-device bootloader packages, which hold
  the device's credential, and belongs on HTTPS only.
- **No read timeout on image uploads.** An OS image upload is several
  gigabytes and the backend bounds it itself. A proxy's default request
  timeout (often 60 s) cuts the upload off and the UI reports a failure the
  backend never saw. Disable or raise the responding/read timeout on the
  HTTPS entry point. A proxy whose read timeout is one deadline for the
  whole request (Traefik's is: it bounds the TLS handshake, the headers and
  the body together, however steadily the body arrives) then holds a
  connection that never sends a request for as long as the client likes.
  Bound what can be bounded separately: the wait for the TLS ClientHello,
  the headers if your proxy has a header timeout, and the number of
  connections one address may hold.

### The agent port

Agents connect to `<DOMAIN>:50051` over gRPC with TLS — the agent switches
to TLS by itself because the platform URL in its adoption file is `https://`.
The server refuses to boot in production until the shape of that channel is
**declared**; three are supported:

1. **TLS terminated at the proxy** (the documented path): the proxy owns the
   certificate for port 50051 and forwards plaintext gRPC to `backend:50051`
   over the compose network. The backend declares this with
   `GRPC_TLS_TERMINATED_EXTERNALLY=true`, which the compose file sets.
2. **TLS passthrough to the backend**: the backend serves TLS itself when
   both `GRPC_TLS_CERT_FILE` and `GRPC_TLS_KEY_FILE` name readable files.
   The compose file sets neither and mounts no certificate, so this shape is
   an edit to it: add both variables to the backend's `environment:`, add a
   read-only volume carrying the certificate and key (readable by uid 1000),
   set `GRPC_TLS_TERMINATED_EXTERNALLY=false`, and have the proxy forward
   port 50051 as TCP without terminating TLS. Supported by the server, not
   yet tested.
3. **Plaintext on a trusted LAN, on purpose**: no certificate, the devices
   reach the server over an `http://` platform URL. Declare it in `.env`
   with `AGENT_GRPC_PLAINTEXT_OK=true` and `GRPC_TLS_TERMINATED_EXTERNALLY=false`
   (the compose file passes both; its default says a proxy terminates TLS).
   The backend then boots and says at every start, in two WARN lines, that
   device credentials and commands cross that wire readable: a
   `configuration advisory` naming `AGENT_GRPC_PLAINTEXT_OK=true`, and
   `gRPC server serving plaintext by declaration` when the agent listener
   starts. Three more
   settings in `.env` make the rest of the stack agree with it:
   - `PUBLIC_SCHEME=http` and `PUBLIC_WS_SCHEME=ws`. The agent chooses TLS
     or plaintext by the scheme of the platform URL in its adoption file
     (`SERVER_ADVERTISEMENT_URL`), and left at `https` every agent dials TLS
     against a plaintext listener; the same two settings give the browser's
     addresses (`APP_BASE_URL`, `CORS_ALLOWED_ORIGINS`, the frontend's
     `API_URL` and `WEBSOCKET_URL`) their scheme;
   - `COMPOSE_PROFILES=proxy-lan`, or a proxy of your own: every service is
     published on `127.0.0.1`, so something on the host has to forward port
     80 (with the route table above) and port 50051 as plain TCP;
   - installing an OS onto a device hands it an image URL on the same
     `http://` address, which the server refuses to dispatch in production
     until `.env` says `IMAGE_ALLOW_PLAINTEXT_URL=true`: the image is root on
     the device and its digest travels over the same connection.

   The one-command installer refuses a plaintext platform URL unless run
   with `--insecure`, for the same reason; the command the Nodes page shows
   carries the flag when the server advertises `http://`. Never use this
   shape on a network you do not own.

Either way, decide about **PROXY protocol**. Behind a TCP proxy the backend
sees the proxy's address for every agent, so its per-source bound on
unauthenticated streams cannot work and only the global bound holds. If your
proxy prepends a HAProxy PROXY protocol v2 header to each forwarded
connection, set `AGENT_GRPC_PROXY_PROTOCOL=require` and the backend keys its
bound on the real address (the parser also accepts v1, but only v2 is
exercised by the tests; prefer v2). **The two sides must agree**: a backend on
`require` refuses every connection without a header at its first read, and
a backend on `off` cannot parse a header and drops the connection. Change
them in the same deploy — backend first, then the proxy — and roll back in
the reverse order. There is deliberately no `optional` mode (a header honoured
from any peer is forgeable). Never attach a TCP health check to the agent
service at the proxy: health probes carry no header and a `require` backend
refuses them.

**Connections per address.** Every agent holds one connection to port 50051
for as long as it runs, and every device behind a NAT shares its site's
public address. The bundled proxies allow one address **4096** simultaneous
agent connections (`PROXY_AGENT_CONNECTIONS_PER_ADDRESS` in `.env`; `docker
compose -f docker-compose.community.yml up -d` then recreates the proxy with
it); a device past that is closed at the proxy, before the
backend sees it, so it never appears and the backend logs nothing. Size the
bound of your own proxy the same way — never below the largest fleet one
site will put behind one address. The bundled HTTPS proxy bounds port 443
the same way, **1024** connections per address
(`PROXY_HTTPS_CONNECTIONS_PER_ADDRESS`), idle ones included: browsers hold
a few each, but a site's devices downloading images or an agent update at
once share one address, so raise it above the number of devices one site
installs or updates at the same time. An idle connection costs the proxy
about 130 KB. The bundled plain-HTTP proxy bounds port 80 the same way,
**1024** connections per address (`PROXY_HTTP_CONNECTIONS_PER_ADDRESS`),
idle ones included: port 80 passes every connection, unread, to the HTTP
routes on Traefik's loopback, with the client's address in a PROXY protocol
header. A device booting over the network or downloading an image holds
about one connection at a time, so the bound is the number of devices one
site boots or installs at once behind one address. Nothing there bounds a
connection in time: Traefik forwards a plaintext connection without
reading it. Separately, the backend lets one address
hold `AGENT_MAX_PREAUTH_STREAMS_PER_SOURCE` (256) streams that have not yet
authenticated, under `require` only. That bounds the handshake, not the
fleet: a slot is free again the moment its agent authenticates, so a larger
site reconnecting all at once (after a server restart) is let in over a few
retries, each refusal a `ResourceExhausted` and a throttled backend warning
`too many unauthenticated streams from one source`.

**IPv6 clients can all share one address.** Every per-address bound above
— the bundled proxies' and the backend's — is only as good as the client
address the proxy sees, and with a bundled proxy on Docker's default
settings that address is not always the client's. The compose network
(`orchestrator-network`) has no IPv6, and Docker publishes the proxy's
ports on the host's IPv6 addresses as well as its IPv4 ones. An IPv4
connection is forwarded with its source address kept. An IPv6 connection
cannot be forwarded that way to a container with no IPv6 address, so
Docker's userland proxy (`docker-proxy`, on by default) accepts it and
opens a new IPv4 connection to the container, from the network's gateway
address. **Every IPv6 client then counts as one address**: they share one
pool of 1024 connections on port 443 (and 4096 on the agent port, 1024 on
port 80 of the plain-HTTP shape), so one IPv6 client holding the pool full
locks every other IPv6 client out, and they share every limit the backend
keeps per address. IPv4 clients are not affected. This applies when
`DOMAIN` has an AAAA record (or devices and browsers reach the server by
an IPv6 address) and the host has IPv6. Two remedies:

- **Do not publish an AAAA record** for `DOMAIN` (and do not hand devices
  an IPv6 address): every client then arrives over IPv4, with its own
  address. The simplest choice, and the one to take unless you need IPv6.
- **Give the compose network IPv6**, so that Docker forwards IPv6 the way
  it forwards IPv4, keeping the source address: in a compose override
  file, `enable_ipv6: true` on `orchestrator-network` (Docker Engine before
  27 also needs an IPv6 `subnet` under its `ipam:`), with the daemon's
  `ip6tables` on (the default from Engine 27). An existing network is not
  changed in place: `docker compose -f docker-compose.community.yml down`
  then `up -d` recreates it. To check it, while an IPv6 client is
  connected, `docker exec orchestrator-proxy netstat -tn` must list that
  client by its own IPv6 address, not by the network gateway's IPv4 one.

The compose file ships with neither, because the second changes the host's
networking and depends on the Docker version, and this release does not
test it. On some hosts every published-port connection, IPv4 included,
reaches the container through a proxy and arrives from one address —
Docker with its `iptables` integration turned off, and rootless Docker
with its default port driver, as far as Docker's documentation describes
them; none of these was measured for this release. If that `netstat`
shows the same private address for every client, that is the cause, and
the per-address bounds there are one shared pool. Running the proxy with
`network_mode: host` would also keep addresses, but it changes how the
proxy reaches the backend and is not a supported shape.

### Example: Traefik, file provider

Traefik takes two files. The **static** configuration defines the entry
points `web` (:80), `https` (:443), `websecure` (the HTTP routes, on
Traefik's own loopback) and `grpc` (:50051), the certificate resolver
`letsencrypt` and the file provider. This is the shape the bundled HTTPS
proxy runs. Traefik has no header timeout, and its `readTimeout` covers a
whole request, body included, so `websecure` needs `readTimeout: 0` for
uploads (the rule above; Traefik's default is 60 seconds). An entry point
with only TCP routes applies its `readTimeout` until it has read the TLS
ClientHello and then clears it, so port 443 is `https`, which passes every
TLS connection unopened to `websecure` and closes one that sends no
ClientHello within 10 seconds. That is the only time bound on port 443
before a request: everything after the ClientHello is unbounded in time —
a client that sends its ClientHello and then stalls the rest of the TLS
handshake, and one that completes TLS over HTTP/1.1 and then sends no
request or half a header (over HTTP/2 its preface must arrive within 10
seconds). Those are bounded only in number, per address: the
`inFlightConn` on the TCP route caps how many connections one address
holds. Traefik runs on the `orchestrator-network` network.

```yaml
# traefik.yml — static configuration
entryPoints:
  web:
    address: ":80"
    # No `http.redirections` here: an entry-point redirect also redirects
    # /http-boot, which the bootloader cannot follow. Port 80 serves only
    # GET requests, so its reads can be bounded.
    transport:
      respondingTimeouts:
        readTimeout: 30s
        writeTimeout: 30m
        idleTimeout: 60s
  https:
    address: ":443"
    transport:
      respondingTimeouts:
        # Bounds only the wait for the TLS ClientHello: the entry point
        # holds TCP routes only, and Traefik clears the bound once it has
        # chosen one.
        readTimeout: 10s
  websecure:
    # Reached only through the `https` route, with the client's address in
    # a PROXY protocol header.
    address: "127.0.0.1:8443"
    proxyProtocol:
      trustedIPs: ["127.0.0.1/32"]
    transport:
      respondingTimeouts:
        readTimeout: 0
        writeTimeout: 0
        idleTimeout: 60s
  grpc:
    address: ":50051"
    transport:
      respondingTimeouts:
        # Bounds only the wait for the TLS ClientHello: Traefik clears it
        # once a TCP route is chosen, so the agent's stream is not cut.
        readTimeout: 10s
certificatesResolvers:
  letsencrypt:
    acme:
      email: admin@example.com
      storage: /letsencrypt/acme.json
      httpChallenge:
        entryPoint: web
providers:
  file:
    filename: /etc/traefik/dynamic/dynamic.yml
    watch: true
```

If Traefik runs in a container, mount the **directory** holding
`dynamic.yml` (here at `/etc/traefik/dynamic`), never the file alone: git,
editors and rsync replace a file rather than rewrite it, and a single-file
mount keeps showing the container the old one, so a changed route never
takes effect and the watcher never fires.

The **dynamic** configuration (`dynamic.yml` above) carries the routes. If
you want plain HTTP redirected to HTTPS, do it with a router on `web` that
excludes the boot prefix, as `redirect-https` below does — never with
the entry-point redirect most Traefik examples show, which also redirects
`/http-boot` and so breaks network boot.

```yaml
# dynamic.yml — dynamic configuration
http:
  routers:
    api:
      rule: "Host(`orchestrator.example.com`) && PathPrefix(`/api`)"
      entryPoints: [websecure]
      service: backend
      middlewares: [hsts, strip-api]
      tls: {certResolver: letsencrypt}
    ws:
      rule: "Host(`orchestrator.example.com`) && PathPrefix(`/ws`)"
      entryPoints: [websecure]
      service: backend
      middlewares: [hsts, strip-ws]
      tls: {certResolver: letsencrypt}
    device-https:
      rule: "Host(`orchestrator.example.com`) && (PathPrefix(`/cdn`) || PathPrefix(`/capture`) || PathPrefix(`/http-boot`) || PathPrefix(`/net_install`) || PathPrefix(`/source/`) || Path(`/install.sh`) || Path(`/uninstall.sh`))"
      entryPoints: [websecure]
      service: backend
      middlewares: [hsts]
      tls: {certResolver: letsencrypt}
    # Plain HTTP: the bootloader's path, never redirected.
    device-http:
      rule: "PathPrefix(`/http-boot`)"
      entryPoints: [web]
      service: backend
    # Optional: everything else on port 80 goes to HTTPS.
    redirect-https:
      rule: "Host(`orchestrator.example.com`) && !PathPrefix(`/http-boot`)"
      entryPoints: [web]
      middlewares: [to-https]
      service: noop@internal
    spa:
      rule: "Host(`orchestrator.example.com`)"
      entryPoints: [websecure]
      service: frontend
      middlewares: [hsts]
      priority: 1
      tls: {certResolver: letsencrypt}
  middlewares:
    hsts:
      headers: {stsSeconds: 31536000}
    strip-api:
      stripPrefix: {prefixes: ["/api"]}
    strip-ws:
      stripPrefix: {prefixes: ["/ws"]}
    to-https:
      redirectScheme: {scheme: https, permanent: true}
  services:
    backend:
      loadBalancer: {servers: [{url: "http://backend:8080"}]}
    frontend:
      loadBalancer: {servers: [{url: "http://frontend:8080"}]}

tcp:
  routers:
    # Port 443: every TLS connection, unopened, to the HTTP routes above.
    https:
      rule: "HostSNI(`*`)"
      entryPoints: [https]
      service: websecure
      middlewares: [https-inflight]
      tls: {passthrough: true}
    grpc:
      rule: "HostSNI(`orchestrator.example.com`)"
      entryPoints: [grpc]
      service: grpc
      middlewares: [grpc-inflight]
      tls: {certResolver: letsencrypt}   # terminates TLS; the backend gets plaintext
  middlewares:
    # HTTPS connections one client address may hold, idle ones included;
    # size it for a site's devices downloading at once behind one NAT.
    https-inflight:
      inFlightConn: {amount: 1024}
    # Agent connections one client address may hold: a whole site behind
    # one NAT shares one (see "The agent port").
    grpc-inflight:
      inFlightConn: {amount: 4096}
  services:
    websecure:
      loadBalancer:
        servers: [{address: "127.0.0.1:8443"}]
        serversTransport: proxy-protocol
    grpc:
      loadBalancer:
        servers: [{address: "backend:50051"}]
        # Remove this line to run the backend with AGENT_GRPC_PROXY_PROTOCOL=off.
        serversTransport: proxy-protocol
  serversTransports:
    proxy-protocol:
      proxyProtocol: {version: 2}
```

With this file in place, set `AGENT_GRPC_PROXY_PROTOCOL=require` in `.env`.
Other proxies: nginx's `stream` module sends the header with
`proxy_protocol on;` and HAProxy with `send-proxy-v2`; both terminate TLS on
the listener in the usual way. The HTTP half is ordinary path routing with a
prefix strip for `/api` and `/ws`.

## Start the stack

```sh
docker compose -f docker-compose.community.yml pull
docker compose -f docker-compose.community.yml up -d
docker compose -f docker-compose.community.yml logs -f backend
```

The backend's first lines say what it loaded. A good start reads
`configuration loaded successfully`, then the schema migration, then
`gRPC server starting` and the HTTP listener. There is no separate migration
step: the backend creates and migrates its tables on every start.

One warning appears on every start of this compose file and is expected:
`configuration file not found, using environment variables` — the compose
file configures the backend entirely through its environment and the
credential files, and mounts no configuration file. On the plaintext LAN
shape (what the installer's local-network choice writes) two more WARN lines
follow, both saying the agent channel is plaintext by declaration ("The
agent port", item 3), so that shape logs three. Beside them, an INFO line
`instance anomalies are recorded in instance_anomalies; this install
declares that nothing reads them (INSTANCE_MONITORING_READER=none)`: the
backend records instance-level anomalies for a separate monitoring reader,
which this compose file does not run, and the compose file says so. Nothing
needs doing for either.

**Three settings of this compose file differ from the code's defaults in a
way you will notice** (`docs/CONFIGURATION.md` gives the code's; set the
variable in `.env` and `up -d` to change any of them):

- **Device command history is deleted after 90 days**
  (`NODE_COMMAND_RETENTION_DAYS=90`; the code's default keeps it forever).
  A command that is still waiting for delivery or a result is never deleted;
  a finished one, with its output, leaves the node's history once it is 90
  days old. Set `0` to keep every command, or a larger number.
- **The browser terminal is on** (`TERMINAL_ENABLED=true`; the code's
  default is off). It is a root shell on a connected node, opened from the
  node's panel by a member whose role allows it (admin and super admin, by
  default); set `false` to remove it from the install.
- **An uploaded OS image is not kept once it is compressed**
  (`IMAGE_COMPRESS_REPLACE_ORIGINAL=true`; the code's default keeps the
  original as a safety copy). Each image then takes its disk space once, and
  a compression cannot be redone from the original: upload it again. Set
  `false` to keep originals (`docs/OS-IMAGES.md`).

Two endpoints tell you the state from outside, both through the proxy:

- `https://orchestrator.example.com/api/health` — **liveness**: the process
  is up. This is what the container healthcheck watches, deliberately: a
  database outage must not become a restart loop.
- `https://orchestrator.example.com/api/ready` — **readiness**: the
  database answers and the storage volume has free space (or the object
  storage bucket is reachable). Point an uptime monitor here; it is cached
  for five seconds and tolerates three consecutive bucket misses before it
  reports failure, but none for the database.

```sh
curl -fsS https://orchestrator.example.com/api/health
curl -fsS https://orchestrator.example.com/api/ready
```

## The checks a production install refuses to boot on

The backend image sets `APP_ENV=production` and the compose file does not
override it, so the install runs in **production** mode (an unset `APP_ENV`
would mean the same): the backend computes `Config.Problems()` and refuses to start while the list is
non-empty, printing every item. A fresh install meets five of them; here is
how the steps above satisfy each. The full list, with the two conditional
checks (email, PROXY-protocol spelling), is in `docs/CONFIGURATION.md` →
Production checks.

| The backend refuses when | Satisfied by |
|---|---|
| `JWT_SECRET_KEY` is unset, is shorter than 32 characters, or contains a placeholder marker such as `change-me` | `secrets/jwt_secret`, written by `--init-secrets` |
| `CDN_SIGNING_KEY` is unset (or weak) | `secrets/cdn_signing_key` — a second random value; it has to agree with nothing outside this server |
| `SECRETS_ENCRYPTION_KEY` is unset (or weak) | `secrets/secrets_encryption_key` — and its backup |
| the agent channel would be plaintext with nothing declared: no certificate files, `GRPC_TLS_TERMINATED_EXTERNALLY` not `true`, `AGENT_GRPC_PLAINTEXT_OK` not `true` | the compose file sets `GRPC_TLS_TERMINATED_EXTERNALLY=true`; your proxy must actually terminate TLS on the agent port (or declare the LAN shape, see "The agent port") |
| CORS allows credentials with a wildcard origin | the compose file pins `CORS_ALLOWED_ORIGINS` to `https://<DOMAIN>` |

An empty `secrets/invite_code` is not a refusal: it means sign-up is closed
once the first owner exists (next section).

The refusal names the variable and the reason; fix the file, recreate the
backend. Setting `APP_ENV=development` to get past a refusal turns every
check but the three secret checks into a warning, logs every SQL statement
with its parameter values, and is the one thing this page asks you not to do
on a reachable server. The secret checks refuse in every mode: there is no
environment in which a placeholder key is acceptable on a server anyone else
can reach.

## Create the first owner

Open `https://orchestrator.example.com`, choose sign-up, and enter an email
address and a password (the name fields are optional). **An empty install
admits its first owner without a code** — there is nobody yet who could have
handed one out — so the sign-up page says so and shows no invite code field;
this creates the first user, their account, and makes them its owner: the
role the product calls **super admin** (`super_admin` in the API, and the
word in its refusals, "super admin rights required"), which alone manages
members, invitations and roles. Do this right after the first boot: until the first owner exists,
whoever reaches the sign-up page first becomes the owner — and the tokenless
`GET /api/v1/server/sign-in-options` tells anyone who asks that the install is
unclaimed (`docs/SECURITY-ACCEPTANCES.md` §6.4). Register the owner before the
port is reachable from anywhere you do not trust.

From the first account on, sign-up is **closed**: a second sign-up is refused
with "registration is currently closed; ask the operator of this server to
invite you to an account" (HTTP 403, code `registration_closed`), whether
because `secrets/invite_code` is empty — the default `--init-secrets`
writes — or because `SERVER_MAX_ACCOUNTS` (1 in the compose file) is reached.
Colleagues join the existing account by invitation from the Members page; an
invitation does not spend the ceiling. To host several separate
organisations on one server, put a code into `secrets/invite_code`, raise
`SERVER_MAX_ACCOUNTS`, and recreate the backend: a sign-up presenting that
code then creates a new account, up to the ceiling. The code is a shared
string with no expiry; the ceiling is what bounds it.

To let **anyone** register a new organisation without a code — a hosted
install open to the public — set `SERVER_OPEN_REGISTRATION=true` in `.env`,
raise `SERVER_MAX_ACCOUNTS`, and recreate the backend. Open registration
needs working mail (`EMAIL_*` and `APP_BASE_URL`, see the configuration
reference): without it sign-up stays closed, the sign-up page says why, and
the backend logs a `configuration advisory` line at start.

- **Every open sign-up confirms its address before the account can do
  anything.** The sign-up answers "check your email" and opens no session.
  Until the link in that mail is opened, signing in reaches only a "confirm
  your email address" screen with a button that sends a new link; no node
  can be adopted, and no token, bundle or other change can be made.
- **Unconfirmed sign-ups do not spend the ceiling.** They count toward
  `SERVER_OPEN_REGISTRATION_MAX_PENDING` instead (20 by default), while they
  are younger than `SERVER_OPEN_REGISTRATION_PENDING_HOURS` (48 by default);
  past the cap, sign-up answers "try again later". A confirmed sign-up
  becomes an ordinary account on its first use, if the ceiling has room; at
  the ceiling it stays unusable and is told that registration is closed. An
  abandoned sign-up is not deleted: it holds nothing but its owner, stops
  counting after the pending window, and can still be confirmed later.
- **The open form does not tell anyone whether an address is registered.**
  An address that already has an account gets the same "check your email"
  answer and no mail; its owner signs in, or resets the password.

Sign-up through the identity provider, when one is configured, needs no
code either, whatever `OIDC_AUTO_PROVISION` says; the provider has verified
the address, so such an account is usable at once and counts toward the
ceiling. Open registration takes precedence over a code in
`secrets/invite_code`: the code is then unused, and the backend says so in
an advisory. With `SERVER_MAX_ACCOUNTS=0` nothing bounds how many confirmed
accounts strangers create, each carrying the full per-account quotas on your
disk and database, and the backend logs an advisory for that too. The first
owner, sign-ups with an invite code and invitations are unaffected: those
accounts are usable before their address is confirmed, as before.

An invitation is valid for seven days, once, for the address it names, and
only while the member who issued it is still an active super admin of the
account (the only role that may invite). Removing, disabling or demoting that
member below super admin revokes their pending invitations at once, and an
invitation whose issuer has lost the role is refused at acceptance as if it
had been revoked. A super admin who stays one keeps their invitations.

**An owner the installer created** signed up through the backend's loopback
port with an email address and a password only, so the product names them
with its defaults: the user is called "User" and the account "My Account".
Rename the account on Team Management (the account menu) → Organization →
Rename. The web UI has no field for the user's own name; set it through the
API, `PUT /api/v1/users/me` with `{"name": "…"}` (a route that needs a
recent re-authentication, "Using the API" below).

Once sign-up is closed, the sign-up page says so instead of showing a form,
and the sign-in page drops its "create an account" link (both read the
tokenless `GET /api/v1/server/sign-in-options`).

Without an email relay, invitations are created but not delivered: the
Members page shows the invite link for you to pass on by hand, and says it
was not emailed. The sign-in page shows no "forgot password" link; it tells
the user to ask the operator to reset the password from the server ("Recover
a lost password or authenticator" below), and the account page hides the
email-confirmation section. See `docs/EMAIL.md` for what mail adds and what
works without it.

Without single sign-on (`OIDC_ENABLED` unset, the compose default), the
sign-in and sign-up pages show only the email and password form, and the
account page has no sign-in methods section. With it, every page names the
provider: Google for Google's issuer, the value of `OIDC_PROVIDER_NAME` for
another provider, and "single sign-on" when neither names it (the sign-in
and sign-up buttons then read "your identity provider";
`docs/GOOGLE-OAUTH-SETUP.md`).

An account owner who asks to delete the account (Team Management → Delete
this account) files a request: on a self-hosted install it is recorded for
the operator and nothing is erased automatically, and the page says exactly
that. The account export on the same page is the copy to keep.

## Import an agent release

**This is a required step of the first start.** The server ships no agent
package: until a release is imported, the install link has nothing to
download (the served script stops with `agent package signature
unavailable`), installing an OS onto a device cannot place an agent on it,
and `update_agent` has nothing to serve. Import one now, and again whenever
you want devices to move to a newer agent.

Agent releases are published, signed, as GitHub Releases of
`https://github.com/uptime-lab/orchestrator-agent` (tag `agent-v<version>`).
A release carries an `orchestrator-agent_<version>_linux_arm64.deb` and an
`orchestrator-watcher_<version>_linux_arm64.deb`, each with its detached
signature `….deb.sig`; those four files are what the import takes. Download
them and import them with the server binary; the server never fetches them
itself.

```sh
V=<agent version>
mkdir agent-release-$V
# With the GitHub CLI:
gh release download agent-v$V -R uptime-lab/orchestrator-agent \
  -D agent-release-$V -p '*.deb' -p '*.deb.sig'
# Or with curl alone:
for f in orchestrator-agent_${V}_linux_arm64.deb orchestrator-watcher_${V}_linux_arm64.deb; do
  for g in "$f" "$f.sig"; do
    curl -fL -o "agent-release-$V/$g" \
      "https://github.com/uptime-lab/orchestrator-agent/releases/download/agent-v$V/$g"
  done
done

docker compose -f docker-compose.community.yml run --rm --no-deps \
  -v "$PWD/agent-release-$V:/release:ro" \
  backend /app/server --import-agent-release /release --set-latest
```

The files must be readable by uid 1000, the user the backend runs as. The
import verifies every package's signature against the public keys embedded
in the server — the same set the install script checks with — and checks
that the version and architecture inside each signed package match its file
name; a checksum file in the directory (`checksums.txt`, `SHA256SUMS` or a
`<file>.sha256`) is checked too. **Nothing is written unless every package
passes.** It then stores the packages under the `agent-artifacts/` prefix of
whichever storage the server uses (the local volume or the S3 bucket), where
`/cdn/agent/…`, `/cdn/watcher/…` and `/cdn/agent-versions` find them, and
prints one line per object stored: package, version, architecture, size,
the signing key and the storage key. Exit 0 is success, 1 a refusal or a
failure, 2 a usage error.

- **`latest` moves only with `--set-latest`.** `latest` is a literal file
  name, not a resolved version: the install script, the Nodes page's update
  action and the Status page's update remedy all ask for
  `orchestrator-agent_latest_linux_<arch>.deb`. `--set-latest` also writes
  `agent-artifacts/latest.<arch>.version` for each architecture the release
  carries — the version that architecture's `latest` now is — and the
  "update available" badge and the Status page compare each node against
  that version, not against the newest one listed. A release that carries
  one architecture only leaves the other's `latest` and marker where they
  were (`/cdn/agent-versions` reports both as `latest_by_arch`). Without the flag the
  release is imported and can be installed by naming its version
  (`update_agent` with an explicit version through the API), `latest` and
  its marker stay where they were, and the import says which version a
  one-click update keeps installing — what you want when importing an older
  version to roll back, and a surprise when you meant to upgrade. An install
  that has never run `--set-latest` has no marker and is compared against the
  newest listed version, as before. `--set-latest` moves only the
  architectures the release carries; the summary names any architecture
  whose `latest` stayed behind. If moving `latest` fails half way, the
  previous `latest` objects and marker are written back.
- **A version already published is refused** unless you pass `--replace`.
  If a replacement fails half way, the objects it was replacing are written
  back, so a published package and its signature never disagree; the error
  names any object that could not be put back.
- **A pre-release** is named `…_0.8.0-rc.1_…` while its package says
  `0.8.0~rc.1` (Debian's pre-release spelling); the import treats the two as
  the same version.
- **Nothing is ever deleted.** Keep the previous release as the rollback
  step; removing old versions is your decision and is done by hand.
- **A rollback must say so.** The agent refuses a package older than the
  one it runs — the version is read from inside the signed package, so
  moving `latest` back does not get past it — unless the `update_agent`
  command carries `allow_downgrade: true`. The Nodes page's update action
  never sends it; roll a node back through the API, with the older release
  still stored (or imported again without `--set-latest`):

  ```sh
  # The backend's POST /api/v1/nodes/<node id>/command; through the proxy
  # the first /api is stripped ("HTTP routes"). <token> is a signed-in
  # user's access token.
  curl -fsS -X POST "https://orchestrator.example.com/api/api/v1/nodes/<node id>/command" \
    -H "Authorization: Bearer <token>" -H "Content-Type: application/json" \
    -d '{"command":"update_agent","params":{"agent_version":"<older version>","allow_downgrade":true}}'
  ```

  Without the flag the node fails the command with `refusing rollback (set
  allow_downgrade to override)`.

The agent and the install script verify the signature again before they
install, so a package from elsewhere is refused there as well.

## The entitlement

A self-hosted server manages **25 devices free**, counted across the whole
install. Above that an admission — a device's first connect into the node
made for it, or the approval of a pending device — is refused with a typed
`device_cap_reached` response, and the node waits un-admitted, until an
entitlement file is applied: pasted on the account page by the instance
operator, or written into `secrets/entitlement` (the compose file maps it as
`ENTITLEMENT_FILE`; an empty file means the free cap) followed by a recreate
of the backend. Nothing already adopted is ever locked out, and no download
is gated. What the file contains,
how it is obtained and verified, and what expiry does are in
`docs/ENTITLEMENT.md` (its "Obtaining a file" section says where one comes
from).

An install that hosts other organisations can also limit each **account**:
set `ACCOUNT_DEVICE_LIMIT` (for example `5`) and `ACCOUNT_DEVICE_LIMIT_CONTACT`
(where to ask for more) in `.env` and recreate the backend. Every account
other than the instance operator's may then hold that many admitted devices,
in addition to the install-wide limit; its Subscription page shows its own
plan and count instead of the install's entitlement. A larger limit for one
account, optionally until a date, is a value the operator writes into the
database: `docs/ENTITLEMENT.md` → "The per-account device limit" has the
statements.

## Adopt a first device

A device becomes a node by one of three paths. Only the first works on an
install with no recovery image: the SD bundle is the recovery image's boot
partition (`data/net_install/sd-payload`), and network boot needs all of
"Network boot" below. Every path needs an imported agent release ("Import an
agent release"): the install link downloads it, and an OS installed from the
recovery system gets its agent from the same place.

### By install link (a device already running an OS)

This needs an imported agent release ("Import an agent release"); without
one the script stops with `agent package signature unavailable`.

On the Nodes page choose *Add node* → *One command*. The server mints an
adoption token (a master token, valid for seven days by default; devices
adopted with it land **pending approval**) and shows the command to run on
the device as root:

```sh
curl -fsSL https://orchestrator.example.com/install.sh | sudo sh -s -- --token '<token>'
```

`--token-file <path>` reads the token from a file instead (keep it
root-only); use it whenever the install runs inside a wrapper whose command
line is recorded, as the system journal records a unit's.

The script checks the architecture (arm64 only), refuses a plaintext platform
URL, downloads the agent package from the server's `/cdn` route, verifies it
against the signing keys embedded in the script, installs it, writes the
adoption file and waits up to three minutes for the agent's first connection.
It reports success (exit 0, a line starting with `OK:`) only when the agent
is installed and the server has accepted its connection — the server answers
a refusal up to 30 seconds after the connection opens, so the script waits
that long before it calls a connection accepted. Otherwise it says what it saw
and exits non-zero:

| Exit | Meaning |
|---|---|
| 2 | bad arguments or token, not root, or the one-time code could not be exchanged |
| 3 | not an arm64 Debian-based device |
| 4 | the agent package could not be downloaded |
| 5 | the agent is installed but its service is not running |
| 6 | a plaintext (`http://`) platform URL without `--insecure` |
| 7 | the agent was not installed |
| 8 | the agent runs but has not reached the server (it keeps trying on its own — check that the device reaches the server on 443, or 80 on a local-network install, and on the agent port) |
| 9 | the server refused the device: the install is at its device limit ("The entitlement") |
| 10 | the package signature is missing or invalid, or `openssl` is missing; nothing was installed — or the server refused the device: its account is at its device limit |
| 11 | the server did not accept the device's credential: the token expired or was revoked, or its node was deleted |
| 12 | the board is registered to another account on the server |
| 13 | an account token on a board without a unique hardware serial; use a per-node command |
| 14 | the server could not check its device limit; this clears on its own and the agent keeps asking |

A one-time code (`ac_…`) is exchanged only after the agent package is
installed, so a run that stops earlier leaves the code unused and the same
command can be run again. The device then appears on the Nodes page; approve
it. A per-node token from an existing node's card pre-approves instead.

**A freshly installed system may make the command wait.** Within minutes of
its first boot, Ubuntu (for one) starts its first automatic upgrade, which
can hold the package manager for close to half an hour on a full image. The script waits for
it — up to 45 minutes, printing once a minute which process holds the lock
and how long it has waited — and installs as soon as it is free. Until then
the server has not heard from the device at all: no node, no refusal. If the
wait runs out the script exits 7 and nothing was changed; run the same
command again. Running the command once the first upgrade has finished
avoids the wait. (The upgrade restarts services whose libraries it replaced;
a script run from your own terminal is not affected, one wrapped in a
service of its own may be restarted.)

**Before approving, look at the boot target.** A node adopted this way
arrives with its boot target unset (*Inherit*), and on a default install
that resolves to network boot (`docs/NETWORK-FIRST.md` → "Serve/withhold
control"). It matters only for a device whose bootloader fetches from this
server: on its next reboot such a device boots the recovery system instead of
its own operating system. For such a device set the target to *Local OS* on
the node's panel (or `PUT /api/v1/nodes/{id}/policy` with
`{"boot_to_os":"on"}`) before or right after approving it; *Update
bootloader* on the panel also sets an unset target to *Local OS*, because
the node reports from an installed system. A device whose bootloader does
not ask this server boots its disk whatever the target says. Approving a
device that runs its own operating system never rewrites its bootloader,
whether it boots from NVMe, USB or an SD card: only a device running the
recovery system from a provisioning card is flashed for network boot at
approval.

**A device adopted again keeps the old node's credential in its
bootloader.** The command writes a new adoption file on the disk; a device
provisioned for network boot also carries the previous node's credential in
its bootloader (`UPTIME_TOKEN`), and that copy is not touched. If the old
node is deleted, the server refuses that credential, so the device's next
network boot — recovery, a reinstall — presents an identity the server
refuses. Run *Update bootloader* on the new node's panel
(`POST /api/v1/nodes/{id}/provision-eeprom`) once it is approved: it flashes
a bootloader carrying the new node's credential, which the device uses from
its next boot (on an installed system with `rpi-eeprom-update` the flash itself
does not reboot it; on the recovery system, or a system without that tool, the
node reboots at once to apply it).
While the board is that new node — including a node created by flashing the
board again from the browser, which knows only the last eight characters of
its serial — the deleted node's record cannot be restored: the list of deleted nodes names the new node, and a restore is
refused (409 `serial_bound_in_account`) until you delete the new node first.

Requirements on the device: a Debian-based arm64 OS with `systemd` and
`dpkg`, root, `curl` or `wget` (the downloads), `openssl` (the script
verifies the package signature with it and refuses to install without it),
and outbound reach to the server on 443 and the agent port. Master-token
adoption additionally needs a Raspberry Pi hardware serial; per-node tokens
work on any arm64 Debian-based machine.

The undo is `curl -fsSL https://orchestrator.example.com/uninstall.sh | sudo sh`,
public and tokenless on purpose, so a device removed from the account can
still get the agent off itself; "Uninstalling" below gives the order for each
case, including a device you reach only through the server.

### By SD provisioning bundle (a blank device)

*Nodes → Add node → Master image → Download SD Image* builds it: a ZIP of
the recovery image's boot partition plus a generated adoption file carrying
a master token. Unzip it onto a FAT32 SD card, insert, power on: the device boots the
recovery system from the card, adopts (pending), and can then be installed
with an OS image from the Images page.

**The card must have an MBR partition table with the FAT32 volume as its
first partition.** A card erased on macOS with the default "GUID Partition
Map" scheme carries a small EFI partition first: an older bootloader does
not boot it at all, and a newer one (a Compute Module 5, a Pi 5) boots the
recovery system from it, which then looks for the adoption file on the
first partition only, finds none and never reaches this server — the screen
shows a login prompt and the node never appears. In Disk Utility choose
*View → Show All Devices*, select the card itself, *Erase*, format
*MS-DOS (FAT)*, scheme *Master Boot Record*; then unzip the bundle again.

**A device that another server managed before** needs more than the card.
Its bootloader still tries that server's boot order — network, then NVMe,
then USB, then SD — so if its drive holds a bootable system it starts that
system and never reads the card; and without a readable card its recovery
system uses the identity the old server baked into the bootloader, which
this server refuses (`adoption handshake rejected … node not found` in the
backend log). Remove the drive while the card does its work, or flash the
bootloader from the browser (Nodes → Add node) instead, which replaces the
old identity. The bundle endpoint is enabled by
`PROVISIONING_SD_PAYLOAD_DIR`, which the compose file points at
`data/net_install/sd-payload` — that directory must hold the extracted boot
partition of the recovery image. See the next section for where the image
comes from.

**Treat a provisioning card as a secret.** It carries the master token, and
every device it provisions writes a credential onto it.

The bundle carries the recovery image's licence notices and its
`SOURCE-OFFER.txt`; the download dialog links to `/source/`, where this
install serves the corresponding source if `SOURCE_OFFER_DIR` is set (see
"Network boot").

### By network boot

A device whose bootloader has been baked to fetch from this server boots the
recovery system over HTTP with no card at all, on every reboot the policy
asks for. That is the full network-first design, `docs/NETWORK-FIRST.md`,
and needs the signer material below.

A node that is up in the recovery system has no operating system yet.
Installing one is three actions on the node's panel, in this order: *Install
OS* (an image from the Images page, onto the drive you pick), *Provision
Access* (optional: the first user, its key and the environment), and *Install
Agent* — the step that puts the agent and this node's identity onto the new
system.
**A device rebooted after *Install OS* alone starts an operating system with
no agent in it and never reports back**; it is recovered by setting its boot
target to *Recovery (netboot)* and power-cycling it. Only then set the boot target to
*Local OS* and reboot (`docs/NETWORK-FIRST.md` → "Lifecycle").
Provisioning the bootloader before any OS is installed is safe: a node in the
recovery system with no OS install on record keeps its boot target, so the
flash's reboot brings it back to recovery. Once an install has succeeded,
provisioning sets an unset boot target to *Local OS*.

## Three things about device commands

- **A script runs under `/bin/sh`, whatever its first line says.** The
  agent hands the body of `run_script` to `/bin/sh` as a file, so a
  shebang (`#!/usr/bin/python3`) is an ordinary comment and a Python body
  fails with shell errors (`import: not found`). Run another interpreter
  from the shell:

  ```sh
  python3 - <<'EOF'
  print("hello from python")
  EOF
  ```

- **A download's checksum is a SHA-256.** `download_file` takes it as 64
  hex characters, as `sha256sum` prints it, or the same value with a
  `sha256:` prefix: the server removes the prefix when it dispatches the
  command, from the API and from a playbook alike, so every agent receives
  the bare digest. Another algorithm or a value of the wrong length is
  refused before anything is sent. A download whose content does not match
  fails and leaves no file behind.
- **Saving a tracked file does not change the device.** Saving new content
  (the editor's save, `PUT /api/v1/nodes/{id}/files/content`) records a
  new version with the status `saved` and answers with its `id`; writing it
  to the device is the separate apply step
  (`POST /api/v1/nodes/{id}/files/{file_id}/apply` with that id), after
  which the version is `applying` until the agent reports.

`docs/DEVICE-COMMANDS.md` describes identify, setting the hostname, access
entries (the users and keys the platform puts on a device) and the whole
tracked-file sequence, with the API calls.

## Network boot

Network boot and the SD bundle both need the **recovery image**
(`boot.img` and its signature `boot.sig`) in `data/net_install`, and the
EEPROM signer needs its material.

**One recovery image serves every install.** It carries no server address
and no credential — a device learns its server from its bootloader or from
the adoption file on a provisioning card — so what is per install is only
the signature. A prebuilt image is published as a release of the separate
`orchestrator-boot-image` repository
(`https://github.com/uptime-lab/orchestrator-boot-image`), together with the
corresponding source its licences require; `tools/netboot/recovery-image.env`
in this repository names the image this platform release was tested
with, by address and SHA-256:

```sh
. tools/netboot/recovery-image.env
curl -fL -o boot.img "$RECOVERY_IMAGE_URL"
echo "$RECOVERY_IMAGE_SHA256  boot.img" | sha256sum -c -
```

Or build it yourself from that repository, whose README is the procedure:
a Buildroot build, run on an aarch64 Linux machine (not necessarily this
server), hours of build time and tens of gigabytes of disk. The agent the
recovery system runs is baked in during the build, with that repository's
`make fetch-agent VERSION=<agent version>`. Either way, signing the image with this
install's key, extracting the SD payload and populating `data/eeprom-build`
are one script each in `tools/netboot/` (so they need the clone, not the
compose file alone), and `docs/NETWORK-FIRST.md` → "Serving the recovery
image" is the procedure.

What the directories hold:

| Directory | Contents |
|---|---|
| `data/net_install/boot.img`, `boot.sig` | the signed recovery image the HTTP-boot endpoint serves per device; `boot.sig` is a three-line text file produced by `rpi-eeprom-digest`, not a raw signature |
| `data/net_install/sd-payload/` | the recovery image's boot partition, extracted, for the SD bundle |
| `data/net_install/eeprom/` | written by the signer: one package per device serial |
| `data/eeprom-keys/privkey.pem`, `pubkey.pem` | the RSA-2048 keypair whose public half is baked into every device's bootloader; it signs `boot.img` and every per-device package. Generate once (`docs/NETWORK-FIRST.md`, step 1). **Losing the private key means no rebuilt server can ever sign a boot image those devices accept** — escrow it (`docs/BACKUP-RESTORE.md`) |
| `data/eeprom-build/` | `rpi-eeprom/` — a clone of the Raspberry Pi `rpi-eeprom` repository, whose `firmware-2711/latest` and `firmware-2712/latest` are the firmware bases — and `tools/rpi-eeprom-config`, `tools/rpi-eeprom-digest` copied from it. `tools/netboot/update-eeprom-firmware.sh` creates and refreshes exactly that layout; a bare clone at `data/eeprom-build` is not it |
| `data/source-offer/` | the GPL corresponding-source archives for the recovery image, served at `/source/` (next paragraph) |

**Corresponding source.** The recovery image aggregates GPL-licensed software
(the kernel, BusyBox and more), and the GPL obliges whoever distributes it to
offer its source. An install whose devices are all its operator's own
distributes it to nobody else. **An install that serves the recovery image to
third parties** — other people's devices netbooting from it, SD bundles handed
to other people — **must set `SOURCE_OFFER_DIR`** and place in that directory
the two archives named in the image's own `SOURCE-OFFER.txt` (the source
bundle for the image's base and the toolchain source snapshot) together with
a `SHA256SUMS` beside them. The server then publishes them at `GET /source/`
with no token, which is the URL the notice inside the image points to; with
the variable unset the route answers 404 with a short notice. The compose
file mounts `data/source-offer` read-only at `/app/source-offer`, which is the
value to set — the server never writes there.

The signer bakes `PROVISIONING_BOOT_HOST` (default: `DOMAIN`) into each
device as the boot host, with a boot order that tries HTTP first, then NVMe,
USB and SD. Which device netboots and which boots its own disk is the
`boot_to_os` policy per node, unit or account; `docs/NETWORK-FIRST.md`
explains it, the boot-loop failsafe, and the reasons behind the design.

## Upgrading

A platform release is three images and a compose file built from one
commit, and meant to be deployed together. Every command below runs in the
install directory: the clone you made in "Get the files", or, for an install
the installer made, the directory it printed at the end — a clone of the
release tag at `--dir` (by default `orchestrator/` under the directory it
was started in), or, with `--source`, that directory itself, used in place.
`data/`, `secrets/`, `.env` and the `agent-release-<version>/` directories
in it are not release files; nothing below touches them.

**What changes for an existing install** is in `CHANGELOG.md`: read every
entry between your release and the new one before you start — new
settings, changed defaults, and what an upgrade does to data already
there. An upgrade can migrate the database to a schema the older release
refuses to open, so the way back is the restore of step 1, never starting
the older images (see the ledger below).

**1. Back up.** Run the dump from `docs/BACKUP-RESTORE.md` → "A scripted
nightly dump" once by hand, and check that the copies of `secrets/` and
`.env` that page asks for are current. Rolling back is restoring that dump:
`docs/BACKUP-RESTORE.md` → "Restore into the same install".

**2. Get the new release's files.** In a git checkout (the installer's is
one):

```sh
OLD=<installed version>    # PLATFORM_VERSION in .env
NEW=<new version>
git fetch --tags
# What changed in the compose file between the two releases: new secret
# files, new or renamed settings.
git diff "platform-v$OLD" "platform-v$NEW" -- docker-compose.community.yml
git checkout "platform-v$NEW"
# If you edited the compose file (the TLS-passthrough agent shape needs
# edits), checkout refuses; set the edits aside, switch, and re-apply them
# on the new file after reading the diff above:
#   git stash && git checkout "platform-v$NEW" && git stash pop
# The bundled proxy must be able to read proxy/ (see below):
chmod -R a+rX proxy
```

In a tree that is not a git checkout (an unpacked release), unpack the new
release beside it, compare, and copy it over. The archive holds release
files only, so `data/`, `secrets/`, `.env` and `agent-release-*/` stay as they
are; a compose edit of yours is overwritten and must be re-applied, and a
file the new release deleted stays behind unused:

```sh
NEW=<new version>
mkdir "../orchestrator-$NEW"
curl -fL "https://github.com/uptime-lab/orchestrator/archive/refs/tags/platform-v$NEW.tar.gz" \
  | tar -xz --strip-components=1 -C "../orchestrator-$NEW"
diff docker-compose.community.yml "../orchestrator-$NEW/docker-compose.community.yml"
cp -a "../orchestrator-$NEW/." .
rm -rf "../orchestrator-$NEW"
chmod -R a+rX proxy
```

**Either way, run that `chmod`** (with `sudo` if the files are not yours).
`git checkout` and `tar` write a changed file with your umask, and under a
restrictive one (`027`, `077`) `proxy/tls.yml` or `proxy/lan.yml` becomes
unreadable to the bundled proxy, which runs without the capability to
bypass file modes: it then serves no routes, and ports 443, 80 and 50051
stop answering. The installer does the same on a fresh install; the
command changes nothing on a tree that is already readable.

**3. Create any new credential file.** Every file the new compose
`secrets:` block names must exist, or Compose refuses to start and names it.
Create a missing optional one empty (the release notes say when a new one
needs a value):

```sh
sudo touch secrets/<name> && sudo chown 1000:1000 secrets/<name> && sudo chmod 0400 secrets/<name>
```

**4. Name the new release and start it.** `.env` is what names the release;
make sure no older value is exported in this shell.

```sh
sed -i "s/^PLATFORM_VERSION=.*/PLATFORM_VERSION=$NEW/" .env
unset PLATFORM_VERSION
docker compose -f docker-compose.community.yml pull
docker compose -f docker-compose.community.yml up -d
docker compose -f docker-compose.community.yml logs backend | grep -i "configuration loaded"
```

The images and the compose file move together: never run a release's
images with another release's compose file.

**Check the upgrade.** Every container is up, the backend migrated, and,
with a bundled proxy, the proxy reads its routes and answers:

```sh
docker compose -f docker-compose.community.yml ps --format '{{.Name}} {{.Status}}'
# every line says "Up", none "Restarting"
docker compose -f docker-compose.community.yml logs backend | grep -ci "refusing to start"
# 0
docker exec orchestrator-proxy sh -c 'cat /etc/traefik/routes/*.yml >/dev/null' && echo routes readable
# routes readable
curl -fsS -o /dev/null -w '%{http_code}\n' "https://$(sed -n 's/^DOMAIN=//p' .env)/api/health"
# 200 (on the plain-HTTP shape: http:// instead of https://)
```

`docker exec` runs as the proxy itself does (root without the capability
to bypass file modes), so a `Permission denied` there is what the proxy
meets: run the `chmod` of step 2 and `docker restart orchestrator-proxy`.

**The recovery image.** A release names the recovery image it was
tested with in `tools/netboot/recovery-image.env`, and `CHANGELOG.md`
says when that changes. An upgrade does not replace the image your install
serves: devices keep booting the one you signed. To take the release's
image on a network-boot install, download and check it as "Network boot"
below shows, then sign it and extract the SD payload
(`docs/NETWORK-FIRST.md` → "Serving the recovery image", steps 3 and 4).
The agent inside an older image updates itself on first contact, so this
is not urgent.

**5. The bundled proxy.** With `COMPOSE_PROFILES` naming `proxy-lan` or
`proxy-tls`, `up -d` recreates the proxy when its service changed, and the
proxy itself picks up a changed route file, because it mounts the whole
`proxy/` directory and watches it. If a compose file of your own mounts a
single file under `proxy/` instead, restart the proxy whenever `proxy/`
changed, or it keeps the old routes:

```sh
git diff --quiet "platform-v$OLD" "platform-v$NEW" -- proxy/ || docker restart orchestrator-proxy
```

The backend migrates the schema on start. **There is no downgrade across a
schema change**: roll back by restoring the backup from step 1
(`docs/BACKUP-RESTORE.md` → "Restore into the same install"), not by
starting an older image against a newer schema.

The database carries a **schema-version ledger**, the `schema_versions`
table: one row per backend build that started its migrations, with the
schema version, the build's commit, the version it upgraded from, whether
the migrations finished (`migrated`) and when.
Before it migrates anything, the backend reads the highest recorded version
and compares it with its own:

| The database records | The backend |
|---|---|
| no ledger (a new install, or one from before the ledger) | records its row, migrates, marks it migrated |
| an older version | records its row, migrates, marks it migrated — an upgrade |
| its own version, migration finished | starts; adds a row only the first time a new build of that version starts |
| its own version, migration NOT finished | migrates again (an earlier start of this version failed part-way) |
| a NEWER version | **refuses to start**, without touching the database |
| a ledger it cannot read | refuses to start (it does not guess) |

A refused start exits with a log line naming both versions:

```sh
docker compose -f docker-compose.community.yml logs backend | grep -i "schema"
# refusing to start: schema-version ledger check failed ...
#   database schema is version 3 but this build knows only version 2 ...
#   The database was NOT touched.
```

Seen from outside, the web page loads and every API call fails with 502
(`/api/health` included). It means an older image is running against a
database a newer release has already migrated. Either roll forward (check out the newer release's tag,
set `PLATFORM_VERSION` back to it and `up -d`), or restore the backup
taken before the upgrade and then start the older image. To see the ledger:

```sh
docker compose -f docker-compose.community.yml exec postgres \
  psql -U postgres -d orchestrator -c 'SELECT * FROM schema_versions ORDER BY id'
```

The row is written BEFORE the first migration, with `migrated = false`, and
marked `migrated = true` after the last one succeeded. So an upgrade that
fails part-way — some tables already changed — still leaves its row: the
next start of the same release reads it as unfinished and migrates again,
and an OLDER release is refused on it, as on any newer schema; roll forward,
or restore the backup taken before the upgrade. The refusal protects only
from releases that carry the ledger: an image older than the ledger does not
read it.

**Agents are upgraded from the server**, not by the package manager on the
device: the Nodes page's update action dispatches `update_agent`, which pulls
the package the server serves at `/cdn/agent/latest/<arch>/deb`, `<arch>`
being the device's own — `arm64`, the only architecture an agent package is
built for. A device already running the version it is sent reports success
without reinstalling or restarting; `reinstall: true`, accepted only with the
version the node runs, forces the reinstall. The server
enforces a **floor per command**: a command whose agent is older than the
version it requires is refused (or, if it was queued, settles as `skipped`),
so after a platform upgrade some actions may ask for an agent update first.
The floor table lives in the backend (`domain/node_command.MinAgentVersion`);
the release notes of each platform version say which commands moved. A new
agent release reaches the server the same way the first one did: "Import an
agent release" above, with `--set-latest`; moving a node back to an older
agent needs `allow_downgrade` (same section).

## Recover a lost password or authenticator

Self-service password reset needs a mail relay, and the recovery codes shown
at enrolment are the only self-service answer to a lost authenticator app. For
everything else the operator resets the user from the host, with the server
binary run inside the backend container — it then reads the same
configuration, database and credential files the server does:

```sh
# A forgotten password: sets a generated temporary password and prints it, once.
docker compose -f docker-compose.community.yml exec backend \
  /app/server --reset-password --email user@example.com

# Or choose the password: the first line of standard input. Never as an
# argument: the process list shows arguments to every user of the host.
docker compose -f docker-compose.community.yml exec -T backend \
  /app/server --reset-password --email user@example.com --password-stdin < new-password.txt

# A lost authenticator app (TOTP).
docker compose -f docker-compose.community.yml exec backend \
  /app/server --reset-totp --email user@example.com
```

- **The user is found by address**, case-insensitively, and the command acts
  on exactly one identity: an address nobody has, or one that two identities
  answer to, is refused and nothing changes.
- **`--reset-password`** writes the new password, ends every stored session
  of the user, and spends any reset link already mailed. Standard output
  carries the generated password and nothing else, so
  `PW=$(docker compose … exec -T backend /app/server --reset-password --email …)`
  captures exactly it; every message goes to standard error. The product has
  no "must change the password at next sign-in" flag: the temporary password
  works until the user changes it on their account page, so tell them to. A
  user who until now signed in only through an identity provider gains a
  password as well — and with it the two-factor mandate of their accounts.
- **`--reset-totp`** removes the user's authenticator app and its recovery
  codes and ends every stored session; the password is untouched. A user with
  no enabled authenticator is refused and nothing changes. **Passkeys are not
  touched**: a passkey is a complete sign-in on its own, not a second factor
  on top of the password, and the user deletes one on their account page.
  What the user meets at the next sign-in depends on their accounts: where
  two-factor is optional, the password alone signs in and they may enrol
  again; where an account requires it, a user with no passkey signs in with
  the password and is held on the enrolment screen until they register a new
  authenticator, and a user with a passkey is asked for the passkey after
  the password.
- **A user who lost both** gets both commands, password first.
- **Sessions**: every stored session is revoked at once and a login in
  progress (waiting for its second factor or its account choice) is refused.
  A browser that is already signed in keeps its short-lived access token until
  it expires (`JWT_ACCESS_TOKEN_TTL`, 15 minutes by default) — the same window
  "sign out everywhere" leaves — and is signed out at its next refresh.
  A sign-in that starts within the same second as the reset can be refused,
  or signed out at its first refresh: the stored change time is sub-second
  and a token's issue time is whole seconds. Signing in again works.
- **The audit trail** of every account the user is a member of — active or
  disabled — records the reset as done by the platform, with no password or
  secret in it. A user who is a member of no account has no trail to record
  it in: the command still exits 0 and says on standard error
  `WARNING: this user is a member of no account, so the reset is recorded in no audit trail; keep your own record of it.`
- **Exit status**: 0 done, 1 refused or failed (the reason is on standard
  error), 2 a usage error. The command migrates nothing and listens on
  nothing, so it is safe to run while the server is up. Like the server, it
  reads the schema-version ledger first and is refused, with nothing
  changed, when the database was migrated by a newer release than the
  image it runs from — run it from the same image as the server.
  `exec` needs a running backend: against one that is restarting in a loop
  (a refused start, "Upgrading") it answers `is restarting, wait until the
  container is running`. Run the command in a one-off container of the same
  image instead, which works whatever state the server is in:
  `docker compose -f docker-compose.community.yml run --rm --no-deps -T
  backend /app/server --reset-password --email user@example.com` (`-T`
  for a script or a shell without a terminal, as with `exec`). Anyone who can run
  `docker compose exec` on the host can run it — and could read the database
  anyway; that access is the boundary, as it is for the credential files.

## Uninstalling

**Devices.** Removing a device is two actions: removing the agent from the
device (`uninstall.sh`), and deleting its node on the server. Which comes
first depends on how you reach the device, because a deleted node takes no
commands, and the server refuses to delete a node whose device is connected
(online, in maintenance or restarting) unless the delete is *forced*.

The removal script, served by the server, public and tokenless so that it
works for a device already deleted from its account:

```sh
curl -fsSL https://orchestrator.example.com/uninstall.sh | sudo sh
```

It disarms the watchdog the agent may have armed first (a stopped agent
would otherwise reboot the machine), then stops and purges the agent package
and removes the adoption file and the agent's state (`/etc/orchestrator`,
`/var/lib/orchestrator-agent`). Like the install command it waits, saying
so, while another package operation holds the package manager; it checks
that the package is really gone, and when it is not it says why and exits 8
— run it again. SSH access the platform configured — keys, sudo grants, sshd
drop-ins — stays unless you pass `--purge-access` (`sh -s -- --purge-access`),
which is refused (exit 7) when no account that can still log in keeps its
own `~/.ssh/authorized_keys` — an account the platform locked does not count
(`--force` overrides it; `--dry-run` prints what
would change). OS users the platform created are left alone.

*A device you reach only through the server* (no shell of your own — a
remote site behind NAT, say). Remove the agent first, through a command, then
delete the node:

1. Send the removal as a script (*Run script* on the node's panel, or
   `POST /api/v1/nodes/{id}/command` with
   `{"command":"run_script","params":{"script":"…"}}`):

   ```sh
   cat > /root/orchestrator-uninstall.sh <<'EOF'
   LOG=/var/log/orchestrator-uninstall.log
   MARK=/root/orchestrator-uninstall.started
   if [ -e "$MARK" ]; then echo "$(date): already started once; not run again" >> "$LOG"; exit 0; fi
   touch "$MARK"
   sleep 15
   curl -fsSL https://orchestrator.example.com/uninstall.sh | sh >> "$LOG" 2>&1
   EOF
   setsid -f systemd-run --scope --unit=orchestrator-uninstall /bin/sh /root/orchestrator-uninstall.sh </dev/null >/dev/null 2>&1
   ```

   The removal stops the agent, and a process the agent started is stopped
   with it, so the script starts the removal in a scope of its own, outside
   the agent's service; the pause lets the command report back before the
   agent goes. It is a scope and not a service because systemd never
   restarts a scope, while a fresh system's first package upgrade restarts
   services, a transient one included, which would run the removal again from the
   top; the marker file stops a second start anyway, and the log is appended
   to, not overwritten. The script's own output is in the log file on the
   device, not on the server.
2. Wait for the node to go offline (the agent has stopped).
3. Delete the node. It is no longer connected, so the delete is an ordinary
   one (*Delete* on the panel, `DELETE /api/v1/nodes/{id}`).

*A device you also reach yourself* (a shell, a console): the same two
actions in either order; run `curl -fsSL https://orchestrator.example.com/uninstall.sh | sudo sh`
on the device yourself, with no wrapper.

*A device that is gone, broken or that you will not touch again:* delete the
node. If the server still sees it connected, the delete is forced: the
panel's *Delete* on a connected node says so and forces it, and the API needs
`DELETE /api/v1/nodes/{id}?force=true` (without it: 409). A forced delete
asks the agent to *release its identity*: an agent of 0.5.0 or newer erases
its adoption file and stays installed and running, holding no identity and
waiting to be adopted again; from then on it is refused by this server, and
a current agent does not fall back to a credential in the bootloader either.
The request is best-effort: the deleted-nodes list says it was requested, and
when the device confirmed it. A device deleted while offline keeps its
adoption file; if it comes back it is refused, and the deleted-nodes list
shows that a device presented the deleted node's identity (it can be
restored from there). Either way the agent stays on the device until you run
the removal script there. A released device is adopted again like a new one:
run the install command with a fresh token (a new adoption file lifts the
release), or reprovision it from a card or the browser.

*Moving a device to another server:* adopt it there first, then delete it
here. On the new server, take an install command for the device; a per-node
install link is a one-time code valid for an hour, which is what you want in
a command that is stored in this server's history (one from a server
reached over `http://` ends in `--insecure`; keep that flag). Run it on the device — through this server,
with the same kind of wrapper as above, since the install restarts the
agent. The code goes into a root-only file and the install reads it from
there (`--token-file`): a token on a unit's command line is written to the
system journal, which more accounts than root can read.

```sh
umask 077
printf '%s\n' '<code>' > /root/orchestrator-move.token
cat > /root/orchestrator-move.sh <<'EOF'
LOG=/var/log/orchestrator-move.log
MARK=/root/orchestrator-move.started
if [ -e "$MARK" ]; then echo "$(date): already started once; not run again" >> "$LOG"; exit 0; fi
touch "$MARK"
sleep 15
curl -fsSL https://new-server.example.com/install.sh | sh -s -- --token-file /root/orchestrator-move.token >> "$LOG" 2>&1
rm -f /root/orchestrator-move.token
EOF
setsid -f systemd-run --scope --unit=orchestrator-move /bin/sh /root/orchestrator-move.sh </dev/null >/dev/null 2>&1
```

Add `--insecure` after the token file when the new server's command carries
it. Should the install start a second time with the same code anyway, it
says the code was already exchanged on this device and finishes with the
adoption file it wrote then, instead of presenting a spent code.

The agent restarts with the new server's adoption file and connects there;
the node here goes offline. Then delete it here and forget it in the deleted
nodes list. A device provisioned for network boot also needs its bootloader
provisioned by the new server (*Update bootloader* on the new node's panel):
until then its bootloader still names this server and carries this server's
node credential.

**The bootloader keeps a credential.** A device provisioned for network boot
keeps this server's name and public key in its bootloader, and also its node
credential (`UPTIME_TOKEN` in the bootloader configuration), which stays
valid; uninstalling the agent changes none of it (`docs/NETWORK-FIRST.md`),
and the script says so when it finds that credential. To revoke it, delete
the node — the server refuses the credential of a deleted node — and then
forget the node in the deleted nodes list as well: while its record exists,
restoring it makes the same credential valid again.

**Accounts.** An owner's *Delete this account* files a request: it is
recorded for the operator (and in the account's audit trail), the account
keeps working, and nothing is erased automatically. This software has no
command that erases a single account; erasing one is the operator's manual
step. Removing the whole install, below, removes every account with it.

**The server.** Decide first whether you keep a last backup
(`docs/BACKUP-RESTORE.md`). A dump is readable only with the
`secrets_encryption_key` it was taken under, so keep the escrowed key as
long as the dump; and keep the EEPROM signing key for as long as any device
still has this install's public key in its bootloader — it is the only key
that can sign a boot image such a device accepts. **Both keys live in the
directories the first `rm -rf` below deletes** (`secrets/secrets_encryption_key`,
`data/eeprom-keys/privkey.pem`): if you have not escrowed them, copy them
off this host first. Then:

```sh
docker compose -f docker-compose.community.yml down --volumes --rmi all
sudo rm -rf data secrets     # or your DATA_PATH and SECRETS_DIR
```

`down --volumes` removes the containers, the networks and the two volume
definitions, but the volumes are bind mounts: the database and the storage
stay in `data/` until you delete it. `data/` and `secrets/` hold files owned
by uid 1000 and by the database container's user, with modes your own user
cannot read or delete, hence `sudo`. Remove the routes from your reverse
proxy last.

**What those two commands leave.** The install directory itself — the
release files, `.env`, the downloaded `agent-release-<version>/`
directories and, on an install the installer made, the `owner-password`
file with the owner's generated password, if it was not deleted after the
first sign-in — and the images of every release installed before the current
one, because `--rmi all` removes only the images the current compose file
names. To remove both:

```sh
docker image ls --filter 'reference=ghcr.io/uptime-lab/orchestrator/*'   # what is left
docker image ls --filter 'reference=ghcr.io/uptime-lab/orchestrator/*' \
  --format '{{.Repository}}:{{.Tag}}' \
  | xargs -r docker image rm
cd .. && rm -rf orchestrator     # the install directory, by its own name
```

Earlier releases' `postgres` and `traefik` images, if a release changed
their tags, are removed the same way by name.

## Troubleshooting

| Symptom | Likely cause | What to do |
|---|---|---|
| backend exits at once; log says `insecure configuration for production mode` | one of the five checks above | read the listed items; each names its variable |
| backend logs `failed to read a credential from its file; treating it as unset`, then exits on what that absence causes (a missing key fails the production checks; a missing database password fails the database connection) | a secret file is missing, or not readable by uid 1000, the uid the compose file runs the backend as (a 0400 file owned by root or by you) | `sudo sh -c 'chown 1000:1000 secrets/*'` (the glob must expand as root: the directory is 0700); files 0400, directory 0700 |
| `docker compose up` refuses the whole project naming a secret file | a declared secret file does not exist | run `--init-secrets` on an empty directory, or create the one file empty if unused |
| `--init-secrets` refuses, naming existing files | the directory already holds a set | it never overwrites; use the existing set, or remove the directory only if no server has ever booted on it |
| UI loads, every API call fails | the proxy does not strip the first `/api`, or forwards `/api` to the frontend | compare with the route table; `curl https://<DOMAIN>/api/health` must answer |
| sign-up says the invite code is wrong | a different value was seeded, or the file carries a trailing space or tab: only a trailing newline (`\n`, `\r\n`) is stripped, any other whitespace is part of the code | `sudo cat -A secrets/invite_code` and recreate the backend after any change |
| the first sign-up is refused with "registration is currently closed" | an account already exists (someone reached the page first), or the backend could not count accounts (database down) | check the Members page of the existing account, or `/api/ready` |
| second sign-up is refused with "registration is currently closed" | `secrets/invite_code` is empty (closed by default), or `SERVER_MAX_ACCOUNTS` reached (default 1) | invite colleagues into the existing account; set a code and raise the ceiling only for separate organisations |
| install script says `unsupported architecture` | the device is not arm64 | only 64-bit Raspberry Pi OS and the like are supported |
| agent installed but the node never appears; agent log shows reconnect attempts | the device cannot reach `<DOMAIN>:50051`, or TLS on that port is not terminated by your proxy | `openssl s_client -connect <DOMAIN>:50051` from the device must show your certificate |
| at one site, devices past a certain count never appear while the rest connect; the backend logs nothing about them | the site's devices share one public address (a NAT), and the proxy's per-address bound on agent connections is below their number | raise it above the site's fleet: `PROXY_AGENT_CONNECTIONS_PER_ADDRESS` for a bundled proxy, the `inFlightConn` amount in your own; see "The agent port" |
| at one site, HTTPS connections fail at the TLS handshake while the server is otherwise up; the proxy logs `max number of connections reached for <address>` | the site's browsers and devices share one public address, and together hold more HTTPS connections than the proxy's per-address bound | raise `PROXY_HTTPS_CONNECTIONS_PER_ADDRESS` in `.env`, then `docker compose -f docker-compose.community.yml up -d`; see "The agent port" |
| clients that reach the server over IPv6 fail at the TLS handshake (or devices on IPv6 never connect) while IPv4 clients are fine; the proxy logs `max number of connections reached for` a private IPv4 address | the compose network has no IPv6, so Docker's userland proxy forwards every IPv6 connection from the network's gateway address, and all IPv6 clients share one per-address pool | remove the AAAA record for `DOMAIN`, or give the compose network IPv6; see "IPv6 clients can all share one address" under "The agent port" |
| after an upgrade, every route is gone: HTTPS answers 404 or fails, network boot and agents fail too, and `docker logs orchestrator-proxy` shows `permission denied` reading a file under `/etc/traefik/routes` | the upgrade wrote `proxy/tls.yml` (or `lan.yml`) under a restrictive umask, so the file is not readable by others, and the proxy, which runs without the capability to bypass file modes, cannot read it | `chmod -R a+rX proxy` (with `sudo` if the files are not yours), then `docker restart orchestrator-proxy`; "Upgrading", step 2 |
| on the plain-HTTP shape, at one site, connections to port 80 are reset (pages fail to load, devices fail to boot over the network) while the server is otherwise up; the proxy logs `max number of connections reached for <address>` | the site's browsers and devices share one address, and together hold more connections to port 80 than the proxy's per-address bound | raise `PROXY_HTTP_CONNECTIONS_PER_ADDRESS` in `.env`, then `docker compose -f docker-compose.community.yml up -d`; see "The agent port" |
| browsers refuse `https://<DOMAIN>` and agents fail TLS on port 50051; the proxy presents a self-signed `TRAEFIK DEFAULT CERT` | the bundled proxy could not obtain its Let's Encrypt certificate: `DOMAIN` did not resolve to this machine from outside, or port 80 was closed to the internet, when it asked. It asks only when it starts | `docker logs orchestrator-proxy 2>&1 \| grep -i acme` names the reason; fix it first (Let's Encrypt stops trying a name for an hour after five failed validations), then `docker restart orchestrator-proxy` (an install the installer left waiting for its certificate: run the installer again) |
| no agent ever connects, the backend logs nothing about them, and the agents log connection errors | `AGENT_GRPC_PROXY_PROTOCOL=require` but the proxy sends no header (or the reverse): the connection is closed during the handshake, before any stream exists, so no log line names it | make the two sides agree; see "The agent port" |
| image upload fails after about a minute | proxy read/response timeout | raise or disable it on the HTTPS entry point |
| image upload fails with a disk error on a host with free space | `data/backend/storage` (including `tmp/`) not writable by the backend's uid — 1000 under the compose file's `user:` line — or `IMAGE_MAX_STAGING_BYTES` reached | `sudo chown -R 1000:1000 data/backend`, then `docker compose -f docker-compose.community.yml exec backend touch /app/storage/.w` must succeed; check the staging bound |
| compression of an uploaded image fails or the backend is OOM-killed | the xz bound and the container limit disagree | keep `IMAGE_MAX_CONCURRENT_COMPRESSIONS=1` on a 4 GB host; the boot log prints the memory arithmetic |
| the proxy logs `remote error: tls: bad certificate` on the agent port for a few seconds after a device boots, then the device connects | the device has no battery-backed clock and starts with a date in the past, so your certificate looks not yet valid until its clock is set from the network | nothing: the agent retries until the clock is right. A device that never gets the time (no NTP reachable) never connects over TLS |
| a device netboots from the SD card but never from the network | `/http-boot` is redirected to HTTPS, or served by something other than the backend | serve it on port 80 straight to the backend |
| a device was reset or lost power while *Install OS* was writing its disk | the half-written disk is not booted: the device comes back in network recovery on its next power-up | repeat the install from the node's panel — *Install OS*, then *Install Agent* ("By network boot"); nothing else needs undoing |
| a node shows `recovery` after a reboot that should have booted its disk | `boot_to_os` resolves to off for it — which a failed or interrupted OS install sets on purpose | set the policy once an install has succeeded; `docs/NETWORK-FIRST.md` |
| a node lost power or was reset during an OS install and stays offline | the disk holds a partial image. While the install is unsettled the server serves the node recovery on its next network boot, and once it is settled as failed it sets `boot_to_os` off; but a board that never asks (its boot order reaches the disk before HTTP, or it booted the partial disk before the server could answer) hangs where nothing reaches it. The compose keeps the boot-loop failsafe off (`PROVISIONING_BOOT_FAILSAFE_THRESHOLD=0`, the condition `docs/HTTPBOOT-AUTH-RESEARCH.md` rests on), so the platform does not bring such a node back by itself | power-cycle it; if it comes back on its disk again, make sure its `boot_to_os` is off and its boot order tries HTTP first, then power-cycle once more |
| a device flashed from the browser (WebUSB) never comes online as its node when it is first powered on | it was not powered on within the serial lease: a placeholder nothing has been heard from for `NODE_SERIAL_LEASE_DAYS` (default 30) loses its reservation of the device's serial; the node row stays | flash the device again from the node's panel, then power it on. Raise `NODE_SERIAL_LEASE_DAYS` before flashing devices that will wait longer than that |
| reboot to recovery answers 409 | the node is offline (`node_unavailable`), or its boot order never reaches HTTP boot (`recovery_unreachable`); nothing was changed | bring the node online, or provision its EEPROM for network boot first |
| after going back to an older release, the backend restarts in a loop; the web page loads but every API call (`/api/health` too) fails with 502 | the newer release had already migrated the database, and the older backend refuses a database newer than itself; the reason is only in its log: `docker compose -f docker-compose.community.yml logs backend \| grep -i schema` | roll forward to the newer release again, or restore the backup taken before the upgrade and start the older release on it; "Upgrading" |
| `/api/ready` answers 503 while `/api/health` is fine | database down, or under 256 MiB free on the storage volume | the log names which |
| password reset by email answers 503 | no mail relay configured | `docs/EMAIL.md`; reset it from the host meanwhile ("Recover a lost password or authenticator") |

## Using the API

The web UI is a client of the same HTTP API a script can call. **The API's
description is `backend/docs/swagger.json`** (and `swagger.yaml`) in the
release tree — the install directory holds it. A production install does not
serve it: `/swagger/` there is the web UI's own page.

Through the bundled proxy, and any proxy built to the table in "HTTP
routes", the API is under `/api`, and the proxy strips that one `/api`. So a
route the description lists as `/auth/login` is
`https://<DOMAIN>/api/auth/login`, and one listed as `/api/v1/nodes` is
`https://<DOMAIN>/api/api/v1/nodes` (`http://` on the plain-HTTP shape).

**Sign in** with `POST /api/auth/login`:

```sh
curl -fsS -X POST https://orchestrator.example.com/api/auth/login \
  -H 'Content-Type: application/json' \
  -d '{"email":"owner@example.com","password":"<password>"}'
```

(A password in a command's arguments is visible to every user of the host in
the process list; on a shared host write the body to a file only you can
read and pass `-d @<file>`.) A 200 carries `data.access_token`, valid for `JWT_ACCESS_TOKEN_TTL` (15
minutes by default) and sent as `Authorization: Bearer <access token>`, and
`data.refresh_token`, which `POST /api/auth/refresh` with
`{"refresh_token": "…"}` exchanges for the next pair. A 202 means the
sign-in is not finished: `two_factor_required` carries a
`two_factor_token`, completed by `POST /api/auth/2fa` with
`{"two_factor_token": "…", "code": "<authenticator code>"}`; "account
selection required" (a user who is a member of several accounts) carries a
`pre_token` and the `accounts`, completed by `POST /api/auth/select-account`
with `{"pre_token": "…", "account_id": "…"}`.

**Some routes need a recent re-authentication**: the ones that change who
can sign in or what a member may do — creating or re-sending an invitation,
changing a member's roles or status, removing a member, the role-permission
settings, enrolling an authenticator app or a passkey, changing the password
or the email address, applying an entitlement file, linking an identity
provider. The description names the `X-Reauth-Ticket` header on each of
them. Without a valid ticket such a route answers 403 with
`"code": "reauth_required"`; a caller whose role does not allow the request
is refused for that first, without being asked. To get a ticket, prove the
password again:

```sh
curl -fsS -X POST https://orchestrator.example.com/api/api/v1/users/me/reauth/password \
  -H "Authorization: Bearer <access token>" -H 'Content-Type: application/json' \
  -d '{"password":"<password>"}'
# {"data":{"ticket":"…","expires_in":300},…}
```

— or a current authenticator code (`POST /api/api/v1/users/me/reauth/totp`
with `{"code": "…"}`; a recovery code works too), or a passkey
(`/reauth/passkey/begin`, then `/reauth/passkey/finish?ticket=…` with the
authenticator's answer). `GET /api/api/v1/users/me/reauth/methods` says
which the user can use. Send the ticket as `X-Reauth-Ticket: <ticket>` on the
request that needs it. It is good for five minutes and for several requests,
only for the user and the signed-in session that earned it: a sign-out, a
"sign out everywhere" or a password change ends it. A user with nothing at
all to prove themselves with is not asked.

The authenticated API is rate-limited per account
(`API_RATE_LIMIT_PER_ACCOUNT_RPS`, `docs/CONFIGURATION.md`), and the
sign-in routes per client address.

## Known limitations

These are properties of the current code, stated so that nobody spends a
day on them; none is a documentation gap.

- **The agent port defaults to PROXY protocol off.** The community compose
  defaults `AGENT_GRPC_PROXY_PROTOCOL` to `off` so that any reverse proxy
  works; the per-source bound on unauthenticated agent streams is then
  inert, and only the global bound protects the listener. Switch to
  `require` when your proxy can send the header.
- **The boot host is baked into every device's bootloader.** Once a device
  has been provisioned for network boot, the server's name cannot change
  without re-flashing that device — a restore onto a host with a new name
  does not bring those devices back (`docs/BACKUP-RESTORE.md`).
- **Managed devices are arm64 only.** The install script refuses other
  architectures, and there is no amd64 agent package.
- **`backend/.env.example` is for development.** It carries placeholder
  secrets with `ALLOW_INSECURE_DEV_SECRETS=true` beside them — the opt-in a
  development mode needs to boot on such values at all, and one that
  production ignores; it is not the environment file for this compose,
  which takes its credentials from the files `--init-secrets` writes.
- **The recovery image is a separate download.** A prebuilt `boot.img` is
  published by the `orchestrator-boot-image` repository, or built from it
  (see "Network boot"); signing it with your key, the SD payload and
  `eeprom-build` are the `tools/netboot/` scripts and the procedure in
  `docs/NETWORK-FIRST.md` → "Serving the recovery image" — or the
  installer, which does all of it. Install-link adoption works without any
  of it.
- **The HTTP-boot path identifies a device by its serial alone.** The
  design and the accepted risk are in `docs/HTTPBOOT-AUTH-RESEARCH.md`; the
  compose keeps the boot-loop failsafe off, which is the condition that
  acceptance rests on. `docs/SECURITY-ACCEPTANCES.md` argues it, and the
  other accepted risks, for a self-hosted install.
- **One replica.** Several bounds (upload admission, the capture reservation,
  pending-login tickets) live in the process; two backend replicas behind
  one proxy are not supported.
