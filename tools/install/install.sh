#!/bin/sh
#
# Install an Uptime Orchestrator server on this machine.
#
#   curl -fsSL https://get.orchestrator.sh | sh
#
# It asks how the server will be reached, then does what docs/INSTALL.md
# describes by hand: Docker (on Ubuntu, if missing), the release's files, the
# credential files, the environment file, one of the two reverse proxies
# bundled with the compose file, the stack itself, the owner account, an
# agent release, and — if you want devices to boot from the network — the
# signing key and the recovery image. It ends by printing the address to open.
#
# The owner account is created through the backend's loopback port BEFORE the
# proxy starts, so there is no moment in which the server answers on the
# network while sign-up still admits a first owner. Its password is generated
# and left in a file readable only by you.
#
# Two ways to be reached, and the question is asked first because the answer
# is written into every device you later adopt:
#
#   lan     on a local network you trust, over plain HTTP, by the name your
#           router gives this machine or by its address. No certificate.
#   public  from the internet, by a DNS name that points here, over HTTPS
#           with a Let's Encrypt certificate.
#
# Every question has a flag, so the script also runs unattended:
#
#   --mode lan|public        how the server is reached
#   --address NAME_OR_IP     lan: what devices and browsers will call it
#   --domain NAME            public: the DNS name pointing at this machine
#   --skip-address-check     lan: accept an address that is not one of this
#                            machine's own (a NAT or proxy forwards it here)
#   --email ADDRESS          public: where Let's Encrypt sends expiry notices
#   --owner-email ADDRESS    the email the owner account signs in with
#   --netboot yes|no         set up network boot and the provisioning card
#   --recovery-image FILE    a recovery image you built, instead of the
#                            published one (tools/netboot/recovery-image.env)
#   --version X.Y.Z          the platform release (default: the latest final
#                            release)
#   --prerelease             take the newest release even if it is a release
#                            candidate (X.Y.Z-rc1), when --version is not given
#   --agent-version X.Y.Z    the agent release to import (default: the latest)
#   --dir DIR                where to install (default: ./orchestrator)
#   --source DIR             install in place in an existing checkout of the
#                            release instead of cloning it (--dir is ignored)
#   --yes                    do not ask for confirmation
#
# Every answer, and every port the stack binds, is checked before anything on
# the machine changes, and so are which release to install and whether its
# images are published, when git and curl are already here. Only whether an
# existing checkout is that release (and, on a machine without git, the
# release itself) is checked after the missing tools are installed.
#
# It never changes a finished install, nor one it did not make (a directory
# whose .env or secrets/ it did not write): those are refused, and upgrading
# is docs/INSTALL.md, "Upgrading". An install this script started and did
# not finish (a failure, an interrupted run) is resumed by running it again
# with the same answers: the credentials it already wrote are kept, because
# the database was created with them, and the steps already done are skipped.
# A public install whose certificate was not issued yet is finished but for
# that; running the script again then only makes the proxy ask again.

set -eu

REPO_URL=${ORCHESTRATOR_REPO_URL:-https://github.com/uptime-lab/orchestrator.git}
RELEASES_API=${ORCHESTRATOR_RELEASES_API:-https://api.github.com/repos/uptime-lab/orchestrator/releases/latest}
AGENT_RELEASES_API=${ORCHESTRATOR_AGENT_RELEASES_API:-https://api.github.com/repos/uptime-lab/orchestrator-agent/releases/latest}
AGENT_DOWNLOAD=${ORCHESTRATOR_AGENT_DOWNLOAD:-https://github.com/uptime-lab/orchestrator-agent/releases/download}
COMPOSE_FILE=docker-compose.community.yml
# Where the release's images are looked up before anything is installed.
# ORCHESTRATOR_IMAGE_CHECK=off skips the lookup (a mirror that needs a login).
IMAGE_REGISTRY=${ORCHESTRATOR_IMAGE_REGISTRY:-https://ghcr.io}
IMAGE_REPOSITORY=${ORCHESTRATOR_IMAGE_REPOSITORY:-uptime-lab/orchestrator}
IMAGE_CHECK=${ORCHESTRATOR_IMAGE_CHECK:-on}
RELEASE_IMAGES="backend frontend eeprom-signer"
MANIFEST_TYPES='application/vnd.oci.image.index.v1+json, application/vnd.docker.distribution.manifest.list.v2+json, application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.v2+json'

MODE=""
ADDRESS=""
ADDRESS_CHECK=yes
DOMAIN=""
EMAIL=""
OWNER_EMAIL=""
NETBOOT=""
NETBOOT_FAILED=""
RECOVERY_IMAGE=""
VERSION=""
VERSION_GIVEN=0
RELEASE_RESOLVED=0
CERT_PENDING=0
PRERELEASE=0
AGENT_VERSION=""
DIR=""
SOURCE=""
STATE=""
RESUME=0
ASSUME_YES=0
_choice=""
_go=""
DOCKER=docker
SUDO=""

# Questions are read from the terminal even when the script itself arrives
# on standard input (`curl … | sh`).
TTY_IN=${ORCHESTRATOR_INSTALL_TTY:-/dev/tty}
TTY_OPEN=0

say() { printf '%s\n' "$*"; }
step() { printf '\n==> %s\n' "$*"; }
warn() { printf 'WARNING: %s\n' "$*" >&2; }
die() { printf 'ERROR: %s\n' "$*" >&2; exit 1; }

# ask VARIABLE "question" [default] — reads one line from the terminal.
ask() {
  _var=$1 _question=$2 _default=${3:-}
  if [ -n "$_default" ]; then
    printf '%s [%s]: ' "$_question" "$_default" >&2
  else
    printf '%s: ' "$_question" >&2
  fi
  _answer=""
  if [ "$TTY_OPEN" -eq 0 ]; then
    # Opened once and kept: a file standing in for the terminal must be read
    # line after line, not from its start at every question. The open is
    # tried in a subshell first: a failed redirection on `exec` ends a
    # non-interactive dash at once, before any `||` after it could run.
    if ! (exec 3<"$TTY_IN") 2>/dev/null; then
      printf '\n' >&2
      die "no terminal to ask \"$_question\" on; pass the answer as a flag (see --help)"
    fi
    exec 3<"$TTY_IN"
    TTY_OPEN=1
  fi
  if ! IFS= read -r _answer <&3; then
    die "no answer to \"$_question\"; pass it as a flag (see --help)"
  fi
  [ -n "$_answer" ] || _answer=$_default
  eval "$_var=\$_answer"
}

valid_ipv4() {
  printf '%s\n' "$1" | grep -Eq '^([0-9]{1,3}\.){3}[0-9]{1,3}$' || return 1
  _oldifs=$IFS; IFS=.
  # shellcheck disable=SC2086
  set -- $1
  IFS=$_oldifs
  for _octet in "$@"; do [ "$_octet" -le 255 ] || return 1; done
}

# A host name as DNS and the bootloader accept it: labels of letters, digits
# and hyphens, at least one dot, no underscore, at most 253 characters.
valid_hostname() {
  [ "${#1}" -le 253 ] || return 1
  printf '%s\n' "$1" | grep -Eq '^([A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?\.)+[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?$'
}

# The bootloader of a Raspberry Pi resolves names through DNS only; a
# multicast name (.local) answers on a laptop and never on a device.
is_mdns_name() {
  case "$1" in *.local|*.local.) return 0 ;; *) return 1 ;; esac
}

valid_version() {
  printf '%s\n' "$1" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.]+)?$'
}

# An address the sign-up form accepts, and one that can be written into a
# JSON string as it is: no quote, backslash, space or control character.
valid_email() {
  printf '%s\n' "$1" | grep -Eq '^[^@"\\[:space:][:cntrl:]]+@[^@"\\[:space:][:cntrl:]]+\.[^@"\\[:space:][:cntrl:]]+$'
}

# This machine's own IPv4 addresses, one per line: an address devices are
# given must lead here from the network. Loopback is excluded, and so are
# container bridges (docker0, br-*, veth*): their addresses exist only inside
# this machine, and no device on the network reaches one.
host_ipv4s() {
  ip -4 -o addr show 2>/dev/null |
    awk '$2 !~ /^(docker[0-9]*|br-.*|veth.*)$/ {print $4}' | cut -d/ -f1 | grep -v '^127\.' || true
}

# The IPv4 addresses a name resolves to on this machine, one per line.
resolve_ipv4s() {
  getent ahostsv4 "$1" 2>/dev/null | awk '{print $1}' | sort -u
}

# check_lan_address — the address is written into every adopted device and
# into the bootloader of every network-booted one, so it must lead to THIS
# machine; the installer's own health checks use the loopback and would not
# notice an address that leads elsewhere.
check_lan_address() {
  [ "$ADDRESS_CHECK" = yes ] || return 0
  _mine=$(host_ipv4s)
  if [ -z "$_mine" ]; then
    warn "cannot list this machine's addresses; not checking that $ADDRESS leads here"
    return 0
  fi
  if valid_ipv4 "$ADDRESS"; then
    _targets=$ADDRESS
  else
    _targets=$(resolve_ipv4s "$ADDRESS" || true)
    [ -n "$_targets" ] ||
      die "$ADDRESS does not resolve on this machine, so devices could not resolve it either. Use a name your router gives this machine, or its address (pass --skip-address-check if you know better)"
  fi
  _foreign="" _here=0
  for _t in $_targets; do
    if printf '%s\n' "$_mine" | grep -qxF "$_t"; then _here=1; else _foreign="$_foreign $_t"; fi
  done
  [ -n "$_foreign" ] || return 0
  # A name is good only if EVERY address it resolves to is this machine's: a
  # device takes whichever answer it gets, and one that takes a stale record
  # (a router keeping an old lease's name) never finds the server.
  if [ "$_here" -eq 1 ]; then
    die "$ADDRESS resolves to more than one address, and not every one is this machine (not this machine:$_foreign): a device that is given that answer would never reach this server. Use this machine's address instead, or remove the stale record from the router (pass --skip-address-check if a NAT or proxy forwards that address here)"
  fi
  die "$ADDRESS leads to $(printf '%s\n' "$_targets" | tr '\n' ' ')but this machine's addresses are $(printf '%s\n' "$_mine" | tr '\n' ' ')— devices would be sent to another host. Use one of this machine's addresses, or pass --skip-address-check if a NAT or proxy in front forwards that address here"
}

# A git command run on a checkout this user may not own (the installer runs
# under sudo as often as not); reading tags changes nothing in it.
gitd() { _gd=$1; shift; git -c safe.directory='*' -C "$_gd" "$@"; }

# is_git_checkout DIR — DIR is the top of a git checkout (not a directory
# inside some other repository).
is_git_checkout() {
  [ "$(gitd "$1" rev-parse --show-toplevel 2>/dev/null || true)" = "$1" ]
}

# checkout_versions DIR — the platform versions whose tag points at the
# checked-out commit, one per line.
checkout_versions() {
  gitd "$1" tag --points-at HEAD 2>/dev/null | sed -n 's/^platform-v//p' |
    while IFS= read -r _t; do valid_version "$_t" && printf '%s\n' "$_t"; done
  return 0
}

# versions_ascending — the versions on standard input in version order:
# numerically per field, and a final release above its own release
# candidates (1.2.0-rc2 < 1.2.0-rc10 < 1.2.0). `sort -V` is not used: it
# orders 1.2.0 below 1.2.0-rc1, and not every sort implements it.
versions_ascending() {
  awk '{
    v = $0; pre = ""; i = index(v, "-")
    if (i > 0) { pre = substr(v, i + 1); v = substr(v, 1, i - 1) }
    split(v, n, ".")
    final = (pre == "") ? 1 : 0
    alpha = pre; num = 0
    if (match(pre, /[0-9]+$/)) { alpha = substr(pre, 1, RSTART - 1); num = substr(pre, RSTART) }
    if (alpha == "") alpha = "-"
    print n[1], n[2], n[3], final, alpha, num, $0
  }' | sort -k1,1n -k2,2n -k3,3n -k4,4n -k5,5 -k6,6n | awk '{print $7}'
}

# highest_version — the highest of the versions on standard input.
highest_version() { versions_ascending | tail -n 1; }

# platform_tags — the release versions tagged in the repository, final ones
# only unless --prerelease.
platform_tags() {
  git ls-remote --tags --refs "$REPO_URL" 'platform-v*' 2>/dev/null |
    sed -n 's|.*refs/tags/platform-v||p' |
    while IFS= read -r _t; do
      valid_version "$_t" || continue
      case "$_t" in *-*) [ "$PRERELEASE" -eq 1 ] || continue ;; esac
      printf '%s\n' "$_t"
    done
  return 0
}

# release_candidates — the releases to install when none is named, best
# first. The GitHub Release the release pipeline publishes once a release's
# images exist comes first; the repository's tags follow, newest first, so a
# release whose GitHub Release is missing (or an install from a mirror that
# has none) still resolves. Release candidates count only with --prerelease,
# and then the tags decide, because the Releases API never calls a
# pre-release "latest".
release_candidates() {
  _api=""
  [ "$PRERELEASE" -eq 1 ] || _api=$(latest_tag "$RELEASES_API" platform-v || true)
  if [ -n "$_api" ] && valid_version "$_api"; then
    printf '%s\n' "$_api"
  else
    _api=""
  fi
  platform_tags | versions_ascending | awk -v skip="$_api" '$0 != skip { v[n++] = $0 } END { while (n > 0) print v[--n] }'
}

# latest_platform_version — the best of release_candidates.
latest_platform_version() { release_candidates | head -n 1; }

# http_status — the status code of the last HTTP response in the headers on
# standard input (a proxy's CONNECT answer may come first).
http_status() {
  tr -d '\r' | sed -n 's|^HTTP/[0-9.]* \([0-9][0-9][0-9]\).*|\1|p' | tail -n 1
}

# auth_param NAME CHALLENGE — one quoted parameter of a WWW-Authenticate
# Bearer challenge.
auth_param() {
  printf '%s\n' "$2" | sed -n 's/.*'"$1"'="\([^"]*\)".*/\1/p' | head -n 1
}

# manifest_status IMAGE TAG — the registry's HTTP status for the manifest of
# IMAGE:TAG, asked anonymously: a registry that wants a token for a public
# image says where to get one (the WWW-Authenticate challenge), and an
# anonymous token is asked there. 000 when the registry does not answer.
manifest_status() {
  _url="$IMAGE_REGISTRY/v2/$IMAGE_REPOSITORY/$1/manifests/$2"
  _head=$(curl -sSI --max-time 15 -H "Accept: $MANIFEST_TYPES" "$_url" 2>/dev/null | tr -d '\r' || true)
  _code=$(printf '%s\n' "$_head" | http_status)
  if [ "$_code" = 401 ]; then
    _challenge=$(printf '%s\n' "$_head" | sed -n 's/^[Ww][Ww][Ww]-[Aa][Uu][Tt][Hh][Ee][Nn][Tt][Ii][Cc][Aa][Tt][Ee]: *[Bb]earer *//p' | head -n 1)
    _realm=$(auth_param realm "$_challenge")
    _token=""
    if [ -n "$_realm" ]; then
      _token=$(curl -fsS --max-time 15 -G --data-urlencode "service=$(auth_param service "$_challenge")" \
        --data-urlencode "scope=$(auth_param scope "$_challenge")" "$_realm" 2>/dev/null |
        sed -n 's/.*"token": *"\([^"]*\)".*/\1/p' | head -n 1 || true)
    fi
    if [ -n "$_token" ]; then
      _code=$(curl -sSI --max-time 15 -H "Accept: $MANIFEST_TYPES" -H "Authorization: Bearer $_token" "$_url" 2>/dev/null |
        http_status || true)
    fi
  fi
  printf '%s\n' "${_code:-000}"
}

# images_published VERSION — yes when every image of the release is in the
# registry, no when one is missing, unknown when the registry could not say
# (unreachable, or it wants a login).
images_published() {
  [ "$IMAGE_CHECK" != off ] || { echo yes; return 0; }
  _state=yes
  for _img in $RELEASE_IMAGES; do
    case $(manifest_status "$_img" "$1") in
      200) ;;
      404) echo no; return 0 ;;
      *) _state=unknown ;;
    esac
  done
  echo "$_state"
}

# resolve_release — VERSION: the release named, the one a resumed install
# began with, the one a --source checkout is, or else the newest release
# whose images are published. A tag exists as soon as it is pushed, its
# images only once the release pipeline has built them (and never, for a tag
# the pipeline refused), so the images are looked up here, before anything
# is installed or cloned.
resolve_release() {
  [ "$RELEASE_RESOLVED" -eq 0 ] || return 0
  # A resumed install keeps the release it began with.
  if [ -z "$VERSION" ] && [ -r "$STATE/version" ] && [ ! -f "$STATE/complete" ]; then
    VERSION=$(cat "$STATE/version")
    VERSION_GIVEN=1
  fi
  if [ -z "$VERSION" ] && [ -n "$SOURCE" ]; then
    is_git_checkout "$DIR" && VERSION=$(checkout_versions "$DIR" | highest_version)
    [ -n "$VERSION" ] ||
      die "cannot tell which release $SOURCE is: its commit carries no platform-v tag. Pass --version X.Y.Z"
  fi
  if [ -n "$VERSION" ]; then
    valid_version "$VERSION" || die "$VERSION is not a version (X.Y.Z)"
    case $(images_published "$VERSION") in
      no) die "release $VERSION has no published images yet ($IMAGE_REGISTRY, $IMAGE_REPOSITORY): its release pipeline has not finished, or it refused that tag. Wait for it, or choose another release with --version. Behind a registry that needs a login, run docker login and set ORCHESTRATOR_IMAGE_CHECK=off" ;;
      unknown) warn "could not check that the images of release $VERSION are published; continuing" ;;
    esac
  else
    _skipped=""
    for _v in $(release_candidates | head -n 5); do
      case $(images_published "$_v") in
        yes) VERSION=$_v; break ;;
        unknown)
          warn "could not check that the images of release $_v are published; installing it"
          VERSION=$_v
          break ;;
        no)
          say "Release $_v has no published images yet; trying the one before it."
          _skipped="$_skipped $_v" ;;
      esac
    done
    if [ -z "$VERSION" ]; then
      [ -z "$_skipped" ] ||
        die "none of the newest releases ($_skipped ) has published images yet: their release pipeline has not finished. Run this again later, or pass --version X.Y.Z"
      die "could not find a platform release: no GitHub Release, and no final platform-v tag at $REPO_URL. The releases are listed by: git ls-remote --tags $REPO_URL 'platform-v*'. Pass one as --version X.Y.Z, or pass --prerelease to take the newest release candidate"
    fi
  fi
  RELEASE_RESOLVED=1
}

# The address this machine uses to reach the network.
lan_address() {
  ip -4 route get 1.1.1.1 2>/dev/null | sed -n 's/.* src \([0-9.]*\).*/\1/p' | head -n 1
}

# The name the network's DNS gives an address, kept only if that name
# resolves back to the same address (so a stale or loopback answer is not
# offered as something devices could use).
router_name_for() {
  _ip=$1
  _name=$(getent hosts "$_ip" 2>/dev/null | awk '{print $2; exit}')
  [ -n "$_name" ] || return 1
  valid_hostname "$_name" || return 1
  is_mdns_name "$_name" && return 1
  resolve_ipv4s "$_name" | grep -qxF "$_ip" || return 1
  printf '%s\n' "$_name"
}

# foreign_ipv4s NAME — the addresses NAME resolves to that are not this
# machine's, space-separated; empty when every one leads here.
foreign_ipv4s() {
  _fmine=$(host_ipv4s)
  for _ft in $(resolve_ipv4s "$1" || true); do
    printf '%s\n' "$_fmine" | grep -qxF "$_ft" || printf '%s ' "$_ft"
  done
}

latest_tag() { # latest_tag API_URL PREFIX — prints the version after PREFIX
  curl -fsSL "$1" 2>/dev/null | sed -n 's/.*"tag_name": *"'"$2"'\([^"]*\)".*/\1/p' | head -n 1
}

# render_env — the environment file for the chosen shape, on standard output.
render_env() {
  say "# Written by tools/install/install.sh. docs/INSTALL.md explains every line."
  if [ "$MODE" = lan ]; then
    say "DOMAIN=$ADDRESS"
  else
    say "DOMAIN=$DOMAIN"
  fi
  say "DATA_PATH=./data"
  say "PLATFORM_VERSION=$VERSION"
  say "AGENT_GRPC_PROXY_PROTOCOL=require"
  if [ "$MODE" = lan ]; then
    say "COMPOSE_PROFILES=proxy-lan"
    say "# Plain HTTP on a trusted local network: device credentials, commands and"
    say "# OS images cross it readable. Never expose these ports to the internet."
    say "PUBLIC_SCHEME=http"
    say "PUBLIC_WS_SCHEME=ws"
    say "GRPC_TLS_TERMINATED_EXTERNALLY=false"
    say "AGENT_GRPC_PLAINTEXT_OK=true"
    say "IMAGE_ALLOW_PLAINTEXT_URL=true"
  else
    say "COMPOSE_PROFILES=proxy-tls"
    say "ACME_EMAIL=$EMAIL"
  fi
}

usage() {
  say "usage: install.sh [--mode lan|public] [--address NAME_OR_IP] [--skip-address-check]"
  say "                  [--domain NAME] [--email ADDRESS] [--owner-email ADDRESS]"
  say "                  [--netboot yes|no] [--recovery-image FILE]"
  say "                  [--version X.Y.Z] [--prerelease] [--agent-version X.Y.Z]"
  say "                  [--dir DIR] [--source DIR] [--yes]"
  say "Without flags it asks. The comment at the top of the script says what each does."
}

parse_args() {
  while [ $# -gt 0 ]; do
    case "$1" in
      --mode) MODE=$2; shift 2 ;;
      --address) ADDRESS=$2; shift 2 ;;
      --skip-address-check) ADDRESS_CHECK=no; shift ;;
      --domain) DOMAIN=$2; shift 2 ;;
      --email) EMAIL=$2; shift 2 ;;
      --owner-email) OWNER_EMAIL=$2; shift 2 ;;
      --netboot) NETBOOT=$2; shift 2 ;;
      --recovery-image) RECOVERY_IMAGE=$2; shift 2 ;;
      --version) VERSION=$2; VERSION_GIVEN=1; shift 2 ;;
      --prerelease) PRERELEASE=1; shift ;;
      --agent-version) AGENT_VERSION=$2; shift 2 ;;
      --dir) DIR=$2; shift 2 ;;
      --source) SOURCE=$2; shift 2 ;;
      --yes|-y) ASSUME_YES=1; shift ;;
      --help|-h) usage; exit 0 ;;
      *) die "unknown argument: $1 (see --help)" ;;
    esac
  done
}

choose_mode() {
  if [ -z "$MODE" ]; then
    say ""
    say "How will your devices and your browser reach this server?"
    say ""
    say "  1) On my local network only."
    say "     Plain HTTP, no certificate. For a home or lab network you trust."
    say "  2) From the internet."
    say "     HTTPS with a Let's Encrypt certificate. Needs a DNS name that"
    say "     already points at this machine, and ports 80, 443 and 50051 open."
    say ""
    ask _choice "Choose 1 or 2" 1
    case "$_choice" in 1|lan) MODE=lan ;; 2|public) MODE=public ;; *) die "answer 1 or 2" ;; esac
  fi
  case "$MODE" in lan|public) ;; *) die "--mode is lan or public" ;; esac
}

choose_lan_address() {
  _ip=$(lan_address || true)
  _name=""
  [ -n "$_ip" ] && _name=$(router_name_for "$_ip" || true)
  _stale=""
  if [ -n "$_name" ]; then
    _stale=$(foreign_ipv4s "$_name")
    [ -z "$_stale" ] || { _stale_name=$_name; _name=""; }
  fi
  if [ -z "$ADDRESS" ]; then
    say ""
    say "The address you choose now is written into every device you adopt, and"
    say "into the bootloader of every device set up for network boot. If it stops"
    say "leading to this machine, those devices lose their server."
    say ""
    if [ -n "$_name" ]; then
      say "Your router gives this machine the name: $_name"
      say "A name follows the machine if its address changes, so it is the better"
      say "choice — on a network where the router hands out names. Devices look it"
      say "up through the router every time they start."
      say "This machine's address is: $_ip"
      ask ADDRESS "Name or address the devices should use" "$_name"
    elif [ -n "$_ip" ] && [ -n "$_stale" ]; then
      say "This machine's address is: $_ip"
      say "Your router calls it $_stale_name, but that name also resolves to"
      say "${_stale}which is not this machine (often a record the router kept"
      say "from an old lease). A device given that answer would not find this"
      say "server, so the address itself is what devices will use."
      ask ADDRESS "Name or address the devices should use" "$_ip"
    elif [ -n "$_ip" ]; then
      say "This machine's address is: $_ip"
      say "Your router does not give it a name that resolves back to it, so the"
      say "address itself is what devices will use."
      ask ADDRESS "Name or address the devices should use" "$_ip"
    else
      ask ADDRESS "Name or address the devices should use"
    fi
  fi
  # Every refusal comes before any advice about the address: advice to
  # reserve an address that is then refused reads as an instruction.
  if valid_ipv4 "$ADDRESS"; then
    :
  elif is_mdns_name "$ADDRESS"; then
    die "$ADDRESS is a multicast (.local) name: a computer resolves it, the bootloader of a Raspberry Pi does not. Use the name your router gives this machine, or its address"
  elif ! valid_hostname "$ADDRESS"; then
    die "$ADDRESS is neither an IPv4 address nor a DNS name with a dot in it"
  fi
  check_lan_address
  if valid_ipv4 "$ADDRESS"; then
    say ""
    say "IMPORTANT: reserve $ADDRESS for this machine in your router's DHCP"
    say "settings (a \"static lease\" or \"always assign this address\") before you"
    say "adopt a device. An address handed out by DHCP can change after a restart"
    say "of the router or of this machine, and every adopted device would then"
    say "call an address that is no longer this server."
  else
    say ""
    say "Devices will look up $ADDRESS through your router. Still reserve this"
    say "machine's address in the router's DHCP settings: some routers forget a"
    say "name when the lease changes. If you ever replace the router, the new"
    say "one must give this machine the same name."
  fi
}

choose_public_name() {
  [ -n "$DOMAIN" ] || ask DOMAIN "DNS name that points at this machine (for example orchestrator.example.com)"
  valid_hostname "$DOMAIN" || die "$DOMAIN is not a DNS name"
  is_mdns_name "$DOMAIN" && die "$DOMAIN is a local-only name; a public install needs a name the internet resolves"
  _resolved=$(getent ahostsv4 "$DOMAIN" 2>/dev/null | awk '{print $1; exit}')
  if [ -z "$_resolved" ]; then
    warn "$DOMAIN does not resolve yet. Let's Encrypt cannot issue a certificate until it points at this machine."
  else
    say "$DOMAIN resolves to $_resolved — that must be this machine's public address."
  fi
  if [ -z "$EMAIL" ] && [ "$ASSUME_YES" -eq 0 ]; then
    ask EMAIL "Email address for Let's Encrypt expiry notices (empty for none)" ""
  fi
  say ""
  say "This name is written into every device you adopt. It cannot be changed"
  say "later without touching each of them."
}

choose_netboot() {
  if [ -z "$NETBOOT" ]; then
    say ""
    say "Network boot lets a blank device start from this server and have an"
    say "operating system installed on it from the web UI, with no card. It needs"
    say "a recovery image (about 100 MB, downloaded now) and a signing key that"
    say "is generated on this machine and never leaves it."
    say "Without it you can still adopt devices that already run an OS."
    ask _choice "Set up network boot? (y/n)" y
    case "$_choice" in y|Y|yes) NETBOOT=yes ;; n|N|no) NETBOOT=no ;; *) die "answer y or n" ;; esac
  fi
  case "$NETBOOT" in yes|no) ;; *) die "--netboot is yes or no" ;; esac
  [ "$NETBOOT" = yes ] && check_recovery_image
  return 0
}

# check_recovery_image — a named image is checked now, with the other
# answers, and kept as an absolute path: the install steps run in the install
# directory, where a relative path would name another file.
check_recovery_image() {
  [ -n "$RECOVERY_IMAGE" ] || return 0
  case "$RECOVERY_IMAGE" in /*) ;; *) RECOVERY_IMAGE=$(pwd)/$RECOVERY_IMAGE ;; esac
  [ -f "$RECOVERY_IMAGE" ] || die "--recovery-image $RECOVERY_IMAGE: no such file"
  [ -r "$RECOVERY_IMAGE" ] || die "--recovery-image $RECOVERY_IMAGE: not readable by $(id -un)"
  [ -s "$RECOVERY_IMAGE" ] || die "--recovery-image $RECOVERY_IMAGE: the file is empty"
}

choose_owner() {
  if [ -z "$OWNER_EMAIL" ]; then
    say ""
    say "The installer creates the owner account of this server itself, before"
    say "the server can be reached from the network, so nobody else can claim it."
    ask OWNER_EMAIL "Email address the owner signs in with" "$EMAIL"
  fi
  valid_email "$OWNER_EMAIL" || die "$OWNER_EMAIL is not an email address"
}

need_root_tool() {
  if [ "$(id -u)" -eq 0 ]; then
    SUDO=""
  elif command -v sudo >/dev/null 2>&1; then
    SUDO=sudo
  else
    die "run this as root, or install sudo"
  fi
}

# Docker as this user when the user may talk to the daemon (a member of the
# `docker` group keeps their own registry login), through sudo otherwise.
pick_docker() {
  if docker info >/dev/null 2>&1; then DOCKER=docker; else DOCKER="$SUDO docker"; fi
}

# preflight — the checks that need nothing installed: this machine, the
# version flags, the install directory and every port the stack binds. They
# run before any package is installed and before anything is cloned, so a
# refusal leaves the machine as it was. A resumed install's ports are checked
# later, once the containers the interrupted run left are stopped.
preflight() {
  step "Checking this machine"
  [ "$(uname -s)" = Linux ] || die "the server runs on Linux; this is $(uname -s)"
  case "$(uname -m)" in x86_64|amd64|aarch64|arm64) ;; *) die "no server image is published for $(uname -m)" ;; esac
  [ -z "$VERSION" ] || valid_version "$VERSION" || die "--version $VERSION is not a version (X.Y.Z)"
  [ -z "$AGENT_VERSION" ] || valid_version "$AGENT_VERSION" || die "--agent-version $AGENT_VERSION is not a version (X.Y.Z)"
  install_dir
  [ ! -d "$DIR" ] || detect_existing_install >/dev/null
  [ "$RESUME" -eq 1 ] || check_ports
  # Which release, and whether its images exist, needs git and curl; on a
  # machine that has both it is settled here too, before anything changes.
  if command -v git >/dev/null 2>&1 && command -v curl >/dev/null 2>&1; then
    resolve_release
  fi
}

# os_id — the distribution's ID from /etc/os-release, empty when unknown.
os_id() {
  # shellcheck disable=SC1091
  [ -r /etc/os-release ] && (. /etc/os-release && printf '%s' "${ID:-}")
}

# docker_engine_present — a Docker engine or client is installed here, from
# whatever packaging.
docker_engine_present() {
  command -v docker >/dev/null 2>&1 || command -v dockerd >/dev/null 2>&1
}

# package_installed NAME — the Debian package NAME is installed.
package_installed() {
  dpkg-query -W -f='${Status}' "$1" 2>/dev/null | grep -q 'install ok installed'
}

# compose_packages — the packages that add Docker Compose here without
# touching the engine, or nothing when no Docker is installed yet. An engine
# that is already installed is never replaced: Ubuntu's docker.io conflicts
# with Docker's own docker-ce and containerd.io, and installing it over them
# removes the engine and stops every container it runs.
compose_packages() {
  if ! docker_engine_present; then
    case "$1" in
      ubuntu) printf 'docker.io docker-compose-v2' ;;
      *) die "Docker Engine with the Compose plugin is required and was not found. Install it (https://docs.docker.com/engine/install/) and run this again" ;;
    esac
    return 0
  fi
  if package_installed docker-ce || package_installed docker-ce-cli; then
    printf 'docker-compose-plugin'
  elif package_installed docker.io && [ "$1" = ubuntu ]; then
    printf 'docker-compose-v2'
  elif package_installed docker.io; then
    die "Docker is installed here from this distribution's docker.io package, which has no Compose plugin package beside it. Install the Compose plugin (https://docs.docker.com/compose/install/linux/) and run this again; the installer does not replace an existing Docker"
  else
    die "Docker is installed here, but not its Compose plugin, and not from a package this installer knows how to extend. Install the Compose plugin for this Docker (https://docs.docker.com/compose/install/linux/) and run this again; the installer does not replace an existing Docker"
  fi
}

ensure_packages() {
  step "Checking the tools the install needs"
  _id=$(os_id || true)
  _missing=""
  for _tool in git curl openssl; do command -v "$_tool" >/dev/null 2>&1 || _missing="$_missing $_tool"; done
  if [ "$NETBOOT" = yes ]; then
    command -v xxd >/dev/null 2>&1 || _missing="$_missing xxd"
    command -v mcopy >/dev/null 2>&1 || _missing="$_missing mtools"
  fi
  pick_docker
  if ! $DOCKER compose version >/dev/null 2>&1; then
    _compose=$(compose_packages "$_id") || exit 1
    _missing="$_missing $_compose"
  fi
  if [ -n "$_missing" ]; then
    case "$_id" in
      ubuntu|debian|raspbian) ;;
      *) die "missing:$_missing — install them and run this again" ;;
    esac
    say "Installing:$_missing"
    $SUDO env DEBIAN_FRONTEND=noninteractive apt-get update -qq
    # --no-remove: apt stops rather than remove a package to make room, so
    # nothing already installed here, an engine above all, is ever replaced.
    # shellcheck disable=SC2086
    $SUDO env DEBIAN_FRONTEND=noninteractive NEEDRESTART_SUSPEND=1 apt-get install -y -qq --no-remove $_missing >/dev/null ||
      die "apt-get could not install$_missing without removing a package; nothing was removed. Install them yourself and run this again"
  fi
  pick_docker
  $DOCKER compose version >/dev/null 2>&1 || die "docker compose still does not run; see the output above"
  say "Docker: $($DOCKER --version | cut -d, -f1); Compose: $($DOCKER compose version --short)"
}

# port_in_use PORT — something listens on TCP PORT here. The ports are checked
# before any package is installed, so this needs nothing beyond the kernel's
# own tables when `ss` is absent.
port_in_use() {
  if command -v ss >/dev/null 2>&1; then
    ss -ltn 2>/dev/null | awk '{print $4}' | grep -Eq "[:.]$1\$"
    return
  fi
  set -- "$1"
  for _table in /proc/net/tcp /proc/net/tcp6; do
    [ -r "$_table" ] && set -- "$@" "$_table"
  done
  [ $# -gt 1 ] || { warn "cannot list the ports in use here; not checking port $1"; return 1; }
  proc_listens "$@"
}

# proc_listens PORT TABLE… — a socket in TABLE (the format of /proc/net/tcp)
# listens on PORT: state 0A, the port in hexadecimal after the address.
proc_listens() {
  _hex=$(printf '%04X' "$1")
  shift
  awk -v p="$_hex" '$4 == "0A" { n = split($2, a, ":"); if (toupper(a[n]) == p) found = 1 } END { exit !found }' "$@"
}

# check_ports — every port the stack binds, checked before anything starts:
# a port that is taken would otherwise surface as a container that never
# starts and a readiness wait that ends minutes later.
check_ports() {
  _ports="80 50051"
  [ "$MODE" = public ] && _ports="80 443 50051"
  for _port in $_ports; do
    if port_in_use "$_port"; then
      die "port $_port is already in use on this machine; the server's proxy needs it"
    fi
  done
  # The loopback ports the compose file publishes for the proxy: the backend
  # (8080), the web UI (8088) and the agent channel (50052).
  for _port in 8080 8088 50052; do
    if port_in_use "$_port"; then
      die "port $_port is already in use on this machine; the server publishes a service on 127.0.0.1:$_port. Stop what holds it (sudo ss -ltnp 'sport = :$_port' names it) and run this again"
    fi
  done
}

# missing_secret_files — the credential files the release's compose file
# names that do not exist yet in secrets/.
missing_secret_files() {
  # The names are single words; the `${SECRETS_DIR…}` in the pattern is text.
  # shellcheck disable=SC2013,SC2016
  for _f in $(sed -n 's|^ *file: \${SECRETS_DIR:-\./secrets}/||p' "$DIR/$COMPOSE_FILE"); do
    $SUDO test -e "$DIR/secrets/$_f" || printf '%s ' "$_f"
  done
}

# env_value KEY FILE — the value KEY has in the environment file FILE (the
# last assignment wins, as in Compose), surrounding quotes removed.
env_value() {
  sed -n 's/^[[:space:]]*'"$1"'=//p' "$2" 2>/dev/null | tail -n 1 |
    sed -e 's/[[:space:]]*$//' -e 's/^"\(.*\)"$/\1/' -e "s/^'\(.*\)'\$/\1/"
}

# write_env — the environment file for the chosen shape, readable by its
# owner only from the first byte. An existing one that differs is kept beside
# it, so no setting is ever lost without a copy.
write_env() {
  _new=$(mktemp "$DIR/.env.new.XXXXXX")
  render_env >"$_new"
  if [ -f "$DIR/.env" ] && ! cmp -s "$_new" "$DIR/.env"; then
    _bak="$DIR/.env.bak.$(date -u +%Y%m%dT%H%M%SZ)"
    cp -p "$DIR/.env" "$_bak"
    say "The previous $DIR/.env is kept as $_bak"
  fi
  chmod 0600 "$_new"
  mv -f "$_new" "$DIR/.env"
}

# share_read PATH… — PATH and everything under it readable by every uid, its
# directories searchable. The containers that read these files run as another
# uid with every capability dropped, so a mode the caller's umask narrowed
# would hide them. Only files that hold no secret are passed here.
share_read() { $SUDO chmod -R a+rX "$@"; }

# database_files — non-empty when data/postgres holds a database.
database_files() {
  [ -d "$DIR/data/postgres" ] || return 0
  $SUDO ls -A "$DIR/data/postgres" 2>/dev/null || true
}

# check_checkout DIR — the checkout must be the release being installed: the
# compose file, the proxy routes and the recovery-image pin of one release
# must not run the images of another.
check_checkout() {
  _d=$1
  if is_git_checkout "$_d"; then
    _tags=$(checkout_versions "$_d" | tr '\n' ' ')
    case " $_tags " in *" $VERSION "*) return 0 ;; esac
    if [ -z "$_tags" ] && [ -n "$SOURCE" ] && [ "$VERSION_GIVEN" -eq 1 ]; then
      warn "$_d is a commit with no platform-v tag; nothing checks that it is release $VERSION"
      return 0
    fi
    die "$_d is ${_tags:+release }${_tags:-a commit with no platform-v tag }and not platform-v$VERSION: one release's compose file must not run another release's images. Check out the release (git -C $_d fetch --tags && git -C $_d checkout platform-v$VERSION) or choose another --dir"
  fi
  if [ -n "$SOURCE" ] && [ "$VERSION_GIVEN" -eq 1 ]; then
    warn "$_d is not a git checkout; nothing checks that it is release $VERSION"
    return 0
  fi
  die "$_d is not a git checkout, so the installer cannot tell which release it holds. Remove it or choose another --dir; to install from an unpacked release, pass --source $_d --version X.Y.Z"
}

# detect_existing_install — sorts the install directory into: new, an install
# this script began and did not finish (resumed), or anything else (refused).
# A finished install is never touched; credentials a database was created
# with are never replaced.
detect_existing_install() {
  _secrets=""
  if [ -d "$DIR/secrets" ]; then
    _secrets=$(ls -A "$DIR/secrets" 2>/dev/null || $SUDO ls -A "$DIR/secrets" 2>/dev/null || true)
  fi
  if [ -f "$STATE/complete" ]; then
    die "$DIR is a finished install; this installer does not change one. Upgrading is docs/INSTALL.md, \"Upgrading\""
  fi
  # An install made by hand may keep its credentials and its data outside
  # this directory (SECRETS_DIR, DATA_PATH), so an empty secrets/ proves
  # nothing: its .env is what marks it, and is never overwritten.
  if [ -f "$DIR/.env" ] && [ ! -f "$STATE/version" ]; then
    _paths=""
    for _key in SECRETS_DIR DATA_PATH; do
      _val=$(env_value "$_key" "$DIR/.env")
      [ -z "$_val" ] || _paths="$_paths $_key=$_val"
    done
    die "$DIR/.env exists and this installer did not write it: $DIR is an existing install${_paths:+ (its .env names$_paths)}. Upgrading is docs/INSTALL.md, \"Upgrading\". To install afresh here instead, move that .env aside first"
  fi
  if [ -n "$_secrets" ]; then
    [ -f "$STATE/version" ] ||
      die "$DIR/secrets already holds credential files, and this installer did not start that install: it is an existing install. Upgrading is docs/INSTALL.md, \"Upgrading\""
    RESUME=1
    say "$DIR holds an install this installer started and did not finish; resuming it."
    return 0
  fi
  if [ -n "$(database_files)" ]; then
    die "$DIR/data/postgres holds a database, but $DIR/secrets is empty: the database was created with credentials that are gone, and new ones cannot open it. Put the old secrets/ back and run this again — or, if that database holds nothing you need, remove it (sudo rm -rf $DIR/data/postgres) and run this again"
  fi
}

# compose_project_name DIR — the project name Compose gives the compose file
# in DIR: the directory's name, lower case, with every character other than a
# letter, a digit, `-` and `_` removed.
compose_project_name() {
  basename "$1" | tr '[:upper:]' '[:lower:]' | tr -cd 'a-z0-9_-'
}

# check_volumes — Compose names its volumes after the install directory's
# NAME and keeps them when the containers go, so an earlier install in
# another directory of the same name leaves volumes this one would reuse:
# its database opens with none of the credentials this run writes. A volume
# bound to anywhere but this directory is refused, with its name.
check_volumes() {
  _project=$(compose_project_name "$DIR")
  for _v in postgres-data:data/postgres backend-storage:data/backend/storage; do
    _name="${_project}_orchestrator-${_v%%:*}"
    _want="$DIR/${_v#*:}"
    _dev=$($DOCKER volume inspect --format '{{index .Options "device"}}' "$_name" 2>/dev/null) || continue
    case "$_dev" in
      "$_want"|"$_want/"|./*|"") ;;
      *) die "the Docker volume $_name already exists and is bound to $_dev, not to $_want: it belongs to an earlier install in another directory, and this install would open that one's data with new credentials. If you need that install, run the installer from its directory instead. Otherwise remove the volume (docker volume rm $_name, after docker compose down in that directory if it is running) and the data at $_dev, and run this again" ;;
    esac
  done
}

# install_dir — the install directory, from --source or --dir alone, as an
# absolute path.
install_dir() {
  if [ -n "$SOURCE" ]; then
    [ -f "$SOURCE/$COMPOSE_FILE" ] || die "$SOURCE is not a checkout of the release (no $COMPOSE_FILE)"
    DIR=$(cd "$SOURCE" && pwd -P)
  else
    [ -n "$DIR" ] || DIR=$(pwd)/orchestrator
    case "$DIR" in /*) ;; *) DIR=$(pwd)/$DIR ;; esac
    [ ! -d "$DIR" ] || DIR=$(cd "$DIR" && pwd -P)
  fi
  STATE="$DIR/data/.installer"
}

fetch_release() {
  step "Getting the release"
  install_dir
  resolve_release
  if [ -e "$DIR" ]; then
    [ -f "$DIR/$COMPOSE_FILE" ] || die "$DIR exists and is not a checkout of the release"
    check_checkout "$DIR"
  else
    git -c advice.detachedHead=false clone --quiet --branch "platform-v$VERSION" "$REPO_URL" "$DIR"
    DIR=$(cd "$DIR" && pwd -P)
    STATE="$DIR/data/.installer"
  fi
  [ -f "$DIR/proxy/lan.yml" ] || die "release $VERSION has no bundled proxy; this installer needs a newer release"
  # The proxy runs as root with no capability to bypass file modes, so its
  # routes must be readable by everyone however the checkout was made.
  share_read "$DIR/proxy"
  detect_existing_install
  say "Platform $VERSION in $DIR"
}

compose() { (cd "$DIR" && $DOCKER compose -f "$COMPOSE_FILE" "$@"); }

# compose_up [SERVICE…] — starts services and stops the run if Compose
# cannot, with Compose's own words.
compose_up() {
  _log=$(mktemp)
  if compose up -d --quiet-pull "$@" >"$_log" 2>&1; then
    rm -f "$_log"
    return 0
  fi
  cat "$_log" >&2
  rm -f "$_log"
  die "docker compose could not start the server; its output is above"
}

# stop_unfinished — a resumed install first takes down what the interrupted
# run left running, both proxies included (the shape may have changed), so
# the port checks see this machine as it will be without it.
stop_unfinished() {
  step "Stopping what the unfinished install left running"
  cd "$DIR"
  write_env
  compose --profile proxy-lan --profile proxy-tls down --remove-orphans >/dev/null 2>&1 || true
}

create_install() {
  step "Creating directories and credentials"
  cd "$DIR"
  mkdir -p data/postgres data/backend/storage/tmp data/net_install/eeprom \
    data/net_install/sd-payload data/eeprom-keys data/eeprom-build \
    data/source-offer data/proxy secrets "$STATE"
  # Recorded before any credential exists: a directory with credentials and
  # this record is an install this script may resume.
  printf '%s\n' "$VERSION" >"$STATE/version"
  $SUDO chown -R 1000:1000 data/backend data/net_install data/eeprom-keys secrets
  $SUDO chown root:root data/proxy && $SUDO chmod 0700 data/proxy
  IMAGE="ghcr.io/uptime-lab/orchestrator/backend:$VERSION"
  $DOCKER pull --quiet "$IMAGE" >/dev/null ||
    die "could not pull $IMAGE. If the registry asks for a login (a private mirror, or a release that is not public yet), run \`$DOCKER login ghcr.io\` and run this again"
  _missing=""
  [ "$RESUME" -eq 0 ] || _missing=$(missing_secret_files)
  if [ "$RESUME" -eq 1 ] && [ -z "$_missing" ]; then
    say "Keeping the credential files the earlier run wrote to $DIR/secrets: a database is created with them once, and opens with no others."
  else
    if [ -n "$_missing" ]; then
      # Only a set whose writing was cut short, which no database uses yet,
      # is written again; otherwise the database would be locked out.
      [ -z "$(database_files)" ] ||
        die "$DIR/secrets lacks $_missing while data/postgres already holds a database. Restore the missing files from your backup (an optional one may be an empty file: docs/INSTALL.md, \"Create the credentials\")"
      say "The credential set was cut short and no database uses it yet; writing it again."
      $SUDO rm -rf "$DIR/secrets"
      mkdir -p secrets
      $SUDO chown 1000:1000 secrets
    fi
    $DOCKER run --rm --user 1000:1000 -v "$DIR/secrets:/secrets" "$IMAGE" \
      /app/server --init-secrets /secrets >/dev/null
    say "Credential files written to $DIR/secrets (owner uid 1000, mode 0400)."
  fi

  write_env
  say "Environment written to $DIR/.env"
}

# start_stack — everything except the proxy. Until the owner account exists
# the server answers on this machine's loopback interface only.
start_stack() {
  step "Starting the server"
  compose pull --quiet
  compose_up postgres backend frontend eeprom-signer
  _i=0
  until curl -fsS -o /dev/null http://127.0.0.1:8080/ready 2>/dev/null; do
    _i=$((_i + 1))
    if [ "$_i" -gt 60 ]; then
      compose logs --tail 30 backend >&2 || true
      die "the backend did not become ready; its last log lines are above"
    fi
    sleep 3
  done
  say "The backend is ready."
}

# registration_state — how the server admits a sign-up right now
# (first_owner, invite_code or closed), asked on the loopback port.
registration_state() {
  curl -fsS http://127.0.0.1:8080/api/v1/server/sign-in-options 2>/dev/null |
    sed -n 's/.*"registration": *"\([a-z_]*\)".*/\1/p' | head -n 1
}

# create_owner — the first sign-up owns the server, so it is made here,
# through the loopback port, while nothing outside this machine can reach the
# server. The password is generated and kept in a file only this user can
# read; it travels to curl on standard input, never on a command line.
create_owner() {
  step "Creating the owner account"
  _state=""
  _i=0
  while [ -z "$_state" ] && [ "$_i" -lt 10 ]; do
    _state=$(registration_state || true)
    [ -n "$_state" ] || sleep 2
    _i=$((_i + 1))
  done
  case "$_state" in
    first_owner) ;;
    closed|invite_code)
      say "The server already has its owner account."
      return 0 ;;
    *) die "could not read the server's sign-up state on http://127.0.0.1:8080; see: docker logs orchestrator-backend" ;;
  esac
  OWNER_PASSWORD_FILE="$DIR/owner-password"
  if [ ! -s "$OWNER_PASSWORD_FILE" ]; then
    (umask 077 && openssl rand -base64 18 >"$OWNER_PASSWORD_FILE")
  fi
  _pw=$(cat "$OWNER_PASSWORD_FILE")
  _code=$(printf '{"email":"%s","password":"%s"}' "$OWNER_EMAIL" "$_pw" |
    curl -sS -o /dev/null -w '%{http_code}' -H 'Content-Type: application/json' \
      --data-binary @- http://127.0.0.1:8080/auth/signup 2>/dev/null) || _code=000
  [ "$_code" = 201 ] ||
    die "the server did not create the owner account (HTTP $_code); see: docker logs orchestrator-backend"
  say "Owner account created: $OWNER_EMAIL"
}

# open_to_network — the proxy, started only now that the server has its owner.
open_to_network() {
  step "Opening the server to the network"
  compose_up
  if [ "$MODE" = lan ]; then
    _i=0
    until curl -fsS -o /dev/null http://127.0.0.1/api/health 2>/dev/null; do
      _i=$((_i + 1))
      [ "$_i" -le 10 ] ||
        die "the backend is up but the proxy on port 80 does not answer; see: docker logs orchestrator-proxy"
      sleep 2
    done
    say "The proxy answers on port 80."
    if curl -fsS -o /dev/null --max-time 5 "http://$ADDRESS/api/health" 2>/dev/null; then
      say "http://$ADDRESS answers."
    else
      warn "http://$ADDRESS does not answer from this machine. Devices and browsers will use that address; check that it leads here and that no firewall closes port 80"
    fi
  elif https_answers || retry_certificate; then
    rm -f "$STATE/certificate-pending"
    say "https://$DOMAIN answers with a valid certificate."
  else
    # Everything else is in place, so the install is finished; this one step
    # is left, and a later run of the installer does only that.
    CERT_PENDING=1
    : >"$STATE/certificate-pending"
    certificate_help
  fi
}

# https_answers — https://DOMAIN/api/health answers with a valid certificate
# within about two minutes.
https_answers() {
  _i=0
  # --resolve: a home router often does not route the machine's own public
  # address back to it, and the certificate is checked against the name
  # either way.
  until curl -fsS -o /dev/null --max-time 10 --resolve "$DOMAIN:443:127.0.0.1" "https://$DOMAIN/api/health" 2>/dev/null; do
    _i=$((_i + 1))
    [ "$_i" -lt 40 ] || return 1
    sleep 3
  done
}

# retry_certificate — the proxy asks Let's Encrypt for a certificate when it
# starts, and does not ask again for one it failed to obtain; a restart makes
# it ask again, so a DNS record or a firewall rule fixed meanwhile counts.
retry_certificate() {
  say "No valid certificate yet; restarting the proxy so it asks Let's Encrypt again."
  $DOCKER restart orchestrator-proxy >/dev/null 2>&1 || return 1
  https_answers
}

certificate_help() {
  warn "https://$DOMAIN has no valid certificate yet, so browsers refuse it and devices cannot connect. Let's Encrypt checks over port 80 that $DOMAIN leads here. Check that $DOMAIN resolves, from outside your network, to this machine's public address, and that port 80 is open to the internet (a cloud firewall or security group, the router's port forwarding). The proxy's log says what Let's Encrypt answered: $DOCKER logs orchestrator-proxy 2>&1 | grep -i acme. Then make the proxy ask again: $DOCKER restart orchestrator-proxy — or run this installer again, which only checks the certificate"
}

# recheck_certificate — a finished public install whose certificate was not
# issued: nothing is asked or changed except that the proxy asks again.
recheck_certificate() {
  step "Checking the certificate of the install in $DIR"
  DOMAIN=$(env_value DOMAIN "$DIR/.env")
  [ -n "$DOMAIN" ] || die "$DIR/.env names no DOMAIN"
  need_root_tool
  pick_docker
  if https_answers || retry_certificate; then
    rm -f "$STATE/certificate-pending"
    say "https://$DOMAIN answers with a valid certificate. The install is finished."
    return 0
  fi
  certificate_help
  exit 1
}

import_agent() {
  step "Importing an agent release"
  if [ -s "$STATE/agent" ]; then
    say "Agent $(cat "$STATE/agent") was imported by the earlier run."
    return 0
  fi
  [ -n "$AGENT_VERSION" ] || AGENT_VERSION=$(latest_tag "$AGENT_RELEASES_API" agent-v)
  [ -n "$AGENT_VERSION" ] || die "could not find the latest agent release; pass --agent-version"
  valid_version "$AGENT_VERSION" || die "$AGENT_VERSION is not a version (X.Y.Z)"
  _rel="$DIR/agent-release-$AGENT_VERSION"
  mkdir -p "$_rel"
  for _pkg in orchestrator-agent orchestrator-watcher; do
    for _suffix in deb deb.sig; do
      _file="${_pkg}_${AGENT_VERSION}_linux_arm64.$_suffix"
      curl -fsSL -o "$_rel/$_file" "$AGENT_DOWNLOAD/agent-v$AGENT_VERSION/$_file" ||
        die "could not download $_file of agent $AGENT_VERSION"
    done
  done
  # The import runs as uid 1000; signed packages hold no secret.
  share_read "$_rel"
  # The import verifies each package against the keys built into the server
  # and writes nothing unless every one passes. A resumed install may hold
  # this release half written by the interrupted run; it is written again.
  _replace=""
  [ "$RESUME" -eq 0 ] || _replace=--replace
  if ! run_logged "The import of agent $AGENT_VERSION" compose run --rm --no-deps -v "$_rel:/release:ro" backend \
    /app/server --import-agent-release /release --set-latest $_replace; then
    die "the server refused agent release $AGENT_VERSION; the reason is above (docs/INSTALL.md, \"Import an agent release\")"
  fi
  printf '%s\n' "$AGENT_VERSION" >"$STATE/agent"
  say "Agent $AGENT_VERSION imported; devices will install it."
}

# run_logged WHAT COMMAND… — runs COMMAND with its output kept aside: none of
# it is shown when it succeeds (the helpers write for someone running them by
# hand, and the installer says what each step did itself), all of it when it
# fails. A pipe into head or tail would hide the failure instead, since POSIX
# sh has no pipefail.
run_logged() {
  _what=$1
  shift
  _log=$(mktemp)
  if "$@" >"$_log" 2>&1; then
    rm -f "$_log"
    return 0
  else
    _rc=$?
  fi
  warn "$_what failed (exit status $_rc). Its output:"
  cat "$_log" >&2
  rm -f "$_log"
  return 1
}

# netboot_failed STEP — network boot is not ready. The install is left
# unfinished, so running the installer again retries network boot.
netboot_failed() {
  NETBOOT=failed
  NETBOOT_FAILED=$1
}

setup_netboot() {
  step "Setting up network boot"
  cd "$DIR"
  if ! $SUDO test -f data/eeprom-keys/privkey.pem; then
    # Created unreadable to others, not narrowed after the fact.
    (umask 077 && $SUDO openssl genrsa -out data/eeprom-keys/privkey.pem 2048 2>/dev/null)
    $SUDO openssl rsa -in data/eeprom-keys/privkey.pem -pubout -out data/eeprom-keys/pubkey.pem 2>/dev/null
    $SUDO chown -R 1000:1000 data/eeprom-keys
    $SUDO chmod 0400 data/eeprom-keys/privkey.pem
    say "Signing key generated: data/eeprom-keys/privkey.pem"
  fi
  if ! run_logged "Fetching the bootloader firmware" tools/netboot/update-eeprom-firmware.sh data/eeprom-build; then
    netboot_failed "fetching the bootloader firmware"
    return 0
  fi
  say "Bootloader firmware fetched (data/eeprom-build)."
  # The signer reads the firmware as uid 1000; it is public.
  share_read data/eeprom-build

  _img=""
  if [ -n "$RECOVERY_IMAGE" ]; then
    _img=$RECOVERY_IMAGE
    say "Using your recovery image (sha256 $(sha256sum "$_img" | cut -d' ' -f1))."
  else
    RECOVERY_IMAGE_URL="" RECOVERY_IMAGE_SHA256=""
    # shellcheck disable=SC1091
    [ -r tools/netboot/recovery-image.env ] && . tools/netboot/recovery-image.env
    if [ -z "$RECOVERY_IMAGE_URL" ] || [ -z "$RECOVERY_IMAGE_SHA256" ]; then
      warn "this release names no published recovery image."
    else
      _img="$DIR/data/recovery-image.download"
      say "Downloading the recovery image…"
      if ! curl -fsSL -o "$_img" "$RECOVERY_IMAGE_URL"; then
        rm -f "$_img"
        warn "could not download $RECOVERY_IMAGE_URL"
        netboot_failed "downloading the recovery image"
        return 0
      elif [ "$(sha256sum "$_img" | cut -d' ' -f1)" != "$RECOVERY_IMAGE_SHA256" ]; then
        rm -f "$_img"
        warn "the downloaded recovery image is not the one this release names (sha256 mismatch); nothing was installed from it"
        netboot_failed "checking the downloaded recovery image"
        return 0
      fi
    fi
  fi
  if [ -z "$_img" ]; then
    NETBOOT=partial
    say "Network boot is prepared except for the recovery image. Build one from"
    say "https://github.com/uptime-lab/orchestrator-boot-image, then run, in $DIR:"
    say "  sudo tools/netboot/sign-boot-image.sh /path/to/boot.img data/net_install"
    say "  sudo tools/netboot/extract-boot-payload.sh data/net_install/boot.img data/net_install/sd-payload"
    return 0
  fi
  if ! run_logged "Signing the recovery image" $SUDO tools/netboot/sign-boot-image.sh "$_img" data/net_install; then
    netboot_failed "signing the recovery image"
    return 0
  fi
  rm -f "$DIR/data/recovery-image.download"
  say "Recovery image signed and installed: data/net_install/boot.img"
  if ! run_logged "Extracting the SD payload" $SUDO tools/netboot/extract-boot-payload.sh data/net_install/boot.img data/net_install/sd-payload; then
    netboot_failed "extracting the SD payload"
    return 0
  fi
  say "Provisioning-card payload extracted: data/net_install/sd-payload"
  # Ready means what a device fetches is in place, not that the steps ran.
  if ! $SUDO test -s data/net_install/boot.img || ! $SUDO test -s data/net_install/boot.sig ||
    [ -z "$($SUDO ls -A data/net_install/sd-payload 2>/dev/null || true)" ]; then
    netboot_failed "checking data/net_install (boot.img, boot.sig and sd-payload must not be empty)"
  fi
}

finish() {
  if [ "$MODE" = lan ]; then _url="http://$ADDRESS"; else _url="https://$DOMAIN"; fi
  say ""
  say "------------------------------------------------------------------"
  say " Uptime Orchestrator $VERSION is running."
  say ""
  say " Open:  $_url"
  say ""
  if [ -s "$DIR/owner-password" ]; then
    say " Sign in as $OWNER_EMAIL with the password in"
    say "   $DIR/owner-password"
    say " then change it in the web UI and delete that file."
  else
    say " Sign in with the owner account."
  fi
  say " Sign-up is closed: colleagues join by invitation."
  say ""
  say " Then add a device: Nodes -> Add node. A Raspberry Pi that already runs"
  say " a 64-bit Debian-based OS (Raspberry Pi OS, Ubuntu) joins with the one"
  say " command shown there; the command needs the board's hardware serial."
  case "$NETBOOT" in
    yes) say " A blank device joins from a provisioning card (Nodes -> Add node ->"
         say " Master image -> Download SD Image) and gets its operating system from"
         say " the web UI." ;;
    partial) say " Network boot still needs a recovery image; see the note above." ;;
    failed) say ""
            say " NETWORK BOOT IS NOT READY: $NETBOOT_FAILED failed; the reason"
            say " is above. Run this installer again with the same answers to retry"
            say " it; everything else is kept." ;;
  esac
  if [ "$CERT_PENDING" -eq 1 ]; then
    say ""
    say " ONE STEP REMAINS: https://$DOMAIN has no certificate yet (see the"
    say " warning above). Once $DOMAIN leads here and port 80 is open, run"
    say "   $DOCKER restart orchestrator-proxy"
    say " or this installer again, which then only checks the certificate."
  fi
  say ""
  say " Keep safe, off this machine:"
  say "   $DIR/secrets/secrets_encryption_key   (a database backup is unreadable without it)"
  [ "$NETBOOT" = no ] || say "   $DIR/data/eeprom-keys/privkey.pem      (the only key your devices' bootloaders accept)"
  if [ "$MODE" = lan ]; then
    say ""
    say " This server speaks plain HTTP. Do not forward its ports to the internet."
  fi
  say " Files: $DIR   Manual: $DIR/docs/INSTALL.md"
  say "------------------------------------------------------------------"
}

main() {
  parse_args "$@"
  # What this run writes for the containers — the proxy routes, the
  # bootloader firmware, an agent release — is read there by another uid
  # with no capability to bypass file modes, so it must not inherit a
  # narrower umask. Credentials are created with their own modes.
  umask 022
  say "Uptime Orchestrator — server installer"
  # A finished install with only its certificate outstanding: nothing is
  # asked; the certificate is checked again.
  if [ -z "$SOURCE" ] || [ -f "$SOURCE/$COMPOSE_FILE" ]; then
    install_dir
    if [ -f "$STATE/complete" ] && [ -f "$STATE/certificate-pending" ]; then
      recheck_certificate
      return 0
    fi
    # A finished install is refused before anything is asked.
    if [ -f "$STATE/complete" ]; then
      die "$DIR is a finished install; this installer does not change one. Upgrading is docs/INSTALL.md, \"Upgrading\""
    fi
  fi
  choose_mode
  if [ "$MODE" = lan ]; then choose_lan_address; else choose_public_name; fi
  choose_netboot
  choose_owner
  need_root_tool
  preflight
  if [ "$ASSUME_YES" -eq 0 ]; then
    say ""
    ask _go "Install now? (y/n)" y
    case "$_go" in y|Y|yes) ;; *) die "nothing was changed" ;; esac
  fi
  ensure_packages
  fetch_release
  check_volumes
  [ "$RESUME" -eq 0 ] || stop_unfinished
  # Again, for a resumed install and for anything that took a port meanwhile.
  check_ports
  create_install
  start_stack
  create_owner
  open_to_network
  import_agent
  [ "$NETBOOT" = yes ] && setup_netboot
  # Recorded last: only a finished install is refused by a later run.
  [ "$NETBOOT" = failed ] || : >"$STATE/complete"
  finish
  [ "$NETBOOT" != failed ] || exit 1
  [ "$CERT_PENDING" -eq 0 ] || exit 1
}

# The test suite sources this file for its functions.
[ -n "${ORCHESTRATOR_INSTALL_LIB:-}" ] || main "$@"
