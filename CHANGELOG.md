# Changelog

All notable changes to Uptime Orchestrator are recorded here, by platform
release (`platform-v<version>`) and agent release (`agent-v<version>`), newest
first. The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

## Unreleased

## platform-v0.2.1 — 2026-11-25

Nothing to do on upgrade beyond the steps of `docs/INSTALL.md`, "Upgrading".

### Changed

- Dependencies: eight Go modules of the backend and the agent, eight
  frontend packages (minor and patch versions) and the build images.
- The installer asks a question again when the answer is refused, with the
  reason, instead of ending; a wrong flag still ends the run. Declining at
  "Install now?" is no longer printed as an error. In the public shape a
  typed name that does not resolve yet is confirmed before it is kept.

### Fixed

- On an install with `TERMS_URL` set, only the e-mail sign-up form asked for
  the terms: an account made through an identity provider or by an
  invitation was never asked. A signed-in user with no stored acceptance is
  now asked once, in a dialog, and the acceptance is recorded as before.
- The publication guard's shape check, on a diff, read a file name with a
  space in it with git's trailing TAB attached and did not read a quoted
  (non-ASCII) name at all: the first was judged as prose whatever its
  suffix, which reported property chains in the Postman collections as
  host names; lines of the second were not checked.
- The installer no longer prints git's "is not a commit" warning while it
  fetches the release.

## platform-v0.2.0 — 2026-11-24

The first public release.
