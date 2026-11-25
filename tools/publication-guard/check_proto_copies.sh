#!/usr/bin/env bash
# CI contract: the generated gRPC code is ONE artifact kept in three places.
#
# The proto source is compiled once under proto/ and the output is copied into
# the agent and backend modules, which share no Go workspace. Nothing but this
# check notices when one copy is regenerated and another is not: the two modules
# would still compile, each against its own idea of the wire contract, and the
# mismatch would surface as a field silently dropped on one side of the stream.
#
# Asserts, for every generated file name:
#   1. the file exists in all three locations;
#   2. the three copies are byte-identical (sha256);
#   3. the generator versions recorded in the file headers agree across copies.
# The generator versions are also compared with a pin WHEN one exists. The
# proto Makefile installs the plugins from the module proxy without a version,
# and the Go modules pin the protobuf RUNTIME, not the plugin, so today the
# script can only print the versions it finds and assert they agree with each
# other. Pinning them is a change to the Makefile, not to this script; the
# script picks a pin up from `PROTOC_GEN_GO_VERSION` / `PROTOC_GEN_GO_GRPC_VERSION`
# in the Makefile or the environment once one is written.
#
# Usage: check_proto_copies.sh [REPO_ROOT]   (default: the git top level, else cwd)
# Exit: 0 all copies identical, 1 mismatch (a diff summary is printed), 2 setup error.

set -euo pipefail

root="${1:-}"
if [ -z "$root" ]; then
  root="$(git rev-parse --show-toplevel 2>/dev/null || pwd)"
fi
cd "$root"

proto_dir="proto"
makefile="$proto_dir/Makefile"
if [ ! -f "$makefile" ]; then
  echo "error: $makefile not found under $root" >&2
  exit 2
fi

# The two copy destinations are read from the Makefile that writes them, so the
# check cannot drift from the generator.
agent_dir="$(sed -n 's/^AGENT_PROTO_DIR *= *\$(AGENT_DIR)\/\(.*\)$/agent\/\1/p' "$makefile")"
backend_dir="$(sed -n 's/^BACKEND_PROTO_DIR *= *\$(BACKEND_DIR)\/\(.*\)$/backend\/\1/p' "$makefile")"
generated="$(sed -n 's/^GENERATED_FILES *= *//p' "$makefile")"
if [ -z "$agent_dir" ] || [ -z "$backend_dir" ] || [ -z "$generated" ]; then
  echo "error: could not read AGENT_PROTO_DIR, BACKEND_PROTO_DIR or GENERATED_FILES from $makefile" >&2
  exit 2
fi

if command -v sha256sum >/dev/null 2>&1; then
  sum() { sha256sum "$1" | cut -d' ' -f1; }
elif command -v shasum >/dev/null 2>&1; then
  sum() { shasum -a 256 "$1" | cut -d' ' -f1; }
else
  echo "error: neither sha256sum nor shasum is available" >&2
  exit 2
fi

# Header lines look like `// \tprotoc-gen-go v1.2.3`, `// - protoc-gen-go-grpc v1.2.3`
# and `// \tprotoc        v1.2.3`; the tool name and version are what matters.
header_versions() {
  sed -n '1,12p' "$1" | sed -n 's/^\/\/[[:space:]-]*\(protoc\(-gen-go\(-grpc\)\{0,1\}\)\{0,1\}\)[[:space:]]*\(v[0-9][0-9.]*\).*$/\1 \4/p' | sort -u
}

# A pin, if anyone has written one. Absent today; see the header comment.
pin_from() {
  sed -n "s/^$1 *[:?]*= *//p" "$makefile" | head -n 1
}
pin_gen_go="${PROTOC_GEN_GO_VERSION:-$(pin_from PROTOC_GEN_GO_VERSION)}"
pin_gen_go_grpc="${PROTOC_GEN_GO_GRPC_VERSION:-$(pin_from PROTOC_GEN_GO_GRPC_VERSION)}"

status=0
for name in $generated; do
  echo "== $name"
  first_sum=""
  first_path=""
  first_versions=""
  for dir in "$proto_dir" "$agent_dir" "$backend_dir"; do
    path="$dir/$name"
    if [ ! -f "$path" ]; then
      echo "  MISSING  $path"
      status=1
      continue
    fi
    s="$(sum "$path")"
    v="$(header_versions "$path" | tr '\n' ';')"
    echo "  ${s:0:16}  $path  [$v]"
    if [ -z "$first_sum" ]; then
      first_sum="$s"; first_path="$path"; first_versions="$v"
      continue
    fi
    if [ "$s" != "$first_sum" ]; then
      echo "  DIFFERS  $path vs $first_path"
      diff -u "$first_path" "$path" | head -n 40 | sed 's/^/    /' || true
      status=1
    fi
    if [ "$v" != "$first_versions" ]; then
      echo "  GENERATOR VERSIONS DIFFER  $path [$v] vs $first_path [$first_versions]"
      status=1
    fi
  done

  # Compare with a pin only when a pin exists.
  if [ -n "$first_versions" ]; then
    found_gen_go="$(printf '%s' "$first_versions" | tr ';' '\n' | sed -n 's/^protoc-gen-go \(v.*\)$/\1/p')"
    found_gen_go_grpc="$(printf '%s' "$first_versions" | tr ';' '\n' | sed -n 's/^protoc-gen-go-grpc \(v.*\)$/\1/p')"
    if [ -n "$pin_gen_go" ] && [ -n "$found_gen_go" ] && [ "$pin_gen_go" != "$found_gen_go" ]; then
      echo "  PIN MISMATCH  protoc-gen-go header $found_gen_go, pinned $pin_gen_go"
      status=1
    fi
    if [ -n "$pin_gen_go_grpc" ] && [ -n "$found_gen_go_grpc" ] && [ "$pin_gen_go_grpc" != "$found_gen_go_grpc" ]; then
      echo "  PIN MISMATCH  protoc-gen-go-grpc header $found_gen_go_grpc, pinned $pin_gen_go_grpc"
      status=1
    fi
  fi
done

if [ -z "$pin_gen_go" ] && [ -z "$pin_gen_go_grpc" ]; then
  echo "note: generator versions are not pinned (the Makefile installs the plugins unversioned);"
  echo "      asserting only that the copies agree with each other."
fi

if [ "$status" -eq 0 ]; then
  echo "proto copies: identical"
else
  echo "proto copies: MISMATCH (run 'make proto/generate' and commit all three copies together)"
fi
exit "$status"
