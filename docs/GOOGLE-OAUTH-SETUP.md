# Enabling Google sign-in (invite-gated)

> This is the enablement recipe. An install's live values (client id, client
> secret, redirect URL, invite code) live with its operator and are never
> committed.

The invite-gated OIDC code ships inert: `/auth/oidc/config` answers 404 until
it is enabled, and also when provider discovery failed at boot (the backend
logs `OIDC discovery failed; OIDC login disabled` and keeps running). An
existing user can log in with Google; whether a NEW email gets an account
depends on the invite code. With an invite code configured
(`SERVER_INVITE_CODE`), a new account is provisioned only when the sign-in
carries the matching code, whatever `OIDC_AUTO_PROVISION` says — a stranger
with a Google account cannot self-register. With no invite code configured,
`OIDC_AUTO_PROVISION` alone decides, and with it on any Google account could
register; keep it off (the community compose pins it to `false`). With open
registration (`SERVER_OPEN_REGISTRATION=true`) the instance's sign-up is open
to everyone, and a new email is provisioned through Google without a code,
whatever the invite code and `OIDC_AUTO_PROVISION` say; `/auth/oidc/config`
then answers `"sign_up":"open"`. The account ceiling (`SERVER_MAX_ACCOUNTS`)
applies to every path.

The examples below use `https://orchestrator.example.com`; substitute the
install's public URL.

## 1. Create the Google OAuth client (in a browser)

Google Cloud Console → **APIs & Services → Credentials**:
1. Configure the **OAuth consent screen** (User type: External): app name, support
   email, app domain, developer email. Publish (or keep in testing and add allowed
   testers).
2. **Create Credentials → OAuth client ID → Web application**:
   - Authorized JavaScript origins: `https://orchestrator.example.com`
   - Authorized redirect URIs: `https://orchestrator.example.com/api/auth/oidc/callback`
3. Copy the **Client ID** and **Client secret**.

## 2. Turn it on

**The client secret is a file.** The community compose maps
`OIDC_CLIENT_SECRET_FILE=/run/secrets/oidc_client_secret`, and the loader reads
`<VAR>_FILE` first, so an `OIDC_CLIENT_SECRET` in `.env` is never read. Write
the secret into the file `--init-secrets` created empty:

```sh
printf '%s\n' '<the client secret>' | sudo tee secrets/oidc_client_secret >/dev/null
sudo chown 1000:1000 secrets/oidc_client_secret && sudo chmod 0400 secrets/oidc_client_secret
```

An empty file does not stop the button from appearing — discovery needs no
secret — but every sign-in then fails at the token exchange, which reads like
a provider-side fault.

The rest goes into `.env`:

```
OIDC_ENABLED=true
OIDC_CLIENT_ID=<the client id>
OIDC_REDIRECT_URL=https://orchestrator.example.com/api/auth/oidc/callback
# The community compose defaults OIDC_ISSUER_URL to https://accounts.google.com
# (the backend itself has no default issuer); set it only for another provider.
# OIDC_PROVIDER_NAME is the name the sign-in button and the account page show
# ("Sign in with <name>"). Empty keeps "Google" for Google's issuer and says
# "single sign-on" for any other (its buttons read "Sign in with your
# identity provider"); set it when using another provider.
# OIDC_AUTO_PROVISION is pinned to false in the compose file.
```

Recreate the backend container so it re-reads its environment and files.

## 3. Verify

- `curl https://orchestrator.example.com/api/auth/oidc/config` → `200` with `"provider":"Google"` and `"sign_up":"invite_code"` (`"closed"` with no invite code configured).
- Login page shows the **Sign in with Google** button, plus an optional **invite code** field only when an invite code is configured.
- New user + correct code (the live value of `SERVER_INVITE_CODE`, delivered as a
  Docker secret; never write it into a committed file) → account created and
  logged in.
- New user + no/wrong code → redirected back with `invite_required` ("An invite
  code is required to sign up…").
- Existing user whose Google account is already linked → logs in without a code.
- Existing user, Google account not linked yet → see the security note below.

## Security note — a sign-in never links an existing account on its own

A Google identity is attached to an existing local account only with proof of
that account, never as a side effect of signing in — whether or not the
account's email is verified. Google vouches for the mailbox, and the mailbox is
not the account: an implicit link would let whoever can read it (or can get a
Google identity minted for that address) in without the password and without
any second factor the owner enrolled, through a sign-in method that outlives a
password change.

When a Google login matches a local account it is not linked to, the callback
returns to the sign-in page with a one-time link code, and the page asks for
that account's password (`POST /auth/oidc/link-existing`). A correct password
links Google, stamps `email_verified_at` and signs in; later logins with the
same Google account are then direct. The code is single-use and redeemable only
in the browser that finished the Google round-trip, so a wrong password means
starting again. An account that has a second factor enrolled is refused this
inline step (the password alone would bypass the factor), and an account
without a password (it signs in with a passkey or another provider) gets
`account_exists`; both link from *Account settings* after a normal sign-in,
which is also the alternative for anyone. OIDC-provisioned users are verified
from day one (the IdP asserts the email).

Email-ownership verification on password signup itself exists and is wired
(`domain/email_verification`, its service and repository, and the routes
`POST /auth/verify-email` + `POST /api/v1/users/me/resend-verification`
registered from `router.go`); it needs the install's outbound mail sender
configured. What is still open is narrower and is a product decision, not a
missing sender: **no route yet REQUIRES a verified email before a caller can
create billable resources** — a signup can adopt nodes, upload images and run
playbooks while `users.email_verified_at` is still NULL.
