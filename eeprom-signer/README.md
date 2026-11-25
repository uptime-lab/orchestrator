# eeprom-signer

Isolated container that produces **per-device signed Raspberry Pi EEPROM images**
for the network-first onboarding (Variant B, see `docs/NETWORK-FIRST.md`). It is
the **only** component that holds the private signing key.

The RPi bootloader does not substitute the serial into `HTTP_PATH` (it is a literal
path; serial substitution is TFTP-only). So per-device serve/withhold control
requires each device to carry its own real serial in `HTTP_PATH=http-boot/<serial>`.
This service bakes that serial + our pubkey (deliberately **without** `SIGNED_BOOT`
— see `docs/NETWORK-FIRST.md`; netboot still RSA-verifies `boot.sig` against the
embedded pubkey, while an unsigned local OS can still boot) + adoption token into a
signed `pieeprom.upd` and emits the recovery-flash package.

## API (signer-only network — reachable from the backend and nothing else)

`GET /health` → `{"ok": true}`

`POST /sign` (JSON):
```json
{ "soc": "cm5", "serial": "10000000abcd1234", "http_host": "192.0.2.10",
  "adoption_b64": "<base64 node adoption>", "boot_order": "0xf1467",
  "node_id": "<node uuid>" }
```
→ writes `recovery.bin, pieeprom.upd, pieeprom.sig, SHA256SUMS` to
`OUTPUT_ROOT/by-serial/<serial>/<node_id>/` and returns:
```json
{ "ok": true, "serial": "...", "soc": "cm5", "boot_order": "0xf1467",
  "path": "eeprom/by-serial/<serial>/<node_id>", "files": ["recovery.bin", ...] }
```
`node_id` (a canonical lowercase UUID) is optional for compatibility; without it
the package lands in `by-serial/<serial>/`, which the current backend never serves.
The package carries ONE node's adoption token and a short serial is not unique, so
it is keyed on the node: the backend's download capability names the node and the
handler serves only that node's directory. Deploy this signer BEFORE a backend that
sends `node_id`; that backend refuses a bake whose returned `path` is not node-scoped.
**Every `/sign` field is validated before the signer runs.** `soc` is `cm4|cm5`,
`serial` is 8-16 hex, `boot_order` is `0x` + 1-8 hex (`^0x[0-9a-fA-F]{1,8}$`), and
`http_host` is a hostname or IPv4 literal (`^[A-Za-z0-9.-]{1,253}$`, no
leading/trailing dot), and `adoption_b64` is padded standard base64 of at most
1536 characters (it becomes the `UPTIME_TOKEN` line, and a line break in it
would add configuration lines under the signature); a malformed value is a 400. The values reach `sign.sh`,
which bakes them into the EEPROM config beside the private key, so nothing
unvalidated may get there. `sign.sh` passes every value as data (a python3
rewrite reading key/value from the environment, never interpolated into a
program) and re-guards `boot_order`, `http_host`, `serial` and `adoption_b64` itself;
the backend's client refuses a malformed adoption blob before it calls. Tests: `eeprom-signer/tests/`
(stdlib `unittest`), `make eeprom/test`.

`DELETE /by-node/<node_id>` (same token) is what the operator's account purge
calls: it removes `by-serial/*/<node_id>/` under EVERY serial directory and
answers `{"deleted": <bool>, "serials": [<serial directories it removed from>]}`
— `deleted:false` with an empty list when nothing was there, which the purge
reports as ABSENT, never as deleted. It exists because the serial a package was
baked under is not necessarily the one the node carries at purge time: a WebUSB
flash bakes under the SHORT serial the browser read, and the node is rebound to
its FULL serial on first connect. Node ids are unique, so every match is that
node's; each match is contained by realpath, a symlink is never followed or
removed, and the route is idempotent.
`DELETE /by-serial/<serial>/<node_id>` removes ONE node's package under one
named serial. Node ids are unique, so that directory belongs
to exactly the named node; an absent one answers 200 `deleted:false`.
`DELETE /by-serial/<serial>` removes only the LEGACY remainder — files directly
under the serial directory, written before packages were keyed on the node — and
the directory once empty. **It never removes a node subdirectory**: while any
remain it answers 409 `{"remaining": <count>}` (the legacy files are gone by
then), because a short serial is shared and the directory can hold another
tenant's live package. Both validate `serial` and `node_id` as `/sign` does,
contain every path by realpath, and remove a symlink as the link, never its
target. The caller keeps the cross-tenant guard for the legacy remainder (a
serial a live node still owns is never asked for). A caller of `/by-node/` needs
a signer that has the route: an older signer answers it 404, which the caller
must report as a FAILED package deletion, never as absent.

`GET /base` → the hosted firmware base (proxied by the backend for the UI's
"Up-to-date" badge). `GET /bootcode?soc=cm4|cm5` → the raw bootcode blob.

**Auth is REQUIRED and fail-closed.** `server.py` refuses to start without a token
(`FATAL: SIGNER_TOKEN is unset`, exit 1), and the comparison against
`Authorization: Bearer <token>` is constant-time. "Internal network" is not an
authorization boundary — every container on a shared bridge could otherwise reach :9099 and
mint signed packages.

Supply the token as **`SIGNER_TOKEN_FILE`**, a mounted Docker secret, not as
`SIGNER_TOKEN` in the environment (a container's whole environment is exposed by
`docker inspect` and `/proc/<pid>/environ`). A named-but-unreadable file yields empty and
does **not** fall back to the environment — deliberately, so a rotation that did not take
effect stops the signer instead of leaving it on the old value.

The token gates **`POST /sign` and the `DELETE /by-serial/…` routes** (an
operator's account purge removes a node's package through them). `GET /health`, `GET /base` and
`GET /bootcode` stay open on purpose: they expose no key material and the backend polls
`/base` for a badge.

## Mounts (nothing secret is baked into the image)

- `/keys` (ro) — `privkey.pem` + `pubkey.pem`
- `/eeprom-build` (ro) — rpi-eeprom clone (`rpi-eeprom/firmware-27{11,12}/latest/`)
  + `tools/rpi-eeprom-config`, `tools/rpi-eeprom-digest`
- `/output` (rw) — the `net_install/eeprom` directory the backend serves at
  `/net_install/eeprom/…`, only against a signed `?exp=&sig=` capability, so the
  agent can download its own package by serial. Never serve this directory from a
  static file server: it cannot check the capability (`docs/NETWORK-FIRST.md`)

## Compose service (internal only — never routed)

Two properties of the block below are load-bearing. The token is not an optional
setting: a signer started without one exits, and a signer "fixed" by dropping the
token is an unauthenticated signing oracle reachable from every app container. And
the signer does **not** sit on `orchestrator-network`: it has its own internal network
shared with the backend and nothing else that faces the internet, so a compromise of
the internet-facing containers cannot reach the signing oracle. A service that deletes
an account's packages through the signer belongs on that network too. The shipped
`docker-compose.community.yml` is authoritative; this is an illustration.

Whatever reverse proxy fronts the install, the signer must not get a route.

```yaml
  eeprom-signer:
    build: ./eeprom-signer
    container_name: orchestrator-eeprom-signer
    restart: unless-stopped
    security_opt:
      - no-new-privileges:true
    cap_drop:
      - ALL
    pids_limit: 128
    environment:
      - HTTP_HOST=${PROVISIONING_BOOT_HOST}
      - BOOT_ORDER=0xf1467
      - SIGNER_TOKEN_FILE=/run/secrets/eeprom_signer_token
    secrets:
      - eeprom_signer_token
    volumes:
      - ${DATA_PATH}/eeprom-keys:/keys:ro
      - ${DATA_PATH}/eeprom-build:/eeprom-build:ro
      - ${DATA_PATH}/net_install/eeprom:/output   # served at /net_install/eeprom
    networks:
      - orchestrator-signer-network              # NOT orchestrator-network
```

Where the secret value itself comes from, use `${VAR:-}` and never `${VAR:?}`: an unset value
must fail only THIS container — `server.py` exits 1 without a token — rather than every
compose invocation for every service.

The container runs as **uid 1000** (`USER 1000:1000` in the Dockerfile) — nothing here needs
root, and 1000 is what `/output` is chowned to on the host. It is deliberately **not**
`read_only`: rpi-eeprom's tooling writes scratch files next to its inputs while baking, and
`/output` is the only writable mount.

## Integration (Variant B flow)

1. Backend, when a node needs network-first provisioning, calls
   `POST http://eeprom-signer:9099/sign` with the node's serial + SoC + its
   node id + that node's OWN adoption blob (the backend's `GetAdoptionInfo` for the node), never a
   master token.
2. The signer writes the package under `net_install/eeprom/by-serial/<serial>/<node_id>/`.
3. The agent downloads `recovery.bin`/`pieeprom.upd`/`pieeprom.sig` from
   `<platform>/net_install/eeprom/by-serial/<serial>/` with the node-bound
   capability, which the backend maps to that node's directory, writes them to the FAT boot
   partition, and reboots; the ROM flashes the EEPROM.

The private key never leaves this container; the backend and CDN never see it.
