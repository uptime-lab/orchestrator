#!/usr/bin/env bash
# Tests for check_trust_stores.sh against a synthetic repository layout.
# Usage: check_trust_stores_test.sh [PATH_TO_SCRIPT_UNDER_TEST]
set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
script="${1:-$here/check_trust_stores.sh}"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

failures=0
expect() { # expect <label> <expected exit> <command...>
  local label="$1" want="$2"; shift 2
  local got=0
  "$@" >"$tmp/out" 2>&1 || got=$?
  if [ "$got" -ne "$want" ]; then
    echo "FAIL $label: exit $got, wanted $want"; sed 's/^/    /' "$tmp/out"; failures=$((failures + 1))
  else
    echo "ok   $label"
  fi
}
expect_line() { # expect_line <label> <pattern>
  grep -q "$2" "$tmp/out" || { echo "FAIL $1: no line matching '$2'"; failures=$((failures + 1)); }
}

stores="signing/trusted-keys agent/pkg/signing/keys backend/pkg/webapp/infrastructure/signing/keys"

make_layout() { # make_layout <root>
  local r="$1" d
  for d in $stores; do
    mkdir -p "$r/$d"
    printf -- '-----BEGIN PUBLIC KEY-----\nAAAA\n-----END PUBLIC KEY-----\n' >"$r/$d/agent-signing-1.pem"
    printf -- '-----BEGIN PUBLIC KEY-----\nBBBB\n-----END PUBLIC KEY-----\n' >"$r/$d/agent-signing-2.pem"
  done
}

make_layout "$tmp/ok"
expect "identical stores" 0 "$script" "$tmp/ok"
expect_line "identical stores" "trust stores: identical"

# A file that is not a key does not count: only *.pem is embedded.
make_layout "$tmp/readme"
printf 'notes\n' >"$tmp/readme/signing/trusted-keys/README"
expect "a non-key file is ignored" 0 "$script" "$tmp/readme"

# A rotation that reached two of the three copies.
make_layout "$tmp/added"
for d in signing/trusted-keys backend/pkg/webapp/infrastructure/signing/keys; do
  printf -- '-----BEGIN PUBLIC KEY-----\nCCCC\n-----END PUBLIC KEY-----\n' >"$tmp/added/$d/agent-signing-3.pem"
done
expect "a key missing from one copy" 1 "$script" "$tmp/added"
expect_line "a key missing from one copy" "MISSING  agent/pkg/signing/keys/agent-signing-3.pem"

# Same names, different bytes.
make_layout "$tmp/diff"
printf -- '-----BEGIN PUBLIC KEY-----\nZZZZ\n-----END PUBLIC KEY-----\n' >"$tmp/diff/agent/pkg/signing/keys/agent-signing-2.pem"
expect "a key that differs" 1 "$script" "$tmp/diff"
expect_line "a key that differs" "DIFFERS"

# A key removed from one copy only.
make_layout "$tmp/removed"
rm "$tmp/removed/backend/pkg/webapp/infrastructure/signing/keys/agent-signing-1.pem"
expect "a key removed from one copy" 1 "$script" "$tmp/removed"
expect_line "a key removed from one copy" "MISSING  backend/"

# Three empty stores agree with each other and still trust nothing.
for d in $stores; do mkdir -p "$tmp/empty/$d"; done
expect "empty stores" 1 "$script" "$tmp/empty"
expect_line "empty stores" "EMPTY"

# A store directory that does not exist is a setup error.
make_layout "$tmp/nodir"
rm -r "$tmp/nodir/agent/pkg/signing/keys"
expect "a missing store directory" 2 "$script" "$tmp/nodir"

if [ "$failures" -ne 0 ]; then
  echo "FAILED ($failures)"; exit 1
fi
echo "OK"
