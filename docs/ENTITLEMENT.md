# Device entitlement

An install of Orchestrator admits up to **25 devices** with no entitlement
at all. A signed entitlement file raises that number. This is the whole of
the licence-key functionality in the product, and this page is its
specification: the file, how it is verified, what it changes, and what it
deliberately does not.

Two promises hold everywhere in this design:

- **An entitlement never locks the UI and never touches data.** Its one
  effect is on *admission* — whether one more device may be added. Devices
  already admitted keep connecting, keep taking commands, keep netbooting
  into recovery and keep receiving updates, whatever the entitlement says.
- **Everything free at announcement stays free.** The free cap is a floor,
  not a trial — and no file lowers it: a signed cap between 1 and 24 is
  enforced as 25 (see [Expiry and grace](#expiry-and-grace)).

An install that hosts other organisations may also bound each account on
its own: [The per-account device limit](#the-per-account-device-limit), an
operator's setting that is off by default, keeps the first promise exactly
and is checked in addition to the cap described here.

## What is counted

The cap is **per install**, across every account the install hosts. It
counts *admitted* devices: a node that is approved and is not sitting in a
pre-connect state (`ready_for_adoption`, `flashing`). Specifically **not**
counted:

- a node an operator created that no agent has connected to yet;
- a browser-flashed or SSH-adopted placeholder waiting for its first boot;
- a device that presented a master token and is still pending approval;
- a rejected device;
- a deleted node (its record is gone; a restore is covered below).

So a leaked master token cannot fill a paid cap with rows that never
connected, and a batch of placeholders is not a batch of devices. The counts
the platform reports — `admitted_devices` and `account_devices` on
`GET /api/v1/server/entitlement` and `count` / `account_devices` in a
refusal — are this same admitted count, so they can be lower than the number
of rows on the Nodes page: a device that has connected and waits for approval
is listed there and counted only once it is approved. A deleted
node that was admitted when it was deleted counts again from the moment it is
restored, even before its device reconnects, and its restore is admitted
under the cap like any other admission (below). Any other deleted node is
restored exactly as it was — a placeholder, a pending device, a rejected
one — and is not counted.

## What reaching the cap does

A new device is refused **at admission** — the moment a row that is not yet
admitted would become admitted. There are exactly four such moments, and
each runs its count and its write together under the lock described below.
A restore puts a row back in the admission state it had when it was
deleted: a row that was never admitted comes back un-admitted and meets one
of the first three like any new device, and a row that was admitted is
admitted again by the restore itself — the fourth.

| Admission | Refusal | The row afterwards |
|---|---|---|
| A device's first connect into a node created for it (operator create, browser flash, SSH adoption, the per-node install link) | the gRPC handshake is refused with `ResourceExhausted` and the fixed message `device cap reached` | unchanged: still waiting for its first connect |
| A master-token device reconciled into the placeholder made for it | the same gRPC refusal | unchanged: still a placeholder |
| Approving a pending master-token device (`POST /api/v1/nodes/{id}/approve`) | HTTP 409, code `device_cap_reached` | unchanged: still pending |
| Restoring a deleted node that was admitted when it was deleted (`POST /api/v1/nodes/deleted/{id}/restore`) | HTTP 409, code `device_cap_reached` | no row; the deleted-node record stays restorable |

Those are the moments the product takes on purpose. The bound holds for
every other write as well, because it is enforced where the row is written:
a status-only write, a full-row save and a plain approval each refuse to
move an approved row out of a pre-connect status (or approve a row that is
no longer in one), and the status writers retry such a move under the same
lock — so the status a device reports, the monitor's offline sweep and a
command's `maintenance` pin all count as an admission when they would admit.
A device may report only `online` and `offline`; the pre-connect statuses are
set by the platform, and a device that reports one is ignored with a WARN.
The browser's flash commit moves a node from `flashing` to
`ready_for_adoption` only while it is still `flashing`, and a re-flash puts a
placeholder back to `flashing` only while it is still awaiting its first
connect (otherwise `409 device_connected_during_flash`), so a device that
connected first is not taken back out of the count.

A refused row stays as it was, so the same device is admitted later — on its
next connect, or the next approval — once a slot is free (a device removed,
or an entitlement applied). The device keeps retrying on its own; see
[The device's side](#the-devices-side).

The paths that *create* a row check the cap too, as a courtesy, so an
operator at the cap is told before a row, a bake or an SSH job exists:

| Path | Refusal |
|---|---|
| Add a node by hand (`POST /api/v1/nodes`) | HTTP 409, code `device_cap_reached` |
| Browser flash of a new device (WebUSB) | HTTP 409, code `device_cap_reached`, before the EEPROM is baked |
| SSH adoption (`POST /api/v1/nodes/ssh-adopt`) | HTTP 409, code `device_cap_reached`, before any job starts and before the device is dialled |
| A new device presenting a master token over gRPC | the handshake is refused with `ResourceExhausted` and the fixed message `device cap reached` |

That check is advice, not the bound: none of those rows is admitted when it
is created, so the bound is the admission above.

The HTTP refusal carries the numbers in `data`, and what they are depends on
who asks, because the install-wide admitted count is the sum of every
account's fleet. The instance operator (the
oldest account) gets `cap`, `count` (install-wide), `subscription_url`,
`account_devices` (its own admitted count) and `instance_operator: true`, and
the message names the numbers and the subscription link. Any other account
gets exactly `{"account_devices": N, "instance_operator": false, "cap": C}`
and the message "This install's device limit is reached; ask its operator."
— the limit is not theirs to lift. `account_devices` is left out when that
count cannot be read; the operator check fails closed (a caller it cannot
confirm gets the tenant view). The web UI renders the fields as a notice
wherever a device is added. While the install is at its cap, the Status page
lists every node waiting to be admitted — created and not yet connected, or
pending approval — with the condition `device_cap_reached`, `expected` (the
cap), `account_devices` and `instance_operator`, plus `current` (the
install-wide count) for the operator only, and links it to the Subscription
tab. A refusal is recorded in the audit trail of the refused account as
`entitlement.device_cap_reached`, its detail scoped like the HTTP answer: the
instance operator's row carries `cap`, the install-wide `count` and its own
`account_devices`; any other account's row carries `cap` and its own
`account_devices` only. Every refusal an operator makes is recorded, and once per
hour for a device that keeps connecting, since it asks again on its own and
its refusals would otherwise fill the trail within any retention window.
That throttle is keyed on the node for a first
connect and on the master token for a new master-token device, never on the
serial the device presents, so a holder of a token cannot mint a row per
invented serial.

Nothing else happens. In particular:

- **A reconnect is never an admission.** A device the install already
  manages always comes back, over the cap or not.
- **A restore of an admitted node needs a free slot.** The tombstone records
  whether the node was admitted when it was deleted. If it was, the restore
  is an admission: its count and its insert run under the lock, and at the
  cap it is refused with the typed 409, inserting nothing and leaving the
  record restorable, so the same restore succeeds once a slot is free. Its
  device's rebind on the next connect is then a reconnect, never a second
  admission. If it was not, the node comes back with its original approval
  and pre-connect status (a rejected device stays rejected, a pending one
  pending, a placeholder a placeholder), and its first connect or approval
  is the admission. A record written before this was tracked is restored as
  a placeholder, so its next connect is an admission too.
- **A hardware replacement is exempt.** It creates no row.
- **Deleting a device frees a slot**, and the slot is anyone's: a device
  admitted into it before the deleted node is restored makes that restore
  wait for another free slot. No sequence of delete, admit and restore takes
  the admitted count past the cap.

Concurrent admissions are serialised: the count and the write that admits
(the first connect, the reconcile, the approval — and the courtesy insert)
run in one database transaction under a single advisory lock, so a batch of devices
arriving together cannot all pass the same count. If the lock cannot be taken
or the count cannot be read — a statement or lock timeout on the connection
included — the admission is refused and nothing is written: the transaction
is rolled back, and there is no second attempt without the lock, so a
database that cannot answer is never a way to admit past the cap. The
refusal is transient and says so: over HTTP a typed 503 with the code
`device_cap_unverified` ("the device limit could not be checked, try
again"), which the web UI shows as that rather than as the limit being
reached; over gRPC `ResourceExhausted` with the fixed message `device cap
could not be checked; retry shortly`. The agent holds its long retry only
for the exact cap-reached message, so it treats this one as a transient
refusal: it stays up, keeps its identity, and reconnects through its usual
backoff (doubling from one second to a 30-second ceiling, after the server's
30-second park of the refused handshake). One ERROR line records each
refusal, once per device per hour for a device that keeps asking; no audit
row is written, because the cap was not reached. On an unlimited install
(effective cap 0) nothing is counted, so nothing is refused this way, and a
device the install already manages never reaches the check. The SSH
adoption's pre-check refuses the same way before any job starts.

The cap itself follows a stricter rule: a failed read never lifts it. The
file in force is resolved from the boot file and the stored files and cached
for 30 seconds; a read of the stored files that fails is never cached. The
last complete resolution keeps answering (the next request tries the table
again), so a transient failure never refuses a device an applied file
covers. When there is none yet — a failure on the first read after a
restart — that one request is bounded by what can be established without
the table: the boot file if it verifies, else the free cap, with an ERROR
line saying so. It is never unlimited, so making the table unreadable is not
a way to admit past the cap; the cost is that a device arriving in that
window on an install whose file lives only in the table may be refused once
and admitted on its next attempt.

### The device's side

A device refused at the cap is told so with the fixed gRPC message
`device cap reached`, and the platform holds the refused handshake for
30 seconds before answering, so a device cannot turn the refusal into a
flood. The agent recognises the message and does not reconnect at once:

- the process **keeps running** — exiting would only hand the same refusal
  to the service manager's restart loop;
- it logs **one WARN line** for the first refusal of a streak (repeats are
  INFO), saying that the install is at its cap and that it will ask again;
- it waits **about 15 minutes** (jittered by up to three minutes either
  way, so a batch of devices refused together does not come back together)
  and then asks again, its heartbeat-driven reconnect standing down for the
  wait.

So a refused device is admitted, without anyone touching it, within about a
quarter of an hour of a slot becoming free. Agents released before this
behaviour reconnect about once a minute instead (each attempt held for 30
seconds by the platform); a device takes the new behaviour with its next
agent update.

## The per-account device limit

An install that hosts other organisations can bound how many devices **each
account** may hold. It is an operator's setting, off by default, and it is
independent of and additional to the instance-wide cap above: an admission
must pass both. A self-hosted install that does not set it behaves exactly
as before it existed, and reads nothing more at admission.

```
ACCOUNT_DEVICE_LIMIT=5                           # devices each account may hold; 0 (the default) = none
ACCOUNT_DEVICE_LIMIT_CONTACT=sales@example.com   # where an account at its limit writes for more
```

**What it counts.** The account's own *admitted* devices — exactly the
predicate the cap counts ([What is counted](#what-is-counted)), restricted to
the account. A placeholder, a node waiting for its first connect, a pending
device and a rejected one are not counted.

**Whom it binds.** Every account except the instance operator's (the oldest
account; [Who may apply a file](#who-may-apply-a-file)). That check fails
closed: an account the lookup cannot confirm as the operator is bound.

**Where it is enforced.** At the same four admission moments as the cap, in
the same transaction under the same lock, after the instance count: the
first connect into a node created for the device, the master-token
placeholder reconcile, the approval of a pending device, and the restore of
a node that was admitted when it was deleted. The courtesy checks on the
row-creating paths (adding a node, a browser flash, the SSH adoption's
pre-check, a new master-token device) ask it too. The lock is the single
admission lock, so two admissions racing for an account's last slot admit
exactly one, and an install whose instance cap is unlimited still takes the
lock while an account limit applies.

**The two promises hold unchanged.** The limit bounds admission only. A
device already admitted keeps connecting, taking commands, netbooting into
recovery and receiving updates. An account that holds more than its limit —
because its limit was lowered, or a subscription ended — keeps every device
it has; it simply cannot admit another until it is below the limit or its
limit is raised. A reconnect is never an admission and never asks the limit.

**The refusal.** Its own code, wherever `device_cap_reached` is answered:

| Path | Refusal |
|---|---|
| A first connect, or a master-token device (new or reconciled) | gRPC `ResourceExhausted` with the fixed message `account device limit reached` |
| Approve, restore, add a node, browser flash, SSH adoption | HTTP 409, code `account_device_limit_reached` |

The HTTP refusal's `data` carries the account's own numbers only: `limit`,
`account_devices`, `plan` (`free` or `subscription`), `until` (a dated
subscription's end, RFC 3339) and `contact` (when configured). The refused
row stays as it was and is admitted later, exactly like a row refused at the
cap: a current agent holds for its long jittered retry on this message as on
the cap's ([The device's side](#the-devices-side)); an agent released before
the account limit treats it as transient and reconnects about once a minute,
each attempt held 30 seconds by the platform. The install script exits with
its own status (10) for it. A limit that cannot be read, or an account count
that fails, refuses as unverified (`device_cap_unverified`), never admits.

A refusal is recorded in the account's audit trail as
`entitlement.account_device_limit_reached` (detail: the limit, the account's
count, the plan and a dated subscription's end), throttled per device like
the cap's. While the account is at its limit and the install has room, the
Status page lists every node waiting to be admitted with the condition
`account_device_limit_reached`: `expected` is the account's limit and
`account_devices` its count. (When the install is at its cap too, the row
names the cap, which admission checks first.)

### The override, and a dated subscription

An operator raises or lowers one account's limit with two nullable columns
on `accounts`, written by instance administration outside this API (the
backend never writes them; its account update leaves them alone):

| `device_limit` | `device_limit_until` | The account's limit |
|---|---|---|
| NULL | anything | `ACCOUNT_DEVICE_LIMIT` (plan `free`) |
| `N` (> 0) | NULL | `N`, with no end (plan `subscription`) |
| `N` (> 0) | a timestamp | `N` before that instant, `ACCOUNT_DEVICE_LIMIT` from it on |
| `0` | as above | no per-account limit for this account, for as long as the override is in force |

A negative `device_limit` is refused by the column's CHECK. The override is
read only while `ACCOUNT_DEVICE_LIMIT` is above 0. The end is judged at
every admission and every read of the page on the server's clock, the one
the entitlement's expiry is judged on: no job runs when a subscription ends,
and there is **no grace** — at `device_limit_until` the override is simply
gone and the account is back on the default, holding whatever it holds. A
change takes effect at the next admission; there is no cache to wait for.

The statements, in `psql` against the backend's database (`<id>` is the
account's id; the day count in the last two is yours):

```sql
-- Grant a six-month subscription of 50 devices.
UPDATE accounts SET device_limit = 50, device_limit_until = now() + interval '6 months'
 WHERE id = '<id>';

-- Extend it by six months from its current end (or from now, if it already ended).
UPDATE accounts SET device_limit_until = greatest(device_limit_until, now()) + interval '6 months'
 WHERE id = '<id>' AND device_limit IS NOT NULL;

-- A limit with no end; or no per-account limit at all for this account.
UPDATE accounts SET device_limit = 50, device_limit_until = NULL WHERE id = '<id>';
UPDATE accounts SET device_limit = 0,  device_limit_until = NULL WHERE id = '<id>';

-- End it now: the account returns to the default at once.
UPDATE accounts SET device_limit = NULL, device_limit_until = NULL WHERE id = '<id>';

-- Subscriptions that end within the next 30 days.
SELECT id, name, device_limit, device_limit_until FROM accounts
 WHERE device_limit IS NOT NULL
   AND device_limit_until > now() AND device_limit_until <= now() + interval '30 days'
 ORDER BY device_limit_until;

-- Accounts holding more admitted devices than their limit now. :default is
-- ACCOUNT_DEVICE_LIMIT (psql: \set default 5). The instance operator, the
-- oldest account, is not bound and is left out.
SELECT a.id, a.name, count(n.id) AS admitted, l.lim AS account_limit
  FROM accounts a
  CROSS JOIN LATERAL (SELECT CASE
           WHEN a.device_limit IS NOT NULL
            AND (a.device_limit_until IS NULL OR now() < a.device_limit_until)
           THEN a.device_limit ELSE :default END AS lim) l
  JOIN nodes n ON n.account_id = a.id
   AND n.approval_status = 'approved'
   AND n.status NOT IN ('ready_for_adoption', 'flashing')
 WHERE a.id <> (SELECT id FROM accounts ORDER BY created_at, id LIMIT 1)
 GROUP BY a.id, a.name, l.lim
HAVING l.lim > 0 AND count(n.id) > l.lim
 ORDER BY count(n.id) - l.lim DESC;
```

(Use `>=` in the last `HAVING` for the accounts that are AT their limit, the
ones whose next device is refused.)

### What the account sees

`GET /api/v1/server/entitlement` answers such an account with
`mode: "account_limit"` and its own figures only:

| Field | Meaning |
|---|---|
| `account_device_limit` | its limit now; `0` = no limit, `-1` = it could not be read |
| `account_plan` | `free` (the default) or `subscription` (an override in force); absent when unreadable |
| `account_plan_until` | when a dated subscription ends; absent otherwise |
| `account_devices` | its admitted count (`-1` when unreadable) |
| `account_limit_contact` | `ACCOUNT_DEVICE_LIMIT_CONTACT`, when set |

Nothing of the install's entitlement is in it: no `state`, no caps, no
expiry or grace, no subscription link, no terms. The instance
operator, and every account of an install that does not set the limit, gets
the answers described above, unchanged except for `mode: "instance"`.

The Subscription page shows "Free: 3 of 5 devices." with no date, and a
sentence that more devices need a subscription naming the contact; a dated
subscription shows "Subscription: 12 of 50 devices, until <date>." and what
happens when it ends; an account above its limit is told its devices keep
working and whom to write to. The contact is text, made a mailto link when it
is an e-mail address. The entitlement paste is never shown in this mode. The
add-device notices name the account's numbers and the contact instead of the
install's limit.

## The file

An entitlement is a small signed document. Every term is a **signed field**,
chosen per file when it is issued; nothing about an entitlement is a setting
on the install.

| Field | Meaning |
|---|---|
| `version` | always `1` |
| `customer_id` | who the file was issued to; shown on the card, bound to no host |
| `device_cap` | how many devices the install may admit; `0` means unlimited; never enforced below the free cap of 25 |
| `issued_at` | when it was issued (RFC 3339, UTC); a file issued more than 24 hours in the future is refused by the signer and the server alike, since the issue date decides which file wins |
| `expires_at` | when it expires |
| `kind` | `evaluation`, `subscription` or `hosted` — shown on the card, no effect on the arithmetic |
| `grace_days` | days after expiry during which the cap still holds (default 30; a short free key sets it low or to 0) |
| `note` | free text shown on the card |

### The paste form

```
entl1.<payload>.<signature>
```

`payload` is the document's JSON, base64url without padding;
`signature` is base64url without padding, over the bytes
`orchestrator-entitlement/1\n` followed by the payload, made by one of two
key types: **Ed25519** (64 bytes, over the message itself) or **ECDSA
P-384** (ASN.1 DER as `ecdsa.SignASN1` produces it, over the SHA-384 of the
message — the scheme `tools/agent-signer` uses for agent artifacts). The
production key is the second kind, because it lives on a hardware PIV token,
and PIV has no Ed25519 on every token. The paste form does not name the algorithm, and
needs no version bump for it: the verifier tries each trusted key with that
key's own algorithm. The prefix binds the signature to this format and
version. Whitespace anywhere in a
pasted value is ignored, so a file wrapped by an e-mail client still
verifies.

The verifier checks the signature over the payload bytes **exactly as
received** and only then parses them, so an unsigned payload is never
interpreted, and the verifier has no canonicalisation step of its own. The
signer produces canonical bytes by marshalling the fields in the order of the
table above, with no HTML escaping.

### Verification

The server trusts the public keys embedded at build time
(`backend/pkg/webapp/domain/entitlement/keys/*.pem`), each an Ed25519 or an
ECDSA P-384 PKIX `PUBLIC KEY`. Each file is parsed on its own; a file holding
any other key type or curve (P-256, RSA) fails the load with its name in the
boot ERROR, and the whole set is dropped — the install then trusts no key
and every file is rejected, which is loud, rather than quietly trusting one
key fewer than the build meant to. Any one trusted key suffices, which is what lets a signing key be rotated: ship the successor
beside its predecessor for a release, and files signed by either verify.
The private keys live outside every repository, with the person who issues
entitlements.

A file that does not verify, or whose terms do not validate, is **rejected**:
the install runs on the free cap, logs the reason at boot, and the
Subscription card says the file was not accepted. A rejected file never
refuses a boot.

### Expiry and grace

| When | Cap in force |
|---|---|
| before `expires_at` | `device_cap` |
| from `expires_at` until `expires_at + grace_days` | `device_cap`, the card says the file has expired |
| after that | the free cap of 25, for **new admissions only** |

A positive `device_cap` below the free cap is **clamped up to 25** in every
phase, never refused. The file is genuine and verifies; refusing it would
make the card say "not accepted" for a document that grants nothing worse
than the free tier, and clamping keeps the floor promise literally. The
server logs one WARN per such file naming the signed number, and the
operator's `GET /api/v1/server/entitlement` reports the effective 25 beside
the signed `device_cap` with `device_cap_below_free_floor: true`; the
operator's Subscription card says in one line that the file signs fewer
devices than the free cap and the free cap applies. The signer
never issues such a file (below), and its copy of the free cap is pinned to
the server's by a test that reads the server's source.

The grace is counted from the signed expiry, never from a clock the install
keeps, so a host whose clock predates the issue date is neither short-changed
nor extended. An expired hundred-device evaluation keeps all hundred devices
connected, commanded and netbooting; only the hundred-and-first is refused.

## Obtaining a file

An install that manages 25 devices or fewer needs no file. Above that, a
file is issued by the project's maintainers, who hold the signing keys:
the server accepts only a file signed by one of the keys embedded in it
([The signing keys](#the-signing-keys)), so a file
cannot be made on the install itself. The instance operator's Subscription
card and the `device_cap_reached` refusal link to where one is requested —
`https://orchestrator.sh/subscription/` unless the install sets
`ENTITLEMENT_SUBSCRIPTION_URL` to another address. A file arrives as the
[paste form](#the-paste-form); [Applying a file](#applying-a-file) says
where it goes.

## Applying a file

### Who may apply a file

The entitlement belongs to the install, not to an account, and an install
that hosts several accounts has one super_admin per account. So only the
**instance operator** — the account that operates the install, defined as
its oldest account (by creation time, the id breaking a tie) — may apply a
file or read its terms. On a single-account install that is simply the
account. A super_admin of any other account sees the summary (or, where a
[per-account device limit](#the-per-account-device-limit) binds the account,
its own plan instead and nothing of the install's file): the state,
the cap in force, its OWN account's admitted-device count
(`account_devices`), the expiry and the grace — never the customer, the
kind, the note, where the file came from, the files that were offered and
not accepted, or the install-wide admitted count, which on a hosted install
would tell every tenant how many devices the whole service manages
(`admitted_devices` is operator-only; the operator's card carries both
counts) — and is refused the paste with `403 not_instance_operator`. That
withholding also covers the typed `409 device_cap_reached` and the
`device_cap_reached` item of the fleet attention view: a non-operator gets
`account_devices` and `instance_operator: false`, never the install count.
A rejected file is reported by its source and
reason only, never by the customer it was issued to. The instance
operator's account must never be erased while other accounts exist: the role
is not stored, so removing the oldest account would hand it to the
next-oldest one, a tenant. The backend's `DELETE /api/v1/accounts/{id}` deletes no
account for any caller (403), and the account service's delete behind it
refuses the operator's account with `409 instance_operator_account`, so
re-enabling that route cannot reopen the hand-over.

### From the web UI

Account → **Subscription** (visible to super_admins). The card shows the
state — free, active, in grace, lapsed, or a file that was not accepted —
with the file's terms, the install's admitted-device count (every account
counted) against the cap, the operator's own account's count, the link to
get or extend an entitlement, and a paste box. Applying asks for a recent
step-up confirmation. A super_admin of an account that does not operate the
install sees the summary — the state and its own account's device count —
with no paste box and no link: extending the install's licence is the
operator's business. (Where a per-account device limit binds that account,
the card is its own plan instead and says nothing of the install's
entitlement; see [What the account sees](#what-the-account-sees).) Its state line is about the install, never a number
that reads as its own limit ("This install is covered until …", and outside
an active entitlement "This install can add devices up to its limit; its
operator manages the entitlement."). The same split holds on the add-device
notice and the Status page's device-limit row: the operator sees the
install's admitted count against the cap and the link; any other account
sees "This install's device limit is reached; ask its operator." with its
own count, no install number, no link and no Subscription remedy. The web UI
reads `instance_operator` on both payloads and treats its absence (an older
backend) as the operator.

### Through the API

```
GET /api/v1/server/entitlement
PUT /api/v1/server/entitlement   {"document": "entl1.…"}
```

Both require `entitlement.manage`, which no role below super_admin holds;
the `PUT` also requires a step-up ticket and the instance operator's
account. The `GET` answers `instance_operator`, `mode` (`instance`) and, for
anyone else, the summary shape, whose only device count is the caller's own
(`account_devices`). Where a per-account device limit binds the caller's
account, `mode` is `account_limit` and the answer is that account's own
([below](#what-the-account-sees)). The `PUT` verifies, stores and
answers with the entitlement now in force; a file that does not verify is
`400 entitlement_rejected`, a paste from another account is
`403 not_instance_operator`, and in both cases nothing is stored. The audit trail records
the file's terms (`entitlement.apply`), never the file.

A valid file is stored even when it does not take effect — it was issued
before a file still active or in grace, or it has already lapsed — so the
top-level terms of the `PUT` answer can be another file's. The answer
therefore also names the pasted file:

```
"applied": {
  "in_force": false,                       // is the pasted file the one in force now
  "issued_at": "2026-02-01T00:00:00Z",     // its signed issue date
  "expires_at": "2026-12-01T00:00:00Z",    // its signed expiry
  "device_cap": 50,                        // its signed cap; 0 = unlimited
  "state": "active"                        // its own phase: active | grace | lapsed
}
```

`applied` is on the `PUT` answer only (absent from every `GET`), and only
the instance operator ever receives it, like the other terms.

### Extending, upgrading, renewing

Paste the next file. **The newest valid file wins** — newest by its signed
issue date, among the files that have not lapsed — and applying one revokes
nothing. A lapsed file never displaces a live one, and re-pasting an old
file cannot demote a newer one. Every file ever applied is kept, so the card
can say which one ran out. Newest wins on the issue date, not on the cap: a
newer active file signing 10 devices displaces an older active one signing
100, and the install then runs at the free cap of 25 (the floor holds; the
signer no longer issues such a file).

### Air-gapped and bootstrap installs

Set the file at boot instead of pasting it:

```
ENTITLEMENT_FILE=/run/secrets/entitlement     # a path to a file holding the paste form
ENTITLEMENT=entl1.…                           # or the value itself
```

`ENTITLEMENT_FILE` is read first; a named file that cannot be read leaves
the value unset (the install logs it and runs on the free cap) rather than
falling back to a stale environment copy. The boot file is a candidate like
any other: a newer file pasted through the UI wins over it.

The community compose declares the `entitlement` secret (`secrets/entitlement`,
mapped as `ENTITLEMENT_FILE=/run/secrets/entitlement`); `server --init-secrets`
writes it empty, which is a valid "no entitlement". Write the file into it and
recreate the backend, or paste it in the UI. An install that needs a file at
first boot writes it before the first `docker compose up` of a build that
carries the count.

At boot the backend logs one `device entitlement` line with the state and
the effective cap. If the install already admits MORE devices than that cap
— an install started without its file, or one whose file lapsed — it logs
one WARN instead, naming both numbers: the devices keep working, and every
new admission is refused until the cap covers them.

## The hosted install

The hosted install runs **exactly this code**, with no build flag and no
environment exemption, under an entitlement issued with the same signer.
Its per-account device limit, where it sets one, is the operator's setting
above, not a term of its file.

## Issuing a file

`tools/entitlement-signer` is a Go program whose default build is standard
library only (no cgo): that build signs with a key FILE and verifies
anything, and it is the build a deploy-time check of a file runs
(`verify`, below). Signing with the
production key on its hardware token needs the PIV build, `go build -tags piv`
(cgo and the PC/SC library, as `tools/agent-signer` — on macOS the system
framework, nothing to install); the default build refuses `--piv-slot` and
says so.

```
entitlement-signer sign --piv-slot 82 --touch --customer acme \
    --device-cap 100 --expires 2027-01-01 --kind subscription \
    [--serial N] [--pin-env NAME] \
    [--grace-days 30] [--note "…"] [--issued-at 2026-01-01]
entitlement-signer pubkey --piv-slot 82 [--serial N] --out entitlement-signing-1.pem
entitlement-signer keygen --out dev-signing          # a DEVELOPMENT Ed25519 pair only
entitlement-signer sign --key dev-signing.key …      # a development key file, same flags
entitlement-signer verify --pub entitlement-signing-1.pem [--pub …] --in file.entl \
    [--require-active] [--min-cap N]
entitlement-signer verify --select --pub entitlement-signing-1.pem [--pub …] \
    --in boot.entl [--in applied-1.entl …] [--require-active] [--min-cap N]
```

`sign` prints the paste form, after checking it against the signing key's
own public half. A bare `--expires` date covers that whole day. Every
refusal below happens before the token is opened, so a typo costs no PIN
and no touch. `--piv-slot` accepts only the retired slots `82`..`95` and
refuses `9c` by name (the slot `tools/agent-signer` expects); with `--touch` it also refuses
a slot whose touch policy is not ALWAYS. `--key` takes a PKCS#8 Ed25519 key
(what `keygen` writes) or an ECDSA P-384 key (PKCS#8 or SEC1).
The signer refuses an expiry in the past or more than five years out, a
positive `--device-cap` below the free cap of 25 (issue 25 or more, or 0 for
unlimited), and any document the verifier would refuse. `verify` checks the
signature and then the same document validation the server runs — a file
signed elsewhere over terms the server refuses is reported as such, never as
OK — and prints the decoded terms and the state at the current time. A file
signed elsewhere with a cap between 1 and 24 verifies with a WARNING line,
since the server accepts it and enforces 25.

The signer and the server implement the format independently; the server's
tests verify a fixture the signer produced, byte for byte, so the two cannot
drift.

`verify --require-active --min-cap N` turns the check into an exit code: it
fails unless the file is before its expiry and the cap the server enforces
for it (its `device_cap`, never below 25) is 0 or at least `N` — what a
deploy preflight asks of the file it is about to rely on.

Three things keep that answer equal to the server's. Every `--pub` file must
parse on its own, as the server's embedded key files must: one that does not
(an empty file, another PEM type) makes the server trust no key at all, so
`verify` refuses naming it rather than letting it vanish into a concatenation.
A paste longer than the server's `MaxFormLength` (8 KiB) is refused, as the
server refuses it before any signature check. And `--select` judges the file
the server actually puts in force among several offered ones, given in the
server's order — the boot file first, then the applied files newest issued
first: the newest by signed issue date that has not lapsed (all lapsed: the
newest of them), an empty input counting as no file and a file that does not
verify as no candidate; `--require-active` and `--min-cap` then apply to that
file, its cap counted as the free cap once it has lapsed. No candidate at all
is a failure: the server would run on the free cap.

### The signing keys

Production files are signed with ECDSA P-384 keys held on hardware tokens;
the repository carries their public halves only, in
`backend/pkg/webapp/domain/entitlement/keys/`. The source tree is developed
against a development Ed25519 pair, recognised by fingerprint
(`entitlement.DevelopmentKeyFingerprint`): a build that embeds it logs a
WARN at boot saying it is a development build. No released build embeds it.

## Licence

The licence-key functionality the Fair Core License's limitations clause
refers to is the backend's verification of entitlement files
(`backend/pkg/webapp/domain/entitlement`) and the instance-wide
admitted-device count in the node repository. It is named as such in the
repository `LICENSE`, `REUSE.toml` and `README.md`. The signer,
`tools/entitlement-signer`, is not part of it: it is Apache-2.0 like the rest
of `tools/`, and anyone may use, modify and redistribute it. A file it
produces raises the cap only if it is signed by a key the backend embeds.
