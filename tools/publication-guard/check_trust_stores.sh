#!/usr/bin/env bash
# CI contract: the agent-signing trust store is ONE key set kept in three places.
#
#   signing/trusted-keys/                           the published set
#   agent/pkg/signing/keys/                         embedded in the agent, which
#                                                   verifies every package it installs
#   backend/pkg/webapp/infrastructure/signing/keys/ embedded in the server, which
#                                                   verifies a release on import and
#                                                   bakes the keys into install.sh
#
# The two modules share no Go workspace, so each compiles against its own copy.
# A key added to one copy and not another passes every build: the server then
# imports a release every device refuses, or refuses one every device accepts.
#
# Asserts that the three directories hold the same non-empty set of `*.pem`
# file names (the pattern both binaries embed) with byte-identical contents.
#
# Usage: check_trust_stores.sh [REPO_ROOT]   (default: the git top level, else cwd)
# Exit: 0 identical, 1 mismatch (each difference is printed), 2 setup error.

set -euo pipefail

root="${1:-}"
if [ -z "$root" ]; then
  root="$(git rev-parse --show-toplevel 2>/dev/null || pwd)"
fi
cd "$root"

stores=(
  "signing/trusted-keys"
  "agent/pkg/signing/keys"
  "backend/pkg/webapp/infrastructure/signing/keys"
)

if command -v sha256sum >/dev/null 2>&1; then
  sum() { sha256sum "$1" | cut -d' ' -f1; }
elif command -v shasum >/dev/null 2>&1; then
  sum() { shasum -a 256 "$1" | cut -d' ' -f1; }
else
  echo "error: neither sha256sum nor shasum is available" >&2
  exit 2
fi

for dir in "${stores[@]}"; do
  if [ ! -d "$dir" ]; then
    echo "error: $dir not found under $root" >&2
    exit 2
  fi
done

# The union of every key file name across the three stores.
names="$(for dir in "${stores[@]}"; do
  find "$dir" -maxdepth 1 -type f -name '*.pem' -exec basename {} \;
done | LC_ALL=C sort -u)"

status=0
for dir in "${stores[@]}"; do
  if [ -z "$(find "$dir" -maxdepth 1 -type f -name '*.pem' | head -n 1)" ]; then
    echo "  EMPTY    $dir holds no *.pem key"
    status=1
  fi
done

for name in $names; do
  echo "== $name"
  first_sum=""
  first_path=""
  for dir in "${stores[@]}"; do
    path="$dir/$name"
    if [ ! -f "$path" ]; then
      echo "  MISSING  $path"
      status=1
      continue
    fi
    s="$(sum "$path")"
    echo "  ${s:0:16}  $path"
    if [ -z "$first_sum" ]; then
      first_sum="$s"; first_path="$path"
      continue
    fi
    if [ "$s" != "$first_sum" ]; then
      echo "  DIFFERS  $path vs $first_path"
      status=1
    fi
  done
done

if [ "$status" -eq 0 ]; then
  echo "trust stores: identical"
else
  echo "trust stores: MISMATCH (add, change or remove a key in all three directories together)"
fi
exit "$status"
