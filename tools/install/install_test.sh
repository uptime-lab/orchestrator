#!/bin/sh
#
# Tests of the installer's decisions: what it accepts as an address, what it
# asks, the environment file it writes, which release it picks, which
# directories it resumes or refuses, and the order in which it opens the
# server to the network. Nothing here touches Docker or the network: git
# fixtures are local repositories and Docker, curl and the netboot tools are
# stubs.  Run: sh tools/install/install_test.sh (Ubuntu's sh is dash, which is
# what `curl … | sh` runs it with; run it under dash elsewhere too).
set -eu

HERE=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
ORCHESTRATOR_INSTALL_LIB=1
# shellcheck source=tools/install/install.sh disable=SC1091
. "$HERE/install.sh"

# This machine, as the address check sees it: one LAN address, and a router
# that resolves server.home.arpa to it.
host_ipv4s() { printf '192.0.2.40\n'; }
resolve_ipv4s() {
  case "$1" in
    server.home.arpa) printf '192.0.2.40\n' ;;
    elsewhere.home.arpa) printf '192.0.2.99\n' ;;
  esac
}

pass=0 fail=0
ok() { pass=$((pass + 1)); printf 'ok   - %s\n' "$1"; }
bad() { fail=$((fail + 1)); printf 'FAIL - %s\n' "$1"; }
accepts() { if "$@"; then ok "$*"; else bad "$* (refused)"; fi; }
refuses() { if "$@"; then bad "$* (accepted)"; else ok "refuses: $*"; fi; }

accepts valid_ipv4 192.0.2.40
refuses valid_ipv4 192.0.2.400
refuses valid_ipv4 192.0.2
refuses valid_ipv4 orchestrator.example.com

accepts valid_hostname orchestrator.example.com
accepts valid_hostname server-1.home.arpa
refuses valid_hostname orchestrator
refuses valid_hostname bad_name.example.com
refuses valid_hostname -bad.example.com
refuses valid_hostname "two words.example.com"

accepts is_mdns_name server.local
refuses is_mdns_name server.localdomain.example.com

accepts valid_version 1.2.3
accepts valid_version 0.1.0-rc1
refuses valid_version 1.2
refuses valid_version "1.2.3; rm -rf /"

# The environment file of each shape.
MODE=lan ADDRESS=server.home.arpa VERSION=1.2.3 DOMAIN="" EMAIL=""
env_lan=$(render_env)
for line in DOMAIN=server.home.arpa PLATFORM_VERSION=1.2.3 COMPOSE_PROFILES=proxy-lan \
  PUBLIC_SCHEME=http PUBLIC_WS_SCHEME=ws GRPC_TLS_TERMINATED_EXTERNALLY=false \
  AGENT_GRPC_PLAINTEXT_OK=true IMAGE_ALLOW_PLAINTEXT_URL=true AGENT_GRPC_PROXY_PROTOCOL=require; do
  if printf '%s\n' "$env_lan" | grep -qx "$line"; then ok "lan env has $line"; else bad "lan env lacks $line"; fi
done

MODE=public DOMAIN=orchestrator.example.com EMAIL=admin@example.com ADDRESS=""
env_public=$(render_env)
for line in DOMAIN=orchestrator.example.com COMPOSE_PROFILES=proxy-tls ACME_EMAIL=admin@example.com \
  AGENT_GRPC_PROXY_PROTOCOL=require; do
  if printf '%s\n' "$env_public" | grep -qx "$line"; then ok "public env has $line"; else bad "public env lacks $line"; fi
done
# A public install must never be written as the plaintext shape.
for word in PUBLIC_SCHEME AGENT_GRPC_PLAINTEXT_OK IMAGE_ALLOW_PLAINTEXT_URL; do
  if printf '%s\n' "$env_public" | grep -q "^$word="; then bad "public env carries $word"; else ok "public env has no $word"; fi
done

# Questions are answered from the terminal file, and an empty answer takes
# the default.
answers=$(mktemp)
trap 'rm -f "$answers"' EXIT
printf '2\n' >"$answers"
MODE="" TTY_IN=$answers TTY_OPEN=0
choose_mode >/dev/null 2>&1
[ "$MODE" = public ] && ok "answer 2 selects the public shape" || bad "answer 2 gave MODE=$MODE"
printf '\n' >"$answers"
MODE="" TTY_OPEN=0
choose_mode >/dev/null 2>&1
[ "$MODE" = lan ] && ok "an empty answer selects the local shape" || bad "empty answer gave MODE=$MODE"

# Several questions in a row read successive lines, not the first one again.
printf '1\nserver.home.arpa\nn\n' >"$answers"
MODE="" ADDRESS="" NETBOOT="" TTY_OPEN=0
choose_mode >/dev/null 2>&1
choose_lan_address >/dev/null 2>&1
choose_netboot >/dev/null 2>&1
if [ "$MODE" = lan ] && [ "$ADDRESS" = server.home.arpa ] && [ "$NETBOOT" = no ]; then
  ok "three questions take three successive answers"
else
  bad "answers were read wrongly: MODE=$MODE ADDRESS=$ADDRESS NETBOOT=$NETBOOT"
fi

# A multicast name is refused for a local install, with the reason.
MODE=lan ADDRESS=server.local
if (choose_lan_address >/dev/null 2>&1); then bad "a .local address was accepted"; else ok "refuses a .local address"; fi
ADDRESS=192.0.2.40
out=$(choose_lan_address 2>&1)
if printf '%s\n' "$out" | grep -q "reserve 192.0.2.40"; then ok "an address answer tells to reserve it"; else bad "no reservation notice for an address"; fi
ADDRESS=server.home.arpa
out=$(choose_lan_address 2>&1)
if printf '%s\n' "$out" | grep -q "through your router"; then ok "a name answer says devices resolve it through the router"; else bad "no router notice for a name"; fi

# Without a terminal, a question ends with the guidance to pass a flag —
# under dash too, where a failed `exec 3<` would end the shell before any
# `||` after it ran.
out=$( (MODE="" TTY_IN=/nonexistent/tty TTY_OPEN=0; choose_mode) 2>&1 ) && rc=0 || rc=$?
if [ "$rc" -eq 1 ] && printf '%s\n' "$out" | grep -q "pass the answer as a flag"; then
  ok "no terminal: the question ends with the pass-a-flag guidance"
else
  bad "no terminal: rc=$rc, output: $out"
fi

# The LAN address must lead to this machine.
MODE=lan ADDRESS_CHECK=yes
for a in 192.0.2.40 server.home.arpa; do
  ADDRESS=$a
  if (choose_lan_address >/dev/null 2>&1); then ok "accepts $a, which leads here"; else bad "refused $a, which leads here"; fi
done
for a in 192.0.2.250 elsewhere.home.arpa nowhere.home.arpa; do
  ADDRESS=$a
  out=$( (choose_lan_address) 2>&1 ) && rc=0 || rc=$?
  if [ "$rc" -ne 0 ] && printf '%s\n' "$out" | grep -q -- "--skip-address-check"; then
    ok "refuses $a, which does not lead here, and names the override"
  else
    bad "accepted $a (rc=$rc), which does not lead here"
  fi
  # Advice about an address comes only once it is accepted.
  if printf '%s\n' "$out" | grep -q "reserve"; then
    bad "advised reserving $a before refusing it"
  else
    ok "gives no reservation advice for $a, which it refuses"
  fi
done
for a in server.local 'not an address'; do
  ADDRESS=$a
  out=$( (choose_lan_address) 2>&1 ) && rc=0 || rc=$?
  if [ "$rc" -ne 0 ] && ! printf '%s\n' "$out" | grep -q "reserve"; then
    ok "refuses '$a' without advice about it first"
  else
    bad "'$a': rc=$rc, said: $out"
  fi
done
ADDRESS=192.0.2.250 ADDRESS_CHECK=no
if (choose_lan_address >/dev/null 2>&1); then ok "--skip-address-check accepts a forwarded address"; else bad "--skip-address-check did not skip"; fi
ADDRESS_CHECK=yes

# A name that also resolves to an address that is not this machine's (a
# router keeping an old lease's record) sends some devices elsewhere: it is
# refused when given, and not offered when found.
two_name_stubs='
  resolve_ipv4s() {
    case "$1" in
      (server.home.arpa) printf "192.0.2.40\n" ;;
      (twoaddr.home.arpa) printf "192.0.2.40\n192.0.2.98\n" ;;
    esac
  }
  getent() {
    case "$1 $2" in
      ("hosts 192.0.2.40") printf "192.0.2.40 %s\n" "$REVERSE_NAME" ;;
      ("ahostsv4 twoaddr.home.arpa") printf "192.0.2.40 STREAM twoaddr.home.arpa\n192.0.2.98 STREAM\n" ;;
      ("ahostsv4 server.home.arpa") printf "192.0.2.40 STREAM server.home.arpa\n" ;;
    esac
  }
  lan_address() { printf "192.0.2.40\n"; }
'
out=$( (eval "$two_name_stubs"; ADDRESS=twoaddr.home.arpa; choose_lan_address) 2>&1 ) && rc=0 || rc=$?
if [ "$rc" -ne 0 ] && printf '%s\n' "$out" | grep -q "192.0.2.98"; then
  ok "refuses a name that also resolves to another host, naming that address"
else
  bad "a name that also resolves elsewhere: rc=$rc, said: $out"
fi
printf '\n' >"$answers"
out=$( (eval "$two_name_stubs"; REVERSE_NAME=twoaddr.home.arpa ADDRESS="" TTY_OPEN=0
  choose_lan_address; printf 'ADDRESS=%s\n' "$ADDRESS") 2>&1 ) && rc=0 || rc=$?
if [ "$rc" -eq 0 ] && printf '%s\n' "$out" | grep -qx "ADDRESS=192.0.2.40" &&
  printf '%s\n' "$out" | grep -q "192.0.2.98"; then
  ok "a router name that also resolves elsewhere is not offered, and the reason is said"
else
  bad "a router name that also resolves elsewhere: rc=$rc, said: $out"
fi
printf '\n' >"$answers"
out=$( (eval "$two_name_stubs"; REVERSE_NAME=server.home.arpa ADDRESS="" TTY_OPEN=0
  choose_lan_address; printf 'ADDRESS=%s\n' "$ADDRESS") 2>&1 ) && rc=0 || rc=$?
if [ "$rc" -eq 0 ] && printf '%s\n' "$out" | grep -qx "ADDRESS=server.home.arpa"; then
  ok "a router name that resolves to this machine alone is offered"
else
  bad "a router name that resolves here alone: rc=$rc, said: $out"
fi

# A named recovery image is checked with the answers, as an absolute path.
scratch=$(mktemp -d)
trap 'rm -f "$answers"; rm -rf "$scratch"' EXIT
NETBOOT=yes RECOVERY_IMAGE=no-such-boot.img
if (choose_netboot >/dev/null 2>&1); then bad "a missing --recovery-image was accepted"; else ok "a missing --recovery-image is refused before anything changes"; fi
printf 'image' >"$scratch/boot.img"
RECOVERY_IMAGE=boot.img
out=$(cd "$scratch" && choose_netboot >/dev/null 2>&1 && printf '%s' "$RECOVERY_IMAGE")
case "$out" in /*/boot.img) ok "a relative --recovery-image becomes absolute" ;; *) bad "--recovery-image became '$out'" ;; esac
RECOVERY_IMAGE="" NETBOOT=""

# The owner's email.
accepts valid_email owner@example.com
refuses valid_email owner
refuses valid_email 'own"er@example.com'
refuses valid_email 'owner name@example.com'

# Version order: numeric per field, a final release above its candidates.
v=$(printf '1.2.0\n1.10.0\n1.9.3\n' | highest_version)
[ "$v" = 1.10.0 ] && ok "1.10.0 is above 1.9.3" || bad "highest of 1.2.0 1.10.0 1.9.3 gave $v"
v=$(printf '1.2.0-rc2\n1.2.0-rc10\n1.1.9\n' | highest_version)
[ "$v" = 1.2.0-rc10 ] && ok "rc10 is above rc2" || bad "highest of rc2 rc10 gave $v"
v=$(printf '1.2.0\n1.2.0-rc1\n' | highest_version)
[ "$v" = 1.2.0 ] && ok "a final release is above its candidate" || bad "highest of 1.2.0 1.2.0-rc1 gave $v"

# The default release: a GitHub Release when there is one, else the newest
# final platform tag; a release candidate only when asked.
# The lookup is read from files here; not every curl build speaks file://,
# so the test answers those itself and leaves every other call alone.
curl() {
  for _last in "$@"; do :; done
  case $_last in
    file://*) cat "${_last#file://}" 2>/dev/null ;;
    *) command curl "$@" ;;
  esac
}
repo="$scratch/repo"
git init -q "$repo"
git -C "$repo" -c user.name=t -c user.email=t@example.com commit -q --allow-empty -m one
for t in platform-v0.9.0 platform-v1.0.0 platform-v1.1.0-rc1 agent-v9.9.9 platform-vbogus; do
  git -C "$repo" tag "$t"
done
REPO_URL=$repo RELEASES_API="file://$scratch/no-releases.json" PRERELEASE=0
v=$(latest_platform_version)
[ "$v" = 1.0.0 ] && ok "no GitHub Release: the newest final tag (1.0.0)" || bad "no GitHub Release gave '$v'"
PRERELEASE=1
v=$(latest_platform_version)
[ "$v" = 1.1.0-rc1 ] && ok "--prerelease takes the newest candidate" || bad "--prerelease gave '$v'"
PRERELEASE=0
printf '{\n  "tag_name": "platform-v0.9.0",\n  "name": "Platform 0.9.0"\n}\n' >"$scratch/release.json"
RELEASES_API="file://$scratch/release.json"
v=$(latest_platform_version)
[ "$v" = 0.9.0 ] && ok "a GitHub Release is preferred to the tags" || bad "GitHub Release gave '$v'"
RELEASES_API="file://$scratch/no-releases.json"
REPO_URL="$scratch/no-repo"
v=$(latest_platform_version)
[ -z "$v" ] && ok "no release anywhere gives no version" || bad "no release anywhere gave '$v'"

# A reused checkout must be the release being installed.
repo_dir=$(cd "$repo" && pwd -P)
SOURCE="" VERSION=1.0.0 VERSION_GIVEN=0
if (check_checkout "$repo_dir" >/dev/null 2>&1); then ok "a checkout at platform-v1.0.0 is release 1.0.0"; else bad "a checkout at its own tag was refused"; fi
VERSION=1.1.0
out=$( (check_checkout "$repo_dir") 2>&1 ) && rc=0 || rc=$?
if [ "$rc" -ne 0 ] && printf '%s\n' "$out" | grep -q "not platform-v1.1.0"; then ok "a checkout of another release is refused"; else bad "a checkout of another release was accepted (rc=$rc)"; fi
mkdir -p "$scratch/unpacked"
VERSION=1.0.0
if (check_checkout "$scratch/unpacked" >/dev/null 2>&1); then bad "an unknown directory was reused"; else ok "a directory that is not a checkout is refused"; fi
SOURCE="$scratch/unpacked" VERSION_GIVEN=1
if (check_checkout "$scratch/unpacked" >/dev/null 2>&1); then ok "--source with --version installs an unpacked tree"; else bad "--source --version refused an unpacked tree"; fi
SOURCE="" VERSION_GIVEN=0
v=$(checkout_versions "$repo_dir" | highest_version)
[ "$v" = 1.1.0-rc1 ] && ok "the release of a checkout is read from its tags" || bad "checkout tags gave '$v'"

# New, resumed, or refused.
SUDO=""
inst="$scratch/inst"
mkdir -p "$inst/secrets" "$inst/data/postgres"
DIR=$inst STATE="$inst/data/.installer" RESUME=0
if (detect_existing_install >/dev/null 2>&1); then ok "an empty directory is a new install"; else bad "an empty directory was refused"; fi
: >"$inst/secrets/db_password"
out=$( (detect_existing_install) 2>&1 ) && rc=0 || rc=$?
if [ "$rc" -ne 0 ] && printf '%s\n' "$out" | grep -q "did not start"; then ok "credentials this installer did not write are refused"; else bad "foreign credentials were not refused (rc=$rc)"; fi
mkdir -p "$STATE"
printf '1.0.0\n' >"$STATE/version"
out=$(detect_existing_install >/dev/null 2>&1; printf '%s' "$RESUME")
[ "$out" = 1 ] && ok "an install this installer began is resumed" || bad "an unfinished install was not resumed (RESUME=$out)"
: >"$STATE/complete"
out=$( (detect_existing_install) 2>&1 ) && rc=0 || rc=$?
if [ "$rc" -ne 0 ] && printf '%s\n' "$out" | grep -q "Upgrading"; then ok "a finished install is refused"; else bad "a finished install was not refused (rc=$rc)"; fi
rm -f "$STATE/complete" "$inst/secrets/db_password"
: >"$inst/data/postgres/PG_VERSION"
out=$( (detect_existing_install) 2>&1 ) && rc=0 || rc=$?
if [ "$rc" -ne 0 ] && printf '%s\n' "$out" | grep -q "data/postgres"; then ok "a database without its credentials is refused, naming data/postgres"; else bad "a database without credentials was not refused (rc=$rc)"; fi

# Which credential files are missing is read from the release's compose file.
cat >"$inst/docker-compose.community.yml" <<'EOF_COMPOSE'
secrets:
  db_password:
    file: ${SECRETS_DIR:-./secrets}/db_password
  jwt_secret:
    file: ${SECRETS_DIR:-./secrets}/jwt_secret
EOF_COMPOSE
: >"$inst/secrets/db_password"
out=$(missing_secret_files)
[ "$out" = "jwt_secret " ] && ok "a cut-short credential set names the missing file" || bad "missing secret files: '$out'"

# The owner exists before anything outside this machine can reach the server:
# the proxy starts only after the sign-up on the loopback port.
calls="$scratch/calls"
: >"$calls"
compose() { printf 'compose %s\n' "$*" >>"$calls"; }
curl() {
  printf 'curl %s\n' "$*" >>"$calls"
  case "$*" in
    *sign-in-options*) printf '{"data":{"email_delivery":false,"registration":"first_owner"}}\n' ;;
    *auth/signup*) cat >>"$calls"; printf '\n' >>"$calls"; printf '201' ;;
  esac
  return 0
}
sleep() { :; }
DIR=$inst MODE=lan ADDRESS=192.0.2.40 OWNER_EMAIL=owner@example.com
(start_stack && create_owner && open_to_network) >/dev/null 2>&1 || bad "the start sequence failed"
first_up=$(grep -n '^compose up' "$calls" | head -n 1)
signup=$(grep -n 'auth/signup' "$calls" | head -n 1 | cut -d: -f1)
proxy_up=$(grep -n '^compose up -d --quiet-pull$' "$calls" | head -n 1 | cut -d: -f1)
case "$first_up" in
  *"up -d --quiet-pull postgres backend frontend eeprom-signer") ok "the first start names the services without the proxy" ;;
  *) bad "the first start was: $first_up" ;;
esac
if [ -n "$signup" ] && [ -n "$proxy_up" ] && [ "$signup" -lt "$proxy_up" ]; then
  ok "the owner is created before the proxy starts"
else
  bad "signup at line '$signup', proxy at line '$proxy_up'"
fi
pw=$(cat "$inst/owner-password" 2>/dev/null || true)
if [ -n "$pw" ] && grep -q "\"password\":\"$pw\"" "$calls" && ! grep "^curl" "$calls" | grep -qF "$pw"; then
  ok "the generated password reaches the sign-up on standard input, never in curl's arguments"
else
  bad "the owner password was not sent on standard input"
fi
mode=$(ls -l "$inst/owner-password" 2>/dev/null | cut -c1-10)
[ "$mode" = "-rw-------" ] && ok "the owner password file is readable by its owner only" || bad "owner password file mode is $mode"
unset -f compose curl sleep

# A failing network-boot step is reported with its output, and the install is
# not called ready.
nb="$scratch/nb"
mkdir -p "$nb/tools/netboot" "$nb/data/eeprom-keys" "$nb/data/eeprom-build" "$nb/data/net_install/sd-payload"
: >"$nb/data/eeprom-keys/privkey.pem"
printf '#!/bin/sh\necho "cloning rpi-eeprom"\necho "error: clone failed" >&2\nexit 1\n' >"$nb/tools/netboot/update-eeprom-firmware.sh"
printf '#!/bin/sh\nprintf x >"$2/boot.img"; printf x >"$2/boot.sig"; echo signed\n' >"$nb/tools/netboot/sign-boot-image.sh"
printf '#!/bin/sh\nprintf x >"$2/file"; echo extracted\n' >"$nb/tools/netboot/extract-boot-payload.sh"
chmod +x "$nb"/tools/netboot/*.sh
printf 'image' >"$scratch/boot.img"
DIR=$nb NETBOOT=yes RECOVERY_IMAGE="$scratch/boot.img"
out=$( (setup_netboot; printf 'NETBOOT=%s\n' "$NETBOOT") 2>&1 )
if printf '%s\n' "$out" | grep -q '^NETBOOT=failed$' && printf '%s\n' "$out" | grep -q "clone failed"; then
  ok "a failed firmware fetch fails network boot and shows why"
else
  bad "a failed firmware fetch was hidden: $out"
fi
printf '#!/bin/sh\necho "HEAD=00000000"\n' >"$nb/tools/netboot/update-eeprom-firmware.sh"
out=$( (setup_netboot; printf 'NETBOOT=%s\n' "$NETBOOT") 2>&1 )
printf '%s\n' "$out" | grep -q '^NETBOOT=yes$' && ok "network boot is ready when every step succeeded" || bad "a clean network-boot setup gave: $out"
printf '#!/bin/sh\necho "extracted 0 files"\n' >"$nb/tools/netboot/extract-boot-payload.sh"
rm -rf "$nb/data/net_install/sd-payload"; mkdir -p "$nb/data/net_install/sd-payload"
out=$( (setup_netboot; printf 'NETBOOT=%s\n' "$NETBOOT") 2>&1 )
printf '%s\n' "$out" | grep -q '^NETBOOT=failed$' && ok "an empty SD payload is not called ready" || bad "an empty SD payload gave: $out"

# A container bridge's address exists only inside this machine: it is not an
# address devices can be given. The real address listing runs here, over a
# stand-in for `ip`, in a shell that sources the installer afresh.
bridge_check() {
  (
    # shellcheck source=tools/install/install.sh disable=SC1091
    . "$HERE/install.sh"
    # shellcheck disable=SC2329 # called by host_ipv4s
    ip() {
      printf '%s\n' \
        '1: lo    inet 127.0.0.1/8 scope host lo\       valid_lft forever preferred_lft forever' \
        '2: eth0    inet 192.0.2.40/24 brd 192.0.2.255 scope global dynamic eth0\       valid_lft 86000sec preferred_lft 86000sec' \
        '3: docker0    inet 198.18.0.1/16 brd 198.18.255.255 scope global docker0\       valid_lft forever preferred_lft forever' \
        '4: br-1f2e3d4c5b6a    inet 198.19.0.1/16 brd 198.19.255.255 scope global br-1f2e3d4c5b6a\       valid_lft forever preferred_lft forever' \
        '5: veth1a2b3c4    inet 169.254.7.1/16 scope link veth1a2b3c4\       valid_lft forever preferred_lft forever'
    }
    ADDRESS=$1 ADDRESS_CHECK=$2
    check_lan_address
  ) >/dev/null 2>&1
}
if bridge_check 192.0.2.40 yes; then ok "the LAN interface's address is this machine's"; else bad "the LAN interface's address was refused"; fi
for a in 198.18.0.1 198.19.0.1 169.254.7.1 127.0.0.1; do
  if bridge_check "$a" yes; then bad "accepted $a, a bridge or loopback address"; else ok "refuses $a, a bridge or loopback address"; fi
done
if bridge_check 198.18.0.1 no; then ok "--skip-address-check accepts a bridge address"; else bad "--skip-address-check did not skip for a bridge address"; fi

# A listening socket is read from the kernel's table when `ss` is absent.
cat >"$scratch/tcp" <<'EOF_TCP'
  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 00000000:0050 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 1001 1 0 100 0 0 10 0
   1: 0100007F:1F90 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 1002 1 0 100 0 0 10 0
   2: 2800020C:A1B2 2800020C:01BB 01 00000000:00000000 00:00000000 00000000  1000        0 1003 1 0 20 4 30 10 -1
EOF_TCP
cat >"$scratch/tcp6" <<'EOF_TCP6'
  sl  local_address                         remote_address                        st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 00000000000000000000000000000000:C383 00000000000000000000000000000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 1004 1 0 100 0 0 10 0
EOF_TCP6
for p in 80 8080 50051; do
  if proc_listens "$p" "$scratch/tcp" "$scratch/tcp6"; then ok "port $p is seen listening"; else bad "port $p, listening, was not seen"; fi
done
for p in 443 41394 8088; do
  if proc_listens "$p" "$scratch/tcp" "$scratch/tcp6"; then bad "port $p, not listening, was seen as taken"; else ok "port $p is free"; fi
done

# Nothing is installed or cloned before every check that needs neither has
# passed: a taken port or a malformed version stops the run with the machine
# as it was. Each step after the checks only records that it ran.
# A registry that answers like ghcr.io: a manifest request without a token
# gets 401 and the token service to ask, the token service hands out an
# anonymous token, and with it a manifest is 200 when the image exists and 404
# when it does not. Release 1.1.0-rc1 has no images; 1.0.1 has a backend but
# no frontend (a release pipeline half way through). Every other request
# fails as an unreachable host would.
registry_curl() {
  case "$*" in
    *registry.test/token*) printf '{"token":"anon-token"}\n' ;;
    *registry.test/v2/*/manifests/*)
      case "$*" in
        *"Bearer anon-token"*)
          case "$*" in
            *manifests/1.1.0-rc1*|*frontend/manifests/1.0.1*) printf 'HTTP/2 404 \r\ncontent-type: application/json\r\n\r\n' ;;
            *) printf 'HTTP/2 200 \r\ncontent-type: application/vnd.oci.image.index.v1+json\r\n\r\n' ;;
          esac ;;
        *) printf 'HTTP/2 401 \r\nwww-authenticate: Bearer realm="https://registry.test/token",service="registry.test",scope="repository:uptime-lab/orchestrator/backend:pull"\r\n\r\n' ;;
      esac ;;
    *) return 7 ;;
  esac
}
curl() { registry_curl "$@"; }
IMAGE_REGISTRY=https://registry.test IMAGE_CHECK=on

for v in 1.0.0:yes 1.1.0-rc1:no 1.0.1:no; do
  got=$(images_published "${v%%:*}" 2>/dev/null || true)
  [ "$got" = "${v#*:}" ] && ok "the images of ${v%%:*} are published: $got" || bad "images of ${v%%:*}: got '$got', want ${v#*:}"
done
IMAGE_REGISTRY=https://unreachable.test
got=$(images_published 1.0.0 2>/dev/null || true)
[ "$got" = unknown ] && ok "an unreachable registry leaves the images unknown" || bad "an unreachable registry gave '$got'"
IMAGE_REGISTRY=https://registry.test

# shellcheck disable=SC2329,SC2034 # the stand-ins and the variables are main's
order_run() {
  (
    uname() { case "$1" in -s) echo Linux ;; -m) echo x86_64 ;; esac; }
    need_root_tool() { SUDO=""; }
    ensure_packages() { echo "ensure_packages umask=$(umask) version=$VERSION" >>"$calls"; }
    fetch_release() { echo fetch_release >>"$calls"; exit 0; }
    port_in_use() { [ "$1" = "${TAKEN_PORT:-80}" ]; }
    MODE="" ADDRESS="" ADDRESS_CHECK=yes NETBOOT="" RECOVERY_IMAGE="" VERSION="" VERSION_GIVEN=0
    AGENT_VERSION="" DIR="" SOURCE="" STATE="" RESUME=0 ASSUME_YES=0 PRERELEASE=0 RELEASE_RESOLVED=0
    main --mode lan --address 192.0.2.40 --netboot no --owner-email owner@example.com --yes "$@"
  ) >"$scratch/order.out" 2>&1
}
calls="$scratch/order"
: >"$calls"
order_run --dir "$scratch/fresh" && rc=0 || rc=$?
if [ "$rc" -ne 0 ] && [ ! -s "$calls" ]; then
  ok "a taken port stops the run before any package is installed or anything cloned"
else
  bad "a taken port: rc=$rc, ran: $(tr '\n' ' ' <"$calls")"
fi
: >"$calls"
order_run --dir "$scratch/fresh" --version 1.2 && rc=0 || rc=$?
if [ "$rc" -ne 0 ] && [ ! -s "$calls" ]; then
  ok "a malformed --version stops the run before anything changes"
else
  bad "a malformed --version: rc=$rc, ran: $(tr '\n' ' ' <"$calls")"
fi
# An unfinished install's own containers may hold the ports; its ports are
# checked after they are stopped, not before.
mkdir -p "$scratch/resumed/secrets" "$scratch/resumed/data/.installer"
: >"$scratch/resumed/secrets/db_password"
printf '1.0.0\n' >"$scratch/resumed/data/.installer/version"
: >"$calls"
order_run --dir "$scratch/resumed" && rc=0 || rc=$?
if [ "$rc" -eq 0 ] && grep -q fetch_release "$calls"; then
  ok "a resumed install is not refused for the ports its own containers hold"
else
  bad "a resumed install: rc=$rc, ran: $(tr '\n' ' ' <"$calls")"
fi

# The files other containers read do not inherit the caller's umask: the run
# sets its own before anything is written.
: >"$calls"
(umask 077; order_run --dir "$scratch/resumed") && rc=0 || rc=$?
if grep -q 'umask=0022' "$calls"; then ok "the install runs under umask 022 whatever the caller's"; else bad "the install ran with: $(tr '\n' ' ' <"$calls")"; fi

# The release is chosen, and its images found, before any package is
# installed: a tag whose images are not published is passed over for the
# newest one that has them, and a named one is refused.
REPO_URL=$repo RELEASES_API="file://$scratch/no-releases.json"
: >"$calls"
TAKEN_PORT=none order_run --dir "$scratch/fresh" --prerelease && rc=0 || rc=$?
if grep -q '^ensure_packages .* version=1.0.0$' "$calls"; then
  ok "--prerelease passes over a candidate whose images are not published, before any package"
else
  bad "--prerelease with an unbuilt candidate: rc=$rc, ran: $(tr '\n' ' ' <"$calls"), said: $(cat "$scratch/order.out")"
fi
: >"$calls"
TAKEN_PORT=none order_run --dir "$scratch/fresh" --version 1.1.0-rc1 && rc=0 || rc=$?
if [ "$rc" -ne 0 ] && [ ! -s "$calls" ] && grep -q "no published images" "$scratch/order.out"; then
  ok "a named release without images is refused before any package, saying why"
else
  bad "a named release without images: rc=$rc, ran: $(tr '\n' ' ' <"$calls"), said: $(cat "$scratch/order.out")"
fi
IMAGE_REGISTRY=https://unreachable.test
: >"$calls"
TAKEN_PORT=none order_run --dir "$scratch/fresh" --prerelease && rc=0 || rc=$?
if grep -q '^ensure_packages .* version=1.1.0-rc1$' "$calls" && grep -q "could not check" "$scratch/order.out"; then
  ok "an unreachable registry is said, and the newest release installed"
else
  bad "an unreachable registry: rc=$rc, ran: $(tr '\n' ' ' <"$calls"), said: $(cat "$scratch/order.out")"
fi
IMAGE_REGISTRY=https://registry.test
REPO_URL="$scratch/no-repo"

# A release cloned under a strict umask still gives the proxy routes every
# container can read.
rel="$scratch/release"
git init -q "$rel"
mkdir -p "$rel/proxy"
printf 'http: {}\n' >"$rel/proxy/lan.yml"
printf 'http: {}\n' >"$rel/proxy/tls.yml"
printf 'services: {}\n' >"$rel/docker-compose.community.yml"
git -C "$rel" add -A
git -C "$rel" -c user.name=t -c user.email=t@example.com commit -q -m release
git -C "$rel" tag platform-v1.0.0
out=$( (umask 077; REPO_URL=$rel DIR="$scratch/cloned" SOURCE="" VERSION=1.0.0 VERSION_GIVEN=1 RESUME=0 RELEASE_RESOLVED=0
  fetch_release >/dev/null 2>&1; ls -l "$scratch/cloned/proxy/tls.yml" | cut -c1-10; ls -ld "$scratch/cloned/proxy" | cut -c1-10) 2>&1 | tr '\n' ' ')
[ "$out" = "-rw-r--r-- drwxr-xr-x " ] && ok "proxy routes cloned under umask 077 are readable by the proxy" || bad "proxy routes cloned under umask 077: $out"

# An agent release downloaded under a strict umask is readable by the import
# container, which runs as another uid.
ai="$scratch/agent-import"
mkdir -p "$ai/data/.installer"
out=$( (
  umask 077
  curl() {
    while [ $# -gt 0 ]; do case "$1" in (-o) printf 'pkg' >"$2"; shift 2 ;; (*) shift ;; esac; done
  }
  compose() { :; }
  DIR=$ai STATE="$ai/data/.installer" AGENT_VERSION=9.9.9 RESUME=0
  import_agent >/dev/null 2>&1
  ls -ld "$ai/agent-release-9.9.9" | cut -c1-10
  ls -l "$ai/agent-release-9.9.9/orchestrator-agent_9.9.9_linux_arm64.deb" | cut -c1-10
) 2>&1 | tr '\n' ' ')
[ "$out" = "drwxr-xr-x -rw-r--r-- " ] && ok "an agent release downloaded under umask 077 is readable by the import" || bad "agent release under umask 077: $out"

# The bootloader firmware fetched under a strict umask is readable by the
# signer, which runs as another uid.
printf '#!/bin/sh\nmkdir -p "$1/rpi-eeprom"; printf x >"$1/rpi-eeprom/pieeprom.bin"; echo "HEAD=00000000"\n' >"$nb/tools/netboot/update-eeprom-firmware.sh"
printf '#!/bin/sh\nprintf x >"$2/boot.img"; printf x >"$2/boot.sig"; echo signed\n' >"$nb/tools/netboot/sign-boot-image.sh"
printf '#!/bin/sh\nprintf x >"$2/file"; echo extracted\n' >"$nb/tools/netboot/extract-boot-payload.sh"
rm -rf "$nb/data/eeprom-build"
out=$( (umask 077; DIR=$nb NETBOOT=yes RECOVERY_IMAGE="$scratch/boot.img"; setup_netboot >/dev/null 2>&1
  ls -ld "$nb/data/eeprom-build/rpi-eeprom" | cut -c1-10; ls -l "$nb/data/eeprom-build/rpi-eeprom/pieeprom.bin" | cut -c1-10) 2>&1 | tr '\n' ' ')
[ "$out" = "drwxr-xr-x -rw-r--r-- " ] && ok "bootloader firmware fetched under umask 077 is readable by the signer" || bad "firmware under umask 077: $out"

# An environment file the installer did not write is an existing install,
# whatever its secrets and data paths are; it is never overwritten.
envinst="$scratch/envinst"
mkdir -p "$envinst"
printf 'DOMAIN=orchestrator.example.com\nSECRETS_DIR=/etc/orchestrator/secrets\nDATA_PATH=/srv/orchestrator\n' >"$envinst/.env"
out=$( (DIR=$envinst STATE="$envinst/data/.installer" RESUME=0; detect_existing_install) 2>&1 ) && rc=0 || rc=$?
if [ "$rc" -ne 0 ] && printf '%s\n' "$out" | grep -q "SECRETS_DIR=/etc/orchestrator/secrets" && printf '%s\n' "$out" | grep -q "Upgrading"; then
  ok "a .env this installer did not write is refused, naming its paths"
else
  bad "a foreign .env: rc=$rc, said: $out"
fi
# A resumed install rewrites its own .env, keeping a copy of one that differs.
mkdir -p "$envinst/data/.installer"
printf '1.0.0\n' >"$envinst/data/.installer/version"
out=$( (
  compose() { :; }
  DIR=$envinst MODE=lan ADDRESS=192.0.2.40 VERSION=1.0.0
  stop_unfinished >/dev/null 2>&1
  cat "$envinst"/.env.bak.* 2>/dev/null | grep -c '^DATA_PATH=/srv/orchestrator$'
  ls -l "$envinst/.env" | cut -c1-10
) 2>&1 | tr '\n' ' ')
[ "$out" = "1 -rw------- " ] && ok "a rewritten .env keeps the previous one beside it" || bad "rewriting .env: $out"

# A public install whose certificate is not issued tells what to check and
# what makes the proxy ask again, retries once itself, and leaves a marker
# that a later run finishes.
cert="$scratch/cert"
mkdir -p "$cert/data/.installer"
calls="$scratch/cert-calls"
: >"$calls"
out=$( (
  compose() { printf 'compose %s\n' "$*" >>"$calls"; }
  curl() { return 7; }
  docker_stub() { printf 'docker %s\n' "$*" >>"$calls"; }
  sleep() { :; }
  DOCKER=docker_stub DIR=$cert STATE="$cert/data/.installer" MODE=public DOMAIN=orchestrator.example.com
  open_to_network
) 2>&1 ) && rc=0 || rc=$?
if [ "$rc" -eq 0 ] && grep -q '^docker restart orchestrator-proxy$' "$calls" &&
  printf '%s\n' "$out" | grep -q 'restart orchestrator-proxy' && printf '%s\n' "$out" | grep -q 'port 80' &&
  [ -f "$cert/data/.installer/certificate-pending" ]; then
  ok "a certificate not issued: one retry, the checks and the command named, a marker left"
else
  bad "a certificate not issued: rc=$rc, ran: $(tr '\n' ' ' <"$calls"), said: $out"
fi
# The later run asks nothing, restarts the proxy and clears the marker once
# HTTPS answers.
: >"$cert/data/.installer/complete"
printf 'DOMAIN=orchestrator.example.com\n' >"$cert/.env"
: >"$calls"
out=$( (
  uname() { case "$1" in (-s) echo Linux ;; (-m) echo x86_64 ;; esac; }
  need_root_tool() { SUDO=""; }
  pick_docker() { DOCKER=docker_stub; }
  docker_stub() { printf 'docker %s\n' "$*" >>"$calls"; }
  # HTTPS answers once the proxy has asked again.
  curl() { grep -q restart "$calls"; }
  sleep() { :; }
  MODE="" DOMAIN="" OWNER_EMAIL="" NETBOOT="" DIR="" SOURCE="" STATE="" TTY_IN=/nonexistent/tty TTY_OPEN=0
  main --dir "$cert"
) 2>&1 ) && rc=0 || rc=$?
if [ "$rc" -eq 0 ] && grep -q '^docker restart orchestrator-proxy$' "$calls" && [ ! -f "$cert/data/.installer/certificate-pending" ]; then
  ok "a run over an install waiting for its certificate re-checks only that"
else
  bad "re-checking the certificate: rc=$rc, ran: $(tr '\n' ' ' <"$calls"), said: $out"
fi
unset -f curl

# The bundled proxies read their routes through the mounted proxy/ DIRECTORY:
# a route file an upgrade replaces is a new inode, which a single-file mount
# never shows the running proxy.
root="$HERE/../.."
compose_file="$root/docker-compose.community.yml"
if grep -Eq '^[[:space:]]*- \./proxy/[a-z]+\.yml:' "$compose_file"; then bad "a bundled proxy mounts a single route file"; else ok "no bundled proxy mounts a single route file"; fi
n=$(grep -c -- '- \./proxy:/etc/traefik/routes:ro$' "$compose_file" || true)
[ "$n" = 2 ] && ok "both bundled proxies mount proxy/ read-only" || bad "proxy/ is mounted $n times"
for f in lan tls; do
  if grep -q -- "--providers.file.filename=/etc/traefik/routes/$f.yml" "$compose_file" &&
    grep -q -- "--providers.file.watch=true" "$compose_file"; then
    ok "the $f proxy watches routes/$f.yml"
  else
    bad "the $f proxy does not watch routes/$f.yml"
  fi
done

# One address may hold many agent connections — a whole site behind one NAT
# shares one — and the bound is one setting.
for f in lan tls; do
  if grep -q 'amount: {{ env "PROXY_AGENT_CONNECTIONS_PER_ADDRESS" }}' "$root/proxy/$f.yml"; then
    ok "proxy/$f.yml takes its per-address agent connection bound from the environment"
  else
    bad "proxy/$f.yml has a fixed per-address agent connection bound"
  fi
done
d=$(sed -n 's/.*PROXY_AGENT_CONNECTIONS_PER_ADDRESS=\${PROXY_AGENT_CONNECTIONS_PER_ADDRESS:-\([0-9]*\)}.*/\1/p' "$compose_file" | sort -u)
n=$(grep -c 'PROXY_AGENT_CONNECTIONS_PER_ADDRESS=\${PROXY_AGENT_CONNECTIONS_PER_ADDRESS:-' "$compose_file" || true)
if [ "$n" = 2 ] && [ -n "$d" ] && [ "$(printf '%s\n' "$d" | wc -l)" -eq 1 ] && [ "$d" -ge 4096 ]; then
  ok "both proxies default to $d agent connections per address"
else
  bad "per-address agent connection default: '$d' in $n places"
fi

# Port 443 of the HTTPS proxy closes a connection that sends no ClientHello:
# the published entry point carries only a TLS-passthrough TCP route, whose
# read bound ends at the ClientHello, to the HTTP entry point on loopback,
# whose read bound must stay 0 for uploads. One address holds a bounded
# number of those connections.
tls_routes="$root/proxy/tls.yml"
if grep -q -- '--entrypoints.https.address=:443$' "$compose_file" &&
  grep -Eq -- '--entrypoints.https.transport.respondingTimeouts.readTimeout=[1-9][0-9]*s$' "$compose_file" &&
  grep -q -- '--entrypoints.websecure.address=127.0.0.1:8443$' "$compose_file" &&
  grep -q -- '--entrypoints.websecure.proxyProtocol.trustedIPs=127.0.0.1/32$' "$compose_file" &&
  grep -q -- '--entrypoints.websecure.transport.respondingTimeouts.readTimeout=0$' "$compose_file" &&
  ! grep -q -- '--entrypoints.websecure.address=:443' "$compose_file"; then
  ok "port 443 has a ClientHello bound and the HTTP entry point behind it none"
else
  bad "port 443 of the HTTPS proxy waits for a ClientHello without a bound"
fi
if sed -n '/^tcp:/,$p' "$tls_routes" | grep -q 'entryPoints: \[https\]' &&
  sed -n '/^tcp:/,$p' "$tls_routes" | grep -q 'tls: {passthrough: true}' &&
  sed -n '/^tcp:/,$p' "$tls_routes" | grep -q 'address: "127.0.0.1:8443"' &&
  grep -q 'amount: {{ env "PROXY_HTTPS_CONNECTIONS_PER_ADDRESS" }}' "$tls_routes" &&
  ! sed -n '/^http:/,/^tcp:/p' "$tls_routes" | grep -q 'entryPoints: \[https\]'; then
  ok "proxy/tls.yml passes port 443 to the HTTP entry point under a per-address bound"
else
  bad "proxy/tls.yml does not pass port 443 through a bounded TCP route"
fi
d=$(sed -n 's/.*PROXY_HTTPS_CONNECTIONS_PER_ADDRESS=\${PROXY_HTTPS_CONNECTIONS_PER_ADDRESS:-\([0-9]*\)}.*/\1/p' "$compose_file")
if [ -n "$d" ] && [ "$d" -ge 1 ]; then
  ok "the HTTPS proxy defaults to $d HTTPS connections per address"
else
  bad "no default for the per-address HTTPS connection bound"
fi

# Port 80 of the plain-HTTP proxy bounds how many connections one address
# holds: the published entry point carries only a catch-all TCP route, under
# a per-address bound, to the HTTP entry point on loopback, which trusts the
# PROXY protocol header from loopback alone and keeps no read bound for
# uploads.
lan_routes="$root/proxy/lan.yml"
lan_service=$(sed -n '/^  proxy-lan:$/,/^  [a-z-]*:$/p' "$compose_file")
if printf '%s\n' "$lan_service" | grep -q -- '--entrypoints.http.address=:80$' &&
  printf '%s\n' "$lan_service" | grep -q -- '--entrypoints.web.address=127.0.0.1:8081$' &&
  printf '%s\n' "$lan_service" | grep -q -- '--entrypoints.web.proxyProtocol.trustedIPs=127.0.0.1/32$' &&
  printf '%s\n' "$lan_service" | grep -q -- '--entrypoints.web.transport.respondingTimeouts.readTimeout=0$' &&
  ! printf '%s\n' "$lan_service" | grep -q -- '--entrypoints.web.address=:80'; then
  ok "port 80 of the plain-HTTP proxy is a TCP hop to the HTTP entry point on loopback"
else
  bad "port 80 of the plain-HTTP proxy serves HTTP routes directly"
fi
if sed -n '/^tcp:/,$p' "$lan_routes" | grep -q 'entryPoints: \[http\]' &&
  sed -n '/^tcp:/,$p' "$lan_routes" | grep -q 'address: "127.0.0.1:8081"' &&
  grep -q 'amount: {{ env "PROXY_HTTP_CONNECTIONS_PER_ADDRESS" }}' "$lan_routes" &&
  ! sed -n '/^http:/,/^tcp:/p' "$lan_routes" | grep -q 'entryPoints: \[http\]'; then
  ok "proxy/lan.yml passes port 80 to the HTTP entry point under a per-address bound"
else
  bad "proxy/lan.yml does not pass port 80 through a bounded TCP route"
fi
d=$(printf '%s\n' "$lan_service" | sed -n 's/.*PROXY_HTTP_CONNECTIONS_PER_ADDRESS=\${PROXY_HTTP_CONNECTIONS_PER_ADDRESS:-\([0-9]*\)}.*/\1/p')
if [ -n "$d" ] && [ "$d" -ge 1 ]; then
  ok "the plain-HTTP proxy defaults to $d HTTP connections per address"
else
  bad "no default for the per-address HTTP connection bound"
fi

# A helper's own output is for someone running it by hand: a step that
# succeeds shows none of it, a step that fails shows all of it.
out=$(run_logged "A step" sh -c 'echo "HEAD=0123abcd 2711=a.bin"; echo "next: tools/netboot/x.sh"' 2>&1) && rc=0 || rc=$?
[ "$rc" -eq 0 ] && [ -z "$out" ] && ok "a step that succeeds shows none of its helper's output" ||
  bad "a step that succeeds printed: $out"
out=$(run_logged "A step" sh -c 'echo first; echo "the reason"; exit 3' 2>&1) && rc=0 || rc=$?
if [ "$rc" -ne 0 ] && printf '%s\n' "$out" | grep -q "^first$" && printf '%s\n' "$out" | grep -q "exit status 3"; then
  ok "a step that fails shows all of its helper's output"
else
  bad "a step that fails: rc=$rc, said: $out"
fi

# A Docker volume left by an earlier install elsewhere: Compose names its
# volumes after the directory's name, so an install from another directory
# with the same name would open the earlier database with new credentials.
vol="$scratch/vol/orchestrator"
mkdir -p "$vol"
out=$( (
  docker_stub() {
    case "$*" in
      ("volume inspect --format {{index .Options \"device\"}} orchestrator_orchestrator-postgres-data")
        printf '/elsewhere/orchestrator/data/postgres\n' ;;
      ("volume inspect "*) return 1 ;;
    esac
  }
  DOCKER=docker_stub DIR=$vol
  check_volumes
) 2>&1 ) && rc=0 || rc=$?
if [ "$rc" -ne 0 ] && printf '%s\n' "$out" | grep -q "orchestrator_orchestrator-postgres-data" &&
  printf '%s\n' "$out" | grep -q "/elsewhere/orchestrator/data/postgres" &&
  printf '%s\n' "$out" | grep -q "docker volume rm orchestrator_orchestrator-postgres-data"; then
  ok "a volume bound to another directory is refused, naming it and its removal"
else
  bad "a volume bound to another directory: rc=$rc, said: $out"
fi
out=$( (
  docker_stub() {
    case "$*" in
      ("volume inspect "*orchestrator-postgres-data) printf '%s/data/postgres\n' "$vol" ;;
      ("volume inspect "*orchestrator-backend-storage) printf '%s/data/backend/storage\n' "$vol" ;;
    esac
  }
  DOCKER=docker_stub DIR=$vol
  check_volumes
) 2>&1 ) && rc=0 || rc=$?
[ "$rc" -eq 0 ] && ok "volumes bound to this directory are this install's" || bad "this directory's own volumes were refused: $out"
out=$( (
  docker_stub() { return 1; }
  DOCKER=docker_stub DIR=$vol
  check_volumes
) 2>&1 ) && rc=0 || rc=$?
[ "$rc" -eq 0 ] && ok "no volume yet is a new install" || bad "no volume was refused: $out"
[ "$(compose_project_name /srv/My.Orchestrator_1)" = myorchestrator_1 ] &&
  ok "the project name is the directory name as Compose normalises it" ||
  bad "project name: $(compose_project_name /srv/My.Orchestrator_1)"

# Docker without its Compose plugin: only the missing piece is installed,
# from the packaging the engine came from, and apt is never allowed to
# remove a package, so an engine cannot be replaced.
stubbin="$scratch/stubbin"
mkdir -p "$stubbin"
cat >"$stubbin/apt-get" <<'EOF_APT'
#!/bin/sh
printf 'apt-get %s\n' "$*" >>"$APT_CALLS"
EOF_APT
chmod +x "$stubbin/apt-get"
APT_CALLS="$scratch/apt-calls"
export APT_CALLS
# packaging_case INSTALLED_PACKAGE HAS_BINARY OS_ID — runs ensure_packages
# with Docker's compose command failing until apt-get ran.
packaging_case() {
  : >"$APT_CALLS"
  (
    PATH="$stubbin:$PATH"
    SUDO="" NETBOOT=no
    _pkg=$1 _bin=$2 _os=$3
    pick_docker() { DOCKER=docker_stub; }
    docker_stub() {
      case "$*" in
        ("compose version"*) grep -q install "$APT_CALLS" ;;
        (*) printf 'Docker version 27.0.0, build x\n' ;;
      esac
    }
    docker_engine_present() { [ "$_bin" = yes ]; }
    package_installed() { [ "$1" = "$_pkg" ]; }
    os_id() { printf '%s' "$_os"; }
    ensure_packages
  ) 2>&1
}
out=$(packaging_case docker-ce yes ubuntu) && rc=0 || rc=$?
if [ "$rc" -eq 0 ] && grep -q 'install .*--no-remove.* docker-compose-plugin' "$APT_CALLS" && ! grep -q 'docker\.io' "$APT_CALLS"; then
  ok "docker-ce without Compose: only docker-compose-plugin is installed, with --no-remove"
else
  bad "docker-ce without Compose: rc=$rc, apt: $(tr '\n' ' ' <"$APT_CALLS"), said: $out"
fi
out=$(packaging_case docker.io yes ubuntu) && rc=0 || rc=$?
if [ "$rc" -eq 0 ] && grep -q 'install .*--no-remove.* docker-compose-v2' "$APT_CALLS" && ! grep -q 'install .*docker\.io' "$APT_CALLS"; then
  ok "docker.io without Compose: only docker-compose-v2 is installed"
else
  bad "docker.io without Compose: rc=$rc, apt: $(tr '\n' ' ' <"$APT_CALLS"), said: $out"
fi
out=$(packaging_case "" yes ubuntu) && rc=0 || rc=$?
if [ "$rc" -ne 0 ] && [ ! -s "$APT_CALLS" ] && printf '%s\n' "$out" | grep -q "Compose plugin"; then
  ok "a Docker of another packaging without Compose is refused, nothing installed"
else
  bad "a Docker of another packaging: rc=$rc, apt: $(tr '\n' ' ' <"$APT_CALLS"), said: $out"
fi
out=$(packaging_case "" no ubuntu) && rc=0 || rc=$?
if [ "$rc" -eq 0 ] && grep -q 'install .*--no-remove.* docker\.io docker-compose-v2' "$APT_CALLS"; then
  ok "no Docker on Ubuntu: docker.io and docker-compose-v2, with --no-remove"
else
  bad "no Docker on Ubuntu: rc=$rc, apt: $(tr '\n' ' ' <"$APT_CALLS"), said: $out"
fi
out=$(packaging_case docker.io yes debian) && rc=0 || rc=$?
if [ "$rc" -ne 0 ] && ! grep -q 'install' "$APT_CALLS" && printf '%s\n' "$out" | grep -q "Compose plugin"; then
  ok "Debian's docker.io without Compose: nothing is installed, the Compose plugin is asked for"
else
  bad "Debian's docker.io without Compose: rc=$rc, apt: $(tr '\n' ' ' <"$APT_CALLS"), said: $out"
fi

printf 'passed %d, failed %d\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
