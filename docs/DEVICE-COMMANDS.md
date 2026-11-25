# Device commands: a short reference

What some of the everyday device actions do, and the API calls behind them.
Everything here is also on a node's panel in the web UI; the API description
(`backend/docs/swagger.json`, see `docs/INSTALL.md` → "Using the API") has
every parameter. Paths below are as the backend serves them; through the
bundled reverse proxy they carry the extra `/api` prefix that page explains.

A device takes commands only while it is connected and approved: a node
pending approval answers `409 node_not_approved`. Most commands are refused
for an offline device; the ones that wait for it say so below.

## Identify

Blinks the device's locate LED so someone at the rack can find it.

```http
POST /api/v1/nodes/{id}/identify
{"duration_sec": 60}
```

`duration_sec` is optional: without it the account's identify duration
applies (default 300 s, at most 3600 s); the answer carries the duration in
force. `{"stop": true}` ends a running blink, and a repeat call restarts it.
The command succeeds as soon as the blink starts; the device stops on its
own timer. `GET /api/v1/nodes/identify-state` lists which nodes are blinking
and until when. It is not queued: an offline device fails at once, since a
blink hours later would surprise whoever is at the rack. Needs agent 0.6.0
or newer.

## Set the hostname

```http
POST /api/v1/nodes/{id}/command
{"command": "set_hostname", "params": {"hostname": "edge-07"}}
```

The name must be one RFC 1123 label — lowercase `a-z`, `0-9` and `-`, not
starting or ending with `-`, at most 63 characters — or it is refused before
anything is sent. On the device the agent sets it with `hostnamectl` (or
`/etc/hostname` where that is missing), updates the `127.0.1.1` line in
`/etc/hosts`, and reports its system information again, so the node shows
the new name within seconds.

## Access entries: users and keys on the device

An access entry declares an OS user on the device: its SSH keys, whether it
may use `sudo`, optionally a password, optionally certificate principals. (A
second type trusts an SSH certificate authority.) An entry is scoped to one
node, to organizational units, or to the whole account; an entry with no
scope applies to no node. Every create, change and delete is pushed to the
devices it reaches by itself — there is no separate apply step — and a device
that is offline gets it when it reconnects.

```http
POST /api/v1/ssh-keys
{"alias": "ops laptop", "key_content": "ssh-ed25519 AAAA… ops@laptop"}

POST /api/v1/access-entries
{"type": "user", "username": "ops", "sudo": true,
 "node_id": "<node id>", "key_ids": ["<key id>"]}
```

Keys are added to and removed from an entry one at a time
(`POST` / `DELETE /api/v1/access-entries/{id}/keys/{keyId}`), so two people
editing the same entry do not overwrite each other. `DELETE
/api/v1/access-entries/{id}` removes the entry.

On the device:

- The user is created in the group `orchestrator-managed`, which is how the
  platform knows which users it manages. A user that already existed on the
  device is not taken over unless the entry says so (`adopt_unmanaged`).
  Such an entry must carry a key or a password from the start (400
  otherwise): taking over an account with neither clears its password, locks
  it and ends its sessions, so attach the key in the same request.
- Its keys are written to `/etc/ssh/authorized_keys.d/<user>`, a root-owned
  file the platform rewrites as a whole on every apply; the user's own
  `~/.ssh/authorized_keys` is never touched. The platform configures sshd to
  read both.
- Without a password `sudo` needs none; with one it asks for it. A password
  reaches the device only as a hash.
- **A user is locked, never deleted.** Removing its last key locks it: its
  sessions are ended and the account is expired (`chage -E`). Deleting the
  entry does the same, and also empties its key file and revokes its `sudo`.
  The home directory stays either way, and an entry that gives the user a
  key again unlocks it.
- An apply that would leave the device with no way to log in is refused —
  by the server before it is sent, and again by the device. The entry itself
  is saved; the refusal shows on the node's command.

`GET /api/v1/nodes/{id}/effective-access` shows what reaches one node and from
which level; `POST /api/v1/nodes/{id}/access/re-apply` pushes it again. A node
waiting for approval gets its access when it is approved.

## Tracked files

A tracked file is a text file on the device that the platform keeps
versions of, so it can be edited and written back. The sequence:

1. **Track** — the platform reads the file from the device:

   ```http
   POST /api/v1/nodes/{id}/files/track
   {"path": "/etc/hosts"}
   ```

   The answer carries the read's `command_id`. The file is recorded as
   tracked, with its content as the first version, as soon as the device's
   read arrives. To wait for that, poll
   `GET /api/v1/nodes/{id}/files/read/{command_id}?path=/etc/hosts` until
   `status` is `completed` (with the `version`) or `failed` (with a
   `detail`: too large, binary, not UTF-8, and so on).
2. **The file is listed**: `GET /api/v1/nodes/{id}/files`; its content is
   `GET /api/v1/nodes/{id}/files/content?path=/etc/hosts`, its history
   `GET /api/v1/nodes/{id}/files/versions?path=/etc/hosts`.
3. **Save a new version** — nothing is sent to the device yet:

   ```http
   PUT /api/v1/nodes/{id}/files/content
   {"path": "/etc/hosts", "content": "127.0.0.1 localhost\n…"}
   ```

   The answer carries the new version's `id` and `version`, with status
   `saved`.
4. **Apply** it — the device writes it, keeping the file's mode and owner:

   ```http
   POST /api/v1/nodes/{id}/files/{version id}/apply
   ```

   The version is `applying` until the device reports, then `applied` (or
   `failed`, with the reason).

`POST /api/v1/nodes/{id}/files/refresh` (same body as track) reads the file
again and adds a version when it changed. `POST .../files/untrack` stops
following it and leaves the device alone; `POST .../files/delete-on-device`
removes it from the device and needs the path repeated as
`confirm_path` (`backup: true` keeps a `.bak` beside it). File operations are
refused while the device runs the network-boot recovery system, whose files
are not the installed system's. Some files the platform itself manages are
protected: they cannot be untracked or deleted. The files the access
configuration writes — its sudo grants (`/etc/sudoers.d/90-orchestrator-*`),
the key, principal and CA files under `/etc/ssh` and the platform's sshd
drop-ins — can be read and tracked but are never written, patched or
deleted this way (403 on apply); change the access entries instead.

A device agent that validates writes checks a file in `/etc/sudoers.d` with
`visudo -cf` before writing it, and `/etc/ssh/sshd_config` or a file in
`/etc/ssh/sshd_config.d` with `sshd -t` after writing it, restoring the
previous content when sshd rejects it; either refusal carries the
validator's message. An older agent writes those files unchecked.
