#!/usr/bin/env python3
"""Layer-1 publication guard: the private exact-identifier checker.

It loads `private-identifiers.txt` (one `<category>\\t<token>\\t<note>` per
line) and reports every place a listed identifier, or a form derived from one,
appears in a tree, a commit, a range, the index, a file list or stdin.

Two ways to run it:

  check  --list L  <source>           plaintext: prints `path:line:category: <token>`
  digest --list L --key-env K --out F writes HMAC-SHA256/128(key, form) per derived form
  digest --list L --key-env K --out-dir D
                                      the same payload split into the bounded parts
                                      D/PUBLICATION_GUARD_DIGESTS_<i>, one per CI secret
  digest --list L --self-check        exit 1 if an entry could never match in digest mode
  verify-digests --digests F          exit 0 only if the (reassembled) payload is whole
  check  --digests F --key-env K <source>
                                      CI mode: prints `path:line:category: hmac:<6 hex>`,
                                      never a token; the list itself is never published.

Sources: --tree DIR | --commit REV | --range A..B | --staged | --stdin | --files F...
Exit codes: 0 clean, 1 findings, 2 misuse, a list line that fails to parse, or a
digest payload that does not verify (a missing, truncated, reordered or stale
part). A malformed list or payload must never silently narrow the guard, hence
the hard stop.

Derived forms, by category (all matching is case-insensitive):
  serial16   the 16-hex serial as a whole hex word, its 8-hex tail as a whole
             hex word, `node-<tail>` and `rpi-<tail>`. An 8-hex serial16 entry
             is accepted as a tail-only entry when only the tail is known.
  mac        `aa:bb:cc:dd:ee:ff` with `:` or `-` separators, and bare 12-hex.
  ipv4       exact dotted quad.  cidr: any IPv4 literal inside the network.
  hostname / domain
             the label sequence as a whole-label run of any dotted name: itself,
             a subdomain of it, an FQDN it starts, or with labels added after.
  email / uuid
             exact (uuid also as bare 32-hex).
  person / account / tenant / handle
             whole words, the parts joined by space, `.`, `_` or `-`.
  path       exact prefix of an absolute path.
  fingerprint / token / keyid / misc
             exact substring.

Digest mode cannot express a range or a prefix: a cidr of /20 or smaller is
expanded to its addresses, a larger one is skipped with a warning; hostnames
and paths rely on the tokeniser emitting every dotted suffix and every path
prefix as a candidate; a substring category is matched only when it is a
candidate token on its own (a base64 word with `+`/`=`, or a phrase of up to
four words in any script, NFKC- and case-folded, brackets and punctuation
dropped) -- never inside a longer word, which only plaintext mode sees.
`digest --self-check` proves every entry has at least one candidate shape.
"""
from __future__ import annotations

import argparse
import hashlib
import hmac
import ipaddress
import os
import re
import subprocess
import sys
import unicodedata
import uuid as uuidlib
from dataclasses import dataclass
from pathlib import Path
from typing import Callable, Iterable, Iterator, Sequence

CATEGORIES = frozenset(
    {
        "serial16", "mac", "ipv4", "cidr", "hostname", "email", "domain", "person",
        "account", "tenant", "handle", "keyid", "path", "uuid", "fingerprint", "token", "misc",
    }
)
WORD_CATEGORIES = frozenset({"person", "account", "tenant", "handle"})
SUBSTRING_CATEGORIES = frozenset({"fingerprint", "token", "keyid", "misc"})

DEFAULT_LIST = Path(__file__).resolve().with_name("private-identifiers.txt")
BINARY_SNIFF_BYTES = 8192
LARGE_BINARY_BYTES = 1 << 20
# A /20: a listed network up to this size is enumerated into the digest file,
# so any address inside it matches (4096 HMACs at most per entry). A wider network
# cannot be enumerated usefully and is skipped with a warning, which
# `digest --self-check` turns into a failure.
MAX_DIGEST_CIDR_HOSTS = 4096

HEX_CHARS = frozenset("0123456789abcdef")
WORD_CHARS = frozenset("abcdefghijklmnopqrstuvwxyz0123456789")
LEAD_HOST = WORD_CHARS | {"-"}
LEAD_EMAIL = WORD_CHARS | set("._+-")
LEAD_PATH = WORD_CHARS | set("_./-")
LEAD_UUID = HEX_CHARS | {"-"}

JOIN_RE = re.compile(r"[\s._-]+")
JOIN_PATTERN = r"[ \t._-]+"
HEX16_RE = re.compile(r"^[0-9a-f]{16}$")
HEX8_RE = re.compile(r"^[0-9a-f]{8}$")
MAC_RE = re.compile(r"^([0-9a-f]{2})[:-]?([0-9a-f]{2})[:-]?([0-9a-f]{2})[:-]?([0-9a-f]{2})[:-]?([0-9a-f]{2})[:-]?([0-9a-f]{2})$")
OCTET = r"(?:25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)"
IPV4_LITERAL_RE = re.compile(rf"(?<![0-9])(?<![0-9]\.)({OCTET}(?:\.{OCTET}){{3}})(?![0-9])(?!\.[0-9])")


class ListError(Exception):
    """A list line that cannot be trusted to mean what it says."""


@dataclass(frozen=True)
class Entry:
    category: str
    token: str  # normalised
    note: str
    line_no: int


@dataclass(frozen=True)
class Form:
    """One matchable spelling derived from an entry.

    `text` is the normalised string a digest is taken over. `pattern` matches it
    in lowercased text, trailing boundary included; `lead` is the set of
    characters that must NOT precede the match (the leading boundary is checked
    in Python so the alternation keeps its literal first characters, which is
    what lets `re` skip positions quickly).
    """

    entry: Entry
    text: str
    pattern: str | None
    lead: frozenset[str]


@dataclass(frozen=True)
class Finding:
    label: str
    line: int
    category: str
    message: str

    def render(self) -> str:
        return f"{self.label}:{self.line}:{self.category}: {self.message}"


# --------------------------------------------------------------------------- list


def normalise(category: str, raw: str, line_no: int) -> str:
    token = raw.strip().lower()
    if not token:
        raise ListError(f"line {line_no}: empty token")
    if category == "serial16":
        if not (HEX16_RE.match(token) or HEX8_RE.match(token)):
            raise ListError(f"line {line_no}: serial16 must be 16 hex characters (or an 8-hex tail)")
        return token
    if category == "mac":
        m = MAC_RE.match(token)
        if not m:
            raise ListError(f"line {line_no}: mac must be six hex pairs")
        return ":".join(m.groups())
    if category == "ipv4":
        try:
            return str(ipaddress.IPv4Address(token))
        except ValueError as exc:
            raise ListError(f"line {line_no}: ipv4: {exc}") from exc
    if category == "cidr":
        try:
            return str(ipaddress.IPv4Network(token, strict=False))
        except ValueError as exc:
            raise ListError(f"line {line_no}: cidr: {exc}") from exc
    if category in ("hostname", "domain"):
        if not re.fullmatch(r"[a-z0-9-]+(?:\.[a-z0-9-]+)*", token):
            raise ListError(f"line {line_no}: {category} must be dotted labels")
        return token
    if category == "email":
        if token.count("@") != 1 or token.startswith("@") or token.endswith("@"):
            raise ListError(f"line {line_no}: email must contain one @")
        return token
    if category in WORD_CATEGORIES:
        parts = [p for p in JOIN_RE.split(token) if p]
        if not parts or not all(re.fullmatch(r"[a-z0-9]+", p) for p in parts):
            raise ListError(f"line {line_no}: {category} must be words joined by space, '.', '_' or '-'")
        return " ".join(parts)
    if category == "path":
        if not token.startswith("/") or len(token) < 2:
            raise ListError(f"line {line_no}: path must be absolute")
        return token.rstrip("/")
    if category == "uuid":
        try:
            return str(uuidlib.UUID(token))
        except ValueError as exc:
            raise ListError(f"line {line_no}: uuid: {exc}") from exc
    return token  # substring categories: taken verbatim, lowercased


def parse_list(text: str) -> list[Entry]:
    entries: list[Entry] = []
    for line_no, raw in enumerate(text.splitlines(), start=1):
        line = raw.rstrip("\n")
        if not line.strip() or line.lstrip().startswith("#"):
            continue
        if "\t" not in line:
            raise ListError(f"line {line_no}: expected <category>\\t<token>[\\t<note>] (tab-separated)")
        fields = line.split("\t")
        category = fields[0].strip().lower()
        if category not in CATEGORIES:
            raise ListError(f"line {line_no}: unknown category {category!r}")
        if len(fields) < 2:
            raise ListError(f"line {line_no}: missing token")
        note = fields[2].strip() if len(fields) > 2 else ""
        entries.append(Entry(category, normalise(category, fields[1], line_no), note, line_no))
    return entries


def load_list(path: Path) -> list[Entry]:
    try:
        text = path.read_text(encoding="utf-8")
    except OSError as exc:
        raise ListError(f"cannot read list {path}: {exc}") from exc
    return parse_list(text)


# --------------------------------------------------------------------------- forms


def _hexword(entry: Entry, text: str, lead: frozenset[str] = HEX_CHARS) -> Form:
    return Form(entry, text, re.escape(text) + r"(?![0-9a-f])", lead)


def forms_for(entry: Entry) -> list[Form]:
    c, t = entry.category, entry.token
    if c == "serial16":
        tail = t[-8:]
        out = [_hexword(entry, t)] if len(t) == 16 else []
        out.append(_hexword(entry, tail))
        out.append(_hexword(entry, "node-" + tail, WORD_CHARS))
        out.append(_hexword(entry, "rpi-" + tail, WORD_CHARS))
        return out
    if c == "mac":
        pairs = t.split(":")
        sep = r"[:-]".join(re.escape(p) for p in pairs)
        return [
            Form(entry, t, sep + r"(?![0-9a-f])", HEX_CHARS),
            _hexword(entry, "".join(pairs)),
        ]
    if c in ("ipv4", "cidr"):
        return [Form(entry, t, None, frozenset())]  # handled by the IPv4 literal pass
    if c in ("hostname", "domain"):
        # A whole-label run of any dotted name: a subdomain, the first label
        # of an FQDN, or followed by another label. The digest tokeniser
        # emits every contiguous label run, so both modes share this rule.
        return [Form(entry, t, re.escape(t) + r"(?![a-z0-9-])", LEAD_HOST)]
    if c == "email":
        return [Form(entry, t, re.escape(t) + r"(?![a-z0-9-])", LEAD_EMAIL)]
    if c in WORD_CATEGORIES:
        parts = t.split(" ")
        pattern = JOIN_PATTERN.join(re.escape(p) for p in parts) + r"(?![a-z0-9])"
        return [Form(entry, t, pattern, WORD_CHARS)]
    if c == "path":
        return [Form(entry, t, re.escape(t), LEAD_PATH)]
    if c == "uuid":
        return [
            Form(entry, t, re.escape(t) + r"(?![0-9a-f-])", LEAD_UUID),
            _hexword(entry, t.replace("-", "")),
        ]
    return [Form(entry, t, re.escape(t), frozenset())]


# --------------------------------------------------------------------------- matchers


class Matcher:
    """Finds listed identifiers in text. Subclassed for the digest mode."""

    def findings_in_text(self, text: str) -> list[tuple[int, str, str]]:
        """Return (offset, category, message) for every hit in `text` (any case)."""
        raise NotImplementedError

    def scan_blob(self, label: str, text: str) -> list[Finding]:
        hits = sorted(self.findings_in_text(text))
        out: list[Finding] = []
        line, last = 1, 0
        for pos, category, message in hits:
            line += text.count("\n", last, pos)
            last = pos
            out.append(Finding(label, line, category, message))
        return out

    def scan_lines(self, label: str, lines: Iterable[tuple[int, str]]) -> list[Finding]:
        out: list[Finding] = []
        for line_no, text in lines:
            for _, category, message in sorted(self.findings_in_text(text)):
                out.append(Finding(label, line_no, category, message))
        return out


class PlaintextMatcher(Matcher):
    def __init__(self, entries: Sequence[Entry]) -> None:
        by_pattern: dict[str, None] = {}
        self.by_text: dict[str, list[Form]] = {}
        self.ipv4: dict[str, Entry] = {}
        self.nets: list[tuple[int, int, int, Entry]] = []  # (network, mask, prefixlen, entry)
        for entry in entries:
            for form in forms_for(entry):
                if entry.category == "ipv4":
                    self.ipv4.setdefault(form.text, entry)
                elif entry.category == "cidr":
                    net = ipaddress.IPv4Network(form.text)
                    self.nets.append((int(net.network_address), int(net.netmask), net.prefixlen, entry))
                else:
                    assert form.pattern is not None
                    by_pattern[form.pattern] = None
                    self.by_text.setdefault(form.text, []).append(form)
        # Longest first so that at one position the fullest spelling wins.
        patterns = sorted(by_pattern, key=lambda p: -len(p))
        self.rx = re.compile("|".join(f"(?:{p})" for p in patterns)) if patterns else None
        self.nets.sort(key=lambda n: -n[2])  # most specific network first

    def _lookup(self, matched: str) -> list[Form]:
        for key in (matched, matched.replace("-", ":"), JOIN_RE.sub(" ", matched)):
            forms = self.by_text.get(key)
            if forms:
                return forms
        return []

    def findings_in_text(self, text: str) -> list[tuple[int, str, str]]:
        """Hits in the lowercased text and, for non-ASCII text, in its `fold`ed
        spelling too -- the one normalisation digest mode applies (NFKC, accents
        dropped, casefolded), so a fullwidth or otherwise compatibility-spelled
        token is found by the hooks exactly when CI's digest scan finds it.
        Folding can change a line's length, so a folded
        hit is reported at the start of its ORIGINAL line: callers count lines
        from the offset, nothing else."""
        out = self._hits(text.lower())
        if text.isascii():
            return out
        starts = [0]
        for line in text.split("\n")[:-1]:
            starts.append(starts[-1] + len(line) + 1)
        have = {(text.count("\n", 0, pos), category, message) for pos, category, message in out}
        for index, line in enumerate(text.split("\n")):
            if line.isascii():
                continue
            for _, category, message in self._hits(fold(line)):
                if (index, category, message) not in have:
                    have.add((index, category, message))
                    out.append((starts[index], category, message))
        return out

    def _hits(self, low: str) -> list[tuple[int, str, str]]:
        out: list[tuple[int, str, str]] = []
        seen: set[tuple[int, int]] = set()
        if self.rx is not None:
            for m in self.rx.finditer(low):
                matched = m.group(0)
                prev = low[m.start() - 1] if m.start() > 0 else ""
                for form in self._lookup(matched):
                    if prev and prev in form.lead:
                        continue
                    key = (m.start(), form.entry.line_no)
                    if key in seen:
                        continue
                    seen.add(key)
                    message = form.entry.token
                    if form.text != form.entry.token:
                        message += f" via {matched}"
                    out.append((m.start(), form.entry.category, message))
        if self.ipv4 or self.nets:
            for m in IPV4_LITERAL_RE.finditer(low):
                literal = m.group(1)
                entry = self.ipv4.get(literal)
                if entry is None:
                    addr = int(ipaddress.IPv4Address(literal))
                    for network, mask, _, candidate in self.nets:
                        if addr & mask == network:
                            entry = candidate
                            break
                if entry is None:
                    continue
                message = entry.token if entry.category == "ipv4" else f"{entry.token} via {literal}"
                out.append((m.start(1), entry.category, message))
        return out


# --------------------------------------------------------------------------- digests


def digest_forms(entries: Sequence[Entry], warn: Callable[[str], None]) -> list[tuple[str, str]]:
    """(category, normalised text) for every form a digest is taken over.

    Every text is `fold`ed and capped at MAX_PHRASE_WORDS words, the shapes
    the candidate tokeniser emits. A token the word tokeniser cannot emit
    whole (spaces, brackets, another script) also gets its phrase form.
    """
    out: list[tuple[str, str]] = []
    for entry in entries:
        if entry.category == "cidr":
            net = ipaddress.IPv4Network(entry.token)
            if net.num_addresses > MAX_DIGEST_CIDR_HOSTS:
                warn(f"list line {entry.line_no}: cidr /{net.prefixlen} is larger than a /20 and cannot be digested; skipped")
                continue
            out.extend(("cidr", str(a)) for a in net)
            continue
        texts: dict[str, None] = {}
        for form in forms_for(entry):
            text = fold(form.text)
            if " " in text:
                text = " ".join(text.split(" ")[:MAX_PHRASE_WORDS])
            texts[text] = None
        if entry.category in SUBSTRING_CATEGORIES and fold(entry.token) not in line_candidates(entry.token):
            phrase = phrase_form(entry.token)
            if phrase:
                texts[phrase] = None
        out.extend((entry.category, text) for text in texts)
    return out


# The tag is HMAC-SHA256 truncated to its first 128 bits (32 hex). That halves
# the payload, and it costs no security that matters here: a truncated HMAC is
# still a PRF (RFC 2104 section 5 recommends keeping at least half the hash,
# which 128 of 256 bits is; NIST SP 800-107 allows truncation for MACs), so
# without the key a row reveals nothing and a guess is no more checkable than
# before. With the key the file is an offline oracle at ANY tag length -- the
# list's small categories fall to a brute force over the inputs, not over the
# tag -- so the full 256 bits never protected anything the truncation gives
# up. The one property the length does carry is a false hit: a candidate word
# colliding with a listed form, about rows / 2**128 per word, i.e. never.
DIGEST_TAG_HEX = 32

# The payload travels to CI as GitHub environment secrets, and one secret is
# limited to 48 KB, and a list that enumerates networks address by address
# outgrows one secret quickly. So it is split into parts of at most MAX_DIGEST_PART_BYTES -- a margin under
# the limit whether GitHub means 48,000 or 49,152 -- each one secret named
# DIGEST_PART_PREFIX + <i>. MAX_DIGEST_PARTS is the fixed set of names
# `.github/workflows/guard.yml` declares and `assemble_digests.sh` reads (a
# workflow cannot enumerate secret names); generation refuses a payload that
# needs more, and the tests pin all three to one number.
MAX_DIGEST_PART_BYTES = 40_000
MAX_DIGEST_PARTS = 12
DIGEST_PART_PREFIX = "PUBLICATION_GUARD_DIGESTS_"

# Every part starts with one header line naming its place and the WHOLE
# payload: `#! publication-guard-digests/v2 part=<i>/<n> rows=<r> sha256=<h>
# key=<k>`, where <h> is SHA-256 over the rows of all parts in order, each
# followed by a newline, and <k> is the tag of the fixed text KEY_CHECK_TEXT
# under the key the rows were made with. `check --digests` refuses a key whose
# tag differs: a whole payload paired with another key (a half-done rotation,
# a mistyped secret) verifies as a payload yet matches nothing, which is a
# green job over a blind check. <k> reveals nothing about the key (a PRF output
# of a public input). An unset secret expands to the empty string, so a missing,
# truncated, reordered, duplicated or stale part must be a hard failure and
# never a smaller table: `load_digests` refuses anything that does not
# reassemble into exactly n parts, r rows and that hash. A file with no
# header at all (the v1 format, or an empty secret) is refused the same way.
DIGEST_FORMAT = "publication-guard-digests/v2"
DIGEST_HEADER_RE = re.compile(
    r"^#! publication-guard-digests/v2 part=([1-9][0-9]*)/([1-9][0-9]*) rows=([0-9]+) sha256=([0-9a-f]{64})"
    rf" key=([0-9a-f]{{{DIGEST_TAG_HEX}}})$"
)
KEY_CHECK_TEXT = "publication-guard-digests/v2 key check"
DIGEST_ROW_RE = re.compile(rf"^([a-z0-9]+)\t([0-9a-f]{{{DIGEST_TAG_HEX}}})$")
DIGEST_HEADER_RESERVE = 200  # bytes a header line can take, with room to spare


def hmac_hex(key: bytes, text: str) -> str:
    return hmac.digest(key, text.encode("utf-8"), "sha256").hex()[:DIGEST_TAG_HEX]


def digest_rows(entries: Sequence[Entry], key: bytes, warn: Callable[[str], None]) -> list[str]:
    """`<category>\\t<tag>` per derived form, in list order, duplicates dropped."""
    rows: dict[str, None] = {}
    for category, text in digest_forms(entries, warn):
        rows[f"{category}\t{hmac_hex(key, text)}"] = None
    return list(rows)


def render_digest_parts(
    rows: Sequence[str], key_check: str, max_part_bytes: int | None, max_parts: int | None
) -> list[str]:
    """The payload as part texts, each starting with its header line.

    `max_part_bytes=None` renders one part (a local file); otherwise every part,
    header included, is at most that many bytes, and more than `max_parts`
    parts is refused rather than producing a part no secret name carries.
    """
    if not rows:
        raise ListError("the list yields no digest at all; an empty payload would guard nothing")
    body_hash = hashlib.sha256("".join(row + "\n" for row in rows).encode("utf-8")).hexdigest()
    chunks: list[list[str]] = [[]]
    if max_part_bytes is None:
        chunks = [list(rows)]
    else:
        budget = max_part_bytes - DIGEST_HEADER_RESERVE
        used = 0
        for row in rows:
            size = len(row.encode("utf-8")) + 1
            if chunks[-1] and used + size > budget:
                chunks.append([])
                used = 0
            chunks[-1].append(row)
            used += size
    n = len(chunks)
    if max_parts is not None and n > max_parts:
        raise ListError(f"the payload needs {n} parts of at most {max_part_bytes} bytes, more than the "
                        f"{max_parts} secret names the workflow declares; raise MAX_DIGEST_PARTS together with "
                        f"guard.yml and assemble_digests.sh")
    parts = []
    for i, chunk in enumerate(chunks, start=1):
        header = f"#! {DIGEST_FORMAT} part={i}/{n} rows={len(rows)} sha256={body_hash} key={key_check}"
        text = header + "\n" + "".join(row + "\n" for row in chunk)
        if max_part_bytes is not None and len(text.encode("utf-8")) > max_part_bytes:
            raise ListError(f"digest part {i}/{n} is {len(text.encode('utf-8'))} bytes, over {max_part_bytes}")
        parts.append(text)
    return parts


def write_digests(
    entries: Sequence[Entry],
    key: bytes,
    out_path: Path | None,
    warn: Callable[[str], None],
    out_dir: Path | None = None,
) -> tuple[int, list[Path]]:
    """Writes the payload as one file (`out_path`) or as bounded parts (`out_dir`)."""
    rows = digest_rows(entries, key, warn)
    if out_dir is not None:
        parts = render_digest_parts(rows, hmac_hex(key, KEY_CHECK_TEXT), MAX_DIGEST_PART_BYTES, MAX_DIGEST_PARTS)
        out_dir.mkdir(parents=True, exist_ok=True)
        if any(out_dir.iterdir()):
            # A stale part left beside fresh ones would be uploaded with them.
            raise ListError(f"{out_dir} is not empty; write the parts into an empty directory")
        written = []
        for i, text in enumerate(parts, start=1):
            path = out_dir / f"{DIGEST_PART_PREFIX}{i}"
            path.write_text(text, encoding="utf-8")
            written.append(path)
        return len(rows), written
    assert out_path is not None
    (text,) = render_digest_parts(rows, hmac_hex(key, KEY_CHECK_TEXT), None, None)
    out_path.write_text(text, encoding="utf-8")
    return len(rows), [out_path]


@dataclass(frozen=True)
class DigestPayload:
    table: dict[str, str]
    parts: int
    rows: int
    key_check: str


def parse_digests(text: str, label: str) -> DigestPayload:
    """Verifies a (reassembled) payload; any doubt is a ListError, never a smaller table."""
    headers: list[tuple[int, int, int, str, str]] = []
    rows: list[str] = []
    table: dict[str, str] = {}
    for line_no, raw in enumerate(text.splitlines(), start=1):
        line = raw.strip()
        if not line:
            continue
        if line.startswith("#!"):
            m = DIGEST_HEADER_RE.match(line)
            if not m:
                raise ListError(f"{label} line {line_no}: not a {DIGEST_FORMAT} part header")
            headers.append((int(m.group(1)), int(m.group(2)), int(m.group(3)), m.group(4), m.group(5)))
            continue
        if line.startswith("#"):
            continue
        if not headers:
            raise ListError(f"{label} line {line_no}: a digest row before any part header")
        m = DIGEST_ROW_RE.match(line)
        if not m or m.group(1) not in CATEGORIES:
            raise ListError(f"{label} line {line_no}: expected <category>\\t<{DIGEST_TAG_HEX} hex>")
        rows.append(line)
        table.setdefault(m.group(2), m.group(1))
    if not headers:
        raise ListError(f"{label}: no {DIGEST_FORMAT} header -- an empty secret, a truncated part 1 or a v1 file")
    _, n, total, body_hash, key_check = headers[0]
    for index, (i, n_i, total_i, hash_i, key_i) in enumerate(headers, start=1):
        if (n_i, total_i, hash_i, key_i) != (n, total, body_hash, key_check):
            raise ListError(f"{label}: part {i}/{n_i} belongs to another payload than part 1/{n} "
                            f"(a stale part from an earlier upload?)")
        if i != index:
            raise ListError(f"{label}: expected part {index}/{n}, found part {i}/{n_i} -- a part is missing, "
                            f"empty, duplicated or out of order")
    if len(headers) != n:
        raise ListError(f"{label}: {len(headers)} of {n} parts present -- a part is missing or empty")
    if len(rows) != total:
        raise ListError(f"{label}: {len(rows)} of {total} digest rows present -- a part is truncated")
    actual = hashlib.sha256("".join(row + "\n" for row in rows).encode("utf-8")).hexdigest()
    if actual != body_hash:
        raise ListError(f"{label}: the digest rows do not hash to the header's sha256 -- a part is corrupt")
    if total == 0:
        raise ListError(f"{label}: an empty payload guards nothing")
    return DigestPayload(table, n, total, key_check)


def load_digests(path: Path, key: bytes) -> dict[str, str]:
    """The verified table, refused unless `key` is the key the rows were made with."""
    try:
        text = path.read_text(encoding="utf-8")
    except OSError as exc:
        raise ListError(f"cannot read digests {path}: {exc}") from exc
    payload = parse_digests(text, f"digests {path}")
    if not hmac.compare_digest(hmac_hex(key, KEY_CHECK_TEXT), payload.key_check):
        raise ListError(f"digests {path}: the key is not the one these digests were made with "
                        f"(rotate the key and every part together)")
    return payload.table


# Candidate shapes for the digest check. Each emits the normalised spelling a
# digest was taken over, so the two modes agree on what counts as a hit.
# `+` and `=` belong to a word so that a base64 token is one candidate; `=`
# also splits, so `key=value` yields `value`.
CAND_WORD_RE = re.compile(r"[a-z0-9._:@/,+=-]+")
CAND_SPLIT_RE = re.compile(r"[._:@/,=-]+")
CAND_HEX8_RE = re.compile(r"(?<![0-9a-f])[0-9a-f]{8}(?![0-9a-f])")
CAND_HEX16_RE = re.compile(r"(?<![0-9a-f])[0-9a-f]{16}(?![0-9a-f])")
CAND_HEX32_RE = re.compile(r"(?<![0-9a-f])[0-9a-f]{32}(?![0-9a-f])")
CAND_MAC_RE = re.compile(r"(?<![0-9a-f])(?:[0-9a-f]{2}[:-]){5}[0-9a-f]{2}(?![0-9a-f])")
CAND_EMAIL_RE = re.compile(r"(?<![a-z0-9._+-])[a-z0-9._+-]+@[a-z0-9-]+(?:\.[a-z0-9-]+)+(?![a-z0-9-])")
CAND_HOST_RE = re.compile(r"(?<![a-z0-9.-])[a-z0-9-]+(?:\.[a-z0-9-]+)+(?![a-z0-9-])")
CAND_FP_RE = re.compile(r"(?:sha256|md5):[a-z0-9+/:]{20,}")
CAND_UUID_RE = re.compile(r"(?<![0-9a-f-])[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}(?![0-9a-f-])")
CAND_PATH_RE = re.compile(r"(?<![a-z0-9_./-])/[a-z0-9_.-]+(?:/[a-z0-9_.-]+)*")
# A phrase word: a run of letters or digits in ANY script (no underscore, so
# `jane_doe` is two words, as the plaintext join rule has it).
PHRASE_WORD_RE = re.compile(r"[^\W_]+")
CAND_STRIP = ".:/-@_,"
# Nothing on a list is shorter than this; dropping the short pieces early
# saves an HMAC per two-letter fragment of every identifier in a code tree.
MIN_CANDIDATE_LEN = 4
# Multi-word candidates are every run of 2..MAX_PHRASE_WORDS consecutive
# phrase words joined by one space. A list entry with more words is digested
# as its first MAX_PHRASE_WORDS words: a text carrying the whole phrase
# carries that prefix too.
MAX_PHRASE_WORDS = 4


def fold(text: str) -> str:
    """The case- and compatibility-folded text both digest sides agree on.

    NFKC-folded, accents dropped where the letter has an ASCII base, then
    casefolded. A letter with no ASCII base (another script) is kept, so a
    name written in it is still one candidate word.
    """
    if text.isascii():
        return text.lower()
    decomposed = unicodedata.normalize("NFKD", text)
    bare = "".join(ch for ch in decomposed if not unicodedata.combining(ch))
    return unicodedata.normalize("NFKC", bare).casefold()


def phrase_words(text: str) -> list[str]:
    return PHRASE_WORD_RE.findall(text)


def candidates(line: str) -> set[str]:
    """Every candidate spelling in `line`, which must already be `fold`ed."""
    out: set[str] = set()
    for m in CAND_WORD_RE.finditer(line):
        raw = m.group(0)
        word = raw.strip(CAND_STRIP)
        if len(word) < MIN_CANDIDATE_LEN:
            continue
        out.add(word)
        out.add(raw)
        pieces = [p for p in CAND_SPLIT_RE.split(word) if len(p) >= MIN_CANDIDATE_LEN]
        out.update(pieces)
        if len(pieces) > 1:
            out.add(" ".join(pieces))
    for rx in (CAND_HEX8_RE, CAND_HEX16_RE, CAND_HEX32_RE, CAND_UUID_RE, CAND_FP_RE, CAND_EMAIL_RE):
        out.update(m.group(0) for m in rx.finditer(line))
    out.update(m.group(0).replace("-", ":") for m in CAND_MAC_RE.finditer(line))
    out.update(m.group(1) for m in IPV4_LITERAL_RE.finditer(line))
    for m in CAND_HOST_RE.finditer(line):
        labels = m.group(0).split(".")
        out.update(".".join(labels[i:j]) for i in range(len(labels)) for j in range(i + 1, len(labels) + 1))
    for m in CAND_PATH_RE.finditer(line):
        segments = m.group(0).split("/")
        out.update("/".join(segments[: i + 1]) for i in range(1, len(segments)))
    # Phrase n-grams: names, multi-word misc entries, entries with brackets.
    words = phrase_words(line)
    if not line.isascii():
        out.update(w for w in words if not w.isascii())
    for n in range(2, MAX_PHRASE_WORDS + 1):
        for i in range(len(words) - n + 1):
            out.add(" ".join(words[i : i + n]))
    return out


def line_candidates(line: str) -> set[str]:
    return candidates(fold(line))


def phrase_form(token: str) -> str | None:
    """The phrase spelling of a token the word tokeniser cannot emit whole."""
    words = phrase_words(fold(token))
    if not words:
        return None
    return " ".join(words[:MAX_PHRASE_WORDS])


def self_check_samples(entry: Entry) -> list[str]:
    """Texts that MUST hit the entry in digest mode: the token alone and in prose."""
    token = entry.token
    if entry.category == "cidr":
        net = ipaddress.IPv4Network(token)
        token = str(net.network_address + 1) if net.num_addresses > 2 else str(net.network_address)
    return [token, f"see {token} here", f"({token})"]


def self_check(entries: Sequence[Entry]) -> list[str]:
    """One line per entry digest mode can never match; never the token itself."""
    problems: list[str] = []
    for entry in entries:
        skipped: list[str] = []
        texts = {text for _, text in digest_forms([entry], skipped.append)}
        if not texts:
            problems.append(f"list line {entry.line_no}: {entry.category}: no digest form at all ({'; '.join(skipped) or 'empty'})")
            continue
        for sample in self_check_samples(entry):
            if not (line_candidates(sample) & texts):
                shape = "".join(sorted({"a" if ch.isascii() and ch.isalnum() else "U" if ch.isalpha() else ch
                                        for ch in entry.token}))
                problems.append(f"list line {entry.line_no}: {entry.category}: unmatchable in digest mode "
                                f"(token shape {shape!r}, {len(entry.token)} chars)")
                break
    return problems


class DigestMatcher(Matcher):
    def __init__(self, table: dict[str, str], key: bytes) -> None:
        self.table = table
        self.key = key
        self.known_clean: set[str] = set()

    def findings_in_text(self, text: str) -> list[tuple[int, str, str]]:
        out: list[tuple[int, str, str]] = []
        offset = 0
        for line in text.split("\n"):
            for cand in candidates(fold(line)):
                if cand in self.known_clean:
                    continue
                digest = hmac_hex(self.key, cand)
                category = self.table.get(digest)
                if category is None:
                    self.known_clean.add(cand)
                    continue
                out.append((offset, category, f"hmac:{digest[:6]}"))
            offset += len(line) + 1
        return out


# --------------------------------------------------------------------------- sources


def is_binary(head: bytes) -> bool:
    return b"\0" in head


UTF16_BOMS = (b"\xff\xfe", b"\xfe\xff")
UTF32_BOMS = (b"\xff\xfe\x00\x00", b"\x00\x00\xfe\xff")


def text_views(data: bytes) -> list[str]:
    """Every decoding of `data` a listed identifier could hide behind.

    A file without a NUL and without a UTF-16 byte-order mark is one UTF-8
    view. Otherwise it is read as latin-1 (a NUL-prefixed text file keeps its
    ASCII intact), as UTF-16 in both byte orders, BOM or not, at both byte
    alignments, and as UTF-32 in both byte orders at all four alignments, and
    every view is scanned: a file that merely LOOKS binary is not evidence
    that it carries no identifier, and skipping it would let a UTF-16 file, a
    UTF-32 file or a text file with one leading NUL
    pass clean. A UTF-32 byte-order mark decodes to U+FEFF in its view and
    is otherwise harmless.
    """
    if b"\0" not in data and not data.startswith(UTF16_BOMS):
        return [data.decode("utf-8", errors="replace")]
    body = data[2:] if data.startswith(UTF16_BOMS) and not data.startswith(UTF32_BOMS) else data
    views = [data.decode("latin-1")]
    for codec in ("utf-16-le", "utf-16-be"):
        for offset in (0, 1):  # a stray odd byte before the text shifts the code units
            chunk = body[offset:]
            views.append(chunk[: len(chunk) - (len(chunk) % 2)].decode(codec, errors="replace"))
    for codec in ("utf-32-le", "utf-32-be"):
        for offset in (0, 1, 2, 3):
            chunk = data[offset:]
            views.append(chunk[: len(chunk) - (len(chunk) % 4)].decode(codec, errors="replace"))
    return views


def scan_views(matcher: Matcher, label: str, data: bytes) -> list[Finding]:
    out: dict[Finding, None] = {}
    for view in text_views(data):
        for finding in matcher.scan_blob(label, view):
            out[finding] = None
    return list(out)


def scan_file(matcher: Matcher, path: Path, label: str) -> list[Finding]:
    try:
        size = path.stat().st_size
        with path.open("rb") as fh:
            head = fh.read(BINARY_SNIFF_BYTES)
            # The binary skip applies ONLY above the size bound, where the
            # file is a finding of its own. Below it every byte is read.
            if is_binary(head) and size > LARGE_BINARY_BYTES:
                return [Finding(label, 0, "binary-large", f"binary file of {size} bytes")]
            data = head + fh.read()
    except OSError as exc:
        return [Finding(label, 0, "unreadable", str(exc))]
    return scan_views(matcher, label, data)


def scan_tree(matcher: Matcher, root: Path) -> list[Finding]:
    out: list[Finding] = []
    for dirpath, dirnames, filenames in os.walk(root):
        # Dependency and build output trees are never exported and would take minutes to walk.
        dirnames[:] = sorted(d for d in dirnames if d not in (".git", "node_modules", "dist", "__pycache__"))
        for name in sorted(filenames):
            path = Path(dirpath) / name
            if not path.is_file() or path.is_symlink():
                continue
            out.extend(scan_file(matcher, path, str(path.relative_to(root))))
    return out


def scan_files(matcher: Matcher, files: Sequence[str]) -> list[Finding]:
    out: list[Finding] = []
    for name in files:
        path = Path(name)
        if not path.is_file():
            out.append(Finding(name, 0, "unreadable", "not a regular file"))
            continue
        out.extend(scan_file(matcher, path, name))
    return out


def git(repo: Path | None, *args: str) -> str:
    # quotePath off: a path with a non-ASCII byte is labelled as itself, not
    # as a C-quoted string (a tab or newline in a path is still quoted).
    # The diff configuration a user or a repository can set is pinned so that
    # the parser below always sees `a/` and `b/` paths (DIFF_ARGS repeats the
    # prefixes for the commands that take them).
    cmd = ["git", "-c", "core.quotePath=false", "-c", "diff.noprefix=false", "-c", "diff.mnemonicPrefix=false",
           *(["-C", str(repo)] if repo else []), *args]
    proc = subprocess.run(cmd, capture_output=True, text=True, errors="replace")
    if proc.returncode != 0:
        raise ListError(f"{' '.join(cmd)} failed: {proc.stderr.strip()}")
    return proc.stdout


# Every diff the checker reads is forced to text. Without --text, a
# `.gitattributes` line `* -diff` (or `binary`) turns every change into
# "Binary files differ" and the scan sees nothing; --no-textconv and
# --no-ext-diff stop a `diff=<driver>` from substituting a converter's
# output (or nothing) for the blob's bytes. The prefixes and --no-relative
# keep the path parser below stable whatever the configuration says.
DIFF_ARGS = ("--text", "--no-textconv", "--no-ext-diff", "--src-prefix=a/", "--dst-prefix=b/", "--no-relative")


def added_lines(diff: str) -> Iterator[tuple[str, int, str]]:
    """(new path, new line number, text) for every added line of a unified diff."""
    path: str | None = None
    line_no: int | None = None
    for line in diff.split("\n"):
        if line.startswith("diff --git"):
            path, line_no = None, None
        elif line.startswith("+++ "):
            target = line[4:].split("\t")[0]
            path = None if target == "/dev/null" else target[2:] if target.startswith("b/") else target
        elif line.startswith("@@"):
            m = re.match(r"@@+ (?:-\S+ )+\+(\d+)", line)
            line_no = int(m.group(1)) if m else None
        elif path is None or line_no is None:
            continue
        elif line.startswith("+"):
            yield path, line_no, line[1:]
            line_no += 1
        elif line.startswith(" "):
            line_no += 1


def scan_diff(matcher: Matcher, label_prefix: str, diff: str) -> list[Finding]:
    by_path: dict[str, list[tuple[int, str]]] = {}
    for path, line_no, text in added_lines(diff):
        by_path.setdefault(path, []).append((line_no, text))
    out: list[Finding] = []
    for path, lines in by_path.items():
        # A line carrying a NUL is a forced-text view of a binary-looking blob
        # (UTF-16 among them): it is also scanned with the NULs dropped.
        lines = lines + [(n, t.replace("\0", "")) for n, t in lines if "\0" in t]
        out.extend(dict.fromkeys(matcher.scan_lines(f"{label_prefix}{path}", lines)))
    return out


def gitattributes_problems(text: str) -> list[tuple[int, str]]:
    """(line, message) per attribute that blinds a scan or an export.

    `-diff`, `binary` and `diff=` change what a diff prints, `filter=` changes
    the bytes a checkout and a commit see, `export-subst` makes `git archive`
    expand `$Format:...$` with commit metadata, and `export-ignore` drops a path
    from `git archive` (the boot-image publication path). The same rule as the
    public shape checker's `gitattributes` rule; `!attr` (back to unspecified)
    and a plain `diff` (force a text diff) blind nothing.
    """
    out: list[tuple[int, str]] = []
    for line_no, raw in enumerate(text.splitlines(), start=1):
        line = raw.strip()
        if not line or line.startswith("#"):
            continue
        for token in line.split()[1:]:
            if token.startswith("!"):
                continue
            name, eq, _ = token.lstrip("-").partition("=")
            if (name == "diff" and (token.startswith("-") or eq)) or name in ("binary", "filter", "export-ignore", "export-subst"):
                out.append((line_no, f"attribute {token} would hide changes from the guard"))
    return out


def scan_gitattributes_staged(repo: Path | None, pathspec: Sequence[str]) -> list[Finding]:
    """Every staged `.gitattributes` (any directory) is checked whole."""
    names = git(repo, "diff", "--cached", "--name-only", "-z", "--no-renames", "--diff-filter=ACMRT",
                *literal_pathspec(pathspec)).split("\0")
    out: list[Finding] = []
    for name in names:
        if name.rsplit("/", 1)[-1] != ".gitattributes":
            continue
        blob = git(repo, "cat-file", "-p", f":{name}")
        out.extend(Finding(name, n, "gitattributes", msg) for n, msg in gitattributes_problems(blob))
    return out


def literal_pathspec(pathspec: Sequence[str]) -> list[str]:
    """`-- :(literal)p ...`: a path naming `*` or `[` is that path, never a glob."""
    return ["--", *(f":(literal){p}" for p in pathspec)] if pathspec else []


def scan_staged(matcher: Matcher, repo: Path | None, pathspec: Sequence[str] = ()) -> list[Finding]:
    limit = literal_pathspec(pathspec)
    # --no-renames: a renamed-and-edited file is scanned whole at its new path;
    # with rename detection only the edit would be, and a move from a private
    # path to a public one would be scanned not at all.
    out = scan_diff(matcher, "", git(repo, "diff", "--cached", "--no-color", "--no-renames", *DIFF_ARGS, *limit))
    out += scan_gitattributes_staged(repo, pathspec)
    for row in git(repo, "diff", "--cached", "--numstat", "--no-renames", "-z", *limit).split("\0"):
        fields = row.split("\t")
        if len(fields) == 3 and fields[0] == "-" and fields[1] == "-":
            try:
                size = int(git(repo, "cat-file", "-s", f":{fields[2]}").strip() or 0)
            except ListError:
                continue  # a staged DELETION of a binary has no blob in the index; nothing is published
            if size > LARGE_BINARY_BYTES:
                out.append(Finding(fields[2], 0, "binary-large", f"staged binary of {size} bytes"))
    return out


def scan_commits(
    matcher: Matcher, repo: Path | None, revs: Sequence[str], messages_only: bool, pathspec: Sequence[str] = ()
) -> list[Finding]:
    fmt = "--format=%x1e%H%x1f%B%x1f"
    args = ["log", "--no-color", fmt]
    if not messages_only:
        args += ["-p", "--no-renames", "--diff-merges=first-parent", *DIFF_ARGS]
    # A pathspec limits which diffs are shown, never which commits: every message stays scanned.
    limit = literal_pathspec(pathspec)
    log = git(repo, *args, *revs, *limit)
    out: list[Finding] = []
    for chunk in log.split("\x1e")[1:]:
        parts = chunk.split("\x1f", 2)
        sha = parts[0].strip()[:12]
        message = parts[1] if len(parts) > 1 else ""
        out.extend(matcher.scan_lines(f"{sha}:message", enumerate(message.split("\n"), start=1)))
        if len(parts) > 2:
            out.extend(scan_diff(matcher, f"{sha}:", parts[2]))
    return out


# --------------------------------------------------------------------------- cli


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    sub = parser.add_subparsers(dest="command", required=True)

    check = sub.add_parser("check", help="scan a source and report findings")
    check.add_argument("--list", type=Path, default=DEFAULT_LIST, help="plaintext identifier list")
    check.add_argument("--digests", type=Path, help="HMAC digest file (CI mode; never prints a token)")
    check.add_argument("--key-env", help="environment variable holding the HMAC key (with --digests)")
    check.add_argument("--repo", type=Path, help="git repository for --commit/--range/--staged (default: cwd)")
    check.add_argument("--messages-only", action="store_true", help="with --commit/--range: skip the diffs")
    check.add_argument("--pathspec", help="with --staged/--commit/--range: restrict the diff to the paths listed "
                                           "in FILE ('-' = stdin; newline or NUL separated); messages are still scanned")
    src = check.add_mutually_exclusive_group(required=True)
    src.add_argument("--tree", type=Path, help="scan every regular file under DIR (skips .git)")
    src.add_argument("--commit", help="scan REV's message and added lines")
    src.add_argument("--range", help="scan every commit in A..B")
    src.add_argument("--staged", action="store_true", help="scan the added lines of the index")
    src.add_argument("--stdin", action="store_true", help="scan text from stdin")
    src.add_argument("--files", nargs="+", help="scan these files")
    src.add_argument("--files-from", help="scan the files listed one per line in FILE ('-' = stdin; NUL-separated accepted)")
    src.add_argument("--tracked", action="store_true", help="scan every file git tracks in --repo (the tree as a commit would publish it)")

    digest = sub.add_parser("digest", help="write the HMAC digests of every list form")
    digest.add_argument("--list", type=Path, default=DEFAULT_LIST)
    digest.add_argument("--key-env", help="environment variable holding the HMAC key (required unless --self-check)")
    digest.add_argument("--out", type=Path, help="write the payload as ONE file (local use; no size bound)")
    digest.add_argument("--out-dir", type=Path,
                        help=f"write the payload as parts {DIGEST_PART_PREFIX}1..n, each at most "
                             f"{MAX_DIGEST_PART_BYTES} bytes, one per CI secret (an empty or new directory)")
    digest.add_argument("--self-check", action="store_true",
                        help="write nothing; exit 1 if any list entry could never match in digest mode "
                             "(prints list line, category and token SHAPE, never the token)")

    verify = sub.add_parser("verify-digests", help="exit 0 only if a (reassembled) digest payload is whole")
    verify.add_argument("--digests", type=Path, required=True)
    return parser


def key_from_env(name: str) -> bytes:
    value = os.environ.get(name, "")
    if not value:
        raise ListError(f"environment variable {name} is unset or empty")
    return value.encode("utf-8")


def warn(message: str) -> None:
    print(f"warning: {message}", file=sys.stderr)


def main(argv: Sequence[str] | None = None) -> int:
    args = build_parser().parse_args(argv)
    try:
        if args.command == "digest":
            entries = load_list(args.list)
            problems = self_check(entries)
            if args.self_check:
                for problem in problems:
                    print(problem)
                print(f"self-check: {len(entries)} entries, {len(problems)} unmatchable in digest mode", file=sys.stderr)
                return 1 if problems else 0
            if not args.key_env or (args.out is None) == (args.out_dir is None):
                raise ListError("digest needs --key-env and exactly one of --out / --out-dir (or --self-check)")
            for problem in problems:
                warn(problem)
            count, paths = write_digests(entries, key_from_env(args.key_env), args.out, warn, args.out_dir)
            sizes = [p.stat().st_size for p in paths]
            target = args.out if args.out_dir is None else args.out_dir
            print(f"wrote {count} digests for {len(entries)} entries to {target} as {len(paths)} part(s), "
                  f"largest {max(sizes)} bytes", file=sys.stderr)
            return 0

        if args.command == "verify-digests":
            try:
                text = args.digests.read_text(encoding="utf-8")
            except OSError as exc:
                raise ListError(f"cannot read digests {args.digests}: {exc}") from exc
            payload = parse_digests(text, "digests")
            print(f"digests verified: {payload.parts} part(s), {payload.rows} rows", file=sys.stderr)
            return 0

        matcher: Matcher
        if args.digests is not None:
            if not args.key_env:
                raise ListError("--digests needs --key-env")
            key = key_from_env(args.key_env)
            matcher = DigestMatcher(load_digests(args.digests, key), key)
        else:
            matcher = PlaintextMatcher(load_list(args.list))

        pathspec: list[str] = []
        if args.pathspec:
            raw = sys.stdin.read() if args.pathspec == "-" else Path(args.pathspec).read_text(encoding="utf-8")
            # NUL-separated when any NUL is present (a path may contain a newline).
            pathspec = [n for n in (raw.split("\0") if "\0" in raw else raw.split("\n")) if n]
            if not pathspec:
                raise ListError("--pathspec names no path; refusing to scan nothing")
        if args.tree is not None:
            if not args.tree.is_dir():
                raise ListError(f"{args.tree} is not a directory")
            findings = scan_tree(matcher, args.tree)
        elif args.commit is not None:
            findings = scan_commits(matcher, args.repo, ["-1", args.commit], args.messages_only, pathspec)
        elif args.range is not None:
            findings = scan_commits(matcher, args.repo, [args.range], args.messages_only, pathspec)
        elif args.staged:
            findings = scan_staged(matcher, args.repo, pathspec)
        elif args.stdin:
            findings = matcher.scan_blob("stdin", sys.stdin.read())
        elif args.files_from is not None:
            listing = sys.stdin.read() if args.files_from == "-" else Path(args.files_from).read_text(encoding="utf-8")
            names = [n for n in (listing.split("\0") if "\0" in listing else listing.split("\n")) if n]
            findings = scan_files(matcher, names)
        elif args.tracked:
            root = Path(git(args.repo, "rev-parse", "--show-toplevel").strip())
            names = [n for n in git(args.repo, "ls-files", "-z").split("\0") if n]
            findings = [
                Finding(str(Path(f.label).relative_to(root)), f.line, f.category, f.message)
                for f in scan_files(matcher, [str(root / n) for n in names])
            ]
        else:
            findings = scan_files(matcher, args.files)
    except ListError as exc:
        print(f"error: {exc}", file=sys.stderr)
        return 2

    findings = sorted(set(findings), key=lambda f: (f.label, f.line, f.category, f.message))
    for finding in findings:
        print(finding.render())
    if findings:
        print(f"{len(findings)} finding(s)", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
