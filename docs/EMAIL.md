# Email

Uptime Orchestrator sends a small number of transactional messages over SMTP.
Mail is optional: an install with no relay works, with the exceptions named
at the end of this page. This page covers the configuration, what is sent,
the two kinds of relay, how to test without delivering anything, and what
to expect without mail.

## Configuration

Set these in the `.env` the community compose reads (`docs/CONFIGURATION.md`
has every variable with its default; the compose already sets `APP_BASE_URL`
to `https://<DOMAIN>`):

```sh
EMAIL_ENABLED=true
EMAIL_SMTP_HOST=smtp.example.com
EMAIL_SMTP_PORT=587                             # 0 or unset also means 587
EMAIL_SMTP_STARTTLS=true                        # the compose's default; the code's is false
EMAIL_FROM_ADDRESS=noreply@example.com          # must exist in the sending domain
EMAIL_FROM_NAME=Orchestrator
# For a host that authenticates by credentials; leave it unset for a relay
# that authorises by sender address:
EMAIL_SMTP_USERNAME=…
```

**The SMTP password is a file**, like every credential of the community
install: the compose maps `EMAIL_SMTP_PASSWORD_FILE=/run/secrets/smtp_password`,
the loader reads the `_FILE` form first, and an `EMAIL_SMTP_PASSWORD` in
`.env` is never read. Write it into the file `--init-secrets` created empty:

```sh
printf '%s\n' '<the password>' | sudo tee secrets/smtp_password >/dev/null
sudo chown 1000:1000 secrets/smtp_password && sudo chmod 0400 secrets/smtp_password
```

**Only STARTTLS submission is implemented.** The mailer connects in plain
text and upgrades with STARTTLS (port 587); there is no implicit-TLS
(port 465, "SMTPS") mode, so a provider that offers only 465 cannot be used
directly.

What actually decides whether mail is sent is `EMAIL_ENABLED` **and** a host
**and** a sender address: `EMAIL_ENABLED=true` with either of the other two
empty logs `email is enabled but incompletely configured` at boot and
behaves as off.

Two of these are enforced by the production checks (`docs/CONFIGURATION.md`
→ Production checks): with email enabled, the backend refuses to boot when
`EMAIL_SMTP_STARTTLS` is off or `APP_BASE_URL` is empty. A verification link
is a bearer credential for the account; mailing it in the clear, or with no
origin to point at, is worse than not mailing it.

`APP_BASE_URL` is distinct from `SERVER_ADVERTISEMENT_URL`: the first is what
people's browsers open, the second is what devices call. On a single-host
install they are the same name.

## What the platform sends

Four messages, all through the same transport:

1. **Email verification** — at sign-up and after an address change. A
   verified address is what lets a later single-sign-on login attach to the
   same account silently (`docs/GOOGLE-OAUTH-SETUP.md`).
2. **Password reset** — the self-service "forgot password" link.
3. **Member invitations** — when a member is invited to an account.
4. **"Your email address was changed"** — sent to the **old** address,
   naming the new one. It is the only signal the rightful owner gets if
   someone with a session re-points the account at their own inbox, and it
   goes to the mailbox that is about to stop receiving anything else. It
   carries no link and deliberately does not suggest a reset.

Behaviour worth knowing:

- **The verification link is a credential.** Single-use, 24-hour expiry,
  stored only as a digest, revoked whenever a newer one is issued. Clicking
  it a second time still shows success (idempotent); a stranger's copy of a
  spent token is refused.
- **Resends are throttled** per user (five per hour) on top of the
  per-account API rate limit, so the endpoint cannot be used to flood an
  inbox.
- **A changed address re-triggers verification** and clears the previous
  stamp; a link minted for the old address does not verify the new one.
- **Delivery failures never fail the operation.** If the relay is down,
  sign-up and address changes still succeed; the failure is logged and the
  user can resend. Watch the backend log for `email` warnings when something
  looks silent.
- **Invitations are bounded** by `MEMBER_MAX_INVITES_PER_DAY` per account
  (counted off the audit trail, so revoking and re-sending does not reset
  it), because an invite is mail sent to a stranger over your domain.

## Two kinds of relay

**A submission host that authenticates by credentials** — the common case
with a mailbox provider or your own MTA. Set `EMAIL_SMTP_USERNAME` and write
the password into `secrets/smtp_password` (above); STARTTLS on port 587.

**A relay that authorises by sender address** — Google Workspace's SMTP
relay (`smtp-relay.gmail.com:587`) is one: it accepts mail from the public
addresses you list in its configuration, requires TLS, and needs no
password, so there is no SMTP credential to store or rotate. Leave the
username unset and `secrets/smtp_password` empty. Three things to get right:

- **Allow every address family the server actually uses.** If the relay
  resolves to an IPv6 address and the server has IPv6, the connection is made
  over IPv6 and the relay sees that address. Allow both the IPv4 and the
  IPv6 **address** (a single address, not the prefix — the relay caps the
  size of a listed range). `ip route get <relay address>` on the server
  prints the source address it will use.
- **The sender must exist.** The relay refuses to send as an address that is
  not a mailbox or group in the domain; a group with posting restricted to
  owners is the cheapest `noreply@` sender.
- **Changing the server's address silently breaks sending** — the relay
  answers `550` and, because delivery failures do not fail operations, the
  only trace is a warning in the backend log. Check the allow list first
  after any move.

## Reaching the inbox: SPF, DKIM, DMARC

Whichever relay you use, the recipient's provider decides whether to accept
the message, and that is decided by DNS records on the sending domain.
Without all three, mail is accepted by the relay and binned by the
recipient.

- **SPF**: one TXT record on the domain root naming the relay, for example
  `v=spf1 include:<your relay's SPF include> ~all`. If an SPF record already
  exists, **edit it** — two SPF records is a hard failure, not a merge.
- **DKIM**: generate the key at the relay (2048 bits), publish the TXT record
  it gives you under `<selector>._domainkey`, then enable signing. A lone
  DKIM failure right after enabling it is usually the relay's signing
  catching up; re-test an hour later before touching DNS.
- **DMARC**: a TXT record on `_dmarc`, starting in monitor mode:
  `v=DMARC1; p=none; rua=mailto:dmarc-reports@<your domain>`. Point `rua` at
  an address in the **same** domain (reporting to another domain requires
  that domain to publish an authorisation record, and without it receivers
  send nothing, silently). `p=none` with no `rua` does nothing at all.
  Tighten to `p=quarantine` and then `p=reject` only after a week of clean
  reports.

Verify on a real message: open it in the recipient's client, show the
original headers, and look for `SPF: PASS`, `DKIM: PASS`, `DMARC: PASS`.

## Testing without delivering

The following validates the two things that actually break — the relay's
acceptance of your server and of your sender address — and stops before
`DATA`, so nothing is sent. Run it on the server:

```sh
python3 - <<'PY'
import smtplib
s = smtplib.SMTP('smtp.example.com', 587, timeout=15)
print('connected from:', s.sock.getsockname()[0])
s.ehlo(); s.starttls(); s.ehlo()
# s.login('user', 'password')            # for a credential-authenticated host
print('MAIL FROM ->', s.mail('noreply@example.com'))
print('RCPT TO   ->', s.rcpt('you@example.com'))
s.quit()                                 # no DATA: nothing is delivered
PY
```

Expected: both commands answer `250`. A `550` on `MAIL FROM` means the
server's address is not authorised (check the printed source address — it
may be the IPv6 one); a `550` naming the sender means the mailbox does not
exist in the domain.

End to end: sign up a throwaway user, or on an unverified account open
*Account settings → Email address → Send confirmation email*. The message
should arrive within seconds and its link should land on the verification
page with a success state.

## What works without mail, and what does not

Each message degrades differently on an install with no relay:

| Message | Without mail |
|---|---|
| Verification | never sent; users stay unverified, nothing breaks, and they link single sign-on manually from account settings |
| **Password reset** | **refused, visibly**: the endpoint answers `503` with "password reset by email is not configured on this deployment; ask an administrator to reset it for you". Nobody waits for mail that will not come — and nobody can self-serve. The operator resets the password from the host with `/app/server --reset-password` (`docs/INSTALL.md` → Recover a lost password or authenticator) |
| Invitations | created, not delivered: the invite and its link are returned to the inviter with `delivered: false`, to be passed on by hand; the invitee is told nothing |
| Address-change notice | silently never leaves; the change itself proceeds, and the old address is not told |

## Troubleshooting

| Symptom | Cause | Fix |
|---|---|---|
| `email is enabled but incompletely configured` at boot | `EMAIL_SMTP_HOST` or `EMAIL_FROM_ADDRESS` empty | set both, recreate the backend |
| backend refuses to start with email enabled | `APP_BASE_URL` empty or `EMAIL_SMTP_STARTTLS` off | set the origin; keep STARTTLS on |
| `550` relay access denied / invalid credentials | the server's address is not in the relay's allow list, or credentials are wrong | add the current public address (both families), or fix the credentials |
| `550` sender address not allowed | `EMAIL_FROM_ADDRESS` is not a mailbox or alias in the domain | create it |
| mail sends but lands in spam | SPF, DKIM or DMARC missing or not propagated | check the message headers; re-check the three records |
| link opens but says invalid or expired | token already used, older than 24 hours, or superseded by a resend | send a fresh one from account settings |
| nothing arrives and nothing fails | delivery failures are logged, not raised | `docker compose logs backend | grep -i email` |
