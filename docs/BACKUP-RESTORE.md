# Backup and restore

> **What has been exercised.** By this page: the scripted dump, run by hand;
> "Restore into the same install", step by step; and
> "Restore onto an empty host" — a second, empty machine rebuilt from the
> dump under another name, with the restored rows decrypting, the entitlement
> and the signing key intact, and the recovery image's signature verifying.
> **Not yet exercised:** a device reconnecting to a restored server, and a
> restore while a key rotation is in progress. Run a restore against a
> scratch host before you rely on it (the "Verification checklist" at the
> end).

A database dump of Uptime Orchestrator is **not a backup on its own**. The
schema is full of ciphertext — secrets, provisioning passwords, tracked file
contents, command payloads — and the keys that read it live beside the
database on the same host. Restore the dump without them and you get a
working server nobody can read anything from. This page says what to copy,
how to dump, how to restore in the order that works, and what a restore
cannot bring back.

## What must be backed up

Ranked by what is unrecoverable when it is lost:

| Item | Where it lives (community compose) | If lost |
|---|---|---|
| **The EEPROM signing key** `privkey.pem` | `data/eeprom-keys/` | its public half is baked into every network-booting device's bootloader; no rebuilt server can ever sign a boot image those devices accept. Recovery is a physical re-flash, per device |
| **`SECRETS_ENCRYPTION_KEY`** | `secrets/secrets_encryption_key` | every encrypted row dies with it: stored secrets, provisioning passwords, tracked files, command history. The dump restores, and decrypts nothing |
| **The backup encryption key** | wherever you keep it — not on the server | without it the dumps you shipped off-host are blobs |
| **The database** | the `postgres` volume (`data/postgres`) | everything else: accounts, nodes, policies, playbooks, audit trail |
| **The storage volume** | `data/backend/storage/` on local storage, or the object-storage bucket | uploaded and captured OS images — each a full image or a device's whole disk, with no other copy — and, under `agent-artifacts/`, the agent packages and their `.sig` files the server hands to devices (re-importable from the agent's releases, `docs/INSTALL.md` → "Import an agent release"). Its `tmp/` is upload staging and need not be kept |
| **The other credential files** | `secrets/` | `db_password` (must be the one the roles dump was taken with: restoring that dump puts the old password back into the database, see "Restore onto an empty host"); `secrets_encryption_key_old` (non-empty only during a key rotation, and then every row not yet re-encrypted is readable only with it); `jwt_secret` (a new one signs everyone out, nothing else); `cdn_signing_key` (a new one invalidates capabilities minted in the last minutes); `eeprom_signer_token` (shared by the backend and the signer only; a new one works as soon as both restart); `invite_code` (a new one replaces the code you handed out); `oidc_client_secret`, `s3_access_key`, `s3_secret_key`, `smtp_password` (re-issuable from the identity provider, the bucket's provider and the mail relay; until then sign-on, the bucket and mail fail); `entitlement` (the boot-time entitlement file; a file pasted in the UI lives in the database and comes back with it, but an install whose only file was this one runs at the free device cap until the file is applied again) |
| **`.env`** | beside the compose file | `DOMAIN`, `PLATFORM_VERSION` and your overrides; cheap to retype, easy to get subtly wrong |
| **The bundled proxy's certificate state** | `data/proxy/acme.json`, with the `proxy-tls` proxy | the Let's Encrypt account and the certificate issued for `DOMAIN`. Lost, the proxy asks for a new certificate when it starts, which works — within Let's Encrypt's rate limits (five certificates for the same name a week): a host rebuilt several times in a week can be refused one and serve its default certificate, which browsers and agents reject, until the limit clears. The file holds the account's private key: restore it with mode 0600 |
| **The source offer** | `data/source-offer/` | only on an install that serves the recovery image to third parties: the GPL corresponding-source archives `/source/` publishes. Re-obtainable from the recovery image's build, but the offer must stay answerable while you do |

Not on the list, on purpose:

- **The agent trust store is in the code.** The public keys that devices and
  the install script verify agent packages against are embedded in the agent
  and backend binaries; a fresh image carries them.
- **`data/net_install/` and `data/eeprom-build/`.** `net_install` is
  re-created from the recovery image and the signing key (sign, extract the
  SD payload; the per-device EEPROM packages are re-baked on demand), and
  `eeprom-build` is a re-cloned public repository, independent of any key
  (`docs/NETWORK-FIRST.md` → "Serving the recovery image"). Back them up for
  convenience, not for safety.
- **A reverse proxy of your own**: its certificates are re-issued (the
  bundled one's are `data/proxy/acme.json`, above).
- **`data/.installer/`**, on an install the installer made: its own record
  of the run (the release and the agent version it installed, and a
  `complete` marker once it finished), which is what makes a later run of
  the installer refuse the directory as a finished install. A restore does
  not need it: without it, the installer still refuses a directory that
  holds a `.env` or credential files it did not write.
- **Docker images** are pulled again by version.

## The dump without the keys is not restorable

Say it to whoever keeps the backups: a nightly dump in object storage with
the keys on the server it came from protects against a bad `DELETE`, not
against losing the host. The keys are small and change rarely; the right
place for them is a password manager or an escrow the operator controls,
**separate from the backup location**, so that neither store alone is
enough to read the data. The dumps hold ciphertext, the escrow holds the
keys; an attacker needs both, and so does a restore.

The two keys that cannot be regenerated — the EEPROM signing key and
`SECRETS_ENCRYPTION_KEY` — go into the escrow on the day the install is
created, before the first device is adopted. The rest can join them.

## A scripted nightly dump

The recipe below uses generic paths. `DBUSER` and `DBNAME` are the database
role and database the community compose file uses: `postgres` and
`orchestrator`, its defaults, unless your `.env` sets `DB_USER` or `DB_NAME`
(one the installer wrote sets neither). `docs/CONFIGURATION.md` lists the
backend's own default for `DB_NAME`, which the compose file overrides — take
the compose file's, or `docker compose -f docker-compose.community.yml
config | grep -E 'DB_(USER|NAME)'` to see the values in force.
It runs under `bash` with `pipefail` on purpose: under `sh` a failing
`pg_dump` still lets `gzip | openssl` exit 0, so a truncated dump is written,
reported as success, and rotation then deletes the good backups behind it. It
writes to a `.part` file and promotes only after the artefact decrypts,
decompresses in full and contains a real dump; rotation runs only after a
verified new backup exists.

```bash
#!/bin/bash
# Nightly encrypted PostgreSQL dump with 14-day rotation.
set -euo pipefail
DIR=/var/backups/orchestrator            # local backup directory, mode 0700
KEY=/root/.orchestrator-backup-key       # a random passphrase file, mode 0600; ESCROWED elsewhere
CONTAINER=orchestrator-postgres
DBUSER=postgres                          # the compose default; DB_USER if .env sets it
DBNAME=orchestrator                      # the compose default; DB_NAME if .env sets it
TS=$(date -u +%Y%m%d-%H%M%S)
install -d -m 700 "$DIR"                 # created on the first run, kept 0700
OUT="$DIR/orchestrator-$TS.sql.gz.enc"
PART="$OUT.part"
trap 'rm -f "$PART"' EXIT

docker exec "$CONTAINER" pg_dump -U "$DBUSER" -d "$DBNAME" \
  | gzip \
  | openssl enc -aes-256-cbc -pbkdf2 -salt -pass "file:$KEY" > "$PART"

# Verify the WHOLE artefact before promoting it: decrypt and decompress to
# /dev/null (a truncated stream fails at gzip's trailer), then a content check.
verify_stream() {
  openssl enc -d -aes-256-cbc -pbkdf2 -pass "file:$KEY" -in "$1" 2>/dev/null | gunzip > /dev/null 2>&1
}
head_of() {
  set +o pipefail   # `head` closing the pipe is not a failure
  openssl enc -d -aes-256-cbc -pbkdf2 -pass "file:$KEY" -in "$1" 2>/dev/null | gunzip 2>/dev/null | head -c 100000
  set -o pipefail
}
verify_stream "$PART" || { echo "backup FAILED full decrypt/gunzip, not promoting: $PART" >&2; exit 1; }
head_of "$PART" | grep -q "PostgreSQL database dump" || { echo "backup FAILED verification: $PART" >&2; exit 1; }

chmod 600 "$PART"
mv "$PART" "$OUT"
trap - EXIT
find "$DIR" -name "orchestrator-*.sql.gz.enc" -mtime +14 -delete
echo "backup ok: $OUT ($(stat -c%s "$OUT") bytes)"

# Cluster roles, in a SUBDIRECTORY so that an off-host shipper selecting the
# newest dump by glob cannot pick the roles file instead of the data.
# A single-database dump carries GRANTs but no CREATE ROLE; a restore onto a
# fresh cluster then fails every grant to a role that does not exist.
ROLES_DIR="$DIR/roles"; mkdir -p "$ROLES_DIR"; chmod 700 "$ROLES_DIR"
ROLES_OUT="$ROLES_DIR/orchestrator-roles-$TS.sql.gz.enc"
docker exec "$CONTAINER" pg_dumpall -U "$DBUSER" --roles-only \
  | gzip \
  | openssl enc -aes-256-cbc -pbkdf2 -salt -pass "file:$KEY" > "$ROLES_OUT.part"
verify_stream "$ROLES_OUT.part" || { echo "roles dump FAILED: $ROLES_OUT.part" >&2; rm -f "$ROLES_OUT.part"; exit 1; }
head_of "$ROLES_OUT.part" | grep -q "CREATE ROLE" || { echo "roles dump has no CREATE ROLE" >&2; rm -f "$ROLES_OUT.part"; exit 1; }
chmod 600 "$ROLES_OUT.part"; mv "$ROLES_OUT.part" "$ROLES_OUT"
echo "roles ok: $ROLES_OUT"
```

Run it from a systemd timer (`OnCalendar=*-*-* 03:30:00 UTC`,
`Persistent=true`) or cron, as root. It creates `DIR` (mode 0700) on its
first run. Create the key once, before that run, and put a copy in the
escrow immediately:

```sh
sudo sh -c 'umask 077 && set -C && openssl rand -hex 32 > /root/.orchestrator-backup-key'
```

`umask 077` creates the file 0600 rather than readable by everyone first,
and `set -C` refuses to overwrite a key that already exists. The script
never creates the key, because a freshly generated key would silently orphan
every existing backup.

**Ship the dump off the host.** The local directory survives a bad deletion,
not a lost disk. Copy the newest dump, its roles file and — once, and again
whenever they change — the key material to a remote (object storage, a
drive, another machine) with `rclone`, `rsync` or the provider's tool. Keep
the backup encryption key out of that remote. A bundle that carries the
keys together with the dump is convenient and is safe **only** because it is
encrypted with a key that is not in it.

**Images are separate and large.** `data/backend/storage/` (or the bucket)
holds every uploaded and captured image; sync it on its own schedule with
an incremental tool, or accept that a lost host loses them. Decide which,
and write the decision down.

## Restore onto an empty host

The order matters. The keys go in **before** the backend ever starts: a
backend started against an empty database creates the schema and writes its
first rows under whatever key it finds, and a key placed afterwards reads
none of them.

1. **Prepare the host by hand, and start nothing.** Follow `docs/INSTALL.md`
   from "What you need" through "Put a reverse proxy in front", with the
   release the dump was taken from (its `PLATFORM_VERSION`) and the same
   `DOMAIN`, and create the data directories as "Get the files" does — but
   **skip "Create the credentials"** (step 2 puts the backed-up ones in
   place), take `.env` from the backup rather than writing a new one, and
   do not run "Start the stack". The installer does not fit this step: it
   always generates new credentials, starts the stack and creates an owner
   in a new database, which is everything a restore must not have.

   **If the new host cannot carry the same name** (another DNS name, or
   another shape: an internet install restored onto a local-network machine,
   or the reverse), edit the restored `.env` before step 5, and keep the
   original beside it. Set `DOMAIN` to the new name or address and
   `COMPOSE_PROFILES` to the proxy that fits (`proxy-tls` or `proxy-lan`);
   for the plain-HTTP shape remove `ACME_EMAIL` and add the settings of
   `docs/INSTALL.md` → "The agent port", item 3, and do not restore
   `data/proxy/acme.json`. The data restores the same either way. The
   devices do not follow: see "What a restore cannot bring back".
2. **Place the key material.** Restore `secrets/` (all of it — a missing
   declared secret file stops Compose) with the modes INSTALL.md gives,
   `data/eeprom-keys/`, and `.env`. The files must arrive owned as they
   were (the backend reads `secrets/` as uid 1000): carry them in an archive
   made and unpacked as root with `tar --numeric-owner -p`, or `chown` them
   afterwards; a copy made by an ordinary user leaves files the backend
   cannot read. Compare each key with the escrow copy,
   not with memory. **`db_password` must be the file from the same backup
   as the roles dump**, not a freshly generated one: the roles dump sets the
   database user's password back to the hash it was taken with, so a new
   `db_password` would let Postgres start and the backend fail to log in.
   Put the backup material where the nightly script keeps it, which is
   where step 3 reads it: the backup encryption key from the escrow at
   `/root/.orchestrator-backup-key` (mode 0600), and the dump and the roles
   file of the same run (the same `<ts>` in both names) from the off-host
   copy into `/var/backups/orchestrator/` and
   `/var/backups/orchestrator/roles/` (mode 0700 directories, 0600 files,
   owned by root).
3. **Start PostgreSQL alone, then restore roles, then data.** The paths are
   the nightly script's (`KEY`, `DIR` and its `roles/` subdirectory); `ls
   /var/backups/orchestrator/` (as root) lists the dumps by timestamp. They
   are readable by root only, hence `sudo openssl`.

   ```sh
   docker compose -f docker-compose.community.yml up -d postgres
   DBUSER=postgres DBNAME=orchestrator      # the compose defaults; DB_USER / DB_NAME if .env sets them
   KEY=/root/.orchestrator-backup-key
   TS=<ts>                                  # the run to restore, e.g. 20260101-033000
   DUMP=/var/backups/orchestrator/orchestrator-$TS.sql.gz.enc
   ROLES=/var/backups/orchestrator/roles/orchestrator-roles-$TS.sql.gz.enc
   # Wait for the server proper. On its first start the image initialises
   # the cluster behind a temporary server that listens on no TCP port, and
   # a restore sent to that one is cut off when it stops.
   for i in $(seq 60); do
     docker exec orchestrator-postgres pg_isready -q -h 127.0.0.1 -U "$DBUSER" -d "$DBNAME" && break
     sleep 2
   done
   # The database must be empty: this prints 0, or stop here.
   docker exec orchestrator-postgres psql -U "$DBUSER" -d "$DBNAME" -tAc \
     "SELECT count(*) FROM pg_tables WHERE schemaname = 'public'"
   sudo openssl enc -d -aes-256-cbc -pbkdf2 -pass "file:$KEY" \
     -in "$ROLES" | gunzip \
     | docker exec -i orchestrator-postgres psql -U "$DBUSER" -d postgres
   sudo openssl enc -d -aes-256-cbc -pbkdf2 -pass "file:$KEY" \
     -in "$DUMP" | gunzip \
     | docker exec -i orchestrator-postgres \
         psql -v ON_ERROR_STOP=1 --single-transaction -U "$DBUSER" -d "$DBNAME"
   ```

   The data step stops at its first error, exits non-zero and leaves the
   database as it was, because it runs as one transaction; a restore into a
   database that already holds tables fails this way instead of printing
   errors and exiting 0 with nothing restored. The roles step prints
   `ERROR: role "postgres" already exists` (or the
   name of your `DB_USER`): the container created that role at first start,
   and the error is expected. Any other error is not. The `ALTER ROLE` that
   follows it restores that role's old password, which is why step 2 restores
   `db_password` with the dump. The database name is the
   `DB_NAME` of the install (`orchestrator` by default); a dump restored into
   a differently named database is a database the backend never opens.
4. **Restore the storage.** Copy the whole storage volume back into
   `data/backend/storage/` — images and `agent-artifacts/` — owned by uid
   1000, or point the backend at the bucket with the same credentials.
   Restore `data/net_install/`, `data/eeprom-build/` and `data/source-offer/`
   if you have them; otherwise re-create the first two as
   `docs/NETWORK-FIRST.md` → "Serving the recovery image" describes. With
   the `proxy-tls` proxy, restore `data/proxy/acme.json` (mode 0600) too, so
   the proxy starts with its certificate instead of asking for a new one.
5. **Start the rest and verify.**

   ```sh
   docker compose -f docker-compose.community.yml up -d
   docker compose -f docker-compose.community.yml logs backend
   curl -fsS https://<DOMAIN>/api/ready     # an install reached from the internet
   curl -fsS http://<DOMAIN>/api/ready      # a plain-HTTP install on a local network
   ```

   The backend runs a key canary at boot when `secrets_encryption_key_old`
   is empty (the normal case): a wrong `SECRETS_ENCRYPTION_KEY` produces one
   WARN line beginning `ENCRYPTION KEY MISMATCH: this database holds values
   the configured SECRETS_ENCRYPTION_KEY cannot read`. With an old key
   present, the boot runs the rotation coverage report instead, and the
   line to look for is `encryption key coverage: …`. The absence of the
   mismatch line, not the row count, is what proves the restore.

Managed devices reconnect on their own: their adoption files name the
platform URL and the agent endpoint, so a host restored under the same name
needs no re-adoption.

**The dump carries its schema version** (the `schema_versions` table, the
ledger described in `docs/INSTALL.md` → "Upgrading"), and the backend reads it
before it migrates anything. Restoring an OLDER dump onto a newer release is an
upgrade: the backend records a new ledger row on start, migrates, and marks
the row finished. A NEWER dump onto an older release is refused — including
one taken while a newer release's migration had failed part-way, whose row
is recorded but unfinished — the backend exits naming both
versions and leaves the restored database untouched; start the release the
dump was taken from, or a newer one. A dump from before the ledger existed has
no such table and is migrated like a new install.

## Restore into the same install

Rolling back an upgrade, or undoing a bad change, on the host that is still
running. A dump restored into the database the install is using restores
nothing: every object already exists. The database is replaced by an empty
one first, and the current one is kept aside until the restore is proven.
Run everything in the install directory (`docs/INSTALL.md` → "Upgrading"
says where it is); `data/` below is your `DATA_PATH`.

1. **Stop the stack.**

   ```sh
   docker compose -f docker-compose.community.yml down
   ```

   Without `--volumes`: the data stays where it is. `down` removes the
   containers and with them their logs; if you may need the backend's log
   from before the restore, save it first:
   `docker compose -f docker-compose.community.yml logs --no-color backend > backend-before-restore.log`.
2. **Put back the release the dump was taken from** (rolling back an
   upgrade only): its files as in `docs/INSTALL.md` → "Upgrading", step 2,
   with the older version, and `PLATFORM_VERSION=<older version>` in `.env`.
   Restoring into the release that is installed needs neither.
3. **Put back the credential files that match the dump.** An upgrade
   rotates no key, so if none was rotated since the dump, `secrets/` as it
   is matches it. Otherwise restore `secrets/` from the same backup as the
   dump — `secrets_encryption_key` decides whether the rows can be read, and
   `db_password` must be the one the roles dump carries (see "Restore onto
   an empty host", step 2). A file a newer release added and the older
   compose file does not name can stay.
4. **Set the current database aside and start an empty one.**

   ```sh
   sudo mv data/postgres data/postgres.before-restore
   mkdir data/postgres
   ```

   Then restore exactly as "Restore onto an empty host", step 3, says — it
   starts PostgreSQL alone, which initialises an empty cluster in the new
   directory, checks it is empty, and restores roles, then data. On the
   host whose nightly script wrote the dump, the key and both files are
   already at the paths that step reads; choose the `TS` of the dump to
   go back to.
5. **Start the rest and verify** as in step 5 there, and go through the
   "Verification checklist".
6. **Remove the old database** once the restored install is proven:
   `sudo rm -rf data/postgres.before-restore`. Until then it is the way
   back: `down`, move the two directories the other way round, `up -d`.

The storage volume is not rolled back. Images uploaded after the dump stay
as files no row refers to; delete them by hand. Agent releases live in the
storage alone, so those imported after the dump stay imported, and `latest`
stays where the last `--set-latest` moved it.

## What a restore cannot bring back

- **Devices whose bootloader points at the old address.** Network-boot
  provisioning bakes the boot host (`PROVISIONING_BOOT_HOST`, which defaults
  to `DOMAIN`) and the server's public key into each device's EEPROM. A
  restore under the **same name** keeps them; a restore under a new name, or
  with a new signing key, does not — each such device must be re-baked
  through its per-device package, which takes a working path to the device
  (a provisioning card, or an agent still running on its installed OS). Plan
  the name as permanent, and keep the key.
- **Devices that call the old address.** Every adopted device's agent keeps
  the server address it was adopted with. Under a new name the devices stay
  with the old server while it answers, and are offline on the restored one
  until each is adopted again with a command from the restored server
  (`docs/INSTALL.md` → "Uninstalling", *Moving a device to another server*).
- **Anything encrypted under a key you did not keep.** There is no recovery
  path; the rows are left in place as unreadable ciphertext and the affected
  secrets must be re-entered.
- **Images that were never copied off the host**, on a local-storage install.
- **In-flight state**: a playbook that was running, an upload that was in
  progress, a command that was dispatched and not yet settled. Running
  executions are re-queued or settled on the next start; an upload is simply
  gone.
- **Whatever happened after the dump.** The recovery point is the last
  nightly run; there is no write-ahead-log archiving in this recipe.

## Verification checklist

Run after every restore, and on a schedule against a scratch host so that
the day the backup is needed is not the day it is first tested.

- [ ] The backend starts and its log shows `configuration loaded
      successfully`. (In production mode a configuration problem does not
      warn: the process prints `insecure configuration for production mode`
      with the list and exits, so a running backend has passed the checks.)
- [ ] No `ENCRYPTION KEY MISMATCH` line in the backend log.
- [ ] `/api/ready` answers 200.
- [ ] Sign in as an existing user (the JWT secret is the restored one; a new
      one would have signed everyone out — tolerable, but notice it).
- [ ] The Secrets page marks no entry `UNREADABLE`, and a variable (a
      non-sensitive entry) shows its value to an admin: it decrypts. A
      sensitive secret's value is never shown, so it cannot be the check.
- [ ] Provisioning passwords decrypt: open the Nodes page and the
      Organization page, then check that the backend log holds no
      `stored provisioning password could not be decrypted with any
      configured key` line. The UI never shows a provisioning password (it
      reports only that one is set), but every read of a node, a unit or the
      account defaults decrypts it under the same key — a second witness.
- [ ] The node list is complete and devices reconnect within their backoff —
      watch the status column turn online.
- [ ] An image on the Images page downloads and its checksum matches the
      stored one.
- [ ] The entitlement card shows the restored entitlement, if one was
      installed.
- [ ] For a network-boot install: `data/eeprom-keys/pubkey.pem` is the key
      baked into the devices (compare with the escrow copy), and one device
      completes a reboot-to-recovery round trip. Without a device at hand,
      the fingerprint and the served image's signature can be checked:

      ```sh
      # the same line on the old host, or on the escrow copy
      sudo openssl pkey -pubin -in data/eeprom-keys/pubkey.pem -outform DER | sha256sum
      # line 1 of boot.sig is the image's SHA-256, line 3 its signature
      head -1 data/net_install/boot.sig; sha256sum data/net_install/boot.img
      sed -n 's/^rsa2048: //p' data/net_install/boot.sig | xxd -r -p > /tmp/boot.sig.bin
      sudo openssl dgst -sha256 -verify data/eeprom-keys/pubkey.pem \
        -signature /tmp/boot.sig.bin data/net_install/boot.img   # Verified OK
      ```
- [ ] The nightly dump timer is installed and enabled on the new host, with
      the backup key in place, and the first run after the restore promotes
      a verified dump.
