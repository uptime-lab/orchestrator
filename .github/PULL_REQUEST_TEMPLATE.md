<!--
Thank you. The title and this description become the squash commit's message,
so write them as one: what changed and why. Keep it free of anything from a
live install (CONTRIBUTING.md → Publishing discipline).
-->

## What and why



## How it was tested

<!-- Unit tests, a local server, a device? Say what you could not exercise. -->



## Checklist

- [ ] No install facts or identifiers anywhere in this pull request — commits,
      branch name, description, screenshots, pasted logs: no device serial, MAC,
      hostname, real network address, tenant, account, person or key id; no
      deploy state; no description of an unfixed vulnerability.
- [ ] New or changed comments state the property that must hold and the
      reason, not a narration of the code or the history of how it was learned.
- [ ] Tests accompany the behaviour change, and a new test was run against the
      unfixed code once.
- [ ] Documentation that this change falsifies is fixed in the same pull
      request (`docs/`, READMEs, `make backend/docs` after a handler change,
      `make proto/generate` after a protocol change).
- [ ] Third-party material, if any, is named in the description above
      (CONTRIBUTING.md → Third-party material).
- [ ] I understand which licence the directories I touched carry (Apache-2.0
      for `agent/`, `proto/`, `tools/`; FCL-1.0-ALv2 elsewhere) and I have not
      copied code from an FCL directory into an Apache one.
- [ ] `python3 tools/publication-guard/check_public_shape.py --range origin/main..HEAD`
      is clean, or every opt-out carries its reason.
