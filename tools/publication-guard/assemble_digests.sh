#!/usr/bin/env bash
# Reassembles the private-identifier digest payload from the environment
# secrets PUBLICATION_GUARD_DIGESTS_1 .. _<MAX_PARTS> into ONE file, in order.
#
#   assemble_digests.sh OUT
#
# Why parts: one GitHub secret holds at most 48 KB and the payload is several
# times that, so `private_identifiers_ci.py digest --out-dir` splits it into
# bounded parts, one secret each. A workflow cannot enumerate secret names,
# so `.github/workflows/guard.yml` declares the fixed set
# PUBLICATION_GUARD_DIGESTS_1..MAX_PARTS as env of the step that runs this
# script; MAX_PARTS here, that set and the checker's MAX_DIGEST_PARTS are one
# number, pinned by assemble_digests_test.py.
#
# Fail closed: an unset secret expands to the empty string, which must never
# turn into a smaller table. This script refuses (exit 2) when part 1 is
# empty, when any part 1..n that part 1's header announces is empty, and when
# a part beyond n is set (a stale part from an earlier, larger upload). The
# checker then verifies the whole payload -- every header, the order, the row
# count and the SHA-256 over the rows -- and refuses a missing, truncated or
# corrupt part on its own (`verify-digests`, and `check --digests` at load).
#
# The workflow runs main's copy of this file, never the tree's, like the
# checker. It never prints a part; no `set -x`.
set -eu

MAX_PARTS=12

out=${1:?usage: assemble_digests.sh OUT}

fail() {
  echo "::error::$1" >&2
  exit 2
}

part() {
  local name="PUBLICATION_GUARD_DIGESTS_$1"
  printf '%s' "${!name:-}"
}

first=$(part 1)
[ -n "$first" ] || fail "PUBLICATION_GUARD_DIGESTS_1 is empty or unset"
header=${first%%$'\n'*}
case "$header" in
  '#! publication-guard-digests/v2 part=1/'*) ;;
  *) fail "PUBLICATION_GUARD_DIGESTS_1 does not start with a part 1 header" ;;
esac
n=${header#'#! publication-guard-digests/v2 part=1/'}
n=${n%% *}
case "$n" in
  '' | *[!0-9]*) fail "PUBLICATION_GUARD_DIGESTS_1 announces no part count" ;;
esac
{ [ "$n" -ge 1 ] && [ "$n" -le "$MAX_PARTS" ]; } \
  || fail "part 1 announces $n parts; this workflow reads at most $MAX_PARTS"

: > "$out"
i=1
while [ "$i" -le "$MAX_PARTS" ]; do
  value=$(part "$i")
  if [ "$i" -le "$n" ]; then
    [ -n "$value" ] || fail "PUBLICATION_GUARD_DIGESTS_$i is empty or unset; part 1 announces $n parts"
    printf '%s\n' "$value" >> "$out"
  elif [ -n "$value" ]; then
    fail "PUBLICATION_GUARD_DIGESTS_$i is set but part 1 announces $n parts -- delete the stale secret"
  fi
  i=$((i + 1))
done
echo "assembled $n digest part(s)" >&2
