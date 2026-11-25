# Publication guard

Checks that keep this tree publishable. They run in CI
(`.github/workflows/guard.yml`, which carries no path filter: documentation is
exactly where an identifier lands) on every push to `main`, every pull request,
including one from a fork, and every merge-queue run: none of them needs a
secret, a network connection or anything outside this directory and the tree
itself.

**This checker sees shapes only.** It knows what a device serial, a private
address, a vendor MAC prefix, a home directory or a tracking identifier
looks like; it does not know any real value. A separate exact-list check that
does know the real values exists outside the public repository and is the
check that decides a merge. A clean run here is necessary, not sufficient.

## `check_public_shape.py`

Flags identifier shapes that do not belong in a public tree. Each rule carries
a one-line reason in the code; the names are what the opt-out marker uses.

| rule | what it flags |
|---|---|
| `serial-shape` | a 16-hex token (a device serial, prefixed or not) unless it is an obviously synthetic shape listed in `allow-synthetic.txt`; a node name carrying an 8-hex serial tail |
| `private-ip` | RFC 1918 and CGNAT literals outside the generic example subnets (the first /24 of each block); any public address outside the small example set; RFC 5737 documentation ranges, loopback, link-local, multicast and netmask-shaped literals pass |
| `mac-real-oui` | a MAC address carrying the device vendor's registered prefix unless its NIC half is a listed placeholder; locally administered addresses pass |
| `home-path` | a macOS home directory, or a Linux home directory naming anyone but a documented placeholder (`user`, `example`, `username`, `<user>`) |
| `email-domain` | a mailbox whose domain is not an example or reserved domain listed in `allow-domains.txt`; dotless right-hand sides (`@v4`, `@sha256`, `@latest`) are pins, not mailboxes, and pass. An `example.*` entry means `example.<tld>` and hosts under it, never an `example` label in the middle of another name |
| `internal-hostname` | a fully qualified name under a real top-level domain whose registrable domain is not in `allow-domains.txt`; `.local`, `.lan`, `.internal` and the like are not real TLDs and pass |
| `id-token` | the phrasing of a private engineering journal: finding ids, numbered backlog entries, audit rounds, dated measurements and incidents |
| `large-file` | any file over 1 MiB |
| `executable-magic` | ELF, Mach-O or PE magic, or an executable bit on a binary file without a shebang |
| `gitattributes` | a `.gitattributes` (any directory) that sets `-diff`, `binary`, `diff=`, `filter=` or `export-ignore`: each hides changes from a diff-based scan, or a path from `git archive`. It cannot be opted out |

The text rules skip generated and vendored content — the lockfiles and
`THIRD-PARTY-NOTICES.md` files at the exact paths the build writes them
(`TEXT_SKIP_PATHS`), the frontend's `frontend/dist/`, `*.pb.go` and images;
a file of the same name anywhere else is scanned. A `node_modules/` directory
beside a `package.json` is not walked; one anywhere else is. The two file
rules run on every file.

The allow lists are data, not code: `allow-synthetic.txt` holds the synthetic
serial families, placeholder MAC halves and example addresses;
`allow-domains.txt` holds the public domains accepted in hostnames and the
domains accepted in mailboxes (a vendor's site in a link is routine, a mailbox
at it is a person, so the two sets differ). A new well-known domain goes
there, with a comment, never into the checker.

### Running it

```sh
# the whole tree, as CI does on a pull request
python3 tools/publication-guard/check_public_shape.py --tree .

# only the files git tracks (what an export sees)
git ls-files | python3 tools/publication-guard/check_public_shape.py --files - --root .

# a commit, a range, the index, or a message (hook use)
python3 tools/publication-guard/check_public_shape.py --commit HEAD
python3 tools/publication-guard/check_public_shape.py --range origin/main..HEAD
python3 tools/publication-guard/check_public_shape.py --staged
git log -1 --format=%B | python3 tools/publication-guard/check_public_shape.py --stdin

# counts per rule and the most-hit files, without the findings themselves
python3 tools/publication-guard/check_public_shape.py --tree . --quiet --stats 15
```

Output is one finding per line, `path:line:rule: message`; `--format github`
emits workflow annotations: an error per finding, a warning per opt-out and a
notice with the opt-out count. Exit status is 0 when clean, 1 on findings, 2 on
misuse. Commit modes check the lines a commit adds, the blobs it writes and
its message; renames are not detected, so a file moved and edited in one
commit is checked whole at its new path. Every diff is read with `--text
--no-textconv --no-ext-diff`, so attributes already in the history cannot
turn a change into "Binary files differ". A path is read as git wrote it
and then cleaned: the TAB git adds after a name with a space is dropped and
a quoted name (one with a byte outside printable ASCII) is unquoted, so a
file is judged by its own suffix and its own allow entries in every mode.

### Opting out of a documented false positive

Put the marker on the line itself, naming the rule:

```
some text that trips a rule   # publication-guard: allow private-ip
```

Several rules are comma-separated. Every opt-out is counted and listed in the
run summary, and in `--format github` each is a warning annotation on its
line, so that a reviewer sees each one on the pull request; an opt-out without a reason next
to it is a finding in review, not a fix. For a path that is itself the subject
of a rule, such as a documentation line that explains the `/Users/` rule, <!-- publication-guard: allow home-path -->
the marker is the right tool.

A finding with no line of its own to carry a marker -- a file rule on a
prebuilt binary, or a line of a third-party notice that must be reproduced
unaltered -- is silenced in `--tree` mode by `.publication-guard-allow` at the
root of the scanned tree, one `<path> <rule>[,<rule>]  # reason` per line. The
reason is required, each use is listed as an opt-out exactly like a marker, an
entry that silences nothing fails the run with exit 2, and `gitattributes`
cannot be listed.
`--commit` and `--range` apply the file as committed at the commit, or at the
last commit of the range, to the size rule only: a generated file the tip
exempts is the same file in the commits that lead to it.

## `check_proto_copies.sh`

The generated gRPC code is one artifact kept in three places (`proto/` and the
two Go modules, which share no workspace). The script reads the locations from
the proto Makefile, asserts that every generated file exists in all three, is
byte-identical and carries the same generator versions in its header, and
prints the versions it found. When a version pin exists
(`PROTOC_GEN_GO_VERSION` / `PROTOC_GEN_GO_GRPC_VERSION` in the Makefile or the
environment) the headers must match it; without one the script says so and
asserts only agreement between the copies.

```sh
bash tools/publication-guard/check_proto_copies.sh
```

## `check_trust_stores.sh`

The agent-signing key set is kept in three places: `signing/trusted-keys/`,
the copy the agent embeds (`agent/pkg/signing/keys/`) and the copy the server
embeds (`backend/pkg/webapp/infrastructure/signing/keys/`). The script asserts
that the three hold the same non-empty set of `*.pem` files, byte for byte, so
a key rotation that misses one copy fails CI instead of producing a release
the server imports and the devices refuse, or the reverse.

```sh
bash tools/publication-guard/check_trust_stores.sh
```

## `check_doc_citations.py`

Every `docs/<NAME>.md` cited from code or documentation must exist in the tree
being checked. Bare citations in prose (`` `<NAME>.md` ``, `see <NAME>.md`)
are resolved against `docs/`, the citing file's directory and the root.
`--allow FILE` lists citations that deliberately point at a document kept
elsewhere; the default is none.

```sh
python3 tools/publication-guard/check_doc_citations.py --root .
```

## Tests

```sh
python3 -m unittest discover -s tools/publication-guard -p '*_test.py'
bash tools/publication-guard/check_proto_copies_test.sh
bash tools/publication-guard/check_trust_stores_test.sh
```

The test fixtures that must trip a rule are assembled at run time, so the test
sources carry none of the shapes they test for and this directory passes its
own checks.

## `check_licence_boundary.py`

Reads `REUSE.toml` and asserts that every tracked path resolves to a licence,
that the Apache-2.0 directories (`agent/`, `proto/`, `tools/` —
`REUSE.toml`'s list and the checker's `DEFAULT_APACHE_DIRS`) hold nothing
else, and that a file's own
`SPDX-License-Identifier` header never disagrees with the map. Code may move
from the Apache directories into the FCL ones; the reverse is the licensor's
call alone, and this check is how the tree says so.

## `check_workflows.py`

Asserts that no workflow runs on a self-hosted runner, and that none triggers
on `pull_request_target` or `workflow_run` — the two events that run with the
base repository's secrets. A fork's pull request is evaluated with the fork's
copy of the workflow files, so this is a tripwire; the organisation's runner
settings are the boundary. A job's runner may come from the
`PUBLIC_CI_RUNS_ON` repository variable (or its `_AMD64` and `_ARM64` forms,
for a job that must build natively), which no file can check: it must never
name a self-hosted label on this repository.

A job counts as hosted only if EVERY label it can run on is a GitHub-hosted
one (`HOSTED_RUNNER`, actionlint 1.7.12's list): GitHub routes any other label
— `example-arm64`, `[linux, example-arm64]` — to self-hosted runners whether
or not `self-hosted` is listed. Scalar, flow-list and block-list forms are
read; the mapping form (`group:`/`labels:`) fails closed; an expression is
accepted, but its JSON-array fallback literal and the matrix values it names
(`${{ matrix.NAME }}`) are checked as labels.

It also fails a workflow that runs the `publication-guard` or
`private-identifier-guard` job and declares `paths` or `paths-ignore` on its
triggers (`guard-path-filter`): prose is where an identifier lands, so the
guard runs on every change, docs-only ones included.

## `private_identifiers_ci.py`

A byte-identical copy of the exact-list checker that lives outside the public
repository, shipped here so the `private-identifier-guard` job of `guard.yml`
(push to `main` and the merge queue) can run it from HMAC digests. The digests
and the key are ENVIRONMENT secrets of the `publication-guard` deployment
environment, whose branch policy admits `main` and the merge-queue refs
(`gh-readonly-queue/main/*`) only, never repository or organisation secrets: a
secret at either of those levels is readable by a job on any branch of the
repository. The job runs `main`'s copy of this file, not the one in the tree
under test: in the merge queue that tree is the queued pull request, whose
checker would otherwise run with the key in its environment. It carries no list and no key: without the digests it can
check nothing, and with them it prints a category and six hex characters per
finding, never a value. It hashes candidate words and phrases of each line
after Unicode and case folding, so it matches a listed value as a whole word or
phrase, not inside a longer word. On the first push of a one-commit
repository the job scans the tree and the root commit's message.

The digests do not fit one secret (GitHub limits a secret to 48 KB), so
`digest --out-dir` writes them as parts of at most 40,000 bytes, one
environment secret each, `PUBLICATION_GUARD_DIGESTS_1` up to `_12`; the
workflow declares that fixed set. Each tag is HMAC-SHA256 truncated to 128
bits. Every part opens with a header naming its index, the part count, the
total row count and a SHA-256 over all rows. `assemble_digests.sh` (also run
from `main`) joins the parts in order and fails on an empty part or a stale
one beyond the count; `verify-digests` and `check --digests` refuse a payload
whose headers, order, row count or hash do not agree. An unset secret is an
empty string, so a missing part fails the job (exit 2) and never shrinks the
table. `assemble_digests_test.py` drives the script and the checker the way the
workflow does.
