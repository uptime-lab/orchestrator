# Changelog

All notable changes to Uptime Orchestrator are recorded here, by platform
release (`platform-v<version>`) and agent release (`agent-v<version>`), newest
first. The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

## Unreleased

### Changed

- The installer asks a question again when the answer is refused, with the
  reason, instead of ending; a wrong flag still ends the run. Declining at
  "Install now?" is no longer printed as an error. In the public shape a
  typed name that does not resolve yet is confirmed before it is kept.

### Fixed

- The installer no longer prints git's "is not a commit" warning while it
  fetches the release.

## platform-v0.2.0 — 2026-11-24

The first public release.
